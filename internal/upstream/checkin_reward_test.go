// checkin_reward_test.go 钉住「签到得分归因」的逻辑。
//
// 背景：签到日志原本只打「签到成功」，看不出本次得了多少分。上游 get-user-resource
// 的每个积分包带 CreateTime（epoch 毫秒）+ AccountAttributes 里的
// grantSource=daily_checkin 标记，据此可算出「本次签到新增了多少额度」。
//
// 覆盖三件容易写错的事：
//  1. 只统计来源匹配的批次（growth_travel 等不得混入）；
//  2. 时间窗口过滤（签到之前就存在的 checkin 批次不得算作「本次」）；
//  3. 缺少 CreateTime 的批次不得被当作「本次」（宁可漏报，不可错报）。
package upstream

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// acct 造一个上游 get-user-resource 风格的积分包 JSON。
func acct(createMs, size int64, grantSource string) string {
	attrs := `{"Key":"payerType","Value":"0","Type":5}`
	if grantSource != "" {
		attrs += `,{"Key":"grantSource","Value":"` + grantSource + `","Type":3}`
	}
	n := strconv.FormatInt
	return `{"PackageName":"赠送包","CycleEndTime":"2026-10-29 09:00:02",` +
		`"CapacitySize":` + n(size, 10) + `,"CapacityRemain":` + n(size, 10) + `,"CapacityUsed":0,` +
		`"CycleCapacitySize":` + n(size, 10) + `,"CycleCapacityRemain":` + n(size, 10) + `,"CycleCapacityUsed":0,` +
		`"CreateTime":` + n(createMs, 10) + `,"AccountAttributes":[` + attrs + `]}`
}

// resourceResp 包一层上游信封（code/data/Response/Data/Accounts）。
func resourceResp(accounts ...string) string {
	return `{"code":0,"msg":"OK","data":{"Response":{"Data":{"Accounts":[` +
		strings.Join(accounts, ",") + `]}}}}`
}

// TestGrantsParsesSourceAndCreatedAt 验证解析层：逐批次带出来源与发放时刻，
// 聚合值不受影响。
func TestGrantsParsesSourceAndCreatedAt(t *testing.T) {
	nowMs := time.Now().UnixMilli()
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v2/billing/meter/get-user-resource") {
			return nil, errString("wrong path: " + r.URL.Path)
		}
		return jsonResp(200, resourceResp(
			acct(nowMs, 100, "daily_checkin"),
			acct(nowMs-86400000, 50, "growth_travel"),
		)), nil
	})

	g, err := c.UserResourceDetailedGrants(&auth.Auth{AccessToken: "at"}, time.Hour)
	if err != nil {
		t.Fatalf("UserResourceDetailedGrants: %v", err)
	}
	if g.Remain != 150 {
		t.Errorf("Remain=%d want 150", g.Remain)
	}
	if len(g.Grants) != 2 {
		t.Fatalf("Grants 数量=%d want 2", len(g.Grants))
	}
	if g.Grants[0].GrantSource != "daily_checkin" {
		t.Errorf("Grants[0].GrantSource=%q want daily_checkin", g.Grants[0].GrantSource)
	}
	if g.Grants[1].GrantSource != "growth_travel" {
		t.Errorf("Grants[1].GrantSource=%q want growth_travel", g.Grants[1].GrantSource)
	}
	// epoch 毫秒 → 秒级还原（允许 1ms 内误差）
	if got := g.Grants[0].CreatedAt.UnixMilli(); got != nowMs {
		t.Errorf("CreatedAt=%d want %d", got, nowMs)
	}
	// 旧签名仍可用（委托）
	remain, total, _, _, _, err := c.UserResourceDetailedWithExpiry(&auth.Auth{AccessToken: "at"}, time.Hour)
	if err != nil {
		t.Fatalf("UserResourceDetailedWithExpiry: %v", err)
	}
	if remain != 150 || total != 150 {
		t.Errorf("委托版 remain=%d total=%d want 150/150", remain, total)
	}
}

