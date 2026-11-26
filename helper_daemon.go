//go:build darwin || linux

package main

import (
	"encoding/json"
	"log"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	wgtun "github.com/tailscale/wireguard-go/tun"
)

// 常驻助手监听在这里。装在 /Library/PrivilegedHelperTools 的副本由 launchd 拉起，
// 只在安装时授权一次，之后 App 每次连接直接连这个 socket，不再弹密码。
const helperSocket = "/var/run/weft.sock"

const daemonFlag = "--weft-daemon"

const helperVersion = "0.2.2"

// daemonState 是跨连接共享的唯一一套 TUN 与路由。
// 同一时刻只允许一个连接持有它（主 App 只会建一个），
// 其它连接（比如状态页的 ping）不参与竞争。
type daemonState struct {
	mu      sync.Mutex
	dev     wgtun.Device
	ifName  string
	applied []netip.Prefix
	owner   *net.UnixConn
}

func (s *daemonState) cleanupLocked() {
	if s.ifName != "" {
		cleanupAll(s.ifName, s.applied)
	}
	if s.dev != nil {
		_ = s.dev.Close()
		s.dev = nil
	}
	s.ifName, s.applied, s.owner = "", nil, nil
}

func openHelperListener(allowUID int) (net.Listener, error) {
	_ = os.Remove(helperSocket)
	ln, err := net.Listen("unix", helperSocket)
	if err != nil {
		return nil, err
	}
	if err := os.Chown(helperSocket, allowUID, -1); err != nil {
		log.Printf("[helper] chown socket 失败: %v", err)
	}
	_ = os.Chmod(helperSocket, 0o600)
	return ln, nil
}

// runDaemon 常驻并自愈：socket 文件可能被外部清理，
// 或启动瞬间两个实例互相覆盖，这里发现文件消失就重建监听。
// 已建立的连接（主 App 的 TUN）不受影响。
func runDaemon(allowUID int) error {
	st := &daemonState{}
	for {
		ln, err := openHelperListener(allowUID)
		if err != nil {
			log.Printf("[helper] 监听 %s 失败: %v，5 秒后重试", helperSocket, err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("[helper] 已就绪 socket=%s pid=%d allowUID=%d", helperSocket, os.Getpid(), allowUID)

		stop := make(chan struct{})
		go watchSocketFile(ln, stop)
		serveAccept(ln, st)
		close(stop)
		_ = ln.Close()
		log.Printf("[helper] 监听结束，重建中")
		time.Sleep(300 * time.Millisecond)
	}
}

// watchSocketFile 每 5 秒确认 socket 文件还在，不在就关掉 listener 触发重建。
func watchSocketFile(ln net.Listener, stop <-chan struct{}) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if _, err := os.Stat(helperSocket); err != nil {
				log.Printf("[helper] socket 文件消失（%v），重建监听", err)
				_ = ln.Close()
				return
			}
		}
	}
}

func serveAccept(ln net.Listener, st *daemonState) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !isClosedErr(err) {
				log.Printf("[helper] Accept 失败: %v", err)
			}
			return
		}
		// 每个连接独立处理：早期版本用一把锁罩住整个连接生命周期，
		// 结果主 App 的长期连接把 ping 全堵死，状态页一直显示「未运行」。
		go serveHelperConn(conn, st)
	}
}

func serveHelperConn(conn net.Conn, st *daemonState) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return
	}
	defer func() {
		// 只有持有 TUN 的那个连接断开时才清理，避免误删别人的网卡
		st.mu.Lock()
		if st.owner == uc {
			st.cleanupLocked()
		}
		st.mu.Unlock()
		_ = uc.Close()
	}()

	dec := json.NewDecoder(uc)
	enc := json.NewEncoder(uc)

	for {
		var req helperRequest
		if err := dec.Decode(&req); err != nil {
			return // 主进程断开
		}
		switch req.Op {
		case "ping":
			// 不碰共享状态，永远即时返回
			_ = enc.Encode(helperResponse{OK: true, Version: helperVersion})

		case "tun":
			st.mu.Lock()
			st.cleanupLocked() // 单活跃 TUN：旧的一律先关掉
			dev, err := createTUNDevice()
			if err != nil {
				st.mu.Unlock()
				_ = enc.Encode(helperResponse{Error: "创建 TUN 失败: " + err.Error()})
				continue
			}
			name, _ := dev.Name()
			st.dev, st.ifName, st.applied, st.owner = dev, name, nil, uc
			if err := sendFD(uc, dev.File()); err != nil {
				st.cleanupLocked()
				st.mu.Unlock()
				_ = enc.Encode(helperResponse{Error: "传递网卡句柄失败: " + err.Error()})
				return
			}
			_ = enc.Encode(helperResponse{OK: true, IfName: name})
			st.mu.Unlock()

		case "configure":
			st.mu.Lock()
			if st.dev == nil || st.owner != uc {
				st.mu.Unlock()
				_ = enc.Encode(helperResponse{Error: "当前连接没有持有网卡"})
				continue
			}
			ip, err := netip.ParseAddr(req.SelfIP)
			if err != nil {
				st.mu.Unlock()
				_ = enc.Encode(helperResponse{Error: "地址无效: " + req.SelfIP})
				continue
			}
			cleanupAll(st.ifName, st.applied)
			st.applied = nil
			if err := applyInterface(st.ifName, ip); err != nil {
				st.mu.Unlock()
				_ = enc.Encode(helperResponse{Error: err.Error()})
				continue
			}
			var firstErr string
			for _, raw := range req.Routes {
				p, perr := netip.ParsePrefix(raw)
				if perr != nil {
					continue
				}
				if perr := addRoute(st.ifName, p); perr != nil {
					if firstErr == "" {
						firstErr = perr.Error()
					}
					continue
				}
				st.applied = append(st.applied, p)
			}
			ifName := st.ifName
			st.mu.Unlock()
			if firstErr != "" {
				_ = enc.Encode(helperResponse{Error: firstErr})
			} else {
				_ = enc.Encode(helperResponse{OK: true, IfName: ifName})
			}

		case "cleanup":
			st.mu.Lock()
			if st.owner == uc {
				st.cleanupLocked()
			}
			st.mu.Unlock()
			_ = enc.Encode(helperResponse{OK: true})

		default:
			_ = enc.Encode(helperResponse{Error: "未知操作: " + req.Op})
		}
	}
}
