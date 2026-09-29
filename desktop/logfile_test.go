package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotatingLogWriterAppends 续写现有文件，不丢历史。
func TestRotatingLogWriterAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := newRotatingLogWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	got, _ := os.ReadFile(path)
	if string(got) != "old\nnew\n" {
		t.Fatalf("内容 = %q", got)
	}
}

// TestRotatingLogWriterTruncatesAtLimit 到上限即截断重写：保留最近的日志，
// 且截断本身不报错（不能因为日志太大就把业务写日志的路径打挂）。
func TestRotatingLogWriterTruncatesAtLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.log")
	w, err := newRotatingLogWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// 直接写超限内容（绕过逐行累计，快）。
	chunk := strings.Repeat("x", 1<<20) // 1 MiB
	for i := 0; i < (logMaxBytes>>20)+2; i++ {
		if _, err := w.Write([]byte(chunk + "\n")); err != nil {
			t.Fatalf("第 %d 次写入失败: %v", i, err)
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > logMaxBytes {
		t.Fatalf("文件未截断: %d 字节 > 上限 %d", st.Size(), logMaxBytes)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "已截断重写") {
		t.Fatal("应有截断提示行，便于用户知道日志不连续")
	}
}

// TestRotatingLogWriterWriteAfterClose 关闭后写入不报错（停机期日志不该刷屏）。
func TestRotatingLogWriterWriteAfterClose(t *testing.T) {
	dir := t.TempDir()
	w, err := newRotatingLogWriter(filepath.Join(dir, "c.log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("after close")); err != nil || n != len("after close") {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err := w.Close(); err != nil { // 幂等
		t.Fatalf("二次 Close: %v", err)
	}
}

// TestRotatingLogWriterOpenFailure 数据目录不可写时构造失败（调用方据此提示用户，
// 而不是静默把日志丢进黑洞）。
func TestRotatingLogWriterOpenFailure(t *testing.T) {
	dir := t.TempDir()
	// 用一个目录当文件路径 → OpenFile 必然失败。
	if _, err := newRotatingLogWriter(dir); err == nil {
		t.Fatal("应返回错误")
	}
}
