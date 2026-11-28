package desktop

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// 站点图标抓取。书签只存地址，图标是「看一眼就知道点哪个」的东西，
// 让用户自己找 png 上传等于把这个活推回去，所以能抓就抓。

const (
	iconMaxBytes = 256 << 10 // 256 KB，超过这个尺寸的图标没有意义
	pageMaxBytes = 512 << 10 // 只读页面开头，图标声明都在 <head> 里
	iconTimeout  = 5 * time.Second
)

// iconClient 跳过证书校验：内网自签证书很常见，而图标只是展示用的图片。
// 用户本来就是要访问这个站点，真正的信任判断在「打开应用」那一步，
// 卡在这里只会得到一排空色块。重定向限三次，防止绕圈。
var iconClient = &http.Client{
	Timeout: iconTimeout,
	Transport: &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("重定向次数太多")
		}
		return nil
	},
}

// fetchSiteIcon 返回一个 data URL。失败时给出人话原因，前端只在
// 批量补齐时汇总展示，单条失败不弹窗。
func fetchSiteIcon(raw string) (string, error) {
	target, err := appTargetURL(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("地址解析不了：%s", raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), iconTimeout)
	defer cancel()

	// 一、先试约定的 /favicon.ico。绝大多数站点都有，省掉一次页面请求。
	if data, err := fetchIconAt(ctx, u.Scheme+"://"+u.Host+"/favicon.ico"); err == nil {
		return data, nil
	}

	// 二、读页面里的 <link rel="icon">。很多站点把图标放在 CDN 或
	// 带哈希的文件名里，/favicon.ico 是 404 或者一张 1×1 的占位图。
	page, err := fetchHTML(ctx, target)
	if err != nil {
		return "", err
	}
	href := bestIconHref(page, target)
	if href == "" {
		return "", errors.New("页面里没有声明图标")
	}
	return fetchIconAt(ctx, href)
}

func fetchIconAt(ctx context.Context, raw string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; WeftDesk icon fetch)")
	resp, err := iconClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, iconMaxBytes+1))
	if err != nil {
		return "", err
	}
	return iconDataURL(b)
}

func fetchHTML(ctx context.Context, target string) (*html.Node, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; WeftDesk icon fetch)")
	resp, err := iconClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("页面 HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, pageMaxBytes))
	if err != nil {
		return nil, err
	}
	return html.Parse(bytes.NewReader(b))
}

// bestIconHref 在 <head> 里找图标声明，按 sizes 选最大的。
// 只看 rel 里有 icon 的 link；apple-touch-icon 也认，
// 苹果那套图标通常是无背景的高清方图，比 16×16 的清楚。
func bestIconHref(doc *html.Node, base string) string {
	type cand struct {
		href string
		size int
	}
	baseURL, _ := url.Parse(base)
	var cands []cand

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "link" {
			rel, href := "", ""
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "rel":
					rel = strings.ToLower(a.Val)
				case "href":
					href = strings.TrimSpace(a.Val)
				}
			}
			if href != "" && strings.Contains(rel, "icon") {
				c := cand{href: resolveRef(baseURL, href)}
				for _, a := range n.Attr {
					if strings.ToLower(a.Key) == "sizes" {
						c.size = parseSizes(a.Val)
					}
				}
				cands = append(cands, c)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(cands) == 0 {
		return ""
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.size > best.size {
			best = c
		}
	}
	return best.href
}

// parseSizes 只认「32x32」这种，`any`（矢量图）当作比任何位图都大：
// SVG 在任何尺寸下都清楚。
func parseSizes(v string) int {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "any" {
		return 1 << 20
	}
	parts := strings.SplitN(v, "x", 2)
	if len(parts) != 2 {
		return 0
	}
	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0
	}
	return w
}

func resolveRef(base *url.URL, href string) string {
	if base == nil {
		return href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(u).String()
}

// iconDataURL 把字节套成 data URL。类型靠内容嗅探而不是响应头：
// 不少服务端把 .ico 当 octet-stream 发，甚至有把 404 页面当图标回的，
// 那种情况这里会直接判成「不是图片」。
func iconDataURL(b []byte) (string, error) {
	if len(b) == 0 {
		return "", errors.New("图标是空的")
	}
	if len(b) > iconMaxBytes {
		return "", fmt.Errorf("图标超过 %d KB", iconMaxBytes>>10)
	}
	mime := http.DetectContentType(b)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp",
		"image/x-icon", "image/vnd.microsoft.icon", "image/bmp":
	default:
		if isSVG(b) {
			mime = "image/svg+xml"
		} else {
			return "", fmt.Errorf("不是图片（%s）", mime)
		}
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

func isSVG(b []byte) bool {
	head := b
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(bytes.ToLower(head), []byte("<svg"))
}
