// clientkeys.go 面板「客户端密钥」接口：列出 / 新建 / 启停 / 删除。
//
// 分工与 config.go 相同：panel 只做 HTTP 编排，配置结构、校验、落盘与热生效都在 appcore
// 里（通过 Config 上的回调注入）。panel 不 import appcore —— 依赖方向是 appcore → panel，
// 否则成环。因此这里的 ClientKeyView 是两边约定的视图类型，由 appcore 负责映射。
//
// 安全前提：本组接口全部经 withAuth，而面板**只接受管理凭据**（见 panel.withAuth），
// 所以持有客户端密钥的设备拿不到这里的能力 —— 这正是不把管理密钥发出去的原因。
package panel

import (
	"encoding/json"
	"io"
	"net/http"
)

// ClientKeyView 一条客户端密钥的完整信息（两侧约定的视图）。
//
// Key 是**密钥原文**。这里刻意回明文而不是脱敏：客户端密钥存在的意义就是交付给别的
// 设备，面板需要能随时把它读回来重新粘贴（用户会忘记存下来）。读取方必须是管理密钥
// ——面板不给客户端密钥开门。若日后把管理面板暴露到公网，这一条与 GET /panel/api/config
// 回明文 api_key 属于同一类问题，要一起收敛（加反代 TLS + 只放 /v1/* 出公网）。
type ClientKeyView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at,omitempty"`
	Note      string `json:"note,omitempty"`
}

// clientKeysList 列出全部客户端密钥（含已停用的 —— 停用不等于删除，列表里要能看到并重新启用）。
func (p *Panel) clientKeysList(w http.ResponseWriter, r *http.Request) {
	if p.cfg.ListClientKeys == nil {
		writeErr(w, http.StatusNotImplemented, "client keys api not available")
		return
	}
	keys, err := p.cfg.ListClientKeys()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []ClientKeyView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "keys": keys})
}

// clientKeyCreate 新建一把客户端密钥。
//
// 密钥由服务端生成（appcore.NewClientKey）：生成随机凭据这件事不该交给浏览器 ——
// 前端生成的密钥质量取决于运行环境，而且那样"密钥长什么样"就成了客户端的自由。
func (p *Panel) clientKeyCreate(w http.ResponseWriter, r *http.Request) {
	if p.cfg.CreateClientKey == nil {
		writeErr(w, http.StatusNotImplemented, "client keys api not available")
		return
	}
	var body struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	view, err := p.cfg.CreateClientKey(body.Name, body.Note)
	if err != nil {
		// 校验失败（重名/空名/密钥撞车等）是用户输入问题，按 400 回，让前端把原因显示出来。
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": view})
}

// clientKeySetEnabled 启用/停用一把密钥。
//
// 停用（而不是删除）是首选的止血动作：设备掉线后你还能把它重新打开，
// 而删除会让历史统计里的归属只剩 id。两者都提供，让用户按情况选。
func (p *Panel) clientKeySetEnabled(w http.ResponseWriter, r *http.Request) {
	if p.cfg.SetClientKeyEnabled == nil {
		writeErr(w, http.StatusNotImplemented, "client keys api not available")
		return
	}
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ID == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if err := p.cfg.SetClientKeyEnabled(body.ID, body.Enabled); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// clientKeyRemove 删除一把密钥。
func (p *Panel) clientKeyRemove(w http.ResponseWriter, r *http.Request) {
	if p.cfg.DeleteClientKey == nil {
		writeErr(w, http.StatusNotImplemented, "client keys api not available")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ID == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if err := p.cfg.DeleteClientKey(body.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// decodeJSONBody 读取并解析请求体（限制大小，避免超大 body 占内存）。
func decodeJSONBody(r *http.Request, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return errEmptyBody{}
	}
	return json.Unmarshal(raw, dst)
}

// errEmptyBody 空请求体的错误（写成类型是为了让文案只在 decodeJSONBody 一处）。
type errEmptyBody struct{}

func (errEmptyBody) Error() string { return "请求体为空" }
