package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
	"github.com/linguo2625469/workbuddy2api-panel/internal/usage"
)

// resetDynamicModelsCache 清掉包级动态模型缓存，**包括负缓存 lastFail 与冷却窗口**。
//
// 为什么必须清：fetchDynamicModels 拉取失败会写下 lastFail，在 modelsFetchFailCooldown
// 内直接返回空列表。本文件用的假上游"对任何请求都回 SSE"，模型探测必然失败 ——
// 若不清，同包内之后依赖模型列表的用例（TestModelsEndpoint）会看到 0 个模型而失败，
// 且失败原因与它们自身的逻辑毫无关系（实测踩过：本文件加入后 TestModelsEndpoint 变红）。
func resetDynamicModelsCache() {
	dynamicModelsCache.Lock()
	dynamicModelsCache.ids = nil
	dynamicModelsCache.fetched = time.Time{}
	dynamicModelsCache.lastFail = time.Time{}
	dynamicModelsCache.Unlock()
}

// gatewayWithKeys 构造一个带「管理密钥 + 客户端密钥」的网关。
func gatewayWithKeys(t *testing.T) *Handler {
	t.Helper()
	// 进出一律清缓存：既不受前面用例的负缓存影响，也不把负缓存留给后面的用例。
	resetDynamicModelsCache()
	t.Cleanup(resetDynamicModelsCache)
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	return NewHandler(Config{
		Pool:     p,
		Upstream: up,
		Live: livecfg.New(livecfg.Snapshot{
			APIKey: "sk-admin",
			Creds: []httpauth.Credential{
				{ID: httpauth.AdminID, Key: "sk-admin", Admin: true},
				{ID: "k_tv", Key: "sk-tv", Admin: false},
			},
		}),
	})
}

// TestGatewayAcceptsClientKey 网关必须接受客户端密钥 —— 这正是它存在的意义：
// 分发给其他设备调用 /v1/*。分级隔离在面板侧（见 panel 包的同名测试）。
func TestGatewayAcceptsClientKey(t *testing.T) {
	h := gatewayWithKeys(t)

	cases := []struct {
		name, key, method, path string
		body                    string
		wantAuth                bool
	}{
		{"管理密钥 /status", "sk-admin", "GET", "/status", "", true},
		{"客户端密钥 /status", "sk-tv", "GET", "/status", "", true},
		{"客户端密钥 /v1/models", "sk-tv", "GET", "/v1/models", "", true},
		{"客户端密钥 /v1/chat/completions", "sk-tv", "POST", "/v1/chat/completions", `{"model":"glm-5.2","messages":[]}`, true},
		{"无效密钥 /status", "sk-nope", "GET", "/status", "", false},
		{"不带密钥 /status", "", "GET", "/status", "", false},
		{"客户端密钥不能被停用后仍生效（此处仅验证错误密钥）", "sk-other", "GET", "/status", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var req *http.Request
			if c.body != "" {
				req = httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			} else {
				req = httptest.NewRequest(c.method, c.path, nil)
			}
			if c.key != "" {
				req.Header.Set("Authorization", "Bearer "+c.key)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			gotAuth := rec.Code != http.StatusUnauthorized
			if gotAuth != c.wantAuth {
				t.Errorf("code=%d（期望鉴权%s）；body=%s", rec.Code,
					map[bool]string{true: "通过", false: "失败"}[c.wantAuth], rec.Body.String())
			}
		})
	}
}

