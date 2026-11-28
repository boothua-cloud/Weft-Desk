package sysproxy

import (
	"net"
	"net/url"
	"os/exec"
	"strings"
)

// systemProxyURL 读 macOS 网络设置里当前生效的代理。
//
// scutil --proxy 给的是系统层面那一套，命令行进程也能用上。
// 按 HTTPS → HTTP → SOCKS 的顺序取第一个开着的；目标命中例外列表就不走代理，
// 否则内网域名会被绕到代理上，反而连不上。
func systemProxyURL(host string) *url.URL {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return nil
	}
	fields, exceptions := parseSCUtilProxy(string(out))
	if excepted(host, exceptions) {
		return nil
	}
	pick := func(prefix string) *url.URL {
		if fields[prefix+"Enable"] != "1" {
			return nil
		}
		h, p := fields[prefix+"Proxy"], fields[prefix+"Port"]
		if h == "" || p == "" {
			return nil
		}
		scheme := "http"
		if prefix == "SOCKS" {
			scheme = "socks5"
		}
		return &url.URL{Scheme: scheme, Host: net.JoinHostPort(h, p)}
	}
	for _, p := range []string{"HTTPS", "HTTP", "SOCKS"} {
		if u := pick(p); u != nil {
			return u
		}
	}
	return nil
}

// parseSCUtilProxy 拆 scutil 的字典输出现。ExceptionsList 是个数组，
// 单独收一份，其余是 key : value。
func parseSCUtilProxy(text string) (map[string]string, []string) {
	fields := map[string]string{}
	var exceptions []string
	inList := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExceptionsList") {
			inList = true
			continue
		}
		if inList {
			if line == "}" {
				inList = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				exceptions = append(exceptions, strings.TrimSpace(v))
			}
			continue
		}
		k, v, ok := strings.Cut(line, " : ")
		if !ok || strings.HasSuffix(k, "<array> {") {
			continue
		}
		fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return fields, exceptions
}

// excepted 判断目标是否在代理例外里。
// 支持域名、*.通配、CIDR、<local>（不含点的主机名），够覆盖系统设置里常见的写法。
func excepted(host string, list []string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	ip := net.ParseIP(h)
	for _, raw := range list {
		e := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case e == "":
		case e == "<local>":
			if !strings.Contains(h, ".") {
				return true
			}
		case strings.Contains(e, "/"):
			if ip != nil {
				if _, n, err := net.ParseCIDR(e); err == nil && n.Contains(ip) {
					return true
				}
			}
		case strings.HasPrefix(e, "*."):
			if strings.HasSuffix(h, e[1:]) {
				return true
			}
		default:
			if h == e || strings.HasSuffix(h, "."+e) {
				return true
			}
		}
	}
	return false
}
