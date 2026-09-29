package logfmt

import "io"

// Tee 把写入广播给所有 sink，且**某个 sink 失败不会影响其他 sink**。
//
// 与 io.MultiWriter 的关键区别：io.MultiWriter 的语义是「依序写，遇错即停」——
// 一旦前面的 sink 返回错误，后面的 sink 一个字节都收不到。
//
// 为什么必须换掉：
//
//	桌面版用 -H windowsgui 构建（GUI 子系统，无控制台），os.Stderr 的写入会失败。
//	若 os.Stderr 排在落盘 writer 之前，落盘日志就**完全拿不到启动后半段的任何内容**，
//	而应用本身运行正常（面板可访问）。同理，排在后面的面板环形缓冲也会一起空掉，
//	用户点「日志」页看到的是空白。表现是「程序好着但日志只有开头几行」，
//	而日志是 GUI 程序唯一的排查手段——这类静默失踪比崩溃更难查。
//
// 返回错误仍按 io.Writer 约定给出（第一个出错的 sink），但调用方是 log 包，
// 会忽略它；语义是「有 sink 写失败了」，而**不是**「全部失败」。
func Tee(sinks ...io.Writer) io.Writer {
	// 过滤 nil：调用方常从可选配置直接拼切片（如 entry.LogWriter 可为 nil），
	// 留一个 nil 进来会 panic 在 Write 上。
	live := make([]io.Writer, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			live = append(live, s)
		}
	}
	switch len(live) {
	case 0:
		return io.Discard
	case 1:
		// 单 sink 不加包装层，省一次间接调用，也让 == 比较仍能识别原 writer。
		return live[0]
	}
	return &teeWriter{sinks: live}
}

type teeWriter struct {
	// sinks 构造后只读，无锁即可并发写（各 sink 自己负责并发安全）。
	sinks []io.Writer
}

func (w *teeWriter) Write(p []byte) (int, error) {
	var firstErr error
	for _, s := range w.sinks {
		// 不用 break/short-circuit：一个 sink 坏了不能饿死其它 sink，
		// 这正是本类型存在的理由。
		if _, err := s.Write(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// 统一报告 len(p)：对调用方而言这份数据已经被我们全部接收（只是某些 sink 写失败），
	// 返回短计数会被上层当成部分写入，导致 log 包重复切割或丢行。
	return len(p), firstErr
}
