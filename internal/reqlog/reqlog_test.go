package reqlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderMetricsAndRecent(t *testing.T) {
	r := New(Config{})
	r.Begin()
	r.Record(Event{RequestID: "ok", Status: 200, OK: true, Outcome: OutcomeSuccess, DurationMs: 10})
	r.Begin()
	r.Record(Event{RequestID: "bad", Status: 429, OK: false, Outcome: OutcomeHTTPError, DurationMs: 30})

	s := r.Snapshot()
	if s.Completed != 2 || s.InFlight != 0 || s.Succeeded != 1 || s.Failed != 1 {
		t.Fatalf("counts = %+v", s)
	}
	if s.SuccessRate != 50 || s.HTTPSuccessRate != 50 || s.AvgDurationMs != 20 {
		t.Fatalf("rates = success:%v http:%v avg:%v", s.SuccessRate, s.HTTPSuccessRate, s.AvgDurationMs)
	}
	if len(s.Recent) != 2 || s.Recent[0].RequestID != "bad" || s.Recent[1].RequestID != "ok" {
		t.Fatalf("recent = %+v, want newest first", s.Recent)
	}
}

func TestArchiveRotationReadAndFilter(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, FileMaxBytes: 120, MaxBytes: 1 << 20, RetentionDays: 7})
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 12; i++ {
		r.Record(Event{
			Time:       base.Add(time.Duration(i) * time.Second),
			RequestID:  "multi-" + string(rune('a'+i)),
			Model:      "glm-5.3",
			Account:    "账号(uid8)",
			Status:     200,
			OK:         true,
			Outcome:    OutcomeSuccess,
			DurationMs: int64(i + 1),
		})
	}
	r.Record(Event{
		Time:       base.Add(20 * time.Second),
		RequestID:  "other",
		Model:      "other-model",
		Status:     500,
		Outcome:    OutcomeHTTPError,
		DurationMs: 99,
	})
	r.Close()

	stats := r.Snapshot().Archive
	if !stats.Enabled || stats.Files < 2 || stats.Bytes == 0 || stats.DroppedWrites != 0 {
		t.Fatalf("archive stats = %+v", stats)
	}
	rows, err := r.ReadArchive(5, Filter{Model: "glm"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[0].RequestID != "multi-l" || rows[4].RequestID != "multi-h" {
		t.Fatalf("filtered rows = %+v", rows)
	}
}

func TestArchiveQueueDropCounter(t *testing.T) {
	w := &archiveWriter{
		cfg:  Config{Enabled: true, Dir: t.TempDir()},
		ch:   make(chan Event, 1),
		done: make(chan struct{}),
	}
	w.ch <- Event{RequestID: "occupied"}
	w.enqueue(Event{RequestID: "dropped"})
	if got := w.dropped.Load(); got != 1 {
		t.Fatalf("dropped=%d want 1", got)
	}
}

func TestArchivePruneHonorsSize(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"requests-2026-09-20.jsonl", "requests-2026-09-21.jsonl", "requests-2026-09-22.jsonl"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().AddDate(0, 0, -10+i)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	w := &archiveWriter{cfg: Config{Enabled: true, Dir: dir, RetentionDays: 30, MaxBytes: 50}, done: make(chan struct{})}
	w.prune()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "requests-2026-09-22.jsonl" {
		t.Fatalf("remaining = %+v, want newest file only", entries)
	}
}

// TestArchiveFilterByKey 按密钥过滤归档：既能按 id 也能按名字匹配。
//
// 「这台设备用了多少、发了什么」是拿到密钥分发出去之后第一个要回答的问题，面板的
// /panel/api/request_logs 因此暴露 ?key=<id 或名字>：用户看到/记住的是名字，
// 而归档里稳定的标识是 id，两种写法都必须能用。
func TestArchiveFilterByKey(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
	base := time.Now().Add(-time.Minute)
	for i, e := range []Event{
		{RequestID: "a", KeyID: "k_aaa", KeyName: "客厅电视"},
		{RequestID: "b", KeyID: "k_bbb", KeyName: "卧室平板"},
		{RequestID: "c", KeyID: "admin", KeyName: "管理密钥"},
		{RequestID: "d"}, // 未鉴权流量 / 升级前的历史数据：没有密钥归属
	} {
		e.Time = base.Add(time.Duration(i) * time.Second)
		e.Model = "glm-5.3"
		e.Status = 200
		e.OK = true
		e.Outcome = OutcomeSuccess
		r.Record(e)
	}
	r.Close()

	ids := func(filter Filter) []string {
		rows, err := r.ReadArchive(10, filter)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.RequestID)
		}
		return out
	}

	for _, tc := range []struct{ name, key, want string }{
		{"按名字", "客厅电视", "a"},
		{"按 id", "k_bbb", "b"},
		{"管理密钥也可以筛", "管理密钥", "c"},
		{"管理密钥按 id 筛", "admin", "c"},
	} {
		got := ids(Filter{Key: tc.key})
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s（key=%q）= %v，期望只有 %s", tc.name, tc.key, got, tc.want)
		}
	}

	// 关键：没有密钥归属的记录**不会**被任何密钥筛出来。
	// 否则「某台设备用了多少」会把升级前/未鉴权的历史流量算进去，数字虚高且无从对账。
	for _, key := range []string{"客厅电视", "卧室平板", "管理密钥", "admin", "k_"} {
		for _, id := range ids(Filter{Key: key}) {
			if id == "d" {
				t.Errorf("key=%q 筛出了无密钥归属的记录 d", key)
			}
		}
	}
}