// TestIdentityPropagatesToHandler 鉴权命中的身份必须能被下游 handler 取到。
//
// 用量统计与请求日志的"是哪台设备发的"完全依赖这条链路：withAuth 写入 → 下游读取。
// 这里直接测这条连接，而不是等到统计层才发现身份丢了。
func TestIdentityPropagatesToHandler(t *testing.T) {
	h := gatewayWithKeys(t)

	cases := []struct {
		authz     string
		wantID    string
		wantAdmin bool
	}{
		{"Bearer sk-admin", httpauth.AdminID, true},
		{"Bearer sk-tv", "k_tv", false},
	}
	for _, c := range cases {
		var got httpauth.Identity
		called := false
		wrapped := h.withAuth(func(w http.ResponseWriter, r *http.Request) {
			got = identityFrom(r)
			called = true
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest("GET", "/probe", nil)
		req.Header.Set("Authorization", c.authz)
		wrapped(httptest.NewRecorder(), req)

		if !called {
			t.Fatalf("authz=%q: 下游未被调用（鉴权层拒绝了合法密钥）", c.authz)
		}
		if got.ID != c.wantID || got.Admin != c.wantAdmin {
			t.Errorf("authz=%q: 身份 = %+v, want id=%q admin=%v", c.authz, got, c.wantID, c.wantAdmin)
		}
	}
}

// TestIdentityAbsentWithoutAuth 未经过鉴权层时取到零值身份，
// 而不是被误当成管理密钥（那会把"未鉴权"的流量记到管理密钥名下）。
func TestIdentityAbsentWithoutAuth(t *testing.T) {
	if got := identityFrom(httptest.NewRequest("GET", "/probe", nil)); got.ID != "" || got.Admin {
		t.Errorf("未经鉴权的请求不该有身份，得到 %+v", got)
	}
	if got := identityFrom(nil); got.ID != "" || got.Admin {
		t.Errorf("nil 请求应得到零值身份，得到 %+v", got)
	}
}

// ------------------------------------------------------------------ 用量归属 ----

// attributedHandler 构造一个「管理密钥 + 客户端密钥」且**同时开启用量与归档**的网关，
// 用来验证整条归属链路：鉴权命中 → 写入请求上下文 → 用量/日志标注来源。
func attributedHandler(t *testing.T) (*Handler, *usage.Recorder, *reqlog.Recorder) {
	t.Helper()
	resetDynamicModelsCache()
	t.Cleanup(resetDynamicModelsCache)

	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	rec := usage.New("") // 纯内存，不落盘
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:   up,
		Usage:      rec,
		RequestLog: reqLog,
		APIKey:     clientKeyAdminKey, // 静态回落路径也要给出全部凭据
		Live: livecfg.New(livecfg.Snapshot{
			APIKey: clientKeyAdminKey,
			Creds:  attributedCreds(),
		}),
	})
	return h, rec, reqLog
}

const (
	clientKeyAdminKey = "sk-admin"
	clientKeyDevice   = "sk-tv"
	clientKeyTVName   = "客厅电视"
)

func attributedCreds() []httpauth.Credential {
	return []httpauth.Credential{
		{ID: httpauth.AdminID, Name: httpauth.AdminName, Key: clientKeyAdminKey, Admin: true},
		{ID: "k_tv", Name: clientKeyTVName, Key: clientKeyDevice},
	}
}

// chatOnce 发一次对话请求（用量与归档的唯一记账口径）。
func chatOnce(h *Handler, key string) {
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
}

// byKeyRow 从用量快照里找一行按密钥聚合的结果（找不到返回 false）。
func byKeyRow(s usage.Snapshot, id string) (usage.KeyedAgg, bool) {
	for _, r := range s.ByKey {
		if r.Key == id {
			return r, true
		}
	}
	return usage.KeyedAgg{}, false
}

