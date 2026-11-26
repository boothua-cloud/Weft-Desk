//go:build windows

package main

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"sync"

	wgtun "github.com/tailscale/wireguard-go/tun"
	"golang.org/x/sys/windows"
)

const winIfName = "Weft"

// Windows 没有 Unix socket 传 fd 这条路，WinTun 又必须管理员权限，
// 所以直接要求进程本身已提权；未提权时给出明确提示，不静默降级。
func newOSRouter() osRouter { return &winRouter{} }

func maybeRunHelper() bool { return false }

type winRouter struct {
	mu      sync.Mutex
	ifName  string
	dev     wgtun.Device
	applied []netip.Prefix
}

func (r *winRouter) Prepare() (wgtun.Device, error) {
	if !isElevated() {
		return nil, fmt.Errorf("接管系统网络需要管理员权限：请退出后以管理员身份重新启动 Weft")
	}
	// 需要 wintun.dll 与可执行文件同目录
	dev, err := wgtun.CreateTUN(winIfName, tunMTU)
	if err != nil {
		return nil, fmt.Errorf("创建 TUN 失败（确认 wintun.dll 已随程序分发）: %w", err)
	}
	r.dev, r.ifName = dev, winIfName
	return dev, nil
}

func (r *winRouter) Configure(dev wgtun.Device, selfIP netip.Addr, routes []netip.Prefix) error {
	if err := runNetsh("interface", "ipv4", "set", "address",
		"name="+r.ifName, "static", selfIP.String(), "255.255.255.255"); err != nil {
		return err
	}
	for _, p := range routes {
		if err := runNetsh("interface", "ipv4", "add", "route", p.Masked().String(), r.ifName); err != nil {
			return err
		}
		r.applied = append(r.applied, p)
	}
	return nil
}

func (r *winRouter) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.applied {
		_ = runNetsh("interface", "ipv4", "delete", "route", p.Masked().String(), r.ifName)
	}
	r.applied = nil
	if r.dev != nil {
		_ = r.dev.Close()
		r.dev = nil
	}
}

func runNetsh(args ...string) error {
	out, err := exec.Command("netsh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh %s 失败: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// systemRoutes 尚未实现：Windows 下先用接口地址做冲突判断。
func systemRoutes(string) []localRoute { return nil }

// shellExecuteRunas 以管理员身份重新启动当前程序。
func shellExecuteRunas(exe string) error {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
