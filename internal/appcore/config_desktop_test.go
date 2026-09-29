package appcore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCfg 写一份配置文件到 dir，返回路径。
func writeCfg(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// quote 把路径包成 JSON 字符串（Windows 反斜杠必须转义）。
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestDesktopDerivesPathsFromDataDir 桌面模式且配置**未写** auth_dir/state_file 时，
// 两者从数据目录派生——这是「搬动数据目录不产生孤儿账号」的机制。
func TestDesktopDerivesPathsFromDataDir(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data-root")
	cfg := writeCfg(t, dir, `{"desktop":true,"desktop_data_dir":`+quote(dataDir)+`}`)

	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Desktop {
		t.Fatal("desktop 未生效")
	}
	if c.AuthDir != filepath.Join(dataDir, "auths") {
		t.Fatalf("auth_dir=%q", c.AuthDir)
	}
	if c.StateFile != filepath.Join(dataDir, "data", "state.json") {
		t.Fatalf("state_file=%q", c.StateFile)
	}
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		t.Fatalf("数据目录应被创建: %v", err)
	}
}

// TestDesktopFollowsMovedDataDir 数据目录改变后路径跟随（搬机器 / 改 %APPDATA% 场景）。
func TestDesktopFollowsMovedDataDir(t *testing.T) {
	dir := t.TempDir()
	oldDir, newDir := filepath.Join(dir, "old-root"), filepath.Join(dir, "new-root")

	first := writeCfg(t, dir, `{"desktop":true,"desktop_data_dir":`+quote(oldDir)+`}`)
	c1, err := Load(first)
	if err != nil {
		t.Fatal(err)
	}
	if c1.AuthDir != filepath.Join(oldDir, "auths") {
		t.Fatalf("首次加载路径异常: %q", c1.AuthDir)
	}

	second := writeCfg(t, dir, `{"desktop":true,"desktop_data_dir":`+quote(newDir)+`}`)
	c2, err := Load(second)
	if err != nil {
		t.Fatal(err)
	}
	if c2.AuthDir != filepath.Join(newDir, "auths") || strings.Contains(c2.AuthDir, "old-root") {
		t.Fatalf("路径未跟随数据目录: %q", c2.AuthDir)
	}
}

// TestDesktopExplicitPathsWin 用户在配置里显式写了路径 → 一律照用户配置走，
// 只补成绝对路径，不改变指向。
func TestDesktopExplicitPathsWin(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "root")
	custom := t.TempDir()
	stateF := filepath.Join(custom, "s.json")
	cfg := writeCfg(t, dir,
		`{"desktop":true,"desktop_data_dir":`+quote(dataDir)+
			`,"auth_dir":`+quote(custom)+`,"state_file":`+quote(stateF)+`}`)

	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthDir != custom {
		t.Fatalf("显式 auth_dir 被覆盖: %q", c.AuthDir)
	}
	if c.StateFile != stateF {
		t.Fatalf("显式 state_file 被覆盖: %q", c.StateFile)
	}
}

// TestDesktopRelativeExplicitPathGetsAbsolutized 用户写的是相对路径 → 不改指向，
// 但要补成绝对路径（桌面 CWD 不可控）。
func TestDesktopRelativeExplicitPathGetsAbsolutized(t *testing.T) {
	dir := t.TempDir()
	cfg := writeCfg(t, dir, `{"desktop":true,"auth_dir":"./myauths"}`)
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.AuthDir) {
		t.Fatalf("应补成绝对路径: %q", c.AuthDir)
	}
	if filepath.Base(c.AuthDir) != "myauths" {
		t.Fatalf("指向被改动: %q", c.AuthDir)
	}
}

// TestOfflineServerSemanticsUnchanged 未启用 desktop 时路径语义完全不变（零回归）：
// Docker / 服务端部署依赖这份相对路径语义。
func TestOfflineServerSemanticsUnchanged(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(writeCfg(t, dir, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Desktop {
		t.Fatal("desktop 应缺省 false")
	}
	if c.AuthDir != "./auths" || c.StateFile != "./data/state.json" {
		t.Fatalf("路径语义被改动: auth_dir=%q state_file=%q", c.AuthDir, c.StateFile)
	}
}

// TestDesktopDefaultConfigOmitsPathKeys 桌面首次生成的配置**不得**写入
// auth_dir/state_file：键一直缺席，路径才能一直跟随数据目录。
func TestDesktopDefaultConfigOmitsPathKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	key, err := WriteDesktopDefault(path)
	if err != nil {
		t.Fatal(err)
	}
	if key == "" {
		t.Fatal("应生成随机 api_key")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if jsonHasKey(raw, "auth_dir") || jsonHasKey(raw, "state_file") {
		t.Fatalf("桌面默认配置不应写路径键: %s", raw)
	}
	if !jsonHasKey(raw, "desktop") {
		t.Fatal("应写入 desktop: true")
	}

	// 由此负载出来的配置必须落在数据目录里。
	dataDir := filepath.Join(dir, "root")
	t.Setenv("WB2A_DESKTOP_DATA_DIR", dataDir)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthDir != filepath.Join(dataDir, "auths") {
		t.Fatalf("auth_dir=%q", c.AuthDir)
	}

	// 已存在时绝不覆盖。
	if _, err := WriteDesktopDefault(path); err == nil {
		t.Fatal("已存在的配置不应被覆盖")
	}
}

// TestDesktopDataDirEnvOverride 环境变量可覆盖数据目录（测试 / 便携部署）。
func TestDesktopDataDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "from-env")
	t.Setenv("WB2A_DESKTOP_DATA_DIR", envDir)
	c, err := Load(writeCfg(t, dir, `{"desktop":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthDir != filepath.Join(envDir, "auths") {
		t.Fatalf("env 覆盖未生效: %q", c.AuthDir)
	}
}

// TestJsonHasKey 键判定只看原文，不受缺省值干扰。
func TestJsonHasKey(t *testing.T) {
	cases := []struct {
		raw  string
		key  string
		want bool
	}{
		{`{"auth_dir":"x"}`, "auth_dir", true},
		{`{"auth_dir":null}`, "auth_dir", true}, // 显式 null 也算写了 → 不派生
		{`{"listen":":1"}`, "auth_dir", false},
		{``, "auth_dir", false},
		{`{ not json`, "auth_dir", false},
	}
	for _, tc := range cases {
		if got := jsonHasKey([]byte(tc.raw), tc.key); got != tc.want {
			t.Errorf("jsonHasKey(%q,%q)=%v want %v", tc.raw, tc.key, got, tc.want)
		}
	}
}