// TestSumGrantsSinceFiltersSourceAndTime 是核心断言的位置：
// 只有「来源=daily_checkin 且发放时刻在本窗口内」的批次才计入本次得分。
func TestSumGrantsSinceFiltersSourceAndTime(t *testing.T) {
	base := time.Date(2026, 9, 30, 8, 32, 25, 0, time.Local) // 实测签到时刻
	grants := []CreditGrant{
		// 本次签到发放的
		{GrantSource: "daily_checkin", CreatedAt: base, CapacitySize: 100},
		// 同来源，但在签到之前（昨天的）—— 不得算作本次
		{GrantSource: "daily_checkin", CreatedAt: base.Add(-24 * time.Hour), CapacitySize: 77},
		// 同一时刻，但来源是旅行 —— 不得混入
		{GrantSource: "growth_travel", CreatedAt: base, CapacitySize: 9},
		// 来源匹配、时刻匹配，但缺 CreateTime（零值）—— 不得当作本次
		{GrantSource: "daily_checkin", CapacitySize: 55},
	}

	got, n := SumGrantsSince(grants, "daily_checkin", base)
	if got != 100 {
		t.Errorf("合计=%d want 100（只算本次那一个批次）", got)
	}
	if n != 1 {
		t.Errorf("批次数=%d want 1", n)
	}

	// 时钟偏差容差：发放时刻比 since 早 30 秒仍应计入（本机与上游时钟不同步）。
	near := []CreditGrant{
		{GrantSource: "daily_checkin", CreatedAt: base.Add(-30 * time.Second), CapacitySize: 100},
	}
	got, n = SumGrantsSince(near, "daily_checkin", base)
	if got != 100 || n != 1 {
		t.Errorf("容差内应计入：合计=%d n=%d want 100/1", got, n)
	}

	// 超出容差（早 5 分钟）不计入
	far := []CreditGrant{
		{GrantSource: "daily_checkin", CreatedAt: base.Add(-5 * time.Minute), CapacitySize: 100},
	}
	got, n = SumGrantsSince(far, "daily_checkin", base)
	if got != 0 || n != 0 {
		t.Errorf("超出容差不应计入：合计=%d n=%d want 0/0", got, n)
	}
}

// TestSumGrantsSinceNoGrantMeansZero 幂等路径（今天已签到、无新发放）应得 (0,0)，
// 调用方据此不宣称得分。
func TestSumGrantsSinceNoGrantMeansZero(t *testing.T) {
	now := time.Now()
	grants := []CreditGrant{
		{GrantSource: "daily_checkin", CreatedAt: now.Add(-30 * time.Hour), CapacitySize: 100},
	}
	got, n := SumGrantsSince(grants, "daily_checkin", now)
	if got != 0 || n != 0 {
		t.Errorf("无新发放应得 0/0，实际 %d/%d", got, n)
	}
}

// TestGrantsMissingCreateTimeIsVisible 缺 CreateTime 的批次仍出现在明细里
// （供人排查），只是不会被计成"本次"。
func TestGrantsMissingCreateTimeIsVisible(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, resourceResp(acct(0, 42, "daily_checkin"))), nil
	})
	g, err := c.UserResourceDetailedGrants(&auth.Auth{AccessToken: "at"}, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(g.Grants) != 1 {
		t.Fatalf("Grants=%d want 1", len(g.Grants))
	}
	if !g.Grants[0].CreatedAt.IsZero() {
		t.Errorf("缺 CreateTime 应得零值时刻，实际 %v", g.Grants[0].CreatedAt)
	}
	if _, n := SumGrantsSince(g.Grants, "daily_checkin", time.Now()); n != 0 {
		t.Errorf("零值时刻不得计入本次，n=%d", n)
	}
}

// errString 让测试里的 return nil, err 不用引 errors 包。
type errString string

func (e errString) Error() string { return string(e) }
