//go:build darwin || linux

package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// 本地名字解析：把 tailnet 里的设备名写进 /etc/hosts 里一段带标记的区块。
//
// 为什么是 hosts，不是 127.0.0.1:53 的转发器：
//   - macOS 的 /etc/resolver 只按域名后缀分发查询，裸名（nacos）没有后缀可
//     匹配，除非改全局 search domain —— 那会影响本机所有网络。所以 DNS 方案
//     必然要求用户写 nacos.weft，多一层记忆成本。
//   - hosts 是系统级、与后缀无关，ssh / curl / 数据库客户端 / IDE 全都认，
//     不需要各配一遍代理。
//   - 代价是多一个静态文件：设备改名或换 IP 时靠轮询重写，断开连接或关闭
//     功能时整块删掉，不留残留。
//
// 只写 100.x 的 IPv4：tailscale 的 IPv6 日常用不到，写进去反而让 curl 先试
// IPv6 再超时回退。
// hostsFile 用变量而不是常量：单测要指到临时文件上，才能真跑一遍
// 「读现有文件 → 合并 → 原子替换」这条路。
var hostsFile = "/etc/hosts"

const (
	hostsBegin = "# >>> 云栈名字解析（自动维护，关闭功能或退出应用后删除）>>>"
	hostsEnd   = "# <<< 云栈名字解析 <<<"
)

// HostEntry 是一条「设备名 → 地址」映射。
type HostEntry struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// HostsResult 是一次同步的结果，界面直接展示。
type HostsResult struct {
	Applied int      `json:"applied"`
	Skipped []string `json:"skipped"` // 与已有条目或彼此冲突，没有写进去
	Path    string   `json:"path"`
}

// HostsEnabled 回答「/etc/hosts 里现在有没有我们这段」，
// 用来在启动时清理上一次异常退出留下的区块。
func HostsEnabled() bool {
	b, err := os.ReadFile(hostsFile)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), hostsBegin)
}

// SyncHosts 把当前设备名同步进 /etc/hosts。
// entries 为空等同于 ClearHosts：功能关掉时不该在系统文件里留东西。
func SyncHosts(entries []HostEntry) (HostsResult, error) {
	return applyHosts(entries, false)
}

// ClearHosts 删掉我们那段区块，其他行一个字节都不动。
func ClearHosts() error {
	_, err := applyHosts(nil, true)
	return err
}

func applyHosts(entries []HostEntry, clear bool) (HostsResult, error) {
	res := HostsResult{Path: hostsFile}
	if clear {
		entries = nil
	}
	// 已经以 root 跑（开发时直接 sudo 启动）就不必绕助手
	if os.Geteuid() == 0 {
		applied, skipped, err := writeHostsBlock(entries)
		res.Applied, res.Skipped = applied, skipped
		return res, err
	}

	resp, err := askDaemonHosts(entries, clear)
	if err != nil {
		return res, err
	}
	res.Applied, res.Skipped = resp.Applied, resp.Skipped
	return res, nil
}

// askDaemonHosts 交给常驻助手写。只传名字和地址，由助手自己校验、自己拼
// 标记区块——助手不接收整份文件内容，免得这个通道变成「任意改 hosts」的后门。
func askDaemonHosts(entries []HostEntry, clear bool) (helperResponse, error) {
	var out helperResponse
	conn, err := net.DialTimeout("unix", helperSocket, 3*time.Second)
	if err != nil {
		return out, fmt.Errorf("改 /etc/hosts 需要系统助手：先在上方装好系统助手再打开")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	req := helperRequest{Op: "hosts", Hosts: entries, ClearHosts: clear}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return out, fmt.Errorf("连不上系统助手：%w", err)
	}
	if err := json.NewDecoder(conn).Decode(&out); err != nil {
		return out, fmt.Errorf("系统助手没有回应：%w", err)
	}
	if !out.OK {
		if strings.Contains(out.Error, "未知操作") {
			return out, fmt.Errorf("系统助手版本较旧，到设置页点「重新安装」后再开启")
		}
		return out, fmt.Errorf("%s", out.Error)
	}
	return out, nil
}

// writeHostsBlock 读现有 hosts、替换区块、原子落盘。只有 root 会走到这里。
func writeHostsBlock(entries []HostEntry) (int, []string, error) {
	cur, err := os.ReadFile(hostsFile)
	if err != nil {
		return 0, nil, fmt.Errorf("读 %s 失败: %w", hostsFile, err)
	}
	next, applied, skipped := mergeHosts(string(cur), entries)
	if next == string(cur) {
		return applied, skipped, nil // 没变化就别碰文件和 DNS 缓存
	}
	if err := replaceFile(hostsFile, []byte(next)); err != nil {
		return 0, nil, err
	}
	flushDNSCache()
	return applied, skipped, nil
}

