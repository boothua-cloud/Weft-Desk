//go:build !darwin && !windows

package sysproxy

import "net/url"

// systemProxyURL 其他平台只认环境变量，不去读桌面设置。
func systemProxyURL(host string) *url.URL { return nil }
