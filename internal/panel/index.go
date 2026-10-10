// index.go 面板静态资源与安全响应头。
//
// 资源经 go:embed 打进二进制（随服务部署，无外部构建步骤）：
//   - index.html  页面骨架
//   - app.js      全部前端逻辑（独立文件而非内联，为了启用无需 unsafe-inline 的严格 CSP）
//
// 安全头对"面板页面与全部 /panel/api/* 响应"统一生效：CSP 限制脚本只能来自本服务，
// 禁止被 iframe 嵌套（防点击劫持），禁 MIME 嗅探，并声明不泄露 Referer 出去。
package panel

import (
	"bytes"
	_ "embed"
	"html"
	"net/http"
)

//go:embed index.html
var indexHTML []byte

//go:embed app.js
var appJS []byte

// configPathPlaceholder 是 index.html 里密钥门文案中的占位符，由 index 在响应时
// 替换为实际的配置文件路径。用 HTML 注释之外的裸 token（不使用模板引擎）是为了
// 保持"无外部构建依赖"的现状：页面仍是可直接双击打开的静态文件，只是多一行替换。
const configPathPlaceholder = "{{CONFIG_PATH}}"

// csp 内容安全策略（严格版，无需 unsafe-inline）：
//   - default-src 'none'        默认全禁，逐个开口
//   - script-src 'self'         只跑同源脚本（app.js）；页面无内联事件处理器/内联脚本
//   - style-src 'self' 'unsafe-inline'
//     style 的内联是设计取舍：页面有少量 style="..." 属性（进度条宽度、表格列宽），
//     允许内联样式不会导致脚本执行；仍禁止外部样式域与 @import 外链。
//   - connect-src 'self'        前端 fetch 只能打本服务
//   - img-src 'self' data:      图标/内联图
//   - form-action 'none'        页面无表单提交目标（配置页是 JS 提交）
//   - frame-ancestors 'none'    禁止被任何站点 iframe 嵌套（点击劫持）
//   - base-uri 'none'          禁止注入 <base> 改写相对路径
const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; img-src 'self' data:; form-action 'none'; " +
	"frame-ancestors 'none'; base-uri 'none'"

// setSecurityHeaders 写入面板统一安全响应头（页面与 API 都要，API 也含 JSON 数据）。
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff") // 禁 MIME 嗅探
	w.Header().Set("X-Frame-Options", "DENY")           // 老浏览器兜底（CSP frame-ancestors 的等价项）
	w.Header().Set("Referrer-Policy", "no-referrer")    // 不外泄面板地址给外部站点
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
}

// index 输出面板页面（静态无秘密；数据接口 /panel/api/* 才走鉴权）。
func (p *Panel) index(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(p.renderIndex())
}

// renderIndex 把配置文件路径注进页面骨架（纯计算，便于测试）。
//
// 为什么非要在页面里给出**实际路径**：密钥门出现在鉴权之前——此刻用户既进不了配置页，
// 也调不到任何 /panel/api/*，页面本身是匿名可加载的。原本文案只说「密钥在 config.json
// 里」，而桌面版把数据目录钉在 %APPDATA%\WorkBuddy2API（与安装目录无关），拿到安装包
// 的人根本不知道该去哪找这个文件，于是只能卡在密钥窗口前。
//
// 只有桌面版才吐出绝对路径，服务端部署仍回落为 "config.json"：
//   - 服务端面板可能挂在公网，而页面是匿名可加载的；把宿主/容器的文件系统路径白送给
//     任何访客，会破坏「面板 HTML 本身无秘密」这条不变量（见 panel.go 包注释）。
//   - 部署方本来就知道自己把配置放在哪（启动日志里也打了「配置已加载：<path>」），
//     不缺这条线索；而桌面版的数据目录是本机用户的 %APPDATA%，路径对访客无意义，
//     对用户却是唯一能定位到 config.json 的线索。
//
// 路径可能含 "&" 等字符（用户名），所以按 HTML 文本转义后再注入，不做属性拼接。
func (p *Panel) renderIndex() []byte {
	path := p.cfg.ConfigPath
	if !p.cfg.Desktop || path == "" {
		path = "config.json"
	}
	return bytes.Replace(indexHTML, []byte(configPathPlaceholder), []byte(html.EscapeString(path)), 1)
}

// appScript 输出前端逻辑（同源脚本，供 CSP script-src 'self' 加载）。
func (p *Panel) appScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(appJS)
}
