package engine

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/boothua/weft/internal/model"
)

// dialFunc 是引擎内统一的拨号签名，和 model.DialFunc 一致。
type dialFunc = model.DialFunc

type proxyEntry struct {
	ln     net.Listener
	server *http.Server
	local  string
	token  string
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
		return e.local + entryPrefix(e.token) + strings.TrimPrefix(target.RequestURI(), "/") + fragmentOf(target), nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("开启本地端口失败: %w", err)
	}
	local := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)

	tok, err := RandHex(16)
	if err != nil {
		_ = ln.Close()
		return "", fmt.Errorf("生成访问令牌失败: %w", err)
	}

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
	// 这个端口只听 127.0.0.1，但同机任何进程都能扫到它，扫到就等于借用了
	// 你的节点身份去访问内网（ACL 眼里就是你自己在访问）。所以给它一道门：
	// 首次带 /__weft/<token>/ 进来，种下 cookie 后跳到真实路径，
	// 之后页面自己的绝对路径请求（/api/...）靠 cookie 放行。
	ckName, ckValue := entryCookie(tok)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := entryPrefix(tok)
		if r.URL.Path == p || strings.HasPrefix(r.URL.Path, p) {
			http.SetCookie(w, &http.Cookie{
				Name: ckName, Value: ckValue, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			dest := strings.TrimPrefix(r.URL.RequestURI(), p)
			if !strings.HasPrefix(dest, "/") {
				dest = "/" + dest
			}
			http.Redirect(w, r, dest, http.StatusFound)
			return
		}
		c, cerr := r.Cookie(ckName)
		if cerr != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(ckValue)) != 1 {
			http.Error(w, "这个本地端口只接受从云栈打开的应用窗口", http.StatusForbidden)
			return
		}
		// 我们自己的 cookie 不往目标服务转发
		cookies := r.Cookies()
		r.Header.Del("Cookie")
		for _, ck := range cookies {
			if ck.Name == ckName {
				continue
			}
			r.AddCookie(ck)
		}
		proxy.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	h.items[key] = &proxyEntry{ln: ln, server: srv, local: local, token: tok}
	return local + entryPrefix(tok) + strings.TrimPrefix(target.RequestURI(), "/") + fragmentOf(target), nil
}

func entryPrefix(token string) string { return "/__weft/" + token + "/" }

// entryCookie 用 token 的短摘要做 cookie 名：不同 entry 共用 127.0.0.1，
// 名字一样会互相覆盖。值只要「对得上」就够，不留明文 token。
func entryCookie(token string) (string, string) {
	sum := sha256.Sum256([]byte(token))
	return "weft_px_" + hex.EncodeToString(sum[:6]), "1"
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
