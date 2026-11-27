//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/windows"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// Windows 没有 unix socket 传句柄这条路，WinTun 建卡与写系统路由都要求
// 进程本身提权，所以这里的「系统助手」是「登录时以最高权限启动」的计划任务：
// 装一次，之后每次登录都由任务以管理员身份拉起，不再弹 UAC。
const (
	winTaskName = "Weft"

	// 提权子进程的专用动作：做完即退，不打开界面。
	flagInstallHelper   = "--install-helper"
	flagUninstallHelper = "--uninstall-helper"
	// 连接时权限不足会以管理员身份重启自己，带上这个标记表示重启后自动连接。
	flagElevateConnect = "--elevate-connect"

	createNoWindow = 0x08000000
)

// elevationPending 记住「刚才已经请求过 UAC」，让界面能显示等待状态。
var elevationPending atomic.Bool

func helperLogTail() string { return "" }

func helperStatus() HelperStatus {
	st := HelperStatus{}
	st.Installed = winTaskExists()
	st.Running = isElevated()
	if st.Installed {
		elevationPending.Store(false)
	}

	target := helperTarget()
	switch {
	case elevationPending.Load():
		st.Note = "已请求管理员授权：请在系统弹窗里点「是」，安装完成后这里会自动变成「运行中」"
	case st.Installed && target != "" && !samePath(target, selfExe()):
		st.Note = "登录自启指向旧位置（" + target + "），请点「重新安装」"
	case st.Installed && !st.Running:
		st.Note = "已配置登录自启（管理员权限）；当前进程不是管理员，连接前会请求一次授权"
	case st.Installed && st.Running:
		st.Note = "已以管理员运行，连接无需再授权"
	case st.Running:
		st.Note = "当前已以管理员运行；装好登录自启后开机自动接管，不用每次确认 UAC"
	default:
		// exe 清单是 requireAdministrator，正常启动到这里 Running 都是 true，
		// 只剩「被别的方式以普通权限拉起」这种异常情况。
		st.Note = "当前不是管理员：每次打开都会请求一次 UAC，建议安装登录自启"
	}
	return st
}

// winTaskExists 只取退出码，不解析 schtasks 的输出——
// 中文/英文系统的字段名不同，解析必然出错。
func winTaskExists() bool {
	_, err := hiddenCommand("schtasks", "/Query", "/TN", winTaskName).Output()
	return err == nil
}

// installHelper 未提权时先把 UAC 拉起来：由提权后的自己真正安装任务。
// 这样「点一下」就能完成首次授权，不需要用户自己去右键「以管理员身份运行」。
func installHelper() error {
	exe := selfExe()
	if exe == "" {
		return fmt.Errorf("定位自身可执行文件失败")
	}
	if !isElevated() {
		log.Printf("[helper] 未提权，请求管理员授权安装登录自启任务")
		if err := relaunchElevated(flagInstallHelper); err != nil {
			return err
		}
		elevationPending.Store(true)
		return nil
	}

	// /F 覆盖同名任务：程序换了目录之后旧任务还指向老路径，
	// 不覆盖就会「登录时启动了一个看不见的旧版本」。
	out, err := hiddenCommand("schtasks", "/Create", "/F",
		"/TN", winTaskName, "/SC", "ONLOGON", "/RL", "HIGHEST",
		"/TR", `"`+exe+`"`).CombinedOutput()
	if err != nil {
		return fmt.Errorf("创建登录自启任务失败：%s", strings.TrimSpace(decodeConsole(out)))
	}
	if err := os.WriteFile(helperTargetFile(), []byte(exe), 0o600); err != nil {
		log.Printf("[helper] 记录任务指向失败（不影响运行）: %v", err)
	}
	log.Printf("[helper] 已创建登录自启任务 %s → %s", winTaskName, exe)
	return nil
}

