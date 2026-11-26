//go:build darwin || linux

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 网段冲突必须被剔除，否则会把本机局域网流量导进隧道，直接断网。
func TestSplitConflictsDropsLocalOverlap(t *testing.T) {
	routes := []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("10.20.0.0/16"),
	}
	local := []localRoute{
		{prefix: netip.MustParsePrefix("192.168.1.0/24"), iface: "en0"},
		{prefix: netip.MustParsePrefix("10.0.0.0/8"), iface: "utun6"},
	}
	plan := splitConflicts(routes, local)

	// 只有与本机完全相同的前缀才跳过（会覆盖原路由）；
	// 10.20.0.0/16 虽然落在本机 10/8 之内，但更长，最长前缀匹配下
	// 本机更具体的网段仍优先，所以应当安装。
	if got := prefixStrings(plan.keep); len(got) != 2 || got[0] != "100.64.0.0/10" || got[1] != "10.20.0.0/16" {
		t.Fatalf("保留路由不符: %v", got)
	}
	if len(plan.skipped) != 1 {
		t.Fatalf("只应跳过完全相同的 192.168.1.0/24，实际 %v", plan.skipped)
	}
}

// 本机已有的更具体路由不应阻止较宽的 tailnet 段安装。
func TestSplitConflictsAllowsLessSpecific(t *testing.T) {
	routes := []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10")}
	local := []localRoute{
		{prefix: netip.MustParsePrefix("100.64.0.2/32"), iface: "utun4"},
	}
	plan := splitConflicts(routes, local)
	if len(plan.keep) != 1 || len(plan.skipped) != 0 {
		t.Fatalf("100.64/10 应可安装（本机只有 /32），实际 keep=%v skipped=%v",
			prefixStrings(plan.keep), plan.skipped)
	}
}

// 前缀完全相同才是真冲突：会覆盖本机原有路由。
func TestSplitConflictsIdenticalIsConflict(t *testing.T) {
	routes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	local := []localRoute{{prefix: netip.MustParsePrefix("10.0.0.0/8"), iface: "utun6"}}
	plan := splitConflicts(routes, local)
	if len(plan.keep) != 0 || len(plan.skipped) != 1 {
		t.Fatalf("相同前缀应跳过，实际 keep=%v skipped=%v", prefixStrings(plan.keep), plan.skipped)
	}
	if !strings.Contains(plan.skipped[0], "utun6") {
		t.Fatalf("跳过原因应包含冲突网卡名: %s", plan.skipped[0])
	}
}

func TestSplitConflictsDedupes(t *testing.T) {
	routes := []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("100.64.0.0/10"),
	}
	plan := splitConflicts(routes, nil)
	if len(plan.keep) != 1 {
		t.Fatalf("重复路由应去重，实际 %v", prefixStrings(plan.keep))
	}
}

// fd 传递是提权助手把网卡交给主进程的唯一通道，必须可靠。
func TestFDTransferRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")

	src, err := os.CreateTemp(dir, "payload-*")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err = src.WriteString("weft"); err != nil {
		t.Fatal(err)
	}
	if _, err = src.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		c, derr := net.Dial("unix", sock)
		if derr != nil {
			return
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			_ = c.Close()
			return
		}
		_ = sendFD(uc, src)
		_ = c.Close()
	}()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, err := recvFD(conn.(*net.UnixConn))
	if err != nil {
		t.Fatalf("接收 fd 失败: %v", err)
	}
	defer got.Close()

	b, err := io.ReadAll(got)
	if err != nil {
		t.Fatalf("读取收到的 fd 失败: %v", err)
	}
	if string(b) != "weft" {
		t.Fatalf("fd 内容不符: %q", string(b))
	}
}

// 常驻助手的握手协议：ping 不碰 TUN，可以在无 root 环境下验证。
func TestHelperDaemonPing(t *testing.T) {
	sock := fmt.Sprintf("/tmp/wd-hs-%d.sock", os.Getpid())
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		serveHelperConn(c, &daemonState{})
	}()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(helperRequest{Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	var resp helperResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Version != helperVersion {
		t.Fatalf("ping 响应异常: %+v", resp)
	}
}

// 未知操作必须被拒绝，而不是静默通过。
func TestHelperDaemonRejectsUnknown(t *testing.T) {
	sock := fmt.Sprintf("/tmp/wd-hs2-%d.sock", os.Getpid())
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		serveHelperConn(c, &daemonState{})
	}()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(helperRequest{Op: "rm-rf"}); err != nil {
		t.Fatal(err)
	}
	var resp helperResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == "" {
		t.Fatalf("未知操作应报错，实际 %+v", resp)
	}
}

// 在外网（热点）时本机可能有代理留下的 10/8 网卡，
// tailnet 通告的 10.0.1.0/24 不能被它连带跳过。
func TestSplitConflictsKeepsSubnetOnHotspot(t *testing.T) {
	routes := []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("10.0.1.0/24"),
	}
	local := []localRoute{
		{prefix: netip.MustParsePrefix("172.20.10.0/28"), iface: "en0"},
		{prefix: netip.MustParsePrefix("10.222.150.135/8"), iface: "utun6"},
	}
	plan := splitConflicts(routes, local)
	got := prefixStrings(plan.keep)
	if len(got) != 2 || got[1] != "10.0.1.0/24" {
		t.Fatalf("热点下必须安装 10.0.1.0/24，实际 keep=%v skipped=%v", got, plan.skipped)
	}
}

// 本机已有同网段直连（家宽就是 10.0.1.0/24）时仍要跳过，避免断网。
func TestSplitConflictsSkipsSameSubnetAtHome(t *testing.T) {
	routes := []netip.Prefix{netip.MustParsePrefix("10.0.1.0/24")}
	local := []localRoute{
		{prefix: netip.MustParsePrefix("10.0.1.0/24"), iface: "en0"},
	}
	plan := splitConflicts(routes, local)
	if len(plan.keep) != 0 || len(plan.skipped) != 1 {
		t.Fatalf("同网段应跳过，实际 keep=%v skipped=%v", prefixStrings(plan.keep), plan.skipped)
	}
}
