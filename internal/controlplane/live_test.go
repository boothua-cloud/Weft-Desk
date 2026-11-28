package controlplane

import (
	"os"
	"testing"

	"github.com/boothua/weft/internal/model"
)

// 真实服务端联调测试：默认跳过，WEFT_LIVE=1 时用钥匙串里的 API Key 跑一次。
// 主要防「结构体字段类型与 swagger 不一致导致整表反序列化失败」这类问题。
func TestLiveDevices(t *testing.T) {
	if os.Getenv("WEFT_LIVE") == "" {
		t.Skip("设置 WEFT_LIVE=1 才执行")
	}
	c := &HeadscaleCP{cfg: model.Config{BaseURL: "https://headscale.example.com"}}
	if c.bearer() == "" {
		t.Skip("钥匙串里没有 API Key")
	}
	ds, err := c.Devices("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) == 0 {
		t.Fatal("没有拿到设备")
	}
	for _, d := range ds {
		t.Logf("%s %s ip=%s status=%s reg=%s lastSeen=%q expiry=%q routes=%q",
			d.ID, d.Name, d.IP, d.Status, d.RegisterMethod, d.LastSeen, d.Expiry, d.ApprovedRoutes)
	}
}
