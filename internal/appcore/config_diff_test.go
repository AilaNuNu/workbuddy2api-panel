package appcore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// loadConfig 按线上同一套规则造配置（含缺省归一与桌面路径解析）。
func loadConfig(t *testing.T, body string) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

// saveRestartRequired 模拟面板保存：走**生产同一套**合并+解析路径
// （resolveSavedConfig），再与 running 比较。
//
// 这里刻意不再自己 merge+ParseConfig：早先的写法与生产各维护一份，生产加了
// 桌面路径解析后测试没跟上，测的就成了过期逻辑。共用一份才不会漂移。
func saveRestartRequired(t *testing.T, running *Config, path string, raw string) bool {
	t.Helper()
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := resolveSavedConfig(old, []byte(raw))
	if err != nil {
		t.Fatalf("提交内容应能解析: %v", err)
	}
	return assemblyFieldsDiffer(running, next)
}

// TestRestartNotNeededForHotFields 热生效字段的改动不该要求重启。
// 这是本次改动的核心目的：桌面版不该为「改个 api_key」提示重启。
func TestRestartNotNeededForHotFields(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"只改 api_key", `{"api_key":"sk-new"}`},
		{"只改 soft_rate", `{"cooldown":{"soft_rate":"120s","soft_rate_max":"1h"}}`},
		{"只改脱敏开关", `{"features":{"sanitize_blacklist_fingerprints":false}}`},
		{"只改池参数", `{"pool":{"max_in_flight":9}}`},
		{"只改熔断参数", `{"pool":{"breaker_threshold":7,"breaker_cooldown":"10m","breaker_cooldown_max":"1h"}}`},
		{"只改降权参数", `{"pool":{"degrade_threshold":9,"degrade_cooldown":"20m","degrade_cooldown_max":"3h"}}`},
		{"只改权重", `{"pool":{"idle_weight_per_hour":1.5,"idle_weight_max":9.0}}`},
		{"只改探索窗口", `{"pool":{"cost_explore_interval":"1h"}}`},
		{"只改排程时点", `{"schedule":{"checkin_hours":[1,2,3]}}`},
		{"只关某个任务", `{"schedule":{"travel_enabled":false}}`},
		{"只改余额刷新间隔", `{"schedule":{"balance_refresh_minutes":15}}`},
		{"原样回交当前值", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			running, path := loadConfig(t, `{}`)
			if saveRestartRequired(t, running, path, tc.raw) {
				t.Fatalf("不应要求重启：%s", tc.raw)
			}
		})
	}
}

