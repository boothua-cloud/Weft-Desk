package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"testing"

	"tailscale.com/tailcfg"
)

func TestACLEvaluate(t *testing.T) {
	self := netip.MustParseAddr("100.64.0.13")
	nas := netip.MustParseAddr("100.64.0.3")

	// 全放行
	if v := aclEvaluate(tailcfg.FilterAllowAll, self, nas, 22, nil); v.State != aclAllow {
		t.Errorf("全放行规则应该放行 22，得到 %q", v.State)
	}

	rules := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"100.64.0.0/24"},
			DstPorts: []tailcfg.NetPortRange{{IP: "100.64.0.3", Ports: tailcfg.PortRange{First: 22, Last: 22}}},
		},
	}
	if v := aclEvaluate(rules, self, nas, 22, nil); v.State != aclAllow {
		t.Error("同网段到 22 应该放行")
	}
	if v := aclEvaluate(rules, self, nas, 80, nil); v.State != aclDeny {
		t.Error("规则只写了 22，80 应该是明确拒绝")
	}
	if v := aclEvaluate(rules, self, netip.MustParseAddr("100.64.0.9"), 22, nil); v.State != aclDeny {
		t.Error("目标不在规则里，应该拒绝")
	}
	if v := aclEvaluate(rules, netip.MustParseAddr("100.64.1.20"), nas, 22, nil); v.State != aclDeny {
		t.Error("来源不在规则里，应该拒绝")
	}

	// 协议只写了 ICMP 的规则不该被当成 TCP 放行
	icmpOnly := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"*"},
			IPProto:  []int{1},
			DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}},
		},
	}
	if v := aclEvaluate(icmpOnly, self, nas, 22, nil); v.State != aclDeny {
		t.Error("ICMP 规则不该放行 TCP")
	}

	// 目标是 "*"
	anyDst := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"*"},
			DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 22, Last: 22}}},
		},
	}
	if v := aclEvaluate(anyDst, self, netip.MustParseAddr("100.64.9.9"), 22, nil); v.State != aclAllow {
		t.Error("目标 * 应该匹配任意地址")
	}

	// 目标写成区间
	rangeDst := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"*"},
			DstPorts: []tailcfg.NetPortRange{{IP: "100.64.0.1-100.64.0.5", Ports: tailcfg.PortRange{First: 1, Last: 65535}}},
		},
	}
	if v := aclEvaluate(rangeDst, self, nas, 443, nil); v.State != aclAllow {
		t.Error("区间应该包含 100.64.0.3")
	}
	if v := aclEvaluate(rangeDst, self, netip.MustParseAddr("100.64.0.6"), 443, nil); v.State != aclDeny {
		t.Error("区间外的地址不该放行")
	}
}

// headscale 的默认策略常常写成 autogroup:member，客户端得自己算出来。
// 以前这里一律匹配失败，于是「对端没开服务」被说成「ACL 拦了」。
func TestACLEvaluateAutogroup(t *testing.T) {
	self := netip.MustParseAddr("100.64.0.13")
	dst := netip.MustParseAddr("100.64.0.3")
	member := []tailcfg.FilterRule{{
		SrcIPs:   []string{"autogroup:member"},
		DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}},
	}}
	if v := aclEvaluate(member, self, dst, 3306, nil); v.State != aclAllow {
		t.Fatalf("没打标签的节点属于 member，应该放行: %+v", v)
	}
	// tagged 节点不算 member，这是 tailscale 的语义
	if v := aclEvaluate(member, self, dst, 3306, []string{"tag:db"}); v.State != aclDeny {
		t.Fatalf("打了标签就不算 member，应该是明确拒绝: %+v", v)
	}

	tagged := []tailcfg.FilterRule{{
		SrcIPs:   []string{"tag:db"},
		DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 5432, Last: 5432}}},
	}}
	if v := aclEvaluate(tagged, self, dst, 5432, []string{"tag:db"}); v.State != aclAllow {
		t.Fatalf("本机带 tag:db，应该放行: %+v", v)
	}
	if v := aclEvaluate(tagged, self, dst, 5432, []string{"tag:web"}); v.State != aclDeny {
		t.Fatalf("本机不是 tag:db，应该拒绝: %+v", v)
	}
}

