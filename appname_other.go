//go:build !darwin

package main

// setPlatformAppName 只有 macOS 需要：裸二进制默认用文件名做应用名。
// 其他平台由打包产物（.exe 资源 / .desktop）决定，这里留空。
func setPlatformAppName(string) {}

func menuBarTitle() string { return "" }
