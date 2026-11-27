package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ControlPlane 只实现 Headscale 管理 API（/api/v1）。
// 端点与字段以服务端 /swagger/v1/openapiv2.json 为准。
type ControlPlane interface {
	Health() (HealthInfo, error)
	Probe() (Capabilities, error)

	// 节点
	Devices(user string) ([]Device, error)
	Device(id string) (Device, error)
	RegisterNode(user, key string) (Device, error)
	DeleteDevice(id string) error
	RenameDevice(id, name string) error
	SetDeviceTags(id string, tags []string) error
	ExpireDevice(id string) error
	DisableExpiry(id string) error
	ApproveRoutes(id string, routes []string) error
	BackfillIPs(confirmed bool) (int, error)
	DebugCreateNode(user, key, name string, routes []string) (Device, error)

	// 成员
	Users() ([]User, error)
	CreateUser(name, displayName, email, pictureURL string) (User, error)
	RenameUser(id, name string) error
	DeleteUser(id string) error

	// 授权 Key
	AuthKeys() ([]AuthKey, error)
	CreateAuthKey(user string, reusable, ephemeral bool, days int, tags []string) (string, error)
	ExpireAuthKey(id string) error
	DeleteAuthKey(id string) error

	// API Key
	APIKeys() ([]APIKey, error)
	CreateAPIKey(days int) (string, error)
	ExpireAPIKey(prefix, id string) error
	DeleteAPIKey(prefix, id string) error

	// 策略
	Policy() (Policy, error)
	SavePolicy(content string) error
	CheckPolicy(content string) error

	// 节点认证（OIDC / CLI 流程产生的待处理请求）
	RegisterAuth(user, authID string) (Device, error)
	ApproveAuth(authID string) error
	RejectAuth(authID string) error
}

// ---------- 通用 HTTP ----------

var sharedHTTP = &http.Client{Timeout: 15 * time.Second}

func short(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// doJSON 统一处理请求、错误解释与解包。
// Headscale 新版基于 gRPC-gateway，错误体是 {"code":..,"message":".."}，
// 直接把 message 提出来，避免用户只看到一串 JSON。
func doJSON(method, endpoint, bearer string, body any, out any) (http.Header, error) {
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
	resp, err := sharedHTTP.Do(req)
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
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return resp.Header, fmt.Errorf(
					"401 未授权：管理 API 需要 `headscale apikeys create` 生成的 API Key（不是节点 Auth Key）。"+
						"服务端响应：%s", e.Message)
			case http.StatusNotFound:
				return resp.Header, fmt.Errorf("404 资源不存在：%s", e.Message)
			}
			return resp.Header, fmt.Errorf("%s %s -> %d: %s", method, endpoint, resp.StatusCode, e.Message)
		}
		return resp.Header, fmt.Errorf("%s %s -> %d: %s", method, endpoint, resp.StatusCode, short(raw))
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
	ID              string          `json:"id"`
	MachineKey      string          `json:"machineKey"`
	NodeKey         string          `json:"nodeKey"`
	DiscoKey        string          `json:"discoKey"`
	IPAddresses     []string        `json:"ipAddresses"`
	Name            string          `json:"name"`
	GivenName       string          `json:"givenName"`
	User            *hsUser         `json:"user"`
	LastSeen        string          `json:"lastSeen"`
	Expiry          string          `json:"expiry"`
	CreatedAt       string          `json:"createdAt"`
	RegisterMethod  any             `json:"registerMethod"`
	Online          bool            `json:"online"`
	ApprovedRoutes  []string        `json:"approvedRoutes"`
	AvailableRoutes []string        `json:"availableRoutes"`
	SubnetRoutes    []string        `json:"subnetRoutes"`
	Tags            []string        `json:"tags"`
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

