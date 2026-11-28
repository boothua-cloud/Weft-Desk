package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/boothua/weft/internal/controlplane"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/secret"
)

// 凭据与管理 Key：界面偏好、Auth Key / 管理 Key 的存取，
// 以及管理 Key 的到期检查与自动续期。

// uiPrefsPatch 是界面偏好的增量更新。
// 这些字段改了不需要重建隧道引擎，所以单独走一条路径：
// 立刻落库、立刻生效，不必等用户点「保存配置」。
type uiPrefsPatch struct {
	AutoConnect    *bool `json:"autoConnect"`
	AutoReconnect  *bool `json:"autoReconnect"`
	MinimizeToTray *bool `json:"minimizeToTray"`
	DevTools       *bool `json:"devTools"`
}

// SetUIPrefs 只改偏好开关，不动 BaseURL / 引擎等需要重连的字段。
func (s *WeftService) SetUIPrefs(raw string) (string, error) {
	var p uiPrefsPatch
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return "", fmt.Errorf("偏好参数无法解析: %w", err)
	}
	s.mu.Lock()
	if p.AutoConnect != nil {
		s.cfg.AutoConnect = *p.AutoConnect
	}
	if p.AutoReconnect != nil {
		s.cfg.AutoReconnect = *p.AutoReconnect
	}
	if p.MinimizeToTray != nil {
		s.cfg.MinimizeToTray = *p.MinimizeToTray
	}
	if p.DevTools != nil {
		s.cfg.DevTools = *p.DevTools
	}
	cfg, hook := s.cfg, s.prefsHook
	s.mu.Unlock()

	if err := s.store.SaveConfig(cfg); err != nil {
		return "", err
	}
	if hook != nil {
		hook(cfg)
	}
	return toJSON(cfg)
}

// SetCredential 单独把服务端凭证写入系统钥匙串，绝不进 SQLite。
func (s *WeftService) SetCredential(ctx context.Context, kind, value string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	key := secret.Main
	switch kind {
	case "auth":
		key = secret.Auth
	case "client":
		key = secret.Client
	}
	if value == "" {
		secret.Delete(key)
		return nil
	}
	if err := secret.Set(key, value); err != nil {
		return err
	}
	s.store.AddAudit("更新凭证", kind)
	return nil
}

// RevealCredential 返回某个凭证的明文，供「显示」按钮用。
// 钥匙串本来就是为了不落明文，所以这里不做缓存，每次现读。
func (s *WeftService) RevealCredential(ctx context.Context, kind string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	key := secret.Main
	switch kind {
	case "auth":
		key = secret.Auth
	case "client":
		key = secret.Client
	}
	v := strings.TrimSpace(secret.Get(key))
	if v == "" {
		return "", fmt.Errorf("尚未设置")
	}
	return v, nil
}

func (s *WeftService) TestConnection() (string, error) {
	cap, err := s.cp.Probe()
	// 手动点「测试连接」得到的就是最新结论，直接记下来，
	// 省得界面等下一轮轮询才知道这把钥匙到底行不行
	key := strings.TrimSpace(secret.Get(secret.Main))
	s.mu.Lock()
	switch {
	case key == "":
		s.keyProbe = keyProbe{}
	case err == nil:
		s.keyProbe = keyProbe{key: key, at: time.Now()}
	case controlplane.IsRejected(err):
		s.keyProbe = keyProbe{key: key, bad: true, msg: controlplane.Short([]byte(err.Error())), at: time.Now()}
	}
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cap = cap
	s.mu.Unlock()
	s.store.AddAudit("测试连接", cap.ProbeMessage)
	return cap.ProbeMessage, nil
}

// parseExpires 兼容几种时间写法。
// APIKey.Expires 是 controlplane.FmtTS 出来的「2006-01-02 15:04」（空格分隔），
// 直接按 RFC3339 解析会失败，之前自动续期就是卡在这里。
func parseExpires(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("没有到期时间")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析到期时间 %q", s)
}

