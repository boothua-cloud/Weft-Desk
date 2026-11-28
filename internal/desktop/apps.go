package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/boothua/weft/internal/model"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// 应用书签：内网应用的增删、在内窗或系统浏览器里打开，以及审计日志。

func (s *WeftService) ListApps() (string, error) {
	return wrap(s.store.ListApps())
}

func (s *WeftService) SaveApp(raw string) (string, error) {
	var a model.AppBookmark
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return "", err
	}
	// 编辑时不接受前端回传的图标和打开时间：这两样是本机的缓存与记录，
	// 表单里没有它们，回传空值会把已有的清掉。
	if a.ID != "" {
		if apps, err := s.store.ListApps(); err == nil {
			for _, old := range apps {
				if old.ID == a.ID {
					a.IconData, a.LastOpen = old.IconData, old.LastOpen
					break
				}
			}
		}
	}
	if a.ID == "" {
		a.ID = fmt.Sprintf("app-%d", time.Now().UnixNano())
	}
	if err := s.store.SaveApp(a); err != nil {
		return "", err
	}
	// 读回来：新建的那条 sort 是库里的 MAX+1，前端拿着它才能把新卡片
	// 放进正确的位置，不用整表重拉。
	apps, err := s.store.ListApps()
	if err != nil {
		return toJSON(a)
	}
	for _, x := range apps {
		if x.ID == a.ID {
			return toJSON(x)
		}
	}
	return toJSON(a)
}

func (s *WeftService) DeleteApp(id string) error { return s.store.DeleteApp(id) }

// ReorderApps 拖动排序后按新顺序落库。传的是 id 的 JSON 数组。
func (s *WeftService) ReorderApps(raw string) error {
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return fmt.Errorf("排序参数不对：%w", err)
	}
	if len(ids) == 0 {
		return errors.New("排序参数是空的")
	}
	return s.store.ReorderApps(ids)
}

// FetchAppIcon 重新抓一个应用的图标并写库，不管原来有没有。
// 抓不到不算致命：记下原因，前端留着色块，用户想再试就再点一次。
func (s *WeftService) FetchAppIcon(id string) (string, error) {
	apps, err := s.store.ListApps()
	if err != nil {
		return "", err
	}
	for _, a := range apps {
		if a.ID != id {
			continue
		}
		data, ferr := fetchSiteIcon(a.URL)
		if ferr != nil {
			_ = s.store.SetAppIconError(id, iconErrText(ferr))
			return "", ferr
		}
		if err := s.store.SetAppIcon(id, data); err != nil {
			return "", err
		}
		a.IconData, a.IconError = data, ""
		return toJSON(a)
	}
	return "", fmt.Errorf("未找到应用: %s", id)
}

// iconErrText 把网络层的英文报错换成人话。前端会把它直接贴在
// 失败清单里，`Get "https://…": context deadline exceeded` 这种
// 对用户没有意义。
func iconErrText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "context deadline exceeded"), strings.Contains(s, "Client.Timeout"):
		return "连接超时，站点没在 5 秒内回应"
	case strings.Contains(s, "connection refused"):
		return "端口没有服务在听"
	case strings.Contains(s, "no such host"):
		return "域名解析不了"
	case strings.Contains(s, "certificate"):
		return "证书校验失败"
	}
	return s
}

// IconFailure 是某一个应用没抓到的原因。前端按条显示，
// 不用再从一句拼好的中文里猜是哪台。
type IconFailure struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// IconFillResult 是批量抓取的汇总。
type IconFillResult struct {
	Scope  string        `json:"scope"`
	Total  int           `json:"total"`
	Done   int           `json:"done"`
	Failed []IconFailure `json:"failed"`
}

