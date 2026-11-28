package model

import (
	"context"
	"net"
	"os"
	"strings"
)

// 所有 Service 方法统一以 JSON 字符串交换数据，避免绑定生成器对结构体字段的
// 命名差异，前端拿到后 JSON.parse 即可。

type Config struct {
	// ControlPlane 仅保留字段兼容旧配置，固定为 headscale。
	ControlPlane    string `json:"controlPlane"`
	BaseURL         string `json:"baseURL"`
	Tailnet         string `json:"tailnet"`
	AuthMode        string `json:"authMode"`      // apikey | oauth
	APIStyle        string `json:"apiStyle"`      // auto | v1 | v2
	TunnelEngine    string `json:"tunnelEngine"`  // 已固定为 system，保留字段只为兼容旧配置
	Hostname        string `json:"hostname"`      // 内嵌节点在 tailnet 里的名字
	AllowInsecure   bool   `json:"allowInsecure"` // 信任内网应用自签 HTTPS 证书
	AuthKey         string `json:"authKey"`       // 隧道注册用，存钥匙串
	AutoConnect     bool   `json:"autoConnect"`
	AutoReconnect   bool   `json:"autoReconnect"`
	MinimizeToTray  bool   `json:"minimizeToTray"`
	Demo            bool   `json:"demo"`            // 已移除，恒为 false，保留字段只为兼容旧配置
	DevTools        bool   `json:"devTools"`        // 允许应用窗口调试（F12）
	SocksEnabled    bool   `json:"socksEnabled"`    // 通用 SOCKS5 代理
	SocksPort       int    `json:"socksPort"`       // 默认 1080
	ForwardBind     string `json:"forwardBind"`     // 本地监听地址：127.0.0.1（仅本机）| 0.0.0.0（同网段可达）
	AutoForward     bool   `json:"autoForward"`     // 隧道起来后自动拉起已启用的端口映射
	CACert          string `json:"caCert"`          // 服务端自签证书的 CA（PEM），填了只认它
	ControlInsecure bool   `json:"controlInsecure"` // 没给 CA 时跳过服务端证书校验
	SyncEnabled     bool   `json:"syncEnabled"`     // 允许同 tailnet 的其他设备拉取应用书签
	SyncPort        int    `json:"syncPort"`        // 同步端口，默认 4160

	// 多服务端：Profiles 存着每个 Headscale 的连接信息，顶层的 BaseURL/凭证等
	// 始终是「当前激活那个」的副本，这样其余代码不用关心多套配置。
	Profiles []Profile `json:"profiles"`
	ActiveID string    `json:"activeID"`
	// ProfileID 是运行时用的，不落库：决定 tsnet 状态目录和钥匙串条目。
	ProfileID string `json:"-"`
}

// Profile 是一个 Headscale 服务端的连接配置。
type Profile struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ControlPlane  string `json:"controlPlane"`
	BaseURL       string `json:"baseURL"`
	Tailnet       string `json:"tailnet"`
	AuthMode      string `json:"authMode"`
	AllowInsecure bool   `json:"allowInsecure"`
	Hostname      string `json:"hostname"`
	// 服务端是自签证书时的两选一：给 CA，或者干脆不校验。
	CACert          string `json:"caCert"`
	ControlInsecure bool   `json:"controlInsecure"`
}

type Capabilities struct {
	Version          string `json:"version"`
	APIStyle         string `json:"apiStyle"`
	CanCreateUser    bool   `json:"canCreateUser"`
	CanManageAPIKeys bool   `json:"canManageAPIKeys"`
	CanManageACL     bool   `json:"canManageACL"`
	CanManageOAuth   bool   `json:"canManageOAuth"`
	Database         bool   `json:"database"` // 服务端数据库连通性
	ProbeMessage     string `json:"probeMessage"`
}