func uninstallHelper() error {
	if !isElevated() {
		log.Printf("[helper] 未提权，请求管理员授权移除登录自启任务")
		if err := relaunchElevated(flagUninstallHelper); err != nil {
			return err
		}
		return nil
	}
	out, err := hiddenCommand("schtasks", "/Delete", "/F", "/TN", winTaskName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("删除登录自启任务失败：%s", strings.TrimSpace(decodeConsole(out)))
	}
	_ = os.Remove(helperTargetFile())
	elevationPending.Store(false)
	log.Printf("[helper] 已移除登录自启任务 %s", winTaskName)
	return nil
}

// runPrivilegedAction 处理「提权后只做一件事」的子进程：做完即退，不建窗口。
func runPrivilegedAction() (bool, int) {
	action := ""
	for _, a := range os.Args[1:] {
		if a == flagInstallHelper || a == flagUninstallHelper {
			action = a
		}
	}
	if action == "" {
		return false, 0
	}
	log.Printf("[helper] 以管理员身份执行 %s", action)
	var err error
	switch action {
	case flagInstallHelper:
		err = installHelper()
	case flagUninstallHelper:
		err = uninstallHelper()
	}
	if err != nil {
		log.Printf("[helper] %s 失败: %v", action, err)
		showMessage("云栈 · 系统助手", err.Error(), windows.MB_ICONERROR)
		return true, 1
	}
	return true, 0
}

// relaunchElevated 以管理员身份重启当前程序，并让新进程只执行 action。
func relaunchElevated(action string) error {
	exe := selfExe()
	if exe == "" {
		return fmt.Errorf("定位自身可执行文件失败")
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	args, err := windows.UTF16PtrFromString(action)
	if err != nil {
		return err
	}
	dir, err := windows.UTF16PtrFromString(filepath.Dir(exe))
	if err != nil {
		return err
	}
	if err := windows.ShellExecute(0, verb, file, args, dir, windows.SW_SHOWNORMAL); err != nil {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return fmt.Errorf("已取消管理员授权")
		}
		return fmt.Errorf("请求管理员授权失败: %w", err)
	}
	return nil
}

// relaunchElevatedConnect 连接时权限不足的出口：以管理员身份重启，重启后自动连接。
func relaunchElevatedConnect() error { return relaunchElevated(flagElevateConnect) }

// isReplaceLaunch 表示这次启动是用来替换上一个进程的（提权重启）：
// 单实例锁要的是「等旧进程松开再接管」，而不是直接判定已有实例。
func isReplaceLaunch() bool {
	for _, a := range os.Args[1:] {
		if a == flagElevateConnect {
			return true
		}
	}
	return false
}

// wantsAutoConnect 授权重启后立刻把连接接上，用户不用再点一次。
func wantsAutoConnect() bool { return isReplaceLaunch() }

// notifyAlreadyRunning 第二个实例要给出可见反馈，否则用户只会看到「双击没反应」。
func notifyAlreadyRunning() {
	showMessage("云栈", "云栈已经在运行了。\n\n请从任务栏右下角的托盘图标打开；托盘被关掉时，请先退出正在运行的实例。", windows.MB_ICONINFORMATION)
}

func showMessage(title, text string, icon uint32) {
	t, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	c, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	_, _ = windows.MessageBox(0, t, c, icon|windows.MB_TOPMOST|windows.MB_SETFOREGROUND)
}

// selfExe 返回当前可执行文件的绝对路径。
func selfExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func helperTargetFile() string {
	dir := dataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "helper-target.txt")
}

// helperTarget 读回安装时记下的任务指向，用来发现「任务指向旧路径」。
func helperTarget() string {
	p := helperTargetFile()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// hiddenCommand 起一个不闪黑框的控制台子进程。
// 界面每 5 秒轮询一次助手状态，不隐藏的话 schtasks 会不停在屏幕上闪窗。
func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

// decodeConsole 控制台程序在中文系统上按本地代码页（GBK）输出，
// 直接 string() 会是乱码，报错信息就看不懂了。
func decodeConsole(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b)
	}
	if dec, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
		return string(dec)
	}
	return string(b)
}
