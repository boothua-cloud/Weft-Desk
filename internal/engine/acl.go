package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/boothua/weft/internal/model"
	"tailscale.com/tailcfg"
)

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

// ACL 判定的三种结果。为什么不是 bool：客户端拿到的规则是服务端下发的
// 原文，可能写着 `autogroup:member`、`group:ops`、`tag:db` 这类表达式，
// 其中只有一部分能在客户端算出来。算不出来时判成 deny，就会把
// 「对端没开服务」说成「ACL 拦了」——这正是最容易误导人的地方。
const (
	aclAllow   = "allow"
	aclDeny    = "deny"
	aclUnknown = "unknown"
)

// AclVerdict 是判定结果，note 在 deny/unknown 时说明依据。
type AclVerdict struct {
	State string
	Note  string
}

// AclEvaluate 判断包过滤规则里有没有放行 self → dst:port。
//
// 这不是完整求值：tailscale 的规则是「首条匹配生效」，服务端已经把它编译成
// 白名单，所以「没有一条匹配」就等于不放行。难点在于规则里的表达式：
// 能算的（IP/CIDR/区间/*/tag:）照实算，算不出来的（group:/autogroup:admin）
// 必须返回 unknown，而不是当作不匹配。
func AclEvaluate(rules []tailcfg.FilterRule, self, dst netip.Addr, port uint16, selfTags []string) AclVerdict {
	// 0 条规则有两种相反的读法，客户端分不出来：服务端从来没下发过
	// （老语义是放行一切），或者下发的就是空列表（等于拒绝一切）。
	// 所以只能说不知道，不能说「没放行」。
	// 另一种情况是 netmap 还没到，这时 DebugPacketFilterRules 会直接报错，
	// 走不到这里。
	if len(rules) == 0 {
		return AclVerdict{
			State: aclUnknown,
			Note:  "本机一条规则都没拿到，判断不了：服务端可能没配策略（默认放行），也可能下发的就是空规则（等于全拒）",
		}
	}
	var maybe []string
	for _, r := range rules {
		if !ruleTalksTCP(r) {
			continue
		}
		sHit, sKnown, sWhy := evalSrcSet(r.SrcIPs, self, dst, selfTags)
		dHit, dKnown, dWhy := evalDstSet(r.DstPorts, dst, port)

		if sHit && dHit {
			return AclVerdict{State: aclAllow}
		}
		// 有一边确实不匹配，这条规则就不可能放行
		if sKnown && !sHit {
			continue
		}
		if dKnown && !dHit {
			continue
		}
		// 到这里说明两边都不是「确定不匹配」：要么不确定，要么确定匹配
		switch {
		case !sKnown && !dKnown:
			maybe = append(maybe, "规则里同时用了 "+sWhy+" 和 "+dWhy)
		case !sKnown:
			maybe = append(maybe, "规则里用了 "+sWhy)
		default:
			maybe = append(maybe, "目标写的是 "+dWhy)
		}
	}
	if len(maybe) > 0 {
		return AclVerdict{
			State: aclUnknown,
			Note:  "客户端算不出这条规则：" + maybe[0] + "。要确认只能看服务端上的策略原文",
		}
	}
	return AclVerdict{
		State: aclDeny,
		Note:  AclDenyNote(rules, self, dst, port, selfTags),
	}
}

// AclDenyNote 把「没有这条」说得更具体：顺带列出规则里对这台设备放行了哪些端口。
// 只说「没有」用户还得自己去翻策略，说「只放行了 22」他立刻知道要加哪一条。
func AclDenyNote(rules []tailcfg.FilterRule, self, dst netip.Addr, port uint16, tags []string) string {
	target := net.JoinHostPort(dst.String(), strconv.Itoa(int(port)))
	note := fmt.Sprintf("读到的 %d 条规则里，没有一条放行本机到 %s", len(rules), target)
	if ps := AclPortsFor(rules, self, dst, tags); len(ps) > 0 {
		note += "；对这台设备，规则里只放行了 " + strings.Join(ps, "、")
	}
	return note
}