// 算不出来的写法必须是 unknown。判成 deny 就会把「服务没开」说成「ACL 拦了」。
func TestACLEvaluateUnknown(t *testing.T) {
	self := netip.MustParseAddr("100.64.0.13")
	dst := netip.MustParseAddr("100.64.0.3")

	group := []tailcfg.FilterRule{{
		SrcIPs:   []string{"group:ops"},
		DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}},
	}}
	v := aclEvaluate(group, self, dst, 3306, nil)
	if v.State != aclUnknown {
		t.Fatalf("group: 算不出来，不能判成拒绝: %+v", v)
	}
	if !strings.Contains(v.Note, "group:ops") {
		t.Fatalf("说明里要写清是哪条表达式: %q", v.Note)
	}

	admin := []tailcfg.FilterRule{{
		SrcIPs:   []string{"autogroup:admin"},
		DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}},
	}}
	if v := aclEvaluate(admin, self, dst, 22, nil); v.State != aclUnknown {
		t.Fatalf("autogroup:admin 算不出来: %+v", v)
	}

	// 0 条规则不能判成拒绝：既可能服务端没下发（默认放行），
	// 也可能是空规则（全拒），客户端分不出来，说明里要两种都提。
	if v := aclEvaluate(nil, self, dst, 22, nil); v.State != aclUnknown {
		t.Fatalf("空规则只能是 unknown: %+v", v)
	} else if !strings.Contains(v.Note, "默认放行") || !strings.Contains(v.Note, "全拒") {
		t.Fatalf("空规则的说明要写清两种可能: %q", v.Note)
	}

	// 同时有多条时，只要有一条能确定放行，就是放行
	mixed := []tailcfg.FilterRule{
		{SrcIPs: []string{"group:ops"}, DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}}},
		{SrcIPs: []string{"*"}, DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}}},
	}
	if v := aclEvaluate(mixed, self, dst, 22, nil); v.State != aclAllow {
		t.Fatalf("有一条确定放行就该是放行: %+v", v)
	}

	// 不相关的规则（目标不同）不该把结论拖成 unknown
	unrelated := []tailcfg.FilterRule{{
		SrcIPs:   []string{"group:ops"},
		DstPorts: []tailcfg.NetPortRange{{IP: "100.64.9.9", Ports: tailcfg.PortRange{First: 0, Last: 65535}}},
	}}
	if v := aclEvaluate(unrelated, self, dst, 22, nil); v.State != aclDeny {
		t.Fatalf("目标都对不上，就是明确拒绝: %+v", v)
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

	// 对端回了拒绝：包已经到了，ACL 一定放行。哪怕规则里没找到这条，
	// 也不能让用户去改 ACL——这是最容易把人带偏的一种。
	v, h = diagVerdict(DiagReport{
		IP: "100.64.0.3", Port: 3306, Online: true, Path: "直连", PingMs: 5, Name: "aliyun",
		DialErr: "connect: connection refused", DialCode: "refused",
		ACLChecked: true, ACLState: aclDeny,
	})
	if strings.Contains(v, "ACL 没放行") || !strings.Contains(v, "没开服务") {
		t.Fatalf("被拒绝说明 ACL 放行了，结论该指向服务: %q", v)
	}
	joined = strings.Join(h, " ")
	if !strings.Contains(joined, "ACL 放行") || !strings.Contains(joined, "3306") {
		t.Fatalf("要说清为什么跟 ACL 无关，并给出确认命令: %v", h)
	}

	// 超时 + 规则里确实没这条：可以说 ACL，但不能说死
	v, h = diagVerdict(DiagReport{
		IP: "100.64.0.3", Port: 3306, Online: true, Path: "直连", PingMs: 5,
		DialErr: "i/o timeout", DialCode: "timeout",
		ACLChecked: true, ACLState: aclDeny, ACLNote: "读到的 12 条规则里没有",
	})
	if !strings.Contains(v, "规则") {
		t.Fatalf("该指向缺失的规则: %q", v)
	}
	joined = strings.Join(h, " ")
	if !strings.Contains(joined, "防火墙") || !strings.Contains(joined, "tcpdump") {
		t.Fatalf("超时不能只怪 ACL，要给出另一种可能和验证办法: %v", h)
	}

	// 超时 + 规则里有这条：明确指向对端服务
	v, h = diagVerdict(DiagReport{
		IP: "100.64.0.3", Port: 3306, Online: true, Path: "直连", PingMs: 5,
		DialErr: "i/o timeout", DialCode: "timeout",
		ACLChecked: true, ACLState: aclAllow,
	})
	if !strings.Contains(v, "ACL 放行了") {
		t.Fatalf("规则放行 + 超时，应该指向对端: %q", v)
	}
	if !strings.Contains(strings.Join(h, " "), "ss -ltn") {
		t.Fatalf("该给出确认端口的命令: %v", h)
	}

	// 算不出规则：不许下结论
	v, h = diagVerdict(DiagReport{
		IP: "100.64.0.3", Port: 3306, Online: true, Path: "直连", PingMs: 5,
		DialErr: "i/o timeout", DialCode: "timeout",
		ACLChecked: true, ACLState: aclUnknown, ACLNote: "客户端算不出这条规则：group:ops",
	})
	if strings.Contains(v, "ACL 没放行") || strings.Contains(v, "规则里没有") {
		t.Fatalf("判不了的时候不能给结论: %q", v)
	}
	if !strings.Contains(strings.Join(h, " "), "group:ops") {
		t.Fatalf("要把算不出来的原因说出来: %v", h)
	}

	// 路由没装上：跟 ACL 和对端都无关
	v, h = diagVerdict(DiagReport{
		IP: "10.0.1.8", Port: 22, Online: true, Path: "直连", PingMs: 5,
		DialErr: "connect: no route to host", DialCode: "noroute",
		ACLChecked: true, ACLState: aclDeny,
	})
	if !strings.Contains(v, "路由") || strings.Contains(v, "ACL") {
		t.Fatalf("该判成路由问题: %q", v)
	}
	if !strings.Contains(strings.Join(h, " "), "子网路由") {
		t.Fatalf("要指到子网路由那一页: %v", h)
	}
}