type State struct {
	Connected    bool       `json:"connected"`
	Connecting   bool       `json:"connecting"` // 正在建卡 / 等管理员授权
	Mode         string     `json:"mode"`       // demo | live
	ControlPlane string     `json:"controlPlane"`
	Endpoint     string     `json:"endpoint"`
	SelfIP       string     `json:"selfIP"`
	Hostname     string     `json:"hostname"`
	Engine       string     `json:"engine"`
	EngineReady  bool       `json:"engineReady"`
	LoginURL     string     `json:"loginURL"`   // 首次交互式注册时由 tsnet 给出
	LastError    string     `json:"lastError"`  // 最近一次启动/配置路由失败的原因
	Platform     string     `json:"platform"`   // darwin / windows / linux，界面按它选文案
	AppVersion   string     `json:"appVersion"` // 客户端版本，界面不再硬编码
	DataDir      string     `json:"dataDir"`    // 配置与数据库实际所在目录
	Routes       []string   `json:"routes"`     // 已安装的系统路由（system 模式）
	Peers        []PeerInfo `json:"peers"`      // 来自本地 netmap，无需 API Key
	Skipped      []string   `json:"skipped"`    // 因与本地网段冲突被跳过的路由
	NetErr       string     `json:"netErr"`     // 路由配置错误
	CLIReady     bool       `json:"cliReady"`   // 兼容旧前端：等价于 EngineReady
	Configured   bool       `json:"configured"` // 是否已填服务端凭证
	HasAPIKey    bool       `json:"hasApiKey"`  // 是否配了能用的管理 API Key（不是节点 Auth Key）
	// APIKeyIssue 是「为什么不算配了」：非空时界面要把它显示出来，
	// 否则入口突然消失会让人以为软件坏了
	APIKeyIssue  string       `json:"apiKeyIssue"`
	Uptime       string       `json:"uptime"`
	Message      string       `json:"message"`
	Capabilities Capabilities `json:"capabilities"`
}

type Device struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`     // 优先用 givenName
	Hostname  string   `json:"hostname"` // 机器上报的原始名称
	GivenName string   `json:"givenName"`
	User      string   `json:"user"`
	UserID    string   `json:"userId"`
	IP        string   `json:"ip"`
	IPs       []string `json:"ips"`
	Tags      string   `json:"tags"`
	LastSeen  string   `json:"lastSeen"`
	CreatedAt string   `json:"createdAt"`
	Online    bool     `json:"online"`
	Status    string   `json:"status"` // 在线 / 离线 / 已过期
	// 子网路由：available=通告、approved=已批准、subnet=生效中
	AdvertisedRoutes string `json:"advertisedRoutes"`
	ApprovedRoutes   string `json:"approvedRoutes"`
	SubnetRoutes     string `json:"subnetRoutes"`
	Expiry           string `json:"expiry"`
	RegisterMethod   string `json:"registerMethod"`
	MachineKey       string `json:"machineKey"`
	NodeKey          string `json:"nodeKey"`
	DiscoKey         string `json:"discoKey"`
}

type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"` // 展示名（displayName 优先）
	RawName     string `json:"rawName"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Provider    string `json:"provider"`
	PictureURL  string `json:"pictureUrl"`
	CreatedAt   string `json:"createdAt"`
}

type AuthKey struct {
	ID        string `json:"id"`
	Key       string `json:"key"` // 列表里是掩码，创建时才是明文
	User      string `json:"user"`
	Reusable  bool   `json:"reusable"`
	Ephemeral bool   `json:"ephemeral"`
	Used      bool   `json:"used"`
	Expires   string `json:"expires"`
	Created   string `json:"created"`
	ACLTags   string `json:"aclTags"`
	Status    string `json:"status"`
}

type APIKey struct {
	ID        string `json:"id"`
	Prefix    string `json:"prefix"`
	IsCurrent bool   `json:"isCurrent"` // 是否就是当前正在用的这把
	Expires   string `json:"expires"`
	Created   string `json:"created"`
	LastSeen  string `json:"lastSeen"`
	Status    string `json:"status"`
}

// HealthInfo 服务端健康状态。
type HealthInfo struct {
	Database bool `json:"database"`
}

// Policy 是 ACL 策略文档，Headscale v1 只有内容与更新时间，没有 ETag。
// PolicyVersion 是策略历史里的一版。内容单取，列表只带摘要。
type PolicyVersion struct {
	ID    int64  `json:"id"`
	At    string `json:"at"`
	Size  int    `json:"size"`
	Lines int    `json:"lines"`
}

type Policy struct {
	Content   string `json:"content"`
	UpdatedAt string `json:"updatedAt"`
	// live 是刚从服务端读到的；cache 表示服务端不通，内容是本地缓存，
	// UpdatedAt 也就是缓存的写入时间
	Source string `json:"source"`
}

type AppBookmark struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Group string `json:"group"`
	Icon  string `json:"icon"`
	// Browser 为 true 时始终用系统浏览器打开：
	// 需要浏览器扩展（如密码管理器自动填充）的站点走这条路。
	Browser bool `json:"browser"`
	// UpdatedAt 用于设备间同步时判断哪边的改动更新，RFC3339。
	UpdatedAt string `json:"updatedAt"`
}

// SyncInfo 是应用同步的当前状态，给本机界面看。
type SyncInfo struct {
	Enabled   bool   `json:"enabled"`
	Port      int    `json:"port"`
	Token     string `json:"token"` // 同步码，明文只在本机界面出现
	Listening bool   `json:"listening"`
	SelfIP    string `json:"selfIP"`
	Hostname  string `json:"hostname"`
	Count     int    `json:"count"`
	Error     string `json:"error"`
}

// SyncResult 是一次拉取或推送的结果。
type SyncResult struct {
	Peer    string `json:"peer"`
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Skipped int    `json:"skipped"`
	Total   int    `json:"total"`
}

// ForwardRule 一条固定本地端口映射。LocalPort 持久化，重启 App 不变，
// 外部 SSH / 数据库工具里保存的连接配置因此不会失效。
// Kind 只影响 UI 图标和默认生成的命令片段，转发层本身不解析协议。
type ForwardRule struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`   // ssh | mysql | postgres | redis | rdp | custom
	Target     string `json:"target"` // 100.x IP 或 MagicDNS 名称
	TargetPort int    `json:"targetPort"`
	LocalPort  int    `json:"localPort"`
	Enabled    bool   `json:"enabled"`
}

