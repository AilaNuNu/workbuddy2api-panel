package httpauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func req(authz string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	return r
}

func TestVerifyBearer(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		authz string
		want  bool
	}{
		{"空 key 放行（未启用鉴权）", "", "", true},
		{"空 key 也放行任意头", "", "Bearer whatever", true},
		{"正确 key", "sk-abc123", "Bearer sk-abc123", true},
		{"错误 key", "sk-abc123", "Bearer sk-wrong", false},
		{"缺 Authorization 头", "sk-abc123", "", false},
		{"缺 Bearer 前缀", "sk-abc123", "sk-abc123", false},
		{"前缀大小写不符（规范要求精确）", "sk-abc123", "bearer sk-abc123", false},
		{"多余空格", "sk-abc123", "Bearer  sk-abc123", false},
		{"前缀相同但内容短", "sk-abc123", "Bearer sk-abc12", false},
		{"前缀相同但内容长", "sk-abc123", "Bearer sk-abc1234", false},
		{"key 恰好是前缀", "sk-abc", "Bearer sk-abcdef", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := VerifyBearer(req(c.authz), c.key); got != c.want {
				t.Errorf("VerifyBearer(key=%q, authz=%q) = %v, want %v", c.key, c.authz, got, c.want)
			}
		})
	}
}

// TestVerifyBearerWithoutHeaderStillCompares 缺头路径不应因"提前返回"而暴露形状差异：
// 这里只验证它确实返回 false 且不 panic（常量时间的性质无法用单测断言，靠实现保证）。
func TestVerifyBearerWithoutHeaderStillCompares(t *testing.T) {
	if VerifyBearer(req(""), "any-key") {
		t.Error("missing header must not pass")
	}
}

func TestDigestIsFixedLength(t *testing.T) {
	// 不同长度输入摘要后应等长（这是常量时间比较的前提）
	if len(digest("")) != len(digest("a-much-longer-secret-value")) {
		t.Error("digest length must not depend on input length")
	}
	if len(digest("x")) != 32 {
		t.Errorf("sha256 digest length = %d, want 32", len(digest("x")))
	}
}

// TestAuthenticateReturnsMatchedIdentity 多凭据下必须返回**命中的那一个**的身份，
// 而不是"通过了"这种无归属的结果 —— 用量统计与日志全靠它区分设备。
func TestAuthenticateReturnsMatchedIdentity(t *testing.T) {
	admin := Credential{ID: AdminID, Key: "sk-admin", Admin: true}
	tv := Credential{ID: "k_tv", Key: "sk-tv", Admin: false}
	pc := Credential{ID: "k_pc", Key: "sk-pc", Admin: false}
	creds := []Credential{admin, tv, pc}

	cases := []struct {
		name      string
		authz     string
		wantOK    bool
		wantID    string
		wantAdmin bool
	}{
		{"管理密钥 → admin 身份", "Bearer sk-admin", true, AdminID, true},
		{"客户端密钥 tv → 它自己的 id", "Bearer sk-tv", true, "k_tv", false},
		{"客户端密钥 pc → 它自己的 id", "Bearer sk-pc", true, "k_pc", false},
		{"错误密钥", "Bearer sk-nope", false, "", false},
		{"空密钥", "", false, "", false},
		{"只有前缀", "Bearer ", false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, ok := Authenticate(req(c.authz), creds)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if id.ID != c.wantID {
				t.Errorf("id = %q, want %q", id.ID, c.wantID)
			}
			if id.Admin != c.wantAdmin {
				t.Errorf("admin = %v, want %v", id.Admin, c.wantAdmin)
			}
		})
	}
}

