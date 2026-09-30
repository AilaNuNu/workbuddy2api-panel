// build.go Windows 桌面版构建脚本。
//
//	go run ./scripts/build.go            # 构建桌面版（Windows）
//	go run ./scripts/build.go -debug     # 带控制台窗口（GUI 子系统会吞掉 stdout，调试用）
//
// 为什么需要脚本：桌面版的 ldflags 有两个必须同时给对的点，写错任何一个都会
// 产生「看起来正常但没法排查」的产物：
//
//  1. -H windowsgui —— 让 exe 走 GUI 子系统，双击不弹控制台黑窗。
//     代价是 stdout/stderr 进黑洞，所以程序侧的日志必须落盘（见 cmd logfile.go）。
//  2. -s -w —— 去掉符号表与 DWARF，体积约减半。
//
// 另外两点：
//
//  1. 托盘图标通过 go:embed 从 desktop/assets/tray.ico 读；但**资源管理器里看到的
//     exe 图标不在 go:embed 里**，它在 PE 资源段，由 wb2api-desktop.syso 提供。
//     本脚本在 go build 之前生成该 .syso（见 genWindowsResources）。
//     只换 assets/tray.ico 而不重新生成 .syso 的后果：托盘图标变了、exe 图标没变。
//  2. 版本信息（文件属性里的「产品版本」）同样来自 .syso，版本号取自 git tag。
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	appver "github.com/linguo2625469/workbuddy2api-panel/desktop/scripts/internal/version"
)

func main() {
	debug := flag.Bool("debug", false, "保留控制台窗口（不设 -H windowsgui），便于看日志")
	out := flag.String("o", "", "输出文件名（缺省 wb2api-desktop.exe）")
	skipRes := flag.Bool("skip-res", false, "跳过 Windows 资源（exe 图标/版本信息）生成")
	version := flag.String("version", "", "PE 资源里的版本号（缺省取源码 AppVersion，退回 git tag）")
	flag.Parse()

	// 定位 desktop 模块根：从当前目录向上找 go.mod。
	//
	// 不能用「CWD 的上一级」——本脚本既可能从 desktop/scripts 直接跑，
	// 也可能被 package.go 从 desktop/ 以 ./scripts/build.go 调起，
	// 两种 CWD 下「上一级」分别是正确的和错的（后者会指到仓库根）。
	here, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	desktopDir, err := findModuleRoot(here)
	if err != nil {
		fail(err)
	}

	// 资源必须在 go build 之前生成：.syso 是编译输入，后生成就等于这次没生效。
	if runtime.GOOS == "windows" && !*skipRes {
		ver := *version
		if ver == "" {
			ver = appver.Resolve(desktopDir)
		}
		genWindowsResources(desktopDir, exeVersion(ver))
	}

	name := *out
	if name == "" {
		name = "wb2api-desktop"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
	}
	// 相对路径按 desktop 模块根解析，而不是 CWD。
	//
	// 曾经直接 Join(desktopDir, name)：传 `-o rb1.exe` 时这个组合其实是对的，
	// 但传 `-o tmp/rb1.exe` 之类的相对路径会相对于 desktopDir 再拼一层；而真正
	// 的坑是「相对路径的语义随调用方 CWD 变」——package.go 从 desktop/ 调起时
	// 与手工从别处调起时落到不同位置。统一以模块根为基准，行为不再依赖 CWD。
	outPath := name
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(desktopDir, outPath)
	}

	ldflags := "-s -w"
	if runtime.GOOS == "windows" && !*debug {
		ldflags += " -H windowsgui"
	}

	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", outPath, ".")
	cmd.Dir = desktopDir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fail(fmt.Errorf("构建失败: %w", err))
	}

	st, err := os.Stat(outPath)
	if err != nil {
		fail(err)
	}
	fmt.Printf("✅ %s  (%s, %.1f MiB)\n", outPath, runtime.GOOS+"/"+runtime.GOARCH, float64(st.Size())/(1<<20))
	if !*debug && runtime.GOOS == "windows" {
		fmt.Println("   双击运行。日志在 %APPDATA%\\WorkBuddy2API\\workbuddy2api.log")
		fmt.Println("   调试时用 -debug 构建（保留控制台窗口，可看实时日志）。")
	}
}

