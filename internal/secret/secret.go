package secret

import (
	"github.com/zalando/go-keyring"
)

// 凭证只写系统钥匙串：macOS Keychain / Windows Credential Manager / Linux Secret Service。
// 纯 Go 实现，不引入 CGO，三平台交叉编译不受影响。
const (
	serviceName = "weft"
	Main        = "control-plane-secret"
	Client      = "oauth-client-id"
	Auth        = "tunnel-auth-key"
	// secretSync 是应用书签同步码，开了同步才生成。
	Sync = "apps-sync-token"
)

func Set(key, value string) error { return keyring.Set(serviceName, key, value) }

func Get(key string) string {
	v, err := keyring.Get(serviceName, key)
	if err != nil {
		return ""
	}
	return v
}

func Delete(key string) { _ = keyring.Delete(serviceName, key) }
