package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 用本地 httptest 顶替 tailnet 目标，验证 127.0.0.1 代理的转发、
// Host / Location 重写与端口复用，不需要真实服务端。
func TestProxyHubForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "nas.internal:5000" {
			t.Errorf("Host 未正确重写: %s", r.Host)
		}
		if r.URL.Path != "/app" {
			t.Errorf("路径丢失: %s", r.URL.Path)
		}
		w.Header().Set("Location", "http://nas.internal:5000/next")
		w.WriteHeader(http.StatusFound)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	hub := newProxyHub(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	})
	defer hub.closeAll()

	local, err := hub.resolve("http://nas.internal:5000/app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(local, "http://127.0.0.1:") {
		t.Fatalf("未返回本地地址: %s", local)
	}
	u, err := url.Parse(local)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Scheme + "://" + u.Host

	noFollow := func(c *http.Client) *http.Client {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}

	// 不带令牌直接打端口：同机的其它进程扫到了也不能借道
	bare, err := noFollow(&http.Client{}).Get(host + "/app")
	if err != nil {
		t.Fatal(err)
	}
	_ = bare.Body.Close()
	if bare.StatusCode != http.StatusForbidden {
		t.Fatalf("无令牌应被拒，实际 %d", bare.StatusCode)
	}

	// 令牌不对也一样
	wrong, err := noFollow(&http.Client{}).Get(host + "/__weft/deadbeef/app")
	if err != nil {
		t.Fatal(err)
	}
	_ = wrong.Body.Close()
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("错误令牌应被拒，实际 %d", wrong.StatusCode)
	}

	// 首次带前缀进来：种 cookie 并跳回真实路径
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	withJar := noFollow(&http.Client{Jar: jar})
	entry, err := withJar.Get(local)
	if err != nil {
		t.Fatal(err)
	}
	_ = entry.Body.Close()
	if entry.StatusCode != http.StatusFound {
		t.Fatalf("入口应跳转，实际 %d", entry.StatusCode)
	}
	if loc := entry.Header.Get("Location"); loc != "/app" {
		t.Fatalf("入口跳转地址: %s", loc)
	}

	// 之后页面里的绝对路径请求（/api、/assets）靠 cookie 放行
	resp, err := withJar.Get(host + "/app")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("状态码: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "http://127.0.0.1:") || strings.Contains(loc, "nas.internal") {
		t.Fatalf("Location 未改写回本地地址: %s", loc)
	}

	again, err := hub.resolve("http://nas.internal:5000/app")
	if err != nil {
		t.Fatal(err)
	}
	if again != local {
		t.Fatalf("同一目标未复用端口: %s vs %s", local, again)
	}
}

// 自签证书的内网应用是白屏的根因：WebView 直接拒绝导航。
// 验证「本地代理 + 放宽校验」能取到页面，且开关关闭时仍然失败。
func TestProxySelfSigned(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ikua:"+r.URL.Path)
	}))
	defer upstream.Close()

	dial := (&net.Dialer{Timeout: 5 * time.Second}).DialContext

	// open 模拟窗口里的浏览器：先过令牌门，再按 cookie 取真实页面
	open := func(local string) (*http.Response, string) {
		u, err := url.Parse(local)
		if err != nil {
			t.Fatal(err)
		}
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Jar: jar}
		resp, err := client.Get(local)
		if err != nil {
			t.Fatalf("打不开 %s: %v", local, err)
		}
		if resp.StatusCode == http.StatusFound {
			_ = resp.Body.Close()
			resp, err = client.Get(u.Scheme + "://" + u.Host + "/login")
			if err != nil {
				t.Fatalf("跟随后失败: %v", err)
			}
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(body)
	}

	hub := newProxyHub(dial)
	defer hub.closeAll()
	hub.setInsecure(true)

	local, err := hub.resolve(upstream.URL + "/login#/login")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(local, "#/login") {
		t.Fatalf("片段被丢弃: %s", local)
	}
	resp, body := open(local)
	if resp.StatusCode != 200 || !strings.HasPrefix(body, "ikua:/login") {
		t.Fatalf("开启信任后仍失败: 状态=%d body=%q", resp.StatusCode, body)
	}

	strict := newProxyHub(dial)
	defer strict.closeAll()
	strict.setInsecure(false)
	raw, err := strict.resolve(upstream.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	// 代理把 TLS 失败转成 502（页面里会显示原因），所以看状态码而不是 err
	bad, _ := open(raw)
	if bad.StatusCode == 200 {
		t.Fatal("未开启信任时不应成功，说明代理没有校验证书")
	}
}
