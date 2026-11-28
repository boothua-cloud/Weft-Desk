package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/boothua/weft/internal/appinfo"
	"github.com/boothua/weft/internal/desktop"
	"github.com/boothua/weft/internal/engine"
)

func main() {
	setupLogFile()

	// 菜单栏左上角的应用名。必须在 NSApplication 创建之前设置：
	// 系统在启动时就把进程名写进了菜单标题，之后再改不会生效。
	setPlatformAppName("云栈")

	// 关掉 tsnet 的运维日志上报：国内连 log.tailscale.com 会卡 30 秒超时，
	// 拖慢启动且对自用没有价值。
	_ = os.Setenv("TS_NO_LOGS_NO_SUPPORT", "1")

	// 提权子进程只做一件事（装/卸登录自启任务），做完即退，不建界面。
	// 这样「装系统助手」点一下就能完成，不需要用户自己去右键以管理员运行。
	if handled, code := engine.RunPrivilegedAction(); handled {
		os.Exit(code)
	}

	log.Printf("[boot] 平台=%s/%s 管理员=%v 数据目录=%s",
		runtime.GOOS, runtime.GOARCH, engine.IsElevated(), appinfo.DataDirDisplay())

	// 单实例：登录自启任务与手动双击会撞在一起，两个进程抢同一张 WinTun 网卡、
	// 同一个 SQLite 文件、同一个 tsnet 状态目录。
	// 提权重启（--elevate-connect）是替换上一个进程：旧进程正在退出并已交锁，
	// 这里短暂等待接管即可，不能直接判定「已有实例」。
	if engine.IsReplaceLaunch() {
		if !engine.AcquireSingleInstanceWait(6 * time.Second) {
			log.Printf("[boot] 提权重启后仍没能接管单实例锁，退出")
			return
		}
	} else if !engine.AcquireSingleInstance() {
		log.Printf("[boot] 已有实例在运行，本次启动直接退出")
		engine.NotifyAlreadyRunning()
		return
	}

	// 以提权助手身份被拉起时，接管进程去建 TUN / 配路由，不进 GUI。
	if engine.MaybeRunHelper() {
		return
	}

	// --diag：不进 GUI，直接建隧道并输出分层诊断。
	if desktop.WantsDiag() {
		desktop.RunDiag()
		return
	}

	// 窗口、托盘、事件循环都在桌面层；根包只负责把 embed 的资源递过去。
	if err := desktop.Run(desktop.Options{
		Assets:       assets,
		Icon:         appIcon,
		TrayIcon:     trayIcon,
		MenuBarTitle: menuBarTitle,
	}); err != nil {
		log.Fatal(err)
	}
}

// setupLogFile 把日志同时写到数据目录下的 weft.log。
// GUI 程序在 Windows 上没有控制台，出问题时用户能把文件发过来。
func setupLogFile() {
	dir := appinfo.DataDir()
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
	// 顺序很关键：文件必须排在第一个。
	// windowsgui 构建（双击启动）没有控制台，os.Stderr 是无效句柄，写它会返回错误；
	// io.MultiWriter 碰到第一个写失败就直接返回，后面的 writer 不会再写——
	// 之前把 stderr 放前面，结果「双击启动时日志一个字都没有」。
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	log.SetFlags(log.LstdFlags)
}
