package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boothua/weft/internal/engine"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/store"
)

// wantsDiag 判断是否是诊断模式：weft --diag [IP...]
func wantsDiag() bool {
	for _, a := range os.Args[1:] {
		if a == "--diag" || a == "-diag" {
			return true
		}
	}
	return false
}

// runDiag 不进 GUI，直接拉起隧道并输出分层诊断。
// 目的只有一个：用数据定位「包卡在 netmap、magicsock、engine 还是对端」。
func runDiag() {
	targets := diagTargets()
	store, err := store.Open()
	if err != nil {
		log.Fatal("打开本地数据库失败: ", err)
	}
	defer store.Close()

	svc := NewWeftService(store)
	defer svc.shutdown()

	cfg := svc.cfg
	fmt.Printf("== 配置 ==\n服务端=%s 地址=%s 引擎=%s hostname=%s 离线预览=%v\n\n",
		cfg.ControlPlane, cfg.BaseURL, cfg.TunnelEngine, cfg.Hostname, cfg.Demo)
	if cfg.Demo {
		fmt.Println("离线预览已开启，不会建立任何连接。请先到设置页关闭。")
		return
	}

	fmt.Println("== 建立隧道 ==")
	start := time.Now()
	if _, err := svc.Connect(); err != nil {
		fmt.Printf("连接失败（%v）: %v\n", time.Since(start).Round(time.Millisecond), err)
		fmt.Println("提示：如果 GUI 版正在运行并已连接，助手会拒绝第二个 TUN，请先退出 GUI。")
		return
	}
	fmt.Printf("引擎已拉起（%v），等节点上线…\n\n", time.Since(start).Round(time.Millisecond))

	// 等 running 与自身 IP 就绪，最多 40 秒。
	deadline := time.Now().Add(40 * time.Second)
	var selfIP string
	for time.Now().Before(deadline) {
		if _, ip, _ := svc.engine.Status(); ip != "" {
			selfIP = ip
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("自身 IP=%q\n\n", selfIP)

	eng := svc.engine
	dumper, ok := eng.(interface {
		DiagDump(context.Context, []engine.DiagTarget) []string
	})
	if !ok {
		fmt.Printf("当前引擎 %s 不支持分层诊断（只有 system 引擎有）\n", eng.Name())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fmt.Println("== 分层诊断 ==")
	for _, line := range dumper.DiagDump(ctx, targets) {
		fmt.Println(line)
	}
}

// diagTargets 解析命令行目标，支持 `IP` 或 `IP:端口`，默认测 22 端口。
func diagTargets() []engine.DiagTarget {
	var out []engine.DiagTarget
	add := func(s string) {
		if ip, err := netip.ParseAddr(s); err == nil {
			out = append(out, engine.DiagTarget{IP: ip, Port: 22})
			return
		}
		host, portStr, err := net.SplitHostPort(s)
		if err != nil {
			return
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return
		}
		var port uint16 = 22
		if n, err := strconv.Atoi(portStr); err == nil && n > 0 && n < 65536 {
			port = uint16(n)
		}
		out = append(out, engine.DiagTarget{IP: ip, Port: port})
	}
	for _, a := range os.Args[1:] {
		if !strings.HasPrefix(a, "-") {
			add(a)
		}
	}
	if len(out) == 0 {
		out = []engine.DiagTarget{
			{IP: netip.MustParseAddr("100.64.0.3"), Port: 22},
			{IP: netip.MustParseAddr("100.64.0.5"), Port: 22},
		}
	}
	return out
}

// ---------- 界面诊断 ----------

// Diagnose 实测一台设备：链路、ACL 放行、目标端口。
// port 传 0 按 22 算。整个过程最长 45 秒（三次 disco 加一次拨测）。
func (s *WeftService) Diagnose(ip string, port int) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", fmt.Errorf("%q 不是有效的 IP", ip)
	}
	if port <= 0 || port > 65535 {
		port = 22
	}
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng == nil {
		return "", errors.New("还没连上，先在总览页连接")
	}
	d, ok := eng.(interface {
		DiagPeer(context.Context, netip.Addr, uint16) (model.DiagReport, error)
	})
	if !ok {
		return "", errors.New("当前引擎不支持诊断")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	rep, err := d.DiagPeer(ctx, addr.Unmap(), uint16(port))
	if err != nil {
		return "", err
	}
	// nil 切片会被序列化成 null，前端拿它当数组用就炸。统一成 []。
	if rep.Hints == nil {
		rep.Hints = []string{}
	}
	out, err := json.Marshal(rep)
	return string(out), err
}

// PeerPorts 列出每台设备上「登记过的服务」——应用书签里的地址、
// 端口转发规则里的目标端口。
//
// 为什么需要它：全局填一个 22 然后轮一遍所有设备，测出来的只是
// 「SSH 通不通」，跟用户实际要访问的东西没关系。诊断应该回答
// 「我那台机器上的 nginx 能不能访问」。
func (s *WeftService) PeerPorts() (string, error) {
	apps, _ := s.store.ListApps()
	rules, _ := s.store.ListForwards()

	// 书签和转发规则里常写主机名（ubuntu），要换成 100.x 才能对上设备卡片
	byName := map[string]string{}
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng != nil {
		if pr, ok := eng.(interface{ Peers() []model.PeerInfo }); ok {
			for _, p := range pr.Peers() {
				if p.IP == "" {
					continue
				}
				byName[strings.ToLower(p.IP)] = p.IP
				if p.Name != "" {
					byName[strings.ToLower(p.Name)] = p.IP
				}
			}
		}
	}
	resolve := func(host string) string {
		h := strings.ToLower(strings.TrimSpace(host))
		if h == "" {
			return ""
		}
		if ip, ok := byName[h]; ok {
			return ip
		}
		if a, err := netip.ParseAddr(h); err == nil {
			return a.String()
		}
		return ""
	}

	out := []model.PeerPort{}
	seen := map[string]bool{}
	add := func(ip string, port int, label, kind string) {
		if ip == "" || port <= 0 || port > 65535 {
			return
		}
		k := ip + ":" + strconv.Itoa(port)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, model.PeerPort{IP: ip, Port: port, Label: label, Kind: kind})
	}

	for _, a := range apps {
		u, err := url.Parse(strings.TrimSpace(a.URL))
		if err != nil || u.Host == "" {
			continue
		}
		ip := resolve(u.Hostname())
		if ip == "" {
			continue
		}
		// 没写端口的按协议默认填，用户点它测的就是浏览器实际连的那个
		port := 443
		if u.Scheme == "http" {
			port = 80
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		}
		add(ip, port, a.Name, "app")
	}
	for _, r := range rules {
		add(resolve(r.Target), r.TargetPort, r.Name, "forward")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IP != out[j].IP {
			return out[i].IP < out[j].IP
		}
		return out[i].Port < out[j].Port
	})
	return toJSON(out)
}

// matchIPSet 判断地址是否落在规则里的一个 IP 集合中。
// 服务端下发的写法有四种：*、单个 IP、CIDR、a-b 区间。
