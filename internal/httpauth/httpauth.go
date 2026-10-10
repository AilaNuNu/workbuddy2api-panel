// Package httpauth 网关与面板共用的 Bearer 鉴权原语。
//
// 单独成包的原因：server（/v1/*、/status）与 panel（/panel/api/*）两处鉴权
// 必须完全同口径——此前各自复制了一份"字符串直接比较"的实现，既容易漂移，
// 又都带计时侧信道。统一到这里后，口径只有一份，且天然常量时间比较。
package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerPrefix 认证方案前缀（大小写敏感，与 HTTP 规范及既有实现一致）。
const bearerPrefix = "Bearer "

// AdminID 管理密钥的归属标识（用量统计与请求日志里用它表示"管理密钥"）。
// 客户端密钥用各自的 ClientKey.ID；两者共用一个命名空间，因此 ClientKey.ID 不应取
// 这个值（见 appcore.normalizeClientKeys 的重名检查）。
const AdminID = "admin"

// AdminName 管理密钥的展示名（用量统计与请求日志里给人看的那一列）。
// 与 AdminID 一样在这里定义：它会被**冗余写进用量桶**，因此必须是稳定字面量，
// 不能由各调用点随手拼（否则同一个管理密钥在不同桶里名字不同，面板上出现两行）。
const AdminName = "管理密钥"

// Credential 一个可比对的凭据。
type Credential struct {
	// ID 归属标识：写入用量统计与请求日志，用来回答"这条请求是哪台设备发的"。
	ID string
	// Name 展示名（"客厅电视"/"管理密钥"）。随 ID 一起写进用量桶与请求日志；
	// 密钥被删除之后，历史统计仍然靠它显示"当时是哪台设备"。
	Name string
	// Key 密钥原文。空值的凭据会被忽略（空字符串不构成凭据）。
	Key string
	// Admin true = 管理密钥，可访问管理面板（/panel/api/*）。
	Admin bool
}

// Identity 命中的凭据身份。
type Identity struct {
	ID    string
	Name  string
	Admin bool
}

// Authenticate 在 creds 中查找与请求头匹配的凭据，返回命中的身份与是否通过。
//
// 语义：
//   - creds 中没有任何非空密钥 → "未启用鉴权"，恒通过（保持"api_key 为空即放行"的旧语义）。
//     返回的身份是**空的**（ID/Name 为空、Admin=true）：放行由 Admin 表达，而 ID 留空
//     让统计与日志能区分"没人鉴权"和"管理密钥在用"。
//   - 否则必须命中某个凭据才通过；返回的是**命中的那一个**的身份。
//
// 为什么分级要在这里（而不是两处调用方各写一套判断）：客户端密钥与管理密钥的差异只有
// "能不能过 /panel/api/*"，判定依据必须是"命中的是哪一类凭据"。放在这里，两处调用方
// 各自只做一次调用，分级口径天然一致。
//
// 实现上刻意**不短路**：无论命中第一个、最后一个还是一个都没命中，都要把全部凭据比完
// 一遍；Authorization 头缺失或方案不对时也走完整轮比较。短路会让响应时间泄露
// "命中在第几个位置""集合里有没有这个前缀"，把猜密钥变成可测量的问题。
func Authenticate(r *http.Request, creds []Credential) (Identity, bool) {
	usable := 0
	for _, c := range creds {
		if c.Key != "" {
			usable++
		}
	}
	if usable == 0 {
		// 未启用鉴权：放行，但身份是"**没有身份**"（ID/Name 留空）。
		//
		// 刻意不复用 AdminID：用量统计与请求日志要靠 ID 回答"哪台设备发的"，
		// 这里若回 admin，"临时关掉鉴权"期间的所有流量都会挂到管理密钥名下 ——
		// 而那正是有人为了排查问题才关鉴权的时候，数字会误导人。
		// 放行与否只看 Admin（面板据此判定），与 ID 无关。
		return Identity{Admin: true}, true
	}

	// 头缺失/方案不对 → tok 为空串，仍参与比较（对空摘要比一轮），不提前返回。
	tok := ""
	if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, bearerPrefix) {
		tok = authz[len(bearerPrefix):]
	}
	tokDigest := digest(tok)

	// matched 累积"是否命中"，idx 记录命中下标（未命中保持 -1），adminHit 累积"命中的是不是管理密钥"。
	// idx 用算术选择而不是 if：分支会把"第几个命中"的差异留在执行路径上。
	matched := 0
	idx := -1
	adminHit := 0
	for i, c := range creds {
		// 这两个分支只依赖凭据自身的属性（是否为空、是否管理密钥），与请求内容无关，
		// 每次请求走的分支完全相同，因此不构成侧信道。
		if c.Key == "" {
			continue
		}
		hit := subtle.ConstantTimeCompare(tokDigest, digest(c.Key))
		idx = i*hit + idx*(1-hit)
		if c.Admin {
			adminHit |= hit
		}
		matched |= hit
	}
	if matched == 0 {
		return Identity{}, false
	}
	// 走到这里比较已全部完成，取值不再影响耗时形状。
	if idx < 0 || idx >= len(creds) {
		// 逻辑上不可达（matched==1 必然写过 idx）；保守拒绝而不是放行。
		return Identity{}, false
	}
	return Identity{ID: creds[idx].ID, Name: creds[idx].Name, Admin: adminHit == 1}, true
}

// AdminCred 单个管理密钥的凭据集合（key 为空时返回 nil = 不鉴权）。
//
// 给"只有管理密钥、没有分级密钥"的调用方用（未提供 Live 快照的嵌入方与测试），
// 让"怎么把管理密钥变成凭据"这条规则只写一次，避免各处自造 Credential 时漏掉 Admin 标记。
func AdminCred(key string) []Credential {
	if key == "" {
		return nil
	}
	return []Credential{{ID: AdminID, Name: AdminName, Key: key, Admin: true}}
}

// VerifyBearer 校验请求头是否携带正确的单个密钥（兼容旧调用方的便捷形式）。
//
// key 为空表示"未启用鉴权"，恒返回 true。新代码应直接用 Authenticate 拿命中身份。
func VerifyBearer(r *http.Request, key string) bool {
	if key == "" {
		return true
	}
	_, ok := Authenticate(r, []Credential{{ID: AdminID, Key: key, Admin: true}})
	return ok
}

// digest 返回 s 的 SHA-256（定长 32 字节，供常量时间比较）。
//
// 为什么先摘要再比较，而不是直接 subtle.ConstantTimeCompare([]byte(a), []byte(b))：
//   - 直接比较要求两侧等长，长度不同就得提前返回（或补齐），长度差异因此可测；
//   - 摘要把任意长度压成定长，长度差异被吸收掉；
//   - 摘要不可逆，即便存在侧信道也拿不到密钥原文。
func digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
