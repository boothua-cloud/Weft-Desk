package desktop

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pngBytes 造一张 size×size 的 PNG。图标抓取现在会拒绝 1×1 的占位图，
// 测试数据不能再用那种透明像素。
func pngBytes(t *testing.T, size int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, color.NRGBA{R: 0x18, G: 0x5F, B: 0xA5, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFetchSiteIconFromFavicon(t *testing.T) {
	png := pngBytes(t, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/favicon.ico" {
			http.NotFound(w, r)
			return
		}
		// 故意不给 Content-Type：真实站点经常这样发 ico/png
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	got, err := fetchSiteIcon(srv.URL)
	if err != nil {
		t.Fatalf("抓图标失败: %v", err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("不是 PNG 的 data URL: %.60s", got)
	}
}

func TestFetchSiteIconPrefersDeclaredBigIcon(t *testing.T) {
	png := pngBytes(t, 64)
	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		// 约定位置只有 16×16
		_, _ = w.Write(pngBytes(t, 16))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head>
			<link rel="icon" href="/favicon.ico" sizes="16x16">
			<link rel="apple-touch-icon" href="/big.png">
			<link rel="mask-icon" href="/mask.svg">
		</head><body></body></html>`)
	})
	mux.HandleFunc("/big.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})
	mux.HandleFunc("/mask.svg", func(w http.ResponseWriter, r *http.Request) {
		t.Error("mask-icon 是单色剪影，不该被当成应用图标")
		http.Error(w, "no", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := fetchSiteIcon(srv.URL)
	if err != nil {
		t.Fatalf("抓图标失败: %v", err)
	}
	if !strings.Contains(got, "iVBOR") && !strings.HasPrefix(got, "data:image/png") {
		t.Fatalf("应取高清的那张: %.60s", got)
	}
}

func TestFetchSiteIconSkipsTinyImage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pngBytes(t, 1))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><head><link rel="icon" href="/real.png" sizes="64x64"></head></html>`)
	})
	mux.HandleFunc("/real.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pngBytes(t, 64))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := fetchSiteIcon(srv.URL)
	if err != nil {
		t.Fatalf("应该跳过 1×1 取下一张: %v", err)
	}
	if got == "" {
		t.Fatal("没有取到图标")
	}
}

func TestFetchSiteIconOnlyTinyIsError(t *testing.T) {
	png := pngBytes(t, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	if _, err := fetchSiteIcon(srv.URL); err == nil {
		t.Fatal("只有 1×1 占位图时应该报错")
	}
}

func TestFetchSiteIconRejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><head></head><body>没有图标</body></html>")
	}))
	defer srv.Close()

	if _, err := fetchSiteIcon(srv.URL); err == nil {
		t.Fatal("没有图标时应该报错，而不是返回一段 HTML 的 data URL")
	}
}

func TestIconDataURLRejectsHTML(t *testing.T) {
	if _, err := iconDataURL([]byte("<html>404</html>")); err == nil {
		t.Fatal("HTML 不该被当成图标")
	}
}

func TestParseSizes(t *testing.T) {
	cases := map[string]int{"32x32": 32, "16X16": 16, "any": 1 << 20, "": 0}
	for in, want := range cases {
		if got := parseSizes(in); got != want {
			t.Fatalf("parseSizes(%q) = %d, want %d", in, got, want)
		}
	}
}

// 内网地址不能走系统代理：公司出口常把所有域名拦成同一个页面，
// 图标就会变成同一张图。
func TestIsIntranetHost(t *testing.T) {
	cases := map[string]bool{
		"localhost":    true,
		"nas":          true,
		"print.local":  true,
		"10.0.1.5":     true,
		"192.168.1.10": true,
		"172.16.3.4":   true,
		"100.64.0.5":   true,
		"100.100.1.2":  true,
		"127.0.0.1":    true,
		"[::1]":        true,
		"gyy.k3s.io":   false,
		"8.8.8.8":      false,
		"172.32.0.1":   false,
	}
	for host, want := range cases {
		if got := isIntranetHost(host); got != want {
			t.Errorf("isIntranetHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestICODims(t *testing.T) {
	// 目录里两档：16×16 和 48×48（宽高字节 0 表示 256，这里不用）
	ico := []byte{
		0, 0, 1, 0, 2, 0,
		16, 16, 0, 0, 1, 0, 32, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		48, 48, 0, 0, 1, 0, 32, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}
	if w, h := icoDims(ico); w != 48 || h != 48 {
		t.Fatalf("icoDims = %d×%d, want 48×48", w, h)
	}
	if w, _ := icoDims([]byte("not an ico")); w != 0 {
		t.Fatalf("非 ICO 应返回 0，得到 %d", w)
	}
}

// 首页跳转后，页面里的 ./img/x.png 要按跳转后的地址解析。
// Nacos 从 / 跳到 /next/，按原始地址算会 404。
func TestFetchSiteIconResolvesRelativeAfterRedirect(t *testing.T) {
	png := pngBytes(t, 64)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/next/", http.StatusFound)
		case "/next/":
			_, _ = io.WriteString(w, `<html><head><link rel="icon" href="./img/x.png"></head></html>`)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/next/img/x.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(png)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := fetchSiteIcon(srv.URL + "/"); err != nil {
		t.Fatalf("相对路径应按跳转后的地址解析: %v", err)
	}
}

func TestIconErrText(t *testing.T) {
	cases := []struct{ in, want string }{
		{`Get "https://x/favicon.ico": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`, "连接超时，站点没在 5 秒内回应"},
		{`dial tcp 10.0.1.9:22: connect: connection refused`, "端口没有服务在听"},
		{`Get "https://nope.invalid/": dial tcp: lookup nope.invalid: no such host`, "域名解析不了"},
		{`HTTP 404`, "HTTP 404"},
	}
	for _, c := range cases {
		err := errors.New(c.in)
		if got := iconErrText(err); got != c.want {
			t.Errorf("iconErrText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if iconErrText(nil) != "" {
		t.Error("nil 应该是空串")
	}
}
