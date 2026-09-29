package main

// 关闭窗口的三种选择。
type closeChoice int

const (
	closeChoiceTray   closeChoice = iota // 最小化到托盘：窗口隐藏，网关继续跑
	closeChoiceQuit                      // 退出程序：收掉网关并结束进程
	closeChoiceCancel                    // 取消：什么都不做，回到窗口
)

// 关闭确认对话框的标题与正文。
//
// 文案不参与判断——这正是修复的要点：Wails v3 beta.25 的 **Windows** 对话框实现
// 拿按钮标签当返回值用（`if button.Label == result`，result 是 "Yes"/"No"），
// 于是自定义标签（中文）永远匹配不上，回调静默不执行：对话框关了却什么都没发生。
// 这里改成按对话框的**返回值**判断，与标签无关，文案就能是任意中文。
// （Linux/macOS 的实现按按钮**下标**分派，本来就没这个问题。）
const (
	closeDialogTitle = "关闭 WorkBuddy2API"
	closeDialogText  = "窗口关闭后网关可以继续在后台运行（托盘图标），已连接的客户端不会断开。\n\n" +
		"「是」= 最小化到托盘（推荐）\n" +
		"「否」= 退出程序\n" +
		"「取消」= 什么都不做"
)

// nativeWindowHandle 取窗口的原生句柄（Windows 上是 HWND），
// 用于让对话框归属主窗口并保持模态；取不到时返回 0
// （对话框仍会显示，只是不挂父窗口）。
//
// 窗口若已在销毁流程中，NativeWindow() 会返回 nil，所以这里必须容忍 0。
func (da *desktopApp) nativeWindowHandle() uintptr {
	if da.window == nil {
		return 0
	}
	return uintptr(da.window.NativeWindow())
}
