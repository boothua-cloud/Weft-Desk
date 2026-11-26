//go:build linux

package main

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"

	wgtun "github.com/tailscale/wireguard-go/tun"
)

func isDarwin() bool { return false }

func createTUNDevice() (wgtun.Device, error) {
	return wgtun.CreateTUN("weft0", tunMTU)
}

func applyInterface(ifName string, ip netip.Addr) error {
	if err := runCmd("ip", "addr", "add", ip.String()+"/32", "dev", ifName); err != nil {
		return err
	}
	if err := runCmd("ip", "link", "set", ifName, "up"); err != nil {
		return err
	}
	return runCmd("ip", "link", "set", "dev", ifName, "mtu", fmt.Sprint(tunMTU))
}

func addRoute(ifName string, p netip.Prefix) error {
	return runCmd("ip", "route", "add", p.Masked().String(), "dev", ifName)
}

func delRoute(ifName string, p netip.Prefix) error {
	_ = runCmd("ip", "route", "del", p.Masked().String(), "dev", ifName)
	return nil
}

func runCmd(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s 失败: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// systemRoutes 读内核路由表，避免把已由内网网关提供的网段抢进隧道。
func systemRoutes(exceptIface string) []localRoute {
	out, err := exec.Command("ip", "-4", "route", "show").Output()
	if err != nil {
		return nil
	}
	var res []localRoute
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[0] == "default" {
			continue
		}
		ifi := ""
		for i, t := range f {
			if t == "dev" && i+1 < len(f) {
				ifi = f[i+1]
			}
		}
		if ifi == "" || ifi == exceptIface {
			continue
		}
		p, err := netip.ParsePrefix(f[0])
		if err != nil {
			continue
		}
		res = append(res, localRoute{prefix: p.Masked(), iface: ifi})
	}
	return res
}
