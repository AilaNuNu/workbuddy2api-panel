package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// callEndpoints 以给定配置请求 /panel/api/endpoints（带正确密钥，绕过鉴权）。
func callEndpoints(t *testing.T, listen string, desktop, allowLAN bool) map[string]any {
	t.Helper()
	p := New(Config{
		Version:    "test",
		APIKey:     testKey,
		ListenAddr: func() string { return listen },
		AllowLAN:   func() bool { return allowLAN },
		Desktop:    desktop,
	})
	req := httptest.NewRequest("GET", "/panel/api/endpoints", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, rec.Body.String())
	}
	return out
}

const testKey = "test-key"

// TestEndpointsDesktopLANClosed 桌面版未开局域网：**不能**给出可用的局域网地址。
//
// 这是本功能的诚实性底线：桌面版只绑 127.0.0.1，此时若照常显示一个 192.168.x.x
// 地址，用户拿去连必然失败，比不显示更糟。
func TestEndpointsDesktopLANClosed(t *testing.T) {
	d := callEndpoints(t, "127.0.0.1:7863", true, false)

	if got := d["localhost"]; got != "http://127.0.0.1:7863" {
		t.Errorf("localhost = %v", got)
	}
	if got := d["api_base"]; got != "http://127.0.0.1:7863/v1" {
		t.Errorf("api_base = %v（OpenAI 客户端要求带 /v1）", got)
	}
	if got := d["lan_available"]; got != false {
		t.Errorf("未开局域网时 lan_available 必须为 false，得到 %v", got)
	}
	if got := d["lan_addr"]; got != "" {
		t.Errorf("未开局域网时不能给出可用地址，得到 %v", got)
	}
	if got := d["lan_note"]; got == nil || got == "" {
		t.Error("必须说明为什么不可用（否则用户不知道去哪里打开）")
	}
}

// TestEndpointsDesktopLANOpen 桌面版已开局域网：给出可用地址，且端口取自**实际**
// 监听地址（桌面版端口可能已回退，不能用配置里的 listen 冒充）。
func TestEndpointsDesktopLANOpen(t *testing.T) {
	// 刻意让实际端口(41234)与配置里的首选端口(7863)不同，模拟端口回退。
	d := callEndpoints(t, "0.0.0.0:41234", true, true)

	if got := d["lan_available"]; got != true {
		t.Errorf("已开局域网时 lan_available 应为 true，得到 %v", got)
	}
	if got := d["localhost"]; got != "http://127.0.0.1:41234" {
		t.Errorf("localhost 必须用实际端口，得到 %v", got)
	}
	addr, _ := d["lan_addr"].(string)
	if addr == "" {
		t.Fatal("已开局域网时 lan_addr 不应为空")
	}
	if want := ":41234"; len(addr) < len(want) || addr[len(addr)-len(want):] != want {
		t.Errorf("lan_addr = %q 的端口应为实际监听端口 41234", addr)
	}
}

// TestEndpointsDisplayListen 0.0.0.0 要显示成人能看懂的写法。
// Go 对 0.0.0.0 的绑定把 Addr() 报成 "[::]:7863"（双栈），直接透出会被误读成
// "IPv6 专用"，且与用户填的 0.0.0.0 对不上。
func TestEndpointsDisplayListen(t *testing.T) {
	cases := map[string]string{
		"[::]:7863":      "0.0.0.0:7863",
		"0.0.0.0:7863":   "0.0.0.0:7863",
		"127.0.0.1:7863": "127.0.0.1:7863",
	}
	for in, want := range cases {
		if got := displayListen(in); got != want {
			t.Errorf("displayListen(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEndpointsServerDeploy 服务端部署：不替用户猜对外地址（可能是反代/域名/容器映射），
// 也不显示局域网开关（绑定范围由 listen 决定，不由面板开关决定）。
func TestEndpointsServerDeploy(t *testing.T) {
	d := callEndpoints(t, ":7863", false, false)

	if got := d["desktop"]; got != false {
		t.Errorf("desktop = %v, want false", got)
	}
	if got := d["lan_available"]; got != true {
		t.Errorf("服务端部署 lan_available 应为 true（不替用户判定不可用），得到 %v", got)
	}
	if got := d["lan_addr"]; got != "" {
		t.Errorf("服务端部署不应编造局域网地址，得到 %v", got)
	}
	if got := d["localhost"]; got != "http://127.0.0.1:7863" {
		t.Errorf("localhost = %v", got)
	}
}
