package appcore

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
)

// rotFixture 造一个带 live holder 的最小 Runtime，仅供 RotateAPIKey 测试。
//
// 刻意不走 appcore.New：那会去装配 upstream/scheduler/redis 等一堆组件并创建真实
// 资源。RotateAPIKey 只依赖 Config / ConfigPath / live 三样，直接构造能让测试聚焦。
func rotFixture(t *testing.T, body string) (*Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{
		Config:     cfg,
		ConfigPath: p,
		// 与生产（runtime.go 的 livecfg.New）用同一组字段构造，避免 fixture 漏字段
		// 导致「测试过了但线上仍会清空快照」——这里正是要验证的陷阱。
		live: livecfg.New(livecfg.Snapshot{
			Creds:                credentialsOf(cfg),
			APIKey:               cfg.APIKey,
			SoftCooldown:         cfg.SoftRateDur,
			SanitizeFingerprints: cfg.Features.SanitizeBlacklistFingerprints,
		}),
	}
	return rt, p
}

// TestRotateAPIKeyChangesKeyAndPersists 轮换后：返回值、磁盘、热快照三处一致，
// 且**立即**按新密钥鉴权（不需要重启）。
func TestRotateAPIKeyChangesKeyAndPersists(t *testing.T) {
	rt, path := rotFixture(t, `{"api_key":"sk-old-key","cooldown":{"soft_rate":"600s"}}`)
	old := rt.Config.APIKey

	key, err := rt.RotateAPIKey()
	if err != nil {
		t.Fatalf("RotateAPIKey: %v", err)
	}
	if key == "" || key == old {
		t.Fatalf("应生成非空且不同于旧值的密钥：old=%q new=%q", old, key)
	}
	if !strings.HasPrefix(key, "sk-") {
		t.Errorf("密钥应带 sk- 前缀，得到 %q", key)
	}

	// 磁盘：必须落盘（否则重启后密钥会「自己变回去」）
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("落盘内容应是合法 JSON: %v", err)
	}
	if got := onDisk["api_key"]; got != key {
		t.Errorf("磁盘上的 api_key = %v，期望 %q", got, key)
	}

	// 热快照：立即生效
	if got := rt.LiveSnapshot().APIKey; got != key {
		t.Errorf("live 快照 api_key = %q，期望 %q", got, key)
	}
	// 运行配置同步：面板保存以 r.Config 作比较基线，过期会让下次保存误判
	if rt.Config.APIKey != key {
		t.Errorf("rt.Config.APIKey = %q，期望同步为 %q", rt.Config.APIKey, key)
	}
}

// TestRotateAPIKeyKeepsOtherLiveFields 是这次改动最容易踩的坑：
// live.Store 是**整体替换**快照，漏掉其它字段会把它们悄悄清零。
//
// 具体后果：SoftCooldown 归零 → 429 软冷却退回内置默认；SanitizeFingerprints
// 归 false → 出站请求体指纹脱敏被关闭（安全相关的行为静默改变）。
func TestRotateAPIKeyKeepsOtherLiveFields(t *testing.T) {
	body := `{"api_key":"sk-old","cooldown":{"soft_rate":"600s"},
	          "features":{"sanitize_blacklist_fingerprints":true}}`
	rt, _ := rotFixture(t, body)
	before := rt.LiveSnapshot()
	if !before.SanitizeFingerprints {
		t.Fatal("前置条件不成立：fixture 里指纹脱敏应为 true")
	}
	if before.SoftCooldown != 600*time.Second {
		t.Fatalf("前置条件不成立：soft cooldown 应为 600s，得到 %v", before.SoftCooldown)
	}

	if _, err := rt.RotateAPIKey(); err != nil {
		t.Fatalf("RotateAPIKey: %v", err)
	}

	after := rt.LiveSnapshot()
	if after.SoftCooldown != before.SoftCooldown {
		t.Errorf("轮换密钥不该改动软冷却：before=%v after=%v", before.SoftCooldown, after.SoftCooldown)
	}
	if after.SanitizeFingerprints != before.SanitizeFingerprints {
		t.Errorf("轮换密钥不该关闭指纹脱敏：before=%v after=%v",
			before.SanitizeFingerprints, after.SanitizeFingerprints)
	}
}

// TestRotateAPIKeyPreservesUnknownKeys 落盘走深合并，用户手写的未知键不能丢。
// 直接 Marshal 整个 Config 就会丢——这正是必须复用 saveConfig 的原因。
func TestRotateAPIKeyPreservesUnknownKeys(t *testing.T) {
	rt, path := rotFixture(t, `{"api_key":"sk-old","my_custom_note":"keep-me"}`)

	if _, err := rt.RotateAPIKey(); err != nil {
		t.Fatalf("RotateAPIKey: %v", err)
	}

	raw, _ := os.ReadFile(path)
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if got := onDisk["my_custom_note"]; got != "keep-me" {
		t.Errorf("未知键被丢弃：my_custom_note = %v", got)
	}
}

// TestRotateAPIKeyTwiceUnique 连续轮换应得到不同密钥（熵源正常、无缓存复用）。
func TestRotateAPIKeyTwiceUnique(t *testing.T) {
	rt, _ := rotFixture(t, `{"api_key":"sk-old"}`)
	a, err := rt.RotateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := rt.RotateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("两次轮换生成了相同密钥")
	}
}

