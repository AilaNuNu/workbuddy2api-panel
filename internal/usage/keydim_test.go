package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// namesOf 取某个维度行的名字（Extra）。
func namesOf(rows []KeyedAgg, id string) string {
	for _, r := range rows {
		if r.Key == id {
			return r.Extra
		}
	}
	return "<缺失>"
}

func requestsOf(rows []KeyedAgg, id string) int64 {
	for _, r := range rows {
		if r.Key == id {
			return r.Requests
		}
	}
	return -1
}

// TestSnapshotGivesNamesPerKey 每个密钥一行，并带上冗余存下的名字。
func TestSnapshotGivesNamesPerKey(t *testing.T) {
	r := New("")
	now := time.Now()
	r.Add(now, "cn", "u1", "glm-5.2", Caller{ID: "k_tv", Name: "客厅电视"}, Delta{TotalTokens: 100, HasTotal: true}, true)
	r.Add(now, "cn", "u1", "glm-5.2", Caller{ID: "k_pc", Name: "办公室电脑"}, Delta{TotalTokens: 50, HasTotal: true}, true)

	snap := r.Snapshot(24, nil)
	if got := namesOf(snap.ByKey, "k_tv"); got != "客厅电视" {
		t.Errorf("k_tv 的名字 = %q, want 客厅电视", got)
	}
	if got := namesOf(snap.ByKey, "k_pc"); got != "办公室电脑" {
		t.Errorf("k_pc 的名字 = %q, want 办公室电脑", got)
	}
	// 同名不同 id 必须是两行：用名字当分组键会把它们并成一行，
	// 而「哪台用了多少」正是要看的。
	r.Add(now, "cn", "u1", "glm-5.2", Caller{ID: "k_pc2", Name: "客厅电视"}, Delta{TotalTokens: 10, HasTotal: true}, true)
	snap = r.Snapshot(24, nil)
	if got := requestsOf(snap.ByKey, "k_pc2"); got != 1 {
		t.Errorf("同名不同 id 的密钥被并成了一行（k_pc2 请求数 = %d, want 1）", got)
	}
}

// TestDeleteKeyKeepsHistoricalName 删掉密钥之后，历史统计仍然显示它当时的名字。
//
// 这正是"名字冗余存进桶"要解决的问题：删密钥是排查历史时会发生的事
// （"那台设备最近用得怎么样，要不要停掉"），此时若只剩一个查不到的 id，
// 历史行就全变成「未知密钥」，等于没有信息。
func TestDeleteKeyKeepsHistoricalName(t *testing.T) {
	r := New("")
	r.Add(time.Now(), "cn", "u1", "glm-5.2", Caller{ID: "k_gone", Name: "旧平板"}, Delta{TotalTokens: 9, HasTotal: true}, true)

	// 模拟"密钥被删除"：config 里不再有它，但桶里已经存了名字。
	snap := r.Snapshot(24, nil)
	if got := namesOf(snap.ByKey, "k_gone"); got != "旧平板" {
		t.Errorf("删除后历史名字丢失，得到 %q（应为桶里冗余存的那份）", got)
	}
}

