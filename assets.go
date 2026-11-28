package main

import "embed"

// 资源必须 embed 在根包：//go:embed 只能引用所在包目录下的文件，
// 而 frontend/dist 与 build/ 都在仓库根。

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

//go:embed build/tray-icon.png
var trayIcon []byte
