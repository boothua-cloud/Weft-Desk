//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Linux 用 systemd 承载常驻助手：装一次、之后由 systemd 拉起，连接不再弹授权。
const (
	linuxUnitPath = "/etc/systemd/system/weft-helper.service"
	linuxBinPath  = "/usr/local/lib/weft/weft"
	linuxUnitName = "weft-helper"
)

func helperLogTail() string {
	out, err := exec.Command("journalctl", "-u", linuxUnitName, "-n", "15", "--no-pager", "-o", "cat").Output()
	if err == nil && len(out) > 0 {
		s := strings.TrimSpace(string(out))
		if len(s) > 900 {
			s = s[len(s)-900:]
		}
		return s
	}
	return tailFile("/var/log/weft-helper.log", 900)
}

func helperStatus() HelperStatus {
	st := HelperStatus{Socket: helperSocket}
	st.Installed = fileExists(linuxUnitPath) && fileExists(linuxBinPath)
	if st.Installed {
		for i := 0; i < 2; i++ {
			if v, err := pingDaemon(); err == nil {
				st.Running, st.Version = true, v
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	switch {
	case !st.Installed:
		st.Note = "未安装：点下方按钮安装常驻助手，之后连接不再请求授权"
	case !st.Running:
		st.Note = "已安装但未运行：systemctl status weft-helper 查看原因"
		st.LogTail = helperLogTail()
	default:
		st.Note = "运行中：打开即可连接"
	}
	return st
}

func installHelper() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	uid := strconv.Itoa(os.Getuid())
	unit := fmt.Sprintf(`[Unit]
Description=Weft network helper
After=network-online.target

[Service]
Type=simple
ExecStart=%s %s --allow-uid %s
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`, linuxBinPath, daemonFlag, uid)

	script := "#!/bin/sh\nset -e\n" +
		"install -d -m 755 /usr/local/lib/weft\n" +
		"install -m 755 " + shQuote(self) + " " + linuxBinPath + "\n" +
		"cat > " + linuxUnitPath + " <<'WEFT_UNIT'\n" + unit + "WEFT_UNIT\n" +
		"systemctl daemon-reload\n" +
		"systemctl enable --now " + linuxUnitName + "\n"

	if out, err := runElevatedScript(script, "weft-install-*.sh"); err != nil {
		return fmt.Errorf("安装常驻助手失败：%s", strings.TrimSpace(out+" "+err.Error()))
	}
	for i := 0; i < 40; i++ {
		if _, err := pingDaemon(); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	tail := helperLogTail()
	if tail == "" {
		tail = "（无日志）常见原因：/usr/local/lib/weft 无可执行权限，或 systemd 未运行"
	}
	return fmt.Errorf("助手已安装但未就绪。日志：\n%s", tail)
}

func uninstallHelper() error {
	script := "#!/bin/sh\nset -e\n" +
		"systemctl disable --now " + linuxUnitName + " 2>/dev/null || true\n" +
		"rm -f " + linuxUnitPath + "\n" +
		"rm -f " + linuxBinPath + "\n" +
		"systemctl daemon-reload\n"
	if out, err := runElevatedScript(script, "weft-uninstall-*.sh"); err != nil {
		return fmt.Errorf("移除失败：%s", strings.TrimSpace(out+" "+err.Error()))
	}
	return nil
}

// runElevatedScript 用 pkexec（回退 sudo -n）执行一段 root 脚本，返回输出。
func runElevatedScript(script, pattern string) (string, error) {
	f, err := os.CreateTemp("/tmp", pattern)
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		return "", err
	}
	_ = f.Chmod(0o700)
	_ = f.Close()

	var cmd *exec.Cmd
	if _, err := exec.LookPath("pkexec"); err == nil {
		cmd = exec.Command("pkexec", "/bin/sh", f.Name())
	} else {
		cmd = exec.Command("sudo", "-n", "/bin/sh", f.Name())
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
