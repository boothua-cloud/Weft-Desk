package desktop

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/boothua/weft/internal/appinfo"
	"github.com/boothua/weft/internal/controlplane"
	"github.com/boothua/weft/internal/engine"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/secret"
)

// 连接状态：总览页的聚合状态、连接与断开、运行时长格式化。

func (s *WeftService) GetState() (string, error) {
	s.mu.Lock()
	cfg, cap, connected, started := s.cfg, s.cap, s.connected, s.started
	e := s.engine // 和 cfg 一起取：reload 会在锁里换掉它
	s.mu.Unlock()
	hasKey, keyWhy := s.apiKeyStatus()
	st := model.State{
		Connected: connected, ControlPlane: cfg.ControlPlane,
		Endpoint: cfg.BaseURL, Engine: e.Name(),
		EngineReady: e.Available(), CLIReady: e.Available(),
		LoginURL: e.LoginURL(), Configured: hasCredential(cfg),
		HasAPIKey:    hasKey,
		APIKeyIssue:  keyWhy,
		HasAuthKey:   strings.TrimSpace(secret.Get(secret.Auth)) != "",
		Capabilities: cap,
		Platform:     runtime.GOOS,
		AppVersion:   appinfo.Version,
		DataDir:      appinfo.DataDirDisplay(),
	}
	if pr, ok := e.(interface{ Peers() []model.PeerInfo }); ok {
		st.Peers = pr.Peers()
	}
	if le, ok := e.(interface{ LastError() string }); ok {
		st.LastError = le.LastError()
	}
	if rr, ok := e.(interface {
		RouteInfo() ([]string, []string, string)
	}); ok {
		st.Routes, st.Skipped, st.NetErr = rr.RouteInfo()
	}
	st.Mode = "live"
	if st1, ok := e.(interface{ Starting() bool }); ok {
		st.Connecting = st1.Starting()
	}
	s.mu.Lock()
	if s.autoPending {
		st.Connecting = true
	}
	s.mu.Unlock()
	running, ip, host := e.Status()
	st.Connected = running
	st.SelfIP = ip
	st.Hostname = host
	// 运行时长以引擎自己的上线时刻为准：自动连接的路径不经过 Connect，
	// 只记 s.started 会永远是零值（界面上就是一直显示「—」）。
	since := started
	if sr, ok := e.(interface{ Since() time.Time }); ok {
		if t := sr.Since(); !t.IsZero() {
			since = t
		}
	}
	if running && !since.IsZero() {
		st.Uptime = formatUptime(time.Since(since))
	}
	st.Message = cfg.ControlPlane + " · " + cfg.BaseURL
	if st.SelfIP == "" {
		st.SelfIP = "—"
	}
	st.LocalIPs = localIPv4s()
	s.fillNameSync(&st)
	// 顺手在后台确认一下这把钥匙服务端认不认（只在没结论时才发请求）
	s.ensureAPIKeyProbe()
	return toJSON(st)
}

// apiKeyStatus 回答「该不该显示服务端管理入口」，顺便给出不该显示的原因。
//
// 顺序：空 → 形状不对 → 服务端确认过没有。最后一步必须等服务端真的点头：
// 以前形状对就先显示，于是填一把假的整组菜单都冒出来，点进去全是 401。
func (s *WeftService) apiKeyStatus() (bool, string) {
	key := strings.TrimSpace(secret.Get(secret.Main))
	if key == "" {
		return false, ""
	}
	if controlplane.IsAuthKey(key) {
		return false, "这是节点注册的 Auth Key，管理接口不认。改用 `headscale apikeys create` 生成的 Key"
	}
	s.mu.Lock()
	p := s.keyProbe
	s.mu.Unlock()
	if p.key == key {
		if p.bad {
			return false, "服务端不认这把 Key（" + p.msg + "）。确认它由 `headscale apikeys create` 生成、没过期"
		}
		return true, ""
	}
	if !controlplane.LooksLikeAPIKey(key) {
		return false, "格式不正确：headscale 的管理 Key 以 `hskey-` 开头"
	}
	// 格式对、但还没跟服务端确认过，先不亮入口。
	// 以前这里乐观返回 true，结果是填一把假的整组菜单都出来，点进去全是 401。
	if p.at.IsZero() {
		return false, "正在确认这把 Key 能不能用…"
	}
	return false, "还没连上服务端，确认不了这把 Key"
}

