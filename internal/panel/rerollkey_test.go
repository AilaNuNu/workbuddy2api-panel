package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// postReroll 以给定配置与密钥请求 /panel/api/config/reroll_key，返回状态码与解析结果。
func postReroll(t *testing.T, p *Panel, bearer string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/panel/api/config/reroll_key", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestRerollKeyReturnsNewKey 正常路径：返回 200 且带上新密钥。
//
// 响应体必须含 api_key——前端要立刻用它覆盖 localStorage，否则轮换后当前会话的
// 后续请求还带旧密钥，会被自己刚设的新密钥挡在 401 外面。
func TestRerollKeyReturnsNewKey(t *testing.T) {
	p := New(Config{
		Version:      "test",
		APIKey:       testKey,
		RotateAPIKey: func() (string, error) { return "sk-brand-new-key", nil },
	})
	code, out := postReroll(t, p, testKey)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := out["api_key"]; got != "sk-brand-new-key" {
		t.Errorf("api_key = %v，期望新密钥被回传", got)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Errorf("ok 应为 true，得到 %v", out["ok"])
	}
}

// TestRerollKeyRequiresAuth 未带密钥不得重置。
//
// 这是该接口的安全边界：它能改鉴权凭据，若匿名可调，局域网内任何人（或任何本机
// 程序）都能把用户锁在门外。
func TestRerollKeyRequiresAuth(t *testing.T) {
	called := false
	p := New(Config{
		Version:      "test",
		APIKey:       testKey,
		RotateAPIKey: func() (string, error) { called = true; return "sk-x", nil },
	})
	code, _ := postReroll(t, p, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("匿名调用 status = %d, want 401", code)
	}
	if called {
		t.Error("未通过鉴权时不得调用 RotateAPIKey")
	}
}

// TestRerollKeyRejectsWrongKey 用错密钥同样不得重置。
func TestRerollKeyRejectsWrongKey(t *testing.T) {
	called := false
	p := New(Config{
		Version:      "test",
		APIKey:       testKey,
		RotateAPIKey: func() (string, error) { called = true; return "sk-x", nil },
	})
	code, _ := postReroll(t, p, "sk-wrong")
	if code != http.StatusUnauthorized {
		t.Fatalf("错误密钥 status = %d, want 401", code)
	}
	if called {
		t.Error("密钥错误时不得调用 RotateAPIKey")
	}
}

// TestRerollKeyNotImplemented 宿主未注入时返回 501，而不是假装成功。
func TestRerollKeyNotImplemented(t *testing.T) {
	p := New(Config{Version: "test", APIKey: testKey})
	code, _ := postReroll(t, p, testKey)
	if code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", code)
	}
}

// TestRerollKeyPropagatesError 落盘失败必须报 500，不得回一个成功但没生效的结果。
//
// 失败时若返回 200 + 密钥，前端会把它写进 localStorage，而服务端仍在用旧密钥 →
// 用户当场被锁在外面，且拿到的"新密钥"是假的。
func TestRerollKeyPropagatesError(t *testing.T) {
	p := New(Config{
		Version:      "test",
		APIKey:       testKey,
		RotateAPIKey: func() (string, error) { return "", errors.New("磁盘只读") },
	})
	code, out := postReroll(t, p, testKey)
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
	if _, has := out["api_key"]; has {
		t.Error("失败响应不得包含 api_key")
	}
}
