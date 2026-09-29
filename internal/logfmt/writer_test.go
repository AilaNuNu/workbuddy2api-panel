package logfmt

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// failingWriter 模拟「写入必定失败」的 sink——桌面版里就是这个角色的 os.Stderr
// （GUI 子系统无控制台）。
type failingWriter struct{ calls int }

func (w *failingWriter) Write(p []byte) (int, error) {
	w.calls++
	return 0, errors.New("write /dev/stderr: handle is invalid")
}

// recordingWriter 记录收到的内容。
type recordingWriter struct {
	buf   strings.Builder
	calls int
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.calls++
	w.buf.Write(p)
	return len(p), nil
}

// TestTeeSurvivesFailingSink 是本次 bug 的核心回归：
// 排在前面的 sink 写失败时，后面的 sink 必须照样收到数据。
//
// io.MultiWriter 在这种情况下会立刻返回，后面的 sink 一个字节都拿不到；
// 桌面版落盘日志就是这样整段消失的（启动后所有日志丢失，应用却运行正常）。
func TestTeeSurvivesFailingSink(t *testing.T) {
	bad := &failingWriter{}
	good := &recordingWriter{}

	w := Tee(bad, good)
	const msg = "workbuddy2api listening on 127.0.0.1:7863\n"
	n, err := w.Write([]byte(msg))

	if n != len(msg) {
		t.Errorf("Write 返回 %d，期望 %d（应整体收下，只报告 sink 出错）", n, len(msg))
	}
	if err == nil {
		t.Error("有 sink 失败时应返回错误（便于上层知晓）")
	}
	if good.buf.String() != msg {
		t.Errorf("坏 sink 之后的 good sink 没拿到数据：got %q, want %q", good.buf.String(), msg)
	}
	if good.calls != 1 {
		t.Errorf("good sink 调用次数 = %d，期望 1", good.calls)
	}
}

// TestTeeContrastWithMultiWriter 把「io.MultiWriter 会丢数据」这一事实钉在测试里。
// 如果哪天有人把它换回 MultiWriter，这个测试会连同上面那个一起失败。
func TestTeeContrastWithMultiWriter(t *testing.T) {
	bad := &failingWriter{}
	good := &recordingWriter{}

	_, _ = io.MultiWriter(bad, good).Write([]byte("lost\n"))
	if good.buf.Len() != 0 {
		t.Log("io.MultiWriter 行为已变（后续 sink 收到了数据），本测试的前提需重新确认")
	} else {
		t.Log("已确认：io.MultiWriter 在首个 sink 失败后不再写后续 sink —— 正是本 bug 的成因")
	}

	// 同样的输入，Tee 必须送达。
	good2 := &recordingWriter{}
	_, _ = Tee(bad, good2).Write([]byte("kept\n"))
	if good2.buf.String() != "kept\n" {
		t.Errorf("Tee 未送达：got %q, want %q", good2.buf.String(), "kept\n")
	}
}

// TestTeeAllSinksReceive 正常路径：所有 sink 都收到，且返回 nil error。
func TestTeeAllSinksReceive(t *testing.T) {
	a, b, c := &recordingWriter{}, &recordingWriter{}, &recordingWriter{}
	n, err := Tee(a, b, c).Write([]byte("hello"))
	if err != nil {
		t.Fatalf("无失败 sink 时不应报错：%v", err)
	}
	if n != 5 {
		t.Errorf("n = %d, want 5", n)
	}
	for i, w := range []*recordingWriter{a, b, c} {
		if w.buf.String() != "hello" {
			t.Errorf("sink %d 内容 = %q, want %q", i, w.buf.String(), "hello")
		}
	}
}

// TestTeeNilSinkSkipped 可选 sink 为 nil（如 entry.LogWriter 缺省）不应 panic。
func TestTeeNilSinkSkipped(t *testing.T) {
	good := &recordingWriter{}
	w := Tee(nil, good, nil)
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("nil sink 应被跳过而不是报错：%v", err)
	}
	if good.buf.String() != "x" {
		t.Errorf("非 nil sink 未收到数据：%q", good.buf.String())
	}
}

// TestTeeDegenerate 零/单 sink 的边界：零 sink 丢弃但不 panic，单 sink 直接返回自身。
func TestTeeDegenerate(t *testing.T) {
	if w := Tee(); w == nil {
		t.Fatal("零 sink 应返回 io.Discard 而非 nil")
	}
	if n, err := Tee().Write([]byte("dropped")); n != 7 || err != nil {
		t.Errorf("io.Discard：n=%d err=%v，期望 7, nil", n, err)
	}

	only := &recordingWriter{}
	if got := Tee(only); got != io.Writer(only) {
		t.Error("单 sink 应直接返回该 writer（不加包装层）")
	}
}

// TestTeeFirstErrorReported 多个 sink 都失败时，报告第一个错误。
func TestTeeFirstErrorReported(t *testing.T) {
	e1 := errors.New("first")
	e2 := errors.New("second")
	w := Tee(failWith(e1), failWith(e2))
	n, err := w.Write([]byte("abc"))
	if n != 3 {
		t.Errorf("n = %d, want 3", n)
	}
	if !errors.Is(err, e1) {
		t.Errorf("err = %v，期望第一个错误 %v", err, e1)
	}
}

type fixedErrWriter struct{ err error }

func (w *fixedErrWriter) Write([]byte) (int, error) { return 0, w.err }
func failWith(e error) io.Writer                    { return &fixedErrWriter{err: e} }
