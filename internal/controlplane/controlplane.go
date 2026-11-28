package controlplane

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/secret"
	"github.com/boothua/weft/internal/sysproxy"
)

// apiError 带上 HTTP 状态码。文案和以前完全一样（日志、界面都照旧），
// 多出来的状态码是给上层判断用的：401/403 说明「这把钥匙不对」，
// 换网络、等一会儿都不会好，界面据此把服务端管理入口收起来；
// 超时、DNS 失败这类只是「现在问不到」，不能当成钥匙有问题。
type apiError struct {
	status int
	text   string
}

func (e *apiError) Error() string { return e.text }

// IsRejected 表示服务端明确拒绝了这把钥匙。
// 403 也算：headscale 对没有权限的 Key 有时回 403 而不是 401。
func IsRejected(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.status == http.StatusUnauthorized || ae.status == http.StatusForbidden
}

// ControlPlane 只实现 Headscale 管理 API（/api/v1）。
// 端点与字段以服务端 /swagger/v1/openapiv2.json 为准。
type ControlPlane interface {
	Health() (model.HealthInfo, error)
	Probe() (model.Capabilities, error)

	// 节点
	Devices(user string) ([]model.Device, error)
	Device(id string) (model.Device, error)
	RegisterNode(user, key string) (model.Device, error)
	DeleteDevice(id string) error
	RenameDevice(id, name string) error
	SetDeviceTags(id string, tags []string) error
	ExpireDevice(id string) error
	DisableExpiry(id string) error
	ApproveRoutes(id string, routes []string) error
	BackfillIPs(confirmed bool) (int, error)
	DebugCreateNode(user, key, name string, routes []string) (model.Device, error)

	// 成员
	Users() ([]model.User, error)
	CreateUser(name, displayName, email, pictureURL string) (model.User, error)
	RenameUser(id, name string) error
	DeleteUser(id string) error

	// 授权 Key
	AuthKeys() ([]model.AuthKey, error)
	CreateAuthKey(user string, reusable, ephemeral bool, days int, tags []string) (string, error)
	ExpireAuthKey(id string) error
	DeleteAuthKey(id string) error

	// API Key
	APIKeys() ([]model.APIKey, error)
	CreateAPIKey(days int) (string, error)
	ExpireAPIKey(prefix, id string) error
	DeleteAPIKey(prefix, id string) error

	// 策略
	Policy() (model.Policy, error)
	SavePolicy(content string) error
	CheckPolicy(content string) error

	// 节点认证（OIDC / CLI 流程产生的待处理请求）
	RegisterAuth(user, authID string) (model.Device, error)
	ApproveAuth(authID string) error
	RejectAuth(authID string) error
}

// ---------- 通用 HTTP ----------

var sharedHTTP = &http.Client{Timeout: 15 * time.Second}

// cpClient 是当前服务端用的 client。换服务端时重建，证书与代理跟着换。
var cpClient atomic.Pointer[http.Client]

func ClientFor() *http.Client {
	if cl := cpClient.Load(); cl != nil {
		return cl
	}
	return sharedHTTP
}

// NewHTTPClient 按服务端配置构造 client。
// 证书：填了 CA 就只认这个 CA，没填而勾了「自签」就跳过校验，都没配走系统 CA。
// 代理：环境变量优先，其次读系统代理设置。
func NewHTTPClient(c model.Config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = sysproxy.FromRequest
	switch {
	case strings.TrimSpace(c.CACert) != "":
		pool, err := CertPool(c.CACert)
		if err != nil {
			// 不能退回系统 CA：用户填了 CA 就是要钉死它，
			// 静默降级会让人以为连的是自己那台服务端。
			return &http.Client{Transport: errTransport{err}}
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	case c.ControlInsecure:
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: tr}
}

// errTransport 让所有请求都返回同一个错误，用来把「配置本身不合法」
// 传播到每个调用点，而不是在这里 panic 或者偷偷换个配置继续连。
type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// ValidateBaseURL 只放行 http/https 的完整地址。
// 管理 API Key 是放在 Authorization 头里的，http 意味着它会明文过网，
// 所以这里不拒绝（自建服务常常就在内网跑 http），但要让调用方知道。
func ValidateBaseURL(raw string) (string, bool, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false, errors.New("服务地址不能为空")
	}
	if !strings.Contains(s, "://") {
		return "", false, errors.New("服务地址要带协议，比如 https://headscale.example.com")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false, fmt.Errorf("服务地址无效：%s", raw)
	}
	switch u.Scheme {
	case "https":
		return strings.TrimRight(s, "/"), false, nil
	case "http":
		return strings.TrimRight(s, "/"), true, nil
	default:
		return "", false, fmt.Errorf("只支持 http 或 https，现在是 %s", u.Scheme)
	}
}

