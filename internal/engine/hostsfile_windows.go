//go:build windows

package engine

import "errors"

// 本地名字解析目前在 macOS / Linux 上实现。
// Windows 的 hosts 在 System32\drivers\etc 下，写入要管理员权限且没有常驻
// 助手通道，等有需求时再接（先别给出「已支持」的假象）。
var errHostsUnsupported = errors.New("本地名字解析暂只支持 macOS 和 Linux")

type HostEntry struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

type HostsResult struct {
	Applied int      `json:"applied"`
	Skipped []string `json:"skipped"`
	Path    string   `json:"path"`
}

func HostsEnabled() bool { return false }

func SyncHosts(entries []HostEntry) (HostsResult, error) {
	return HostsResult{}, errHostsUnsupported
}

func ClearHosts() error { return nil }
