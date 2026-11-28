package engine

import "strings"

// RelayLabel 把 tailscale 的中继代码翻成界面能读的话。
func RelayLabel(code string) string {
	if strings.EqualFold(code, "headscale") {
		return "自建中继"
	}
	return code
}
