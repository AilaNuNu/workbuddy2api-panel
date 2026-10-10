// clientkeys_api.go 客户端密钥的增删改：落盘 + 热生效。
//
// 为什么集中在 appcore：配置结构（ClientKey）、校验（normalizeClientKeys）、原子落盘
// （mergeAndWriteConfig）与热配置快照（livecfg）全在这一层。panel 只做 HTTP 编排，
// 通过 Config 上的回调转进来。
//
// 本文件里所有写操作都遵循同一条路径：**先改盘、再热生效**。
// 反过来的话，热生效成功而落盘失败会让运行态与文件不一致，重启后密钥又回来了 ——
// 用户以为吊销过的密钥重新生效，是比"保存失败"严重得多的结果。
package appcore

import (
	"encoding/json"
	"fmt"

	"github.com/linguo2625469/workbuddy2api-panel/internal/panel"
)

// ClientKeysAPI 把客户端密钥的读写能力包装成 panel 需要的回调集合。
//
// 返回的是闭包而不是让 panel import appcore：依赖方向是 appcore → panel，
// 反向 import 会成环（panel 是 HTTP 层，本就不该感知配置结构）。
func (r *Runtime) ClientKeysAPI() (list func() ([]panel.ClientKeyView, error),
	create func(name, note string) (panel.ClientKeyView, error),
	setEnabled func(id string, enabled bool) error,
	remove func(id string) error) {

	list = func() ([]panel.ClientKeyView, error) {
		cfg, err := r.loadConfigForKeys()
		if err != nil {
			return nil, err
		}
		out := make([]panel.ClientKeyView, 0, len(cfg.ClientKeys))
		for _, k := range cfg.ClientKeys {
			out = append(out, clientKeyView(k))
		}
		return out, nil
	}

	create = func(name, note string) (panel.ClientKeyView, error) {
		key, err := NewClientKey(name, note)
		if err != nil {
			return panel.ClientKeyView{}, err
		}
		// 以**磁盘上的当前值**为基线追加，而不是 r.Config：r.Config 是启动时的装配
		// 基线，用它会让"先加一把、再加一把"把第一把覆盖掉。
		if err := r.mutateClientKeys(func(keys []ClientKey) ([]ClientKey, error) {
			return append(keys, key), nil
		}); err != nil {
			return panel.ClientKeyView{}, err
		}
		return clientKeyView(key), nil
	}

	setEnabled = func(id string, enabled bool) error {
		return r.mutateClientKeys(func(keys []ClientKey) ([]ClientKey, error) {
			i, err := indexClientKey(keys, id)
			if err != nil {
				return nil, err
			}
			keys[i].Enabled = enabled
			return keys, nil
		})
	}

	remove = func(id string) error {
		return r.mutateClientKeys(func(keys []ClientKey) ([]ClientKey, error) {
			i, err := indexClientKey(keys, id)
			if err != nil {
				return nil, err
			}
			return append(keys[:i:i], keys[i+1:]...), nil
		})
	}
	return list, create, setEnabled, remove
}

// clientKeyView 把配置里的密钥映射成面板视图（两侧类型不同，映射只此一处）。
func clientKeyView(k ClientKey) panel.ClientKeyView {
	return panel.ClientKeyView{
		ID:        k.EffectiveID(),
		Name:      k.Name,
		Key:       k.Key,
		Enabled:   k.Enabled,
		CreatedAt: k.CreatedAt,
		Note:      k.Note,
	}
}

// indexClientKey 按 id（显式 id 或派生 id）定位一条密钥。
func indexClientKey(keys []ClientKey, id string) (int, error) {
	if id == "" {
		return 0, fmt.Errorf("缺少 id")
	}
	for i := range keys {
		if keys[i].EffectiveID() == id {
			return i, nil
		}
	}
	// 密钥可能已被另一个标签页删掉。明确报出来，而不是静默成功 ——
	// 静默成功会让界面显示"已停用"，而实际上什么都没发生。
	return 0, fmt.Errorf("找不到密钥 %q（可能已被删除，请刷新列表）", id)
}

// loadConfigForKeys 读取当前生效的密钥集合。
//
// 走 Load（读盘 + 校验）而不是 r.Config：面板刚加过密钥时 r.Config 还是启动时的
// 装配基线，用它会让新加的密钥在列表里看不见，追加第二把时还会把第一把覆盖掉。
func (r *Runtime) loadConfigForKeys() (*Config, error) {
	if r.ConfigPath == "" {
		return r.Config, nil
	}
	return Load(r.ConfigPath)
}

// mutateClientKeys 读盘 → 就地改写密钥集合 → 落盘 → 热生效。
//
// mutate 收到的切片可以就地改（它来自本次读盘，不是共享状态）。
func (r *Runtime) mutateClientKeys(mutate func([]ClientKey) ([]ClientKey, error)) error {
	if r.live == nil {
		return fmt.Errorf("热配置未启用，无法在线修改密钥（请重启内核后重试）")
	}
	cfg, err := r.loadConfigForKeys()
	if err != nil {
		return err
	}
	next, err := mutate(append([]ClientKey(nil), cfg.ClientKeys...))
	if err != nil {
		return err
	}

	// 提交体只带 client_keys：mergeAndWriteConfig 会与磁盘内容深合并，
	// 用户手写的其它键（含未知键）原样保留。
	body, err := json.Marshal(map[string]any{"client_keys": next})
	if err != nil {
		return fmt.Errorf("序列化密钥: %w", err)
	}
	newCfg, err := mergeAndWriteConfig(r.ConfigPath, body)
	if err != nil {
		return err
	}

	// 落盘成功后才热生效。用 newCfg（本次合并后的结果）重建凭据，而不是 r.Config 或
	// 局部变量 —— 只有 newCfg 是"这次保存后真正生效的配置"。
	snap := r.live.Load()
	snap.Creds = credentialsOf(newCfg)
	r.live.Store(snap)
	return nil
}
