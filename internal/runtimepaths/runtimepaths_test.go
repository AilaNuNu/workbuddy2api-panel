package runtimepaths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestUserDataDirEnvOverride 环境变量优先级最高：便携部署/测试靠它指定目录。
func TestUserDataDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDataDir, dir)
	if got := UserDataDir(); got != dir {
		t.Fatalf("UserDataDir() = %q, want %q", got, dir)
	}
}

// TestUserDataDirPerOS 未设环境变量时按平台推断，且路径必须非空、带应用名、
// 且是绝对路径（相对路径会让数据落在工作目录，正是本包要消除的问题）。
func TestUserDataDirPerOS(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	got := UserDataDir()
	if strings.TrimSpace(got) == "" {
		t.Fatal("UserDataDir() 为空")
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("UserDataDir() = %q 不是绝对路径", got)
	}
	switch runtime.GOOS {
	case "windows":
		if !strings.Contains(got, "WorkBuddy2API") {
			t.Fatalf("windows 路径应含 WorkBuddy2API: %q", got)
		}
	case "darwin":
		if !strings.Contains(got, "Application Support") || !strings.Contains(got, "WorkBuddy2API") {
			t.Fatalf("darwin 路径不符合预期: %q", got)
		}
	default:
		if !strings.Contains(got, "workbuddy2api") {
			t.Fatalf("linux 路径应含 workbuddy2api: %q", got)
		}
	}
}

// TestUserDataDirWindowsAppData windows 分支优先用 %APPDATA%（Windows 上
// 即使用户主目录被重定向也应跟随 APPDATA）。非 windows 平台此测试不适用。
func TestUserDataDirWindowsAppData(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 windows")
	}
	t.Setenv(EnvDataDir, "")
	t.Setenv("APPDATA", `C:\Users\x\AppData\Roaming`)
	want := filepath.Join(`C:\Users\x\AppData\Roaming`, "WorkBuddy2API")
	if got := UserDataDir(); got != want {
		t.Fatalf("UserDataDir() = %q, want %q", got, want)
	}
	// APPDATA 缺失时回落到用户主目录，仍须是绝对路径且不 panic。
	t.Setenv("APPDATA", "")
	got := UserDataDir()
	if got == "" || !filepath.IsAbs(got) {
		t.Fatalf("APPDATA 缺失时回落失败: %q", got)
	}
	if home, err := os.UserHomeDir(); err == nil && !strings.HasPrefix(got, home) {
		t.Fatalf("回落路径应位于用户主目录下: %q", got)
	}
}
