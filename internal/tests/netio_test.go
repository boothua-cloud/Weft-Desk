package tests

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/netio"
)

// 监听范围是用户显式要求的功能：切成 0.0.0.0 之后，同网段的其他设备要能连进来。
// 默认必须还是只听本机，不能因为升级就悄悄对内网开放。
func TestForwardBindScope(t *testing.T) {
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, nil }
	h := netio.New(dial)

	// 默认：仅本机
	if got := h.Bind(); got != netio.BindLocalhost {
		t.Fatalf("默认监听地址应是 %s，实际 %s", netio.BindLocalhost, got)
	}

	rule := model.ForwardRule{ID: "t1", Name: "t", Kind: "custom", Target: "127.0.0.1", TargetPort: 9, LocalPort: 45871, Enabled: true}

	if err := h.Apply([]model.ForwardRule{rule}, false, 0, netio.BindLocalhost); err != nil {
		t.Fatalf("Apply(localhost) 失败: %v", err)
	}
	if got := h.ListenAddr("t1"); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("仅本机模式监听在 %s", got)
	}

	// 切到 0.0.0.0：监听器要重建，并且绑到所有接口上。
	if err := h.Apply([]model.ForwardRule{rule}, false, 0, netio.BindAny); err != nil {
		t.Fatalf("Apply(0.0.0.0) 失败: %v", err)
	}
	host, _, err := net.SplitHostPort(h.ListenAddr("t1"))
	if err != nil {
		t.Fatalf("监听地址解析失败: %v", err)
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
		t.Fatalf("切到 0.0.0.0 后仍绑定在 %s", ip)
	}

	// 非法值回落到仅本机
	if err := h.Apply([]model.ForwardRule{rule}, false, 0, "1.2.3.4"); err != nil {
		t.Fatalf("Apply(非法值) 失败: %v", err)
	}
	if got := h.Bind(); got != netio.BindLocalhost {
		t.Fatalf("非法监听地址应回落到 %s，实际 %s", netio.BindLocalhost, got)
	}

	h.CloseAll()
}
