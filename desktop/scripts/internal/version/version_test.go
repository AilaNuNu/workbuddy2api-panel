package version

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseTag 守住 tag → 版本号的规范化。
//
// 这组用例的存在理由：仓库里同时有两套 tag（上游 `v1.11.9` 与桌面版
// `desktop-v1.11.10`），`git describe --tags` 谁近取谁。曾经只做 `TrimPrefix("v")`，
// 遇到 `desktop-v1.11.10` 会得到 `desktop-v1.11.10` 这个串，送进 NSIS 的
// VIProductVersion 会被拒（要求 4 段纯数字）——而且失败信息完全指不到这里。
func TestParseTag(t *testing.T) {
	cases := []struct {
		tag  string
		want string
		ok   bool
	}{
		{"v1.11.9", "1.11.9", true},                  // 上游 tag
		{"desktop-v1.11.10", "1.11.10", true},        // 桌面版 tag（带前缀）
		{"v1.11.10-desktop", "1.11.10", true},        // 桌面版 tag（带后缀）
		{"1.11.10", "1.11.10", true},                 // 裸版本号
		{"  v1.2.0\n", "1.2.0", true},                // 前后空白（git 输出带换行）
		{"v1.11.10.1", "1.11.10.1", true},            // 4 段
		{"", "", false},                              // 空
		{"v", "", false},                             // 只有前缀
		{"desktop", "", false},                       // 只有前缀、无版本
		{"nightly-build", "", false},                 // 名字里没有版本号
		{"v1", "", false},                            // 段数不够（至少 x.y）
		{"vabc", "", false},                          // 非数字
	}
	for _, c := range cases {
		got, err := parseTag(c.tag)
		if c.ok {
			if err != nil {
				t.Errorf("parseTag(%q) 意外报错: %v", c.tag, err)
				continue
			}
			if got != c.want {
				t.Errorf("parseTag(%q) = %q, 期望 %q", c.tag, got, c.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("parseTag(%q) 期望报错，却得到 %q", c.tag, got)
		}
	}
}

// TestSourceTrimSuffix 守住「源码 AppVersion 剥 -panel 后缀」这条路。
// 这个函数读的是仓库真实文件，所以用一个临时目录伪造仓库结构，避免依赖真实仓库状态。
func TestSourceTrimSuffix(t *testing.T) {
	root := t.TempDir()
	appcore := filepath.Join(root, "internal", "appcore")
	if err := os.MkdirAll(appcore, 0o755); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(root, "desktop")
	if err := os.MkdirAll(desktop, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{`const AppVersion = "1.11.10-panel"`, "1.11.10", true},
		{`const AppVersion = "1.11.10"`, "1.11.10", true},
		{"const AppVersion   =   \"2.0.0-panel\"", "2.0.0", true},
		{`var AppVersion = "1.0.0"`, "", false}, // 不是 const，不该匹配
		{`const OtherVersion = "9.9.9"`, "", false},
	}
	for _, c := range cases {
		src := "package appcore\n\n" + c.line + "\n"
		if err := os.WriteFile(filepath.Join(appcore, "runtime.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Source(desktop)
		if c.ok {
			if err != nil {
				t.Errorf("Source() 对 %q 报错: %v", c.line, err)
				continue
			}
			if got != c.want {
				t.Errorf("Source() 对 %q = %q, 期望 %q", c.line, got, c.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("Source() 对 %q 期望报错，却得到 %q", c.line, got)
		}
	}
}

// TestResolveFallsBackToTag 验证源码缺失时能退回 tag（GitTag 走真实 git，
// 临时目录不是仓库，所以这里期望最终落到 Fallback，而不是崩掉）。
func TestResolveFallsBackToTag(t *testing.T) {
	root := t.TempDir()
	desktop := filepath.Join(root, "desktop")
	if err := os.MkdirAll(desktop, 0o755); err != nil {
		t.Fatal(err)
	}
	got := Resolve(desktop)
	if got != Fallback {
		t.Errorf("源码与 tag 都取不到时应退回 Fallback=%q，实际 %q", Fallback, got)
	}
}
