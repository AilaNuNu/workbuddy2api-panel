package appcore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadOrGenerateFirstRunFlag 首启标记：只有「本次真的新建了配置文件」才为 true。
//
// 这个标记决定桌面版是否弹「首次运行：访问密钥」。判错的两种后果都不轻：
//   - 漏报（该 true 却 false）→ 全新安装的用户拿不到密钥，进不去面板（就是本次要修的 bug）
//   - 误报（该 false 却 true）→ 老用户每次启动都被弹窗、剪贴板被覆盖
func TestLoadOrGenerateFirstRunFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// 第一次：文件不存在 → 生成，firstRun 应为 true
	cfg, _, firstRun, err := loadOrGenerate(path, WriteDefault)
	if err != nil {
		t.Fatalf("首次生成失败: %v", err)
	}
	if !firstRun {
		t.Error("文件不存在时应报告 firstRun=true")
	}
	if cfg.APIKey == "" {
		t.Error("生成的配置应含非空 api_key")
	}
	firstKey := cfg.APIKey

	// 第二次：文件已存在 → 不得触发首启，且密钥不变
	cfg2, _, firstRun2, err := loadOrGenerate(path, WriteDefault)
	if err != nil {
		t.Fatalf("再次加载失败: %v", err)
	}
	if firstRun2 {
		t.Error("配置已存在时 firstRun 必须为 false（否则老用户每次启动都会被弹窗）")
	}
	if cfg2.APIKey != firstKey {
		t.Errorf("已有配置的密钥不该变化：%q -> %q", firstKey, cfg2.APIKey)
	}
}

// TestLoadOrGenerateSkipsCallbackWhenGenerateFails 生成失败时不得报告 firstRun。
//
// 否则会弹一个「你的密钥是 xxx」的空窗——生成失败时 cfg.APIKey 来自 Default()，
// 与磁盘内容无关，告知用户等于给出一个错的密钥。
func TestLoadOrGenerateSkipsCallbackWhenGenerateFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	failing := func(string) (string, error) {
		return "", os.ErrPermission
	}
	_, _, firstRun, err := loadOrGenerate(path, failing)
	if err != nil {
		t.Fatalf("生成失败应回退默认配置而不报错: %v", err)
	}
	if firstRun {
		t.Error("生成失败时不得报告 firstRun（否则会向用户展示一个无效密钥）")
	}
}

// TestNewCalledWithOnFirstRunInvoked 端到端：通过 New 时首启回调应被调用，
// 参数与实际生效的密钥一致。
//
// 用 New 会装配完整组件，因此这里只断言回调行为，不启动 HTTP。
func TestNewCalledWithOnFirstRunInvoked(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	var got string
	var calls int
	rt, err := New(EntryConfig{
		ConfigPath: cfgPath,
		Generate:   WriteDefault,
		Headless:   true,
		OnFirstRun: func(key string) { calls++; got = key },
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	defer rt.Close()

	if calls != 1 {
		t.Fatalf("首启回调应恰好调用 1 次，实际 %d 次", calls)
	}
	if got == "" {
		t.Fatal("回调收到的密钥为空")
	}
	if got != rt.Config.APIKey {
		t.Errorf("回调密钥 %q 与生效密钥 %q 不一致", got, rt.Config.APIKey)
	}
}

// TestNewOnFirstRunNotCalledForExistingConfig 已有配置时不触发首启回调。
func TestNewOnFirstRunNotCalledForExistingConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if _, err := WriteDefault(cfgPath); err != nil {
		t.Fatal(err)
	}

	called := false
	rt, err := New(EntryConfig{
		ConfigPath: cfgPath,
		Generate:   WriteDefault,
		Headless:   true,
		OnFirstRun: func(string) { called = true },
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	defer rt.Close()

	if called {
		t.Error("配置已存在时不得触发首启回调（老用户每次启动都会被弹窗）")
	}
}

// TestNewOnFirstRunNilSafe 不注入回调时（服务端入口）不得 panic。
func TestNewOnFirstRunNilSafe(t *testing.T) {
	dir := t.TempDir()
	rt, err := New(EntryConfig{
		ConfigPath: filepath.Join(dir, "config.json"),
		Generate:   WriteDefault,
		Headless:   true,
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	defer rt.Close()
}
