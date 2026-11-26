package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
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
