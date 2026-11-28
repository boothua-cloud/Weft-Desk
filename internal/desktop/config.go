package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/boothua/weft/internal/appinfo"
	"github.com/boothua/weft/internal/controlplane"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/secret"
	"github.com/boothua/weft/internal/store"
)

// 配置与连接档案：读写 config、切换/保存/删除 Profile、
// 以及把旧版单个配置迁移成多 Profile 的逻辑。

func (s *WeftService) GetConfig() (string, error) {
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	return toJSON(c)
}

func (s *WeftService) SaveConfig(ctx context.Context, raw string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	var c model.Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return err
	}
	if c.ControlPlane == "" {
		c.ControlPlane = "headscale"
	}
	if c.TunnelEngine == "" {
		c.TunnelEngine = "system"
	}
	if c.Hostname == "" {
		c.Hostname = appinfo.DefaultHostname()
	}
	if c.SocksPort == 0 {
		c.SocksPort = 1080
	}
	if strings.TrimSpace(c.BaseURL) != "" {
		base, plain, err := controlplane.ValidateBaseURL(c.BaseURL)
		if err != nil {
			return err
		}
		c.BaseURL = base
		if plain {
			log.Printf("[cp] %s 用的是 http，管理密钥会明文过网", base)
		}
	}
	// 导入的配置、老版本留下的配置都可能带着解析不了的 CA。
	// 这里挡住，不然 reload 之后每次请求都只报一个看不懂的错误。
	if ca := strings.TrimSpace(c.CACert); ca != "" {
		if _, err := controlplane.CertPool(ca); err != nil {
			return err
		}
	}
	// 顶层字段是「当前激活 profile」的副本，改完要写回去，否则切走再切回会丢
	s.mu.Lock()
	activeID := s.cfg.ActiveID
	s.mu.Unlock()
	if c.ActiveID == "" {
		c.ActiveID = activeID
	}
	c.ProfileID = c.ActiveID
	syncProfileFromConfig(&c)
	if err := s.store.SaveConfig(c); err != nil {
		return err
	}
	if c.AuthKey != "" {
		if err := secret.Set(secret.Auth, c.AuthKey); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.cfg = c
	s.cfg.AuthKey = ""
	s.cap = defaultCapabilities()
	hook := s.prefsHook
	s.mu.Unlock()
	if hook != nil {
		hook(c)
	}
	s.reload()
	s.store.AddAudit("保存配置", c.ControlPlane+" · "+c.BaseURL)
	s.autoConnect()
	return nil
}

// ProfilesView 返回服务端列表和当前激活项，前端用来做切换器。
func (s *WeftService) ProfilesView() (string, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	return toJSON(map[string]any{
		"items":  cfg.Profiles,
		"active": cfg.ActiveID,
	})
}

// SwitchProfile 切到另一个 Headscale 服务端：断开当前隧道、换凭证、重连。
// 每个服务端在本地有独立的节点身份目录，不会把上一个 tailnet 的登录态带过去。
func (s *WeftService) SwitchProfile(id string) error {
	s.mu.Lock()
	cfg := s.cfg
	oldID := cfg.ActiveID
	s.mu.Unlock()

	if id == "" || id == oldID {
		return nil
	}
	idx := -1
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("没有这个服务端配置")
	}

	// 1) 当前凭证存回旧 profile
	cfg.ProfileID = oldID
	store.ProfileSecrets(oldID)

	// 2) 断开旧隧道（reload 里也会关，但先断开让界面立刻回到未连接）
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng != nil {
		eng.Shutdown()
	}

	// 3) 顶层字段换成目标 profile
	target := cfg.Profiles[idx]
	cfg.BaseURL = target.BaseURL
	cfg.Tailnet = target.Tailnet
	cfg.AuthMode = target.AuthMode
	cfg.AllowInsecure = target.AllowInsecure
	cfg.CACert = target.CACert
	cfg.ControlInsecure = target.ControlInsecure
	if target.Hostname != "" {
		cfg.Hostname = target.Hostname
	}
	if target.ControlPlane != "" {
		cfg.ControlPlane = target.ControlPlane
	}
	cfg.ActiveID = id
	cfg.ProfileID = id
	cfg.AuthKey = ""

	// 4) 目标凭证写回固定条目，其余代码继续读固定 key
	store.LoadProfileSecrets(id)

	if err := s.store.SaveConfig(cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.cap = defaultCapabilities()
	s.mu.Unlock()

	s.reload()
	s.store.AddAudit("切换服务端", target.Name+" · "+target.BaseURL)
	s.autoConnect()
	return nil
}

