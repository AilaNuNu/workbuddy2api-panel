package appcore

import (
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

// parseKeys 用最小 JSON 走完整的 ParseConfig 路径（与生产同一套校验），
// 返回配置或错误。仅给本文件的用例用。
func parseKeys(t *testing.T, body string) (*Config, error) {
	t.Helper()
	return ParseConfig([]byte(body))
}

// TestNormalizeClientKeysRejects 每一条拒绝规则都对应一个具体的坑，
// 这里逐条钉住 —— 静默放过任何一条都会变成本该被拦下的错误配置。
func TestNormalizeClientKeysRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // 期望错误信息里出现的关键词
	}{
		{
			name: "名字为空",
			body: `{"api_key":"sk-admin","client_keys":[{"name":"","key":"sk-a","enabled":true}]}`,
			want: "名字不能为空",
		},
		{
			name: "密钥为空",
			body: `{"api_key":"sk-admin","client_keys":[{"name":"电视","key":"","enabled":true}]}`,
			want: "密钥不能为空",
		},
		{
			name: "密钥与 api_key 相同（分发它等于泄露管理权）",
			body: `{"api_key":"sk-same","client_keys":[{"name":"电视","key":"sk-same","enabled":true}]}`,
			want: "与 api_key 相同",
		},
		{
			name: "两个密钥重复",
			body: `{"api_key":"sk-admin","client_keys":[
				{"name":"电视","key":"sk-a","enabled":true},
				{"name":"平板","key":"sk-a","enabled":true}]}`,
			want: "密钥与 client_keys[0] 相同",
		},
		{
			name: "id 重复",
			body: `{"api_key":"sk-admin","client_keys":[
				{"id":"k_x","name":"电视","key":"sk-a","enabled":true},
				{"id":"k_x","name":"平板","key":"sk-b","enabled":true}]}`,
			want: "重复",
		},
		{
			name: "名字重名（归属无法辨认）",
			body: `{"api_key":"sk-admin","client_keys":[
				{"name":"电视","key":"sk-a","enabled":true},
				{"name":"电视","key":"sk-b","enabled":true}]}`,
			want: "重名",
		},
		{
			name: "有客户端密钥但 api_key 为空",
			body: `{"api_key":"","client_keys":[{"name":"电视","key":"sk-a","enabled":true}]}`,
			want: "api_key 为空",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseKeys(t, c.body)
			if err == nil {
				t.Fatalf("期望被拒绝，但通过了")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息 %q 里没有 %q", err.Error(), c.want)
			}
		})
	}
}

// TestNormalizeClientKeysAccepts 合法配置不被误拒。
func TestNormalizeClientKeysAccepts(t *testing.T) {
	cfg, err := parseKeys(t, `{
		"api_key":"sk-admin",
		"client_keys":[
			{"name":"  客厅电视  ","key":" sk-a ","enabled":true,"note":"  我爸那台  "},
			{"name":"平板","key":"sk-b","enabled":false}
		]}`)
	if err != nil {
		t.Fatalf("合法配置被拒: %v", err)
	}
	if len(cfg.ClientKeys) != 2 {
		t.Fatalf("client_keys 数量 = %d, want 2", len(cfg.ClientKeys))
	}
	// 空白应被整理掉：日志与统计里直接用这些值展示，留空白会出现"看起来一样但不同"的名字。
	if cfg.ClientKeys[0].Name != "客厅电视" || cfg.ClientKeys[0].Key != "sk-a" || cfg.ClientKeys[0].Note != "我爸那台" {
		t.Errorf("空白未整理: %+v", cfg.ClientKeys[0])
	}
	// 没有客户端密钥的旧配置必须照常可用（零迁移）。
	if _, err := parseKeys(t, `{"api_key":"sk-admin"}`); err != nil {
		t.Errorf("无 client_keys 的旧配置被拒: %v", err)
	}
}

