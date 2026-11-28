package desktop

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 1×1 的透明 PNG，够让 http.DetectContentType 认出 image/png
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func pngBytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFetchSiteIconFromFavicon(t *testing.T) {
	png := pngBytes(t)
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

func TestFetchSiteIconFromLinkTag(t *testing.T) {
	png := pngBytes(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		// SPA 常见：/favicon.ico 回一张 HTML
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<!doctype html><html><body>app</body></html>")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head>
			<link rel="shortcut icon" href="/small.png" sizes="16x16">
			<link rel="icon" href="/big.png" sizes="96x96">
		</head><body></body></html>`)
	})
	mux.HandleFunc("/big.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})
	mux.HandleFunc("/small.png", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "should not be picked", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := fetchSiteIcon(srv.URL)
	if err != nil {
		t.Fatalf("抓图标失败: %v", err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("应优先取 sizes 更大的那张: %.60s", got)
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
