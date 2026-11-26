//go:build !windows

package main

// 非 Windows 平台不需要启动时自动提权：macOS 走 LaunchDaemon，Linux 走 systemd。
func maybeElevateOnStart() bool { return false }
