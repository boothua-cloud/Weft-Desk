//go:build windows

package sysproxy

import (
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// systemProxyURL 读 Windows 的 Internet 选项。
// ProxyServer 可能是 "host:port"，也可能是 "http=h:p;https=h:p" 这种分协议写法。
func systemProxyURL(host string) *url.URL {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	on, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || on == 0 {
		return nil
	}
	raw, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "=") {
		https, http := "", ""
		for _, part := range strings.Split(raw, ";") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(kv[0])) {
			case "https":
				https = strings.TrimSpace(kv[1])
			case "http":
				http = strings.TrimSpace(kv[1])
			}
		}
		if https != "" {
			raw = https
		} else {
			raw = http
		}
	}
	if raw == "" {
		return nil
	}
	return &url.URL{Scheme: "http", Host: raw}
}
