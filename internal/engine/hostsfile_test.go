//go:build darwin || linux

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleHosts = `##
# Host Database
##
127.0.0.1	localhost
255.255.255.255	broadcasthost
::1             localhost
`

func TestMergeHostsAppendsBlock(t *testing.T) {
	next, applied, skipped := mergeHosts(sampleHosts, []HostEntry{
		{Name: "nacos", IP: "100.64.0.3"},
		{Name: "gitea", IP: "100.64.0.4"},
	})
	if applied != 2 || len(skipped) != 0 {
		t.Fatalf("applied=%d skipped=%v", applied, skipped)
	}
	// 别人的行一个都不能少
	for _, keep := range []string{"127.0.0.1\tlocalhost", "broadcasthost", "::1             localhost"} {
		if !strings.Contains(next, keep) {
			t.Fatalf("原有内容丢了: %q\n%s", keep, next)
		}
	}
	if !strings.Contains(next, "100.64.0.3\tnacos") || !strings.Contains(next, hostsBegin) || !strings.Contains(next, hostsEnd) {
		t.Fatalf("区块没写对:\n%s", next)
	}
	// 区块必须在文件末尾，方便下次整段替换
	if !strings.HasSuffix(strings.TrimSpace(next), hostsEnd) {
		t.Fatalf("区块不在末尾:\n%s", next)
	}
}

func TestMergeHostsReplacesOldBlock(t *testing.T) {
	first, _, _ := mergeHosts(sampleHosts, []HostEntry{{Name: "old", IP: "100.64.0.9"}})
	second, applied, _ := mergeHosts(first, []HostEntry{{Name: "new", IP: "100.64.0.10"}})
	if applied != 1 {
		t.Fatalf("applied=%d", applied)
	}
	if strings.Contains(second, "100.64.0.9") {
		t.Fatalf("旧条目没被替换掉:\n%s", second)
	}
	if strings.Count(second, hostsBegin) != 1 {
		t.Fatalf("区块重复了:\n%s", second)
	}
	if strings.Count(second, "127.0.0.1\tlocalhost") != 1 {
		t.Fatalf("原有行被复制了:\n%s", second)
	}
}