// TestAuthenticateOrderIndependent 命中结果不能依赖凭据在列表里的位置。
// 这同时是"实现不能短路"的行为侧证据：若命中第一个就返回，靠后的凭据会表现为
// "有时认、有时不认"，随构造顺序漂移。
func TestAuthenticateOrderIndependent(t *testing.T) {
	a := Credential{ID: AdminID, Key: "sk-admin", Admin: true}
	b := Credential{ID: "k_b", Key: "sk-b", Admin: false}
	c := Credential{ID: "k_c", Key: "sk-c", Admin: false}

	orders := [][]Credential{{a, b, c}, {c, b, a}, {b, a, c}, {c, a, b}}
	for i, creds := range orders {
		for _, want := range []struct{ authz, id string }{
			{"Bearer sk-admin", AdminID}, {"Bearer sk-b", "k_b"}, {"Bearer sk-c", "k_c"},
		} {
			id, ok := Authenticate(req(want.authz), creds)
			if !ok || id.ID != want.id {
				t.Errorf("顺序%d: authz=%q → (%q,%v)，want id=%q", i, want.authz, id.ID, ok, want.id)
			}
		}
	}
}

// TestAuthenticateEmptyCredsMeansNoAuth 保持"未配置任何密钥 = 不鉴权"的旧语义。
//
// 返回的身份是**空的**（ID/Name 为空、Admin=true）：放行由 Admin 表达，而 ID 留空
// 让用量统计与请求日志能把"没人鉴权"和"管理密钥在用"区分开。若这里回 AdminID，
// 临时关掉鉴权期间的所有流量都会记到管理密钥名下。
func TestAuthenticateEmptyCredsMeansNoAuth(t *testing.T) {
	for _, creds := range [][]Credential{nil, {}, {{ID: "x", Key: ""}}} {
		id, ok := Authenticate(req(""), creds)
		if !ok {
			t.Errorf("空凭据集合应放行（未启用鉴权），得到拒绝")
			continue
		}
		if !id.Admin {
			t.Errorf("未启用鉴权应放行（Admin=true），得到 %+v", id)
		}
		if id.ID != "" || id.Name != "" {
			t.Errorf("未启用鉴权不该有归属身份（否则会被记成某个密钥在用），得到 %+v", id)
		}
	}
}

// TestAuthenticateAdminIdentity 真管理密钥命中时 identity 要带上归属标识与名字
// （用量统计与日志靠这两个字段回答"哪台设备发的"）。
func TestAuthenticateAdminIdentity(t *testing.T) {
	id, ok := Authenticate(req("Bearer sk-admin"), AdminCred("sk-admin"))
	if !ok {
		t.Fatal("管理密钥应通过")
	}
	if id.ID != AdminID || id.Name != AdminName || !id.Admin {
		t.Errorf("管理密钥身份 = %+v, want id=%q name=%q admin=true", id, AdminID, AdminName)
	}
}

// TestAuthenticateEmptyKeyCredentialNeverMatches 空 Key 的凭据绝不能命中。
// 这是最危险的一种错配：请求没带 Authorization 头时 token 是空串，若空 Key 参与比较
// 就会"匹配成功"，等于给所有人开门。
func TestAuthenticateEmptyKeyCredentialNeverMatches(t *testing.T) {
	creds := []Credential{
		{ID: "k_empty", Key: "", Admin: false},
		{ID: AdminID, Key: "sk-admin", Admin: true},
	}
	for _, authz := range []string{"", "Bearer ", "Bearer sk-other"} {
		if id, ok := Authenticate(req(authz), creds); ok {
			t.Errorf("authz=%q 竟然通过（id=%q）—— 空 Key 凭据被误匹配", authz, id.ID)
		}
	}
}

// TestAdminCred 只有管理密钥的调用方（未提供 Live 快照的嵌入方与测试）拿到的凭据集合。
func TestAdminCred(t *testing.T) {
	if got := AdminCred(""); got != nil {
		t.Errorf("空管理密钥应得到 nil（= 不鉴权），得到 %+v", got)
	}
	creds := AdminCred("sk-admin")
	if len(creds) != 1 || creds[0].ID != AdminID || !creds[0].Admin || creds[0].Key != "sk-admin" {
		t.Errorf("AdminCred 构造不正确: %+v", creds)
	}
}