// CertPool 把粘贴进来的 PEM 转成证书池，读不出来就报错，别静默退回系统 CA。
func CertPool(pemText string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pemText)) {
		return nil, errors.New("CA 证书解析不了，要 PEM 格式（-----BEGIN CERTIFICATE----- 开头）")
	}
	return pool, nil
}

func Short(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// DoJSON 统一处理请求、错误解释与解包。
// Headscale 新版基于 gRPC-gateway，错误体是 {"code":..,"message":".."}，
// 直接把 message 提出来，避免用户只看到一串 JSON。
func DoJSON(method, endpoint, bearer string, body any, out any) (http.Header, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := ClientFor().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		var text string
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				text = fmt.Sprintf(
					"401 未授权：管理 API 需要 `headscale apikeys create` 生成的 API Key（不是节点 Auth Key）。"+
						"服务端响应：%s", e.Message)
			case http.StatusNotFound:
				text = "404 资源不存在：" + e.Message
			default:
				text = fmt.Sprintf("%s %s -> %d: %s", method, endpoint, resp.StatusCode, e.Message)
			}
		} else {
			text = fmt.Sprintf("%s %s -> %d: %s", method, endpoint, resp.StatusCode, Short(raw))
		}
		return resp.Header, &apiError{status: resp.StatusCode, text: text}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.Header, fmt.Errorf("解析响应失败: %w (%.120s)", err, string(raw))
		}
	}
	return resp.Header, nil
}

// ---------- 服务端原始结构（严格对齐 swagger） ----------

type hsUser struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Provider    string `json:"provider"`
	ProviderID  string `json:"providerId"`
	PictureURL  string `json:"profilePicUrl"`
	CreatedAt   string `json:"createdAt"`
}

type hsPreAuthKey struct {
	ID         string   `json:"id"`
	Key        string   `json:"key"`
	User       *hsUser  `json:"user"`
	Reusable   bool     `json:"reusable"`
	Ephemeral  bool     `json:"ephemeral"`
	Used       bool     `json:"used"`
	Expiration string   `json:"expiration"`
	CreatedAt  string   `json:"createdAt"`
	ACLTags    []string `json:"aclTags"`
}

type hsNode struct {
	ID              string   `json:"id"`
	MachineKey      string   `json:"machineKey"`
	NodeKey         string   `json:"nodeKey"`
	DiscoKey        string   `json:"discoKey"`
	IPAddresses     []string `json:"ipAddresses"`
	Name            string   `json:"name"`
	GivenName       string   `json:"givenName"`
	User            *hsUser  `json:"user"`
	LastSeen        string   `json:"lastSeen"`
	Expiry          string   `json:"expiry"`
	CreatedAt       string   `json:"createdAt"`
	RegisterMethod  any      `json:"registerMethod"`
	Online          bool     `json:"online"`
	ApprovedRoutes  []string `json:"approvedRoutes"`
	AvailableRoutes []string `json:"availableRoutes"`
	SubnetRoutes    []string `json:"subnetRoutes"`
	Tags            []string `json:"tags"`
}

type hsAPIKey struct {
	ID         string `json:"id"`
	Prefix     string `json:"prefix"`
	Expiration string `json:"expiration"`
	CreatedAt  string `json:"createdAt"`
	LastSeen   string `json:"lastSeen"`
}

