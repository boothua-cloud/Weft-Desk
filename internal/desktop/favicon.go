package desktop

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// 站点图标抓取。书签只存地址，图标是「看一眼就知道点哪个」的东西，
// 让用户自己找 png 上传等于把这个活推回去，所以能抓就抓。

const (
	iconMaxBytes   = 256 << 10 // 256 KB，超过这个尺寸的图标没有意义
	pageMaxBytes   = 512 << 10 // 只读页面开头，图标声明都在 <head> 里
	iconTimeout    = 5 * time.Second
	iconBudget     = 15 * time.Second // 一次抓取的总预算：页面 + 最多几个候选
	iconCandidates = 4                // 一次最多试几个地址，别为了一个 32px 的图把站点爬一遍
	iconGoodEnough = 128              // 拿到 128px 以上就不再往下试
)

// iconClient 跳过证书校验：内网自签证书很常见，而图标只是展示用的图片。
// 用户本来就是要访问这个站点，真正的信任判断在「打开应用」那一步，
// 卡在这里只会得到一排空色块。重定向限三次，防止绕圈。
var iconClient = &http.Client{
	Timeout: iconTimeout,
	Transport: &http.Transport{
		Proxy:           proxyForIcon,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("重定向次数太多")
		}
		return nil
	},
}

// proxyForIcon 让内网地址直连。
//
// 公司的 HTTP 出口代理经常不转发内网域名，而是自己回一张拦截页或者
// 一个统一的站标——所有 10.x 应用的图标会变成同一张图。tailnet 的
// 100.64/10 本来就该直连。公网域名仍然按环境变量走代理，否则在必须
// 过代理的网络里连图标都取不到。
func proxyForIcon(req *http.Request) (*url.URL, error) {
	if isIntranetHost(req.URL.Hostname()) {
		return nil, nil
	}
	return http.ProxyFromEnvironment(req)
}

func isIntranetHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	// MagicDNS 名字是单标签（nas、build-01），带点的才是公网域名
	if !strings.Contains(h, ".") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	// 100.64.0.0/10 是 CGNAT，IsPrivate 不覆盖，但它是 tailnet 的常用段
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// iconCand 是一个候选图标地址，size 是页面自己声明的边长（没写就是 0）。
type iconCand struct {
	href string
	size int
}

// fetchSiteIcon 返回一个 data URL。失败时给出人话原因。
//
// 顺序不是「先 /favicon.ico」，而是先把页面里的声明读出来按声明尺寸
// 从大到小试，最后才退到 /favicon.ico：约定位置经常是 16×16 的老图
// 或者占位图，而站点真正的高清图标写在 <link rel="apple-touch-icon"> 里。
func fetchSiteIcon(raw string) (string, error) {
	target, err := appTargetURL(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("地址解析不了：%s", raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), iconBudget)
	defer cancel()

	// 页面读不到不算致命：站点可能是纯静态目录，只有 /favicon.ico
	var cands []iconCand
	if page, finalURL, perr := fetchHTML(ctx, target); perr == nil {
		// 相对地址要按重定向之后的地址解析：Nacos 这类站点会从 /
		// 跳到 /next/，图标写的是 ./img/xxx.png
		cands = append(cands, iconLinks(page, finalURL)...)
	}
	origin := u.Scheme + "://" + u.Host
	if !containsHref(cands, origin+"/favicon.ico") {
		cands = append(cands, iconCand{href: origin + "/favicon.ico"})
	}
	if len(cands) > iconCandidates {
		cands = cands[:iconCandidates]
	}

	best, bestDim := "", -1
	var lastErr error
	for _, c := range cands {
		data, dim, err := fetchIconAt(ctx, c.href)
		if err != nil {
			lastErr = err
			continue
		}
		if dim > bestDim {
			best, bestDim = data, dim
		}
		if bestDim >= iconGoodEnough {
			break
		}
	}
	if best == "" {
		if lastErr == nil {
			lastErr = errors.New("页面里没有声明图标")
		}
		return "", lastErr
	}
	return best, nil
}

