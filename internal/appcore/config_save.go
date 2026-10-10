// config_save.go 面板在线保存配置：校验 → 落盘（深合并 + 原子替换）→ 热应用，
// 并返回「需要重启才生效」的字段清单。
//
// 热生效范围（设计取舍）：
//   - api_key / cooldown.soft_rate / features.sanitize_blacklist_fingerprints → livecfg 快照
//   - pool.* → SetBreaker/SetMaxInFlight/SetSoftRateMax/SetWeights/SetCostExploreInterval/SetPreferExpiring
//   - schedule.* → Scheduler.Reconfigure / SetBalanceInterval / SetExpiringSoonWindow
//
// 需重启（涉及监听地址、HTTP client 超时、auth_dir 等装配期依赖）：
// listen / auth_dir / state_file / upstream.* / upstash.* / session_sticky.ttl 等。
package appcore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/scheduler"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// saveConfig 面板保存配置：校验 → 落盘 → 热应用 → 返回需重启的字段列表。
//
// 关键点：落盘用「先写 tmp 再 rename」原子替换，且优先保留磁盘上的原始 JSON 结构
// （只改面板表单覆盖到的键）。直接把校验后的 Config 整体序列化会丢掉用户手写的
// 未知键，所以先把原始内容反序列化成 map 再深合并。
func saveConfig(raw []byte, path string, running *Config, live *livecfg.Holder, p *pool.Pool, up *upstream.Client, sch *scheduler.Scheduler) ([]string, error) {
	// 1) 读磁盘内容，与面板提交的键深合并 → 解析成「本次保存后真正会生效的配置」，
	//    并原子落盘。合并与写盘抽在 mergeAndWriteConfig 里，供「只改一个键」的路径
	//    （见 RotateAPIKey）复用：直接 Marshal 整个 Config 会丢掉用户手写的未知键。
	newCfg, err := mergeAndWriteConfig(path, raw)
	if err != nil {
		return nil, err
	}

	// 4) 热应用：能立即生效的字段全部应用，并列出仍需重启的字段。
	// client_keys 也是热生效字段：面板里新增/停用/删除密钥后，下一个请求即按新集合鉴权。
	live.Store(livecfg.Snapshot{
		Creds:                credentialsOf(newCfg),
		APIKey:               newCfg.APIKey,
		SoftCooldown:         newCfg.SoftRateDur,
		SanitizeFingerprints: newCfg.Features.SanitizeBlacklistFingerprints,
	})
	up.SanitizeFingerprints.Store(newCfg.Features.SanitizeBlacklistFingerprints)
	p.SetBreaker(newCfg.Pool.BreakerThreshold, newCfg.BreakerCooldownDur, newCfg.BreakerCooldownMaxD)
	p.SetMaxInFlight(newCfg.Pool.MaxInFlight)
	p.SetMaxInFlightGlobal(newCfg.Pool.MaxInFlightGlobal)
	p.SetDegrade(newCfg.Pool.DegradeThreshold, newCfg.DegradeCooldownDur, newCfg.DegradeCooldownMaxD)
	p.SetSoftRateMax(newCfg.SoftRateMaxDur)
	p.SetCostExploreInterval(newCfg.CostExploreIntervalDur)
	p.SetWeights(newCfg.Pool.IdleWeightPerHour, newCfg.Pool.IdleWeightMax)
	p.SetPreferExpiring(newCfg.Pool.PreferExpiring)
	sch.SetExpiringSoonWindow(newCfg.ExpiringSoonDur)
	sch.Reconfigure(
		newCfg.Schedule.CheckinHours, newCfg.Schedule.TravelHours,
		newCfg.Schedule.ActivityHours, newCfg.Schedule.KeepaliveHours, newCfg.Schedule.BlackcatHours,
		newCfg.Schedule.GrowthHours,
		!newCfg.Schedule.CheckinEnabled, !newCfg.Schedule.TravelEnabled,
		!newCfg.Schedule.ActivityEnabled, !newCfg.Schedule.KeepaliveEnabled, !newCfg.Schedule.BlackcatEnabled,
		!newCfg.Schedule.GrowthEnabled)
	sch.SetBalanceInterval(newCfg.BalanceRefreshInterval)

	// 5) 决定要不要让调用方去重启：只有装配期字段**真的变了**才算。
	//    含热生效字段的对象子键（如 pool.*）也在这一层被排除——面板改熔断阈值
	//    会连带把 pool 对象一起提交，但那是立即生效的，不该要求重启。
	if !assemblyFieldsDiffer(running, newCfg) {
		return []string{}, nil
	}
	return restartRequiredFields(newCfg), nil
}

