package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tailscale.com/net/socks5"
)

// forwardHub 在 127.0.0.1 上提供两类出口，让「系统里的外部工具」也能进 tailnet：
//
//  1. 固定端口映射：127.0.0.1:LocalPort → Target:TargetPort，纯 TCP 字节转发，
//     不解析任何协议，因此 SSH / MySQL / PostgreSQL / Redis / RDP / VNC 等
//     任意 TCP 客户端都能直接用，只要把 host 填 127.0.0.1、port 填本地端口。
//  2. SOCKS5：一个通用动态代理，适合目标地址不固定、或工具本身支持 SOCKS
//     的场景（ssh -o ProxyCommand、proxychains、部分数据库客户端）。
//
// 出站一律走 TunnelEngine.Dialer() —— tsnet 下是进程内 userspace 栈，
// 所以不需要安装 Tailscale、不需要 root、不建 TUN、不改系统路由和 DNS。
type forwardHub struct {
	mu       sync.Mutex
	dial     dialFunc
	entries  map[string]*forwardEntry
	socks    *forwardEntry
	socksOn  bool
	socksErr string
	// bind 是本地监听地址：127.0.0.1 只给本机用，0.0.0.0 让同网段的其他设备也能连进来。
	bind string
}

type forwardEntry struct {
	ln     net.Listener
	rule   ForwardRule
	state  string // listening | stopped | error
	err    string
	active atomic.Int64
	bytes  atomic.Int64
}

func newForwardHub(d dialFunc) *forwardHub {
	return &forwardHub{dial: d, entries: map[string]*forwardEntry{}, bind: bindLocalhost}
}

const (
	bindLocalhost = "127.0.0.1"
	bindAny       = "0.0.0.0"
)

// normalizeBind 只认这两个值，其余一律回落到仅本机，避免误开公网监听。
func normalizeBind(v string) string {
	if v == bindAny {
		return bindAny
	}
	return bindLocalhost
}

func (h *forwardHub) setDialer(d dialFunc) {
	h.mu.Lock()
	h.dial = d
	h.mu.Unlock()
}

func (h *forwardHub) dialer() dialFunc {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dial
}

// dialVia 把当前引擎的 dialer 包一层，socks5.Server 持有的闭包因此能跟随引擎切换。
func (h *forwardHub) dialVia(ctx context.Context, network, addr string) (net.Conn, error) {
	d := h.dialer()
	if d == nil {
		return nil, fmt.Errorf("隧道引擎未就绪")
	}
	return d(ctx, network, addr)
}

// apply 全量同步规则：新增/参数变化的重新监听，删除或禁用的关掉。
// 规则里 LocalPort 是持久化的固定值，重启 App 不变，外部工具配置不用改。
func (h *forwardHub) apply(rules []ForwardRule, socksEnabled bool, socksPort int, bind string) error {
	want := map[string]ForwardRule{}
	for _, r := range rules {
		if r.Enabled && r.ID != "" {
			want[r.ID] = r
		}
	}

	h.mu.Lock()
	// 监听地址变了，已有监听器全部作废，下面按新地址重建
	if b := normalizeBind(bind); b != h.bind {
		h.bind = b
		for id, e := range h.entries {
			h.stopLocked(e)
			delete(h.entries, id)
		}
		if h.socks != nil {
			h.stopLocked(h.socks)
			h.socks = nil
		}
	}
	for id, e := range h.entries {
		r, ok := want[id]
		if !ok || r.LocalPort != e.rule.LocalPort || r.Target != e.rule.Target || r.TargetPort != e.rule.TargetPort {
			h.stopLocked(e)
			delete(h.entries, id)
		}
	}
	var firstErr error
	for id, r := range want {
		e := h.entries[id]
		if e == nil {
			ne := &forwardEntry{rule: r}
			if err := h.startLocked(ne, false); err != nil && firstErr == nil {
				firstErr = err
			}
			h.entries[id] = ne
		} else {
			e.rule = r
		}
	}
	// SOCKS5 单独管理
	h.socksOn = socksEnabled
	if socksEnabled {
		if h.socks == nil || h.socks.rule.LocalPort != socksPort {
			if h.socks != nil {
				h.stopLocked(h.socks)
				h.socks = nil
			}
			ne := &forwardEntry{rule: ForwardRule{ID: "socks5", LocalPort: socksPort}}
			h.socksErr = ""
			if err := h.startLocked(ne, true); err != nil {
				h.socksErr = err.Error()
				if firstErr == nil {
					firstErr = err
				}
			}
			h.socks = ne
		}
	} else if h.socks != nil {
		h.stopLocked(h.socks)
		h.socks = nil
		h.socksErr = ""
	}
	h.mu.Unlock()
	return firstErr
}

func (h *forwardHub) startLocked(e *forwardEntry, socks bool) error {
	if e.rule.LocalPort <= 0 || e.rule.LocalPort > 65535 {
		e.state, e.err = "error", "本地端口必须是 1–65535"
		return fmt.Errorf("%s", e.err)
	}
	// 空 host 表示所有接口：IPv4 与 IPv6 都能连进来，比只绑 0.0.0.0 更实用
	host := h.bind
	if host == bindAny {
		host = ""
	}
	addr := net.JoinHostPort(host, strconv.Itoa(e.rule.LocalPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		e.state, e.err = "error", fmt.Sprintf("端口 %d 被占用或不可用：%v", e.rule.LocalPort, err)
		return fmt.Errorf("%s", e.err)
	}
	e.ln, e.state, e.err = ln, "listening", ""

	if socks {
		s5 := &socks5.Server{
			Logf:   func(f string, a ...any) { log.Printf("[socks5] "+f, a...) },
			Dialer: h.dialVia,
		}
		go func() {
			if err := s5.Serve(ln); err != nil && !isClosedErr(err) {
				log.Printf("[socks5] 服务结束: %v", err)
			}
		}()
	} else {
		go h.acceptLoop(e, ln)
	}
	return nil
}

func (h *forwardHub) stopLocked(e *forwardEntry) {
	if e == nil {
		return
	}
	if e.ln != nil {
		_ = e.ln.Close()
		e.ln = nil
	}
	e.state, e.err = "stopped", ""
}

func isClosedErr(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "use of closed network connection") ||
		strings.Contains(err.Error(), "closed"))
}

