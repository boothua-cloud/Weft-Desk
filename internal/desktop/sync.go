package desktop

// 应用书签同步的绑定方法。收发逻辑在 internal/appsync，这里只保留
// 前端能看到的入口与同步码的存取。

import (
	"fmt"
	"log"
	"net"

	"github.com/boothua/weft/internal/appsync"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/secret"
)

// syncToken 取本机同步码。mint 为 true 时没有就生成一个。
func (s *WeftService) syncToken(mint bool) string {
	if v := secret.Get(secret.Sync); v != "" || !mint {
		return v
	}
	t := appsync.NewToken()
	if t == "" {
		return ""
	}
	if err := secret.Set(secret.Sync, t); err != nil {
		log.Printf("[sync] 同步码写入钥匙串失败: %v", err)
		return ""
	}
	return t
}

// GetSync 返回同步状态。同步码只在本机界面出现。
func (s *WeftService) GetSync() (string, error) {
	s.mu.Lock()
	cfg, ss := s.cfg, s.syncSrv
	s.mu.Unlock()
	if cfg.SyncEnabled {
		s.syncToken(true)
	}
	ip, host := s.selfPlace()
	apps, _ := s.store.ListApps()
	info := model.SyncInfo{
		Enabled:  cfg.SyncEnabled,
		Port:     cfg.SyncPort,
		Token:    s.syncToken(false),
		SelfIP:   ip,
		Hostname: host,
		Count:    len(apps),
	}
	if ss != nil {
		info.Listening, info.Error = ss.State()
	}
	return toJSON(info)
}

// SetSync 开关同步并立即生效。端口改了会重启监听。
func (s *WeftService) SetSync(enabled bool, port int) (string, error) {
	if port < 1024 || port > 65535 {
		return "", fmt.Errorf("端口要在 1024–65535 之间")
	}
	s.mu.Lock()
	c := s.cfg
	c.SyncEnabled, c.SyncPort = enabled, port
	ss := s.syncSrv
	s.mu.Unlock()
	if err := s.store.SaveConfig(c); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()

	if ss != nil {
		ss.Stop()
	}
	if enabled {
		s.syncToken(true)
		s.startSyncServer()
	}
	return s.GetSync()
}

// RotateSyncToken 换一个同步码，已经配对过的设备要重新输。
func (s *WeftService) RotateSyncToken() (string, error) {
	t := appsync.NewToken()
	if t == "" {
		return "", fmt.Errorf("生成同步码失败")
	}
	if err := secret.Set(secret.Sync, t); err != nil {
		return "", fmt.Errorf("同步码写入钥匙串失败: %w", err)
	}
	if ss := s.syncSrv; ss != nil {
		ss.Stop()
		s.startSyncServer()
	}
	return s.GetSync()
}

func (s *WeftService) startSyncServer() {
	s.mu.Lock()
	cfg, ss := s.cfg, s.syncSrv
	s.mu.Unlock()
	if !cfg.SyncEnabled || ss == nil {
		return
	}
	ss.Start(cfg.SyncPort)
}

func (s *WeftService) stopSyncServer() {
	s.mu.Lock()
	ss := s.syncSrv
	s.mu.Unlock()
	if ss != nil {
		ss.Stop()
	}
}

// PullAppsFrom 从另一台设备拉取书签并合并进本机。
func (s *WeftService) PullAppsFrom(target, token string) (string, error) {
	if s.syncSrv == nil {
		return "", fmt.Errorf("同步还没准备好")
	}
	res, err := s.syncSrv.Pull(target, token)
	if err != nil {
		return "", err
	}
	return toJSON(res)
}

// PushAppsTo 把本机书签推到另一台，由对方合并。
func (s *WeftService) PushAppsTo(target, token string) (string, error) {
	if s.syncSrv == nil {
		return "", fmt.Errorf("同步还没准备好")
	}
	res, err := s.syncSrv.Push(target, token)
	if err != nil {
		return "", err
	}
	return toJSON(res)
}

// selfPlace 取本机在 tailnet 里的地址和名字。引擎没起来时返回空，不报错。
func (s *WeftService) selfPlace() (string, string) {
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng == nil {
		return "", ""
	}
	_, ip, host := eng.Status()
	return ip, host
}

// listenTailnet 给同步服务用：隧道重建后引擎会换实例，这里每次都取当前的。
func (s *WeftService) listenTailnet(port int) (net.Listener, error) {
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng == nil {
		return nil, fmt.Errorf("隧道还没起来")
	}
	return eng.ListenTailnet(port)
}
