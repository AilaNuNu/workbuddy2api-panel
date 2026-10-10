package panel

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// 桌面版的密钥门必须把**实际** config.json 路径写进页面。
//
// 为什么值得一条测试：密钥门出现在鉴权之前，用户此刻进不了配置页也调不到任何
// /panel/api/*，页面里这条路径是唯一能带他找到文件的线索。桌面版把数据目录钉在
// %APPDATA%\WorkBuddy2API，收到安装包的人此前只能卡在这个窗口前。
func TestIndexInjectsConfigPathInDesktopMode(t *testing.T) {
	const path = `C:\Users\alice\AppData\Roaming\WorkBuddy2API\config.json`
	p := New(Config{Version: "test", APIKey: "k", Desktop: true, ConfigPath: path})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `id="keyCfgPath"`) {
		t.Fatal("页面缺少密钥门的路径元素 keyCfgPath")
	}
	if !strings.Contains(body, path) {
		t.Errorf("桌面版页面未注入配置路径 %q", path)
	}
	if strings.Contains(body, configPathPlaceholder) {
		t.Error("占位符未被替换，用户会看到 {{CONFIG_PATH}}")
	}
}

// 服务端部署只回落到通用文件名：面板页面匿名可加载，把宿主/容器的绝对路径吐给
// 任何访客会破坏「面板 HTML 本身无秘密」这条不变量。
func TestIndexHidesHostPathInServerMode(t *testing.T) {
	const path = "/etc/workbuddy2api/config.json"
	p := New(Config{Version: "test", APIKey: "k", ConfigPath: path})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	if strings.Contains(body, path) {
		t.Errorf("服务端模式页面泄露了配置文件绝对路径 %q", path)
	}
	if strings.Contains(body, configPathPlaceholder) {
		t.Error("占位符未被替换，用户会看到 {{CONFIG_PATH}}")
	}
}

// 注入的路径按 HTML 文本转义：Windows 用户名可能含 & 等字符，不能让它变成标记。
func TestIndexEscapesConfigPath(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "k", Desktop: true, ConfigPath: `C:\a&b\config.json`})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	if strings.Contains(body, `C:\a&b\config.json`) {
		t.Error("路径里的 & 未转义")
	}
	if !strings.Contains(body, `C:\a&amp;b\config.json`) {
		t.Error("转义后的路径未出现在页面里")
	}
}