// mergeAndWriteConfig 读磁盘配置、与提交的键深合并、原子落盘，返回合并后真正生效的配置。
//
// 抽出来是为了让「只改一个键」的路径（RotateAPIKey）复用同一套合并 + 原子写盘逻辑：
// 密钥轮换只需要 submit 一个 {"api_key": ...}，落到磁盘上其余键必须原样保留。
// 直接 Marshal 整个 Config 会丢掉用户手写的未知键，也会把推导字段写成明文垃圾。
//
// 注意进程内并发：本函数没有加锁，与 saveConfig 共用同一份「读-改-写」假设
// （面板保存与密钥轮换都是低频人工操作，且 saveConfig 原本就没有锁）。
func mergeAndWriteConfig(path string, raw []byte) (*Config, error) {
	oldRaw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read current config: %w", err)
	}
	newCfg, merged, err := resolveSavedConfig(oldRaw, raw)
	if err != nil {
		return nil, err
	}

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Docker 单文件 bind mount 无法被 rename 覆盖（Linux 返回 EBUSY）。
		// 常规文件仍走原子路径，这一种部署形态改为原地覆盖。
		if !errors.Is(err, syscall.EBUSY) {
			return nil, fmt.Errorf("replace config: %w", err)
		}
		f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
		if openErr != nil {
			_ = os.Remove(tmp)
			return nil, fmt.Errorf("replace config (bind mount fallback): %w", openErr)
		}
		_, writeErr := f.Write(out)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		// 写失败时保留 tmp（挂载文件已被 O_TRUNC 破坏，tmp 里是完整新内容，可手工恢复）。
		if writeErr != nil {
			return nil, fmt.Errorf("replace config (bind mount fallback, 完整新内容保留在 %s): %w", tmp, writeErr)
		}
		_ = os.Remove(tmp)
		if closeErr != nil {
			return nil, fmt.Errorf("replace config (bind mount fallback): %w", closeErr)
		}
	}
	return newCfg, nil
}

// resolveSavedConfig 把「磁盘上的配置 + 面板本次提交的键」解析成
// 「本次保存后会生效的配置」以及落盘用的合并 map。
//
// 抽成独立函数是为了让**测试与生产走同一条路径**。此前测试自己复写了一遍
// 「merge + ParseConfig」，没有跟上这里的桌面路径解析（下一步），于是测的是一套
// 已经过期的逻辑——测试与实现各写一份，漂移只是时间问题。
func resolveSavedConfig(oldRaw, submitted []byte) (*Config, map[string]any, error) {
	// 反序列化成 map 再深合并：保留磁盘上用户手写的未知键，只覆盖面板表单管理的键。
	var cur, incoming map[string]any
	if err := json.Unmarshal(oldRaw, &cur); err != nil {
		cur = map[string]any{}
	}
	if err := json.Unmarshal(submitted, &incoming); err != nil {
		return nil, nil, fmt.Errorf("parse submitted config: %w", err)
	}
	merged := mergeConfigMaps(cur, incoming)

	// 校验（与启动同一套 Default+normalize），失败直接返回、不落盘。
	cfg, err := ParseConfig(mergedJSON(merged))
	if err != nil {
		return nil, nil, err
	}

	// 用**与 Load 相同的规则**解释新配置：桌面模式下把路径解析为绝对路径。
	//
	// 不做这一步会误判：运行中配置的 auth_dir 已是数据目录下的绝对路径，而磁盘上
	// 若没写这个键，解析出来是默认的 "./auths"——一边绝对一边相对，必然不等 →
	// 桌面版每次保存都会错误地报告「需要重启」。
	//
	// 是否「显式」要按**合并后**的配置判定，而不是合并前的磁盘内容：用户完全可能
	// 就在这次提交里新增 auth_dir。用磁盘内容判定的话，那个键会被派生逻辑直接
	// 覆盖掉——用户明确指定的路径被静默丢弃，且因为两边相等而连「需要重启」都不报。
	// 判定结果与落盘内容一致，下次 Load 按文件判定也会得到同一答案。
	if cfg.Desktop {
		_, hasAuth := merged["auth_dir"]
		_, hasState := merged["state_file"]
		if _, err := resolveDesktopPaths(cfg, hasAuth || hasState); err != nil {
			return nil, nil, err
		}
	}
	return cfg, merged, nil
}

// restartRequiredFields 返回无法热生效、需要重启进程的字段名（面板据此提示用户）。
func restartRequiredFields(c *Config) []string {
	var out []string
	if c.Listen != "" {
		out = append(out, "listen")
	}
	if c.AuthDir != "" {
		out = append(out, "auth_dir")
	}
	if c.StateFile != "" {
		out = append(out, "state_file")
	}
	out = append(out, "upstream.timeout_seconds", "upstream.header_timeout_seconds", "upstream.idle_timeout_seconds")
	if c.Upstash.URL != "" || c.Upstash.Token != "" {
		out = append(out, "upstash")
	}
	out = append(out, "session_sticky.ttl", "session_sticky.gc_interval")
	// 归档目录/保留策略在启动时固化为 reqlog 的装配期参数，改这些要重建内核。
	out = append(out, "logging.request_archive_enabled", "logging.request_retention_days", "logging.request_archive_max_mb")
	return out
}

// mergeConfigMaps 把 incoming 深合并进 cur（原地），返回 cur。
// 对嵌套对象逐键覆盖而不是整体替换：面板表单只提交它管理的键，
// 未提交的兄弟键（含用户手写的未知键）保持原样。
func mergeConfigMaps(cur, incoming map[string]any) map[string]any {
	for k, v := range incoming {
		if inMap, ok := v.(map[string]any); ok {
			if curMap, ok := cur[k].(map[string]any); ok {
				cur[k] = mergeConfigMaps(curMap, inMap)
				continue
			}
		}
		cur[k] = v
	}
	return cur
}

// mergedJSON 把合并后的 map 序列化回 JSON（供 ParseConfig 校验）。
func mergedJSON(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}
