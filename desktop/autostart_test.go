//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 用测试专用标识，绝不碰用户真实的启动项；无论成败都在最后清掉。
const autostartTestID = "wb2a-autostart-test"

// newTestApp 造一个只为 Autostart 用的 App（不 Run，不需要窗口/WebView）。
func newTestApp() *application.App {
	return application.New(application.Options{
		Name:        "wb2a-autostart-test",
		Description: "autostart test",
	})
}

// TestAutostartRegistryRoundTrip 端到端验证开机自启真的写进了
// HKCU\Software\Microsoft\Windows\CurrentVersion\Run，并且能干净地移除。
//
// 为什么值得这么测：自启是「写注册表」这类只在真实系统上才暴露问题的动作——
// 键名、可执行文件路径引号、清理是否彻底，单测里 mock 掉注册表就全测不到了。
// 直接读注册表断言，比只看 Enable() 返回 nil 可靠。
func TestAutostartRegistryRoundTrip(t *testing.T) {
	if os.Getenv("WB2A_SKIP_REGISTRY_TEST") != "" {
		t.Skip("WB2A_SKIP_REGISTRY_TEST set")
	}
	app := newTestApp()
	defer app.Autostart.Disable() // 兜底清理，即使断言失败

	// 起点必须是没有登记（上一次测试残留也不该有）。
	if on, err := app.Autostart.IsEnabled(); err != nil {
		t.Fatalf("IsEnabled: %v", err)
	} else if on {
		t.Fatalf("测试开始前已存在自启登记，拒绝继续（应先清理）")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	if err := app.Autostart.EnableWithOptions(application.AutostartOptions{
		Identifier: autostartTestID,
	}); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// 直接查注册表，而不是信任 Enable() 的返回值。
	got := regQuery(t, autostartTestID)
	if got == "" {
		t.Fatalf("注册表里没有 %s 项（Enable 返回成功但没写进去）", autostartTestID)
	}
	// 引号规则：含空格/引号/制表符才加引号（Wails 的 quoteWindowsArg），
	// 路径无空格时不加引号是**正确**的，不该断言必然有引号。
	needsQuote := strings.ContainsAny(exe, ` "`+"	")
	if needsQuote && !strings.HasPrefix(got, `"`) {
		t.Errorf("路径含空格却未加引号（开机启动会解析错）：%s", got)
	}
	// 无论加不加引号，首段都必须能还原成当前可执行文件（含空格路径时去掉引号再比）。
	first := got
	if strings.HasPrefix(first, `"`) {
		if i := strings.Index(first[1:], `"`); i >= 0 {
			first = first[1 : 1+i]
		}
	} else if i := strings.IndexByte(first, ' '); i >= 0 {
		first = first[:i]
	}
	if !strings.EqualFold(first, exe) {
		t.Errorf("注册表值首段不是当前可执行文件\n  首段: %s\n  期望: %s", first, exe)
	}

	// 读回状态要一致（Status 与 IsEnabled 都走注册表）。
	if on, err := app.Autostart.IsEnabled(); err != nil || !on {
		t.Fatalf("Enable 之后 IsEnabled = %v, err = %v", on, err)
	}
	if st, err := app.Autostart.Status(); err != nil {
		t.Errorf("Status: %v", err)
	} else if st.Strategy != application.AutostartStrategyRegistryRun {
		t.Errorf("Strategy = %q，期望 %q", st.Strategy, application.AutostartStrategyRegistryRun)
	}

	// 关闭必须彻底移除该项。
	if err := app.Autostart.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got := regQuery(t, autostartTestID); got != "" {
		t.Errorf("Disable 之后注册表仍残留：%s", got)
	}
	if on, err := app.Autostart.IsEnabled(); err != nil || on {
		t.Errorf("Disable 之后 IsEnabled = %v, err = %v", on, err)
	}

	// 再删一次应当无害（幂等）。
	if err := app.Autostart.Disable(); err != nil {
		t.Errorf("重复 Disable 不应报错：%v", err)
	}
}

// TestAutostartIdentifierValidation 标识符含非法字符必须在写注册表之前被拒。
// 否则会写出一个键名怪异、事后难以清理的启动项。
func TestAutostartIdentifierValidation(t *testing.T) {
	app := newTestApp()
	err := app.Autostart.EnableWithOptions(application.AutostartOptions{
		Identifier: "bad id with spaces",
	})
	if err == nil {
		app.Autostart.Disable()
		t.Fatal("含空格的标识符应当被拒绝")
	}
}

// regQuery 读取 HKCU\...\Run 下指定值的字符串内容；不存在返回空串。
func regQuery(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("reg", "query",
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", name).CombinedOutput()
	if err != nil {
		return "" // 值不存在时 reg query 以非零码退出，属于预期
	}
	// 输出形如：    wb2a-autostart-test    REG_SZ    "C:\path\app.exe"
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == name && f[1] == "REG_SZ" {
			return strings.Join(f[2:], " ")
		}
	}
	return ""
}
