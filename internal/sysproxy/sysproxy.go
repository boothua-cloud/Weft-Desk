package sysproxy

import (
	"net/http"
	"net/url"
)

// proxyFromSystem 先看环境变量（http_proxy / https_proxy / no_proxy），
// 没有再用系统代理设置。两条都没有就直连。
func FromRequest(req *http.Request) (*url.URL, error) {
	if u, err := http.ProxyFromEnvironment(req); err == nil && u != nil {
		return u, nil
	}
	return systemProxyURL(req.URL.Hostname()), nil
}
