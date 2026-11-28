package desktop

import (
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/boothua/weft/internal/model"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// appUI 管窗口生命周期与托盘图标。
// 关窗口只是把窗口藏起来，应用继续在后台跑；真正退出必须显式触发
// （托盘菜单「退出 Weft」、⌘Q、Dock 右键退出）。
type appUI struct {
	app *application.App
	svc *WeftService
	win *application.WebviewWindow

	mu           sync.Mutex
	tray         *application.SystemTray
	statusItem   *application.MenuItem
	connectItem  *application.MenuItem
	statusText   string
	connectLabel string
	quitting     bool
	revealed     bool
	stopOnce     sync.Once
	stop         chan struct{}
}

// bootAt 记录进程内初始化起点，用来度量「点开图标 → 窗口出现」花了多久。
var bootAt = time.Now()

func newAppUI(app *application.App, svc *WeftService, win *application.WebviewWindow) *appUI {
	return &appUI{app: app, svc: svc, win: win, stop: make(chan struct{})}
}

// bind 挂上关闭拦截，并按当前配置决定要不要显示托盘。
func (u *appUI) bind() {
	if u.win != nil {
		u.win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
			if u.isQuitting() {
				return // 显式退出，放行真实关闭
			}
			if !u.canHideOnClose() {
				// 这个平台藏了窗口就找不回来（没开托盘），关窗即退出
				go u.quit()
				return
			}
			e.Cancel()
			log.Printf("[ui] 关闭窗口 → 收起到后台（应用继续运行）")
			u.hideWindow()
		})
	}
	u.applyTray(u.svc.prefs().MinimizeToTray)
	go u.watchStatus()
	// 前端万一没能调用 RevealWindow（脚本出错等），也不能让窗口永远不出现。
	time.AfterFunc(2500*time.Millisecond, func() {
		u.mu.Lock()
		done := u.revealed
		u.mu.Unlock()
		if done {
			return
		}
		log.Printf("[ui] 前端未在 2.5s 内就绪，先显示窗口")
		u.reveal()
	})
}

// reveal 把启动时隐藏的主窗口显示出来。幂等，只会真正执行一次。
func (u *appUI) reveal() {
	u.mu.Lock()
	if u.revealed {
		u.mu.Unlock()
		return
	}
	u.revealed = true
	u.mu.Unlock()
	log.Printf("[ui] 显示主窗口（进程启动至今 %d ms）", time.Since(bootAt).Milliseconds())
	u.showWindow()
}