// findModuleRoot 从 start 向上找到含 go.mod 的目录（即 desktop 模块根）。
func findModuleRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("从 %s 向上没找到 go.mod", start)
		}
		dir = parent
	}
}

// exeVersion 把 1.11.6 补成 4 段版本号（1.11.6.0）；Windows 的 PE 版本字段要 4 段。
func exeVersion(v string) string {
	parts := strings.Split(v, ".")
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:4], ".")
}

// genWindowsResources 生成 wb2api-desktop.syso：exe 图标 + 版本信息 + GUI manifest。
//
// 为什么用 go-winres 而不是手写 .rc + windres：Go 不认 .rc，只自动包含 .syso；
// 而 .syso 的格式（COFF + 资源段）手写极易出错。go-winres 是纯 Go 的，
// 缺失时给出明确的安装命令而不是静默跳过——静默跳过的后果是「图标没生效但打包成功」，
// 这种失败最难发现。
//
// 失败即终止打包：宁可不出包，也不要出一个图标是老版本的包。
func genWindowsResources(desktopDir, ver string) {
	tool, err := exec.LookPath("go-winres")
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr == nil {
			cand := filepath.Join(home, "go", "bin", "go-winres.exe")
			if _, serr := os.Stat(cand); serr == nil {
				tool = cand
			}
		}
	}
	if tool == "" {
		fail(fmt.Errorf(`找不到 go-winres（生成 exe 图标与版本信息用）。
  安装：go install github.com/tc-hib/go-winres@latest
  装完确认 %%GOPATH%%\bin 在 PATH 上，或用 -skip-res 跳过（exe 图标将保持旧值）`))
	}

	icon := filepath.Join(desktopDir, "assets", "tray.ico")
	if _, err := os.Stat(icon); err != nil {
		fail(fmt.Errorf("图标文件缺失: %w", err))
	}

	// 输出必须恰好是 <包名>.syso：Go 只会自动包含以 .syso 结尾且不带 GOOS/GOARCH 后缀的文件。
	// go-winres 的 --out 是「前缀」，给了名字后它仍可能写出无扩展名的文件，故生成后改名。
	outPrefix := filepath.Join(desktopDir, "wb2api-desktop")
	fmt.Println("→ 生成 Windows 资源（exe 图标 + 版本信息）…")
	cmd := exec.Command(tool, "simply",
		"--arch", "amd64",
		"--no-suffix",
		"--out", outPrefix,
		"--icon", icon,
		"--manifest", "gui",
		"--product-name", "WorkBuddy2API",
		"--file-description", "WorkBuddy2API Desktop",
		"--product-version", ver,
		"--file-version", ver,
	)
	cmd.Dir = desktopDir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fail(fmt.Errorf("go-winres 失败: %w", err))
	}

	syso := outPrefix + ".syso"
	if _, err := os.Stat(syso); err != nil {
		// go-winres 可能写成无扩展名的 outPrefix，补一次改名。
		if _, e2 := os.Stat(outPrefix); e2 == nil {
			if rerr := os.Rename(outPrefix, syso); rerr != nil {
				fail(fmt.Errorf("go-winres 产物改名失败: %w", rerr))
			}
		} else {
			fail(fmt.Errorf("go-winres 未生成 %s: %w", filepath.Base(syso), err))
		}
	}
	fmt.Printf("   %s 已生成（图标 %s，版本 %s）\n", filepath.Base(syso), filepath.Base(icon), ver)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "❌", err)
	os.Exit(1)
}
