package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/secret"
	wgtun "github.com/tailscale/wireguard-go/tun"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

const tunMTU = 1280

// tailnetPrefix 覆盖全部 tailnet 地址，一条路由即可接入所有 peer，
// 不必逐台加 /32。子网路由（subnet router 通告的网段）另外叠加。
const tailnetPrefix = "100.64.0.0/10"

// ErrNeedElevation 表示「当前进程权限不够，必须以管理员身份重启」。
// 只有 Windows 会返回它：WinTun 建卡与写入系统路由都要求进程本身提权。
// 上层据此触发一次 UAC 并以管理员身份重启，而不是把这句话直接甩给用户。
var ErrNeedElevation = errors.New("接管系统网络需要管理员权限")

// osRouter 抽象「接管系统网络」的平台差异：创建 TUN、配接口地址、
// 装路由、退出清理。macOS / Linux 需要 root，实现内部经提权助手完成；
// Windows 要求进程本身已提权。
type osRouter interface {
	// Prepare 创建 TUN 并返回可注入 tsnet 的设备。
	Prepare() (wgtun.Device, error)
	// Configure 给接口配地址并安装路由。
	Configure(dev wgtun.Device, selfIP netip.Addr, routes []netip.Prefix) error
	// Cleanup 删除路由并释放接口，必须可重复调用。
	Cleanup()
}

// systemTUNEngine 把真实 TUN 注入 tsnet：系统网络栈直接收发 100.x 与子网报文，
// 因此外部 SSH / 数据库工具无需任何代理即可访问。
type systemTUNEngine struct {
	mu       sync.Mutex
	srv      *tsnet.Server
	dev      wgtun.Device
	router   osRouter
	cancel   context.CancelFunc
	running  bool
	selfIP   string
	hostname string
	loginURL string
	routes   []string
	skipped  []string
	lastErr  string
	starting bool
	// since 是节点真正上线（拿到地址并装好路由）的时刻。
	// 运行时长以它为准：连接可能是自动拉起的，服务层不一定知道什么时候开始。
	since time.Time
	cfg   model.Config
	proxy *ProxyHub
}

func newSystemTUNEngine() *systemTUNEngine {
	// system 模式下出站走系统栈（已被 TUN 接管），这里不再经 netstack。
	e := &systemTUNEngine{}
	e.proxy = NewProxyHub(func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, network, addr)
	})
	return e
}

func newSystemTUNEngineLegacy() *systemTUNEngine { return &systemTUNEngine{} }

// LastError 返回最近一次启动失败或路由配置失败的原因。
func (e *systemTUNEngine) LastError() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErr
}

// Starting 表示正在建卡/等授权，界面据此显示「连接中」。
// Starting 表示「还没出结果」：要么正在建卡，要么网卡建好了但节点还没上线。
// 只看 starting 是不够的 —— Up() 在 srv.Start() 之后就返回了，而节点要再过几秒
// 才切到 Running，这段空档如果返回 false，界面会先显示「未连接」再跳回「已连接」。
func (e *systemTUNEngine) Starting() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.starting {
		return true
	}
	if e.srv != nil && !e.running && e.lastErr == "" {
		return true
	}
	return false
}

func (e *systemTUNEngine) Name() string    { return "system" }
func (e *systemTUNEngine) Available() bool { return true }

// Since 返回本次上线时刻；没在运行时是零值。
func (e *systemTUNEngine) Since() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.since
}

func (e *systemTUNEngine) Status() (bool, string, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running, e.selfIP, e.hostname
}

func (e *systemTUNEngine) LoginURL() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loginURL
}

func (e *systemTUNEngine) RouteInfo() ([]string, []string, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.routes...), append([]string{}, e.skipped...), e.lastErr
}