// canHideOnClose：macOS 关窗后还能点 Dock 图标把窗口叫回来；
// Windows/Linux 只有托盘是唯一入口，托盘没开时不能藏窗口。
func (u *appUI) canHideOnClose() bool {
	if runtime.GOOS == "darwin" {
		return true
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.tray != nil
}

func (u *appUI) hideWindow() {
	if u.win == nil {
		return
	}
	u.win.Hide()
}

func (u *appUI) showWindow() {
	if u.win == nil {
		return
	}
	if !u.win.IsVisible() {
		u.win.Show()
	}
	u.win.Focus()
}

// applyTray 让托盘图标的显示状态与设置项保持一致，重复调用是安全的。
func (u *appUI) applyTray(on bool) {
	u.mu.Lock()
	if !on {
		tray := u.tray
		u.tray, u.statusItem, u.statusText = nil, nil, ""
		u.mu.Unlock()
		if tray != nil {
			tray.Destroy()
			log.Printf("[ui] 托盘图标已移除")
		}
		return
	}
	if u.tray != nil {
		u.mu.Unlock()
		return
	}

	tray := u.app.SystemTray.New()
	// 直接用应用图标：和 Dock / 关于窗口保持一致，不再另画一个符号
	tray.SetIcon(TrayIcon)
	tray.SetTooltip("云栈")

	menu := application.NewMenu()
	status := menu.Add("云栈")
	status.SetEnabled(false)
	menu.AddSeparator()
	connect := menu.Add("连接")
	connect.OnClick(func(*application.Context) { u.toggleTunnel() })
	menu.Add("显示主窗口").OnClick(func(*application.Context) { u.showWindow() })
	menu.Add("帮助手册").OnClick(func(*application.Context) {
		u.showWindow()
		u.app.Event.Emit("weft:navigate", "help")
	})
	menu.Add("设置…").OnClick(func(*application.Context) {
		u.showWindow()
		u.app.Event.Emit("weft:navigate", "settings")
	})
	menu.Add("关于云栈").OnClick(func(*application.Context) {
		u.showWindow()
		u.app.Event.Emit("weft:navigate", "about")
	})
	menu.AddSeparator()
	menu.Add("退出云栈").OnClick(func(*application.Context) { u.quit() })
	tray.SetMenu(menu)
	tray.OnClick(func() { u.showWindow() })

	// 已经销毁的旧托盘，字段一起换掉，避免刷新时写到废对象上
	u.tray = tray
	u.statusItem = status
	u.connectItem = connect
	u.statusText, u.connectLabel = "", ""
	u.mu.Unlock()
	u.refreshTrayStatus()
	log.Printf("[ui] 托盘图标已显示")
}

// toggleTunnel 让托盘菜单里的连接项直接连/断开，不用先回到窗口。
func (u *appUI) toggleTunnel() {
	go func() {
		var err error
		if u.svc.tunnelRunning() {
			_, err = u.svc.Disconnect()
		} else {
			_, err = u.svc.Connect()
		}
		if err != nil {
			log.Printf("[ui] 托盘连接操作失败: %v", err)
			u.setTrayNote("操作失败：" + err.Error())
		}
		u.refreshTrayStatus()
	}()
}

// setTrayNote 把失败原因临时挂在状态行上，下次状态刷新会覆盖掉。
func (u *appUI) setTrayNote(msg string) {
	u.mu.Lock()
	item := u.statusItem
	u.statusText = msg
	u.mu.Unlock()
	if item != nil {
		item.SetLabel(msg)
	}
}

func (u *appUI) refreshTrayStatus() {
	label := u.svc.statusLine()
	connLabel := "连接"
	if u.svc.tunnelRunning() {
		connLabel = "断开连接"
	}
	u.mu.Lock()
	item, conn, tray := u.statusItem, u.connectItem, u.tray
	statusChanged := label != u.statusText
	connChanged := connLabel != u.connectLabel
	u.statusText, u.connectLabel = label, connLabel
	u.mu.Unlock()
	if tray == nil {
		return
	}
	if item != nil && statusChanged {
		item.SetLabel(label)
		tray.SetTooltip("云栈 · " + label)
	}
	if conn != nil && connChanged {
		conn.SetLabel(connLabel)
	}
}

// watchStatus 让托盘菜单里的连接状态跟着隧道走。
func (u *appUI) watchStatus() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-ticker.C:
			u.refreshTrayStatus()
		}
	}
}

func (u *appUI) isQuitting() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.quitting
}

func (u *appUI) quit() {
	u.mu.Lock()
	if u.quitting {
		u.mu.Unlock()
		return
	}
	u.quitting = true
	u.mu.Unlock()
	u.stopOnce.Do(func() { close(u.stop) })
	log.Printf("[ui] 退出应用")
	u.app.Quit()
}

// statusLine 是托盘里那行状态文字，尽量短，只放 IP 或服务端主机名。
func (s *WeftService) statusLine() string {
	s.mu.Lock()
	cfg, eng := s.cfg, s.engine
	s.mu.Unlock()
	if eng != nil {
		if running, ip, _ := eng.Status(); running {
			if ip != "" {
				return "已连接 · " + ip
			}
			return "已连接"
		}
	}
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(cfg.BaseURL, "https://"), "http://"), "/")
	if host == "" {
		return "未连接"
	}
	return "未连接 · " + host
}

// tunnelRunning 给托盘菜单判断该显示「连接」还是「断开连接」。
func (s *WeftService) tunnelRunning() bool {
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng == nil {
		return false
	}
	running, _, _ := eng.Status()
	return running
}

func (s *WeftService) prefs() model.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}