type ForwardStatus struct {
	ID         string `json:"id"`
	State      string `json:"state"` // listening | stopped | disabled | error
	Active     int64  `json:"active"`
	Bytes      int64  `json:"bytes"`
	Error      string `json:"error"`
	ListenAddr string `json:"listenAddr"`
}

// DiagReport 是一次连通性检查的结果。界面只负责把它摆出来，
// 结论和可能原因都在后端算好：同一份字要给 CLI 和 GUI 用。
type DiagReport struct {
	IP   string `json:"ip"`
	Name string `json:"name"`
	Port int    `json:"port"`

	Online    bool   `json:"online"`
	Path      string `json:"path"`      // 直连 / DERP / 未知
	Relay     string `json:"relay"`     // 当前用的 DERP 区域
	CurAddr   string `json:"curAddr"`   // 直连地址，空表示全靠 DERP
	Handshake string `json:"handshake"` // 最近一次握手，多久以前
	Version   string `json:"version"`

	// disco 探测：走 magicsock，不碰 wireguard 与 IP 层。
	// 通了说明对端可达、密钥协商没问题；不通就是链路本身的事，跟服务和 ACL 无关。
	PingMs    float64 `json:"pingMs"`    // 最快一次，-1 表示一次都没通
	PingWorst float64 `json:"pingWorst"` // 最慢一次，抖动大说明链路不稳
	PingLost  int     `json:"pingLost"`  // 3 次里丢了几次
	PingErr   string  `json:"pingErr"`

	// TCP 拨测：真的连一次目标端口。通了才算「能用」。
	DialMs  float64 `json:"dialMs"`
	DialErr string  `json:"dialErr"`
	// DialCode 是拨测失败的分类，比错误文案可靠：
	// refused 表示对端回了 RST（包到了，ACL 一定放行）；timeout 才是「可能被丢」；
	// noroute 是本机路由没装上，跟 ACL 和对端都无关。
	DialCode string `json:"dialCode"`

	// ACL 判定。三态，不是布尔：读到的规则里可能写着 autogroup:/tag:/group:，
	// 客户端算不出来，这种情况必须说「判不了」，不能默认成「没放行」。
	ACLChecked bool   `json:"aclChecked"`
	ACLState   string `json:"aclState"` // allow / deny / unknown，空表示没读到规则
	ACLNote    string `json:"aclNote"`  // 判不了或被实测推翻时，说明原因

	Verdict string   `json:"verdict"` // 一句话结论
	Hints   []string `json:"hints"`   // 不通时按可能性排序的原因
	At      string   `json:"at"`
}

// SelfReport 是本机自检：不看对端，只回答「我这台机器准备好了没有」。
// 连不上的时候最需要它，所以引擎没起来时也要能出结果，只是字段少一些。
type SelfReport struct {
	At       string `json:"at"`
	Engine   string `json:"engine"`
	Backend  string `json:"backend"` // Running / NeedsLogin / Stopped / 未启动
	LoggedIn bool   `json:"loggedIn"`
	TUN      string `json:"tun"` // 接口名，空表示没建起来
	SelfIP   string `json:"selfIP"`
	Hostname string `json:"hostname"`
	Relay    string `json:"relay"`
	Direct   bool   `json:"direct"` // 是否已打通直连，false 表示只走 DERP
	// AcceptRoutes 是引擎是否接管别人通告的子网。关着的时候，
	// 服务端批准了也没用，表现就是「内网怎么都访问不了」。
	AcceptRoutes bool `json:"acceptRoutes"`

	Control ControlStat `json:"control"`

	// Health 是引擎自己的健康告警（UDP 被挡、DERP 不可达、路由冲突都在里面）。
	// 原样透传，不翻译：这些字是上游给出的最准的说法。
	Health   []string     `json:"health"`
	Problems []string     `json:"problems"` // 自检发现的、会直接影响使用的问题
	Verdict  string       `json:"verdict"`
	Tone     string       `json:"tone"` // ok | warn | bad
	Routes   []RouteCheck `json:"routes"`
}