func (n hsNode) toDevice() Device {
	name := n.GivenName
	if name == "" {
		name = n.Name
	}
	d := Device{
		ID: n.ID, Name: name, Hostname: n.Name, GivenName: n.GivenName,
		IP: strings.Join(n.IPAddresses, ", "), IPs: append([]string{}, n.IPAddresses...),
		Tags: strings.Join(n.Tags, ", "),
		LastSeen: fmtTS(n.LastSeen), CreatedAt: fmtTS(n.CreatedAt), Expiry: fmtTS(n.Expiry),
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

func (k hsPreAuthKey) toAuthKey() AuthKey {
	out := AuthKey{
		ID: k.ID, Key: k.Key, Reusable: k.Reusable, Ephemeral: k.Ephemeral,
		Used: k.Used, Expires: fmtTS(k.Expiration), Created: fmtTS(k.CreatedAt),
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

func (u hsUser) toUser() User {
	name := u.Name
	if u.DisplayName != "" {
		name = u.DisplayName
	}
	return User{
		ID: u.ID, Name: name, RawName: u.Name, DisplayName: u.DisplayName,
		Email: u.Email, Provider: u.Provider, PictureURL: u.PictureURL, CreatedAt: fmtTS(u.CreatedAt),
	}
}

// fmtTS 把服务端的 RFC3339(Nano) 转成本地「年-月-日 时:分」。
// 原始串带纳秒且是 Z 时区，直接显示会很长且不是本地时间。
func fmtTS(ts string) string {
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

type headscaleCP struct{ cfg Config }

var headscaleCPImpl ControlPlane = (*headscaleCP)(nil)

func newControlPlane(c Config) ControlPlane { return &headscaleCP{cfg: c} }

func (c *headscaleCP) bearer() string { return strings.TrimSpace(secretGet(secretMain)) }
func (c *headscaleCP) url(p string) string {
	return strings.TrimRight(c.cfg.BaseURL, "/") + p
}

func (c *headscaleCP) Health() (HealthInfo, error) {
	var v struct {
		DatabaseConnectivity bool `json:"databaseConnectivity"`
	}
	if _, err := doJSON("GET", c.url("/api/v1/health"), c.bearer(), nil, &v); err != nil {
		return HealthInfo{}, err
	}
	return HealthInfo{Database: v.DatabaseConnectivity}, nil
}

func (c *headscaleCP) Probe() (Capabilities, error) {
	cap := Capabilities{
		APIStyle: "v1 REST", CanCreateUser: true, CanManageAPIKeys: true, CanManageACL: true,
	}
	var v map[string]any
	if _, err := doJSON("GET", c.url("/version"), "", nil, &v); err == nil {
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
	if strings.HasPrefix(key, "hskey-auth-") || strings.HasPrefix(key, "tskey-auth-") {
		return cap, fmt.Errorf("这是节点注册用的 Auth Key，不能用于管理 API。请用 `headscale apikeys create` 生成")
	}
	if _, err := doJSON("GET", c.url("/api/v1/node"), key, nil, &v); err != nil {
		return cap, fmt.Errorf("API Key 校验失败：%w", err)
	}
	if h, err := c.Health(); err == nil {
		cap.Database = h.Database
	}
	cap.ProbeMessage = "Headscale " + cap.Version
	return cap, nil
}

// ---- 节点 ----

func (c *headscaleCP) Devices(user string) ([]Device, error) {
	p := "/api/v1/node"
	if user != "" {
		p += "?user=" + url.QueryEscape(user)
	}
	var v struct {
		Nodes []hsNode `json:"nodes"`
	}
	if _, err := doJSON("GET", c.url(p), c.bearer(), nil, &v); err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(v.Nodes))
	for _, n := range v.Nodes {
		out = append(out, n.toDevice())
	}
	return out, nil
}

func (c *headscaleCP) Device(id string) (Device, error) {
	var v struct {
		Node hsNode `json:"node"`
	}
	if _, err := doJSON("GET", c.url("/api/v1/node/"+url.PathEscape(id)), c.bearer(), nil, &v); err != nil {
		return Device{}, err
	}
	return v.Node.toDevice(), nil
}

func (c *headscaleCP) RegisterNode(user, key string) (Device, error) {
	q := url.Values{}
	q.Set("user", user)
	q.Set("key", key)
	var v struct {
		Node hsNode `json:"node"`
	}
	if _, err := doJSON("POST", c.url("/api/v1/node/register?"+q.Encode()), c.bearer(), nil, &v); err != nil {
		return Device{}, err
	}
	return v.Node.toDevice(), nil
}

func (c *headscaleCP) DeleteDevice(id string) error {
	_, err := doJSON("DELETE", c.url("/api/v1/node/"+url.PathEscape(id)), c.bearer(), nil, nil)
	return err
}

// 新版 Headscale 把新名字放在路径里：POST /api/v1/node/{id}/rename/{name}
func (c *headscaleCP) RenameDevice(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("新名称不能为空")
	}
	_, err := doJSON("POST",
		c.url("/api/v1/node/"+url.PathEscape(id)+"/rename/"+url.PathEscape(name)), c.bearer(), nil, nil)
	return err
}

func (c *headscaleCP) SetDeviceTags(id string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	_, err := doJSON("POST", c.url("/api/v1/node/"+url.PathEscape(id)+"/tags"),
		c.bearer(), map[string]any{"tags": tags}, nil)
	return err
}

// expire 的到期时间走 query 参数，不是请求体。
func (c *headscaleCP) ExpireDevice(id string) error {
	q := url.Values{}
	q.Set("expiry", time.Now().UTC().Format(time.RFC3339))
	_, err := doJSON("POST", c.url("/api/v1/node/"+url.PathEscape(id)+"/expire?"+q.Encode()),
		c.bearer(), nil, nil)
	return err
}

// DisableExpiry 取消节点的密钥到期时间（永久有效）。
func (c *headscaleCP) DisableExpiry(id string) error {
	q := url.Values{}
	q.Set("disableExpiry", "true")
	_, err := doJSON("POST", c.url("/api/v1/node/"+url.PathEscape(id)+"/expire?"+q.Encode()),
		c.bearer(), nil, nil)
	return err
}

func (c *headscaleCP) ApproveRoutes(id string, routes []string) error {
	if routes == nil {
		routes = []string{}
	}
	_, err := doJSON("POST", c.url("/api/v1/node/"+url.PathEscape(id)+"/approve_routes"),
		c.bearer(), map[string]any{"routes": routes}, nil)
	return err
}

// BackfillIPs 给缺少 IP 的节点补分配地址。
// 只对老数据（升级前注册、ip_addresses 为空的节点）有意义，正常注册的节点都有 IP。
// headscale 要求显式带 confirmed=true，否则直接拒绝。
// 返回的 changes 是节点名数组，不是数量。
func (c *headscaleCP) BackfillIPs(confirmed bool) (int, error) {
	q := url.Values{}
	q.Set("confirmed", strconv.FormatBool(confirmed))
	var v struct {
		Changes []string `json:"changes"`
	}
	if _, err := doJSON("POST", c.url("/api/v1/node/backfillips?"+q.Encode()), c.bearer(), nil, &v); err != nil {
		return 0, err
	}
	return len(v.Changes), nil
}

func (c *headscaleCP) DebugCreateNode(user, key, name string, routes []string) (Device, error) {
	body := map[string]any{"user": user, "key": key, "name": name}
	if len(routes) > 0 {
		body["routes"] = routes
	}
	var v struct {
		Node hsNode `json:"node"`
	}
	if _, err := doJSON("POST", c.url("/api/v1/debug/node"), c.bearer(), body, &v); err != nil {
		return Device{}, err
	}
	return v.Node.toDevice(), nil
}

// ---- 成员 ----

func (c *headscaleCP) Users() ([]User, error) {
	var v struct {
		Users []hsUser `json:"users"`
	}
	if _, err := doJSON("GET", c.url("/api/v1/user"), c.bearer(), nil, &v); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(v.Users))
	for _, u := range v.Users {
		out = append(out, u.toUser())
	}
	return out, nil
}

func (c *headscaleCP) CreateUser(name, displayName, email, pictureURL string) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, fmt.Errorf("成员名称不能为空")
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
	if _, err := doJSON("POST", c.url("/api/v1/user"), c.bearer(), body, &v); err != nil {
		return User{}, err
	}
	return v.User.toUser(), nil
}

func (c *headscaleCP) RenameUser(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("新名称不能为空")
	}
	_, err := doJSON("POST",
		c.url("/api/v1/user/"+url.PathEscape(id)+"/rename/"+url.PathEscape(name)), c.bearer(), nil, nil)
	return err
}

func (c *headscaleCP) DeleteUser(id string) error {
	_, err := doJSON("DELETE", c.url("/api/v1/user/"+url.PathEscape(id)), c.bearer(), nil, nil)
	return err
}

// ---- 授权 Key ----

func (c *headscaleCP) AuthKeys() ([]AuthKey, error) {
	var v struct {
		Keys []hsPreAuthKey `json:"preAuthKeys"`
	}
	if _, err := doJSON("GET", c.url("/api/v1/preauthkey"), c.bearer(), nil, &v); err != nil {
		return nil, err
	}
	out := make([]AuthKey, 0, len(v.Keys))
	for _, k := range v.Keys {
		out = append(out, k.toAuthKey())
	}
	return out, nil
}

// userID 把用户名解析成接口要求的 uint64 用户 ID。
// 新版 preauthkey 的 user 字段是数字，直接传名字会被 proto 拒绝。
func (c *headscaleCP) userID(nameOrID string) (string, error) {
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
	if _, err := doJSON("GET", c.url("/api/v1/user?name="+url.QueryEscape(nameOrID)), c.bearer(), nil, &v); err == nil {
		for _, u := range v.Users {
			if u.Name == nameOrID {
				return u.ID, nil
			}
		}
	}
	v.Users = nil
	if _, err := doJSON("GET", c.url("/api/v1/user"), c.bearer(), nil, &v); err != nil {
		return "", err
	}
	for _, u := range v.Users {
		if u.Name == nameOrID {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("未找到成员 %q", nameOrID)
}

func (c *headscaleCP) CreateAuthKey(user string, reusable, ephemeral bool, days int, tags []string) (string, error) {
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
	if _, err = doJSON("POST", c.url("/api/v1/preauthkey"), c.bearer(), body, &v); err != nil {
		return "", err
	}
	if v.Key.Key == "" {
		return "", fmt.Errorf("服务端未返回 Key 明文")
	}
	return v.Key.Key, nil
}

func (c *headscaleCP) ExpireAuthKey(id string) error {
	_, err := doJSON("POST", c.url("/api/v1/preauthkey/expire"), c.bearer(),
		map[string]any{"id": id}, nil)
	return err
}

func (c *headscaleCP) DeleteAuthKey(id string) error {
	_, err := doJSON("DELETE", c.url("/api/v1/preauthkey?id="+url.QueryEscape(id)), c.bearer(), nil, nil)
	return err
}

// ---- API Key ----

func (c *headscaleCP) APIKeys() ([]APIKey, error) {
	var v struct {
		Keys []hsAPIKey `json:"apiKeys"`
	}
	if _, err := doJSON("GET", c.url("/api/v1/apikey"), c.bearer(), nil, &v); err != nil {
		return nil, err
	}
	// 当前这把的明文在钥匙串里，用前缀认出来，标在列表上。
	// 前端不能自己猜「最晚到期的是当前这把」—— 续期后新 Key 只有 90 天，
	// 旧的如果还剩 365 天就会被选中，看起来像没续上。
	cur := c.bearer()
	out := make([]APIKey, 0, len(v.Keys))
	for _, k := range v.Keys {
		out = append(out, APIKey{
			ID: k.ID, Prefix: k.Prefix, Expires: fmtTS(k.Expiration),
			Created: fmtTS(k.CreatedAt), LastSeen: fmtTS(k.LastSeen), Status: statusOf(k.Expiration),
			IsCurrent: cur != "" && k.Prefix != "" && strings.HasPrefix(cur, k.Prefix),
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

func (c *headscaleCP) CreateAPIKey(days int) (string, error) {
	if days <= 0 {
		days = 90
	}
	body := map[string]any{
		"expiration": time.Now().Add(time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
	var v struct {
		Key string `json:"apiKey"`
	}
	if _, err := doJSON("POST", c.url("/api/v1/apikey"), c.bearer(), body, &v); err != nil {
		return "", err
	}
	if v.Key == "" {
		return "", fmt.Errorf("服务端未返回 API Key 明文")
	}
	return v.Key, nil
}

func (c *headscaleCP) ExpireAPIKey(prefix, id string) error {
	body := map[string]any{"prefix": prefix}
	if id != "" {
		body["id"] = id
	}
	_, err := doJSON("POST", c.url("/api/v1/apikey/expire"), c.bearer(), body, nil)
	return err
}

func (c *headscaleCP) DeleteAPIKey(prefix, id string) error {
	p := "/api/v1/apikey/" + url.PathEscape(prefix)
	if id != "" {
		p += "?id=" + url.QueryEscape(id)
	}
	_, err := doJSON("DELETE", c.url(p), c.bearer(), nil, nil)
	return err
}

// ---- 策略 ----

func (c *headscaleCP) Policy() (Policy, error) {
	var v struct {
		Policy    string `json:"policy"`
		UpdatedAt string `json:"updatedAt"`
	}
	if _, err := doJSON("GET", c.url("/api/v1/policy"), c.bearer(), nil, &v); err != nil {
		// headscale 在没配策略文件时会回 500，原文是
		// reading policy from path "": open : no such file or directory
		// 这不是客户端问题，直接告诉用户去哪改
		msg := err.Error()
		if strings.Contains(msg, "reading policy from path") || strings.Contains(msg, "no such file or directory") {
			return Policy{}, fmt.Errorf("服务端未配置 ACL 策略文件。在 headscale 的 config.yaml 里加上：\n" +
				"policy:\n  mode: file\n  path: /etc/headscale/acl.hujson\n" +
				"然后创建该文件并重启 headscale")
		}
		return Policy{}, err
	}
	return Policy{Content: v.Policy, UpdatedAt: v.UpdatedAt}, nil
}

func (c *headscaleCP) SavePolicy(content string) error {
	_, err := doJSON("PUT", c.url("/api/v1/policy"), c.bearer(),
		map[string]any{"policy": content}, nil)
	return err
}

// CheckPolicy 只校验不落库，服务端返回 200 即通过。
func (c *headscaleCP) CheckPolicy(content string) error {
	_, err := doJSON("POST", c.url("/api/v1/policy/check"), c.bearer(),
		map[string]any{"policy": content}, nil)
	return err
}

// ---- 节点认证 ----

func (c *headscaleCP) RegisterAuth(user, authID string) (Device, error) {
	var v struct {
		Node hsNode `json:"node"`
	}
	_, err := doJSON("POST", c.url("/api/v1/auth/register"), c.bearer(),
		map[string]any{"user": user, "authId": authID}, &v)
	if err != nil {
		return Device{}, err
	}
	return v.Node.toDevice(), nil
}

func (c *headscaleCP) ApproveAuth(authID string) error {
	_, err := doJSON("POST", c.url("/api/v1/auth/approve"), c.bearer(),
		map[string]any{"authId": authID}, nil)
	return err
}

func (c *headscaleCP) RejectAuth(authID string) error {
	_, err := doJSON("POST", c.url("/api/v1/auth/reject"), c.bearer(),
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
