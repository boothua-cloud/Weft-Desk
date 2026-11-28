package main

// 所有 Service 方法统一以 JSON 字符串交换数据，避免绑定生成器对结构体字段的
// 命名差异，前端拿到后 JSON.parse 即可。

type Config struct {
	// ControlPlane 仅保留字段兼容旧配置，固定为 headscale。
	ControlPlane   string `json:"controlPlane"`
	BaseURL        string `json:"baseURL"`
	Tailnet        string `json:"tailnet"`
	AuthMode       string `json:"authMode"`      // apikey | oauth
	APIStyle       string `json:"apiStyle"`      // auto | v1 | v2
	TunnelEngine   string `json:"tunnelEngine"`  // 已固定为 system，保留字段只为兼容旧配置
	Hostname       string `json:"hostname"`      // 内嵌节点在 tailnet 里的名字
	AllowInsecure  bool   `json:"allowInsecure"` // 信任内网应用自签 HTTPS 证书
	AuthKey        string `json:"authKey"`       // 隧道注册用，存钥匙串
	AutoConnect    bool   `json:"autoConnect"`
	AutoReconnect  bool   `json:"autoReconnect"`
	MinimizeToTray bool   `json:"minimizeToTray"`
	Demo           bool   `json:"demo"`         // 已移除，恒为 false，保留字段只为兼容旧配置
	DevTools       bool   `json:"devTools"`     // 允许应用窗口调试（F12）
	SocksEnabled   bool   `json:"socksEnabled"` // 通用 SOCKS5 代理
	SocksPort      int    `json:"socksPort"`    // 默认 1080
	ForwardBind    string `json:"forwardBind"`  // 本地监听地址：127.0.0.1（仅本机）| 0.0.0.0（同网段可达）
	AutoForward    bool   `json:"autoForward"`  // 隧道起来后自动拉起已启用的端口映射
	SyncEnabled    bool   `json:"syncEnabled"`  // 允许同 tailnet 的其他设备拉取应用书签
	SyncPort       int    `json:"syncPort"`     // 同步端口，默认 4160

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
	Connected    bool         `json:"connected"`
	Connecting   bool         `json:"connecting"` // 正在建卡 / 等管理员授权
	Mode         string       `json:"mode"`       // demo | live
	ControlPlane string       `json:"controlPlane"`
	Endpoint     string       `json:"endpoint"`
	SelfIP       string       `json:"selfIP"`
	Hostname     string       `json:"hostname"`
	Engine       string       `json:"engine"`
	EngineReady  bool         `json:"engineReady"`
	LoginURL     string       `json:"loginURL"`   // 首次交互式注册时由 tsnet 给出
	LastError    string       `json:"lastError"`  // 最近一次启动/配置路由失败的原因
	Platform     string       `json:"platform"`   // darwin / windows / linux，界面按它选文案
	AppVersion   string       `json:"appVersion"` // 客户端版本，界面不再硬编码
	DataDir      string       `json:"dataDir"`    // 配置与数据库实际所在目录
	Routes       []string     `json:"routes"`     // 已安装的系统路由（system 模式）
	Peers        []PeerInfo   `json:"peers"`      // 来自本地 netmap，无需 API Key
	Skipped      []string     `json:"skipped"`    // 因与本地网段冲突被跳过的路由
	NetErr       string       `json:"netErr"`     // 路由配置错误
	CLIReady     bool         `json:"cliReady"`   // 兼容旧前端：等价于 EngineReady
	Configured   bool         `json:"configured"` // 是否已填控制面凭证
	HasAPIKey    bool         `json:"hasApiKey"`  // 是否配了能用的管理 API Key（不是节点 Auth Key）
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
	ID       string `json:"id"`
	Prefix   string `json:"prefix"`
	IsCurrent bool  `json:"isCurrent"` // 是否就是当前正在用的这把
	Expires  string `json:"expires"`
	Created  string `json:"created"`
	LastSeen string `json:"lastSeen"`
	Status   string `json:"status"`
}

// HealthInfo 服务端健康状态。
type HealthInfo struct {
	Database bool `json:"database"`
}

// Policy 是 ACL 策略文档，Headscale v1 只有内容与更新时间，没有 ETag。
type Policy struct {
	Content   string `json:"content"`
	UpdatedAt string `json:"updatedAt"`
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

// PeerInfo 是节点自己从控制面同步下来的对端视图，不需要任何 API Key。
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
// source=cache 表示控制面请求失败，返回的是 SQLite 里的上次同步结果。
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
	Time   string `json:"time"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}
