package netio

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/boothua/weft/internal/model"
)

// 监听范围是用户显式要求的功能：切成 0.0.0.0 之后，同网段的其他设备要能连进来。
// 默认必须还是只听本机，不能因为升级就悄悄对内网开放。
func TestForwardBindScope(t *testing.T) {
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, nil }
	h := New(dial)

	// 默认：仅本机
	if h.bind != BindLocalhost {
		t.Fatalf("默认监听地址应是 %s，实际 %s", BindLocalhost, h.bind)
	}

	rule := model.ForwardRule{ID: "t1", Name: "t", Kind: "custom", Target: "127.0.0.1", TargetPort: 9, LocalPort: 45871, Enabled: true}

	if err := h.Apply([]model.ForwardRule{rule}, false, 0, BindLocalhost); err != nil {
		t.Fatalf("Apply(localhost) 失败: %v", err)
	}
	if got := h.entries["t1"].ln.Addr().String(); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("仅本机模式监听在 %s", got)
	}

	// 切到 0.0.0.0：监听器要重建，且绑定到所有接口（Addr 的 IP 为空或不确定）
	if err := h.Apply([]model.ForwardRule{rule}, false, 0, BindAny); err != nil {
		t.Fatalf("Apply(0.0.0.0) 失败: %v", err)
	}
	addr := h.entries["t1"].ln.Addr().(*net.TCPAddr)
	if addr.IP != nil && !addr.IP.IsUnspecified() {
		t.Fatalf("切到 0.0.0.0 后仍绑定在 %s", addr.IP)
	}

	// 非法值回落到仅本机
	if err := h.Apply([]model.ForwardRule{rule}, false, 0, "1.2.3.4"); err != nil {
		t.Fatalf("Apply(非法值) 失败: %v", err)
	}
	if h.bind != BindLocalhost {
		t.Fatalf("非法监听地址应回落到 %s，实际 %s", BindLocalhost, h.bind)
	}

	h.CloseAll()
}
