//go:build windows

package engine

import (
	"errors"
	"log"
	"time"

	"golang.org/x/sys/windows"
)

// singleInstanceName 用 Local\ 前缀：命名对象按登录会话隔离，
// 普通权限实例与管理员实例在同一会话里能看到同一个名字。
const singleInstanceName = `Local\WeftDesk.SingleInstance`

var singleMutex windows.Handle

// AcquireSingleInstance 用会话内命名互斥保证只跑一个实例。
// 登录自启任务和手动双击会撞在一起，两个进程抢同一张 WinTun 网卡、
// 同一个 SQLite 文件和同一个 tsnet 状态目录。
func AcquireSingleInstance() bool {
	name, err := windows.UTF16PtrFromString(singleInstanceName)
	if err != nil {
		return true
	}
	h, err := windows.CreateMutex(nil, false, name)
	// 注意：x/sys 的 CreateMutex 在「已存在」时仍然返回有效句柄，
	// 但会把 ERROR_ALREADY_EXISTS 一起带回来。所以既不能用 err != nil 判失败，
	// 也不能只看 GetLastError —— 两者都判，且以句柄是否为 0 作为真失败。
	alreadyExists := errors.Is(err, windows.ERROR_ALREADY_EXISTS) ||
		windows.GetLastError() == windows.ERROR_ALREADY_EXISTS
	if h == 0 {
		log.Printf("[boot] 单实例互斥创建失败（忽略，继续启动）: %v", err)
		return true
	}
	if alreadyExists {
		_ = windows.CloseHandle(h)
		return false
	}
	// 句柄随进程退出由系统回收，正常退出时不主动关闭。
	singleMutex = h
	return true
}

// AcquireSingleInstanceWait 用于「提权重启」出来的新进程：
// 旧进程马上就会退出并交锁，这里短暂重试接管，而不是直接判定「已有实例」。
func AcquireSingleInstanceWait(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if AcquireSingleInstance() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// ReleaseSingleInstance 在拉起提权进程之前把锁交出去，
// 否则新进程会因为「旧进程还持着锁」而直接退出，用户看到的是窗口一闪就没。
func ReleaseSingleInstance() {
	if singleMutex != 0 {
		_ = windows.CloseHandle(singleMutex)
		singleMutex = 0
	}
}