// Peers 从本地 netmap 读取对端状态。这是「谁的错」最快的判据：
// 对端 Offline 就与应用无关，别再改客户端。
func (e *systemTUNEngine) Peers() []model.PeerInfo {
	e.mu.Lock()
	srv := e.srv
	e.mu.Unlock()
	if srv == nil {
		return nil
	}
	lc, err := srv.LocalClient()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := lc.Status(ctx)
	if err != nil {
		return nil
	}
	out := make([]model.PeerInfo, 0, len(st.Peer))
	for _, p := range st.Peer {
		ip := ""
		if len(p.TailscaleIPs) > 0 {
			ip = p.TailscaleIPs[0].String()
		}
		routes := ""
		if p.PrimaryRoutes != nil {
			for _, r := range p.PrimaryRoutes.All() {
				if routes != "" {
					routes += ","
				}
				routes += r.String()
			}
		}
		allowed := ""
		if p.AllowedIPs != nil {
			for _, r := range p.AllowedIPs.All() {
				if !r.Addr().Is4() || netip.MustParsePrefix(tailnetPrefix).Contains(r.Addr()) {
					continue
				}
				if allowed != "" {
					allowed += ","
				}
				allowed += r.String()
			}
		}
		last := ""
		if !p.LastSeen.IsZero() {
			last = p.LastSeen.Local().Format("01-02 15:04")
		}
		out = append(out, model.PeerInfo{
			Name: p.HostName, IP: ip, OS: p.OS,
			Online: p.Online, LastSeen: last, Routes: routes, Allowed: allowed,
			Relay: RelayLabel(p.Relay), CurAddr: p.CurAddr,
		})
	}
	return out
}

// ResolveURL 原样返回：系统路由已接管，WebView 与任何程序都能直连 100.x。
// ResolveURL 决定应用窗口最终加载的地址。
//
// WKWebView / WebView2 遇到自签证书会直接拒绝导航（没有"继续访问"的入口），
// 表现就是白屏。开启"信任自签证书"后，把 https 目标改由本地 HTTP 反向代
// 理承载：WebView 只访问 127.0.0.1，TLS 校验在 Go 侧按开关放宽。
func (e *systemTUNEngine) ResolveURL(raw string) (string, error) {
	e.mu.Lock()
	proxy, insecure := e.proxy, e.cfg.AllowInsecure
	e.mu.Unlock()
	if proxy == nil || !insecure {
		return raw, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(raw), "https://") {
		return raw, nil
	}
	return proxy.Resolve(raw)
}

// NetstackDialer 走进程内 userspace 栈，不经过系统路由与 TUN。
// 用它和 Dialer 做对照，就能判断「不通」是 TUN 层还是网络层。
func (e *systemTUNEngine) NetstackDialer() dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		e.mu.Lock()
		srv := e.srv
		e.mu.Unlock()
		if srv == nil {
			return nil, fmt.Errorf("内嵌节点未启动")
		}
		return srv.Dial(ctx, network, addr)
	}
}

// Dialer 用系统标准 dialer：TUN 已经接管路由，出站没必要再绕一层。
func (e *systemTUNEngine) Dialer() dialFunc {
	d := &net.Dialer{Timeout: 20 * time.Second}
	return d.DialContext
}

// ListenTailnet 在节点的 100.x 地址上监听。
//
// 不用 tsnet 自带的 Listen：那个走进程内用户态栈，而这里注入的是真实网卡，
// 入站包直接进内核，把端口绑到网卡地址上才是对的。
// 节点还没上线就先等，最长 90 秒。
func (e *systemTUNEngine) ListenTailnet(port int) (net.Listener, error) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, ip, _ := e.Status(); ip != "" {
			ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
			if err != nil {
				return nil, fmt.Errorf("在 %s:%d 监听失败: %w", ip, port, err)
			}
			return ln, nil
		}
		if msg := e.LastError(); msg != "" {
			return nil, errors.New(msg)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, errors.New("等了 90 秒节点还没上线")
}