// AclPortsFor 列出规则里放行到这台设备的 TCP 端口。
func AclPortsFor(rules []tailcfg.FilterRule, self, dst netip.Addr, tags []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, r := range rules {
		if !ruleTalksTCP(r) {
			continue
		}
		// 来源算不出来就不列：宁可不给，也不能报错端口
		if hit, known, _ := evalSrcSet(r.SrcIPs, self, dst, tags); !known || !hit {
			continue
		}
		for _, d := range r.DstPorts {
			if ok, known := matchDstIP(d.IP, dst); !known || !ok {
				continue
			}
			if d.Ports.First == 0 && d.Ports.Last == 65535 {
				add("全部端口")
				continue
			}
			if d.Ports.First == d.Ports.Last {
				add(strconv.Itoa(int(d.Ports.First)))
				continue
			}
			add(fmt.Sprintf("%d-%d", d.Ports.First, d.Ports.Last))
		}
	}
	// 不排序：保持策略里的顺序，「全部端口」通常在最前面，一眼就懂
	return out
}

// ruleTalksTCP 判断一条规则是否管 TCP。IPProto 为空表示 TCP/UDP/ICMP 全管。
func ruleTalksTCP(r tailcfg.FilterRule) bool {
	if len(r.IPProto) == 0 {
		return true
	}
	for _, p := range r.IPProto {
		if p == 6 {
			return true
		}
	}
	return false
}

// evalSrcSet 求一条规则的来源集合：本机在不在里面。
// 返回 (命中, 能否确定, 算不出来的表达式)。
func evalSrcSet(specs []string, self, dst netip.Addr, tags []string) (bool, bool, string) {
	unknown := ""
	for _, s := range specs {
		hit, known := matchSrc(s, self, dst, tags)
		if !known {
			if unknown == "" {
				unknown = s
			}
			continue
		}
		if hit {
			return true, true, ""
		}
	}
	if unknown != "" {
		return false, false, unknown
	}
	return false, true, ""
}

// matchSrc 求单个来源表达式。(命中, 能否确定)。
//
// 能算的：*、IP、CIDR、区间、tag:xxx、autogroup:member/tagged/self。
// 算不出来的：group:xxx、autogroup:admin 之类——成员列表只在服务端。
func matchSrc(spec string, self, dst netip.Addr, tags []string) (bool, bool) {
	s := strings.TrimSpace(spec)
	switch {
	case s == "":
		return false, true
	case s == "*":
		return true, true
	case s == "autogroup:member":
		// tagged 节点不算 member，这是 tailscale 的语义
		return len(tags) == 0, true
	case s == "autogroup:tagged":
		return len(tags) > 0, true
	case s == "autogroup:self":
		return self == dst, true
	case strings.HasPrefix(s, "tag:"):
		want := strings.TrimPrefix(s, "tag:")
		for _, t := range tags {
			if strings.TrimPrefix(t, "tag:") == want {
				return true, true
			}
		}
		return false, true
	case strings.HasPrefix(s, "group:"), strings.HasPrefix(s, "autogroup:"):
		return false, false
	}
	return matchIPSet(s, self), true
}

// evalDstSet 求目标集合：这个地址的这个端口在不在里面。
func evalDstSet(ranges []tailcfg.NetPortRange, dst netip.Addr, port uint16) (bool, bool, string) {
	unknown := ""
	for _, d := range ranges {
		ipOK, ipKnown := matchDstIP(d.IP, dst)
		if !ipKnown {
			if unknown == "" {
				unknown = d.IP
			}
			continue
		}
		if !ipOK {
			continue
		}
		if d.Ports.Contains(port) {
			return true, true, ""
		}
	}
	if unknown != "" {
		return false, false, unknown
	}
	return false, true, ""
}

// matchDstIP 只认 IP/CIDR/区间/*；autogroup:internet 这类交给 unknown。
func matchDstIP(spec string, dst netip.Addr) (bool, bool) {
	s := strings.TrimSpace(spec)
	if strings.HasPrefix(s, "autogroup:") || strings.HasPrefix(s, "group:") || strings.HasPrefix(s, "tag:") {
		return false, false
	}
	return matchIPSet(s, dst), true
}

// DialCode 把拨测错误归类。分类比错误文案可靠，因为文案随系统和语言变。
//
// refused 是最有价值的一类：对方回了 RST，说明 SYN 已经穿过 tailnet 到达对端，
// 包过滤（ACL）必然是放行的。此时再指着 ACL 说「没放行」就是错的。
func DialCode(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET):
		return "refused"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "noroute"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	// 兜底：Windows 的 Errno 有时套在别的包装里，errors.Is 认不出
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "refused"), strings.Contains(msg, "reset by peer"):
		return "refused"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return "timeout"
	case strings.Contains(msg, "no route"), strings.Contains(msg, "unreachable"):
		return "noroute"
	}
	return "other"
}