// TestRestartNeededForAssemblyFields 装配期字段一旦改变就必须重启
// （运行时对象已捕获旧值，热应用覆盖不到）。
func TestRestartNeededForAssemblyFields(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"改 listen", `{"listen":":9999"}`},
		{"改 upstream 超时", `{"upstream":{"timeout_seconds":30}}`},
		{"改上游 UA", `{"upstream":{"user_agent":"X/1"}}`},
		{"改设备 token 文件", `{"upstream":{"device_token_file":"./dt"}}`},
		{"改粘性 TTL", `{"session_sticky":{"ttl":"10m"}}`},
		{"改 upstash", `{"upstash":{"url":"redis://x"}}`},
		{"改提示词模式", `{"prompt":{"mode":"custom"}}`},
		{"改 auth_dir", `{"auth_dir":"./elsewhere"}`},
		{"改 state_file", `{"state_file":"./other.json"}`},
		{"关掉 global 路由", `{"global":{"enabled":false}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			running, path := loadConfig(t, `{}`)
			if !saveRestartRequired(t, running, path, tc.raw) {
				t.Fatalf("应要求重启：%s", tc.raw)
			}
		})
	}
}

// TestNoRestartWhenHotFieldCoexistsWithUnchangedAssembly 面板会把池参数与熔断参数
// 一起提交（同一张表单）。只要装配期部分没动，就不该因为「提交了 pool 对象」而要求重启。
func TestNoRestartWhenHotFieldCoexistsWithUnchangedAssembly(t *testing.T) {
	running, path := loadConfig(t, `{"listen":":7863"}`)
	raw := `{"listen":":7863","pool":{"max_in_flight":5},"api_key":"k2"}`
	if saveRestartRequired(t, running, path, raw) {
		t.Fatal("listen 值未变、pool 属热生效 → 不应要求重启")
	}
}

// TestDesktopNoFalseRestart 桌面模式的关键回归：
//
// 运行中配置里 auth_dir 被解析成数据目录下的**绝对路径**，而面板提交的内容不含该键。
// 比较用的是「深合并后的新配置」，所以两边都带绝对路径，不会误判为路径变化。
func TestDesktopNoFalseRestart(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "root")
	body := `{"desktop":true,"desktop_data_dir":` + jsonStr(dataDir) + `}`
	running, path := loadConfig(t, body)

	if !running.Desktop || !filepath.IsAbs(running.AuthDir) {
		t.Fatalf("前置条件不成立: desktop=%v auth_dir=%q", running.Desktop, running.AuthDir)
	}
	// 面板提交它表单里的键（不含 auth_dir/state_file）。
	if saveRestartRequired(t, running, path, `{"api_key":"sk-x"}`) {
		t.Fatal("桌面模式下，面板未提交路径不应被判定为需要重启")
	}
	// 用户真的手改了路径 → 要重启。
	other := filepath.Join(dir, "other")
	if !saveRestartRequired(t, running, path, `{"auth_dir":`+jsonStr(other)+`}`) {
		t.Fatal("路径实际改变应要求重启")
	}
}

// TestDerivedFieldsIgnored 推导字段（json:"-"）不参与比较：
// 它们由其他字段推导（Duration 由时长字符串、PromptText 由 mode/file）。
func TestDerivedFieldsIgnored(t *testing.T) {
	running, _ := loadConfig(t, `{}`)
	// 手工塞入非零推导值，模拟「运行中被别处设过」。
	running.SoftRateDur = 600
	running.BreakerCooldownDur = 111
	running.SessionTTL = 222
	running.PromptText = "something"
	next, err := ParseConfig([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if assemblyFieldsDiffer(running, next) {
		t.Fatal("推导字段不应影响判定")
	}
}

// TestDifferNilSafety 基线缺失时保守要求重启：宁可多提示一次，也不要静默漏掉重启。
func TestDifferNilSafety(t *testing.T) {
	next, err := ParseConfig([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !assemblyFieldsDiffer(nil, next) {
		t.Fatal("没有运行中配置时应保守要求重启")
	}
	if !assemblyFieldsDiffer(next, nil) {
		t.Fatal("没有新配置时应保守要求重启")
	}
}

// TestStripHotAndDerivedZeroesByTag 清零规则以 json tag 为准：json:"-" 与热生效
// 顶层字段必须被清零，其余字段原样保留。
func TestStripHotAndDerivedZeroesByTag(t *testing.T) {
	c := Default()
	c.Listen = ":1234"
	c.APIKey = "k"
	c.SoftRateDur = 999
	c.PromptText = "p"
	c.Pool.MaxInFlight = 42
	c.Cooldown.SoftRate = "1s"

	got := stripHotAndDerived(c)

	if got.APIKey != "" {
		t.Error("api_key 应被清零（热生效）")
	}
	if got.Pool.MaxInFlight != 0 {
		t.Error("pool 应被清零（热生效）")
	}
	if got.Cooldown.SoftRate != "" {
		t.Error("cooldown 应被清零（热生效）")
	}
	if got.SoftRateDur != 0 || got.PromptText != "" {
		t.Error("推导字段应被清零（json:\"-\"）")
	}
	if got.Listen != ":1234" {
		t.Errorf("装配期字段不应被清零: %q", got.Listen)
	}
	// 原对象不能被改动（比较时要拿它作基线）。
	if c.APIKey != "k" || c.Listen != ":1234" || c.Pool.MaxInFlight != 42 {
		t.Fatal("stripHotAndDerived 修改了原配置")
	}
}

// jsonStr 把字符串编成 JSON 字面量（Windows 路径的反斜杠必须转义）。
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
