package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/netio"
)

// 本地端口转发：把 127.0.0.1 上的端口送进 tailnet，
// 顺带管 SOCKS5 与自动转发开关。

// startForwards 在隧道起来后调用。AutoForward 关闭时交由用户手动点「启动转发」。
func (s *WeftService) startForwards() error {
	s.mu.Lock()
	auto := s.cfg.AutoForward
	s.mu.Unlock()
	if !auto {
		return nil
	}
	s.mu.Lock()
	s.forwardOn = true
	s.mu.Unlock()
	return s.syncForwards()
}

func (s *WeftService) stopForwards() {
	s.mu.Lock()
	s.forwardOn = false
	s.mu.Unlock()
	_ = s.syncForwards()
}

func (s *WeftService) syncForwards() error {
	s.mu.Lock()
	cfg, on, fw := s.cfg, s.forwardOn, s.fw
	s.mu.Unlock()
	if fw == nil {
		return nil
	}
	if !on {
		_ = fw.Apply(nil, false, cfg.SocksPort, cfg.ForwardBind)
		return nil
	}
	rules, err := s.store.ListForwards()
	if err != nil {
		return err
	}
	return fw.Apply(rules, cfg.SocksEnabled, cfg.SocksPort, cfg.ForwardBind)
}

func (s *WeftService) ListForwards() (string, error) {
	rules, err := s.store.ListForwards()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	cfg, on, fw, eng := s.cfg, s.forwardOn, s.fw, s.engine.Name()
	s.mu.Unlock()
	statuses := []model.ForwardStatus{}
	socks := model.ForwardStatus{ID: "socks5", State: "disabled"}
	if fw != nil {
		statuses, socks = fw.Statuses(rules, cfg.SocksEnabled && on, cfg.SocksPort)
	}
	return toJSON(model.ForwardView{
		Rules: rules, Statuses: statuses, Socks: socks,
		SocksEnabled: cfg.SocksEnabled, SocksPort: cfg.SocksPort,
		AutoForward: cfg.AutoForward, Power: on,
		Bind:   netio.NormalizeBind(cfg.ForwardBind),
		Engine: eng,
	})
}

func (s *WeftService) SaveForward(raw string) error {
	var r model.ForwardRule
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return err
	}
	if r.ID == "" {
		r.ID = fmt.Sprintf("fwd-%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Target) == "" {
		return fmt.Errorf("名称和目标地址不能为空")
	}
	if r.TargetPort < 1 || r.TargetPort > 65535 || r.LocalPort < 1 || r.LocalPort > 65535 {
		return fmt.Errorf("端口必须是 1–65535")
	}
	if r.Kind == "" {
		r.Kind = "custom"
	}
	if err := s.store.SaveForward(r, int(time.Now().UnixNano()/1e6)); err != nil {
		return err
	}
	s.store.AddAudit("保存端口映射", r.Name)
	return s.syncForwards()
}

func (s *WeftService) DeleteForward(id string) error {
	if err := s.store.DeleteForward(id); err != nil {
		return err
	}
	s.store.AddAudit("删除端口映射", id)
	return s.syncForwards()
}

func (s *WeftService) ToggleForward(id string, enabled bool) error {
	rules, err := s.store.ListForwards()
	if err != nil {
		return err
	}
	for i, r := range rules {
		if r.ID == id {
			r.Enabled = enabled
			if err = s.store.SaveForward(r, i); err != nil {
				return err
			}
			return s.syncForwards()
		}
	}
	return fmt.Errorf("未找到规则: %s", id)
}

// SetForwardOptions 一次保存三个全局开关，避免多次写库。
func (s *WeftService) SetForwardOptions(autoForward, socksEnabled bool, socksPort int) error {
	if socksPort < 1 || socksPort > 65535 {
		return fmt.Errorf("SOCKS5 端口必须是 1–65535")
	}
	s.mu.Lock()
	c := s.cfg
	c.AutoForward, c.SocksEnabled, c.SocksPort = autoForward, socksEnabled, socksPort
	s.mu.Unlock()
	if err := s.store.SaveConfig(c); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	return s.syncForwards()
}

// SetForwardBind 切换本地监听范围：仅本机（127.0.0.1）或同网段可达（0.0.0.0）。
// 切换会重建所有监听器，已经建立的连接会断。
func (s *WeftService) SetForwardBind(bind string) error {
	bind = netio.NormalizeBind(bind)
	s.mu.Lock()
	c := s.cfg
	c.ForwardBind = bind
	s.mu.Unlock()
	if err := s.store.SaveConfig(c); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	s.store.AddAudit("转发监听", map[string]string{"127.0.0.1": "仅本机", "0.0.0.0": "同网段可达"}[bind])
	return s.syncForwards()
}

// SetForwardPower 是「启动 / 停止转发」总开关，不影响引擎连接状态。
func (s *WeftService) SetForwardPower(on bool) error {
	s.mu.Lock()
	s.forwardOn = on
	s.mu.Unlock()
	s.store.AddAudit("转发开关", map[bool]string{true: "启动", false: "停止"}[on])
	return s.syncForwards()
}

func (s *WeftService) ForwardSnippet(id string) (string, error) {
	rules, err := s.store.ListForwards()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	port := s.cfg.SocksPort
	s.mu.Unlock()
	for _, r := range rules {
		if r.ID == id {
			return netio.Snippets(r, port)
		}
	}
	return "", fmt.Errorf("未找到规则: %s", id)
}

// TestPeer 用 TCP 直接探目标端口。默认走系统路由（TUN），
// 也可以指定 netstack 走进程内栈，用来区分「TUN 层不通」和「网络层不通」。
func (s *WeftService) TestPeer(host string, port int, via string) (string, error) {
	if host == "" || port < 1 || port > 65535 {
		return "", fmt.Errorf("请填主机与端口")
	}
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng == nil {
		return "", fmt.Errorf("引擎尚未就绪")
	}
	var d model.DialFunc
	label := "系统 TUN"
	if via == "netstack" {
		label = "netstack 用户态"
		if nd, ok := eng.(interface{ NetstackDialer() model.DialFunc }); ok {
			d = nd.NetstackDialer()
		}
	}
	if d == nil {
		d = eng.Dialer()
		if via == "netstack" {
			label = eng.Name() + " 引擎"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := d(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("%s:%d 连接失败（%v，%s）：%w",
			host, port, time.Since(start).Round(time.Millisecond), label, err)
	}
	_ = conn.Close()
	return fmt.Sprintf("%s:%d 连接成功，耗时 %v（%s）",
		host, port, time.Since(start).Round(time.Millisecond), label), nil
}