func (e *systemTUNEngine) Up(c model.Config) error {
	e.mu.Lock()
	if e.srv != nil {
		e.mu.Unlock()
		return nil
	}
	// 并发点「连接」会各自弹一次授权并抢同一个 socket，必须拦住。
	if e.starting {
		e.mu.Unlock()
		return fmt.Errorf("正在请求管理员授权，请稍候…")
	}
	e.starting = true
	e.cfg = c
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.starting = false
		e.mu.Unlock()
	}()

	dir, err := StateDir(c)
	if err != nil {
		return fmt.Errorf("创建节点状态目录失败: %w", err)
	}
	router := newOSRouter()
	dev, err := router.Prepare()
	if err != nil {
		// 权限不足要让界面看得见：启动时自动连接失败不会有弹窗，
		// 只在总览页留一句「未连接」用户根本不知道差什么。
		if errors.Is(err, ErrNeedElevation) {
			e.mu.Lock()
			e.lastErr = "需要管理员授权才能创建虚拟网卡：点「连接」会请求一次系统授权，或在设置页安装系统助手"
			e.mu.Unlock()
		}
		return err
	}

	host := c.Hostname
	if host == "" {
		host = "weft"
	}
	srv := &tsnet.Server{
		Dir:       dir,
		Hostname:  host,
		AuthKey:   secret.Get(secret.Auth),
		Ephemeral: false,
		Logf:      tsnetLogf,
		UserLogf: func(format string, args ...any) {
			msg := fmt.Sprintf(format, args...)
			log.Printf("[tsnet] %s", msg)
			if u := loginURLRe.FindString(msg); u != "" {
				u = cleanAuthURL(u)
				e.mu.Lock()
				if e.loginURL != u {
					e.loginURL = u
					hlog("需要网页授权：%s", u)
				}
				e.mu.Unlock()
			}
		},
		Tun: dev, // 关键：注入真实网卡，系统流量从这里进出
	}
	if c.ControlPlane == "headscale" {
		if u := trimSlash(c.BaseURL); u != "" {
			srv.ControlURL = u
		}
	}
	if err := srv.Start(); err != nil {
		router.Cleanup()
		return fmt.Errorf("启动内嵌节点失败: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	if e.proxy != nil {
		e.proxy.SetInsecure(c.AllowInsecure)
	}
	e.srv, e.dev, e.router, e.cancel = srv, dev, router, cancel
	e.hostname, e.running, e.loginURL, e.lastErr = host, false, "", ""
	e.since = time.Time{}
	e.mu.Unlock()

	go e.watchAuthURL(ctx, srv)
	go e.waitUp(ctx, srv, dev, router)
	return nil
}

// watchAuthURL 订阅引擎的登录通知，把「需要网页授权」的地址交给界面。
//
// 之前是靠抓日志正则，headscale 自建跑在 http 上时一个都抓不到——链接是
// http://host/register/xxx，正则只写了 https。BrowseToURL 是引擎自己给的，
// 不依赖日志格式，日志正则留作兜底。
func (e *systemTUNEngine) watchAuthURL(ctx context.Context, srv *tsnet.Server) {
	lc, err := srv.LocalClient()
	if err != nil {
		log.Printf("[system] 订阅登录通知失败: %v", err)
		return
	}
	w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		log.Printf("[system] 订阅登录通知失败: %v", err)
		return
	}
	defer w.Close()
	for {
		n, err := w.Next()
		if err != nil {
			return
		}
		if n.BrowseToURL != nil && *n.BrowseToURL != "" {
			u := cleanAuthURL(*n.BrowseToURL)
			e.mu.Lock()
			if e.srv == srv && e.loginURL != u {
				e.loginURL = u
				hlog("需要网页授权：%s", u)
			}
			e.mu.Unlock()
		}
		// 登录完成就开始跑数据面，链接没有意义了
		if n.State != nil && *n.State == ipn.Running {
			e.mu.Lock()
			if e.srv == srv {
				e.loginURL = ""
			}
			e.mu.Unlock()
		}
	}
}