// TestUsageAttributedByKey 用量必须按「哪个密钥发起的」分开统计。
//
// 这是分发密钥之后唯一有意义的运维问题：哪台设备用了多少、是不是它在烧积分。
// 只按账号统计的话，所有设备混成一个数字。
func TestUsageAttributedByKey(t *testing.T) {
	h, rec, _ := attributedHandler(t)

	chatOnce(h, clientKeyDevice)   // 客户端密钥 2 次
	chatOnce(h, clientKeyDevice)
	chatOnce(h, clientKeyAdminKey) // 管理密钥 1 次

	snap := rec.Snapshot(24, nil)

	tv, ok := byKeyRow(snap, "k_tv")
	if !ok {
		t.Fatalf("用量里没有客户端密钥那一行：%+v", snap.ByKey)
	}
	if tv.Requests != 2 {
		t.Errorf("客户端密钥请求数 = %d, want 2", tv.Requests)
	}
	// 名字必须跟着出现（面板上要显示"客厅电视"，不是"k_tv"）。
	if tv.Extra != clientKeyTVName {
		t.Errorf("客户端密钥那行的名字 = %q, want %q", tv.Extra, clientKeyTVName)
	}

	ad, ok := byKeyRow(snap, httpauth.AdminID)
	if !ok {
		t.Fatalf("用量里没有管理密钥那一行：%+v", snap.ByKey)
	}
	if ad.Requests != 1 {
		t.Errorf("管理密钥请求数 = %d, want 1（两个密钥不能混成一个数字）", ad.Requests)
	}
	if ad.Extra != httpauth.AdminName {
		t.Errorf("管理密钥那行的名字 = %q, want %q", ad.Extra, httpauth.AdminName)
	}

	// 总量应当是 3：归属维度没有漏计或重复计。
	if snap.Totals.Requests != 3 {
		t.Errorf("总请求数 = %d, want 3", snap.Totals.Requests)
	}
}

// TestUsageNeverStoresKeyValue 用量与归档里**绝不能出现密钥原文**。
//
// 需求只要求"保留名字"，而这里更进一步断言密钥值不存在：密钥是可用凭据，
// 一旦落进 usage.json 或日志文件，就等于把凭据散布到了磁盘上（归档还会被面板读取）。
func TestUsageNeverStoresKeyValue(t *testing.T) {
	h, rec, reqLog := attributedHandler(t)
	chatOnce(h, clientKeyDevice)

	blob, err := json.Marshal(rec.Snapshot(24, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), clientKeyDevice) {
		t.Error("用量快照里出现了密钥原文")
	}

	events, err := json.Marshal(reqLog.Snapshot().Recent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(events), clientKeyDevice) {
		t.Error("请求日志里出现了密钥原文")
	}
	if strings.Contains(string(events), clientKeyAdminKey) {
		t.Error("请求日志里出现了管理密钥原文")
	}
	// 反面确认：名字确实在（否则上面两条会因为整个字段缺失而假通过）。
	if !strings.Contains(string(events), clientKeyTVName) {
		t.Errorf("请求日志里应带上密钥名字 %q：%s", clientKeyTVName, events)
	}
}

// TestRequestLogCarriesKey 归档记录里要能看出请求来自哪个密钥（可按密钥筛日志）。
func TestRequestLogCarriesKey(t *testing.T) {
	h, _, reqLog := attributedHandler(t)
	chatOnce(h, clientKeyDevice)

	recent := reqLog.Snapshot().Recent
	if len(recent) != 1 {
		t.Fatalf("归档记录数 = %d, want 1", len(recent))
	}
	e := recent[0]
	if e.KeyID != "k_tv" || e.KeyName != clientKeyTVName {
		t.Errorf("归档记录的密钥归属 = %q/%q, want k_tv/%q", e.KeyID, e.KeyName, clientKeyTVName)
	}
}

// TestUntrustedUsageNotFiledUnderAdmin 未启用鉴权时（api_key 为空）的流量
// 不能记到管理密钥名下。
//
// 否则「管理密钥用了多少」会在你为了排查而临时关掉鉴权的窗口里凭空变大 ——
// 而那正是你在看这个数字的时候。
func TestUntrustedUsageNotFiledUnderAdmin(t *testing.T) {
	resetDynamicModelsCache()
	t.Cleanup(resetDynamicModelsCache)

	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	rec := usage.New("")
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
		Usage:    rec,
		// 不给凭据：api_key 为空 = 网关不鉴权。
	})

	chatOnce(h, "")
	snap := rec.Snapshot(24, nil)

	if _, ok := byKeyRow(snap, httpauth.AdminID); ok {
		t.Error("未鉴权的流量被记到了管理密钥名下")
	}
	unknown, ok := byKeyRow(snap, "")
	if !ok {
		t.Fatalf("未鉴权的流量应有一行「未标注」：%+v", snap.ByKey)
	}
	if unknown.Requests != 1 {
		t.Errorf("未标注的请求数 = %d, want 1", unknown.Requests)
	}
}
