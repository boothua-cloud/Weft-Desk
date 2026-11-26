package main

import (
	"embed"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func main() {
	setupLogFile()

	// 菜单栏左上角的应用名。必须在 NSApplication 创建之前设置：
	// 系统在启动时就把进程名写进了菜单标题，之后再改不会生效。
	setPlatformAppName("云栈")

	// 关掉 tsnet 的运维日志上报：国内连 log.tailscale.com 会卡 30 秒超时，
	// 拖慢启动且对自用没有价值。
	_ = os.Setenv("TS_NO_LOGS_NO_SUPPORT", "1")

	// Windows 未提权且没有自启任务时，主动以管理员身份重启自己：
	// WinTun 建卡必须管理员，否则第一次连接就会失败。
	if maybeElevateOnStart() {
		return
	}

	// 以提权助手身份被拉起时，接管进程去建 TUN / 配路由，不进 GUI。
	if maybeRunHelper() {
		return
	}

	// --diag：不进 GUI，直接建隧道并输出分层诊断。
	if wantsDiag() {
		runDiag()
		return
	}

	store, err := OpenStore()
	if err != nil {
		log.Fatal("打开本地数据库失败: ", err)
	}
	defer store.Close()

	svc := NewWeftService(store)
	defer svc.shutdown()

	app := application.New(application.Options{
		Name:        "云栈",
		Description: "云栈 · Headscale 桌面客户端",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			// 关窗口不退出应用：窗口被收起后进程继续在后台跑，
			// 退出只走托盘菜单或 ⌘Q。
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})
	// 菜单栏左上角的应用名。macOS 上没有自定义菜单时会退回可执行文件名（weft），
	// 这里显式装一套标准菜单，AppMenu 的标题取的就是 options.Name。
	// 注意：WindowMenu 在 macOS 上不含「关闭」项，⌘W 必须自己加，否则关窗口只能点按钮。
	var mainWindow *application.WebviewWindow
	if runtime.GOOS == "darwin" {
		menu := application.NewMenu()

		// 应用菜单自己拼：Wails 的 AppMenu role 标签固定是英文
		//（About / Hide / Quit），和界面里的中文对不上。
		appMenu := menu.AddSubmenu("云栈")
		appMenu.Add("关于云栈").OnClick(func(*application.Context) {
			if mainWindow != nil {
				mainWindow.Show()
				mainWindow.Focus()
			}
			app.Event.Emit("weft:navigate", "about")
		})
		appMenu.AddSeparator()
		appMenu.Add("隐藏云栈").SetAccelerator("cmd+h").OnClick(func(*application.Context) {
			app.Hide()
		})
		appMenu.AddSeparator()
		appMenu.Add("退出云栈").SetAccelerator("cmd+q").OnClick(func(*application.Context) {
			app.Quit()
		})

		fileMenu := menu.AddSubmenu("文件")
		fileMenu.Add("关闭窗口").SetAccelerator("cmd+w").OnClick(func(*application.Context) {
			// 关当前聚焦的窗口：主窗口会走 WindowClosing 钩子变成「收起」，
			// 应用管理打开的子窗口则直接销毁。
			w := app.Window.Current()
			if w == nil {
				w = mainWindow
			}
			if w != nil {
				w.Close()
			}
		})

		menu.AddRole(application.EditMenu)
		menu.AddRole(application.ViewMenu)
		menu.AddRole(application.WindowMenu)
		app.Menu.Set(menu)
	}

	// 直接跑二进制时 Dock/任务栏没有 bundle 图标，这里显式设一个。
	// 系统在启动瞬间会用默认图标覆盖一次，所以启动完成后再补一次。
	if len(appIcon) > 0 {
		app.SetIcon(appIcon)
		go func() {
			time.Sleep(900 * time.Millisecond)
			app.SetIcon(appIcon)
			log.Printf("[ui] 应用图标已设置（%d KB）", len(appIcon)/1024)
			if t := menuBarTitle(); t != "" {
				log.Printf("[ui] 菜单栏应用名：%s", t)
			}
		}()
	}

	// 启动时把服务端情况打出来，多套配置时一眼能看出当前用的是哪个
	{
		cfg := svc.prefs()
		active := ""
		for _, p := range cfg.Profiles {
			if p.ID == cfg.ActiveID {
				active = p.Name
			}
		}
		log.Printf("[ui] 服务端 %d 个，当前：%s（%s），节点名：%s", len(cfg.Profiles), active, cfg.BaseURL, cfg.Hostname)
	}

	svc.attach(app)

	// 应用窗口调试：F12 / Cmd+Alt+I 打开 WebView 检查器，无条件下发。
	// 必须用 `-tags devtools` 构建，否则 Wails 把 OpenDevTools 编译成空实现。
	for _, acc := range []string{"f12", "cmd+alt+i", "ctrl+shift+i"} {
		app.KeyBinding.Add(acc, func(w application.Window) {
			if w != nil {
				w.OpenDevTools()
			}
		})
	}
	log.Printf("[ui] 调试快捷键已注册（F12 / Cmd+Alt+I）")

	w := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "云栈",
		Width:            1200,
		Height:           760,
		BackgroundColour: application.NewRGB(244, 246, 248),
		URL:              "/",
		// 先不显示：窗口一出现就可能是一块没有任何内容的底色。
		// 等前端把启动画面画好了再 Show，用户看到的第一帧就是加载动画。
		Hidden: true,
	})
	if w == nil {
		log.Printf("[ui] 主窗口创建失败：NewWithOptions 返回 nil")
	} else {
		log.Printf("[ui] 主窗口已创建 id=%d（先隐藏，等前端就绪）", w.ID())
	}

	// 关窗口只隐藏到托盘，退出必须显式触发；托盘显示与否跟随设置项。
	mainWindow = w
	// 截图/自动化用：WEFT_START_PAGE=forwards 启动后自动切到该页
	if p := strings.TrimSpace(os.Getenv("WEFT_START_PAGE")); p != "" {
		go func() {
			time.Sleep(2500 * time.Millisecond)
			app.Event.Emit("weft:navigate", p)
		}()
	}
	// 截图/自动化用：WEFT_DEBUG_CLICK="#btn-add-profile" 启动后点一下这个元素
	if sel := strings.TrimSpace(os.Getenv("WEFT_DEBUG_CLICK")); sel != "" {
		go func() {
			time.Sleep(5000 * time.Millisecond)
			w.ExecJS(fmt.Sprintf("(()=>{const e=document.querySelector(%q); if(e) e.click();})()", sel))
		}()
	}
	ui := newAppUI(app, svc, w)
	svc.mu.Lock()
	svc.prefsHook = func(cfg Config) { ui.applyTray(cfg.MinimizeToTray) }
	svc.revealHook = ui.reveal
	svc.devToolsHook = func() {
		if w != nil {
			w.OpenDevTools()
		}
	}
	svc.mu.Unlock()
	ui.bind()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
	log.Printf("[ui] 事件循环已结束，进程准备退出")
}

// setupLogFile 把日志同时写到数据目录下的 weft.log。
// GUI 程序在 Windows 上没有控制台，出问题时用户能把文件发过来。
func setupLogFile() {
	dir := dataDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, "weft.log")
	// 超过 2MB 就滚动一次，避免无限增长
	if st, err := os.Stat(path); err == nil && st.Size() > 2<<20 {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.SetFlags(log.LstdFlags)
}
