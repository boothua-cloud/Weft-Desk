package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type proxyEntry struct {
	ln     net.Listener
	server *http.Server
	local  string
}

// proxyHub 给每个 tailnet 内地址在 127.0.0.1 上开一个 HTTP 反向代理。
// tsnet 的网络栈在进程内，WebView 直接请求 100.x 有可能拿不到路由，
// 必须经这层转发；这也意味着系统本身和其它程序完全不在 tailnet 内。
type proxyHub struct {
	mu       sync.Mutex
	dial     dialFunc
	items    map[string]*proxyEntry
	insecure atomic.Bool
}

func newProxyHub(d dialFunc) *proxyHub {
	return &proxyHub{dial: d, items: map[string]*proxyEntry{}}
}

func (h *proxyHub) setInsecure(v bool) { h.insecure.Store(v) }

func (h *proxyHub) resolve(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	target, err := url.Parse(raw)
	if err != nil || target.Host == "" {
		return "", fmt.Errorf("应用地址无效: %s", raw)
	}
	key := target.Scheme + "://" + target.Host + target.Path

	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.items[key]; ok {
		return e.local + target.RequestURI() + fragmentOf(target), nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("开启本地端口失败: %w", err)
	}
	local := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)

	proxy := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = target.Scheme
			r.URL.Host = target.Host
			r.Host = target.Host // 很多内网应用会校验 Host 头
		},
		Transport: &http.Transport{
			DialContext:     h.dial, // 唯一的区别：出站走 tailnet，不走系统网络
			TLSClientConfig: &tls.Config{InsecureSkipVerify: h.insecure.Load()},
		},
		ModifyResponse: func(resp *http.Response) error {
			prefix := target.Scheme + "://" + target.Host
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, prefix) {
				resp.Header.Set("Location", local+strings.TrimPrefix(loc, prefix))
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "Weft 无法连接到 "+target.Host+"\n"+err.Error(), http.StatusBadGateway)
		},
	}
	srv := &http.Server{Handler: proxy}
	go func() { _ = srv.Serve(ln) }()
	h.items[key] = &proxyEntry{ln: ln, server: srv, local: local}
	return local + target.RequestURI() + fragmentOf(target), nil
}

// fragmentOf 保留 #/login 这类前端路由，RequestURI() 不包含片段。
func fragmentOf(u *url.URL) string {
	if u.Fragment == "" {
		return ""
	}
	return "#" + u.Fragment
}

func (h *proxyHub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, e := range h.items {
		_ = e.server.Close()
		_ = e.ln.Close()
		delete(h.items, k)
	}
}
