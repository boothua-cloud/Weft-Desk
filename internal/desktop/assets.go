package desktop

// 图标由根包注入。//go:embed 只能引用本包目录下的文件，
// 而 build/ 在仓库根，所以 embed 留在 main 包，这里只放变量。
var (
	// Icon 是应用图标，设置窗口图标与应用内窗口加载页的角标。
	Icon []byte
	// TrayIcon 是托盘图标。
	TrayIcon []byte
)