// mergeHosts 是纯函数，方便单测：保留别人写的行，只替换自己那段。
func mergeHosts(content string, entries []HostEntry) (string, int, []string) {
	base := stripBlock(content)
	taken := map[string]bool{} // hosts 里已经存在的名字，不去覆盖
	for _, ln := range strings.Split(base, "\n") {
		fields := strings.Fields(stripComment(ln))
		if len(fields) < 2 {
			continue // 空行、注释、或只有地址没名字
		}
		for _, n := range fields[1:] {
			taken[strings.ToLower(strings.TrimSpace(n))] = true
		}
	}

	seen := map[string]string{}
	lines := make([]string, 0, len(entries))
	skipped := make([]string, 0)
	for _, e := range entries {
		name, ok := sanitizeHostName(e.Name)
		if !ok {
			continue
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(e.IP))
		if err != nil || !addr.Is4() {
			continue
		}
		ip := addr.String()
		if prev, dup := seen[name]; dup {
			// 同一个名字在两台设备上都叫 nacos：只写第一条，让用户自己去改名
			if prev != ip {
				skipped = append(skipped, name)
			}
			continue
		}
		if taken[name] {
			skipped = append(skipped, name)
			continue
		}
		seen[name] = ip
		lines = append(lines, ip+"\t"+name)
	}
	sort.Strings(lines)

	if len(lines) == 0 {
		return base, 0, dedupStrings(skipped)
	}
	var b strings.Builder
	b.WriteString(base)
	if base != "" && !strings.HasSuffix(base, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(hostsBegin + "\n")
	b.WriteString("# 由云栈维护，手改会被下次同步覆盖。\n")
	for _, ln := range lines {
		b.WriteString(ln + "\n")
	}
	b.WriteString(hostsEnd + "\n")
	return b.String(), len(lines), dedupStrings(skipped)
}

// stripBlock 摘掉标记区块，返回其余内容。没有区块就原样返回。
func stripBlock(content string) string {
	start := strings.Index(content, hostsBegin)
	if start < 0 {
		return content
	}
	endRel := strings.Index(content[start:], hostsEnd)
	if endRel < 0 {
		// 只写了一半（上次写到一半被杀）：从标记处一直截到文件尾
		return strings.TrimRight(content[:start], "\n") + "\n"
	}
	end := start + endRel + len(hostsEnd)
	for end < len(content) && content[end] == '\n' {
		end++
	}
	out := content[:start] + content[end:]
	return strings.TrimRight(out, "\n") + "\n"
}

func stripComment(line string) string {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		return line[:i]
	}
	return line
}

// sanitizeHostName 只接受最常见的主机名：字母数字和连字符，点也允许
// （headscale 里有人写 web-01.prod）。其他一律丢掉，避免把奇怪字符
// 写进系统文件。
func sanitizeHostName(name string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || len(n) > 63 {
		return "", false
	}
	if n[0] == '-' || n[0] == '.' || n[len(n)-1] == '-' || n[len(n)-1] == '.' {
		return "", false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.'
		if !ok {
			return "", false
		}
	}
	// 纯数字（或带点的纯数字）是 IP，别当名字写进 hosts
	if _, err := netip.ParseAddr(n); err == nil {
		return "", false
	}
	return n, true
}

func dedupStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := map[string]bool{}
	out := in[:0]
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// replaceFile 在同一个目录写临时文件再改名，避免中途失败留下半截 hosts。
// /etc/hosts 是系统文件，写坏会让整机域名解析异常，不能直接截断重写。
func replaceFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hosts.weft-")
	if err != nil {
		return fmt.Errorf("写 %s 失败: %w", path, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o644)
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("替换 %s 失败: %w", path, err)
	}
	return nil
}

// flushDNSCache 让改动立刻生效。macOS 不刷缓存时 hosts 变化要等一会儿
// 才看得见，用户会以为没生效。
func flushDNSCache() {
	if runtime.GOOS == "darwin" {
		_ = exec.Command("/usr/bin/dscacheutil", "-flushcache").Run()
		_ = exec.Command("/usr/bin/killall", "-HUP", "mDNSResponder").Run()
		return
	}
	if p, err := exec.LookPath("resolvectl"); err == nil {
		_ = exec.Command(p, "flush-caches").Run()
	}
}