func containsHref(cands []iconCand, href string) bool {
	for _, c := range cands {
		if c.href == href {
			return true
		}
	}
	return false
}

// fetchIconAt 取一张图，顺带量出它真实的边长。声明尺寸可以瞎写，
// 只有解出来的宽高才算数。
func fetchIconAt(ctx context.Context, raw string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; WeftDesk icon fetch)")
	req.Header.Set("Accept", "image/*")
	resp, err := iconClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, iconMaxBytes+1))
	if err != nil {
		return "", 0, err
	}
	w, h := imageDims(b)
	// 1×1 的追踪像素常被当成图标返回，贴在卡片上就是一个透明洞
	if w > 0 && h > 0 && (w < 16 || h < 16) {
		return "", 0, fmt.Errorf("图标只有 %d×%d", w, h)
	}
	data, err := iconDataURL(b)
	if err != nil {
		return "", 0, err
	}
	return data, maxInt(w, h), nil
}

// fetchHTML 返回解析好的文档和「最终地址」——重定向之后的那个。
func fetchHTML(ctx context.Context, target string) (*html.Node, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; WeftDesk icon fetch)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := iconClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("页面 HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, pageMaxBytes))
	if err != nil {
		return nil, "", err
	}
	doc, perr := html.Parse(bytes.NewReader(b))
	if perr != nil {
		return nil, "", perr
	}
	final := target
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return doc, final, nil
}

// iconLinks 在 <head> 里找图标声明，按声明的 sizes 从大到小排。
// 只看 rel 里有 icon 的 link；apple-touch-icon 也认——苹果那套图标
// 通常是无背景的高清方图，比 16×16 的清楚。
//
// mask-icon 排除：那是 Safari 的地址栏蒙版图标，单色剪影，
// 当应用图标用会变成一块纯色。
func iconLinks(doc *html.Node, base string) []iconCand {
	baseURL, _ := url.Parse(base)
	var cands []iconCand

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "link" {
			rel, href, sizes := "", "", ""
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "rel":
					rel = strings.ToLower(strings.TrimSpace(a.Val))
				case "href":
					href = strings.TrimSpace(a.Val)
				case "sizes":
					sizes = a.Val
				}
			}
			if href != "" && strings.Contains(rel, "icon") && !strings.Contains(rel, "mask-icon") {
				abs := resolveRef(baseURL, href)
				if isHTTPURL(abs) && !containsHref(cands, abs) {
					cands = append(cands, iconCand{href: abs, size: parseSizes(sizes)})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	// 稳定排序：声明尺寸大的先试，同样大的保持页面里的顺序
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].size > cands[j].size })
	return cands
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
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

// imageDims 量图片的真实边长。ICO 没有标准解码器，自己读目录；
// SVG 当矢量图，给一个大到不会被比下去的值；认不出的格式返回 0，
// 交给上层当「尺寸未知」处理。
func imageDims(b []byte) (int, int) {
	if w, h := icoDims(b); w > 0 {
		return w, h
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); err == nil {
		return cfg.Width, cfg.Height
	}
	if isSVG(b) {
		return 512, 512
	}
	return 0, 0
}

// icoDims 读 ICO 目录里最大的那一档。宽高字节为 0 表示 256。
func icoDims(b []byte) (int, int) {
	if len(b) < 6 || b[0] != 0 || b[1] != 0 || b[2] != 1 || b[3] != 0 {
		return 0, 0
	}
	n := int(binary.LittleEndian.Uint16(b[4:6]))
	if n <= 0 || len(b) < 6+16*n {
		return 0, 0
	}
	w, h := 0, 0
	for i := 0; i < n; i++ {
		ew := int(b[6+i*16])
		eh := int(b[7+i*16])
		if ew == 0 {
			ew = 256
		}
		if eh == 0 {
			eh = 256
		}
		if ew*eh > w*h {
			w, h = ew, eh
		}
	}
	return w, h
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