// SaveProfile 把当前连接配置另存为一个新的服务端条目，并立即切过去。
func (s *WeftService) SaveProfile(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("给这个服务端起个名字")
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	// 先把当前凭证归到当前 profile 名下，再复制一份给新 profile
	cfg.ProfileID = cfg.ActiveID
	store.ProfileSecrets(cfg.ActiveID)
	if v := secret.Get(secret.Main); v != "" {
		// 新条目一开始沿用同一份凭证，用户可在设置里改
		id := fmt.Sprintf("p%d", time.Now().UnixNano())
		p := model.Profile{
			ID:              id,
			Name:            name,
			ControlPlane:    cfg.ControlPlane,
			BaseURL:         cfg.BaseURL,
			Tailnet:         cfg.Tailnet,
			AuthMode:        cfg.AuthMode,
			AllowInsecure:   cfg.AllowInsecure,
			Hostname:        cfg.Hostname,
			CACert:          cfg.CACert,
			ControlInsecure: cfg.ControlInsecure,
		}
		cfg.Profiles = append(cfg.Profiles, p)
		if err := s.store.SaveConfig(cfg); err != nil {
			return err
		}
		s.mu.Lock()
		s.cfg = cfg
		s.mu.Unlock()
		return s.SwitchProfile(id)
	}
	return fmt.Errorf("先把当前服务端的凭证填好再保存")
}

// DeleteProfile 删掉一个服务端（不能删当前激活的）。
func (s *WeftService) DeleteProfile(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	keep := make([]model.Profile, 0, len(cfg.Profiles))
	found := false
	next := ""
	for _, p := range cfg.Profiles {
		if p.ID == id {
			found = true
			continue
		}
		if next == "" {
			next = p.ID
		}
		keep = append(keep, p)
	}
	if !found {
		return fmt.Errorf("没有这个服务端配置")
	}
	if len(keep) == 0 {
		return fmt.Errorf("至少保留一个服务端")
	}

	// 删的是当前这个：先把隧道断掉，再把激活项换成剩下的第一个
	deletingActive := cfg.ActiveID == id
	if deletingActive {
		s.mu.Lock()
		eng := s.engine
		s.mu.Unlock()
		if eng != nil {
			eng.Shutdown()
		}
		cfg.ActiveID = next
		cfg.ProfileID = next
		for _, p := range keep {
			if p.ID == next {
				cfg.BaseURL = p.BaseURL
				cfg.Tailnet = p.Tailnet
				cfg.AuthMode = p.AuthMode
				cfg.AllowInsecure = p.AllowInsecure
				cfg.CACert = p.CACert
				cfg.ControlInsecure = p.ControlInsecure
				if p.Hostname != "" {
					cfg.Hostname = p.Hostname
				}
				if p.ControlPlane != "" {
					cfg.ControlPlane = p.ControlPlane
				}
			}
		}
	}

	cfg.Profiles = keep
	if err := s.store.SaveConfig(cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.cap = defaultCapabilities()
	s.mu.Unlock()

	// 用 Delete 而不是写空串：钥匙串里不该留一堆空条目。
	// 服务端那把 Key 不顺手吊销 —— 同一个管理 Key 可能还在别的地方用，
	// 删本地配置不该把别人的钥匙作废。
	secret.Delete(secret.Main + "." + id)
	secret.Delete(secret.Auth + "." + id)

	if deletingActive {
		store.LoadProfileSecrets(next)
		s.reload()
		s.store.AddAudit("删除服务端", id)
		s.autoConnect()
	} else {
		s.store.AddAudit("删除服务端", id)
	}
	return nil
}

// profileInput 是服务端编辑弹窗提交的字段。
// 凭证留空表示不修改；要删掉得显式给 Clear*Key —— 以前「空 = 不改」既没法测
// 「没 Key 会怎样」，也没法把填错的那串删掉。
type profileInput struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	BaseURL         string `json:"baseURL"`
	Hostname        string `json:"hostname"`
	APIKey          string `json:"apiKey"`
	AuthKey         string `json:"authKey"`
	ClearAPIKey     bool   `json:"clearApiKey"`
	ClearAuthKey    bool   `json:"clearAuthKey"`
	AllowInsecure   bool   `json:"allowInsecure"`
	CACert          string `json:"caCert"`
	ControlInsecure bool   `json:"controlInsecure"`
}