// FillIcons 批量抓图标。scope 为 all 时连已经有图标的也重抓，
// 其它值只补空的——「抓歪了重来一遍」和「把缺的补上」是两件事。
// 并发 4：串行的话二十个书签每个等到超时就是一百秒，用户会以为卡死。
func (s *WeftService) FillIcons(scope string) (string, error) {
	apps, err := s.store.ListApps()
	if err != nil {
		return "", err
	}
	var todo []model.AppBookmark
	for _, a := range apps {
		if scope == "all" || a.IconData == "" {
			todo = append(todo, a)
		}
	}
	res := IconFillResult{Scope: scope, Total: len(todo), Failed: []IconFailure{}}
	if len(todo) == 0 {
		return toJSON(res)
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for _, a := range todo {
		wg.Add(1)
		go func(a model.AppBookmark) {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			data, ferr := fetchSiteIcon(a.URL)
			mu.Lock()
			defer mu.Unlock()
			if ferr != nil {
				_ = s.store.SetAppIconError(a.ID, iconErrText(ferr))
				res.Failed = append(res.Failed, IconFailure{ID: a.ID, Name: a.Name, Reason: iconErrText(ferr)})
				return
			}
			if err := s.store.SetAppIcon(a.ID, data); err != nil {
				res.Failed = append(res.Failed, IconFailure{ID: a.ID, Name: a.Name, Reason: "写入失败 " + err.Error()})
				return
			}
			res.Done++
		}(a)
	}
	wg.Wait()
	what := "补齐应用图标"
	if scope == "all" {
		what = "重抓应用图标"
	}
	s.audit(what, fmt.Sprintf("%d 个，成功 %d", res.Total, res.Done), nil)
	return toJSON(res)
}

// ClearAppIcon 把图标退回色块 + 名字首字。
func (s *WeftService) ClearAppIcon(id string) error {
	return s.store.ClearAppIcon(id)
}

// OpenApp 用 Wails v3 多窗口新开独立 WebView，不走 iframe（会被 X-Frame-Options 拦截）。
// appTargetURL 校验书签地址并补上协议，只放行 http/https。
// 书签会从对端同步过来（apps_sync.go），不是纯本地输入；
// `file://` 会被 WebView 当本地文件打开，`-a Calculator` 这种值
// 传给 `open` / `rundll32` 会被当成参数而不是网址。
func appTargetURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("书签地址是空的")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("书签地址无效：%s", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("书签只支持 http / https：%s", raw)
	}
	return s, nil
}

func (s *WeftService) OpenApp(id string) error {
	apps, err := s.store.ListApps()
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.ID != id {
			continue
		}
		if s.app == nil {
			return fmt.Errorf("应用窗口尚未就绪")
		}
		// 标记「用系统浏览器打开」的书签，直接走浏览器：
		// 应用内是 WebView，加载不了浏览器扩展（如密码管理器自动填充）。
		if a.Browser {
			return s.OpenInBrowser(id)
		}
		raw, err := appTargetURL(a.URL)
		if err != nil {
			return err
		}
		target, err := s.engine.ResolveURL(raw)
		if err != nil {
			return err
		}
		// 先渲染一个加载页再导航：目标站点在内网，首字节可能要等一两秒，
		// 直接给 URL 会先白屏一段。直接给 http:// 也有概率被当成资源路径处理。
		w := s.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:            a.Name + " · 云栈",
			Width:            1100,
			Height:           720,
			HTML:             appLoadingHTML(a.Name),
			BackgroundColour: application.NewRGB(244, 246, 249),
		})
		if w == nil || w.ID() == 0 {
			return fmt.Errorf("创建应用窗口失败")
		}
		// 页面里塞一个浮动菜单：刷新 / 后退 / 复制地址 / 调试器 / 关窗
		s.attachOverlay(w)
		w.Center()
		w.Show()
		w.Focus()
		go func(win *application.WebviewWindow, u string) {
			// 让加载动画先完整露脸，再开始真正的导航
			time.Sleep(700 * time.Millisecond)
			win.SetURL(u)
		}(w, target)
		s.audit("打开应用", a.Name, nil)
		// 记一次打开时间，总览页的「快捷应用」按它排
		_ = s.store.MarkAppOpened(id)
		return nil
	}
	return fmt.Errorf("未找到应用: %s", id)
}

func (s *WeftService) OpenInBrowser(id string) error {
	apps, err := s.store.ListApps()
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.ID != id {
			continue
		}
		// 系统浏览器同样走本地代理，避免依赖浏览器自身的路由。
		raw, err := appTargetURL(a.URL)
		if err != nil {
			return err
		}
		openURL, err := s.engine.ResolveURL(raw)
		if err != nil {
			return err
		}
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", openURL)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", openURL)
		default:
			cmd = exec.Command("xdg-open", openURL)
		}
		_ = s.store.MarkAppOpened(id)
		return cmd.Start()
	}
	return fmt.Errorf("未找到应用: %s", id)
}

func (s *WeftService) ListAudit() (string, error) {
	return wrap(s.store.ListAudit(2000))
}
