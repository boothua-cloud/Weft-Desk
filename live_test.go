package main

import (
	"os"
	"testing"
)

// 真实控制面联调测试：默认跳过，WEFT_LIVE=1 时用钥匙串里的 API Key 跑一次。
// 主要防「结构体字段类型与 swagger 不一致导致整表反序列化失败」这类问题。
func TestLiveDevices(t *testing.T) {
	if os.Getenv("WEFT_LIVE") == "" {
		t.Skip("设置 WEFT_LIVE=1 才执行")
	}
	c := &headscaleCP{cfg: Config{BaseURL: "https://headscale.boothua.cn"}}
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

// 确认本地库不再自动写入示例数据（首次运行会清理旧版示例，只执行一次）。
func TestStoreNoDemoData(t *testing.T) {
	s, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fw, _ := s.ListForwards()
	apps, _ := s.ListApps()
	t.Logf("forwards=%d apps=%d", len(fw), len(apps))
}