// SaveProfileRaw 新建或更新一个服务端配置，返回它的 ID。
// 改的是当前激活那个时，会顺手重连，让新地址立刻生效。
func (s *WeftService) SaveProfileRaw(ctx context.Context, raw string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	var in profileInput
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return "", err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	if in.Name == "" {
		return "", fmt.Errorf("给这个服务端起个名字")
	}
	base, plain, err := controlplane.ValidateBaseURL(in.BaseURL)
	if err != nil {
		return "", err
	}
	in.BaseURL = base
	if plain {
		log.Printf("[cp] 服务端 %s 用的是 http，管理密钥会明文过网", base)
	}

	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	id := in.ID
	isNew := id == ""
	if isNew {
		id = fmt.Sprintf("p%d", time.Now().UnixNano())
	}
	changedActive := id == cfg.ActiveID

	// 当前激活的服务端：凭证还没归档的话先归到它名下，避免被覆盖
	if changedActive {
		cfg.ProfileID = cfg.ActiveID
		store.ProfileSecrets(cfg.ActiveID)
	}

	hostname := strings.TrimSpace(in.Hostname)
	if hostname == "" {
		hostname = appinfo.DefaultHostname()
	}
	// CA 现在就解析一遍，填错了当场报，别等连不上再猜
	if ca := strings.TrimSpace(in.CACert); ca != "" {
		if _, err := controlplane.CertPool(ca); err != nil {
			return "", err
		}
	}
	p := model.Profile{
		ID:              id,
		Name:            in.Name,
		ControlPlane:    "headscale",
		BaseURL:         in.BaseURL,
		Tailnet:         "-",
		AuthMode:        "apikey",
		AllowInsecure:   in.AllowInsecure,
		Hostname:        hostname,
		CACert:          strings.TrimSpace(in.CACert),
		ControlInsecure: in.ControlInsecure,
	}

	idx := -1
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			idx = i
			break
		}
	}
	if idx >= 0 {
		p.Tailnet = cfg.Profiles[idx].Tailnet
		p.AuthMode = cfg.Profiles[idx].AuthMode
		cfg.Profiles[idx] = p
	} else {
		cfg.Profiles = append(cfg.Profiles, p)
	}

	// 凭证：留空表示沿用原来的，要清掉得点弹窗里的「清空」
	if v := strings.TrimSpace(in.APIKey); v != "" {
		_ = secret.Set(secret.Main+"."+id, v)
	} else if in.ClearAPIKey {
		secret.Delete(secret.Main + "." + id)
	}
	if v := strings.TrimSpace(in.AuthKey); v != "" {
		_ = secret.Set(secret.Auth+"."+id, v)
	} else if in.ClearAuthKey {
		secret.Delete(secret.Auth + "." + id)
	}

	if changedActive {
		cfg.BaseURL = p.BaseURL
		cfg.Hostname = p.Hostname
		cfg.AllowInsecure = p.AllowInsecure
		cfg.CACert = p.CACert
		cfg.ControlInsecure = p.ControlInsecure
		cfg.ControlPlane = p.ControlPlane
		cfg.AuthKey = ""
		store.LoadProfileSecrets(id)
	}

	if err := s.store.SaveConfig(cfg); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.cap = defaultCapabilities()
	s.mu.Unlock()

	if changedActive {
		s.reload()
		s.autoConnect()
	}
	s.store.AddAudit("保存服务端", p.Name+" · "+p.BaseURL)
	return id, nil
}

// RenameProfile 只改显示名，不动连接参数，不会触发重连。
func (s *WeftService) RenameProfile(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("名字不能为空")
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	hit := false
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			cfg.Profiles[i].Name = name
			hit = true
			break
		}
	}
	if !hit {
		return fmt.Errorf("没有这个服务端配置")
	}
	if err := s.store.SaveConfig(cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

// syncProfileFromConfig 把顶层字段写回 profiles 里对应的那一项。
func syncProfileFromConfig(c *model.Config) {
	for i := range c.Profiles {
		if c.Profiles[i].ID != c.ActiveID {
			continue
		}
		c.Profiles[i].BaseURL = c.BaseURL
		c.Profiles[i].Tailnet = c.Tailnet
		c.Profiles[i].AuthMode = c.AuthMode
		c.Profiles[i].AllowInsecure = c.AllowInsecure
		c.Profiles[i].CACert = c.CACert
		c.Profiles[i].ControlInsecure = c.ControlInsecure
		c.Profiles[i].Hostname = c.Hostname
		if c.Profiles[i].ControlPlane == "" {
			c.Profiles[i].ControlPlane = c.ControlPlane
		}
		if c.Profiles[i].Name == "" {
			c.Profiles[i].Name = store.ProfileLabel(c.BaseURL, "服务端")
		}
		return
	}
}