// TestRollupKeepsKeyDimension 折叠成日桶时不能丢掉密钥维度。
//
// 这是最隐蔽的一种坏法：Rollup 的 day 桶键若漏掉 KeyID，两个密钥的日数据会
// 合并进同一个桶 —— 总量对，但"哪台用了多少"从那天起全错，且不报任何错。
func TestRollupKeepsKeyDimension(t *testing.T) {
	r := New("")
	old := time.Now().Add(-100 * 24 * time.Hour) // 超出 hourlyKeep（90 天）

	r.Add(old, "cn", "u", "m", Caller{ID: "k_a", Name: "设备A"}, Delta{TotalTokens: 10, HasTotal: true}, true)
	r.Add(old, "cn", "u", "m", Caller{ID: "k_b", Name: "设备B"}, Delta{TotalTokens: 20, HasTotal: true}, true)

	r.Rollup(time.Now())

	// 全历史视图（hours<=0）会包含折叠后的日桶。
	snap := r.Snapshot(0, nil)
	if got := requestsOf(snap.ByKey, "k_a"); got != 1 {
		t.Errorf("折叠后 k_a 请求数 = %d, want 1（日桶键里丢了密钥维度就会并成一个）", got)
	}
	if got := requestsOf(snap.ByKey, "k_b"); got != 1 {
		t.Errorf("折叠后 k_b 请求数 = %d, want 1", got)
	}
	if got := namesOf(snap.ByKey, "k_b"); got != "设备B" {
		t.Errorf("折叠后名字丢失: %q", got)
	}
	// 折叠是搬运不是重算：总量必须原样保留。
	if snap.Totals.Requests != 2 {
		t.Errorf("折叠后总请求数 = %d, want 2（折叠过程丢失或重复计数）", snap.Totals.Requests)
	}
	if snap.Totals.TotalTokens != 30 {
		t.Errorf("折叠后总 token = %d, want 30", snap.Totals.TotalTokens)
	}
	// 再折叠一次必须幂等（不重复计数）。
	r.Rollup(time.Now())
	if got := r.Snapshot(0, nil).Totals.TotalTokens; got != 30 {
		t.Errorf("重复折叠后总 token = %d, want 30（折叠不幂等）", got)
	}
}

// TestLoadKeepsKeyDimension 落盘往返不能丢密钥维度。
//
// load 若不按 KeyID 重建 map 键，重载时多个密钥的桶会互相覆盖 —— 表现为
// "重启一次，某些设备的统计就少了一半"。而 usage.json 是唯一的数据来源，
// 没有别处可以补回。
func TestLoadKeepsKeyDimension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")

	r := New(path)
	now := time.Now()
	r.Add(now, "cn", "u", "m", Caller{ID: "k_a", Name: "设备A"}, Delta{TotalTokens: 10, HasTotal: true}, true)
	r.Add(now, "cn", "u", "m", Caller{ID: "k_b", Name: "设备B"}, Delta{TotalTokens: 20, HasTotal: true}, true)
	r.Save() // 立即落盘

	// 模拟重启：同一路径重新构造。
	again := New(path)
	snap := again.Snapshot(24, nil)

	if got := requestsOf(snap.ByKey, "k_a"); got != 1 {
		t.Errorf("重载后 k_a 请求数 = %d, want 1（桶键漏了 KeyID 会互相覆盖）", got)
	}
	if got := requestsOf(snap.ByKey, "k_b"); got != 1 {
		t.Errorf("重载后 k_b 请求数 = %d, want 1", got)
	}
	if got := namesOf(snap.ByKey, "k_a"); got != "设备A" {
		t.Errorf("重载后名字丢失: %q", got)
	}
	if snap.Totals.TotalTokens != 30 {
		t.Errorf("重载后总 token = %d, want 30", snap.Totals.TotalTokens)
	}
}

// TestOldBucketsWithoutKeyLoad 旧版本（无密钥维度）的 usage.json 必须能正常加载，
// 归入「未标注」一行而不是报错或丢弃。
func TestOldBucketsWithoutKeyLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	// 版本 3 的落盘内容：没有 k / kn 字段。
	old := `{"version":3,"saved":"2026-01-01T00:00:00Z","buckets":[
		{"s":"h:2026-01-01T10","r":"cn","u":"u1","m":"glm-5.2","q":3,"t":300}]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	r := New(path)
	snap := r.Snapshot(0, nil)
	if got := requestsOf(snap.ByKey, ""); got != 3 {
		t.Errorf("旧桶应归入「未标注」（key 为空）一行，请求数 = %d, want 3", got)
	}
	if snap.Totals.Requests != 3 {
		t.Errorf("旧数据不该被丢弃：总请求数 = %d, want 3", snap.Totals.Requests)
	}
}
