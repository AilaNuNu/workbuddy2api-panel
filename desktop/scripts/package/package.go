// package.go Windows 安装包构建脚本（NSIS）。
//
//	go run ./scripts/package            # 先构建 exe，再打安装包
//	go run ./scripts/package -version 1.11.6
//	go run ./scripts/package -skip-build    # 直接用已有的 exe 打包
//	go run ./scripts/package -makensis C:\path	o\makensis.exe
//
// 目录位置说明：本脚本单独放在 scripts/package/ 而不是和 build.go 并列——
// 两者都是 package main 且各有一个 main()，同目录会直接编译失败（go build ./... 报 redeclared）。
//
// 为什么要脚本而不是让用户手敲 makensis：
//
//  1. NSIS 的三个变量（版本号、要打包的 exe 路径、输出文件名）都得给对，
//     而版本号散落在代码里，手工同步迟早写歪。
//  2. makensis 往往不在 PATH 上（NSIS 安装后不自动加 PATH），脚本按常见位置找。
//  3. 打包前必须先构建，否则会把**上一次**的 exe 打进安装包——这种错误
//     产物体积、文件名全对，只有功能是老版本，最难发现。
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

// 版本号由 internal/version 统一决定（源码 AppVersion → git tag → 兜底），
// 这里只留一个「没显式传 -version 时」的占位默认值。
const defaultVersion = appver.Fallback

func main() {
	version := flag.String("version", defaultVersion, "安装包版本号（缺省取源码 AppVersion，退回 git tag）")
	skipBuild := flag.Bool("skip-build", false, "跳过 exe 构建，直接打已有的二进制")
	makensisFlag := flag.String("makensis", "", "makensis 可执行文件路径（缺省自动查找）")
	flag.Parse()

	if runtime.GOOS != "windows" {
		fail(fmt.Errorf("当前只支持在 Windows 上打安装包（当前 %s）", runtime.GOOS))
	}

	// desktop 模块根：从当前目录向上找 go.mod（本脚本在 desktop/scripts/package/ 下）。
	here, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	desktopDir, err := findModuleRoot(here)
	if err != nil {
		fail(err)
	}

	// 没显式给 -version 时按统一规则解析。
	if !flagWasSet("version") {
		v := appver.Resolve(desktopDir)
		version = &v
	}

	iconPath := filepath.Join(desktopDir, "assets", "tray.ico")
	if _, err := os.Stat(iconPath); err != nil {
		iconPath = ""
		fmt.Printf("⚠ 未找到图标（%s），安装包将使用 NSIS 默认图标\n", filepath.Join("assets", "tray.ico"))
	}

	exePath := filepath.Join(desktopDir, "wb2api-desktop.exe")
	if !*skipBuild {
		build(desktopDir, exePath, *version)
	}
	st, err := os.Stat(exePath)
	if err != nil {
		fail(fmt.Errorf("找不到要打包的 exe（%s）；去掉 -skip-build 让它先构建", exePath))
	}

	nsiPath := filepath.Join(desktopDir, "nsis", "wb2api-desktop.nsi")
	if _, err := os.Stat(nsiPath); err != nil {
		fail(fmt.Errorf("找不到 NSIS 脚本 %s: %w", nsiPath, err))
	}

	makensis, err := findMakensis(*makensisFlag)
	if err != nil {
		fail(err)
	}

	outName := fmt.Sprintf("wb2api-desktop-%s-setup.exe", *version)
	outPath := filepath.Join(desktopDir, "dist", outName)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fail(err)
	}

	args := []string{
		"/DVERSION=" + *version,
		"/DSRC_EXE=" + exePath,
		"/DOUT_FILE=" + outPath,
	}
	if iconPath != "" {
		args = append(args, "/DICON_FILE="+iconPath)
	}
	args = append(args, nsiPath)

	fmt.Printf("→ 打包：%s (%s) → %s\n", filepath.Base(exePath), *version, outName)
	cmd := exec.Command(makensis, args...)
	cmd.Dir = filepath.Dir(nsiPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fail(fmt.Errorf("makensis 失败: %w", err))
	}

	pst, err := os.Stat(outPath)
	if err != nil {
		fail(fmt.Errorf("makensis 退出码为 0 但没生成安装包（检查 .nsi 里的 OutFile）: %w", err))
	}
	fmt.Printf("✅ %s  (%.1f MiB，内置 exe %.1f MiB)\n",
		outPath, float64(pst.Size())/(1<<20), float64(st.Size())/(1<<20))
	fmt.Println("   双击安装（按用户安装，不弹 UAC）。")
	fmt.Println("   开机自启在托盘菜单里开关；卸载器会清掉对应的自启项。")
}

// flagWasSet 判断某个 flag 是否被显式传入（区分「没传」与「传了默认值」）。
func flagWasSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// exeVersion 把 1.11.6 补成 4 段版本号（1.11.6.0）；Windows 的 PE 版本字段要 4 段。
func exeVersion(v string) string {
	parts := strings.Split(v, ".")
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:4], ".")
}

// build 调 build.go 的同一套 ldflags 构建 exe。
// 刻意不在这里重复 ldflags：两个脚本各写一份必然分叉，
// 出现「正式版能跑、安装包里的不能跑」这类难查的差异。
func build(desktopDir, exePath, version string) {
	fmt.Println("→ 构建 exe（含 exe 图标与版本资源）…")
	cmd := exec.Command("go", "run", "./scripts/build.go", "-o", filepath.Base(exePath), "-version", exeVersion(version))
	cmd.Dir = desktopDir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fail(fmt.Errorf("构建失败（先修好构建再打包，避免把旧 exe 打进安装包）: %w", err))
	}
}

// findMakensis 按「显式指定 → PATH → 常见安装位置 → 本地工具目录」的顺序查找。
func findMakensis(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("-makensis 指定的路径不可用: %w", err)
		}
		return explicit, nil
	}
	if p, err := exec.LookPath("makensis"); err == nil {
		return p, nil
	}
	candidates := []string{
		`C:\Program Files (x86)\NSIS\makensis.exe`,
		`C:\Program Files\NSIS\makensis.exe`,
		`D:\Tools\nsis\makensis.exe`,
		`D:\Tools\nsis\NSIS\makensis.exe`,
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf(`找不到 makensis。
  装 NSIS：winget install NSIS.NSIS
  或下载 zip 解压到 D:\Tools\nsis（脚本会自动找到），
  或显式指定：go run ./scripts/package.go -makensis <路径>`)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "❌", err)
	os.Exit(1)
}

// findModuleRoot 从 start 向上找到含 go.mod 的目录（即 desktop 模块根）。
//
// 与 build.go 里同名函数各留一份：两者在不同目录、属于不同 package main，
// 无法互相引用；只有十几行纯路径逻辑，抽公共包反而要引入模块层面的依赖，
// 对一次性构建脚本不划算。
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