// 注册方式可能是字符串 "REGISTER_METHOD_CLI"，也可能是对象 {"cli":{...}}，
// 两种都接受——只按字符串解析会让整个节点列表反序列化失败。
func (n hsNode) registerLabel() string {
	switch v := n.RegisterMethod.(type) {
	case string:
		return registerName(v)
	case map[string]any:
		for k := range v {
			return registerName(k)
		}
	}
	return "—"
}

func registerName(s string) string {
	switch strings.ToUpper(strings.TrimPrefix(s, "REGISTER_METHOD_")) {
	case "CLI", "AUTHKEY", "AUTH_KEY":
		return "Auth Key"
	case "OIDC":
		return "OIDC"
	case "":
		return "—"
	}
	return s
}

func (n hsNode) toDevice() model.Device {
	name := n.GivenName
	if name == "" {
		name = n.Name
	}
	d := model.Device{
		ID: n.ID, Name: name, Hostname: n.Name, GivenName: n.GivenName,
		// IP 只放主地址。这个字段到处被拿去和 netmap 里的 100.x 比，
		// 拼成 "100.64.0.5, fd7a:..." 就永远对不上：诊断页同一台设备
		// 会冒出两行同样的路由，ACL 页还会把它当成一个目标地址写进规则。
		// 两个地址都在 IPs 里，展示的地方本来就遍历 IPs。
		IP: PrimaryIP(n.IPAddresses), IPs: append([]string{}, n.IPAddresses...),
		Tags:     strings.Join(n.Tags, ", "),
		LastSeen: FmtTS(n.LastSeen), CreatedAt: FmtTS(n.CreatedAt), Expiry: FmtTS(n.Expiry),
		Online: n.Online, RegisterMethod: n.registerLabel(),
		MachineKey: n.MachineKey, NodeKey: n.NodeKey, DiscoKey: n.DiscoKey,
		AdvertisedRoutes: strings.Join(n.AvailableRoutes, ", "),
		ApprovedRoutes:   strings.Join(n.ApprovedRoutes, ", "),
		SubnetRoutes:     strings.Join(n.SubnetRoutes, ", "),
	}
	if n.User != nil {
		d.User, d.UserID = n.User.Name, n.User.ID
	}
	d.Status = "离线"
	if n.Online {
		d.Status = "在线"
	}
	if n.Expiry != "" && expired(n.Expiry) {
		d.Status = "已过期"
	}
	return d
}

// PrimaryIP 挑一个能用来做比较的地址：优先 IPv4，没有就取第一个。
// headscale 每个节点会下发 v4 + v6 两个地址，而 100.x 那一半才是
// 客户端 netmap 里能对上的那个。
func PrimaryIP(addrs []string) string {
	for _, a := range addrs {
		if ip, err := netip.ParseAddr(a); err == nil && ip.Is4() {
			return a
		}
	}
	if len(addrs) > 0 {
		return addrs[0]
	}
	return ""
}

func (k hsPreAuthKey) toAuthKey() model.AuthKey {
	out := model.AuthKey{
		ID: k.ID, Key: k.Key, Reusable: k.Reusable, Ephemeral: k.Ephemeral,
		Used: k.Used, Expires: FmtTS(k.Expiration), Created: FmtTS(k.CreatedAt),
		ACLTags: strings.Join(k.ACLTags, ", "),
	}
	if k.User != nil {
		out.User = k.User.Name
	}
	out.Status = "有效"
	if k.Used && !k.Reusable {
		out.Status = "已使用"
	}
	if k.Expiration != "" && expired(k.Expiration) {
		out.Status = "已过期"
	}
	return out
}

func (u hsUser) toUser() model.User {
	name := u.Name
	if u.DisplayName != "" {
		name = u.DisplayName
	}
	return model.User{
		ID: u.ID, Name: name, RawName: u.Name, DisplayName: u.DisplayName,
		Email: u.Email, Provider: u.Provider, PictureURL: u.PictureURL, CreatedAt: FmtTS(u.CreatedAt),
	}
}

