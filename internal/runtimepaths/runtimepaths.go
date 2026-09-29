// Package runtimepaths 统一「桌面模式 vs 网关模式」的数据目录策略。
//
// 背景：本网关原为服务端部署设计，auth_dir / state_file 等路径默认是相对路径
// （./auths、./data/state.json），相对当前工作目录解析。打包成 Windows 桌面应用
// 后，双击启动的工作目录是 exe 所在目录（可能是 Program Files，不可写），因此
// 需要一个与 exe 位置无关、稳定可写的用户数据目录。
//
// 设计取舍：
//   - 桌面模式是**显式开关**（config 的 "desktop": true），不是「检测到 GUI 就切换」：
//     路径策略属于用户的显式配置，隐式行为会让「服务端部署」与「桌面运行」在同一
//     份二进制里产生难以排查的差异；也无法从测试里验证。
//   - 未启用时本包不改变任何路径语义（零回归）。
//   - 有环境变量覆盖时以环境变量为准（WB2A_DATA_DIR），便于测试与便携部署
//     （把数据目录指到 U 盘 / 指定目录）。
package runtimepaths

import (
	"os"
	"path/filepath"
	"runtime"
)

// EnvDataDir 数据目录环境变量名；设置且非空时优先于所有推断逻辑。
const EnvDataDir = "WB2A_DATA_DIR"

// UserDataDir 返回桌面模式的用户数据目录：
//
//	$WB2A_DATA_DIR  非空则原样返回；
//	否则 Windows 取 %APPDATA%\WorkBuddy2API，macOS 取
//	~/Library/Application Support/WorkBuddy2API，其余（Linux 等）取
//	$XDG_DATA_HOME/workbuddy2api 或 ~/.local/share/workbuddy2api。
//
// 只计算路径，不创建目录（是否创建由调用方决定，以便区分「没配」与「建不出来」）。
func UserDataDir() string {
	if v := os.Getenv(EnvDataDir); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "WorkBuddy2API")
		}
		// APPDATA 缺失（罕见：极端精简的服务账号/容器）→ 回落到用户主目录。
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "WorkBuddy2API")
		}
		return "WorkBuddy2API"
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "WorkBuddy2API")
		}
		return "WorkBuddy2API"
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "workbuddy2api")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", "workbuddy2api")
		}
		return "workbuddy2api"
	}
}