// TestClientKeyEffectiveID 显式 id 优先；为空时按键原文**确定性**派生。
//
// 确定性是这里的要点：normalize 每次 Load 都会跑，若"为空就随机补一个"，
// 手写配置每次加载都会换 id，用量统计会被切成互相孤立的碎片。
func TestClientKeyEffectiveID(t *testing.T) {
	explicit := ClientKey{ID: "k_given", Key: "sk-a"}
	if got := explicit.EffectiveID(); got != "k_given" {
		t.Errorf("显式 id 未被采用: %q", got)
	}

	derived := ClientKey{Key: "sk-a"}
	a := derived.EffectiveID()
	b := derived.EffectiveID()
	if a == "" {
		t.Fatal("派生 id 不能为空")
	}
	if a != b {
		t.Errorf("派生 id 不稳定: %q vs %q", a, b)
	}
	if other := (ClientKey{Key: "sk-b"}).EffectiveID(); other == a {
		t.Errorf("不同密钥派生出相同 id: %q", other)
	}

	// 走一遍配置加载：手写条目缺 id 时应得到同一个派生值（跨 Load 稳定）。
	cfg, err := parseKeys(t, `{"api_key":"sk-admin","client_keys":[{"name":"电视","key":"sk-a","enabled":true}]}`)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if got := cfg.ClientKeys[0].EffectiveID(); got != a {
		t.Errorf("配置加载后派生的 id = %q, want %q", got, a)
	}
}

// TestCredentialsOf 凭据集合的构造规则：管理密钥在前、只含**已启用**的客户端密钥、
// Admin 标记正确。这是"哪些密钥能过、能不能进面板"的唯一来源。
func TestCredentialsOf(t *testing.T) {
	cfg, err := parseKeys(t, `{
		"api_key":"sk-admin",
		"client_keys":[
			{"id":"k_tv","name":"电视","key":"sk-tv","enabled":true},
			{"id":"k_pad","name":"平板","key":"sk-pad","enabled":false}
		]}`)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	creds := credentialsOf(cfg)

	if len(creds) != 2 {
		t.Fatalf("凭据数量 = %d, want 2（停用的那个不该出现）: %+v", len(creds), creds)
	}
	if creds[0].ID != httpauth.AdminID || !creds[0].Admin || creds[0].Key != "sk-admin" {
		t.Errorf("第一个凭据应是管理密钥: %+v", creds[0])
	}
	if creds[1].ID != "k_tv" || creds[1].Admin || creds[1].Key != "sk-tv" {
		t.Errorf("第二个凭据应是启用的客户端密钥: %+v", creds[1])
	}
	for _, c := range creds {
		if c.ID == "k_pad" {
			t.Errorf("被停用的密钥不应出现在凭据里: %+v", c)
		}
	}
}

// TestCredentialsOfNoAdminKey 没有管理密钥时不应凭空造出一个空凭据
// （空 Key 凭据会让"没带头"的请求被误判为通过）。
func TestCredentialsOfNoAdminKey(t *testing.T) {
	cfg := Default()
	cfg.APIKey = ""
	cfg.ClientKeys = nil
	if creds := credentialsOf(cfg); len(creds) != 0 {
		t.Errorf("无密钥时凭据应为空（= 不鉴权），得到 %+v", creds)
	}
}

// TestNewClientKey 面板创建密钥的默认状态：立即可用、带 id 与创建时间。
func TestNewClientKey(t *testing.T) {
	k, err := NewClientKey("  客厅电视  ", "  备注  ")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if k.Name != "客厅电视" || k.Note != "备注" {
		t.Errorf("名字/备注未去空白: %+v", k)
	}
	if !k.Enabled {
		t.Error("新建密钥应默认启用")
	}
	if !strings.HasPrefix(k.Key, "sk-") {
		t.Errorf("密钥格式不对: %q", k.Key)
	}
	if k.ID == "" {
		t.Error("新建密钥必须有 id")
	}
	if k.CreatedAt == "" {
		t.Error("新建密钥应记录创建时刻")
	}
	if _, err := NewClientKey("   ", ""); err == nil {
		t.Error("空名字应被拒绝")
	}

	// 两个密钥不能撞车
	k2, _ := NewClientKey("平板", "")
	if k2.ID == k.ID || k2.Key == k.Key {
		t.Error("两次创建的 id/密钥不应相同")
	}
}
