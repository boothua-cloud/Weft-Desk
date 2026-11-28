package tests

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/boothua/weft/internal/engine"
	"github.com/boothua/weft/internal/model"
	"tailscale.com/tailcfg"
)

// 引擎侧：ACL 判定、诊断结论、本地代理。

func TestACLEvaluate(t *testing.T) {
	self := netip.MustParseAddr("100.64.0.13")
	nas := netip.MustParseAddr("100.64.0.3")

	// 全放行
	if v := engine.AclEvaluate(tailcfg.FilterAllowAll, self, nas, 22, nil); v.State != "allow" {
		t.Errorf("全放行规则应该放行 22，得到 %q", v.State)
	}

	rules := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"100.64.0.0/24"},
			DstPorts: []tailcfg.NetPortRange{{IP: "100.64.0.3", Ports: tailcfg.PortRange{First: 22, Last: 22}}},
		},
	}
	if v := engine.AclEvaluate(rules, self, nas, 22, nil); v.State != "allow" {
		t.Error("同网段到 22 应该放行")
	}
	if v := engine.AclEvaluate(rules, self, nas, 80, nil); v.State != "deny" {
		t.Error("规则只写了 22，80 应该是明确拒绝")
	}
	if v := engine.AclEvaluate(rules, self, netip.MustParseAddr("100.64.0.9"), 22, nil); v.State != "deny" {
		t.Error("目标不在规则里，应该拒绝")
	}
	if v := engine.AclEvaluate(rules, netip.MustParseAddr("100.64.1.20"), nas, 22, nil); v.State != "deny" {
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
	if v := engine.AclEvaluate(icmpOnly, self, nas, 22, nil); v.State != "deny" {
		t.Error("ICMP 规则不该放行 TCP")
	}

	// 目标是 "*"
	anyDst := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"*"},
			DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 22, Last: 22}}},
		},
	}
	if v := engine.AclEvaluate(anyDst, self, netip.MustParseAddr("100.64.9.9"), 22, nil); v.State != "allow" {
		t.Error("目标 * 应该匹配任意地址")
	}

	// 目标写成区间
	rangeDst := []tailcfg.FilterRule{
		{
			SrcIPs:   []string{"*"},
			DstPorts: []tailcfg.NetPortRange{{IP: "100.64.0.1-100.64.0.5", Ports: tailcfg.PortRange{First: 1, Last: 65535}}},
		},
	}
	if v := engine.AclEvaluate(rangeDst, self, nas, 443, nil); v.State != "allow" {
		t.Error("区间应该包含 100.64.0.3")
	}
	if v := engine.AclEvaluate(rangeDst, self, netip.MustParseAddr("100.64.0.6"), 443, nil); v.State != "deny" {
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
	if v := engine.AclEvaluate(member, self, dst, 3306, nil); v.State != "allow" {
		t.Fatalf("没打标签的节点属于 member，应该放行: %+v", v)
	}
	// tagged 节点不算 member，这是 tailscale 的语义
	if v := engine.AclEvaluate(member, self, dst, 3306, []string{"tag:db"}); v.State != "deny" {
		t.Fatalf("打了标签就不算 member，应该是明确拒绝: %+v", v)
	}

	tagged := []tailcfg.FilterRule{{
		SrcIPs:   []string{"tag:db"},
		DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 5432, Last: 5432}}},
	}}
	if v := engine.AclEvaluate(tagged, self, dst, 5432, []string{"tag:db"}); v.State != "allow" {
		t.Fatalf("本机带 tag:db，应该放行: %+v", v)
	}
	if v := engine.AclEvaluate(tagged, self, dst, 5432, []string{"tag:web"}); v.State != "deny" {
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
	v := engine.AclEvaluate(group, self, dst, 3306, nil)
	if v.State != "unknown" {
		t.Fatalf("group: 算不出来，不能判成拒绝: %+v", v)
	}
	if !strings.Contains(v.Note, "group:ops") {
		t.Fatalf("说明里要写清是哪条表达式: %q", v.Note)
	}

	admin := []tailcfg.FilterRule{{
		SrcIPs:   []string{"autogroup:admin"},
		DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}},
	}}
	if v := engine.AclEvaluate(admin, self, dst, 22, nil); v.State != "unknown" {
		t.Fatalf("autogroup:admin 算不出来: %+v", v)
	}

	// 0 条规则不能判成拒绝：既可能服务端没下发（默认放行），
	// 也可能是空规则（全拒），客户端分不出来，说明里要两种都提。
	if v := engine.AclEvaluate(nil, self, dst, 22, nil); v.State != "unknown" {
		t.Fatalf("空规则只能是 unknown: %+v", v)
	} else if !strings.Contains(v.Note, "默认放行") || !strings.Contains(v.Note, "全拒") {
		t.Fatalf("空规则的说明要写清两种可能: %q", v.Note)
	}

	// 同时有多条时，只要有一条能确定放行，就是放行
	mixed := []tailcfg.FilterRule{
		{SrcIPs: []string{"group:ops"}, DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}}},
		{SrcIPs: []string{"*"}, DstPorts: []tailcfg.NetPortRange{{IP: "*", Ports: tailcfg.PortRange{First: 0, Last: 65535}}}},
	}
	if v := engine.AclEvaluate(mixed, self, dst, 22, nil); v.State != "allow" {
		t.Fatalf("有一条确定放行就该是放行: %+v", v)
	}

	// 不相关的规则（目标不同）不该把结论拖成 unknown
	unrelated := []tailcfg.FilterRule{{
		SrcIPs:   []string{"group:ops"},
		DstPorts: []tailcfg.NetPortRange{{IP: "100.64.9.9", Ports: tailcfg.PortRange{First: 0, Last: 65535}}},
	}}
	if v := engine.AclEvaluate(unrelated, self, dst, 22, nil); v.State != "deny" {
		t.Fatalf("目标都对不上，就是明确拒绝: %+v", v)
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
	v := engine.AclEvaluate(rules, self, dst, 3306, nil)
	if v.State != "deny" {
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
	if v := engine.AclEvaluate(rules, self, dst, 3306, nil); !strings.Contains(v.Note, "没有一条放行") {
		t.Fatalf("没有可列的端口时别硬凑: %q", v.Note)
	}
}

func TestDiagVerdict(t *testing.T) {
	// 对端离线：即使端口通不通都不管，先说离线
	v, h := engine.DiagVerdict(model.DiagReport{IP: "100.64.0.3", Port: 22, Online: false, PingLost: 3})
	if !strings.Contains(v, "离线") || len(h) == 0 {
		t.Fatalf("离线判定不对: %q %v", v, h)
	}

	// 全通
	v, h = engine.DiagVerdict(model.DiagReport{
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
	v, h = engine.DiagVerdict(model.DiagReport{
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
	v, h = engine.DiagVerdict(model.DiagReport{
		IP: "100.64.0.3", Port: 3306, Online: true, Path: "直连", PingMs: 5, Name: "aliyun",
		DialErr: "connect: connection refused", DialCode: "refused",
		ACLChecked: true, ACLState: "deny",
	})
	if strings.Contains(v, "ACL 没放行") || !strings.Contains(v, "没开服务") {
		t.Fatalf("被拒绝说明 ACL 放行了，结论该指向服务: %q", v)
	}
	joined = strings.Join(h, " ")
	if !strings.Contains(joined, "ACL 放行") || !strings.Contains(joined, "3306") {
		t.Fatalf("要说清为什么跟 ACL 无关，并给出确认命令: %v", h)
	}

	// 超时 + 规则里确实没这条：可以说 ACL，但不能说死
	v, h = engine.DiagVerdict(model.DiagReport{
		IP: "100.64.0.3", Port: 3306, Online: true, Path: "直连", PingMs: 5,
		DialErr: "i/o timeout", DialCode: "timeout",
		ACLChecked: true, ACLState: "deny", ACLNote: "读到的 12 条规则里没有",
	})
	if !strings.Contains(v, "规则") {
		t.Fatalf("该指向缺失的规则: %q", v)
	}
	joined = strings.Join(h, " ")
	if !strings.Contains(joined, "防火墙") || !strings.Contains(joined, "tcpdump") {
		t.Fatalf("超时不能只怪 ACL，要给出另一种可能和验证办法: %v", h)
	}

	// 超时 + 规则里有这条：明确指向对端服务
	v, h = engine.DiagVerdict(model.DiagReport{
		IP: "100.64.0.3", Port: 3306, Online: true, Path: "直连", PingMs: 5,
		DialErr: "i/o timeout", DialCode: "timeout",
		ACLChecked: true, ACLState: "allow",
	})
	if !strings.Contains(v, "ACL 放行了") {
		t.Fatalf("规则放行 + 超时，应该指向对端: %q", v)
	}
	if !strings.Contains(strings.Join(h, " "), "ss -ltn") {
		t.Fatalf("该给出确认端口的命令: %v", h)
	}

	// 算不出规则：不许下结论
	v, h = engine.DiagVerdict(model.DiagReport{
		IP: "100.64.0.3", Port: 3306, Online: true, Path: "直连", PingMs: 5,
		DialErr: "i/o timeout", DialCode: "timeout",
		ACLChecked: true, ACLState: "unknown", ACLNote: "客户端算不出这条规则：group:ops",
	})
	if strings.Contains(v, "ACL 没放行") || strings.Contains(v, "规则里没有") {
		t.Fatalf("判不了的时候不能给结论: %q", v)
	}
	if !strings.Contains(strings.Join(h, " "), "group:ops") {
		t.Fatalf("要把算不出来的原因说出来: %v", h)
	}

	// 路由没装上：跟 ACL 和对端都无关
	v, h = engine.DiagVerdict(model.DiagReport{
		IP: "10.0.1.8", Port: 22, Online: true, Path: "直连", PingMs: 5,
		DialErr: "connect: no route to host", DialCode: "noroute",
		ACLChecked: true, ACLState: "deny",
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
		if got := engine.DialCode(c.err); got != c.want {
			t.Errorf("%v 该分类成 %q，得到 %q", c.err, c.want, got)
		}
	}
	if got := engine.DialCode(nil); got != "" {
		t.Errorf("没错误应该返回空，得到 %q", got)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// 界面直接吃这份 JSON。hints 一旦是 null，Vue 读 length 就抛错，整页白屏。
func TestDiagReportJSONHints(t *testing.T) {
	rep := model.DiagReport{IP: "100.64.0.3", Port: 22, Online: true, Path: "直连", PingMs: 8, DialMs: 12}
	rep.Verdict, rep.Hints = engine.DiagVerdict(rep)
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"hints":[]`) {
		t.Fatalf("hints 要序列化成空数组: %s", b)
	}
}

// 用本地 httptest 顶替 tailnet 目标，验证 127.0.0.1 代理的转发、
// Host / Location 重写与端口复用，不需要真实服务端。
func TestProxyHubForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "nas.internal:5000" {
			t.Errorf("Host 未正确重写: %s", r.Host)
		}
		if r.URL.Path != "/app" {
			t.Errorf("路径丢失: %s", r.URL.Path)
		}
		w.Header().Set("Location", "http://nas.internal:5000/next")
		w.WriteHeader(http.StatusFound)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	hub := engine.NewProxyHub(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	})
	defer hub.CloseAll()

	local, err := hub.Resolve("http://nas.internal:5000/app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(local, "http://127.0.0.1:") {
		t.Fatalf("未返回本地地址: %s", local)
	}
	u, err := url.Parse(local)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Scheme + "://" + u.Host

	noFollow := func(c *http.Client) *http.Client {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}

	// 不带令牌直接打端口：同机的其它进程扫到了也不能借道
	bare, err := noFollow(&http.Client{}).Get(host + "/app")
	if err != nil {
		t.Fatal(err)
	}
	_ = bare.Body.Close()
	if bare.StatusCode != http.StatusForbidden {
		t.Fatalf("无令牌应被拒，实际 %d", bare.StatusCode)
	}

	// 令牌不对也一样
	wrong, err := noFollow(&http.Client{}).Get(host + "/__weft/deadbeef/app")
	if err != nil {
		t.Fatal(err)
	}
	_ = wrong.Body.Close()
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("错误令牌应被拒，实际 %d", wrong.StatusCode)
	}

	// 首次带前缀进来：种 cookie 并跳回真实路径
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	withJar := noFollow(&http.Client{Jar: jar})
	entry, err := withJar.Get(local)
	if err != nil {
		t.Fatal(err)
	}
	_ = entry.Body.Close()
	if entry.StatusCode != http.StatusFound {
		t.Fatalf("入口应跳转，实际 %d", entry.StatusCode)
	}
	if loc := entry.Header.Get("Location"); loc != "/app" {
		t.Fatalf("入口跳转地址: %s", loc)
	}

	// 之后页面里的绝对路径请求（/api、/assets）靠 cookie 放行
	resp, err := withJar.Get(host + "/app")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("状态码: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "http://127.0.0.1:") || strings.Contains(loc, "nas.internal") {
		t.Fatalf("Location 未改写回本地地址: %s", loc)
	}

	again, err := hub.Resolve("http://nas.internal:5000/app")
	if err != nil {
		t.Fatal(err)
	}
	if again != local {
		t.Fatalf("同一目标未复用端口: %s vs %s", local, again)
	}
}

// 自签证书的内网应用是白屏的根因：WebView 直接拒绝导航。
// 验证「本地代理 + 放宽校验」能取到页面，且开关关闭时仍然失败。
func TestProxySelfSigned(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ikua:"+r.URL.Path)
	}))
	defer upstream.Close()

	dial := (&net.Dialer{Timeout: 5 * time.Second}).DialContext

	// open 模拟窗口里的浏览器：先过令牌门，再按 cookie 取真实页面
	open := func(local string) (*http.Response, string) {
		u, err := url.Parse(local)
		if err != nil {
			t.Fatal(err)
		}
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Jar: jar}
		resp, err := client.Get(local)
		if err != nil {
			t.Fatalf("打不开 %s: %v", local, err)
		}
		if resp.StatusCode == http.StatusFound {
			_ = resp.Body.Close()
			resp, err = client.Get(u.Scheme + "://" + u.Host + "/login")
			if err != nil {
				t.Fatalf("跟随后失败: %v", err)
			}
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(body)
	}

	hub := engine.NewProxyHub(dial)
	defer hub.CloseAll()
	hub.SetInsecure(true)

	local, err := hub.Resolve(upstream.URL + "/login#/login")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(local, "#/login") {
		t.Fatalf("片段被丢弃: %s", local)
	}
	resp, body := open(local)
	if resp.StatusCode != 200 || !strings.HasPrefix(body, "ikua:/login") {
		t.Fatalf("开启信任后仍失败: 状态=%d body=%q", resp.StatusCode, body)
	}

	strict := engine.NewProxyHub(dial)
	defer strict.CloseAll()
	strict.SetInsecure(false)
	raw, err := strict.Resolve(upstream.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	// 代理把 TLS 失败转成 502（页面里会显示原因），所以看状态码而不是 err
	bad, _ := open(raw)
	if bad.StatusCode == 200 {
		t.Fatal("未开启信任时不应成功，说明代理没有校验证书")
	}
}

func TestRelayLabel(t *testing.T) {
	if got := engine.RelayLabel("headscale"); got != "自建中继" {
		t.Fatalf("headscale 内嵌中继不该原样显示: %q", got)
	}
	if got := engine.RelayLabel("hkg"); got != "hkg" {
		t.Fatalf("公网区域照原样: %q", got)
	}
}
