package panel

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestFrontendIDsExist 静态守卫：app.js 里引用的每个元素 id 都必须在 index.html 里存在。
//
// 为什么需要它：其它前端测试用的是 Proxy 假 DOM（任何 id 都返回一个惰性对象），
// 所以 `$('ckBdoy')` 这种拼写错误在测试里完全无声 —— 直到用户点开页面，
// 发现那一块永远是空的，而控制台里只有一行 "Cannot set properties of undefined"。
// 这里把两个文件都对一遍，把这类错误挡在提交之前。
func TestFrontendIDsExist(t *testing.T) {
	js, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}

	inHTML := map[string]bool{}
	for _, m := range regexp.MustCompile(`\sid="([^"]+)"`).FindAllStringSubmatch(string(html), -1) {
		inHTML[m[1]] = true
	}

	refs := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`\$\('([A-Za-z][\w-]*)'\)`),                 // $( 'id' )
		regexp.MustCompile(`getElementById\('([A-Za-z][\w-]*)'\)`),     // document.getElementById('id')
		regexp.MustCompile(`getElementById\("([A-Za-z][\w-]*)"\)`),     // 同上，双引号写法
		regexp.MustCompile(`setLabel\('([A-Za-z][\w-]*)'`),             // setLabel('id', ...)
		regexp.MustCompile(`dataset\.ep = '([A-Za-z][\w-]*)'`),         // data-ep 指向的 id
	} {
		for _, m := range re.FindAllStringSubmatch(string(js), -1) {
			refs[m[1]] = true
		}
	}
	// dataset.ep 是运行时赋值的：`$(btn.dataset.ep)` 这种间接引用抓不到，
	// 但 index.html 里 data-ep="..." 的值可以直接收集。
	for _, m := range regexp.MustCompile(`data-ep="([^"]+)"`).FindAllStringSubmatch(string(html), -1) {
		refs[m[1]] = true
	}

	var missing []string
	for id := range refs {
		if !inHTML[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("app.js 引用了 index.html 里不存在的元素 id（拼写错误？）：\n  %s\n"+
			"index.html 里现有 id 共 %d 个", strings.Join(missing, "\n  "), len(inHTML))
	}
}

// TestClientKeyUIIdsPresent 钉住客户端密钥功能用到的 id 全在，且成组出现。
//
// 上面的守卫是"引用的都在"，这条是"该有的都在"：漏掉一个 id 时上面那种测试
// 不会报错（没人引用它），但页面上会缺一块。
func TestClientKeyUIIdsPresent(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := string(html)
	for _, id := range []string{"ckBody", "ckName", "ckNote", "ckHint", "btnCkCreate", "usKeyBody"} {
		if !strings.Contains(h, `id="`+id+`"`) {
			t.Errorf("index.html 缺少客户端密钥 UI 需要的元素 id=%s", id)
		}
	}
}