// 拨测错误必须能分类。分类错了，后面的所有结论都会跟着错。
func TestDialCode(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "refused"},
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}, "noroute"},
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}, "noroute"},
		{context.DeadlineExceeded, "timeout"},
		{&timeoutErr{}, "timeout"},
		{errors.New("dial tcp 100.64.0.3:3306: i/o timeout"), "timeout"},
		{errors.New("connect: connection refused"), "refused"},
		{errors.New("something else"), "other"},
	}
	for _, c := range cases {
		if got := dialCode(c.err); got != c.want {
			t.Errorf("%v 该分类成 %q，得到 %q", c.err, c.want, got)
		}
	}
	if got := dialCode(nil); got != "" {
		t.Errorf("没错误应该返回空，得到 %q", got)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

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

// 「没有这条」要说清对这台设备到底放行了什么，不然用户还得自己去翻策略。
func TestACLDenyNoteListsPorts(t *testing.T) {
	self := netip.MustParseAddr("100.64.0.13")
	dst := netip.MustParseAddr("100.64.0.3")
	rules := []tailcfg.FilterRule{
		{SrcIPs: []string{"*"}, DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 22, Last: 22}}}},
		{SrcIPs: []string{"*"}, DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 8000, Last: 8100}}}},
		// 只针对别的设备，不该混进来
		{SrcIPs: []string{"*"}, DstPorts: []tailcfg.NetPortRange{{IP: "100.64.9.9", Ports: tailcfg.PortRange{First: 5432, Last: 5432}}}},
	}
	v := aclEvaluate(rules, self, dst, 3306, nil)
	if v.State != aclDeny {
		t.Fatalf("3306 没写就该是拒绝: %+v", v)
	}
	if !strings.Contains(v.Note, "3306") {
		t.Fatalf("说明里要带查的端口: %q", v.Note)
	}
	if !strings.Contains(v.Note, "22") || !strings.Contains(v.Note, "8000-8100") {
		t.Fatalf("要列出对这台设备实际放行的端口: %q", v.Note)
	}
	if strings.Contains(v.Note, "5432") {
		t.Fatalf("别的设备的规则不该混进来: %q", v.Note)
	}
}

// 全放行时不该长篇大论
func TestACLDenyNoteNoPorts(t *testing.T) {
	self := netip.MustParseAddr("100.64.0.13")
	dst := netip.MustParseAddr("100.64.0.3")
	rules := []tailcfg.FilterRule{
		{SrcIPs: []string{"*"}, DstPorts: []tailcfg.NetPortRange{{IP: "100.64.9.9", Ports: tailcfg.PortRange{First: 22, Last: 22}}}},
	}
	if v := aclEvaluate(rules, self, dst, 3306, nil); !strings.Contains(v.Note, "没有一条放行") {
		t.Fatalf("没有可列的端口时别硬凑: %q", v.Note)
	}
}
