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

var loginURLRe = regexp.MustCompile(`https://\S+`)

// tsnetStateDir 持久化节点私钥与登录态。重开 App 复用同一身份、不重新注册，
// 这是「关掉再打开连不上」的根治点。按控制面分目录，避免身份串台。
func tsnetStateDir(c Config) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	// 默认服务端沿用老目录（升级不重注册），其他服务端各自一个目录，避免身份串台
	suffix := "tsnet-" + c.ControlPlane
	if c.ProfileID != "" && c.ProfileID != "default" {
		suffix += "-" + c.ProfileID
	}
	dir := filepath.Join(base, "Weft", suffix)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