// EnsureAPIKeyFresh 在当前管理密钥快过期时自动换一把新的。
//
// headscale 没有「续期」接口，只能新建后替换。流程：
//  1. 列出所有 API Key，用前缀认出当前这把
//  2. 剩余天数小于阈值就新建一把（用旧 Key 授权），立刻写进钥匙串
//  3. 旧 Key 吊销，避免留着一把没人管的凭证
//
// 失败不会抛出到界面，只写审计；下次定时器还会再试。
func (s *WeftService) EnsureAPIKeyFresh(window time.Duration) (bool, error) {
	s.mu.Lock()
	cp, cfg := s.cp, s.cfg
	s.mu.Unlock()

	cur := strings.TrimSpace(secret.Get(secret.Main))
	if cur == "" || cp == nil {
		return false, nil
	}

	keys, err := cp.APIKeys()
	if err != nil {
		return false, err
	}
	var self *model.APIKey
	for i := range keys {
		if controlplane.KeyPrefixMatches(cur, keys[i].Prefix) {
			self = &keys[i]
			break
		}
	}
	if self == nil || self.Expires == "" {
		return false, nil // 认不出来或者永不过期，不动
	}
	exp, err := parseExpires(self.Expires)
	if err != nil {
		return false, err
	}
	left := time.Until(exp)
	if left > window {
		return false, nil
	}

	// 新 Key 的有效期给 90 天，比阈值宽得多
	fresh, err := cp.CreateAPIKey(90)
	if err != nil {
		return false, fmt.Errorf("创建新密钥失败: %w", err)
	}
	if err := secret.Set(secret.Main, fresh); err != nil {
		// 新钥匙没存下来就别留着它：服务端那边会变成一把 90 天有效、
		// 本地没有记录的管理凭据，谁也不知道它属于谁。
		// 明文本里没有服务端 ID，只能回列表里按前缀认。
		if keys, kerr := cp.APIKeys(); kerr == nil {
			for i := range keys {
				if !controlplane.KeyPrefixMatches(fresh, keys[i].Prefix) {
					continue
				}
				if eerr := cp.ExpireAPIKey(keys[i].Prefix, keys[i].ID); eerr != nil {
					log.Printf("[keys] 回滚新 API Key 失败，需要手动吊销 %s: %v", keys[i].Prefix, eerr)
				}
				break
			}
		}
		return false, fmt.Errorf("保存新密钥失败: %w", err)
	}
	// 旧 Key 已经没用了，吊销掉；失败不影响本次续期结果
	if err := cp.ExpireAPIKey(self.Prefix, self.ID); err != nil {
		log.Printf("[keys] 吊销旧 API Key 失败（新密钥已生效）: %v", err)
	}
	s.store.AddAudit("自动续期管理密钥", cfg.BaseURL+" · 剩余 "+left.Round(time.Hour).String())
	log.Printf("[keys] 管理密钥已自动续期，旧 Key %s 剩余 %v", self.Prefix, left.Round(time.Hour))
	return true, nil
}

// RenewAPIKey 强制换一把新的管理密钥（不看剩余天数），供界面上的「续期」按钮调用。
// days 是新的有效期，<=0 时按 90 天算。
func (s *WeftService) RenewAPIKey(days int) error {
	if days <= 0 {
		days = 90
	}
	s.mu.Lock()
	cp := s.cp
	s.mu.Unlock()
	if cp == nil {
		return fmt.Errorf("尚未配置服务端")
	}
	cur := strings.TrimSpace(secret.Get(secret.Main))
	if cur == "" {
		return fmt.Errorf("还没有管理密钥，请先在连接设置里填一个")
	}
	keys, err := cp.APIKeys()
	if err != nil {
		return err
	}
	var self *model.APIKey
	for i := range keys {
		if controlplane.KeyPrefixMatches(cur, keys[i].Prefix) {
			self = &keys[i]
			break
		}
	}
	fresh, err := cp.CreateAPIKey(days)
	if err != nil {
		return fmt.Errorf("创建新密钥失败: %w", err)
	}
	if err := secret.Set(secret.Main, fresh); err != nil {
		return fmt.Errorf("保存新密钥失败: %w", err)
	}
	if self == nil {
		// 认不出来就没法吊销旧的，留着两把有效密钥容易搞混
		return fmt.Errorf("新密钥已写入钥匙串，但没在服务端认出旧密钥，去 API Key 页面手动吊销旧的")
	}
	if err := cp.ExpireAPIKey(self.Prefix, self.ID); err != nil {
		log.Printf("[keys] 吊销旧密钥失败（新密钥已生效）: %v", err)
	}
	s.store.AddAudit("手动续期管理密钥", fmt.Sprintf("有效期重置为 %d 天", days))
	return nil
}

// watchAPIKeyExpiry 每小时检查一次，启动后先等半分钟再跑第一次，
// 免得跟自动连接抢带宽。
func (s *WeftService) watchAPIKeyExpiry() {
	go func() {
		time.Sleep(30 * time.Second)
		for {
			if _, err := s.EnsureAPIKeyFresh(7 * 24 * time.Hour); err != nil {
				log.Printf("[keys] 检查管理密钥到期时间失败: %v", err)
			}
			time.Sleep(time.Hour)
		}
	}()
}

// defaultCapabilities 是「还没探测过」时的取值。
// 写操作先放行 —— 真不支持的话服务端会回错，不会假成功；
// 一律给 false 反而会让 UI 把按钮禁掉，用户连试都试不了。
func defaultCapabilities() model.Capabilities {
	return model.Capabilities{
		APIStyle: "v1 REST", CanCreateUser: true,
		CanManageAPIKeys: true, CanManageACL: true,
	}
}

// GetLoginURL 主动要一次网页授权链接。
// 没配授权 Key 时首次连接必须走这条，界面在什么都看不到时给个按钮，
// 不用让用户猜是服务端没回还是界面没刷新。
func (s *WeftService) GetLoginURL() (string, error) {
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng == nil {
		return "", errors.New("还没有可用的引擎")
	}
	if req, ok := eng.(interface {
		RequestLogin(context.Context) (string, error)
	}); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return req.RequestLogin(ctx)
	}
	return eng.LoginURL(), nil
}
