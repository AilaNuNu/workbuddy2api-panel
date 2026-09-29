package panel

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestCfgFormMapInSync 配置页的表单控件与 app.js 的 CFG_MAP 必须一一对应。
//
// 为什么需要这条闸门：CFG_MAP 同时驱动 loadConfig（回填）与 collectConfig（提交），
// 而两边都只处理「表单里有控件」的键。于是任何一种半边缺失都是**静默**的：
//
//   - 有控件、无映射：用户改了这项，保存时被无声丢弃，界面仍回显「已保存」；
//   - 有映射、无控件：该字段永远读不到也存不了，配置页对它完全无效。
//
// 上游加了 logging.*（请求归档）字段却没配 UI，正是后一种情形；Go 侧测试与 JS 语法
// 检查对这类断裂全盲——没有编译错误、没有运行时异常，功能就是不生效。
func TestCfgFormMapInSync(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)

	// 只取配置视图，避免把其它视图（如日志页的筛选控件）算进来。
	i := strings.Index(s, `id="view-config"`)
	if i < 0 {
		t.Fatal("index.html 里找不到 view-config")
	}
	j := strings.Index(s[i:], `id="view-logs"`)
	if j < 0 {
		t.Fatal("index.html 里找不到 view-logs（用来界定配置视图的结束）")
	}
	cfgView := s[i : i+j]

	formNames := map[string]bool{}
	for _, m := range regexp.MustCompile(`name="([^"]+)"`).FindAllStringSubmatch(cfgView, -1) {
		formNames[m[1]] = true
	}

	body := string(js)
	mi := strings.Index(body, "const CFG_MAP = {")
	if mi < 0 {
		t.Fatal("app.js 里找不到 CFG_MAP")
	}
	rest := body[mi:]
	end := strings.Index(rest, "\n};")
	if end < 0 {
		t.Fatal("app.js 里 CFG_MAP 字面量没有收尾的 };")
	}
	mapBody := rest[:end]

	mapKeys := map[string]bool{}
	for _, m := range regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:`).FindAllStringSubmatch(mapBody, -1) {
		mapKeys[m[1]] = true
	}

	// 解析结果过少说明判据本身失效了（比如 CFG_MAP 被换成了别的写法），
	// 这时宁可报错也不要静默通过。
	if len(formNames) < 30 || len(mapKeys) < 30 {
		t.Fatalf("解析到的键太少（表单 %d、映射 %d），判据可能已失效", len(formNames), len(mapKeys))
	}

	var orphanFields, orphanKeys []string
	for n := range formNames {
		if !mapKeys[n] {
			orphanFields = append(orphanFields, n)
		}
	}
	for k := range mapKeys {
		if !formNames[k] {
			orphanKeys = append(orphanKeys, k)
		}
	}
	sort.Strings(orphanFields)
	sort.Strings(orphanKeys)

	if len(orphanFields) > 0 {
		t.Errorf("表单有控件但 CFG_MAP 无映射（保存时会被静默丢弃）: %v", orphanFields)
	}
	if len(orphanKeys) > 0 {
		t.Errorf("CFG_MAP 有映射但表单无控件（该字段在界面上无效）: %v", orphanKeys)
	}
}
