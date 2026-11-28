package controlplane

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boothua/weft/internal/model"
)

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