func (h *forwardHub) acceptLoop(e *forwardEntry, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener 被关闭（换监听地址或规则被删）
		}
		go h.pipe(e, c)
	}
}

// pipe 只做字节搬运，不碰协议。上游连接用引擎 dialer 发起，
// 因此外部工具看到的是普通 TCP，tailnet 侧看到的是来自 Weft 节点的连接。
func (h *forwardHub) pipe(e *forwardEntry, client net.Conn) {
	defer client.Close()
	target := net.JoinHostPort(e.rule.Target, strconv.Itoa(e.rule.TargetPort))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	upstream, err := h.dialVia(ctx, "tcp", target)
	cancel()
	if err != nil {
		log.Printf("[forward] %s → %s 失败: %v", e.rule.Name, target, err)
		return
	}
	defer upstream.Close()

	e.active.Add(1)
	defer e.active.Add(-1)
	if tc, ok := client.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	// 任一侧结束即视为会话结束：关掉两端打断另一个方向的 Copy。
	// 上游是 netstack 的 Conn，不一定支持 CloseWrite，所以不做半关闭假设。
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(&countWriter{w: upstream, n: &e.bytes}, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(&countWriter{w: client, n: &e.bytes}, upstream)
		done <- struct{}{}
	}()
	<-done
	_ = client.Close()
	_ = upstream.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// statuses 给 UI 用：每条规则的监听状态、活跃连接数、累计字节、错误。
func (h *forwardHub) statuses(rules []ForwardRule, socksEnabled bool, socksPort int) ([]ForwardStatus, ForwardStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ForwardStatus, 0, len(rules))
	for _, r := range rules {
		st := ForwardStatus{ID: r.ID, State: "stopped"}
		if !r.Enabled {
			st.State = "disabled"
		} else if e := h.entries[r.ID]; e != nil {
			st.State, st.Error = e.state, e.err
			st.Active, st.Bytes = e.active.Load(), e.bytes.Load()
			st.ListenAddr = net.JoinHostPort(h.bind, strconv.Itoa(r.LocalPort))
		}
		out = append(out, st)
	}
	s := ForwardStatus{ID: "socks5", State: "stopped"}
	if !socksEnabled {
		s.State = "disabled"
	} else {
		s.State, s.Error = "listening", h.socksErr
		if h.socksErr != "" {
			s.State = "error"
		}
		if h.socks != nil {
			s.Active, s.Bytes = h.socks.active.Load(), h.socks.bytes.Load()
		}
		s.ListenAddr = net.JoinHostPort(h.bind, strconv.Itoa(socksPort))
	}
	return out, s
}

func (h *forwardHub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, e := range h.entries {
		h.stopLocked(e)
		delete(h.entries, id)
	}
	if h.socks != nil {
		h.stopLocked(h.socks)
		h.socks = nil
	}
	h.socksOn = false
}

// ---------- 外部工具可直接复制的命令片段 ----------

// snippets 生成各类工具的连接写法。返回 JSON 字符串，前端按 kind 展示对应 tab。
func snippets(r ForwardRule, socksPort int) (string, error) {
	host := "127.0.0.1"
	lp := strconv.Itoa(r.LocalPort)
	alias := strings.ToLower(strings.TrimSpace(r.Name))
	alias = strings.NewReplacer(" ", "-", "_", "-", "/", "-").Replace(alias)
	if alias == "" {
		alias = "weft-host"
	}
	cfg := fmt.Sprintf(`# 追加到 ~/.ssh/config
Host %s
    HostName %s
    Port %s
    User root
    HostKeyAlias %s
    UserKnownHostsFile ~/.ssh/known_hosts_weft
    StrictHostKeyChecking accept-new`, alias, host, lp, alias)

	out := map[string]string{
		"label":     r.Name + "  ·  " + host + ":" + lp + " → " + r.Target + ":" + strconv.Itoa(r.TargetPort),
		"generic":   "地址 " + host + "    端口 " + lp + "    （任意 TCP 客户端）",
		"ssh":       "ssh -p " + lp + " root@" + host,
		"sshconfig": cfg,
		"mysql":     "mysql -h " + host + " -P " + lp + " -u root -p",
		"postgres":  "psql -h " + host + " -p " + lp + " -U postgres -d postgres",
		"redis":     "redis-cli -h " + host + " -p " + lp,
		"jdbc":      "jdbc:mysql://" + host + ":" + lp + "/<database>?useSSL=false",
		"socks": fmt.Sprintf("# 动态目标：工具支持 SOCKS5 时填 127.0.0.1:%d\nssh -o ProxyCommand='nc -X 5 -x 127.0.0.1:%d %%h %%p' root@%s",
			socksPort, socksPort, r.Target),
	}
	b, err := json.Marshal(out)
	return string(b), err
}