// FmtTS 把服务端的 RFC3339(Nano) 转成本地「年-月-日 时:分」。
// 原始串带纳秒且是 Z 时区，直接显示会很长且不是本地时间。
func FmtTS(ts string) string {
	if ts == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			if t.Year() <= 1 {
				return ""
			}
			return t.Local().Format("2006-01-02 15:04")
		}
	}
	return ts
}

// expired 判断 RFC3339 时间是否已过；0001-01-01 表示永不过期。
func expired(ts string) bool {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	if t.Year() <= 1 {
		return false
	}
	return t.Before(time.Now())
}

// ---------- Headscale 实现 ----------

type HeadscaleCP struct{ cfg model.Config }

var headscaleCPImpl ControlPlane = (*HeadscaleCP)(nil)

func New(c model.Config) ControlPlane {
	cpClient.Store(NewHTTPClient(c))
	return &HeadscaleCP{cfg: c}
}

func (c *HeadscaleCP) bearer() string { return strings.TrimSpace(secret.Get(secret.Main)) }

// IsAuthKey 判断这串钥匙是节点入网用的 Auth Key。
// 它也能填进「管理 API Key」，但管理接口一律 401，界面不该把它当成能管服务端。
func IsAuthKey(k string) bool {
	return strings.HasPrefix(k, "hskey-auth-") || strings.HasPrefix(k, "tskey-auth-")
}

// usableAPIKey 表示「管理 API 现在能用」：空值和 Auth Key 都不算。
// 界面拿它决定是否显示服务端管理相关入口。
func usableAPIKey() bool {
	k := strings.TrimSpace(secret.Get(secret.Main))
	return k != "" && !IsAuthKey(k)
}

// LooksLikeAPIKey 只看形状：headscale 的管理钥匙一律 hskey- 开头
// （旧版 hskey-apikey-、新版 hskey-api-）。节点 Auth Key 虽然也是 hskey-，
// 但管理接口不认，单独排掉，让调用方能给出更准的提示。
// 它只用来在没联网时先给个答案，真正认不认还得问服务端（见 CheckKey）。
func LooksLikeAPIKey(k string) bool {
	if IsAuthKey(k) {
		return false
	}
	return strings.HasPrefix(k, "hskey-") || strings.HasPrefix(k, "tskey-")
}

// CheckKey 是最便宜的一次鉴权：打 /api/v1/node，401/403 就代表钥匙不能用。
// 不走 Probe，是因为启动和改配置时会频繁调用，没必要顺带拉 version 和 health。
func (c *HeadscaleCP) CheckKey() error {
	key := c.bearer()
	if strings.TrimSpace(key) == "" {
		return nil
	}
	var v map[string]any
	_, err := DoJSON("GET", c.url("/api/v1/node"), key, nil, &v)
	return err
}

func (c *HeadscaleCP) url(p string) string {
	return strings.TrimRight(c.cfg.BaseURL, "/") + p
}

func (c *HeadscaleCP) Health() (model.HealthInfo, error) {
	var v struct {
		DatabaseConnectivity bool `json:"databaseConnectivity"`
	}
	if _, err := DoJSON("GET", c.url("/api/v1/health"), c.bearer(), nil, &v); err != nil {
		return model.HealthInfo{}, err
	}
	return model.HealthInfo{Database: v.DatabaseConnectivity}, nil
}

func (c *HeadscaleCP) Probe() (model.Capabilities, error) {
	cap := model.Capabilities{
		APIStyle: "v1 REST", CanCreateUser: true, CanManageAPIKeys: true, CanManageACL: true,
	}
	var v map[string]any
	if _, err := DoJSON("GET", c.url("/version"), "", nil, &v); err == nil {
		cap.Version = str(v, "version")
	}
	if cap.Version == "" {
		cap.Version = "未知版本"
	}
	key := c.bearer()
	if key == "" {
		return cap, fmt.Errorf("未配置管理 API Key：在服务器上运行 `headscale apikeys create`，" +
			"把结果填到「管理 API Key」（不是节点注册用的 Auth Key）")
	}
	if IsAuthKey(key) {
		return cap, fmt.Errorf("这是节点注册用的 Auth Key，不能用于管理 API。请用 `headscale apikeys create` 生成")
	}
	if _, err := DoJSON("GET", c.url("/api/v1/node"), key, nil, &v); err != nil {
		return cap, fmt.Errorf("API Key 校验失败：%w", err)
	}
	if h, err := c.Health(); err == nil {
		cap.Database = h.Database
	}
	cap.ProbeMessage = "Headscale " + cap.Version
	return cap, nil
}

