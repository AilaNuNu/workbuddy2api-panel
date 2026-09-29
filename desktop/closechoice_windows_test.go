//go:build windows

package main

import "testing"

// TestCloseChoiceFromID 覆盖「关闭窗口」对话框的返回值映射。
//
// 这是修复用户报的「点是没反应」时留下的回归测试：出问题的版本用 Wails 的
// Question()，它按**按钮标签**分派回调（`button.Label == "Yes"/"No"`），
// 中文标签永远匹配不上 → 三个回调全部静默丢失。现在只按系统返回值判断。
//
// 关键断言：IDYES/IDNO/IDCANCEL 各归其位，且**任何意外值都收敛到「取消」**
// （取消是最安全的分支：不动窗口、不杀网关）。
func TestCloseChoiceFromID(t *testing.T) {
	cases := []struct {
		name string
		ret  int32
		want closeChoice
	}{
		{"是 → 最小化到托盘", idYes, closeChoiceTray},
		{"否 → 退出程序", idNo, closeChoiceQuit},
		{"取消 → 什么都不做", idCancel, closeChoiceCancel},
		{"OK（不该出现）→ 当作取消", idOK, closeChoiceCancel},
		{"超时 32000 → 当作取消", 32000, closeChoiceCancel},
		{"0（异常）→ 当作取消", 0, closeChoiceCancel},
		{"-1（异常）→ 当作取消", -1, closeChoiceCancel},
	}
	for _, c := range cases {
		if got := closeChoiceFromID(c.ret); got != c.want {
			t.Errorf("%s：closeChoiceFromID(%d) = %v，期望 %v", c.name, c.ret, got, c.want)
		}
	}
}

// TestCloseChoiceConstants 锁住与 winuser.h 的一致性。
//
// 这些常量是宏，x/sys 没有导出，只能照定义抄；抄错就会「点什么都没反应」
// 或更糟——点「取消」变成退出。所以钉死数值。
func TestCloseChoiceConstants(t *testing.T) {
	if idOK != 1 || idCancel != 2 || idYes != 6 || idNo != 7 {
		t.Fatalf("MessageBox 返回值常量与 winuser.h 不一致：idOK=%d idCancel=%d idYes=%d idNo=%d",
			idOK, idCancel, idYes, idNo)
	}
}

// TestCloseChoiceIsExhaustive 确认三个分支互不相同（防止常量被改成同值后
// switch 静默合并，导致两种选择执行同一动作）。
func TestCloseChoiceIsExhaustive(t *testing.T) {
	seen := map[closeChoice]string{}
	for _, c := range []struct {
		choice closeChoice
		name   string
	}{
		{closeChoiceTray, "tray"},
		{closeChoiceQuit, "quit"},
		{closeChoiceCancel, "cancel"},
	} {
		if prev, dup := seen[c.choice]; dup {
			t.Errorf("closeChoice 取值重复：%s 与 %s 相同（%d）", prev, c.name, c.choice)
		}
		seen[c.choice] = c.name
	}
}
