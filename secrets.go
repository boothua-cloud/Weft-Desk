package main

import (
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

// 凭证只写系统钥匙串：macOS Keychain / Windows Credential Manager / Linux Secret Service。
// 纯 Go 实现，不引入 CGO，三平台交叉编译不受影响。
const (
	serviceName  = "weft"
	secretMain   = "control-plane-secret"
	secretClient = "oauth-client-id"
	secretAuth   = "tunnel-auth-key"
)

func secretSet(key, value string) error { return keyring.Set(serviceName, key, value) }

func secretGet(key string) string {
	v, err := keyring.Get(serviceName, key)
	if err != nil {
		return ""
	}
	return v
}

func secretDelete(key string) { _ = keyring.Delete(serviceName, key) }

// defaultHostname 默认拿本机主机名当节点名，装完不用想名字就能连。
// macOS 的 os.Hostname() 常带 .local 后缀，名字里的非法字符统一换成 -。
func defaultHostname() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "weft"
	}
	h = strings.TrimSuffix(strings.TrimSpace(h), ".local")
	var b strings.Builder
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == ' ':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "weft"
	}
	return out
}
