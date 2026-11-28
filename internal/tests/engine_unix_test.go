//go:build darwin || linux

package tests

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/boothua/weft/internal/engine"
)

// 网段冲突：只有「本机已有完全相同的前缀」才跳过，否则会把本机局域网
// 流量导进隧道，直接断网。这组用例覆盖的都是真机踩过的形状。

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

func TestSplitConflictsDropsLocalOverlap(t *testing.T) {
	routes := []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("10.20.0.0/16"),
	}
	local := []engine.LocalRoute{
		{Prefix: netip.MustParsePrefix("192.168.1.0/24"), Iface: "en0"},
		{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Iface: "utun6"},
	}
	plan := engine.SplitConflicts(routes, local)

	// 只有与本机完全相同的前缀才跳过（会覆盖原路由）；
	// 10.20.0.0/16 虽然落在本机 10/8 之内，但更长，最长前缀匹配下
	// 本机更具体的网段仍优先，所以应当安装。
	if got := prefixStrings(plan.Keep); len(got) != 2 || got[0] != "100.64.0.0/10" || got[1] != "10.20.0.0/16" {
		t.Fatalf("保留路由不符: %v", got)
	}
	if len(plan.Skipped) != 1 {
		t.Fatalf("只应跳过完全相同的 192.168.1.0/24，实际 %v", plan.Skipped)
	}
}

// 本机已有的更具体路由不应阻止较宽的 tailnet 段安装。
func TestSplitConflictsAllowsLessSpecific(t *testing.T) {
	routes := []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10")}
	local := []engine.LocalRoute{
		{Prefix: netip.MustParsePrefix("100.64.0.2/32"), Iface: "utun4"},
	}
	plan := engine.SplitConflicts(routes, local)
	if len(plan.Keep) != 1 || len(plan.Skipped) != 0 {
		t.Fatalf("100.64/10 应可安装（本机只有 /32），实际 keep=%v skipped=%v",
			prefixStrings(plan.Keep), plan.Skipped)
	}
}

// 前缀完全相同才是真冲突：会覆盖本机原有路由。
func TestSplitConflictsIdenticalIsConflict(t *testing.T) {
	routes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	local := []engine.LocalRoute{{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Iface: "utun6"}}
	plan := engine.SplitConflicts(routes, local)
	if len(plan.Keep) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("相同前缀应跳过，实际 keep=%v skipped=%v", prefixStrings(plan.Keep), plan.Skipped)
	}
	if !strings.Contains(plan.Skipped[0], "utun6") {
		t.Fatalf("跳过原因应包含冲突网卡名: %s", plan.Skipped[0])
	}
}

func TestSplitConflictsDedupes(t *testing.T) {
	routes := []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("100.64.0.0/10"),
	}
	plan := engine.SplitConflicts(routes, nil)
	if len(plan.Keep) != 1 {
		t.Fatalf("重复路由应去重，实际 %v", prefixStrings(plan.Keep))
	}
}

// 在外网（热点）时本机可能有代理留下的 10/8 网卡，
// tailnet 通告的 10.0.1.0/24 不能被它连带跳过。
func TestSplitConflictsKeepsSubnetOnHotspot(t *testing.T) {
	routes := []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("10.0.1.0/24"),
	}
	local := []engine.LocalRoute{
		{Prefix: netip.MustParsePrefix("172.20.10.0/28"), Iface: "en0"},
		{Prefix: netip.MustParsePrefix("10.222.150.135/8"), Iface: "utun6"},
	}
	plan := engine.SplitConflicts(routes, local)
	got := prefixStrings(plan.Keep)
	if len(got) != 2 || got[1] != "10.0.1.0/24" {
		t.Fatalf("热点下必须安装 10.0.1.0/24，实际 keep=%v skipped=%v", got, plan.Skipped)
	}
}

// 本机已有同网段直连（家宽就是 10.0.1.0/24）时仍要跳过，避免断网。
func TestSplitConflictsSkipsSameSubnetAtHome(t *testing.T) {
	routes := []netip.Prefix{netip.MustParsePrefix("10.0.1.0/24")}
	local := []engine.LocalRoute{
		{Prefix: netip.MustParsePrefix("10.0.1.0/24"), Iface: "en0"},
	}
	plan := engine.SplitConflicts(routes, local)
	if len(plan.Keep) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("同网段应跳过，实际 keep=%v skipped=%v", prefixStrings(plan.Keep), plan.Skipped)
	}
}
