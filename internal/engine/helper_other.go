//go:build !windows

package engine

import (
	"os"
	"time"
)

// 非 Windows 平台不需要「启动即提权」：macOS 走 LaunchDaemon，Linux 走 systemd，
// 提权在 unixRouter 内部完成（常驻助手或一次性授权）。
// 这里只放几个让 main 保持平台无关的空实现。

func RelaunchElevatedConnect() error { return ErrNeedElevation }

// IsReplaceLaunch 只有 Windows 的「提权重启」会用到。
func IsReplaceLaunch() bool { return false }

func WantsAutoConnect() bool { return false }

func RunPrivilegedAction() (bool, int) { return false, 0 }

// AcquireSingleInstance 在 macOS / Linux 上不做限制：
// 系统本来就按 bundle / desktop 文件把重复启动合并到一个实例。
func AcquireSingleInstance() bool { return true }

func AcquireSingleInstanceWait(time.Duration) bool { return true }

func ReleaseSingleInstance() {}

func NotifyAlreadyRunning() {}

func IsElevated() bool { return os.Geteuid() == 0 }