// RequestLogin 主动要一次授权链接。
// tsnet 只在 NeedsLogin 时自己打日志，界面上什么都没出现时，用户点一下
// 「获取授权链接」就能重新触发，不用去猜是服务端没响应还是界面没刷新。
func (e *systemTUNEngine) RequestLogin(ctx context.Context) (string, error) {
	e.mu.Lock()
	srv, cur := e.srv, e.loginURL
	e.mu.Unlock()
	if srv == nil {
		return "", errors.New("还没开始连接，先点「连接」")
	}
	if cur != "" {
		return cur, nil
	}
	lc, err := srv.LocalClient()
	if err != nil {
		return "", fmt.Errorf("读取本地节点失败: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	w, err := lc.WatchIPNBus(cctx, ipn.NotifyInitialState)
	if err != nil {
		return "", fmt.Errorf("订阅登录通知失败: %w", err)
	}
	defer w.Close()
	if err := lc.StartLoginInteractive(cctx); err != nil {
		return "", fmt.Errorf("请求授权链接失败: %w", err)
	}
	for {
		n, err := w.Next()
		if err != nil {
			return "", errors.New("没等到授权链接：服务端没响应，或这个账号已经有节点在等授权")
		}
		if n.BrowseToURL != nil && *n.BrowseToURL != "" {
			u := cleanAuthURL(*n.BrowseToURL)
			e.mu.Lock()
			if e.srv == srv {
				e.loginURL = u
			}
			e.mu.Unlock()
			return u, nil
		}
	}
}

// waitUp 等节点 Running，拿到自身 IP 后再配地址和路由。
// 顺序很重要：TUN 必须先于 Start 注入，但接口地址只有注册成功后才存在。
func (e *systemTUNEngine) waitUp(ctx context.Context, srv *tsnet.Server, dev wgtun.Device, router osRouter) {
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, err := srv.Up(cctx)
		cancel()
		if err != nil {
			log.Printf("[system] 等待上线失败，5 秒后重试: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		ip := netip.Addr{}
		if v4, _ := srv.TailscaleIPs(); v4.IsValid() {
			ip = v4
		}
		if !ip.IsValid() {
			log.Printf("[system] 节点已上线但未分配 IPv4，稍后重试")
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		// 子网路由需要客户端显式接受（等价 --accept-routes），
		// 否则引擎不会把通告的网段纳入转发，系统路由装了也只会
		// 得到 no associated peer node。
		acceptSubnetRoutes(ctx, srv)
		routes := collectRoutes(ctx, srv, dev)
		if err := router.Configure(dev, ip, routes.Keep); err != nil {
			e.mu.Lock()
			e.lastErr = err.Error()
			e.mu.Unlock()
			log.Printf("[system] 配置路由失败: %v", err)
		}
		hlog("路由已安装 %v", prefixStrings(routes.Keep))
		if len(routes.Skipped) > 0 {
			hlog("路由被跳过 %v", routes.Skipped)
		}
		e.mu.Lock()
		if e.srv == srv {
			e.running = true
			e.since = time.Now()
			e.selfIP = ip.String()
			e.loginURL = ""
			e.routes = prefixStrings(routes.Keep)
			e.skipped = routes.Skipped
		}
		e.mu.Unlock()
		MarkConnected(e.cfg) // 上线成功才写标记，失败的自动连接不会反复触发
		// 把服务端下发的对端全部打出来：谁在线、谁通告了什么子网。
		// 「连不上」的第一手判据在这里，不用去后台翻。
		peers := e.Peers()
		hlog("对端共 %d 个（本机 home DERP 见上）", len(peers))
		for _, p := range peers {
			hlog("  对端 %-20s %-15s online=%-5v relay=%-8s 直连=%-18s 可用=%s",
				p.Name, p.IP, p.Online, p.Relay, p.CurAddr, p.Allowed)
		}
		if self, err := srv.LocalClient(); err == nil {
			if st, err := self.Status(ctx); err == nil {
				hlog("本机 DERP=%s 直连=%s", st.Self.Relay, st.Self.CurAddr)
			}
		}
		return
	}
}

type RoutePlan struct {
	Keep    []netip.Prefix
	Skipped []string
}

// collectRoutes 汇总需要装进系统路由表的网段：tailnet 段 + 各 peer 通告的子网路由。
// 与本机直连网段重叠的一律丢弃 —— 否则会把本地局域网流量导进隧道，直接断网。
func collectRoutes(ctx context.Context, srv *tsnet.Server, dev wgtun.Device) RoutePlan {
	base, _ := netip.ParsePrefix(tailnetPrefix)
	all := []netip.Prefix{base}

	if lc, err := srv.LocalClient(); err == nil {
		// 开启 accept-routes 后服务端会重下发 netmap，轮询等它生效。
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		for i := 0; i < 20; i++ {
			st, serr := lc.Status(cctx)
			if serr == nil {
				all = append(all[:1], subnetRoutes(st, base)...)
				if len(all) > 1 {
					break
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
		cancel()
	}

	ifName := ""
	if dev != nil {
		if n, err := dev.Name(); err == nil {
			ifName = n
		}
	}
	local := localDirectPrefixes(ifName)
	local = append(local, systemRoutes(ifName)...)
	return SplitConflicts(all, local)
}

// subnetRoutes 取对端 AllowedIPs 里的 IPv4 子网段（tailnet 段已单独覆盖）。
// 用 AllowedIPs 而不是 PrimaryRoutes：后者含「已通告但未批准」的段。
func subnetRoutes(st *ipnstate.Status, base netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, peer := range st.Peer {
		if peer.AllowedIPs == nil {
			continue
		}
		for _, r := range peer.AllowedIPs.All() {
			if !r.IsValid() || !r.Addr().Is4() || base.Contains(r.Addr()) {
				continue
			}
			out = append(out, r.Masked())
		}
	}
	return out
}

// acceptSubnetRoutes 等价 tailscale up --accept-routes。
// 不开的话引擎不接管通告的子网，系统路由装进去也只会得到
// no associated peer node。
func acceptSubnetRoutes(ctx context.Context, srv *tsnet.Server) {
	lc, err := srv.LocalClient()
	if err != nil {
		hlog("开启子网路由失败: %v", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := lc.EditPrefs(cctx, &ipn.MaskedPrefs{
		Prefs:       ipn.Prefs{RouteAll: true},
		RouteAllSet: true,
	})
	if err != nil {
		hlog("开启子网路由失败: %v", err)
		return
	}
	hlog("子网路由已接受 accept-routes=%v", p.RouteAll)
}

func SplitConflicts(routes []netip.Prefix, local []LocalRoute) RoutePlan {
	plan := RoutePlan{Keep: make([]netip.Prefix, 0, len(routes))}
	seen := map[netip.Prefix]bool{}
	for _, r := range routes {
		if seen[r] {
			continue
		}
		seen[r] = true

		// 只有「本机已存在完全相同的前缀」才是真冲突：那会覆盖原有路由，
		// 且原路由不会自动恢复。前缀长度不同不算冲突——系统按最长前缀匹配，
		// 本机已直连的更具体网段仍然优先，tailnet 通告的段照样可用。
		conflictWith := ""
		for _, l := range local {
			if l.Prefix == r.Masked() {
				conflictWith = l.String()
				break
			}
		}
		if conflictWith != "" {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("%s（已被本机 %s 占用）", r, conflictWith))
			continue
		}
		plan.Keep = append(plan.Keep, r)
	}
	return plan
}

// LocalRoute 是本机已直连的网段，带上网卡名，冲突时可以告诉用户是谁占的。
type LocalRoute struct {
	Prefix netip.Prefix
	Iface  string
}

// String 给跳过原因用：让用户知道是哪个网卡占了这段。

func (l LocalRoute) String() string {
	return fmt.Sprintf("%s (%s)", l.Prefix, l.Iface)
}

// localDirectPrefixes 列出本机已直连的网段（不含我们自己的 TUN）。
func localDirectPrefixes(exceptIface string) []LocalRoute {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []LocalRoute
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if exceptIface != "" && ifi.Name == exceptIface {
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
			p, err := netip.ParsePrefix(ipnet.String())
			if err != nil || !p.Addr().Is4() {
				continue
			}
			out = append(out, LocalRoute{Prefix: p.Masked(), Iface: ifi.Name})
		}
	}
	return out
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

func (e *systemTUNEngine) Down() error {
	e.mu.Lock()
	srv, router, cancel := e.srv, e.router, e.cancel
	e.srv, e.dev, e.router, e.cancel = nil, nil, nil, nil
	e.running, e.selfIP, e.loginURL = false, "", ""
	e.routes, e.skipped, e.since = nil, nil, time.Time{}
	proxy := e.proxy
	e.mu.Unlock()

	if proxy != nil {
		proxy.CloseAll()
	}
	if router != nil {
		router.Cleanup() // 先撤路由，再关网卡，避免残留指向已消失接口的路由
	}
	if cancel != nil {
		cancel()
	}
	if srv != nil {
		return srv.Close()
	}
	return nil
}

func (e *systemTUNEngine) Shutdown() { _ = e.Down() }

func hlog(format string, args ...any) {
	log.Printf("[tunnel] "+format, args...)
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// DiagTarget 是一个探测目标：子网服务通常不在 22 端口，需要带端口。
type DiagTarget struct {
	IP   netip.Addr
	Port uint16
}

// DiagDump 把「包卡在哪一层」用数据说清楚。
// 对端的三层状态（netmap / magicsock / engine）是关键：
// 任意一层为 false，包就发不出去，而系统路由看起来完全正常。
func (e *systemTUNEngine) DiagDump(ctx context.Context, targets []DiagTarget) []string {
	e.mu.Lock()
	srv := e.srv
	e.mu.Unlock()
	if srv == nil {
		return []string{"内嵌节点未启动（引擎未运行）"}
	}
	lc, err := srv.LocalClient()
	if err != nil {
		return []string{"LocalClient 失败: " + err.Error()}
	}
	var out []string

	// 自身 IP 就绪时 netmap 可能还没下发完对端，先等 peer 列表非空，
	// 否则 disco 探测会报 no matching peer，看起来像故障。
	var st *ipnstate.Status
	for i := 0; i < 30; i++ {
		var serr error
		st, serr = lc.Status(ctx)
		if serr != nil {
			return []string{"Status 失败: " + serr.Error()}
		}
		if len(st.Peer) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	out = append(out, fmt.Sprintf("本机 self: ip=%v relay=%q curAddr=%q tun=%v backend=%s",
		st.Self.TailscaleIPs, st.Self.Relay, st.Self.CurAddr, st.TUN, st.BackendState))
	for _, h := range st.Health {
		out = append(out, "health: "+h)
	}
	if pr, perr := lc.GetPrefs(ctx); perr == nil {
		out = append(out, fmt.Sprintf("accept-routes=%v（子网路由是否被引擎接管）", pr.RouteAll))
	}

	type row struct {
		ip   string
		line string
	}
	var rows []row
	for _, p := range st.Peer {
		ip := ""
		if len(p.TailscaleIPs) > 0 {
			ip = p.TailscaleIPs[0].String()
		}
		allowed := ""
		if p.AllowedIPs != nil {
			for _, r := range p.AllowedIPs.All() {
				if allowed != "" {
					allowed += ","
				}
				allowed += r.String()
			}
		}
		hs := "never"
		if !p.LastHandshake.IsZero() {
			hs = time.Since(p.LastHandshake).Round(time.Second).String() + " ago"
		}
		// Cap 是服务端给该节点的能力版本号：>=152 时本机才会用 hybrid
		// 后量子握手，对端若不支持会静默丢弃，表现为「发出去没有回应」。
		capv, ver := -1, "?"
		if node, nerr := lc.PeerByID(ctx, p.NodeID); nerr == nil && node != nil {
			capv, ver = int(node.Cap), node.Hostinfo.IPNVersion()
		}
		rows = append(rows, row{ip: ip, line: fmt.Sprintf(
			"%-15s %-18s online=%-5v relay=%-9q 直连=%-20q hs=%-12s tx=%-8d rx=%-8d cap=%-4d 对端版本=%-22s 可用=%s",
			ip, p.HostName, p.Online, p.Relay, p.CurAddr, hs, p.TxBytes, p.RxBytes,
			capv, ver, allowed)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ip < rows[j].ip })
	out = append(out, fmt.Sprintf("对端 %d 个（三层 = 在netmap/在magicsock/在engine）:", len(rows)))
	for _, r := range rows {
		out = append(out, "  "+r.line)
	}

	// disco ping 完全走 magicsock+DERP，不碰 wireguard 和 IP 层。
	// 它通而 TCP 不通，就说明问题在 wireguard 这一层。
	for _, t := range targets {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		pr, perr := lc.Ping(cctx, t.IP, tailcfg.PingDisco)
		cancel()
		if perr != nil {
			out = append(out, fmt.Sprintf("disco %s: 失败 %v", t.IP, perr))
			continue
		}
		out = append(out, fmt.Sprintf("disco %s: node=%s err=%q 延迟=%.0fms endpoint=%q derp=%s/%d",
			t.IP, pr.NodeName, pr.Err, pr.LatencySeconds*1000, pr.Endpoint, pr.DERPRegionCode, pr.DERPRegionID))
	}

	// 包过滤规则：ACL 没放行时表现为「包发出去但没回应」。
	if rules, rerr := lc.DebugPacketFilterRules(ctx); rerr == nil {
		out = append(out, fmt.Sprintf("包过滤规则 %d 条:", len(rules)))
		for _, r := range rules {
			dsts := make([]string, 0, len(r.DstPorts))
			for _, d := range r.DstPorts {
				dsts = append(dsts, fmt.Sprintf("%s:%d-%d", d.IP, d.Ports.First, d.Ports.Last))
			}
			out = append(out, fmt.Sprintf("  src=%v proto=%v dst=%v", r.SrcIPs, r.IPProto, dsts))
		}
	}

	// 两条通道分别拨一次 TCP：TUN 走系统栈，netstack 走进程内栈。
	for _, t := range targets {
		out = append(out, e.dialProbe(ctx, t, "系统 TUN", func(c context.Context, addr string) (net.Conn, error) {
			d := &net.Dialer{Timeout: 12 * time.Second}
			return d.DialContext(c, "tcp", addr)
		}))
		out = append(out, e.dialProbe(ctx, t, "netstack", func(c context.Context, addr string) (net.Conn, error) {
			return srv.Dial(c, "tcp", addr)
		}))
	}

	// 探完再看一次握手统计：hs 有值且 tx/rx 都增长，才算真的通了。
	if after, aerr := lc.Status(ctx); aerr == nil {
		out = append(out, "探测后的握手统计:")
		for _, t := range targets {
			for _, p := range after.Peer {
				if len(p.TailscaleIPs) == 0 || p.TailscaleIPs[0] != t.IP {
					continue
				}
				hs := "never"
				if !p.LastHandshake.IsZero() {
					hs = time.Since(p.LastHandshake).Round(time.Second).String() + " ago"
				}
				out = append(out, fmt.Sprintf("  %s %s hs=%s tx=%d rx=%d",
					t.IP, p.HostName, hs, p.TxBytes, p.RxBytes))
			}
		}
	}
	return out
}

// DiagPeer 实测一台对端：握手快照、disco 探测、ACL 放行判断、TCP 拨测。
// 顺序是有讲究的——先读状态，再探链路，最后拨目标端口，
// 这样结论里能分清是「链路不通」还是「链路通了但服务/ACL 挡着」。
// Snapshot 采一份本机状态，给自检用。
//
// 它跟 DiagPeer 的分工：DiagPeer 回答「能不能到对端」，Snapshot 回答
// 「我自己准备好了没有」。所以引擎没起来时它不返回错误——「引擎没起来」
// 本身就是自检要报告的那条结论，这时候用户最需要看这一页。
func (e *systemTUNEngine) Snapshot(ctx context.Context) (model.SelfReport, error) {
	e.mu.Lock()
	srv, dev, running := e.srv, e.dev, e.running
	selfIP, host := e.selfIP, e.hostname
	e.mu.Unlock()

	rep := model.SelfReport{At: time.Now().Format(time.RFC3339), SelfIP: selfIP, Hostname: host}
	if dev != nil {
		if n, err := dev.Name(); err == nil {
			rep.TUN = n
		}
	}
	if srv == nil {
		rep.Backend = "未启动"
		return rep, nil
	}

	lc, err := srv.LocalClient()
	if err != nil {
		rep.Backend = "未启动"
		return rep, err
	}
	st, err := lc.Status(ctx)
	if err != nil {
		rep.Backend = "读不到状态"
		return rep, err
	}
	rep.Backend = st.BackendState
	// AuthURL 非空表示服务端给了个链接在等授权，这时不算登录成功
	rep.LoggedIn = st.AuthURL == "" && st.BackendState != "NeedsLogin" &&
		st.BackendState != "NeedsMachineAuth"
	rep.Health = st.Health
	if st.Self != nil {
		rep.Relay = RelayLabel(st.Self.Relay)
		rep.Direct = st.Self.CurAddr != ""
		if len(st.Self.TailscaleIPs) > 0 {
			rep.SelfIP = st.Self.TailscaleIPs[0].String()
		}
	}
	if !running {
		// 后端在跑但路由没配完时，流量的去向和「已连接」不是一回事
		rep.Backend = st.BackendState + "（路由未就绪）"
	}
	if pr, perr := lc.GetPrefs(ctx); perr == nil {
		rep.AcceptRoutes = pr.RouteAll
	}

	// 本机 netmap 里已经接受的子网路由。没批准的通告不会出现在这里，
	// 要靠管理 Key 才能补上，那一步在 SelfCheck 里做。
	for _, p := range st.Peer {
		if p.PrimaryRoutes == nil {
			continue
		}
		ip := ""
		if len(p.TailscaleIPs) > 0 {
			ip = p.TailscaleIPs[0].String()
		}
		for _, r := range p.PrimaryRoutes.All() {
			rep.Routes = append(rep.Routes, model.RouteCheck{
				Prefix: r.String(), Peer: p.HostName, PeerIP: ip,
				Accepted: true, PeerOnline: p.Online,
			})
		}
	}
	sort.Slice(rep.Routes, func(i, j int) bool {
		return rep.Routes[i].Prefix < rep.Routes[j].Prefix
	})
	return rep, nil
}

func (e *systemTUNEngine) DiagPeer(ctx context.Context, ip netip.Addr, port uint16) (model.DiagReport, error) {
	e.mu.Lock()
	srv := e.srv
	selfIP := e.selfIP
	running := e.running
	e.mu.Unlock()
	rep := model.DiagReport{IP: ip.String(), Port: int(port), PingMs: -1, PingWorst: -1}
	rep.At = time.Now().Format(time.RFC3339)
	if srv == nil || !running {
		return rep, errors.New("还没连上，先在总览页连接")
	}
	lc, err := srv.LocalClient()
	if err != nil {
		return rep, fmt.Errorf("读取本地节点状态失败: %w", err)
	}
	st, err := lc.Status(ctx)
	if err != nil {
		return rep, fmt.Errorf("读取节点状态失败: %w", err)
	}

	var peer *ipnstate.PeerStatus
	for _, p := range st.Peer {
		if len(p.TailscaleIPs) > 0 && p.TailscaleIPs[0].Unmap() == ip {
			peer = p
			break
		}
	}
	if peer == nil {
		rep.Verdict = "netmap 里没有这台设备"
		rep.Hints = []string{
			"服务端还没把对端下发过来，等几秒再试",
			"对端可能已经从这个服务端退出，或者换了账号",
		}
		return rep, nil
	}

	rep.Name = peer.HostName
	rep.Online = peer.Online
	rep.Relay = RelayLabel(peer.Relay)
	rep.CurAddr = peer.CurAddr
	// 这里原来还塞了 peer.TxBytes / RxBytes。那是这台设备开机以来的
	// 累计流量，摆在一个「三次探测 + 一次拨测」的报告里，等于告诉用户
	// 「3 ms 的 ping 传了 39 KB」。诊断只报这次测出来的东西。
	rep.Path = "DERP"
	if peer.CurAddr != "" {
		rep.Path = "直连"
	}
	rep.Handshake = "从未"
	if !peer.LastHandshake.IsZero() {
		rep.Handshake = fmt.Sprint(time.Since(peer.LastHandshake).Round(time.Second)) + " 前"
	}
	// 对端版本只用于解释「版本差太多导致静默丢包」，拿不到就留空
	if node, nerr := lc.PeerByID(ctx, peer.NodeID); nerr == nil && node != nil {
		rep.Version = node.Hostinfo.IPNVersion()
	}

	// 连发三次 disco 探测。一次通可能只是刚好赶上，三次能看出是否稳定。
	for i := 0; i < 3; i++ {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		pr, perr := lc.Ping(cctx, ip, tailcfg.PingDisco)
		cancel()
		if perr != nil || pr == nil || pr.Err != "" {
			rep.PingLost++
			if rep.PingErr == "" {
				switch {
				case perr != nil:
					rep.PingErr = perr.Error()
				case pr != nil && pr.Err != "":
					rep.PingErr = pr.Err
				default:
					rep.PingErr = "没有回应"
				}
			}
			continue
		}
		ms := pr.LatencySeconds * 1000
		if rep.PingMs < 0 || ms < rep.PingMs {
			rep.PingMs = ms
		}
		if ms > rep.PingWorst {
			rep.PingWorst = ms
		}
	}

	// 包过滤规则是 ACL 在客户端的落地结果。只能回答「规则里有没有这一条」，
	// 算不出来的写法（group:/autogroup:admin）返回 unknown，不伪装成「没放行」。
	var selfTags []string
	if st.Self != nil && st.Self.Tags != nil {
		selfTags = st.Self.Tags.AsSlice()
	}
	if self, serr := netip.ParseAddr(selfIP); serr == nil {
		if rules, rerr := lc.DebugPacketFilterRules(ctx); rerr == nil {
			rep.ACLChecked = true
			v := AclEvaluate(rules, self.Unmap(), ip.Unmap(), port, selfTags)
			rep.ACLState, rep.ACLNote = v.State, v.Note
		}
	}

	// TCP 拨测走系统栈，和外部 SSH / 数据库工具走的是同一条路。
	d := &net.Dialer{Timeout: 8 * time.Second}
	addr := net.JoinHostPort(ip.String(), fmt.Sprint(port))
	start := time.Now()
	conn, derr := d.DialContext(ctx, "tcp", addr)
	rep.DialMs = float64(time.Since(start).Microseconds()) / 1000
	if derr != nil {
		rep.DialErr = derr.Error()
		rep.DialCode = DialCode(derr)
	} else {
		_ = conn.Close()
	}

	rep.Verdict, rep.Hints = DiagVerdict(rep)
	return rep, nil
}

func (e *systemTUNEngine) dialProbe(ctx context.Context, t DiagTarget, label string,
	dial func(context.Context, string) (net.Conn, error)) string {
	// 冷启动首个连接要等 wireguard 握手（重试间隔 5 秒），给足余量。
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	addr := net.JoinHostPort(t.IP.String(), fmt.Sprint(t.Port))
	start := time.Now()
	conn, err := dial(cctx, addr)
	if err != nil {
		return fmt.Sprintf("TCP %s 经 %s 失败（%v）: %v",
			addr, label, time.Since(start).Round(time.Millisecond), err)
	}
	_ = conn.Close()
	return fmt.Sprintf("TCP %s 经 %s 成功（%v）", addr, label, time.Since(start).Round(time.Millisecond))
}