// ControlStat 是服务端那一次探测的结果。
type ControlStat struct {
	URL     string  `json:"url"`
	OK      bool    `json:"ok"`
	Ms      float64 `json:"ms"`
	Version string  `json:"version"`
	Err     string  `json:"err"`
}

// RouteCheck 是一条子网路由走完四步的状态：通告 → 批准 → 接受 → 可达。
// 任何一步断了，症状都是「访问不了内网」，所以四步要分开看。
// 通告与批准只有配了管理 Key 才看得到（Known 为 false 时这两项无意义）。
type RouteCheck struct {
	Prefix     string `json:"prefix"`
	Peer       string `json:"peer"`
	PeerIP     string `json:"peerIP"`
	Known      bool   `json:"known"`
	Advertised bool   `json:"advertised"`
	Approved   bool   `json:"approved"`
	Accepted   bool   `json:"accepted"`
	PeerOnline bool   `json:"peerOnline"`
	Verdict    string `json:"verdict"`
	Tone       string `json:"tone"`
}

// PeerPort 是「这台设备上登记过的服务」，来自应用书签和端口转发规则。
// 检查端口默认不再是全局的 22，而是这些真在用着的端口。
type PeerPort struct {
	IP    string `json:"ip"`
	Port  int    `json:"port"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // app | forward
}

// PeerInfo 是节点自己从服务端同步下来的对端视图，不需要任何 API Key。
// 用来判断「连不上」到底是本机问题还是对端离线。
type PeerInfo struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	OS       string `json:"os"`
	Online   bool   `json:"online"`
	LastSeen string `json:"lastSeen"`
	Routes   string `json:"routes"`  // 通告的路由（可能未批准）
	Allowed  string `json:"allowed"` // 实际放行的路由，只有这些能转发
	Relay    string `json:"relay"`   // 对端当前用的 DERP region，与本机不一致就中继不到
	CurAddr  string `json:"curAddr"` // 已打通的直连地址，空表示全靠 DERP
}

// Cached 是列表接口的统一返回：真实数据 + 来源标记。
// source=cache 表示服务端请求失败，返回的是 SQLite 里的上次同步结果。
type Cached[T any] struct {
	Items     []T    `json:"items"`
	Source    string `json:"source"` // live | cache | none
	UpdatedAt string `json:"updatedAt"`
	Error     string `json:"error,omitempty"`
}

// HelperStatus 报告系统提权助手的状态。装好常驻助手后，连接不再弹管理员授权。
type HelperStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Socket    string `json:"socket"`
	Version   string `json:"version"`
	Note      string `json:"note"`
	LogTail   string `json:"logTail"` // 装了但没跑时附上助手日志尾部，便于定位
}

// ForwardView 是端口转发页的一次性数据，前端不必再拼装状态。
type ForwardView struct {
	Rules        []ForwardRule   `json:"rules"`
	Statuses     []ForwardStatus `json:"statuses"`
	Socks        ForwardStatus   `json:"socks"`
	SocksEnabled bool            `json:"socksEnabled"`
	SocksPort    int             `json:"socksPort"`
	AutoForward  bool            `json:"autoForward"`
	Power        bool            `json:"power"`
	Bind         string          `json:"bind"`
	Engine       string          `json:"engine"`
}

type AuditEvent struct {
	Time string `json:"time"`
	// Action 是做了什么，Target 是对谁做的；Detail 给老界面用，
	// 新记录里它等于 Target，老记录里还是当年的那句话。
	Action  string `json:"action"`
	Detail  string `json:"detail"`
	Profile string `json:"profile"`
	Target  string `json:"target"`
	// ok / fail。失败的记录带服务端或本机的错误原文。
	Result string `json:"result"`
	Error  string `json:"error"`
}

// defaultHostname 默认拿本机主机名当节点名，装完不用想名字就能连。
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

// DialFunc 是统一的拨号签名：引擎、端口转发、本地代理共用。
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// IsClosedErr 判断错误是不是「连接已经关了」这类预期内的噪音。
// 端口转发与助手守护进程都要在关闭时静默退出。
func IsClosedErr(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "use of closed network connection") ||
		strings.Contains(err.Error(), "closed"))
}