// TestRotateAPIKeyWithoutLive 未启用热配置时要明确报错，而不是静默成功
// （静默成功会让面板以为换了密钥，实际上磁盘和运行态都没变）。
func TestRotateAPIKeyWithoutLive(t *testing.T) {
	rt := &Runtime{Config: &Config{}, ConfigPath: filepath.Join(t.TempDir(), "config.json")}
	if _, err := rt.RotateAPIKey(); err == nil {
		t.Fatal("live 为空时应返回错误")
	}
}

// TestNewAPIKeyShape 锁住密钥格式：18 字节 base64url = 24 字符 + "sk-" 前缀。
// 改长度/前缀会同时影响首启与重置两条路径（两处共用 NewAPIKey）。
func TestNewAPIKeyShape(t *testing.T) {
	k, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(k) != 27 {
		t.Errorf("密钥长度应为 27（3 前缀 + 24），得到 %d：%q", len(k), k)
	}
	if !strings.HasPrefix(k, "sk-") {
		t.Errorf("密钥应以 sk- 开头：%q", k)
	}
	if strings.ContainsAny(k, "+/=") {
		t.Errorf("应为 URL-safe 无填充 base64：%q", k)
	}
}

// authProbe 用某个密钥对当前热快照鉴权，返回命中的身份与是否通过。
// 走的是真实的鉴权入口（httpauth.Authenticate），而不是比较字符串。
func authProbe(rt *Runtime, key string) (httpauth.Identity, bool) {
	req := httptest.NewRequest("GET", "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	return httpauth.Authenticate(req, rt.LiveSnapshot().Creds)
}

// TestRotateAPIKeyInvalidatesOldAdminKey 轮换后旧管理密钥必须**立刻**失效。
//
// 这是最容易写错的一点：快照是整体替换的，只把 APIKey 字段改成新值、却保留原样的
// Creds 列表，旧密钥就还留在凭据集合里继续有效 —— 而「重置密钥」的全部意义就是让
// 它立刻失效。只测"新密钥能用"是抓不到这个 bug 的，必须同时断言旧密钥不能用。
func TestRotateAPIKeyInvalidatesOldAdminKey(t *testing.T) {
	rt, _ := rotFixture(t, `{
		"api_key":"sk-old-key",
		"client_keys":[{"id":"k_tv","name":"客厅电视","key":"sk-tv","enabled":true}]
	}`)

	if _, ok := authProbe(rt, "sk-old-key"); !ok {
		t.Fatal("轮换前旧管理密钥应有效")
	}
	if _, ok := authProbe(rt, "sk-tv"); !ok {
		t.Fatal("轮换前客户端密钥应有效")
	}

	newKey, err := rt.RotateAPIKey()
	if err != nil {
		t.Fatalf("RotateAPIKey: %v", err)
	}

	if _, ok := authProbe(rt, "sk-old-key"); ok {
		t.Error("轮换后旧管理密钥**仍然有效** —— Creds 没有被重建（重量级安全缺陷）")
	}
	id, ok := authProbe(rt, newKey)
	if !ok {
		t.Error("轮换后新管理密钥应有效")
	} else if !id.Admin || id.ID != httpauth.AdminID {
		t.Errorf("新管理密钥应命中管理身份，得到 %+v", id)
	}
	// 客户端密钥不该被「重置管理密钥」连坐：那是两个独立的东西，
	// 把它们一起失效会让所有下游设备同时掉线。
	id, ok = authProbe(rt, "sk-tv")
	if !ok {
		t.Error("轮换管理密钥不应影响客户端密钥")
	} else if id.ID != "k_tv" {
		t.Errorf("客户端密钥的身份应保持，得到 %+v", id)
	}
}

// TestRotateAPIKeyKeepsClientKeysFromDisk 轮换必须基于**磁盘上最新的**客户端密钥重建
// 凭据，而不是启动时的基线。
//
// r.Config 是启动时的装配基线（面板保存不会更新它）。若用它重建，面板后来新增的
// 客户端密钥会在一次「重置密钥」之后集体消失 —— 表现为"刚发的密钥过一会儿全失效了"，
// 且不会有任何报错。
func TestRotateAPIKeyKeepsClientKeysFromDisk(t *testing.T) {
	rt, path := rotFixture(t, `{"api_key":"sk-old-key"}`)

	// 模拟面板之后新增了一个客户端密钥（直接改盘，r.Config 保持启动时的样子）。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["client_keys"] = []any{map[string]any{
		"id": "k_new", "name": "后来加的", "key": "sk-later", "enabled": true,
	}}
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if len(rt.Config.ClientKeys) != 0 {
		t.Fatalf("前提不成立：运行配置本不该知道盘上新加的密钥，实际 %+v", rt.Config.ClientKeys)
	}

	if _, err := rt.RotateAPIKey(); err != nil {
		t.Fatalf("RotateAPIKey: %v", err)
	}
	id, ok := authProbe(rt, "sk-later")
	if !ok {
		t.Error("轮换后盘上新加的客户端密钥失效了 —— 凭据是从 r.Config 而不是磁盘重建的")
	} else if id.ID != "k_new" {
		t.Errorf("身份 = %+v, want k_new", id)
	}
}
