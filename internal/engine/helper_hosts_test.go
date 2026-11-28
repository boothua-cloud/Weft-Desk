//go:build darwin || linux

package engine

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 走一遍完整往返：SyncHosts → 连上「助手」→ hosts 操作 → 落盘。
// 真实环境里这一段要么需要 root、要么需要常驻助手，都没法在单测里直接跑，
// 所以这里起一个临时 unix socket，用同一个 serveHelperConn 处理请求。
func TestSyncHostsThroughHelper(t *testing.T) {
	dir := t.TempDir()
	hosts := filepath.Join(dir, "hosts")
	if err := os.WriteFile(hosts, []byte(sampleHosts), 0o644); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "helper.sock")

	oldHosts, oldSock := hostsFile, helperSocket
	hostsFile, helperSocket = hosts, sock
	defer func() { hostsFile, helperSocket = oldHosts, oldSock }()

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveHelperConn(c, &daemonState{})
		}
	}()

	res, err := SyncHosts([]HostEntry{
		{Name: "nacos", IP: "100.64.0.3"},
		{Name: "web-01", IP: "100.64.0.4"},
		{Name: "nacos", IP: "100.64.0.5"},
	})
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if res.Applied != 2 || len(res.Skipped) != 1 {
		t.Fatalf("applied=%d skipped=%v", res.Applied, res.Skipped)
	}
	body, _ := os.ReadFile(hosts)
	if !strings.Contains(string(body), "100.64.0.3\tnacos") || !strings.Contains(string(body), "localhost") {
		t.Fatalf("文件内容不对:\n%s", body)
	}

	if err := ClearHosts(); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	body, _ = os.ReadFile(hosts)
	if strings.Contains(string(body), hostsBegin) {
		t.Fatalf("没清干净:\n%s", body)
	}
}

// 助手不认识 hosts 操作时要给出可执行的提示（对应旧版助手）。
func TestHostsOpUnknownOnOldHelper(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "old.sock")
	oldHosts, oldSock := hostsFile, helperSocket
	hostsFile, helperSocket = filepath.Join(dir, "hosts"), sock
	defer func() { hostsFile, helperSocket = oldHosts, oldSock }()

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// 模拟旧助手：收到什么都说「未知操作」
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 512)
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte(`{"ok":false,"error":"未知操作: hosts"}`))
			}(c)
		}
	}()

	_, err = SyncHosts([]HostEntry{{Name: "nacos", IP: "100.64.0.3"}})
	if err == nil || !strings.Contains(err.Error(), "重新安装") {
		t.Fatalf("提示不对: %v", err)
	}
}
