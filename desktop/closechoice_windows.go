//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

// MessageBox 的返回值（winuser.h 里是宏，x/sys 未导出，这里照定义写）。
const (
	idOK     = 1
	idCancel = 2
	idYes    = 6
	idNo     = 7
)

// askCloseChoice 弹出关闭确认对话框（模态），并**在用户作答后同步调用 done**。
//
// 为什么自己调 MessageBox，而不是用 application.Dialog.Question()：
// 见 closechoice.go 的说明——Wails v3 beta.25 的 Windows 实现按按钮标签分派回调，
// 自定义标签（中文）永远匹配不上，回调静默丢失。这里直接读系统返回值，
// 行为由返回值决定，与标签无关。
//
// 用「是/否/取消」三键（MB_YESNOCANCEL）的理由：
//
//	Wails 自己的 Windows 对话框只支持 MB_OK / MB_YESNO 这类固定组合，
//	自定义按钮文字需要 TaskDialog，而要显示中文自定义按钮得声明
//	comctl32 v6 依赖（manifest），本二进制没嵌 manifest。
//	系统按钮的中文由 Windows 按语言自动显示，反而最稳。
//
// 必须在主线程调用（MessageBox 是模态的，要用到消息泵）。调用点有两个：
//  1. 窗口关闭钩子（本身就在主线程）
//  2. 托盘菜单「退出」（在 UI 线程，但为稳妥仍显式切主线程）
//
// done 在主线程上调用——调用方若要做 Hide/Quit，请自行再切一次主线程，
// 避免在模态对话框返回的栈里做重活。
func askCloseChoice(parent uintptr, done func(closeChoice)) {
	flags := uint32(windows.MB_YESNOCANCEL | windows.MB_ICONQUESTION |
		windows.MB_DEFBUTTON1 | windows.MB_SETFOREGROUND)
	ret, err := windows.MessageBox(
		windows.HWND(parent),
		windows.StringToUTF16Ptr(closeDialogText),
		windows.StringToUTF16Ptr(closeDialogTitle),
		flags,
	)
	if err != nil {
		// 对话框都没弹出来（极少见）。当作「取消」最安全：不动窗口、不杀网关，
		// 用户可以改用托盘菜单「退出」。
		done(closeChoiceCancel)
		return
	}
	done(closeChoiceFromID(ret))
}

// closeChoiceFromID 把 MessageBox 的返回值映射成用户选择。
//
// 抽成纯函数是为了能被测试覆盖：这里正是出过 bug 的地方——原先依赖 Wails
// 「比对按钮标签字符串」来分派，中文标签永远匹配不上 "Yes"/"No"，回调静默丢失。
// 现在判断只依赖返回值，与标签/语言无关，所以可以被断言。
func closeChoiceFromID(ret int32) closeChoice {
	switch ret {
	case idYes:
		return closeChoiceTray
	case idNo:
		return closeChoiceQuit
	default: // idCancel / idOK / IDTIMEOUT / 关闭对话框
		return closeChoiceCancel
	}
}
