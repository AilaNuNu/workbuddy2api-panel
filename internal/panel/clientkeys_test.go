package panel

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
)

// probePath 用作探针的**真实**面板 API 路由。
//
// 必须挑一个真实注册过的路由：面板的鉴权是逐路由挂在 withAuth 上的，未注册的路径
// 会直接落到 mux 的 404，**根本不经过鉴权层** —— 拿它当探针会让"客户端密钥被拒绝"
// 这类用例因为 404 而假通过（实测踩过）。
//
// GET /panel/api/config 带鉴权时返回 501（本测试未注入 LoadConfig），因此
// 「401 = 没通过鉴权」「501 = 通过了鉴权」区分得很干净。
const probePath = "/panel/api/config"

// panelProbe 用一个密钥请求真实的受保护路由，返回响应。
func panelProbe(p *Panel, key string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", probePath, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	p.ServeHTTP(rec, req)
	return rec
}

// clientKeyPanel 构造一个「管理密钥 + 一个客户端密钥」的面板。
func clientKeyPanel() *Panel {
	live := livecfg.New(livecfg.Snapshot{
		APIKey: "sk-admin",
		Creds: []httpauth.Credential{
			{ID: httpauth.AdminID, Key: "sk-admin", Admin: true},
			{ID: "k_tv", Key: "sk-tv", Admin: false},
		},
	})
	return New(Config{Version: "test", Live: live})
}

