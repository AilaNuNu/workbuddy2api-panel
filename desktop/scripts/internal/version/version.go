// Package version 统一「面板自报 / 安装包文件名 / NSIS 的 VIProductVersion /
// PE 资源段」四处的版本号来源。
//
// 为什么需要这个包：版本号散落在四处，手工同步迟早写歪（曾出现 .nsi 里硬编码成
// 旧值）。此前 build.go 与 package.go 各留了一份「取 git tag」的实现——两个
// package main 无法互相引用，同一条规则被复制两份，改一处漏一处只是时间问题。
// 放进共享包，规则只有一份。
package version

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Fallback 是所有来源都取不到时使用的兜底版本。
const Fallback = "1.11.6"

// appVersionRe 匹配 internal/appcore 里的 AppVersion 常量。
var appVersionRe = regexp.MustCompile(`const\s+AppVersion\s*=\s*"([^"]+)"`)

// Source 从仓库源码读出面板自报的版本号（"-panel" 之类的后缀会被去掉）。
//
// desktopDir 是 desktop 模块根，仓库根是它的上一级，面板源码固定在
// internal/appcore/runtime.go。
//
// 这是首选来源，因为 **git tag 会滞后于源码**：实测主分支的 AppVersion 已是
// 1.11.10-panel，而最新 tag 仍停在 v1.11.9——只按 tag 打包，会得到一个比面板
// 自己显示的版本还旧一档的安装包。AppVersion 是每次发版都会改的那一处，
// tag 因此退化成它的备选。
func Source(desktopDir string) (string, error) {
	path := filepath.Join(filepath.Dir(desktopDir), "internal", "appcore", "runtime.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m := appVersionRe.FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("%s 里没有 AppVersion 常量", path)
	}
	v := strings.TrimSpace(string(m[1]))
	if i := strings.Index(v, "-"); i > 0 {
		v = v[:i] // "1.11.10-panel" → "1.11.10"
	}
	if v == "" {
		return "", fmt.Errorf("%s 的 AppVersion 为空", path)
	}
	return v, nil
}

// verRe 从 tag 里抽取版本号本体。抽不出来就报错，不猜。
var verRe = regexp.MustCompile(`^\d+(?:\.\d+){1,3}$`)

// GitTag 取最近的 git tag 并抽出版本号，Source 失败时的备选。
//
// dir 显式传给 git 子进程：不传的话取的是本进程的 CWD，脚本从别处调起时会静默
// 落到错误的仓库上。
//
// 为什么要这么啰嗦地剥前缀：仓库里同时存在两套 tag —— 上游的 `v1.11.9`（服务端/CLI
// 版本线）和桌面版的 `desktop-v1.11.10`。`git describe --tags` 对两者一视同仁，
// 谁近取谁，所以它可能返回形如 `desktop-v1.11.10` 的串。天真的 `TrimPrefix("v")`
// 只对前者有效，遇到后者会得到 `desktop-v1.11.10` —— 这个值送进 NSIS 的
// `VIProductVersion` 会被直接拒绝（它要求 4 段纯数字），错误还很难反查。
// 这里改用「剥掉已知前缀 + 正则校验」：对不上就报错，由 Resolve 决定降级。
func GitTag(dir string) (string, error) {
	cmd := exec.Command("git", "describe", "--tags", "--abbrev=0")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return parseTag(string(out))
}

// parseTag 把 tag 名规范化为纯版本号（v1.11.9 → 1.11.9，
// desktop-v1.11.10 → 1.11.10，v1.11.10-desktop → 1.11.10）。
func parseTag(tag string) (string, error) {
	raw := strings.TrimSpace(tag)
	if raw == "" {
		return "", fmt.Errorf("git tag 为空")
	}
	// 先剥「desktop-v」再剥「v」：前者以 d 开头，顺序反了也剥不掉，但明确写两次
	// 比依赖单一 TrimPrefix 更能表达「两种前缀都存在」这个事实。
	v := strings.TrimPrefix(raw, "desktop-")
	v = strings.TrimPrefix(v, "v")
	// 后缀形态：v1.11.10-desktop → 只取前导版本号。
	if i := strings.Index(v, "-"); i > 0 {
		v = v[:i]
	}
	v = strings.TrimSpace(v)
	if !verRe.MatchString(v) {
		return "", fmt.Errorf("无法从 tag %q 解析出版本号（得到 %q）", raw, v)
	}
	return v, nil
}

// Resolve 按「源码 AppVersion → git tag → Fallback」取版本号，并打印实际用的来源。
func Resolve(desktopDir string) string {
	v, err := Source(desktopDir)
	if err == nil {
		fmt.Printf("→ 版本号取自源码 AppVersion：%s\n", v)
		return v
	}
	fmt.Printf("→ 未取到源码 AppVersion（%v），退回 git tag\n", err)

	v, err = GitTag(desktopDir)
	if err == nil {
		fmt.Printf("→ 版本号取自 git tag：%s\n", v)
		return v
	}
	fmt.Printf("→ 未取到 git tag（%v），用兜底版本 %s\n", err, Fallback)
	return Fallback
}
