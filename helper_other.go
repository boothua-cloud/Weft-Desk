//go:build !windows

package main

import (
	"os"
	"time"
)

// 非 Windows 平台不需要「启动即提权」：macOS 走 LaunchDaemon，Linux 走 systemd，
// 提权在 unixRouter 内部完成（常驻助手或一次性授权）。
// 这里只放几个让 main 保持平台无关的空实现。

func relaunchElevatedConnect() error { return errNeedElevation }

// isReplaceLaunch 只有 Windows 的「提权重启」会用到。
func isReplaceLaunch() bool { return false }

func wantsAutoConnect() bool { return false }

func runPrivilegedAction() (bool, int) { return false, 0 }

// acquireSingleInstance 在 macOS / Linux 上不做限制：
// 系统本来就按 bundle / desktop 文件把重复启动合并到一个实例。
func acquireSingleInstance() bool { return true }

func acquireSingleInstanceWait(time.Duration) bool { return true }

func releaseSingleInstance() {}

func notifyAlreadyRunning() {}

func isElevated() bool { return os.Geteuid() == 0 }
