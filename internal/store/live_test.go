package store

import "testing"

// 确认本地库不再自动写入示例数据（首次运行会清理旧版示例，只执行一次）。
func TestStoreNoDemoData(t *testing.T) {
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fw, _ := s.ListForwards()
	apps, _ := s.ListApps()
	t.Logf("forwards=%d apps=%d", len(fw), len(apps))
}
