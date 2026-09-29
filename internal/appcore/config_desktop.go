// config_desktop.go 桌面模式的路径策略。
//
// 要解决的问题：网关原为服务端部署设计，auth_dir / state_file 默认是相对路径
// （./auths、./data/state.json），相对**当前工作目录**解析。打包成 Windows 桌面应用后，
// 双击启动的 CWD 是 exe 所在目录（可能落在 Program Files，不可写），因此需要一个与
// exe 位置无关、稳定可写的用户数据目录。
//
// 路径来源的判定规则（只有两条，无隐藏状态）：
//
//	用户在 config.json 里**显式写了** auth_dir / state_file → 一律照用户配置走
//	  （补成绝对路径，但不改动指向）。
//	没写（键缺席）→ 缺省值视为「未配置」，从数据目录派生：
//	  <data>/auths 与 <data>/data/state.json。
//
// 这条规则之所以够用，关键在于**桌面模式自动生成的配置不写这两个键**（见
// WriteDesktopDefault）：键一直缺席 → 路径一直跟着数据目录，用户搬动数据目录
// （或换机器、改 %APPDATA%）后自动跟随，不会留下孤儿目录里的账号。
//
// 早先的设计用了一个 desktop.json 记录「上次是按什么意图写的」，用来区分「派生值」
// 与「用户手改」；那引入了状态漂移（记录丢了/旧了就误判）。显式键判定把状态收敛回
// 配置文件本身：唯一事实来源，人工可读可改。
package appcore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/linguo2625469/workbuddy2api-panel/internal/runtimepaths"
)

// resolveDataDir 取桌面模式数据目录：显式配置优先，否则按平台推断
// （Windows: %APPDATA%\WorkBuddy2API；见 internal/runtimepaths）。
func resolveDataDir(c *Config) string {
	if v := trimSpace(c.DesktopDataDir); v != "" {
		return v
	}
	return runtimepaths.UserDataDir()
}

// resolveDesktopPaths 在桌面模式下解析 c 的路径字段为绝对路径（纯计算，不碰文件系统）。
//
// explicitPaths 表示配置文件中是否显式写了 auth_dir / state_file（由 jsonHasKey 在
// Load 里按**原始 JSON 键**判定，不能靠反序列化结果——ParseConfigInto 会先套用
// Default()，区分不出「用户写的」与「缺省值」）。
//
// 返回数据目录，供启动日志透出。
//
// 拆成纯计算不是为了洁癖：配置比较（config_save.go 的 assemblyFieldsDiffer）需要
// 用**同一套规则**解释两份配置，而比较一个动作不该有建目录这种副作用。
func resolveDesktopPaths(c *Config, explicitPaths bool) (string, error) {
	dataDir, err := absClean(resolveDataDir(c))
	if err != nil {
		return "", fmt.Errorf("desktop 数据目录: %w", err)
	}

	if !explicitPaths {
		c.AuthDir = filepath.Join(dataDir, "auths")
		c.StateFile = filepath.Join(dataDir, "data", "state.json")
	}

	// 无论走哪条分支，相对路径都要补成绝对路径：桌面进程的 CWD 不可控
	// （快捷方式可以带任意「起始位置」），相对路径会把数据写到意想不到的地方。
	if c.AuthDir, err = absClean(c.AuthDir); err != nil {
		return dataDir, fmt.Errorf("auth_dir: %w", err)
	}
	if c.StateFile, err = absClean(c.StateFile); err != nil {
		return dataDir, fmt.Errorf("state_file: %w", err)
	}
	return dataDir, nil
}

// applyDesktopLayout 解析路径并确保数据目录存在（Load 用）。
func applyDesktopLayout(c *Config, explicitPaths bool) (string, error) {
	dataDir, err := resolveDesktopPaths(c, explicitPaths)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("创建桌面数据目录 %s 失败: %w", dataDir, err)
	}
	return dataDir, nil
}

// WriteDesktopDefault 首次桌面运行落一份推荐配置：与 WriteDefault 同源，但
//   - 写入 "desktop": true（后续每次启动都走桌面路径策略）；
//   - **不写 auth_dir / state_file**，让它们保持缺席 → 每次启动从数据目录派生
//     （这是「搬动数据目录后不产生孤儿账号」的前提，见文件头注释）。
//
// 已存在时经 O_EXCL 原子拒绝，绝不改写用户配置。
func WriteDesktopDefault(path string) (apiKey string, err error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir config dir: %w", err)
		}
	}
	// 复用 WriteDefault 的取值与随机密钥逻辑，再摘掉两个路径键。
	if apiKey, err = WriteDefault(path); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return apiKey, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return apiKey, err
	}
	delete(m, "auth_dir")
	delete(m, "state_file")
	m["desktop"] = true
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return apiKey, err
	}
	return apiKey, os.WriteFile(path, out, 0o600)
}

// jsonHasKey 判断原始 JSON 对象里是否存在某个键。
// 解析失败按「没有」处理（调用方随后会因为 JSON 非法而报错，不必在这里抢答）。
func jsonHasKey(raw []byte, key string) bool {
	if len(raw) == 0 {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// absClean 把路径规范化为绝对路径（相对路径以当前工作目录为基准解析一次）。
func absClean(p string) (string, error) {
	if trimSpace(p) == "" {
		return "", fmt.Errorf("路径为空")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// trimSpace 本地去首尾空白（只为一处调用引入 strings 不划算）。
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
