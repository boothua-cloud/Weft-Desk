package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"tailscale.com/tailcfg"
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
	store, err := OpenStore()
	if err != nil {
		log.Fatal("打开本地数据库失败: ", err)
	}
	defer store.Close()

	svc := NewWeftService(store)
	defer svc.shutdown()

	cfg := svc.cfg
	fmt.Printf("== 配置 ==\n控制面=%s 地址=%s 引擎=%s hostname=%s 离线预览=%v\n\n",
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
		DiagDump(context.Context, []DiagTarget) []string
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
func diagTargets() []DiagTarget {
	var out []DiagTarget
	add := func(s string) {
		if ip, err := netip.ParseAddr(s); err == nil {
			out = append(out, DiagTarget{IP: ip, Port: 22})
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
		out = append(out, DiagTarget{IP: ip, Port: port})
	}
	for _, a := range os.Args[1:] {
		if !strings.HasPrefix(a, "-") {
			add(a)
		}
	}
	if len(out) == 0 {
		out = []DiagTarget{
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
		DiagPeer(context.Context, netip.Addr, uint16) (DiagReport, error)
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

// matchIPSet 判断地址是否落在规则里的一个 IP 集合中。
// 控制面下发的写法有四种：*、单个 IP、CIDR、a-b 区间。
func matchIPSet(spec string, ip netip.Addr) bool {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return false
	case spec == "*":
		return true
	}
	if a, err := netip.ParseAddr(spec); err == nil {
		return a.Unmap() == ip
	}
	if p, err := netip.ParsePrefix(spec); err == nil {
		if p.Addr().Is4In6() {
			return p.Contains(netip.AddrFrom16(ip.As16()))
		}
		return p.Contains(ip)
	}
	if lo, hi, ok := strings.Cut(spec, "-"); ok {
		a, e1 := netip.ParseAddr(strings.TrimSpace(lo))
		b, e2 := netip.ParseAddr(strings.TrimSpace(hi))
		if e1 == nil && e2 == nil {
			return a.Compare(ip) <= 0 && ip.Compare(b) <= 0
		}
	}
	return false
}

// aclAllows 看包过滤规则里有没有放行 self → dst:port。
// 这不是完整的规则求值，只回答「ACL 里有没有这一条」：
// 拨测不通时用它区分「ACL 拦了」和「对端服务没开」。
func aclAllows(rules []tailcfg.FilterRule, self, dst netip.Addr, port uint16) bool {
	for _, r := range rules {
		// IPProto 为空表示 TCP/UDP/ICMP 全管；带了协议就只认 TCP(6)
		if len(r.IPProto) > 0 {
			tcp := false
			for _, p := range r.IPProto {
				if p == 6 {
					tcp = true
					break
				}
			}
			if !tcp {
				continue
			}
		}
		srcHit := false
		for _, s := range r.SrcIPs {
			if matchIPSet(s, self) {
				srcHit = true
				break
			}
		}
		if !srcHit {
			continue
		}
		for _, d := range r.DstPorts {
			if matchIPSet(d.IP, dst) && d.Ports.Contains(port) {
				return true
			}
		}
	}
	return false
}

// diagVerdict 把实测结果翻成一句话和几条可能原因。
// 判断顺序按「越靠近链路层越先排除」，用户不会被引去改 ACL 但其实是对端离线。
func diagVerdict(r DiagReport) (string, []string) {
	target := net.JoinHostPort(r.IP, strconv.Itoa(r.Port))

	if !r.Online {
		return "这台设备离线",
			[]string{
				"控制面显示它现在不在线，先确认对方开着机、连着网",
				"对方上线后会自动恢复，不用改配置",
			}
	}

	linkOK := r.PingLost < 3
	linkNote := "链路正常"
	switch {
	case r.PingLost == 3:
		linkNote = "链路不通"
	case r.PingLost > 0:
		linkNote = fmt.Sprintf("链路不稳，探测丢了 %d 次", r.PingLost)
	case r.Path == "DERP":
		linkNote = "走 DERP 中继，比直连慢"
	default:
		linkNote = "直连"
	}

	if r.DialErr == "" {
		// 端口通了但探测全丢：少见，通常是中间设备限速探测包，
		// 实际数据能过。别把它说成故障。
		if !linkOK {
			return fmt.Sprintf("能连上 %s，但探测 3 次全丢", target),
				[]string{"端口是通的，探测包可能被中间设备限速拦了，不影响使用"}
		}
		// 返回空切片而不是 nil：JSON 里要是 null，前端一读 length 就崩。
		return fmt.Sprintf("能连上 %s · %s", target, linkNote), []string{}
	}

	if !linkOK {
		hints := []string{}
		if r.PingErr != "" {
			hints = append(hints, "探测报错："+r.PingErr)
		}
		if r.Path == "DERP" && r.Relay != "" {
			hints = append(hints, fmt.Sprintf("目前只能靠 DERP（%s）中继。中继连不上时，两端都换了网络也还是不通", r.Relay))
		} else {
			hints = append(hints, "两端可能都挡了 UDP，直连打不通。看总览页的 DERP 状态")
		}
		if r.Handshake == "从未" {
			hints = append(hints, "和对端从来没握过手：先去控制面确认两边在同一账号、同一 tailnet")
		}
		return fmt.Sprintf("链路不通，%s 连不上", target), hints
	}

	// 链路通、端口不通：这里是 ACL 和服务的分界
	if r.ACLChecked && !r.ACLAllowed {
		return fmt.Sprintf("ACL 没放行 %s", target),
			[]string{
				"策略里没有从本机到这个地址、这个端口的规则",
				"去 ACL 页加一条，保存后立刻生效，不用重连",
			}
	}
	hints := []string{
		"链路是通的，多半是对端这个端口没开服务，换个常用端口试试",
		"对端防火墙可能只放行了部分端口",
	}
	if r.ACLChecked {
		hints = append(hints, "ACL 里有放行规则，所以不是策略问题")
	}
	if r.DialErr != "" {
		hints = append(hints, "拨测报错："+r.DialErr)
	}
	return fmt.Sprintf("链路通，但 %s 连不上", target), hints
}
