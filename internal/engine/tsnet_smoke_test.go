package engine

import (
	"testing"

	"tailscale.com/tsnet"
)

// 验证内嵌节点能在没有 tailscaled、没有 root、服务端不可达时完成初始化。
// ControlURL 故意指向本地不可达端口，只看 Start 是否依赖系统服务或提权。
func TestTSNetStartWithoutDaemon(t *testing.T) {
	srv := &tsnet.Server{
		Dir:        t.TempDir(),
		Hostname:   "weft-smoke",
		ControlURL: "http://127.0.0.1:1",
		Ephemeral:  true,
		Logf:       func(string, ...any) {},
		UserLogf:   func(string, ...any) {},
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer srv.Close()
}
