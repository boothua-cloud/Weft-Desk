package desktop

import (
	"regexp"
	"strings"
)

// 诊断包会发出去给别人看，里面的凭证、邮箱、内网地址都要先处理一遍。
// 脱敏只做这一处：打包时统一过一遍，而不是每个字段各自记得抹。
//
// 保留的东西也有讲究：服务端域名留着（判断连不连得上要用），
// 端口留着，版本号留着；Key、密码、邮箱主体、100.64 网段的具体地址都抹掉。

var (
	// headscale / tailscale 的钥匙都有固定前缀，直接整段换掉
	reKey = regexp.MustCompile(`\b(?:hskey|tskey)-[A-Za-z0-9_-]+`)
	// 邮箱：本地部分留首字母，域名留着
	reMail = regexp.MustCompile(`\b([A-Za-z0-9._%+-])([A-Za-z0-9._%+-]*)@([A-Za-z0-9.-]+\.[A-Za-z]{2,})\b`)
	// tailnet 地址是 100.64.0.0/10，具体到哪台机器没必要带出去
	reTailIP = regexp.MustCompile(`\b100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3}\b`)
	// URL 的路径和 query 里常有 token、房间号，只留 scheme+host
	reURLPath = regexp.MustCompile(`\b(https?://[A-Za-z0-9._~-]+(?::\d+)?)(/[^\s"',)]*)?`)
)

// redactText 处理任意一段文本（JSON、日志、配置都走它）。
func redactText(s string) string {
	if s == "" {
		return s
	}
	s = reKey.ReplaceAllString(s, "<redacted>")
	s = reTailIP.ReplaceAllString(s, "100.64.x.x")
	s = reMail.ReplaceAllString(s, "$1***@$3")
	s = reURLPath.ReplaceAllStringFunc(s, func(m string) string {
		sub := reURLPath.FindStringSubmatch(m)
		if len(sub) < 3 || sub[2] == "" {
			return m
		}
		return sub[1] + "/…"
	})
	return s
}

// redactLines 逐行过，日志量大时不用整块拼接。
func redactLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = redactText(l)
	}
	return out
}

// redactError 只保留错误里「哪一步失败了」，去掉可能带出的地址和 Key。
func redactError(msg string) string {
	return strings.TrimSpace(redactText(msg))
}