// TestClientKeyCannotAccessPanel 客户端密钥必须进不了管理面板。
//
// 这是整个分级密钥设计存在的理由：面板能看全部账号、改配置、重置密钥。
// 若客户端密钥也能过，那么"把密钥发给其他设备"就等于把管理后台的钥匙一起发出去。
func TestClientKeyCannotAccessPanel(t *testing.T) {
	p := clientKeyPanel()

	// 先确认探针路径本身是受保护且可达的：管理密钥应过鉴权（得到 501 而非 401）。
	// 这一条同时守住"探针失效"：若哪天探针路径变成未注册的 404，这里会立刻失败，
	// 而不是让下面的用例继续假通过。
	admin := panelProbe(p, "sk-admin")
	if admin.Code == http.StatusUnauthorized {
		t.Fatalf("探针失效：管理密钥也被 401（探针路径 %s 可能没挂 withAuth）", probePath)
	}
	if admin.Code == http.StatusNotFound {
		t.Fatalf("探针失效：%s 未注册（404），未经过鉴权层，本用例会假通过", probePath)
	}

	if rec := panelProbe(p, "sk-tv"); rec.Code != http.StatusUnauthorized {
		t.Errorf("客户端密钥竟然通过了面板鉴权：code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := panelProbe(p, "sk-nope"); rec.Code != http.StatusUnauthorized {
		t.Errorf("无效密钥应 401，得到 %d", rec.Code)
	}
}

// TestClientKeyRejectionIndistinguishableFromInvalidKey 客户端密钥与完全无效的密钥
// 必须得到**完全相同的**响应。
//
// 若两者有差别（状态码、错误文案、长度足以区分），面板就变成了一个"这个密钥是否有效"
// 的判定接口：拿到一个客户端密钥的人可以据此确认它真实存在，也可以逐条试探别处的密钥。
func TestClientKeyRejectionIndistinguishableFromInvalidKey(t *testing.T) {
	p := clientKeyPanel()

	asClient := panelProbe(p, "sk-tv")
	asInvalid := panelProbe(p, "sk-nope")

	// 两者都必须是 401：若都是 404，说明探针没经过鉴权，本用例会假通过。
	if asClient.Code != http.StatusUnauthorized {
		t.Fatalf("客户端密钥应 401（得到 %d）；若为 404 说明探针未经过鉴权层", asClient.Code)
	}
	if asClient.Body.String() != asInvalid.Body.String() {
		t.Errorf("响应体不同：\n  客户端密钥: %s\n  无效密钥  : %s",
			asClient.Body.String(), asInvalid.Body.String())
	}
	for _, h := range []string{"WWW-Authenticate", "X-Error-Kind"} {
		if asClient.Header().Get(h) != asInvalid.Header().Get(h) {
			t.Errorf("响应头 %s 不同：%q vs %q", h, asClient.Header().Get(h), asInvalid.Header().Get(h))
		}
	}
}

// TestPanelAuthRequiredReflectsCredentials auth_required 反映"是否启用了鉴权"。
func TestPanelAuthRequiredReflectsCredentials(t *testing.T) {
	if !clientKeyPanel().loadLive().AuthRequired() {
		t.Error("有凭据时 AuthRequired 应为 true")
	}
	none := New(Config{Version: "test", APIKey: ""})
	if none.loadLive().AuthRequired() {
		t.Error("无凭据时 AuthRequired 应为 false")
	}
	if rec := panelProbe(none, ""); rec.Code == http.StatusUnauthorized {
		t.Error("未启用鉴权时不应要求密钥")
	}
}

// TestPanelDisabledClientKeyRejected 被停用的客户端密钥在面板侧也要拒绝。
func TestPanelDisabledClientKeyRejected(t *testing.T) {
	live := livecfg.New(livecfg.Snapshot{
		APIKey: "sk-admin",
		Creds:  httpauth.AdminCred("sk-admin"), // 客户端密钥被停用 → 不在凭据集合里
	})
	p := New(Config{Version: "test", Live: live})

	if rec := panelProbe(p, "sk-tv"); rec.Code != http.StatusUnauthorized {
		t.Errorf("停用的客户端密钥应 401，得到 %d", rec.Code)
	}
	if rec := panelProbe(p, "sk-admin"); rec.Code == http.StatusUnauthorized {
		t.Error("管理密钥仍应通过")
	}
}

// TestEveryPanelAPIRouteIsWrappedInAuth 静态守卫：每一条 /panel/api/ 路由都必须经
// withAuth。
//
// 为什么需要它：面板的鉴权是**逐路由**挂的（每条注册自己写 withAuth），没有一层
// "整个 /panel/api/ 前缀统一鉴权"的兜底。因此新增一条路由时漏写 withAuth，就会静默
// 变成一条**完全公开**的接口 —— 没有编译错误、没有运行时报错，只有被人访问时才暴露。
//
// 直接查源码而不是发请求枚举：ServeMux 不暴露已注册的模式列表，而源码检查能覆盖
// 未来新增的路由（不需要有人记得回来改这个测试）。
func TestEveryPanelAPIRouteIsWrappedInAuth(t *testing.T) {
	src, err := os.ReadFile("panel.go")
	if err != nil {
		t.Fatalf("读取 panel.go 失败: %v", err)
	}
	// 形如：p.mux.HandleFunc("GET /panel/api/xxx", p.withAuth(p.yyy))
	re := regexp.MustCompile(`p\.mux\.HandleFunc\("([A-Z]+ [^"]*)"\s*,\s*([^)]*(?:\([^)]*\))?[^)]*)\)`)
	matches := re.FindAllStringSubmatch(string(src), -1)

	var apiRoutes, unauthed int
	for _, m := range matches {
		pattern, handler := m[1], m[2]
		if !strings.HasPrefix(patternToPath(pattern), "/panel/api/") {
			continue
		}
		apiRoutes++
		if !strings.Contains(handler, "withAuth") {
			unauthed++
			t.Errorf("路由 %q 没有挂 withAuth —— 它是一条完全公开的面板接口", pattern)
		}
	}
	// 解析失败保护：正则若被重构打破会匹配到 0 条，此时这个测试会"全绿但什么也没查"。
	if apiRoutes == 0 {
		t.Fatal("未能从 panel.go 解析出任何 /panel/api/ 路由；正则已失效，本守卫不再有效")
	}
	t.Logf("已校验 %d 条 /panel/api/ 路由，全部挂有 withAuth", apiRoutes)
}

// patternToPath 从 "GET /panel/api/x" 里取出 "/panel/api/x"。
func patternToPath(p string) string {
	if i := strings.IndexByte(p, ' '); i >= 0 {
		return p[i+1:]
	}
	return p
}
