package appcore

import (
	"fmt"

	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
)

// RotateAPIKey 生成新密钥，落盘并立即生效，返回新密钥。
//
// 为什么落盘要复用 mergeAndWriteConfig 而不是自己 Marshal 整个 Config：
// 那会丢掉用户手写的未知键（见 config_save.go 的说明）。这里只提交 api_key 一个键，
// 其余原样保留。
//
// 为什么不用重启：api_key 是热生效字段（见 config_diff.go 的 hotAppliedFields）——
// 下一个请求即按新密钥鉴权。轮换密钥不该打断正在服务的客户端，所以这里刻意不触发
// 「需要重启」。
//
// 顺序很关键：**先落盘成功，再热应用**。反过来的话，热应用成功而落盘失败时，运行中的
// 密钥与文件里的就不一致了，重启一次会「密钥自己变回去」——用户明明换了密钥却又被旧
// 密钥放进来。所以这里写完盘才调用 live.Store。
//
// 注意 live.Store 是**整体替换**快照，不是按字段合并：必须带上其它字段的当前值，
// 否则 SoftCooldown 归零（429 软冷却退回内置默认）、SanitizeFingerprints 归 false
// （出站指纹脱敏被静默关闭）。测试 TestRotateAPIKeyKeepsOtherLiveFields 钉住这一点。
func (r *Runtime) RotateAPIKey() (string, error) {
	if r.live == nil {
		return "", fmt.Errorf("rotate api_key: 未启用热配置（live 为空）")
	}
	key, err := NewAPIKey()
	if err != nil {
		return "", err
	}

	newCfg, err := mergeAndWriteConfig(r.ConfigPath, []byte(fmt.Sprintf(`{"api_key":%q}`, key)))
	if err != nil {
		return "", fmt.Errorf("rotate api_key: %w", err)
	}

	// 落盘成功后才热应用；带上快照里的其它字段，避免被整体替换清空。
	snap := r.live.Load()
	snap.APIKey = key
	// Creds 必须**整体重建**，不能只改 APIKey：凭据列表里还留着旧管理密钥，
	// 只改 APIKey 的话旧密钥继续有效 —— 而「重置密钥」的全部意义就是让它立刻失效。
	//
	// 用落盘后的 newCfg 而不是 r.Config 重建：r.Config 是**启动时**的装配基线
	// （面板保存不更新它），拿它重建会把面板后来新增的客户端密钥全部丢掉。
	snap.Creds = credentialsOf(newCfg)
	r.live.Store(snap)

	// 同步内存中的运行配置，使 r.Config 继续代表「当前生效的配置」：
	// 面板保存用它作比较基线，过期会让下一次保存误判装配期字段有无变化。
	r.Config.APIKey = key
	return key, nil
}

// LiveSnapshot 返回当前热配置快照（测试与宿主诊断用）。
func (r *Runtime) LiveSnapshot() livecfg.Snapshot {
	if r.live == nil {
		return livecfg.Snapshot{}
	}
	return r.live.Load()
}
