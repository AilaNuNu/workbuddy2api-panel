package livecfg

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

// TestStoreCopiesCreds 快照必须拥有自己的凭据数组。
//
// 快照的全部意义是"不可变 + 原子替换"：读方 Load 拿到一致视图。若 Store 直接持有
// 调用方的切片，调用方之后改原数组（或复用同一个底层数组构造下一个快照）就会绕过
// 原子替换 —— 读方看到的还是旧快照的地址，内容却被改了。鉴权用的正是这份列表。
func TestStoreCopiesCreds(t *testing.T) {
	creds := []httpauth.Credential{
		{ID: httpauth.AdminID, Key: "sk-admin", Admin: true},
		{ID: "k_tv", Key: "sk-tv"},
	}
	h := New(Snapshot{APIKey: "sk-admin", Creds: creds})

	// 篡改调用方手里的原数组：不得影响已存快照。
	creds[0].Key = "sk-TAMPERED"
	creds[1].Admin = true

	got := h.Load().Creds
	if got[0].Key != "sk-admin" {
		t.Errorf("快照内容被调用方后续修改污染: %q", got[0].Key)
	}
	if got[1].Admin {
		t.Error("快照内容被调用方后续修改污染（Admin 被改写）")
	}
}

// TestLoadAfterNilHolder 未初始化的 Holder 也要能安全 Load（调用方无需判空）。
func TestLoadAfterNilHolder(t *testing.T) {
	var h *Holder
	s := h.Load()
	if s.AuthRequired() {
		t.Error("nil Holder 应得到零值快照（未启用鉴权）")
	}
	if len(s.Creds) != 0 {
		t.Errorf("nil Holder 的 Creds 应为空，得到 %+v", s.Creds)
	}
}

// TestAuthRequired 判据是"有没有可用凭据"，而不是某个字段非空。
func TestAuthRequired(t *testing.T) {
	if (Snapshot{}).AuthRequired() {
		t.Error("空快照不应要求鉴权")
	}
	if (Snapshot{Creds: httpauth.AdminCred("sk-admin")}).AuthRequired() != true {
		t.Error("有凭据时应当要求鉴权")
	}
	// 只有空 Key 的凭据不构成鉴权（对应"未配置任何密钥"）。
	if (Snapshot{Creds: []httpauth.Credential{{ID: "x", Key: ""}}}).AuthRequired() {
		t.Error("只有空 Key 的凭据不应算作启用了鉴权")
	}
}

// TestStoreReplacesAtomically 整体替换语义：新快照完全取代旧快照，
// 不会残留上一次的凭据（残留 = 已删除的密钥仍然有效）。
func TestStoreReplacesAtomically(t *testing.T) {
	h := New(Snapshot{
		APIKey: "sk-admin",
		Creds: []httpauth.Credential{
			{ID: httpauth.AdminID, Key: "sk-admin", Admin: true},
			{ID: "k_tv", Key: "sk-tv"},
		},
	})
	if len(h.Load().Creds) != 2 {
		t.Fatal("初始快照应为 2 条凭据")
	}

	// 模拟面板删掉客户端密钥后重新 Store。
	h.Store(Snapshot{APIKey: "sk-admin", Creds: httpauth.AdminCred("sk-admin")})

	got := h.Load().Creds
	if len(got) != 1 || got[0].Key != "sk-admin" {
		t.Errorf("被删除的密钥不应残留: %+v", got)
	}
}