func TestMergeHostsIsIdempotent(t *testing.T) {
	entries := []HostEntry{{Name: "nacos", IP: "100.64.0.3"}, {Name: "web-01", IP: "100.64.0.5"}}
	once, _, _ := mergeHosts(sampleHosts, entries)
	twice, _, _ := mergeHosts(once, entries)
	if once != twice {
		t.Fatalf("重复合并不幂等:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestMergeHostsSkipsExistingNames(t *testing.T) {
	// 用户自己写的条目优先，不去覆盖
	cur := sampleHosts + "100.64.0.99\tnacos\n"
	next, applied, skipped := mergeHosts(cur, []HostEntry{{Name: "nacos", IP: "100.64.0.3"}})
	if applied != 0 {
		t.Fatalf("不该覆盖已有名字, applied=%d", applied)
	}
	if len(skipped) != 1 || skipped[0] != "nacos" {
		t.Fatalf("skipped=%v", skipped)
	}
	if !strings.Contains(next, "100.64.0.99\tnacos") || strings.Contains(next, "100.64.0.3\tnacos") {
		t.Fatalf("写错了:\n%s", next)
	}
}

func TestMergeHostsConflictingDuplicateNames(t *testing.T) {
	// 两台设备重名：只写第一条，并把名字报上去
	next, applied, skipped := mergeHosts("", []HostEntry{
		{Name: "build", IP: "100.64.0.7"},
		{Name: "BUILD", IP: "100.64.0.8"},
	})
	if applied != 1 {
		t.Fatalf("applied=%d", applied)
	}
	if len(skipped) != 1 || skipped[0] != "build" {
		t.Fatalf("skipped=%v", skipped)
	}
	if !strings.Contains(next, "100.64.0.7\tbuild") || strings.Contains(next, "100.64.0.8") {
		t.Fatalf("写错了:\n%s", next)
	}
}

func TestMergeHostsDropsBadEntries(t *testing.T) {
	next, applied, _ := mergeHosts("", []HostEntry{
		{Name: "has space", IP: "100.64.0.1"},
		{Name: "-leading", IP: "100.64.0.2"},
		{Name: "100.64.0.3", IP: "100.64.0.3"}, // 名字就是 IP
		{Name: "ok-1", IP: "not-an-ip"},
		{Name: "ok-2", IP: "fd7a:115c:a1e0::1"}, // IPv6 不写
		{Name: "ok-3", IP: "100.64.0.4"},
	})
	if applied != 1 {
		t.Fatalf("applied=%d\n%s", applied, next)
	}
	if !strings.Contains(next, "100.64.0.4\tok-3") {
		t.Fatalf("合法条目丢了:\n%s", next)
	}
}

func TestMergeHostsClearsBlockWhenEmpty(t *testing.T) {
	with, _, _ := mergeHosts(sampleHosts, []HostEntry{{Name: "nacos", IP: "100.64.0.3"}})
	empty, applied, _ := mergeHosts(with, nil)
	if applied != 0 {
		t.Fatalf("applied=%d", applied)
	}
	if strings.Contains(empty, hostsBegin) || strings.Contains(empty, "nacos") {
		t.Fatalf("区块没删干净:\n%s", empty)
	}
	if !strings.Contains(empty, "127.0.0.1\tlocalhost") {
		t.Fatalf("原有内容丢了:\n%s", empty)
	}
}

func TestStripBlockHandlesHalfWrittenFile(t *testing.T) {
	// 上次写到一半被杀：只有开始标记，没有结束标记
	cur := sampleHosts + hostsBegin + "\n100.64.0.3\tnacos\n"
	out := stripBlock(cur)
	if strings.Contains(out, hostsBegin) || strings.Contains(out, "nacos") {
		t.Fatalf("没截掉半截区块:\n%s", out)
	}
	if !strings.Contains(out, "localhost") {
		t.Fatalf("原有内容丢了:\n%s", out)
	}
}

func TestSanitizeHostName(t *testing.T) {
	good := map[string]string{
		"nacOS":     "nacos",
		"web-01":    "web-01",
		" db.prod ": "db.prod",
	}
	for in, want := range good {
		got, ok := sanitizeHostName(in)
		if !ok || got != want {
			t.Fatalf("sanitize(%q)=%q,%v want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", " ", "a b", "-a", "a-", "a_b", "主机", "100.64.0.1", strings.Repeat("a", 64)} {
		if _, ok := sanitizeHostName(in); ok {
			t.Fatalf("不该接受 %q", in)
		}
	}
}

// 真写一遍文件：确认原子替换没有留下半截内容，且关掉功能后只剩原有的行。
func TestWriteHostsBlockRealFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte(sampleHosts), 0o644); err != nil {
		t.Fatal(err)
	}
	old := hostsFile
	hostsFile = path
	defer func() { hostsFile = old }()

	applied, skipped, err := writeHostsBlock([]HostEntry{
		{Name: "nacos", IP: "100.64.0.3"},
		{Name: "nacos", IP: "100.64.0.4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 || len(skipped) != 1 {
		t.Fatalf("applied=%d skipped=%v", applied, skipped)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "100.64.0.3\tnacos") || !strings.Contains(string(body), "localhost") {
		t.Fatalf("文件内容不对:\n%s", body)
	}
	if !HostsEnabled() {
		t.Fatal("HostsEnabled 没认出刚写的区块")
	}

	// 关掉功能：区块消失，其他行原样保留
	if _, _, err := writeHostsBlock(nil); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	if strings.Contains(string(body), hostsBegin) || strings.Contains(string(body), "nacos") {
		t.Fatalf("区块没清掉:\n%s", body)
	}
	if !strings.Contains(string(body), "127.0.0.1\tlocalhost") {
		t.Fatalf("原有内容丢了:\n%s", body)
	}
	if HostsEnabled() {
		t.Fatal("HostsEnabled 还认为有区块")
	}
	// 目录里不该留下临时文件
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("残留了临时文件: %v", entries)
	}
}
