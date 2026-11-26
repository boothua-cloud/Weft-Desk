package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 用本地 httptest 顶替 tailnet 目标，验证 127.0.0.1 代理的转发、
// Host / Location 重写与端口复用，不需要真实控制面。
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

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(local)
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
	resp, err := http.Get(local)
	if err != nil {
		t.Fatalf("开启信任后仍失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "ikua:/login") {
		t.Fatalf("状态=%d body=%q", resp.StatusCode, body)
	}

	strict := newProxyHub(dial)
	defer strict.closeAll()
	strict.setInsecure(false)
	raw, err := strict.resolve(upstream.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	// 代理把 TLS 失败转成 502（页面里会显示原因），所以看状态码而不是 err
	bad, err := http.Get(raw)
	if err != nil {
		return
	}
	defer bad.Body.Close()
	if bad.StatusCode == 200 {
		t.Fatal("未开启信任时不应成功，说明代理没有校验证书")
	}
}