// ensureAPIKeyProbe 在后台问一次服务端「这把钥匙收不收」。
// 由 GetState 触发：界面每 3 秒轮询一次，所以配好或清掉 Key 之后最多几秒，
// 入口就会跟着出现/消失，不用在每个保存、切换的路径上各挂一份。
func (s *WeftService) ensureAPIKeyProbe() {
	key := strings.TrimSpace(secret.Get(secret.Main))
	if key == "" || controlplane.IsAuthKey(key) {
		return // 这两种情况 apiKeyStatus 已经能直接回答，不必打接口
	}
	s.mu.Lock()
	p, cp := s.keyProbe, s.cp
	if p.busy || p.key == key || (!p.at.IsZero() && time.Since(p.at) < 30*time.Second) {
		// 正在问 / 已有结论 / 刚试过还没结论：都先不重复发
		s.mu.Unlock()
		return
	}
	// 服务端客户端还没建好（刚启动、还没连上）。这里不能占下 busy 标记：
	// 占下之后没人会清，之后连上了也再不会去问，管理入口就一直不出现。
	if cp == nil {
		s.mu.Unlock()
		return
	}
	s.keyProbe = keyProbe{key: p.key, at: time.Now(), busy: true}
	s.mu.Unlock()
	go func() {
		var err error
		if kc, ok := cp.(interface{ CheckKey() error }); ok {
			err = kc.CheckKey()
		} else {
			_, err = cp.Devices("")
		}
		cur := strings.TrimSpace(secret.Get(secret.Main))
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case cur != key:
			// 问的过程中钥匙被改了，这次结论作废，下一轮重新问
			s.keyProbe = keyProbe{}
		case controlplane.IsRejected(err):
			s.keyProbe = keyProbe{key: key, bad: true, msg: controlplane.Short([]byte(err.Error())), at: time.Now()}
			log.Printf("[keys] 管理 Key 被拒绝，服务端管理入口已收起: %v", err)
		case err == nil:
			s.keyProbe = keyProbe{key: key, at: time.Now()}
		default:
			// 网络不通之类：不知道，过 30 秒再问一次
			s.keyProbe = keyProbe{at: time.Now()}
		}
	}()
}

// tailnetCIDR 是 tailscale 的地址段。本机地址列表必须排除它：
// 那是隧道自己的地址，换 Wi-Fi 也不会变，算进去就判断不出网络切换。
var tailnetCIDR = netip.MustParsePrefix("100.64.0.0/10")

// localIPv4s 列本机真实网卡上的 IPv4，界面拿两次结果比对就知道系统网络换了没有。
//
// 这个函数每次 GetState 都会跑（界面 3 秒一轮），所以只做一次接口枚举，
// 不发任何请求。回环、链路本地（169.254）和 100.64/10 都排除掉，
// Docker、虚拟机那些虚拟网卡只要地址不变就不会影响结论。
func localIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]string, 0, 4)
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			ip := netip.AddrFrom4([4]byte(ip4))
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || tailnetCIDR.Contains(ip) {
				continue
			}
			out = append(out, ip.String())
		}
	}
	sort.Strings(out)
	return out
}

// formatUptime 把时长写成「2 天 3 小时」这种能直接读的形式。
// time.Duration.String() 给的是 50h3m2s，超过一天就没法读。
func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Truncate(time.Second)
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int(d / time.Minute)
	secs := int(d/time.Second) - mins*60
	switch {
	case days > 0:
		if hours > 0 {
			return fmt.Sprintf("%d 天 %d 小时", days, hours)
		}
		return fmt.Sprintf("%d 天", days)
	case hours > 0:
		if mins > 0 {
			return fmt.Sprintf("%d 小时 %d 分", hours, mins)
		}
		return fmt.Sprintf("%d 小时", hours)
	case mins > 0:
		return fmt.Sprintf("%d 分 %d 秒", mins, secs)
	default:
		return fmt.Sprintf("%d 秒", secs)
	}
}

func (s *WeftService) Connect() (string, error) {
	if err := s.engine.Up(s.cfg); err != nil {
		// Windows 权限不够不是错误终点：拉起一次 UAC，以管理员身份重启自己，
		// 重启后自动连接。用户只需要在系统弹窗里点一次「是」。
		if errors.Is(err, engine.ErrNeedElevation) {
			// 诊断模式（--diag）没有界面可重启，也不能替用户弹窗：
			// 直接把话说清楚，让他在管理员终端里跑。
			s.mu.Lock()
			hasApp := s.app != nil
			s.mu.Unlock()
			if !hasApp {
				return "", fmt.Errorf("%w：请以管理员身份打开终端后再执行，例如右键「以管理员身份运行」PowerShell", err)
			}
			// 先把单实例锁交出去，提权后的新进程才能接管；
			// 否则新进程会判定「已有实例」而直接退出，用户看到窗口一闪就没。
			engine.ReleaseSingleInstance()
			if e := engine.RelaunchElevatedConnect(); e != nil {
				return "", fmt.Errorf("%s（%v）。也可以先在设置页点「安装」配好登录自启，以后不用再授权", err, e)
			}
			log.Printf("[tunnel] 已请求管理员授权，授权后将以管理员身份重启并自动连接")
			go func() {
				time.Sleep(1200 * time.Millisecond)
				s.mu.Lock()
				app := s.app
				s.mu.Unlock()
				if app != nil {
					app.Quit()
				}
			}()
			return "", fmt.Errorf("创建虚拟网卡要管理员授权。已在系统弹窗里请求，点「是」后云栈会以管理员身份重启并自动连接")
		}
		s.audit("连接隧道", s.engine.Name(), err)
		return "", err
	}
	s.mu.Lock()
	s.connected = true
	s.started = time.Now()
	s.mu.Unlock()
	s.audit("连接隧道", s.engine.Name(), nil)
	// 节点是异步上线的，但 Server.Dial 会等待 Running，所以监听可以先开。
	if err := s.startForwards(); err != nil {
		log.Printf("拉起端口转发失败: %v", err)
	}
	s.startSyncServer()
	return s.GetState()
}

func (s *WeftService) Disconnect() (string, error) {
	s.stopSyncServer()
	s.stopForwards()
	if err := s.engine.Down(); err != nil {
		s.audit("断开隧道", s.engine.Name(), err)
		return "", err
	}
	s.mu.Lock()
	s.connected = false
	s.started = time.Time{}
	s.mu.Unlock()
	s.audit("断开隧道", s.engine.Name(), nil)
	return s.GetState()
}