// ---- 节点 ----

func (c *HeadscaleCP) Devices(user string) ([]model.Device, error) {
	p := "/api/v1/node"
	if user != "" {
		p += "?user=" + url.QueryEscape(user)
	}
	var v struct {
		Nodes []hsNode `json:"nodes"`
	}
	if _, err := DoJSON("GET", c.url(p), c.bearer(), nil, &v); err != nil {
		return nil, err
	}
	out := make([]model.Device, 0, len(v.Nodes))
	for _, n := range v.Nodes {
		out = append(out, n.toDevice())
	}
	return out, nil
}

func (c *HeadscaleCP) Device(id string) (model.Device, error) {
	var v struct {
		Node hsNode `json:"node"`
	}
	if _, err := DoJSON("GET", c.url("/api/v1/node/"+url.PathEscape(id)), c.bearer(), nil, &v); err != nil {
		return model.Device{}, err
	}
	return v.Node.toDevice(), nil
}

func (c *HeadscaleCP) RegisterNode(user, key string) (model.Device, error) {
	q := url.Values{}
	q.Set("user", user)
	q.Set("key", key)
	var v struct {
		Node hsNode `json:"node"`
	}
	if _, err := DoJSON("POST", c.url("/api/v1/node/register?"+q.Encode()), c.bearer(), nil, &v); err != nil {
		return model.Device{}, err
	}
	return v.Node.toDevice(), nil
}

func (c *HeadscaleCP) DeleteDevice(id string) error {
	_, err := DoJSON("DELETE", c.url("/api/v1/node/"+url.PathEscape(id)), c.bearer(), nil, nil)
	return err
}

// 新版 Headscale 把新名字放在路径里：POST /api/v1/node/{id}/rename/{name}
func (c *HeadscaleCP) RenameDevice(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("新名称不能为空")
	}
	_, err := DoJSON("POST",
		c.url("/api/v1/node/"+url.PathEscape(id)+"/rename/"+url.PathEscape(name)), c.bearer(), nil, nil)
	return err
}

func (c *HeadscaleCP) SetDeviceTags(id string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	_, err := DoJSON("POST", c.url("/api/v1/node/"+url.PathEscape(id)+"/tags"),
		c.bearer(), map[string]any{"tags": tags}, nil)
	return err
}

// expire 的到期时间走 query 参数，不是请求体。
func (c *HeadscaleCP) ExpireDevice(id string) error {
	q := url.Values{}
	q.Set("expiry", time.Now().UTC().Format(time.RFC3339))
	_, err := DoJSON("POST", c.url("/api/v1/node/"+url.PathEscape(id)+"/expire?"+q.Encode()),
		c.bearer(), nil, nil)
	return err
}

// DisableExpiry 取消节点的密钥到期时间（永久有效）。
func (c *HeadscaleCP) DisableExpiry(id string) error {
	q := url.Values{}
	q.Set("disableExpiry", "true")
	_, err := DoJSON("POST", c.url("/api/v1/node/"+url.PathEscape(id)+"/expire?"+q.Encode()),
		c.bearer(), nil, nil)
	return err
}

func (c *HeadscaleCP) ApproveRoutes(id string, routes []string) error {
	if routes == nil {
		routes = []string{}
	}
	_, err := DoJSON("POST", c.url("/api/v1/node/"+url.PathEscape(id)+"/approve_routes"),
		c.bearer(), map[string]any{"routes": routes}, nil)
	return err
}

