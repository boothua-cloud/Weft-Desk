package main

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"tailscale.com/tailcfg"
)

func TestACLAllows(t *testing.T) {
	self := netip.MustParseAddr("100.64.0.13")
	nas := netip.MustParseAddr("100.64.0.3")

	// 全放行
	if !aclAllows(tailcfg.FilterAllowAll, self, nas, 22) {
		t.Error("全放行规则应该放行 22")
	}

	rules := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"100.64.0.0/24"},
			DstPorts: []tailcfg.NetPortRange{{IP: "100.64.0.3", Ports: tailcfg.PortRange{First: 22, Last: 22}}},
		},
	}
	if !aclAllows(rules, self, nas, 22) {
		t.Error("同网段到 22 应该放行")
	}
	if aclAllows(rules, self, nas, 80) {
		t.Error("规则只写了 22，80 不该放行")
	}
	if aclAllows(rules, self, netip.MustParseAddr("100.64.0.9"), 22) {
		t.Error("目标不在规则里，不该放行")
	}
	if aclAllows(rules, netip.MustParseAddr("100.64.1.20"), nas, 22) {
		t.Error("来源不在规则里，不该放行")
	}

	// 协议只写了 ICMP 的规则不该被当成 TCP 放行
	icmpOnly := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"*"},
			IPProto:  []int{1},
			DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}},
		},
	}
	if aclAllows(icmpOnly, self, nas, 22) {
		t.Error("ICMP 规则不该放行 TCP")
	}

	// 目标是 "*"
	anyDst := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"*"},
			DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 22, Last: 22}}},
		},
	}
	if !aclAllows(anyDst, self, netip.MustParseAddr("100.64.9.9"), 22) {
		t.Error("目标 * 应该匹配任意地址")
	}

	// 目标写成区间
	rangeDst := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"*"},
			DstPorts: []tailcfg.NetPortRange{{IP: "100.64.0.1-100.64.0.5", Ports: tailcfg.PortRange{First: 1, Last: 65535}}},
		},
	}
	if !aclAllows(rangeDst, self, nas, 443) {
		t.Error("区间应该包含 100.64.0.3")
	}
	if aclAllows(rangeDst, self, netip.MustParseAddr("100.64.0.6"), 443) {
		t.Error("区间外的地址不该放行")
	}
}

func TestMatchIPSet(t *testing.T) {
	ip := netip.MustParseAddr("100.64.0.3")
	yes := []string{"*", "100.64.0.3", "100.64.0.0/16", "100.64.0.1-100.64.0.9"}
	no := []string{"", "100.64.0.4", "100.65.0.0/16", "100.64.0.4-100.64.0.9"}
	for _, s := range yes {
		if !matchIPSet(s, ip) {
			t.Errorf("%q 应该匹配", s)
		}
	}
	for _, s := range no {
		if matchIPSet(s, ip) {
			t.Errorf("%q 不该匹配", s)
		}
	}
}

func TestDiagVerdict(t *testing.T) {
	// 对端离线：即使端口通不通都不管，先说离线
	v, h := diagVerdict(DiagReport{IP: "100.64.0.3", Port: 22, Online: false, PingLost: 3})
	if !strings.Contains(v, "离线") || len(h) == 0 {
		t.Fatalf("离线判定不对: %q %v", v, h)
	}

	// 全通
	v, h = diagVerdict(DiagReport{
		IP: "100.64.0.3", Port: 22, Online: true, Path: "直连",
		PingMs: 8, DialMs: 12,
	})
	if !strings.Contains(v, "能连上") || len(h) != 0 {
		t.Fatalf("通的时候不该给原因: %q %v", v, h)
	}
	// 必须是非 nil 的空切片：nil 会序列化成 null，前端读 .length 直接崩
	if h == nil {
		t.Fatal("通的时候原因要返回空切片，不能是 nil")
	}

	// 链路不通
	v, h = diagVerdict(DiagReport{
		IP: "100.64.0.3", Port: 22, Online: true, PingLost: 3,
		PingErr: "timeout", Path: "DERP", Relay: "hkg", Handshake: "从未",
		DialErr: "i/o timeout",
	})
	if !strings.Contains(v, "链路不通") {
		t.Fatalf("应该判成链路不通: %q", v)
	}
	joined := strings.Join(h, " ")
	if !strings.Contains(joined, "hkg") || !strings.Contains(joined, "握过手") {
		t.Fatalf("原因里应该带 relay 和握手: %v", h)
	}

	// 链路通、ACL 没放行
	v, h = diagVerdict(DiagReport{
		IP: "100.64.0.3", Port: 22, Online: true, Path: "直连", PingMs: 5,
		DialErr: "connection refused", ACLChecked: true, ACLAllowed: false,
	})
	if !strings.Contains(v, "ACL") {
		t.Fatalf("应该指向 ACL: %q", v)
	}

	// 链路通、ACL 放行，端口没人听
	v, h = diagVerdict(DiagReport{
		IP: "100.64.0.3", Port: 22, Online: true, Path: "直连", PingMs: 5,
		DialErr: "connection refused", ACLChecked: true, ACLAllowed: true,
	})
	if strings.Contains(v, "ACL 没放行") || !strings.Contains(v, "链路通") {
		t.Fatalf("应该指向服务: %q", v)
	}
	if !strings.Contains(strings.Join(h, " "), "端口") {
		t.Fatalf("原因里该提端口: %v", h)
	}
}

// 界面直接吃这份 JSON。hints 一旦是 null，Vue 读 length 就抛错，整页白屏。
func TestDiagReportJSONHints(t *testing.T) {
	rep := DiagReport{IP: "100.64.0.3", Port: 22, Online: true, Path: "直连", PingMs: 8, DialMs: 12}
	rep.Verdict, rep.Hints = diagVerdict(rep)
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"hints":[]`) {
		t.Fatalf("hints 要序列化成空数组: %s", b)
	}
}
