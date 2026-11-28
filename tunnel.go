package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TunnelEngine 是隧道实现的抽象。目前只有系统接管（真实 TUN + 系统路由）一种，
// 保留接口是为了端口转发、代理与诊断能拿到统一的 Dialer / ResolveURL。
type TunnelEngine interface {
	Name() string
	Available() bool
	Status() (running bool, selfIP, hostname string)
	Up(c Config) error
	Down() error
	// ResolveURL 把 tailnet 内地址改写成本机可访问地址。
	ResolveURL(raw string) (string, error)
	LoginURL() string
	// Dialer 给本地端口转发 / SOCKS5 用。
	Dialer() dialFunc
	// ListenTailnet 在节点的 100.x 地址上监听端口，只有同账号设备能连上。
	// 节点没上线时会等，最长 90 秒。
	ListenTailnet(port int) (net.Listener, error)
	Shutdown()
}

// newTunnelEngine 只有一种实现：真实 TUN + 系统路由。
// 进程内 userspace 栈已移除——外部 SSH / 数据库工具直连才是这个应用的用途。
func newTunnelEngine(_ Config) TunnelEngine {
	return newSystemTUNEngine()
}

// tsnetLogf 只保留链路关键事件。tsnet 的 Logf 会把每次 reconfig、
// netcheck、map 更新全打出来，几万行日志会把真正的错误埋掉。
func tsnetLogf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	// WEFT_DEBUG=1 时输出全部日志，用于排查隧道问题
	if os.Getenv("WEFT_DEBUG") != "" {
		log.Printf("[ts] %s", msg)
		return
	}
	for _, k := range []string{
		"Switching ipn state",
		"Handshake did not complete",
		"home is now",
		"UDP is blocked",
		"no associated peer node",
		"magicsock",
		"derp",
		"DERP",
	} {
		if strings.Contains(msg, k) {
			log.Printf("[ts] %s", msg)
			return
		}
	}
}

// headscale 自建常常跑在 http 上，注册链接也是 http://host/register/xxx，
// 只认 https 会直接漏掉。引号和尖括号不进链接，随手清理尾部的标点。
var loginURLRe = regexp.MustCompile(`https?://[^\s"'<>]+`)

// cleanAuthURL 去掉日志里粘在链接尾部的标点。
// tailscale 的日志是 `... go to: <url>`，headscale 的链接本身以 key 结尾，
// 但偶尔会被引号或句号裹住。
func cleanAuthURL(u string) string {
	return strings.TrimRight(u, ".,;:!?)]}'\"")
}

// tsnetStateDir 持久化节点私钥与登录态。重开 App 复用同一身份、不重新注册，
// 这是「关掉再打开连不上」的根治点。按服务端分目录，避免身份串台。
func tsnetStateDir(c Config) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	// 默认服务端沿用老目录（升级不重注册），其他服务端各自一个目录，避免身份串台
	suffix := "tsnet-" + safeDirPart(c.ControlPlane)
	if c.ProfileID != "" && c.ProfileID != "default" {
		suffix += "-" + safeDirPart(c.ProfileID)
	}
	dir := filepath.Join(base, "Weft", suffix)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// safeDirPart 把配置里的字符串压成可以安全当目录名的一段。
// ControlPlane / ProfileID 都来自前端，`..` 和 `/` 不能直接拼进路径。
func safeDirPart(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "default"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}
