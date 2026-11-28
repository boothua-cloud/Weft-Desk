//go:build darwin

package engine

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/boothua/weft/internal/model"
)

const (
	helperLabel     = "com.boothua.weft.helper"
	helperBinPath   = "/Library/PrivilegedHelperTools/" + helperLabel
	helperPlistPath = "/Library/LaunchDaemons/" + helperLabel + ".plist"
)

func HelperStatus() model.HelperStatus {
	st := model.HelperStatus{Socket: helperSocket}
	st.Installed = fileExists(helperBinPath) && fileExists(helperPlistPath)
	if st.Installed {
		// 助手可能刚被 launchd 拉起或正在重建 socket，重试一次避免误判
		for i := 0; i < 2; i++ {
			if v, err := pingDaemon(); err == nil {
				st.Running, st.Version = true, v
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	if !st.Installed {
		st.Note = "未安装：点下方按钮，输一次密码即可长期免密"
	} else if !st.Running {
		st.Note = "已安装但当前连接不上助手（socket 可能在重建），连接会回退到一次性授权"
		st.LogTail = helperLogTail()
	} else if st.Version != helperVersion {
		st.Note = "助手版本与当前程序不一致（" + st.Version + "），建议重新安装"
	} else {
		st.Note = "常驻助手已就绪：打开即可连接，不再请求管理员授权"
	}
	return st
}

// InstallHelper 通过一次管理员授权，把当前二进制复制到
// /Library/PrivilegedHelperTools 并注册为 LaunchDaemon。
// 之后每次启动由 launchd 拉起，App 直连 socket，零弹窗。
func InstallHelper() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	uid := strconv.Itoa(os.Getuid())

	// TCC 会拦 root 读取 ~/Documents 等受保护目录，install 会报
	// "Operation not permitted (71)"。先把自身复制到 root 可读的临时目录。
	// 目录 0700，不跟别人共用 /tmp；真正的防线是下面脚本里的校验值。
	tmpDir, err := os.MkdirTemp("", "weft-helper-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	src, err := os.Open(self)
	if err != nil {
		return fmt.Errorf("读取自身可执行文件失败: %w", err)
	}
	defer src.Close()
	tmpBin, err := os.CreateTemp(tmpDir, "bin-*")
	if err != nil {
		return err
	}
	if _, err = io.Copy(tmpBin, src); err != nil {
		_ = tmpBin.Close()
		return fmt.Errorf("复制助手二进制失败: %w", err)
	}
	_ = tmpBin.Chmod(0o755)
	_ = tmpBin.Close()

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>%s</string>
    <string>--allow-uid</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>/var/log/weft-helper.log</string>
  <key>StandardErrorPath</key><string>/var/log/weft-helper.log</string>
</dict>
</plist>
`, helperLabel, helperBinPath, daemonFlag, uid)

	tmpPlist, err := os.CreateTemp(tmpDir, "helper-*.plist")
	if err != nil {
		return err
	}
	if _, err = tmpPlist.WriteString(plist); err != nil {
		return err
	}
	_ = tmpPlist.Close()

	binSum, err := fileSHA256(tmpBin.Name())
	if err != nil {
		return fmt.Errorf("计算助手校验值失败: %w", err)
	}
	plistSum, err := fileSHA256(tmpPlist.Name())
	if err != nil {
		return fmt.Errorf("计算 plist 校验值失败: %w", err)
	}

	script := fmt.Sprintf(`#!/bin/sh
set -e
LABEL=%s
BIN=%s
PLIST=%s
mkdir -p /Library/PrivilegedHelperTools
[ "$(%s)" = %q ] || { echo '助手二进制校验失败，已中止' >&2; exit 1; }
[ "$(%s)" = %q ] || { echo 'plist 校验失败，已中止' >&2; exit 1; }
launchctl bootout system/$LABEL 2>/dev/null || true
install -m 544 -o root -g wheel %s "$BIN"
install -m 644 -o root -g wheel %s "$PLIST"
launchctl bootstrap system "$PLIST"
`, helperLabel, helperBinPath, helperPlistPath,
		shasumCmd(shQuote(tmpBin.Name())), binSum,
		shasumCmd(shQuote(tmpPlist.Name())), plistSum,
		shQuote(tmpBin.Name()), shQuote(tmpPlist.Name()))

	if err := runElevatedScript(script); err != nil {
		return err
	}

	// launchd 起进程有延迟，给几次机会确认 socket 就绪
	for i := 0; i < 15; i++ {
		if _, err := pingDaemon(); err == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	tail := helperLogTail()
	if tail == "" {
		return fmt.Errorf("助手已安装但未就绪，且 /var/log/weft-helper.log 为空。" +
			"常见原因：launchd 拒绝启动未签名的二进制，或 /Library/PrivilegedHelperTools 权限异常")
	}
	return fmt.Errorf("助手已安装但未就绪。日志尾部：\n%s", tail)
}

func UninstallHelper() error {
	script := fmt.Sprintf(`#!/bin/sh
LABEL=%s
launchctl bootout system/$LABEL 2>/dev/null || true
rm -f %s %s %s
`, helperLabel, helperPlistPath, helperBinPath, helperSocket)
	return runElevatedScript(script)
}

func runElevatedScript(script string) error {
	f, err := os.CreateTemp("/tmp", "weft-install-*.sh")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.WriteString(script); err != nil {
		return err
	}
	_ = f.Chmod(0o700)
	_ = f.Close()

	apple := fmt.Sprintf(`do shell script "/bin/sh %s" with administrator privileges`, shQuote(name))
	var buf bytes.Buffer
	cmd := exec.Command("/usr/bin/osascript", "-e", apple)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(buf.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("安装系统助手失败（可能已取消授权）：%s", msg)
	}
	return nil
}

// helperLogTail 读取 launchd 输出，安装失败或未启动时用来定位原因。
func helperLogTail() string { return tailFile("/var/log/weft-helper.log", 800) }