// BackfillIPs 给缺少 IP 的节点补分配地址。
// 只对老数据（升级前注册、ip_addresses 为空的节点）有意义，正常注册的节点都有 IP。
// headscale 要求显式带 confirmed=true，否则直接拒绝。
// 返回的 changes 是节点名数组，不是数量。
func (c *HeadscaleCP) BackfillIPs(confirmed bool) (int, error) {
	q := url.Values{}
	q.Set("confirmed", strconv.FormatBool(confirmed))
	var v struct {
		Changes []string `json:"changes"`
	}
	if _, err := DoJSON("POST", c.url("/api/v1/node/backfillips?"+q.Encode()), c.bearer(), nil, &v); err != nil {
		return 0, err
	}
	return len(v.Changes), nil
}

func (c *HeadscaleCP) DebugCreateNode(user, key, name string, routes []string) (model.Device, error) {
	body := map[string]any{"user": user, "key": key, "name": name}
	if len(routes) > 0 {
		body["routes"] = routes
	}
	var v struct {
		Node hsNode `json:"node"`
	}
	if _, err := DoJSON("POST", c.url("/api/v1/debug/node"), c.bearer(), body, &v); err != nil {
		return model.Device{}, err
	}
	return v.Node.toDevice(), nil
}

// ---- 成员 ----

func (c *HeadscaleCP) Users() ([]model.User, error) {
	var v struct {
		Users []hsUser `json:"users"`
	}
	if _, err := DoJSON("GET", c.url("/api/v1/user"), c.bearer(), nil, &v); err != nil {
		return nil, err
	}
	out := make([]model.User, 0, len(v.Users))
	for _, u := range v.Users {
		out = append(out, u.toUser())
	}
	return out, nil
}

func (c *HeadscaleCP) CreateUser(name, displayName, email, pictureURL string) (model.User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.User{}, fmt.Errorf("成员名称不能为空")
	}
	body := map[string]any{"name": name}
	if displayName != "" {
		body["displayName"] = displayName
	}
	if email != "" {
		body["email"] = email
	}
	if pictureURL != "" {
		body["pictureUrl"] = pictureURL
	}
	var v struct {
		User hsUser `json:"user"`
	}
	if _, err := DoJSON("POST", c.url("/api/v1/user"), c.bearer(), body, &v); err != nil {
		return model.User{}, err
	}
	return v.User.toUser(), nil
}

func (c *HeadscaleCP) RenameUser(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("新名称不能为空")
	}
	_, err := DoJSON("POST",
		c.url("/api/v1/user/"+url.PathEscape(id)+"/rename/"+url.PathEscape(name)), c.bearer(), nil, nil)
	return err
}

func (c *HeadscaleCP) DeleteUser(id string) error {
	_, err := DoJSON("DELETE", c.url("/api/v1/user/"+url.PathEscape(id)), c.bearer(), nil, nil)
	return err
}

// ---- 授权 Key ----

func (c *HeadscaleCP) AuthKeys() ([]model.AuthKey, error) {
	var v struct {
		Keys []hsPreAuthKey `json:"preAuthKeys"`
	}
	if _, err := DoJSON("GET", c.url("/api/v1/preauthkey"), c.bearer(), nil, &v); err != nil {
		return nil, err
	}
	out := make([]model.AuthKey, 0, len(v.Keys))
	for _, k := range v.Keys {
		out = append(out, k.toAuthKey())
	}
	return out, nil
}

