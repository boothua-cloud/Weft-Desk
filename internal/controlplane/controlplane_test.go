package controlplane

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boothua/weft/internal/model"
)

// 服务端客户端的单测：错误分类、Key 探测、TLS 配置。

// 只有 401/403 能当「钥匙不对」用：界面据此把服务端管理整组收起来。
// 500、404、超时都不能算 —— 离线或者服务端自己抽风时藏掉入口，
// 用户只会以为软件坏了。
func TestIsRejected(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{http.StatusOK, false},
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, true},
		{http.StatusNotFound, false},
		{http.StatusInternalServerError, false},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"code":1,"message":"x"}`))
		}))
		_, err := DoJSON("GET", srv.URL+"/api/v1/node", "k", nil, nil)
		srv.Close()
		if c.status < 300 {
			if err != nil {
				t.Fatalf("status %d: 不该报错，得到 %v", c.status, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("status %d: 应该报错", c.status)
		}
		if got := IsRejected(err); got != c.want {
			t.Errorf("status %d: IsRejected=%v，期望 %v", c.status, got, c.want)
		}
	}
}

// CheckKey 要打到 /api/v1/node 并带上 Bearer，不能顺手改成别的接口：
// 这是唯一一个「有 Key 就能读」的端点，拿它当探针最稳。
func TestCheckKeyUsesNodeEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	defer srv.Close()

	cp := &HeadscaleCP{cfg: model.Config{BaseURL: srv.URL}}
	if err := cp.checkKeyWith("hskey-api-abc"); err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if gotPath != "/api/v1/node" {
		t.Errorf("请求路径是 %q，期望 /api/v1/node", gotPath)
	}
	if gotAuth != "Bearer hskey-api-abc" {
		t.Errorf("Authorization 是 %q", gotAuth)
	}
}

// 形状判断只是离线时的先手：能明确说「这不可能是一把管理 Key」的才报 false。
// 节点 Auth Key 也算 false —— 它同样是 hskey- 开头，但管理接口一律 401。
func TestLooksLikeAPIKey(t *testing.T) {
	yes := []string{"hskey-api-abc", "hskey-apikey-abc", "tskey-api-abc"}
	no := []string{"1", "", "abc", "hskey-auth-abc", "tskey-auth-abc", "Bearer abc"}
	for _, k := range yes {
		if !LooksLikeAPIKey(k) {
			t.Errorf("%q 应该被当成管理 Key", k)
		}
	}
	for _, k := range no {
		if LooksLikeAPIKey(k) {
			t.Errorf("%q 不该被当成管理 Key", k)
		}
	}
}

// 自签证书的 https 服务：给了 CA 能连，没给就不行，勾了跳过校验也能连。
func TestControlPlaneTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	noProxy := func(cl *http.Client) *http.Client {
		cl.Transport.(*http.Transport).Proxy = nil
		return cl
	}

	get := func(c model.Config) error {
		resp, err := noProxy(NewHTTPClient(c)).Get(srv.URL)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	if err := get(model.Config{}); err == nil {
		t.Error("没配 CA 时不该连上自签服务")
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := get(model.Config{CACert: string(caPEM)}); err != nil {
		t.Errorf("给了 CA 应该能连: %v", err)
	}
	if err := get(model.Config{ControlInsecure: true}); err != nil {
		t.Errorf("跳过校验应该能连: %v", err)
	}
}

func TestCertPoolRejectsGarbage(t *testing.T) {
	if _, err := CertPool("not a pem"); err == nil {
		t.Error("乱填的内容应该报错")
	}
	if _, err := x509.SystemCertPool(); err != nil {
		t.Skip("系统证书池不可用")
	}
}
