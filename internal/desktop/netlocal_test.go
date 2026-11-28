package desktop

import (
	"net/netip"
	"testing"
)

// 只验证不变量：拿到的每个地址都是 IPv4、都不在回环/链路本地/tailnet 段里。
// 不写死具体网卡，CI 和本机都能过。
func TestLocalIPv4sLooksSane(t *testing.T) {
	tailnet := netip.MustParsePrefix("100.64.0.0/10")
	for _, s := range localIPv4s() {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("%q 不是地址: %v", s, err)
		}
		if !a.Is4() {
			t.Fatalf("%q 不是 IPv4", s)
		}
		if a.IsLoopback() || a.IsLinkLocalUnicast() || tailnet.Contains(a) {
			t.Fatalf("%q 本该被排除", s)
		}
	}
}