// DiagVerdict 把实测结果翻成一句话和几条可能原因。
// 判断顺序按「越靠近链路层越先排除」，用户不会被引去改 ACL 但其实是对端离线。
func DiagVerdict(r model.DiagReport) (string, []string) {
	target := net.JoinHostPort(r.IP, strconv.Itoa(r.Port))

	if !r.Online {
		return "这台设备离线",
			[]string{
				"服务端显示它现在不在线，先确认对方开着机、连着网",
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
			hints = append(hints, "和对端从来没握过手：先去服务端确认两边在同一账号、同一 tailnet")
		}
		return fmt.Sprintf("链路不通，%s 连不上", target), hints
	}

	// 链路通、端口不通。从这里开始按拨测错误类型分：这是唯一能区分
	// 「没开服务」「ACL 拦了」「路由没装上」的实测证据。
	switch r.DialCode {
	case "refused":
		hints := []string{
			"对方明确回了「连接被拒绝」，说明包已经到达对端、ACL 放行了，问题不在策略",
			fmt.Sprintf("在 %s 上确认服务：ss -ltn | grep :%d。只监听 127.0.0.1 也不行，要听 0.0.0.0",
				OrName(r.Name), r.Port),
			"也可能对端防火墙用 reject 拒绝，同样是「包到了但不让连」",
		}
		if r.ACLChecked && r.ACLState != aclAllow {
			// 规则判断和实测打架时，以实测为准，并且要说出来
			hints = append(hints, "本机读到的 ACL 规则里没找到这条，但对端都回拒绝了，说明没拦住；多半是规则还没同步到客户端")
		}
		return fmt.Sprintf("对端拒绝了 %s：这个端口没开服务", target), hints

	case "noroute":
		return fmt.Sprintf("本机没有到 %s 的路由", r.IP),
			[]string{
				"这跟 ACL 和对端都无关，是本机路由表没装上",
				"如果要访问的是子网路由器背后的地址，先去「连接诊断 → 子网路由」看那条路由接受没有",
				"拨测报错：" + r.DialErr,
			}
	}

	// 超时或未知错误：包多半被丢了，但丢在哪一环定不了
	switch r.ACLState {
	case aclDeny:
		hints := []string{}
		if r.ACLNote != "" {
			hints = append(hints, r.ACLNote)
		}
		return fmt.Sprintf("连不上 %s，ACL 里没有放行这条的规则", target),
			append(hints,
				"但也不能百分百确定：ACL 放行、对端防火墙丢包的症状完全一样",
				fmt.Sprintf("要确认：在对端 tcpdump -ni any tcp port %d，看包到没到", r.Port),
				"确认是策略问题后，去 ACL 页加一条，保存后立刻生效，不用重连",
			)
	case aclUnknown:
		return fmt.Sprintf("连不上 %s，原因还定不了", target),
			[]string{
				r.ACLNote,
				"现在两种可能并列：策略没放行，或者对端服务/防火墙把包丢了",
				fmt.Sprintf("要分清：在对端 tcpdump -ni any tcp port %d，包到了就是服务或防火墙的事；没到再查策略", r.Port),
			}
	case aclAllow:
		return fmt.Sprintf("ACL 放行了，但 %s 连不上", target),
			[]string{
				"包过滤规则里有这一条，所以问题在对端：服务没起、只监听 127.0.0.1、或者对端防火墙挡了",
				fmt.Sprintf("在对端跑 ss -ltn | grep :%d 确认；服务要监听 0.0.0.0 而不是 127.0.0.1", r.Port),
				"拨测报错：" + r.DialErr,
			}
	}
	return fmt.Sprintf("连不上 %s", target),
		[]string{
			"没读到包过滤规则（可能刚连上、netmap 还没同步），分不清是 ACL 还是对端服务",
			"等几秒再检查一次；还读不到就直接看服务端的策略",
			"拨测报错：" + r.DialErr,
		}
}

// OrName 给日志和提示用：没有主机名就退回 IP。
func OrName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "对端"
	}
	return name
}
