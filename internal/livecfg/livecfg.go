// Package livecfg 运行期可变配置的并发安全持有者。
//
// 背景：进程启动时读入的配置是普通字段（读多写零），但管理面板允许在线改配置，
// 于是少量"可热生效"的字段需要有并发安全的读写点。此处用不可变快照 + atomic 指针：
// 读方 Load 拿到一致视图，写方 Store 整体替换，无锁无数据竞争。
//
// 只承载**读路径深、热改需求强**的少数字段；池参数/排程参数等各有既有 setter
// （pool.SetBreaker、scheduler.Reconfigure 等），不重复收编到这里。
package livecfg

import (
	"sync/atomic"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

// Snapshot 一次读取的不可变配置视图。
type Snapshot struct {
	APIKey string // 管理密钥原文；空 = 不鉴权（保留给"面板密钥门要不要显示"等展示判断）
	// Creds 全部可比对的凭据：管理密钥在前，随后是**已启用**的客户端密钥。
	//
	// 存展开后的凭据列表而不是原始的 []ClientKey，是因为鉴权发生在每个请求上：让
	// 鉴权侧直接可用，避免每请求都重新过滤一遍「启用与否」。过滤只在这里做一次。
	Creds                []httpauth.Credential
	SoftCooldown         time.Duration // 429 软冷却基数（<=0 时调用方回退内置默认）
	SanitizeFingerprints bool          // 出站请求体指纹脱敏
}

// AuthRequired 是否启用了鉴权（存在任一可用凭据）。
//
// 判据必须与 Authenticate 完全一致：**存在 Key 非空的凭据**。
// 若只看 len(Creds) > 0，一条 Key 为空的凭据就会让面板弹出密钥门，而鉴权层认为
// "没有可用凭据 = 不鉴权"从而放行 —— 用户被要求填一个根本不会被校验的密钥，
// 同时真正拦人的那道门看起来是关着的（前端与实际行为相反）。两者必须同步演进。
func (s Snapshot) AuthRequired() bool {
	for _, c := range s.Creds {
		if c.Key != "" {
			return true
		}
	}
	return false
}

// Holder 原子持有当前快照。
type Holder struct {
	p atomic.Pointer[Snapshot]
}

// New 以初始快照构建。
func New(s Snapshot) *Holder {
	h := &Holder{}
	h.Store(s)
	return h
}

// Load 返回当前快照（Holder 为 nil 或从未 Store 时返回零值快照，调用方无需判空）。
func (h *Holder) Load() Snapshot {
	if h == nil {
		return Snapshot{}
	}
	if s := h.p.Load(); s != nil {
		return *s
	}
	return Snapshot{}
}

// Store 整体替换快照。
//
// Creds 会拷一份：快照承诺"不可变"，若直接持有调用方的切片，调用方之后改原数组
// （或复用同一个底层数组构造下一个快照）就会绕过原子替换，读方看到被改过的内容。
// 只读的使用方式下这一步是纯粹的保险，但它是"不可变"这个前提的兑现。
func (h *Holder) Store(s Snapshot) {
	if s.Creds != nil {
		cp := make([]httpauth.Credential, len(s.Creds))
		copy(cp, s.Creds)
		s.Creds = cp
	}
	h.p.Store(&s)
}
