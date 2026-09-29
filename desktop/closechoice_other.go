//go:build !windows

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// askCloseChoice 在非 Windows 平台上的实现。
//
// 这里可以放心用 Wails 的对话框：Linux/macOS 的实现是**按按钮下标**分派回调的
// （dialogs_linux.go / dialogs_darwin.go），中文标签可用；出问题的只有 Windows
// 的标签匹配实现（见 closechoice.go）。所以本文件保持 Wails 原生对话框，
// Windows 则走 closechoice_windows.go 的原生 MessageBox。
//
// parent 在非 Windows 平台上不由本函数使用（Wails 自己取当前窗口作为归属）。
func askCloseChoice(parent uintptr, done func(closeChoice)) {
	app := application.Get()
	if app == nil {
		done(closeChoiceCancel)
		return
	}
	dlg := app.Dialog.Question().
		SetTitle(closeDialogTitle).
		SetMessage(closeDialogText)

	yes := dlg.AddButton("是")
	no := dlg.AddButton("否")
	cancel := dlg.AddButton("取消")
	dlg.SetDefaultButton(yes)
	dlg.SetCancelButton(cancel)

	yes.OnClick(func() { done(closeChoiceTray) })
	no.OnClick(func() { done(closeChoiceQuit) })
	cancel.OnClick(func() { done(closeChoiceCancel) })

	// Show 内部会 InvokeSync 到主线程（已在主线程时直接执行）。
	dlg.Show()
}
