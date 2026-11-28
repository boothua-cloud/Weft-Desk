package desktop

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/boothua/weft/internal/engine"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/store"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Options 是根包交给桌面层的启动参数。
//
// 前端产物和图标由根包 embed：//go:embed 只能引用本包目录下的文件，
// 而 frontend/dist 与 build/ 都在仓库根。
type Options struct {
	Assets fs.FS
	Icon   []byte
	// TrayIcon 为空时托盘没有图标，只用文字菜单。
	TrayIcon []byte
	// MenuBarTitle 返回 macOS 菜单栏当前显示的应用名，只用于启动日志。
	// 换名要靠 cgo，那部分在根包。
	MenuBarTitle func() string
}

// Run 建窗口、挂托盘、启动事件循环，直到应用退出才返回。
func Run(opt Options) error {
	Icon = opt.Icon
	TrayIcon = opt.TrayIcon

	st, err := store.Open()
	if err != nil {
		return fmt.Errorf("打开本地数据库失败: %w", err)
	}
	defer st.Close()

	svc := New(st)
	defer svc.shutdown()

	app := application.New(application.Options{
		Name:        "云栈",
		Description: "云栈 · Headscale 桌面客户端",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(opt.Assets),
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
	if len(Icon) > 0 {
		app.SetIcon(Icon)
		go func() {
			time.Sleep(900 * time.Millisecond)
			app.SetIcon(Icon)
			log.Printf("[ui] 应用图标已设置（%d KB）", len(Icon)/1024)
			if opt.MenuBarTitle != nil {
				if t := opt.MenuBarTitle(); t != "" {
					log.Printf("[ui] 菜单栏应用名：%s", t)
				}
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
	svc.initUpdater()

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

	// 帮助里写着 ⌘R 刷新数据，之前只在菜单上标了字，按下去没反应。
	// 快捷键在后端注册，前端收到事件后统一刷新状态和当前页。
	refreshKey := "ctrl+r"
	if runtime.GOOS == "darwin" {
		refreshKey = "cmd+r"
	}
	app.KeyBinding.Add(refreshKey, func(application.Window) {
		app.Event.Emit("weft:refresh")
	})
	log.Printf("[ui] 刷新快捷键已注册（%s）", refreshKey)

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
	if w != nil {
		// 记住主窗口：高危绑定方法只认它，应用内窗口加载的第三方页面一律拒绝。
		svc.mainWindowID = w.ID()
	}
	// 截图/自动化用：WEFT_START_PAGE=forwards 启动后自动切到该页
	if p := strings.TrimSpace(os.Getenv("WEFT_START_PAGE")); p != "" {
		go func() {
			time.Sleep(2500 * time.Millisecond)
			app.Event.Emit("weft:navigate", p)
		}()
	}
	// 截图/自动化用：WEFT_DEBUG_CLICK="#btn-add-profile" 启动后点一下这个元素。
	// 用 | 分隔可以连点多次（比如先点「下载并安装」、等下载完再点「立即重启」），
	// 每次之间留 5 秒，够一轮网络请求或状态切换。
	if sel := strings.TrimSpace(os.Getenv("WEFT_DEBUG_CLICK")); sel != "" {
		go func() {
			time.Sleep(5000 * time.Millisecond)
			for _, one := range strings.Split(sel, "|") {
				one = strings.TrimSpace(one)
				if one == "" {
					continue
				}
				w.ExecJS(fmt.Sprintf("(()=>{const e=document.querySelector(%q); if(e) e.click();})()", one))
				time.Sleep(5 * time.Second)
			}
		}()
	}

	ui := newAppUI(app, svc, w)
	svc.mu.Lock()
	svc.prefsHook = func(cfg model.Config) { ui.applyTray(cfg.MinimizeToTray) }
	svc.revealHook = ui.reveal
	svc.devToolsHook = func() {
		if w != nil {
			w.OpenDevTools()
		}
	}
	svc.mu.Unlock()
	ui.bind()

	// 连接时权限不足会以管理员身份重启自己并带上 --elevate-connect，
	// 重启后把连接接上，用户不用再点一次「连接」。
	if engine.WantsAutoConnect() {
		go func() {
			time.Sleep(1200 * time.Millisecond)
			log.Printf("[tunnel] 已以管理员身份重启，自动连接")
			if _, err := svc.Connect(); err != nil {
				log.Printf("[tunnel] 授权后自动连接失败: %v", err)
			}
		}()
	}

	// 启动后静默检查一次更新：只在确实有新版本时提示，不拖慢启动。
	// 12 秒是等启动动画走完、首次状态刷新落定，避免和连接流程抢带宽。
	go func() {
		time.Sleep(12 * time.Second)
		svc.checkUpdateQuiet()
	}()

	if err := app.Run(); err != nil {
		return err
	}
	log.Printf("[ui] 事件循环已结束，进程准备退出")
	return nil
}