// userID 把用户名解析成接口要求的 uint64 用户 ID。
// 新版 preauthkey 的 user 字段是数字，直接传名字会被 proto 拒绝。
func (c *HeadscaleCP) userID(nameOrID string) (string, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return "", fmt.Errorf("请选择成员")
	}
	if _, err := strconv.ParseUint(nameOrID, 10, 64); err == nil {
		return nameOrID, nil
	}
	var v struct {
		Users []hsUser `json:"users"`
	}
	if _, err := DoJSON("GET", c.url("/api/v1/user?name="+url.QueryEscape(nameOrID)), c.bearer(), nil, &v); err == nil {
		for _, u := range v.Users {
			if u.Name == nameOrID {
				return u.ID, nil
			}
		}
	}
	v.Users = nil
	if _, err := DoJSON("GET", c.url("/api/v1/user"), c.bearer(), nil, &v); err != nil {
		return "", err
	}
	for _, u := range v.Users {
		if u.Name == nameOrID {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("未找到成员 %q", nameOrID)
}

func (c *HeadscaleCP) CreateAuthKey(user string, reusable, ephemeral bool, days int, tags []string) (string, error) {
	uid, err := c.userID(user)
	if err != nil {
		return "", err
	}
	if days <= 0 {
		days = 90
	}
	body := map[string]any{
		"user": uid, "reusable": reusable, "ephemeral": ephemeral,
		"expiration": time.Now().Add(time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
	if len(tags) > 0 {
		body["aclTags"] = tags
	}
	var v struct {
		Key hsPreAuthKey `json:"preAuthKey"`
	}
	if _, err = DoJSON("POST", c.url("/api/v1/preauthkey"), c.bearer(), body, &v); err != nil {
		return "", err
	}
	if v.Key.Key == "" {
		return "", fmt.Errorf("服务端未返回 Key 明文")
	}
	return v.Key.Key, nil
}

func (c *HeadscaleCP) ExpireAuthKey(id string) error {
	_, err := DoJSON("POST", c.url("/api/v1/preauthkey/expire"), c.bearer(),
		map[string]any{"id": id}, nil)
	return err
}

func (c *HeadscaleCP) DeleteAuthKey(id string) error {
	_, err := DoJSON("DELETE", c.url("/api/v1/preauthkey?id="+url.QueryEscape(id)), c.bearer(), nil, nil)
	return err
}

// ---- API Key ----

// KeyPrefixMatches 判断列表里的某条 Key 是不是当前这把。
// headscale 返回的 prefix 带 *** 掩码（hskey-api-8Z6n5H9TrGjV-***），
// 直接 HasPrefix 永远不成立，得先掐掉掩码尾部。
func KeyPrefixMatches(current, listed string) bool {
	cur := strings.TrimSpace(current)
	p := strings.TrimSuffix(strings.TrimSpace(listed), "***")
	return cur != "" && p != "" && strings.HasPrefix(cur, p)
}

func (c *HeadscaleCP) APIKeys() ([]model.APIKey, error) {
	var v struct {
		Keys []hsAPIKey `json:"apiKeys"`
	}
	if _, err := DoJSON("GET", c.url("/api/v1/apikey"), c.bearer(), nil, &v); err != nil {
		return nil, err
	}
	// 当前这把的明文在钥匙串里，用前缀认出来，标在列表上。
	// 前端不能自己猜「最晚到期的是当前这把」—— 续期后新 Key 只有 90 天，
	// 旧的如果还剩 365 天就会被选中，看起来像没续上。
	cur := c.bearer()
	out := make([]model.APIKey, 0, len(v.Keys))
	for _, k := range v.Keys {
		out = append(out, model.APIKey{
			ID: k.ID, Prefix: k.Prefix, Expires: FmtTS(k.Expiration),
			Created: FmtTS(k.CreatedAt), LastSeen: FmtTS(k.LastSeen), Status: statusOf(k.Expiration),
			IsCurrent: KeyPrefixMatches(cur, k.Prefix),
		})
	}
	return out, nil
}

func statusOf(ts string) string {
	if ts == "" {
		return "有效"
	}
	if expired(ts) {
		return "已过期"
	}
	return "有效"
}

func (c *HeadscaleCP) CreateAPIKey(days int) (string, error) {
	if days <= 0 {
		days = 90
	}
	body := map[string]any{
		"expiration": time.Now().Add(time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
	var v struct {
		Key string `json:"apiKey"`
	}
	if _, err := DoJSON("POST", c.url("/api/v1/apikey"), c.bearer(), body, &v); err != nil {
		return "", err
	}
	if v.Key == "" {
		return "", fmt.Errorf("服务端未返回 API Key 明文")
	}
	return v.Key, nil
}

func (c *HeadscaleCP) ExpireAPIKey(prefix, id string) error {
	// headscale 只接受 id 和 prefix 中的一个，同时给会 400「provide either id or prefix, not both」。
	// 有 id 就用 id（精确），没有才退回前缀。
	body := map[string]any{}
	if id != "" {
		body["id"] = id
	} else {
		// 列表里的 prefix 带 *** 掩码，服务端按真实前缀查
		body["prefix"] = strings.TrimSuffix(prefix, "***")
	}
	_, err := DoJSON("POST", c.url("/api/v1/apikey/expire"), c.bearer(), body, nil)
	return err
}

func (c *HeadscaleCP) DeleteAPIKey(prefix, id string) error {
	// 路由是 /api/v1/apikey/{prefix}，路径段必须是前缀；
	// id 只能作为 ?id= 查询参数，而且和前缀二选一，否则会报 not both。
	// 之前把 id 塞进路径，服务端拿 37 当初缀查，直接 500 record not found。
	p := "/api/v1/apikey/" + url.PathEscape(strings.TrimSuffix(prefix, "***"))
	_, err := DoJSON("DELETE", c.url(p), c.bearer(), nil, nil)
	return err
}

// ---- 策略 ----

func (c *HeadscaleCP) Policy() (model.Policy, error) {
	var v struct {
		Policy    string `json:"policy"`
		UpdatedAt string `json:"updatedAt"`
	}
	if _, err := DoJSON("GET", c.url("/api/v1/policy"), c.bearer(), nil, &v); err != nil {
		// headscale 在没配策略文件时会回 500，原文是
		// reading policy from path "": open : no such file or directory
		// 这不是客户端问题，直接告诉用户去哪改
		msg := err.Error()
		if strings.Contains(msg, "reading policy from path") || strings.Contains(msg, "no such file or directory") {
			return model.Policy{}, fmt.Errorf("服务端未配置 ACL 策略文件。在 headscale 的 config.yaml 里加上：\n" +
				"policy:\n  mode: file\n  path: /etc/headscale/acl.hujson\n" +
				"然后创建该文件并重启 headscale")
		}
		return model.Policy{}, err
	}
	return model.Policy{Content: v.Policy, UpdatedAt: v.UpdatedAt}, nil
}

func (c *HeadscaleCP) SavePolicy(content string) error {
	_, err := DoJSON("PUT", c.url("/api/v1/policy"), c.bearer(),
		map[string]any{"policy": content}, nil)
	return err
}

// CheckPolicy 只校验不落库，服务端返回 200 即通过。
func (c *HeadscaleCP) CheckPolicy(content string) error {
	_, err := DoJSON("POST", c.url("/api/v1/policy/check"), c.bearer(),
		map[string]any{"policy": content}, nil)
	return err
}

// ---- 节点认证 ----

func (c *HeadscaleCP) RegisterAuth(user, authID string) (model.Device, error) {
	var v struct {
		Node hsNode `json:"node"`
	}
	_, err := DoJSON("POST", c.url("/api/v1/auth/register"), c.bearer(),
		map[string]any{"user": user, "authId": authID}, &v)
	if err != nil {
		return model.Device{}, err
	}
	return v.Node.toDevice(), nil
}

func (c *HeadscaleCP) ApproveAuth(authID string) error {
	_, err := DoJSON("POST", c.url("/api/v1/auth/approve"), c.bearer(),
		map[string]any{"authId": authID}, nil)
	return err
}

func (c *HeadscaleCP) RejectAuth(authID string) error {
	_, err := DoJSON("POST", c.url("/api/v1/auth/reject"), c.bearer(),
		map[string]any{"authId": authID}, nil)
	return err
}

// ---- 小工具 ----

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch t := v.(type) {
			case string:
				return t
			case float64:
				return strconv.FormatFloat(t, 'f', -1, 64)
			case bool:
				return strconv.FormatBool(t)
			}
		}
	}
	return ""
}
