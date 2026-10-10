package appcore

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

// newAuthReq 构造一个带 Bearer 头的请求（供直接对凭据集合发起的断言使用）。
func newAuthReq(key string) *http.Request {
	req := httptest.NewRequest("GET", "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	return req
}

// TestCreateClientKeyHotAppliesAndPersists 新建密钥必须同时满足三件事：
// 立刻可用（热生效）、已落盘（重启后还在）、出现在列表里。
//
// 三者缺一都会以"看起来成功了"的形式失败：只热生效不落盘 → 重启后密钥消失；
// 只落盘不热生效 → 用户拿到密钥却要重启才能用（并以为密钥是错的）。
func TestCreateClientKeyHotAppliesAndPersists(t *testing.T) {
	rt, path := rotFixture(t, `{"api_key":"sk-admin"}`)
	list, create, _, _ := rt.ClientKeysAPI()

	v, err := create("客厅电视", "给我爸那台平板")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if v.Name != "客厅电视" || v.Note != "给我爸那台平板" || !v.Enabled {
		t.Errorf("返回的视图不对: %+v", v)
	}
	if !strings.HasPrefix(v.Key, "sk-") || len(v.Key) != 27 {
		t.Errorf("密钥格式不对（应复用 NewAPIKey 的 sk- + 24 字符）: %q", v.Key)
	}
	if v.ID == "" {
		t.Error("返回的 id 为空 —— 面板后续启停/删除都要用它")
	}

	// 1) 热生效：不需要重启就能用。
	if _, ok := authProbe(rt, v.Key); !ok {
		t.Error("新建的密钥不能立即通过鉴权（凭据没有被重建）")
	}

	// 2) 落盘：重新读盘仍在。
	persisted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.ClientKeys) != 1 || persisted.ClientKeys[0].Key != v.Key {
		t.Errorf("密钥没有落盘: %+v", persisted.ClientKeys)
	}

	// 3) 列表可见。
	keys, err := list()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Key != v.Key {
		t.Errorf("列表不对: %+v", keys)
	}
}

// TestCreateTwiceKeepsBoth 连续新建两把密钥必须都在。
//
// 这里钉的是"以磁盘为基线"这条规则：若第二次以启动时的 r.Config 为基线追加，
// 第一把会被覆盖掉 —— 表现为"加第二台设备，第一台就掉线了"，且不报任何错。
func TestCreateTwiceKeepsBoth(t *testing.T) {
	rt, path := rotFixture(t, `{"api_key":"sk-admin"}`)
	_, create, _, _ := rt.ClientKeysAPI()

	a, err := create("设备A", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := create("设备B", "")
	if err != nil {
		t.Fatal(err)
	}

	persisted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.ClientKeys) != 2 {
		t.Fatalf("应有 2 把密钥，实际 %d 把（第二把把第一把覆盖了？）", len(persisted.ClientKeys))
	}
	// 两把都要能通过鉴权。
	for _, k := range []string{a.Key, b.Key} {
		if _, ok := authProbe(rt, k); !ok {
			t.Errorf("密钥 %s 无法鉴权", k[:7]+"…")
		}
	}
}

