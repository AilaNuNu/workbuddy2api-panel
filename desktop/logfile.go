// logfile.go 桌面模式的落盘日志。
//
// 为什么必须有：Windows 里把程序编成 GUI 子系统（-H windowsgui）就没有控制台，
// 标准输出/标准错误直接进黑洞。这个项目的全部诊断信息（启动配置、账号加载、
// 调度结果、chat 请求表格日志）原本都只走 stdout/stderr——不落盘的话，用户报
// 「不工作」时没有任何可查的证据。
//
// 落盘策略：单文件 + 超过上限就截断重来。不做多文件轮转：日志是排查用的，
// 最近几 MB 足够覆盖一次「刚刚出的问题」，保留一堆历史文件只会让用户困惑
// 该发哪个。上限内的历史不会丢，超限只会丢掉最旧的部分。
package main

import (
	"fmt"
	"os"
	"sync"
)

// logMaxBytes 单个日志文件上限（8 MiB ≈ 数万个请求的表格日志）。
const logMaxBytes = 8 << 20

// rotatingLogWriter 到上限即截断重写的 writer（并发安全）。
type rotatingLogWriter struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
	// truncationNotice 截断后写入的首行提示（用一次后清空）。
	notice string
}

// newRotatingLogWriter 打开（或创建）日志文件，续写现有内容。
// 打开失败返回 error：桌面壳据此提示用户「日志不可写」而不是静默丢日志。
func newRotatingLogWriter(path string) (*rotatingLogWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingLogWriter{path: path, f: f, size: st.Size()}, nil
}

func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return len(p), nil // 已关闭：吞掉写入不报错，避免停机期刷屏
	}
	if w.size+int64(len(p)) > logMaxBytes {
		if err := w.truncateLocked(); err != nil {
			// 截断失败不阻断写入：继续追加，总比丢日志好。
			w.notice = ""
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// truncateLocked 关闭并重建文件，写入一行截断提示。调用方需持锁。
func (w *rotatingLogWriter) truncateLocked() error {
	_ = w.f.Close()
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		// 重建失败：尽力把原句柄恢复成追加模式，避免整个 writer 失效。
		if reopened, rerr := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); rerr == nil {
			w.f = reopened
		} else {
			w.f = nil
		}
		return err
	}
	w.f = f
	w.size = 0
	msg := fmt.Sprintf("===== 日志超过 %d MiB，已截断重写（旧内容已丢弃）=====\n", logMaxBytes>>20)
	if n, werr := f.WriteString(msg); werr == nil {
		w.size = int64(n)
	}
	return nil
}

// Close 关闭文件（幂等）。
func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Path 日志文件路径（透出到 UI/提示）。
func (w *rotatingLogWriter) Path() string { return w.path }
