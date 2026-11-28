//go:build darwin

package tests

import (
	"testing"

	"github.com/boothua/weft/internal/sysproxy"
)

// macOS 系统代理的读取：例外列表要认得通配和网段，
// scutil 的字典输出要能从数组里退出来继续读后面的键。

func TestExcepted(t *testing.T) {
	list := []string{"<local>", "*.boothua.cn", "10.0.0.0/8", "100.64.0.0/16", "localhost", "*.local"}
	yes := []string{"nas", "hs.boothua.cn", "a.boothua.cn", "10.1.2.3", "100.64.0.7", "localhost", "mac.local"}
	no := []string{"headscale.example.com", "8.8.8.8", "192.168.1.1", "a.local.example.com"}
	for _, h := range yes {
		if !sysproxy.Excepted(h, list) {
			t.Errorf("%s 应该走直连", h)
		}
	}
	for _, h := range no {
		if sysproxy.Excepted(h, list) {
			t.Errorf("%s 不该被判成例外", h)
		}
	}
}

func TestParseSCUtilProxy(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : 10.0.0.0/8
    2 : *.local
  }
  HTTPEnable : 1
  HTTPPort : 7897
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7897
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 0
  SOCKSPort : 7897
  SOCKSProxy : 127.0.0.1
}`
	fields, list := sysproxy.ParseSCUtilProxy(out)
	if fields["HTTPEnable"] != "1" || fields["HTTPPort"] != "7897" || fields["HTTPProxy"] != "127.0.0.1" {
		t.Fatalf("字段没解析对: %v", fields)
	}
	if len(list) != 3 || list[1] != "10.0.0.0/8" {
		t.Fatalf("例外列表不对: %v", list)
	}
	if fields["SOCKSPort"] != "7897" {
		t.Fatalf("列表结束后没继续解析: %v", fields)
	}
}