// TestSetEnabledTakesEffectImmediately 停用后立刻失效，重新启用后立刻恢复。
//
// "停用"是首选的止血动作（比删除温和，可恢复），所以它必须是**立即**生效的：
// 停用了一个正在滥用的密钥却要等重启，等于没有止血。
func TestSetEnabledTakesEffectImmediately(t *testing.T) {
	rt, path := rotFixture(t, `{"api_key":"sk-admin"}`)
	_, create, setEnabled, _ := rt.ClientKeysAPI()

	v, err := create("设备A", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := authProbe(rt, v.Key); !ok {
		t.Fatal("前提不成立：新建密钥应可用")
	}

	if err := setEnabled(v.ID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if _, ok := authProbe(rt, v.Key); ok {
		t.Error("停用后仍然能通过鉴权（凭据没重建）")
	}
	// 停用也要落盘：否则重启后它又活了。
	persisted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.ClientKeys) != 1 || persisted.ClientKeys[0].Enabled {
		t.Errorf("停用状态没有落盘: %+v", persisted.ClientKeys)
	}

	if err := setEnabled(v.ID, true); err != nil {
		t.Fatalf("重新启用失败: %v", err)
	}
	if _, ok := authProbe(rt, v.Key); !ok {
		t.Error("重新启用后仍不可用")
	}
}

// TestDeleteClientKeyTakesEffectImmediately 删除后立刻失效且从磁盘消失。
func TestDeleteClientKeyTakesEffectImmediately(t *testing.T) {
	rt, path := rotFixture(t, `{"api_key":"sk-admin"}`)
	_, create, _, remove := rt.ClientKeysAPI()

	v, err := create("设备A", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := remove(v.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, ok := authProbe(rt, v.Key); ok {
		t.Error("删除后仍然能通过鉴权")
	}
	persisted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.ClientKeys) != 0 {
		t.Errorf("删除没有落盘: %+v", persisted.ClientKeys)
	}
	// 管理密钥不受影响。
	if _, ok := authProbe(rt, "sk-admin"); !ok {
		t.Error("删除客户端密钥不该影响管理密钥")
	}
}

// TestDeleteUnknownKeyReportsError 操作一个不存在的 id 必须报错。
//
// 静默成功会让界面显示"已停用"，而实际什么都没发生 —— 用户以为止血了，其实没有。
func TestDeleteUnknownKeyReportsError(t *testing.T) {
	rt, _ := rotFixture(t, `{"api_key":"sk-admin"}`)
	_, _, setEnabled, remove := rt.ClientKeysAPI()

	if err := remove("k_nope"); err == nil {
		t.Error("删除不存在的密钥应报错")
	}
	if err := setEnabled("k_nope", false); err == nil {
		t.Error("停用不存在的密钥应报错")
	}
	if err := remove(""); err == nil {
		t.Error("空 id 应报错")
	}
}

// TestCreateRejectsDuplicateName 重名必须拒绝：名字是日志与统计里区分设备的唯一依据。
func TestCreateRejectsDuplicateName(t *testing.T) {
	rt, _ := rotFixture(t, `{"api_key":"sk-admin"}`)
	_, create, _, _ := rt.ClientKeysAPI()

	if _, err := create("客厅电视", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := create("客厅电视", ""); err == nil {
		t.Error("重名应被拒绝")
	}
	if _, err := create("   ", ""); err == nil {
		t.Error("空名字应被拒绝")
	}
}

// TestClientKeysSurviveConfigSave 面板保存配置时不能把 client_keys 清掉。
//
// saveConfig 提交的是表单管理的键，client_keys 不在表单里；深合并必须让磁盘上
// 已有的 client_keys 原样保留。否则用户改一次配置，所有分发出的密钥会集体消失
// （而且因为它们本来就没被删，用户会以为是设备坏了）。
//
// 这里直接测 mergeAndWriteConfig（承载"深合并 + 原子落盘"的那一层）而不是整个
// saveConfig：后者还要热应用 pool/scheduler，与本用例要钉的规则无关。
func TestClientKeysSurviveConfigSave(t *testing.T) {
	rt, path := rotFixture(t, `{"api_key":"sk-admin"}`)
	_, create, _, _ := rt.ClientKeysAPI()
	v, err := create("设备A", "")
	if err != nil {
		t.Fatal(err)
	}

	// 模拟面板保存一个不含 client_keys 的表单提交。
	newCfg, err := mergeAndWriteConfig(path, []byte(`{"listen":"127.0.0.1:7863"}`))
	if err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	if len(newCfg.ClientKeys) != 1 {
		t.Fatalf("合并后的配置丢了 client_keys: %+v", newCfg.ClientKeys)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), v.Key) {
		t.Errorf("保存配置把 client_keys 从文件里清掉了：\n%s", raw)
	}
	if !strings.Contains(string(raw), "127.0.0.1:7863") {
		t.Errorf("提交的键没有写进去：\n%s", raw)
	}
	// 提交后按新配置重建的凭据仍然认这把密钥。
	creds := credentialsOf(newCfg)
	if _, ok := httpauth.Authenticate(newAuthReq(v.Key), creds); !ok {
		t.Error("保存配置后客户端密钥失效了")
	}
}
