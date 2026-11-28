package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/boothua/weft/internal/model"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// 应用书签：内网应用的增删、在内窗或系统浏览器里打开，以及审计日志。

func (s *WeftService) ListApps() (string, error) {
	return wrap(s.store.ListApps())
}

func (s *WeftService) SaveApp(raw string) error {
	var a model.AppBookmark
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return err
	}
	if a.ID == "" {
		a.ID = fmt.Sprintf("app-%d", time.Now().UnixNano())
	}
	return s.store.SaveApp(a)
}

func (s *WeftService) DeleteApp(id string) error { return s.store.DeleteApp(id) }

// OpenApp 用 Wails v3 多窗口新开独立 WebView，不走 iframe（会被 X-Frame-Options 拦截）。
// AppTargetURL 校验书签地址并补上协议，只放行 http/https。
// 书签会从对端同步过来（apps_sync.go），不是纯本地输入；
// `file://` 会被 WebView 当本地文件打开，`-a Calculator` 这种值
// 传给 `open` / `rundll32` 会被当成参数而不是网址。
func AppTargetURL(raw string) (string, error) {
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
		raw, err := AppTargetURL(a.URL)
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
		s.store.AddAudit("打开应用", a.Name)
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
		raw, err := AppTargetURL(a.URL)
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
		return cmd.Start()
	}
	return fmt.Errorf("未找到应用: %s", id)
}

func (s *WeftService) ListAudit() (string, error) {
	return wrap(s.store.ListAudit())
}
