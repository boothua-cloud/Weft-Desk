//go:build darwin

package main

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"

	wgtun "github.com/tailscale/wireguard-go/tun"
)

func isDarwin() bool { return true }

func createTUNDevice() (wgtun.Device, error) {
	// 传 "utun" 让系统分配 utunN，避免固定编号被占用
	return wgtun.CreateTUN("utun", tunMTU)
}

func applyInterface(ifName string, ip netip.Addr) error {
	// 点对点写法：本端与对端都填自己的 tailnet 地址
	out, err := exec.Command("/sbin/ifconfig", ifName,
		"inet", ip.String(), ip.String(), "netmask", "255.255.255.255", "up").CombinedOutput()
	if err != nil {
		return fmt.Errorf("配置 %s 地址失败: %s", ifName, strings.TrimSpace(string(out)))
	}
	return nil
}

func addRoute(ifName string, p netip.Prefix) error {
	p = p.Masked()
	out, err := exec.Command("/sbin/route", "-n", "add",
		"-net", p.Addr().String(), "-netmask", maskString(p), "-interface", ifName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("添加路由 %s 失败: %s", p, strings.TrimSpace(string(out)))
	}
	return nil
}

func delRoute(ifName string, p netip.Prefix) error {
	p = p.Masked()
	_ = exec.Command("/sbin/route", "-n", "delete",
		"-net", p.Addr().String(), "-netmask", maskString(p), "-interface", ifName).Run()
	return nil
}

func maskString(p netip.Prefix) string {
	return net.IP(net.CIDRMask(p.Bits(), 32)).String()
}

// systemRoutes 读系统路由表。只看网卡地址是不够的：
// 内网网关可能通过静态路由提供 10.0.1.0/24 这类网段，
// 这类路由不在接口地址里，漏掉就会把内网流量抢进隧道。
func systemRoutes(exceptIface string) []localRoute {
	out, err := exec.Command("/usr/sbin/netstat", "-rn", "-f", "inet").Output()
	if err != nil {
		return nil
	}
	var res []localRoute
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] == "Destination" || f[0] == "default" {
			continue
		}
		ifi := f[3]
		if strings.HasPrefix(ifi, "utun") || ifi == "lo0" || ifi == exceptIface {
			continue
		}
		p, ok := parseBSDNet(f[0])
		if !ok {
			continue
		}
		res = append(res, localRoute{prefix: p, iface: ifi})
	}
	return res
}

// parseBSDNet 把 netstat 的 "10.0.1/24" 补成 "10.0.1.0/24"。
func parseBSDNet(s string) (netip.Prefix, bool) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return netip.Prefix{}, false
	}
	octets := strings.Split(parts[0], ".")
	if len(octets) > 4 {
		return netip.Prefix{}, false
	}
	for len(octets) < 4 {
		octets = append(octets, "0")
	}
	p, err := netip.ParsePrefix(strings.Join(octets, ".") + "/" + parts[1])
	if err != nil {
		return netip.Prefix{}, false
	}
	return p.Masked(), true
}
