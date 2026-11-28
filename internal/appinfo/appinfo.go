// Package appinfo 是应用级的常量与默认值：版本号、发布源、数据目录、
// 默认节点名。这些值被存储、引擎、更新、界面多处共用，单独放一处，
// 改的时候只有这一个地方要看。
package appinfo

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Version 是客户端版本号，同时是自动更新比较的基准。
//
// 之前这个数字散在十来个文件里（plist、info.json、nsi、前端三处、README、
// 打包脚本），每次发版都得挨个改，漏一个就会出现「安装包是 0.1.5、界面还写
// 0.1.4」这种事。现在只留这一处：
//
//   - 前端从 State.AppVersion 读，不再硬编码
//   - CI 可用 -ldflags "-X github.com/boothua/weft/internal/appinfo.Version=x.y.z" 覆盖
//
// 版本号不带 v 前缀，和 updater 的比较规则一致（tag 上的 v 会被剥掉）。
var Version = "0.1.5"

// DefaultSyncPort 是应用书签同步服务的默认端口。
const DefaultSyncPort = 4160

// UpdateRepo 是发布源，"owner/repo" 形式。
const UpdateRepo = "boothua-cloud/Weft-Desk"

// DataDir 是配置、数据库、节点身份实际存放的目录。
func DataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "Weft")
}

// DataDirDisplay 是给界面看的版本：macOS/Linux 缩成 ~，Windows 给完整路径。
func DataDirDisplay() string {
	dir := DataDir()
	if dir == "" {
		return ""
	}
	// Windows 上 os.UserConfigDir() 就是 C:\Users\<用户名>\AppData\Roaming，
	// 直接显示完整路径，不用 %AppData% 这种缩写让人猜。
	if runtime.GOOS == "windows" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(dir, home) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

// DefaultHostname 默认拿本机主机名当节点名，装完不用想名字就能连。
// macOS 的 os.Hostname() 常带 .local 后缀，名字里的非法字符统一换成 -。
func DefaultHostname() string {
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
