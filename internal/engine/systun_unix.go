//go:build darwin || linux

package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	wgtun "github.com/tailscale/wireguard-go/tun"
	"golang.org/x/sys/unix"
)

// ---------- 提权助手协议 ----------

const helperFlag = "--weft-helper"

type helperRequest struct {
	Op     string   `json:"op"` // tun | configure | cleanup | ping
	IfName string   `json:"ifName"`
	SelfIP string   `json:"selfIP"`
	Routes []string `json:"routes"`
}

type helperResponse struct {
	OK      bool   `json:"ok"`
	IfName  string `json:"ifName"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

// unixRouter 在 macOS / Linux 上工作。三条路径，优先级从高到低：
//  1. 常驻助手（launchd 装的，一次授权长期有效，零弹窗）
//  2. 一次性提权助手（没装常驻服务时的回退，会弹密码）
//  3. 进程本身是 root（直接干）
type unixRouter struct {
	conn      *net.UnixConn
	ln        net.Listener
	dev       wgtun.Device
	ifName    string
	applied   []netip.Prefix
	ephemeral bool
}

func newOSRouter() osRouter { return &unixRouter{} }

func (r *unixRouter) Prepare() (wgtun.Device, error) {
	if os.Geteuid() == 0 {
		dev, err := createTUNDevice()
		if err != nil {
			return nil, fmt.Errorf("创建 TUN 失败: %w", err)
		}
		r.dev = dev
		r.ifName, _ = dev.Name()
		return dev, nil
	}

	// 首选常驻助手：装过之后每次打开都不会再弹授权
	if dev, conn, ifName, err := dialDaemon(); err == nil {
		r.conn, r.dev, r.ifName = conn, dev, ifName
		log.Printf("[tunnel] 已通过常驻助手取得网卡 %s", ifName)
		return dev, nil
	} else {
		st := HelperStatus()
		log.Printf("[tunnel] 常驻助手不可用（已安装=%v 运行中=%v）：%v", st.Installed, st.Running, err)
		log.Printf("[tunnel] 改用一次性管理员授权；装好常驻助手后不必再弹密码")
	}

	sockPath := fmt.Sprintf("/tmp/weft-helper-%d.sock", os.Getuid())
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("创建本地通道失败: %w", err)
	}
	_ = os.Chmod(sockPath, 0o600)
	r.ln, r.ephemeral = ln, true

	if err := launchElevated(sockPath); err != nil {
		_ = ln.Close()
		_ = os.Remove(sockPath)
		return nil, err
	}

	if ul, ok := ln.(*net.UnixListener); ok {
		_ = ul.SetDeadline(time.Now().Add(60 * time.Second))
	}
	conn, err := ln.Accept()
	if err != nil {
		_ = ln.Close()
		_ = os.Remove(sockPath)
		detail := readTail(elevateLogPath(), 900)
		if detail == "" {
			return nil, fmt.Errorf("没有等到管理员助手连回来（授权可能被取消）。可在设置页安装常驻助手，之后不再需要每次授权")
		}
		return nil, fmt.Errorf("助手启动失败，授权日志：\n%s", detail)
	}
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("助手通道类型异常")
	}
	file, err := recvFD(uc)
	if err != nil {
		_ = uc.Close()
		return nil, fmt.Errorf("接收网卡句柄失败: %w", err)
	}
	var resp helperResponse
	_ = uc.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewDecoder(uc).Decode(&resp); err != nil || !resp.OK {
		_ = file.Close()
		_ = uc.Close()
		return nil, fmt.Errorf("助手未能创建网卡: %s", resp.Error)
	}
	_ = uc.SetDeadline(time.Time{})

	// 传 0 跳过 setMTU：非 root 设不了 MTU，助手创建时已经设过
	dev, err := wgtun.CreateTUNFromFile(file, 0)
	if err != nil {
		_ = file.Close()
		_ = uc.Close()
		return nil, fmt.Errorf("接管网卡失败: %w", err)
	}
	r.conn, r.dev = uc, dev
	r.ifName, _ = dev.Name()
	return dev, nil
}

// dialDaemon 连常驻助手并取回网卡。
func dialDaemon() (wgtun.Device, *net.UnixConn, string, error) {
	conn, err := net.DialTimeout("unix", helperSocket, 3*time.Second)
	if err != nil {
		return nil, nil, "", err
	}
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, nil, "", fmt.Errorf("助手通道类型异常")
	}
	_ = uc.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(uc).Encode(helperRequest{Op: "tun"}); err != nil {
		_ = uc.Close()
		return nil, nil, "", err
	}
	file, err := recvFD(uc)
	if err != nil {
		_ = uc.Close()
		return nil, nil, "", fmt.Errorf("接收网卡句柄失败: %w", err)
	}
	var resp helperResponse
	if err := json.NewDecoder(uc).Decode(&resp); err != nil || !resp.OK {
		_ = file.Close()
		_ = uc.Close()
		return nil, nil, "", fmt.Errorf("助手未能创建网卡: %s", resp.Error)
	}
	dev, err := wgtun.CreateTUNFromFile(file, 0)
	if err != nil {
		_ = file.Close()
		_ = uc.Close()
		return nil, nil, "", fmt.Errorf("接管网卡失败: %w", err)
	}
	_ = uc.SetDeadline(time.Time{})
	return dev, uc, resp.IfName, nil
}

func (r *unixRouter) Configure(dev wgtun.Device, selfIP netip.Addr, routes []netip.Prefix) error {
	if r.conn == nil {
		// 已经是 root，直接配置
		if err := applyInterface(r.ifName, selfIP); err != nil {
			return err
		}
		for _, p := range routes {
			if err := addRoute(r.ifName, p); err != nil {
				return fmt.Errorf("添加路由 %s 失败: %w", p, err)
			}
			r.applied = append(r.applied, p)
		}
		return nil
	}
	req := helperRequest{Op: "configure", IfName: r.ifName, SelfIP: selfIP.String()}
	for _, p := range routes {
		req.Routes = append(req.Routes, p.String())
	}
	resp, err := r.roundTrip(req)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	r.applied = routes
	return nil
}

func (r *unixRouter) Cleanup() {
	if r.conn != nil {
		req := helperRequest{Op: "cleanup", IfName: r.ifName}
		for _, p := range r.applied {
			req.Routes = append(req.Routes, p.String())
		}
		_, _ = r.roundTrip(req)
		_ = r.conn.Close()
		r.conn = nil
	} else if r.ifName != "" {
		cleanupAll(r.ifName, r.applied)
	}
	if r.ln != nil {
		addr := r.ln.Addr().String()
		_ = r.ln.Close()
		if r.ephemeral {
			_ = os.Remove(addr)
		}
		r.ln = nil
	}
	if r.dev != nil {
		_ = r.dev.Close()
		r.dev = nil
	}
	r.applied = nil
}

func (r *unixRouter) roundTrip(req helperRequest) (helperResponse, error) {
	var resp helperResponse
	_ = r.conn.SetDeadline(time.Now().Add(20 * time.Second))
	if err := json.NewEncoder(r.conn).Encode(req); err != nil {
		return resp, fmt.Errorf("与管理员助手通信失败: %w", err)
	}
	if err := json.NewDecoder(r.conn).Decode(&resp); err != nil {
		return resp, fmt.Errorf("读取管理员助手响应失败: %w", err)
	}
	return resp, nil
}

// ---------- 助手进程 ----------

// MaybeRunHelper 在 main 最前面调用；以助手身份启动时接管进程。
func MaybeRunHelper() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case helperFlag, daemonFlag:
	default:
		return false
	}

	sock, uid := "", 0
	for i := 2; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--sock":
			sock = os.Args[i+1]
		case "--uid", "--allow-uid":
			uid, _ = strconv.Atoi(os.Args[i+1])
		}
	}

	var err error
	if os.Args[1] == daemonFlag {
		err = runDaemon(uid)
	} else {
		err = runHelper(sock, uid)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "weft-helper:", err)
		os.Exit(1)
	}
	return true
}

// runHelper 是一次性助手：建 TUN、传 fd、处理配置，连接断开即退出并清理。
func runHelper(sockPath string, uid int) error {
	dev, err := createTUNDevice()
	if err != nil {
		return fmt.Errorf("创建 TUN 失败: %w", err)
	}
	ifName, _ := dev.Name()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		_ = dev.Close()
		return fmt.Errorf("连接主进程失败: %w", err)
	}
	uc := conn.(*net.UnixConn)
	defer uc.Close()
	if err := sendFD(uc, dev.File()); err != nil {
		_ = dev.Close()
		return fmt.Errorf("传递网卡句柄失败: %w", err)
	}
	if err := json.NewEncoder(uc).Encode(helperResponse{OK: true, IfName: ifName}); err != nil {
		_ = dev.Close()
		return err
	}

	var applied []netip.Prefix
	dec := json.NewDecoder(uc)
	for {
		var req helperRequest
		if err := dec.Decode(&req); err != nil {
			break
		}
		resp := helperResponse{OK: true, IfName: ifName}
		switch req.Op {
		case "configure":
			ip, perr := netip.ParseAddr(req.SelfIP)
			if perr != nil {
				resp = helperResponse{Error: "地址无效: " + req.SelfIP}
				break
			}
			cleanupAll(ifName, applied)
			applied = nil
			if err := applyInterface(ifName, ip); err != nil {
				resp = helperResponse{Error: err.Error()}
				break
			}
			for _, raw := range req.Routes {
				p, err := netip.ParsePrefix(raw)
				if err != nil {
					continue
				}
				if err := addRoute(ifName, p); err != nil {
					resp = helperResponse{Error: err.Error()}
					continue
				}
				applied = append(applied, p)
			}
		case "cleanup":
			cleanupAll(ifName, applied)
			applied = nil
		case "ping":
			resp.Version = helperVersion
		}
		_ = json.NewEncoder(uc).Encode(resp)
	}

	// 主进程崩溃 / 退出：撤掉路由，绝不留指向死网卡的路由
	cleanupAll(ifName, applied)
	return dev.Close()
}

func cleanupAll(ifName string, routes []netip.Prefix) {
	for _, p := range routes {
		_ = delRoute(ifName, p)
	}
}

// ---------- fd 传递 ----------

func sendFD(conn *net.UnixConn, f *os.File) error {
	rights := unix.UnixRights(int(f.Fd()))
	_, _, err := conn.WriteMsgUnix([]byte{'T'}, rights, nil)
	return err
}

func recvFD(conn *net.UnixConn) (*os.File, error) {
	buf := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, err
	}
	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) == 0 {
		return nil, fmt.Errorf("未收到文件描述符")
	}
	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil || len(fds) == 0 {
		return nil, fmt.Errorf("文件描述符解析失败")
	}
	return os.NewFile(uintptr(fds[0]), "weft-tun"), nil
}

// ---------- 一次性提权启动 ----------

func launchElevated(sockPath string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	uid := strconv.Itoa(os.Getuid())

	if isDarwin() {
		// 与安装助手同样的 TCC 问题：root 无法执行 ~/Documents 下的二进制，
		// 先复制到一个私有临时目录再执行，否则脚本会以 Operation not permitted 退出。
		tmpDir, err := os.MkdirTemp("", "weft-elevate-")
		if err != nil {
			return fmt.Errorf("创建临时目录失败: %w", err)
		}
		defer os.RemoveAll(tmpDir)
		runSelf, err := copySelfToTemp(tmpDir)
		if err != nil {
			return fmt.Errorf("准备助手二进制失败: %w", err)
		}
		// root 执行之前先对一遍 hash：从「文件写好」到「root 复制它」之间
		// 有窗口期，光靠目录权限拦不住同用户的进程。
		sum, err := fileSHA256(runSelf)
		if err != nil {
			return fmt.Errorf("计算助手校验值失败: %w", err)
		}
		script := fmt.Sprintf("#!/bin/sh\nset -e\n[ \"$(%s)\" = %q ] || { echo '助手二进制校验失败' >&2; exit 1; }\nexec %s %s --sock %s --uid %s\n",
			shasumCmd(shQuote(runSelf)), sum,
			shQuote(runSelf), helperFlag, shQuote(sockPath), uid)
		f, err := os.CreateTemp(tmpDir, "launch-*.sh")
		if err != nil {
			return err
		}
		name := f.Name()
		_, _ = f.WriteString(script)
		_ = f.Chmod(0o700)
		_ = f.Close()
		defer os.Remove(name)

		apple := fmt.Sprintf(`do shell script "/bin/sh %s" with administrator privileges`, shQuote(name))
		var buf bytes.Buffer
		logPath := elevateLogPath()
		logFile, _ := os.Create(logPath)
		cmd := exec.Command("/usr/bin/osascript", "-e", apple)
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if logFile != nil {
			defer logFile.Close()
			cmd.Stdout, cmd.Stderr = logFile, logFile
		}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("启动授权对话框失败: %w", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		// 助手是常驻的，osascript 会一直挂着。只有它很快退出才是授权失败。
		select {
		case <-done:
			msg := strings.TrimSpace(buf.String())
			if msg == "" {
				msg = "已取消或未授予权限"
			}
			return fmt.Errorf("管理员授权失败：%s", msg)
		case <-time.After(2 * time.Second):
			go func() { <-done }()
			return nil
		}
	}

	if _, err := exec.LookPath("pkexec"); err == nil {
		cmd := exec.Command("pkexec", self, helperFlag, "--sock", sockPath, "--uid", uid)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("启动 pkexec 失败: %w", err)
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	cmd := exec.Command("sudo", "-n", self, helperFlag, "--sock", sockPath, "--uid", uid)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("无法提权：请安装 polkit/pkexec，或以 root 运行 Weft")
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// copySelfToTemp 把当前可执行文件复制到私有临时目录。
// macOS 的 TCC 会阻止 root 读取/执行 ~/Documents 下的文件，
// 提权脚本必须先拿到这个副本。目录是 0700 的，不跟别人共用 /tmp。
func copySelfToTemp(dir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	src, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer src.Close()
	f, err := os.CreateTemp(dir, "bin-*")
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(f, src); err != nil {
		_ = f.Close()
		return "", err
	}
	_ = f.Chmod(0o755)
	_ = f.Close()
	return f.Name(), nil
}

// elevateLogPath 授权失败的日志放在用户缓存目录里，而不是 /tmp：
// 固定路径好回读，0700 目录也不会被换成符号链接让 root 写别处。
func elevateLogPath() string {
	if d, err := os.UserCacheDir(); err == nil {
		p := filepath.Join(d, "Weft")
		if err := os.MkdirAll(p, 0o700); err == nil {
			return filepath.Join(p, "elevate.log")
		}
	}
	return filepath.Join(os.TempDir(), "weft-elevate.log")
}

// fileSHA256 算文件的 sha256，提权脚本用它确认拿到的是自己写的那份。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// shasumCmd 返回「算出这个文件 sha256」的 shell 片段，结果只有十六进制。
// macOS 自带 shasum，多数 Linux 只有 sha256sum。
func shasumCmd(quotedPath string) string {
	return fmt.Sprintf(`if command -v shasum >/dev/null 2>&1; then shasum -a 256 %[1]s | cut -d' ' -f1; else sha256sum %[1]s | cut -d' ' -f1; fi`, quotedPath)
}

func readTail(path string, max int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > max {
		b = b[len(b)-max:]
	}
	return strings.TrimSpace(string(b))
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pingDaemon 确认常驻助手活着，并取回它的版本号。
func pingDaemon() (string, error) {
	conn, err := net.DialTimeout("unix", helperSocket, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewEncoder(conn).Encode(helperRequest{Op: "ping"}); err != nil {
		return "", err
	}
	var resp helperResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("%s", resp.Error)
	}
	return resp.Version, nil
}
