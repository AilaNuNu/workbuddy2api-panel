// config.go 面板配置页接口：读取当前配置、校验并保存（热生效 + 重启项标注）。
//
// 分工：appcore 持有 Config 类型与校验逻辑（Load/normalize），此处只做 HTTP 编排——
// GET 回显、POST 透传给注入的 SaveConfig 闭包（由 appcore 完成
// "校验 → 落盘 → 热应用 → 返回需重启字段列表"）。
package panel

import (
	"io"
	"log"
	"net/http"
	"time"
)

// getConfig 返回当前配置文件内容与路径（前端按 schema 渲染表单）。
func (p *Panel) getConfig(w http.ResponseWriter, r *http.Request) {
	if p.cfg.LoadConfig == nil {
		writeErr(w, http.StatusNotImplemented, "config api not available")
		return
	}
	cfg, err := p.cfg.LoadConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"path":   p.cfg.ConfigPath,
		"config": cfg,
	})
}

// saveConfig 保存配置：body 直接是配置 JSON（前端按 schema 组装完整对象）。
// SaveConfig 闭包内部完成校验+落盘+热应用；校验失败返回 400 且不写盘。
func (p *Panel) saveConfig(w http.ResponseWriter, r *http.Request) {
	if p.cfg.SaveConfig == nil {
		writeErr(w, http.StatusNotImplemented, "config api not available")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	restartRequired, err := p.cfg.SaveConfig(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if restartRequired == nil {
		restartRequired = []string{}
	}
	// 调用方（桌面壳）可以否决「需要重启」的判定：只有本次改动真的碰到装配期字段
	// 才置位，避免用户体验成「随便改个东西都要重启」。nil = 保守按需重启处理。
	needsRestart := len(restartRequired) > 0
	if !needsRestart {
		// 对方明确说不需要重启 → 不要给前端一个吓人的字段列表。
		restartRequired = []string{}
	}
	log.Printf("panel: 配置已保存（热生效完成；需重启进程=%v）", needsRestart)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"restart_required":   restartRequired,
		"process_restarting": needsRestart && p.cfg.RestartProcess != nil,
	})
	// 重启放在响应写完之后异步做：重启会中断 HTTP 服务，同步调用会让前端拿不到
	// 上面这份「已保存」的结果，用户看到的是网络错误而不是成功提示。
	if needsRestart && p.cfg.RestartProcess != nil {
		go func() {
			// 给响应一点时间到达浏览器（含前端渲染 toast）。
			time.Sleep(300 * time.Millisecond)
			p.restartProcess()
		}()
	}
}

// restartProcess 供 SaveConfig 落盘后调用：请求宿主重启进程/内核。
// 未注入（服务端版本）则什么都不做，仅由前端提示用户「需重启」。
func (p *Panel) restartProcess() {
	if p.cfg.RestartProcess == nil {
		return
	}
	log.Printf("panel: 按配置变更请求重启进程")
	p.cfg.RestartProcess()
}

// rerollKey 重新生成 api_key 并把新密钥返回给调用方。
//
// 安全前提：本接口自身由 withAuth 保护，即调用方必须已经用**当前**密钥通过鉴权。
// 因此「忘记密钥的人」无法从远程盲调它——它解决的是「面板已经打开、想把密钥换成
// 一个新的（或上一个已泄漏）」，以及桌面版首启密钥未留存时的自助恢复。
//
// 响应里必须带上新密钥：api_key 是热生效字段，写盘后下一个请求就开始按新密钥鉴权，
// 而浏览器的 localStorage 里还是旧值、当前会话的 Authorization 头也仍是旧值。
// 前端要拿它覆盖本地存储并更新后续请求，否则用户会立刻被 401 锁在门外。
func (p *Panel) rerollKey(w http.ResponseWriter, r *http.Request) {
	if p.cfg.RotateAPIKey == nil {
		writeErr(w, http.StatusNotImplemented, "rotate api_key not available")
		return
	}
	key, err := p.cfg.RotateAPIKey()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("panel: api_key 已重置（旧密钥立即失效）")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"api_key": key,
	})
}
