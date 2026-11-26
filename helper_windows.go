//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Windows 没有 unix socket 传句柄这条路，因此不做常驻守护进程：
// 改为「以管理员权限开机自启」，App 自己提权后直接创建 WinTun 网卡。
const winTaskName = "Weft"

func helperLogTail() string { return "" }

func helperStatus() HelperStatus {
	st := HelperStatus{}
	st.Installed = winTaskExists()
	st.Running = isElevated()
	switch {
	case !st.Installed:
		st.Note = "未配置开机自启：每次启动会请求一次管理员授权"
	case !st.Running:
		st.Note = "已配置开机自启；当前进程不是管理员，连接前会请求授权"
	default:
		st.Note = "已以管理员运行，连接无需再授权"
	}
	return st
}

func winTaskExists() bool {
	out, err := exec.Command("schtasks", "/Query", "/TN", winTaskName).Output()
	return err == nil && len(out) > 0
}

// installHelper 创建登录时以最高权限启动的任务，等价于 macOS 的免密体验。
func installHelper() error {
	if !isElevated() {
		return fmt.Errorf("这一步需要管理员权限：请先以管理员身份运行 Weft，再点安装")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	// /RL HIGHEST 让任务以管理员权限运行，避免每次登录都弹 UAC。
	out, err := exec.Command("schtasks", "/Create", "/F",
		"/TN", winTaskName, "/SC", "ONLOGON", "/RL", "HIGHEST",
		"/TR", `"`+exe+`"`).CombinedOutput()
	if err != nil {
		return fmt.Errorf("创建开机任务失败：%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallHelper() error {
	out, err := exec.Command("schtasks", "/Delete", "/F", "/TN", winTaskName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("删除任务失败：%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// maybeElevateOnStart 在未提权且未配置自启任务时，主动以管理员身份重启自己。
// 返回 true 表示已经拉起新实例，调用方应退出当前进程。
func maybeElevateOnStart() bool {
	if isElevated() || winTaskExists() {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if err := shellExecuteRunas(exe); err != nil {
		return false // 用户取消 UAC：继续以普通权限启动，连接时会给出提示
	}
	return true
}
