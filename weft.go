package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boothua/weft/internal/appinfo"
	"github.com/boothua/weft/internal/appsync"
	"github.com/boothua/weft/internal/controlplane"
	"github.com/boothua/weft/internal/engine"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/netio"
	"github.com/boothua/weft/internal/secret"
	"github.com/boothua/weft/internal/store"
	"github.com/boothua/weft/internal/update"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// WeftService 是前端唯一入口。所有方法以 JSON 字符串交换数据。
type WeftService struct {
	store     *store.Store
	app       *application.App
	mu        sync.Mutex
	cfg       model.Config
	cp        controlplane.ControlPlane
	engine    engine.TunnelEngine
	cap       model.Capabilities
	connected bool
	started   time.Time
	fw        *netio.Hub
	forwardOn bool // 隧道起来后端口映射是否应该监听
	// prefsHook 在界面偏好（托盘开关等）变更后回调，用于即时生效，不重建引擎。
	prefsHook func(model.Config)
	// revealHook 把启动时隐藏的主窗口显示出来，由前端在启动画面画好后调用。
	revealHook func()
	// devToolsHook 打开主窗口的 WebView 检查器。
	devToolsHook func()
	// autoPending 表示启动时的自动连接还没结束，界面据此显示加载态。
	autoPending bool
	// keyProbe 是「服务端认不认这把管理 Key」的最近一次结论。
	keyProbe keyProbe
	// upd 是自动更新的运行态（框架的 Updater + 最近一次检查结果）。
	upd *update.Manager
	// syncSrv 是应用书签同步的 tailnet 监听，只在开关打开时存在。
	syncSrv *appsync.Server
	// mainWindowID 是主界面窗口的 ID。0 表示还不知道（放行，避免误伤启动流程）。
	mainWindowID uint
}

// callerAllowed 给改配置、看凭证、删除这类方法用。
//
// 应用内窗口加载的是第三方内网页面，Wails v3.0.0 收到绑定调用时只看
// 「是不是主 frame」，不校验来源，所以那个页面（或它被 XSS 打穿的版本）
// 可以直接调到所有后端方法。这里按调用来源窗口再挡一道：
// 不是主界面发起的，一律不认。
func (s *WeftService) callerAllowed(ctx context.Context) bool {
	if s.mainWindowID == 0 {
		return true
	}
	w, ok := ctx.Value(application.WindowKey).(application.Window)
	if !ok || w == nil {
		// 托盘、启动流程这类不是从窗口发起的调用
		return true
	}
	return w.ID() == s.mainWindowID
}

var errNotMainWindow = errors.New("这个操作只能在云栈主界面里做")

// keyProbe 缓存管理 Key 的判定结果。
// 只有服务端明确回 401/403 才记 bad；网络不通记成「还没结论」，
// 界面按乐观处理 —— 离线时把整组入口藏起来会被当成软件坏了。
type keyProbe struct {
	key  string    // 判定时用的钥匙，换了就作废
	bad  bool      // 服务端明确拒绝
	msg  string    // 拒绝原因，界面直接显示
	at   time.Time // 上次尝试的时间，避免离线时每 3 秒打一次接口
	busy bool      // 正在问，别重复发
}

// OpenDevTools 供界面里的「更多」菜单调用。
func (s *WeftService) OpenDevTools() error {
	s.mu.Lock()
	hook := s.devToolsHook
	s.mu.Unlock()
	if hook == nil {
		return fmt.Errorf("调试器不可用：需要用 devtools 标签构建")
	}
	hook()
	return nil
}

// RevealWindow 供前端在启动画面渲染完成后调用。
// 主窗口创建时是隐藏的，这样用户不会先看到一块空白底色。
func (s *WeftService) RevealWindow() error {
	s.mu.Lock()
	hook := s.revealHook
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func NewWeftService(store *store.Store) *WeftService {
	cfg, err := store.GetConfig()
	if err != nil {
		cfg = model.Config{ControlPlane: "headscale", TunnelEngine: "system", Hostname: appinfo.DefaultHostname(),
			SocksEnabled: true, SocksPort: 1080, AutoForward: true}
	}
	// cap 必须给个初值：零值里 CanCreateUser=false，前端会把写操作按钮全禁掉
	s := &WeftService{store: store, cfg: cfg, cap: defaultCapabilities()}
	s.syncSrv = appsync.New(store, func() string { return s.syncToken(false) }, s.selfPlace, s.listenTailnet)
	s.reload()
	s.mu.Lock()
	s.fw = netio.New(s.engine.Dialer())
	s.mu.Unlock()
	return s
}

// attach 是私有方法，不会被绑定生成器暴露给前端。
// 启动时自动拉起内嵌节点：App 关掉再打开时复用持久化身份，直接连回来。
func (s *WeftService) attach(app *application.App) {
	s.app = app
	s.autoConnect()
	s.watchAPIKeyExpiry()
}

// autoConnect 在启动与保存配置后复用，避免「配置好了还要手动再点一次连接」。
func (s *WeftService) autoConnect() {
	s.mu.Lock()
	cfg, eng := s.cfg, s.engine
	s.mu.Unlock()
	// 首次使用既没身份也没 Auth Key 时不自动连，否则一打开就弹管理员授权。
	// 用户主动点「连接」时才走首次授权流程。
	log.Printf("[boot] autoConnect 检查：autoConnect=%v engine=%v identity=%v authKey=%v baseURL=%s",
		cfg.AutoConnect, eng != nil, hasNodeIdentity(cfg), secret.Get(secret.Auth) != "", cfg.BaseURL)
	if !cfg.AutoConnect || eng == nil {
		log.Printf("[boot] 跳过自动连接：开关已关或引擎为空")
		return
	}
	if !hasNodeIdentity(cfg) && secret.Get(secret.Auth) == "" {
		log.Printf("[boot] 跳过自动连接：没有节点身份也没有 Auth Key（首次使用）")
		return
	}
	log.Printf("[boot] 开始自动连接")
	// 启动即连接：这段时间界面要显示「正在连接」，而不是「未连接」
	s.mu.Lock()
	s.autoPending = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.autoPending = false
			s.mu.Unlock()
		}()
		// 等前端首帧画完再建卡。启动遮罩要盖 2.5 秒，这段时间界面是看不见的，
		// Up() 只要四秒就完成了，等于用户从头到尾没见过「正在连接」。
		time.Sleep(1500 * time.Millisecond)
		if err := eng.Up(cfg); err != nil {
			log.Printf("自动连接失败: %v", err)
			return
		}
		s.mu.Lock()
		s.connected = true
		s.started = time.Now()
		s.mu.Unlock()
		if err := s.startForwards(); err != nil {
			log.Printf("自动拉起端口转发失败: %v", err)
		}
		s.startSyncServer()
	}()
}

// reload 在换引擎前先关掉旧节点、本地 HTTP 代理和端口监听，避免残留。
func (s *WeftService) reload() {
	s.mu.Lock()
	old, cfg, fw := s.engine, s.cfg, s.fw
	s.mu.Unlock()
	if fw != nil {
		fw.CloseAll()
	}
	s.stopSyncServer() // 旧引擎一关，监听也得跟着撤
	if old != nil {
		old.Shutdown()
	}
	s.mu.Lock()
	s.cp = controlplane.New(cfg)
	s.engine = engine.New(cfg)
	eng := s.engine
	s.forwardOn = false
	s.mu.Unlock()
	if fw != nil {
		fw.SetDialer(eng.Dialer())
	}
}

// shutdown 由 main 在退出时调用，确保 tsnet 节点和全部 127.0.0.1 端口都被释放。
func (s *WeftService) shutdown() {
	s.mu.Lock()
	e, fw := s.engine, s.fw
	s.forwardOn = false
	s.mu.Unlock()
	s.stopSyncServer()
	if fw != nil {
		fw.CloseAll()
	}
	if e != nil {
		e.Shutdown()
	}
}

// cachedList 先请求真实服务端；成功就写 SQLite 缓存，
// 失败则回退到上次同步的数据，前端会标注「离线缓存」。
// cacheKey 给列表缓存加服务端前缀。
// 之前所有 profile 共用 "devices" 这一个 key，切换服务端后一旦新服务端请求失败，
// 就会把上一个服务端的缓存当成当前数据显示出来。
func (s *WeftService) cacheKey(name string) string {
	s.mu.Lock()
	id := s.cfg.ActiveID
	s.mu.Unlock()
	if id == "" {
		id = "default"
	}
	return name + ":" + id
}

func cachedList[T any](st *store.Store, key string, fetch func() ([]T, error)) (string, error) {
	items, err := fetch()
	if err == nil {
		_ = st.CachePut(key, items)
		return toJSON(model.Cached[T]{Items: items, Source: "live", UpdatedAt: fmtTime(time.Now())})
	}
	var cached []T
	if ts, ok := st.CacheRead(key, &cached); ok {
		return toJSON(model.Cached[T]{Items: cached, Source: "cache", UpdatedAt: fmtTime(parseTS(ts)), Error: err.Error()})
	}
	return "", err
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// wrap 直接接收 (值, error) 双返回值，省掉每个方法里重复的 err 判断。
func wrap[T any](v T, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return toJSON(v)
}

// ---------- 配置与状态 ----------

func (s *WeftService) GetConfig() (string, error) {
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	return toJSON(c)
}

func (s *WeftService) SaveConfig(ctx context.Context, raw string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	var c model.Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return err
	}
	if c.ControlPlane == "" {
		c.ControlPlane = "headscale"
	}
	if c.TunnelEngine == "" {
		c.TunnelEngine = "system"
	}
	if c.Hostname == "" {
		c.Hostname = appinfo.DefaultHostname()
	}
	if c.SocksPort == 0 {
		c.SocksPort = 1080
	}
	if strings.TrimSpace(c.BaseURL) != "" {
		base, plain, err := controlplane.ValidateBaseURL(c.BaseURL)
		if err != nil {
			return err
		}
		c.BaseURL = base
		if plain {
			log.Printf("[cp] %s 用的是 http，管理密钥会明文过网", base)
		}
	}
	// 导入的配置、老版本留下的配置都可能带着解析不了的 CA。
	// 这里挡住，不然 reload 之后每次请求都只报一个看不懂的错误。
	if ca := strings.TrimSpace(c.CACert); ca != "" {
		if _, err := controlplane.CertPool(ca); err != nil {
			return err
		}
	}
	// 顶层字段是「当前激活 profile」的副本，改完要写回去，否则切走再切回会丢
	s.mu.Lock()
	activeID := s.cfg.ActiveID
	s.mu.Unlock()
	if c.ActiveID == "" {
		c.ActiveID = activeID
	}
	c.ProfileID = c.ActiveID
	syncProfileFromConfig(&c)
	if err := s.store.SaveConfig(c); err != nil {
		return err
	}
	if c.AuthKey != "" {
		if err := secret.Set(secret.Auth, c.AuthKey); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.cfg = c
	s.cfg.AuthKey = ""
	s.cap = defaultCapabilities()
	hook := s.prefsHook
	s.mu.Unlock()
	if hook != nil {
		hook(c)
	}
	s.reload()
	s.store.AddAudit("保存配置", c.ControlPlane+" · "+c.BaseURL)
	s.autoConnect()
	return nil
}

// ProfilesView 返回服务端列表和当前激活项，前端用来做切换器。
func (s *WeftService) ProfilesView() (string, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	return toJSON(map[string]any{
		"items":  cfg.Profiles,
		"active": cfg.ActiveID,
	})
}

// SwitchProfile 切到另一个 Headscale 服务端：断开当前隧道、换凭证、重连。
// 每个服务端在本地有独立的节点身份目录，不会把上一个 tailnet 的登录态带过去。
func (s *WeftService) SwitchProfile(id string) error {
	s.mu.Lock()
	cfg := s.cfg
	oldID := cfg.ActiveID
	s.mu.Unlock()

	if id == "" || id == oldID {
		return nil
	}
	idx := -1
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("没有这个服务端配置")
	}

	// 1) 当前凭证存回旧 profile
	cfg.ProfileID = oldID
	store.ProfileSecrets(oldID)

	// 2) 断开旧隧道（reload 里也会关，但先断开让界面立刻回到未连接）
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng != nil {
		eng.Shutdown()
	}

	// 3) 顶层字段换成目标 profile
	target := cfg.Profiles[idx]
	cfg.BaseURL = target.BaseURL
	cfg.Tailnet = target.Tailnet
	cfg.AuthMode = target.AuthMode
	cfg.AllowInsecure = target.AllowInsecure
	cfg.CACert = target.CACert
	cfg.ControlInsecure = target.ControlInsecure
	if target.Hostname != "" {
		cfg.Hostname = target.Hostname
	}
	if target.ControlPlane != "" {
		cfg.ControlPlane = target.ControlPlane
	}
	cfg.ActiveID = id
	cfg.ProfileID = id
	cfg.AuthKey = ""

	// 4) 目标凭证写回固定条目，其余代码继续读固定 key
	store.LoadProfileSecrets(id)

	if err := s.store.SaveConfig(cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.cap = defaultCapabilities()
	s.mu.Unlock()

	s.reload()
	s.store.AddAudit("切换服务端", target.Name+" · "+target.BaseURL)
	s.autoConnect()
	return nil
}

// SaveProfile 把当前连接配置另存为一个新的服务端条目，并立即切过去。
func (s *WeftService) SaveProfile(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("给这个服务端起个名字")
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	// 先把当前凭证归到当前 profile 名下，再复制一份给新 profile
	cfg.ProfileID = cfg.ActiveID
	store.ProfileSecrets(cfg.ActiveID)
	if v := secret.Get(secret.Main); v != "" {
		// 新条目一开始沿用同一份凭证，用户可在设置里改
		id := fmt.Sprintf("p%d", time.Now().UnixNano())
		p := model.Profile{
			ID:              id,
			Name:            name,
			ControlPlane:    cfg.ControlPlane,
			BaseURL:         cfg.BaseURL,
			Tailnet:         cfg.Tailnet,
			AuthMode:        cfg.AuthMode,
			AllowInsecure:   cfg.AllowInsecure,
			Hostname:        cfg.Hostname,
			CACert:          cfg.CACert,
			ControlInsecure: cfg.ControlInsecure,
		}
		cfg.Profiles = append(cfg.Profiles, p)
		if err := s.store.SaveConfig(cfg); err != nil {
			return err
		}
		s.mu.Lock()
		s.cfg = cfg
		s.mu.Unlock()
		return s.SwitchProfile(id)
	}
	return fmt.Errorf("先把当前服务端的凭证填好再保存")
}

// DeleteProfile 删掉一个服务端（不能删当前激活的）。
func (s *WeftService) DeleteProfile(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	keep := make([]model.Profile, 0, len(cfg.Profiles))
	found := false
	next := ""
	for _, p := range cfg.Profiles {
		if p.ID == id {
			found = true
			continue
		}
		if next == "" {
			next = p.ID
		}
		keep = append(keep, p)
	}
	if !found {
		return fmt.Errorf("没有这个服务端配置")
	}
	if len(keep) == 0 {
		return fmt.Errorf("至少保留一个服务端")
	}

	// 删的是当前这个：先把隧道断掉，再把激活项换成剩下的第一个
	deletingActive := cfg.ActiveID == id
	if deletingActive {
		s.mu.Lock()
		eng := s.engine
		s.mu.Unlock()
		if eng != nil {
			eng.Shutdown()
		}
		cfg.ActiveID = next
		cfg.ProfileID = next
		for _, p := range keep {
			if p.ID == next {
				cfg.BaseURL = p.BaseURL
				cfg.Tailnet = p.Tailnet
				cfg.AuthMode = p.AuthMode
				cfg.AllowInsecure = p.AllowInsecure
				cfg.CACert = p.CACert
				cfg.ControlInsecure = p.ControlInsecure
				if p.Hostname != "" {
					cfg.Hostname = p.Hostname
				}
				if p.ControlPlane != "" {
					cfg.ControlPlane = p.ControlPlane
				}
			}
		}
	}

	cfg.Profiles = keep
	if err := s.store.SaveConfig(cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.cap = defaultCapabilities()
	s.mu.Unlock()

	// 用 Delete 而不是写空串：钥匙串里不该留一堆空条目。
	// 服务端那把 Key 不顺手吊销 —— 同一个管理 Key 可能还在别的地方用，
	// 删本地配置不该把别人的钥匙作废。
	secret.Delete(secret.Main + "." + id)
	secret.Delete(secret.Auth + "." + id)

	if deletingActive {
		store.LoadProfileSecrets(next)
		s.reload()
		s.store.AddAudit("删除服务端", id)
		s.autoConnect()
	} else {
		s.store.AddAudit("删除服务端", id)
	}
	return nil
}

// profileInput 是服务端编辑弹窗提交的字段。
// 凭证留空表示不修改；要删掉得显式给 Clear*Key —— 以前「空 = 不改」既没法测
// 「没 Key 会怎样」，也没法把填错的那串删掉。
type profileInput struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	BaseURL         string `json:"baseURL"`
	Hostname        string `json:"hostname"`
	APIKey          string `json:"apiKey"`
	AuthKey         string `json:"authKey"`
	ClearAPIKey     bool   `json:"clearApiKey"`
	ClearAuthKey    bool   `json:"clearAuthKey"`
	AllowInsecure   bool   `json:"allowInsecure"`
	CACert          string `json:"caCert"`
	ControlInsecure bool   `json:"controlInsecure"`
}

// SaveProfileRaw 新建或更新一个服务端配置，返回它的 ID。
// 改的是当前激活那个时，会顺手重连，让新地址立刻生效。
func (s *WeftService) SaveProfileRaw(ctx context.Context, raw string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	var in profileInput
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return "", err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	if in.Name == "" {
		return "", fmt.Errorf("给这个服务端起个名字")
	}
	base, plain, err := controlplane.ValidateBaseURL(in.BaseURL)
	if err != nil {
		return "", err
	}
	in.BaseURL = base
	if plain {
		log.Printf("[cp] 服务端 %s 用的是 http，管理密钥会明文过网", base)
	}

	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	id := in.ID
	isNew := id == ""
	if isNew {
		id = fmt.Sprintf("p%d", time.Now().UnixNano())
	}
	changedActive := id == cfg.ActiveID

	// 当前激活的服务端：凭证还没归档的话先归到它名下，避免被覆盖
	if changedActive {
		cfg.ProfileID = cfg.ActiveID
		store.ProfileSecrets(cfg.ActiveID)
	}

	hostname := strings.TrimSpace(in.Hostname)
	if hostname == "" {
		hostname = appinfo.DefaultHostname()
	}
	// CA 现在就解析一遍，填错了当场报，别等连不上再猜
	if ca := strings.TrimSpace(in.CACert); ca != "" {
		if _, err := controlplane.CertPool(ca); err != nil {
			return "", err
		}
	}
	p := model.Profile{
		ID:              id,
		Name:            in.Name,
		ControlPlane:    "headscale",
		BaseURL:         in.BaseURL,
		Tailnet:         "-",
		AuthMode:        "apikey",
		AllowInsecure:   in.AllowInsecure,
		Hostname:        hostname,
		CACert:          strings.TrimSpace(in.CACert),
		ControlInsecure: in.ControlInsecure,
	}

	idx := -1
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			idx = i
			break
		}
	}
	if idx >= 0 {
		p.Tailnet = cfg.Profiles[idx].Tailnet
		p.AuthMode = cfg.Profiles[idx].AuthMode
		cfg.Profiles[idx] = p
	} else {
		cfg.Profiles = append(cfg.Profiles, p)
	}

	// 凭证：留空表示沿用原来的，要清掉得点弹窗里的「清空」
	if v := strings.TrimSpace(in.APIKey); v != "" {
		_ = secret.Set(secret.Main+"."+id, v)
	} else if in.ClearAPIKey {
		secret.Delete(secret.Main + "." + id)
	}
	if v := strings.TrimSpace(in.AuthKey); v != "" {
		_ = secret.Set(secret.Auth+"."+id, v)
	} else if in.ClearAuthKey {
		secret.Delete(secret.Auth + "." + id)
	}

	if changedActive {
		cfg.BaseURL = p.BaseURL
		cfg.Hostname = p.Hostname
		cfg.AllowInsecure = p.AllowInsecure
		cfg.CACert = p.CACert
		cfg.ControlInsecure = p.ControlInsecure
		cfg.ControlPlane = p.ControlPlane
		cfg.AuthKey = ""
		store.LoadProfileSecrets(id)
	}

	if err := s.store.SaveConfig(cfg); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.cap = defaultCapabilities()
	s.mu.Unlock()

	if changedActive {
		s.reload()
		s.autoConnect()
	}
	s.store.AddAudit("保存服务端", p.Name+" · "+p.BaseURL)
	return id, nil
}

// RenameProfile 只改显示名，不动连接参数，不会触发重连。
func (s *WeftService) RenameProfile(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("名字不能为空")
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	hit := false
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			cfg.Profiles[i].Name = name
			hit = true
			break
		}
	}
	if !hit {
		return fmt.Errorf("没有这个服务端配置")
	}
	if err := s.store.SaveConfig(cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

// syncProfileFromConfig 把顶层字段写回 profiles 里对应的那一项。
func syncProfileFromConfig(c *model.Config) {
	for i := range c.Profiles {
		if c.Profiles[i].ID != c.ActiveID {
			continue
		}
		c.Profiles[i].BaseURL = c.BaseURL
		c.Profiles[i].Tailnet = c.Tailnet
		c.Profiles[i].AuthMode = c.AuthMode
		c.Profiles[i].AllowInsecure = c.AllowInsecure
		c.Profiles[i].CACert = c.CACert
		c.Profiles[i].ControlInsecure = c.ControlInsecure
		c.Profiles[i].Hostname = c.Hostname
		if c.Profiles[i].ControlPlane == "" {
			c.Profiles[i].ControlPlane = c.ControlPlane
		}
		if c.Profiles[i].Name == "" {
			c.Profiles[i].Name = store.ProfileLabel(c.BaseURL, "服务端")
		}
		return
	}
}

// uiPrefsPatch 是界面偏好的增量更新。
// 这些字段改了不需要重建隧道引擎，所以单独走一条路径：
// 立刻落库、立刻生效，不必等用户点「保存配置」。
type uiPrefsPatch struct {
	AutoConnect    *bool `json:"autoConnect"`
	AutoReconnect  *bool `json:"autoReconnect"`
	MinimizeToTray *bool `json:"minimizeToTray"`
	DevTools       *bool `json:"devTools"`
}

// SetUIPrefs 只改偏好开关，不动 BaseURL / 引擎等需要重连的字段。
func (s *WeftService) SetUIPrefs(raw string) (string, error) {
	var p uiPrefsPatch
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return "", fmt.Errorf("偏好参数无法解析: %w", err)
	}
	s.mu.Lock()
	if p.AutoConnect != nil {
		s.cfg.AutoConnect = *p.AutoConnect
	}
	if p.AutoReconnect != nil {
		s.cfg.AutoReconnect = *p.AutoReconnect
	}
	if p.MinimizeToTray != nil {
		s.cfg.MinimizeToTray = *p.MinimizeToTray
	}
	if p.DevTools != nil {
		s.cfg.DevTools = *p.DevTools
	}
	cfg, hook := s.cfg, s.prefsHook
	s.mu.Unlock()

	if err := s.store.SaveConfig(cfg); err != nil {
		return "", err
	}
	if hook != nil {
		hook(cfg)
	}
	return toJSON(cfg)
}

// SetCredential 单独把服务端凭证写入系统钥匙串，绝不进 SQLite。
func (s *WeftService) SetCredential(ctx context.Context, kind, value string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	key := secret.Main
	switch kind {
	case "auth":
		key = secret.Auth
	case "client":
		key = secret.Client
	}
	if value == "" {
		secret.Delete(key)
		return nil
	}
	if err := secret.Set(key, value); err != nil {
		return err
	}
	s.store.AddAudit("更新凭证", kind)
	return nil
}

// RevealCredential 返回某个凭证的明文，供「显示」按钮用。
// 钥匙串本来就是为了不落明文，所以这里不做缓存，每次现读。
func (s *WeftService) RevealCredential(ctx context.Context, kind string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	key := secret.Main
	switch kind {
	case "auth":
		key = secret.Auth
	case "client":
		key = secret.Client
	}
	v := strings.TrimSpace(secret.Get(key))
	if v == "" {
		return "", fmt.Errorf("尚未设置")
	}
	return v, nil
}

func (s *WeftService) TestConnection() (string, error) {
	cap, err := s.cp.Probe()
	// 手动点「测试连接」得到的就是最新结论，直接记下来，
	// 省得界面等下一轮轮询才知道这把钥匙到底行不行
	key := strings.TrimSpace(secret.Get(secret.Main))
	s.mu.Lock()
	switch {
	case key == "":
		s.keyProbe = keyProbe{}
	case err == nil:
		s.keyProbe = keyProbe{key: key, at: time.Now()}
	case controlplane.IsRejected(err):
		s.keyProbe = keyProbe{key: key, bad: true, msg: controlplane.Short([]byte(err.Error())), at: time.Now()}
	}
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cap = cap
	s.mu.Unlock()
	s.store.AddAudit("测试连接", cap.ProbeMessage)
	return cap.ProbeMessage, nil
}

// parseExpires 兼容几种时间写法。
// APIKey.Expires 是 controlplane.FmtTS 出来的「2006-01-02 15:04」（空格分隔），
// 直接按 RFC3339 解析会失败，之前自动续期就是卡在这里。
func parseExpires(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("没有到期时间")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析到期时间 %q", s)
}

// EnsureAPIKeyFresh 在当前管理密钥快过期时自动换一把新的。
//
// headscale 没有「续期」接口，只能新建后替换。流程：
//  1. 列出所有 API Key，用前缀认出当前这把
//  2. 剩余天数小于阈值就新建一把（用旧 Key 授权），立刻写进钥匙串
//  3. 旧 Key 吊销，避免留着一把没人管的凭证
//
// 失败不会抛出到界面，只写审计；下次定时器还会再试。
func (s *WeftService) EnsureAPIKeyFresh(window time.Duration) (bool, error) {
	s.mu.Lock()
	cp, cfg := s.cp, s.cfg
	s.mu.Unlock()

	cur := strings.TrimSpace(secret.Get(secret.Main))
	if cur == "" || cp == nil {
		return false, nil
	}

	keys, err := cp.APIKeys()
	if err != nil {
		return false, err
	}
	var self *model.APIKey
	for i := range keys {
		if controlplane.KeyPrefixMatches(cur, keys[i].Prefix) {
			self = &keys[i]
			break
		}
	}
	if self == nil || self.Expires == "" {
		return false, nil // 认不出来或者永不过期，不动
	}
	exp, err := parseExpires(self.Expires)
	if err != nil {
		return false, err
	}
	left := time.Until(exp)
	if left > window {
		return false, nil
	}

	// 新 Key 的有效期给 90 天，比阈值宽得多
	fresh, err := cp.CreateAPIKey(90)
	if err != nil {
		return false, fmt.Errorf("创建新密钥失败: %w", err)
	}
	if err := secret.Set(secret.Main, fresh); err != nil {
		// 新钥匙没存下来就别留着它：服务端那边会变成一把 90 天有效、
		// 本地没有记录的管理凭据，谁也不知道它属于谁。
		// 明文本里没有服务端 ID，只能回列表里按前缀认。
		if keys, kerr := cp.APIKeys(); kerr == nil {
			for i := range keys {
				if !controlplane.KeyPrefixMatches(fresh, keys[i].Prefix) {
					continue
				}
				if eerr := cp.ExpireAPIKey(keys[i].Prefix, keys[i].ID); eerr != nil {
					log.Printf("[keys] 回滚新 API Key 失败，需要手动吊销 %s: %v", keys[i].Prefix, eerr)
				}
				break
			}
		}
		return false, fmt.Errorf("保存新密钥失败: %w", err)
	}
	// 旧 Key 已经没用了，吊销掉；失败不影响本次续期结果
	if err := cp.ExpireAPIKey(self.Prefix, self.ID); err != nil {
		log.Printf("[keys] 吊销旧 API Key 失败（新密钥已生效）: %v", err)
	}
	s.store.AddAudit("自动续期管理密钥", cfg.BaseURL+" · 剩余 "+left.Round(time.Hour).String())
	log.Printf("[keys] 管理密钥已自动续期，旧 Key %s 剩余 %v", self.Prefix, left.Round(time.Hour))
	return true, nil
}

// RenewAPIKey 强制换一把新的管理密钥（不看剩余天数），供界面上的「续期」按钮调用。
// days 是新的有效期，<=0 时按 90 天算。
func (s *WeftService) RenewAPIKey(days int) error {
	if days <= 0 {
		days = 90
	}
	s.mu.Lock()
	cp := s.cp
	s.mu.Unlock()
	if cp == nil {
		return fmt.Errorf("尚未配置服务端")
	}
	cur := strings.TrimSpace(secret.Get(secret.Main))
	if cur == "" {
		return fmt.Errorf("还没有管理密钥，请先在连接设置里填一个")
	}
	keys, err := cp.APIKeys()
	if err != nil {
		return err
	}
	var self *model.APIKey
	for i := range keys {
		if controlplane.KeyPrefixMatches(cur, keys[i].Prefix) {
			self = &keys[i]
			break
		}
	}
	fresh, err := cp.CreateAPIKey(days)
	if err != nil {
		return fmt.Errorf("创建新密钥失败: %w", err)
	}
	if err := secret.Set(secret.Main, fresh); err != nil {
		return fmt.Errorf("保存新密钥失败: %w", err)
	}
	if self == nil {
		// 认不出来就没法吊销旧的，留着两把有效密钥容易搞混
		return fmt.Errorf("新密钥已写入钥匙串，但没在服务端认出旧密钥，去 API Key 页面手动吊销旧的")
	}
	if err := cp.ExpireAPIKey(self.Prefix, self.ID); err != nil {
		log.Printf("[keys] 吊销旧密钥失败（新密钥已生效）: %v", err)
	}
	s.store.AddAudit("手动续期管理密钥", fmt.Sprintf("有效期重置为 %d 天", days))
	return nil
}

// watchAPIKeyExpiry 每小时检查一次，启动后先等半分钟再跑第一次，
// 免得跟自动连接抢带宽。
func (s *WeftService) watchAPIKeyExpiry() {
	go func() {
		time.Sleep(30 * time.Second)
		for {
			if _, err := s.EnsureAPIKeyFresh(7 * 24 * time.Hour); err != nil {
				log.Printf("[keys] 检查管理密钥到期时间失败: %v", err)
			}
			time.Sleep(time.Hour)
		}
	}()
}

// defaultCapabilities 是「还没探测过」时的取值。
// 写操作先放行 —— 真不支持的话服务端会回错，不会假成功；
// 一律给 false 反而会让 UI 把按钮禁掉，用户连试都试不了。
func defaultCapabilities() model.Capabilities {
	return model.Capabilities{
		APIStyle: "v1 REST", CanCreateUser: true,
		CanManageAPIKeys: true, CanManageACL: true,
	}
}

// GetLoginURL 主动要一次网页授权链接。
// 没配授权 Key 时首次连接必须走这条，界面在什么都看不到时给个按钮，
// 不用让用户猜是服务端没回还是界面没刷新。
func (s *WeftService) GetLoginURL() (string, error) {
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng == nil {
		return "", errors.New("还没有可用的引擎")
	}
	if req, ok := eng.(interface {
		RequestLogin(context.Context) (string, error)
	}); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return req.RequestLogin(ctx)
	}
	return eng.LoginURL(), nil
}

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
	s.keyProbe = keyProbe{key: p.key, at: time.Now(), busy: true}
	s.mu.Unlock()
	if cp == nil {
		return
	}
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
		return "", err
	}
	s.mu.Lock()
	s.connected = true
	s.started = time.Now()
	s.mu.Unlock()
	s.store.AddAudit("连接隧道", s.engine.Name())
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
		return "", err
	}
	s.mu.Lock()
	s.connected = false
	s.started = time.Time{}
	s.mu.Unlock()
	s.store.AddAudit("断开隧道", s.engine.Name())
	return s.GetState()
}

// ---------- 管理 API（Headscale v1） ----------

// manageErr 统一未配置/离线时的返回，避免每次都发一次必然 401 的请求。
func (s *WeftService) manageErr() string {
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	if !hasCredential(c) {
		return "未配置管理 API Key（headscale apikeys create 生成，不是 Auth Key）"
	}
	return ""
}

func (s *WeftService) ListDevices() (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.Device]{Items: []model.Device{}, Source: "none", Error: msg})
	}
	return cachedList(s.store, s.cacheKey("devices"), func() ([]model.Device, error) { return s.cp.Devices("") })
}

// ListDevicesByUser 支持按成员过滤（对应 ?user=）。
func (s *WeftService) ListDevicesByUser(user string) (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.Device]{Items: []model.Device{}, Source: "none", Error: msg})
	}
	return wrap(s.cp.Devices(user))
}

func (s *WeftService) GetDevice(id string) (string, error) {
	return wrap(s.cp.Device(id))
}

// RegisterNode 用 Auth Key 手动注册一个节点（对应 POST /node/register）。
func (s *WeftService) RegisterNode(user, key string) (string, error) {
	d, err := s.cp.RegisterNode(user, key)
	if err != nil {
		return "", err
	}
	s.store.AddAudit("注册节点", d.Name)
	return wrapOne(d)
}

func (s *WeftService) DeleteDevice(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteDevice(id); err != nil {
		return err
	}
	s.store.AddAudit("删除设备", id)
	return nil
}

func (s *WeftService) RenameDevice(id, name string) error {
	if err := s.cp.RenameDevice(id, name); err != nil {
		return err
	}
	s.store.AddAudit("重命名设备", id+" → "+name)
	return nil
}

func (s *WeftService) SetDeviceTags(id, tags string) error {
	if err := s.cp.SetDeviceTags(id, normalizeTags(tags)); err != nil {
		return err
	}
	s.store.AddAudit("设置设备标签", id+" · "+tags)
	return nil
}

func (s *WeftService) ExpireDevice(id string) error {
	if err := s.cp.ExpireDevice(id); err != nil {
		return err
	}
	s.store.AddAudit("设备登出", id)
	return nil
}

// DisableDeviceExpiry 取消密钥到期时间，让节点长期有效。
func (s *WeftService) DisableDeviceExpiry(id string) error {
	if err := s.cp.DisableExpiry(id); err != nil {
		return err
	}
	s.store.AddAudit("取消设备到期", id)
	return nil
}

func (s *WeftService) ApproveRoutes(ctx context.Context, id, routes string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.ApproveRoutes(id, splitList(routes)); err != nil {
		return err
	}
	s.store.AddAudit("批准路由", id+" · "+routes)
	return nil
}

// BackfillIPs 为历史节点补齐缺失的 IP，返回受影响节点数。
func (s *WeftService) BackfillIPs(confirmed bool) (int, error) {
	n, err := s.cp.BackfillIPs(confirmed)
	if err != nil {
		return 0, err
	}
	s.store.AddAudit("补齐节点 IP", fmt.Sprintf("%d 个节点", n))
	return n, nil
}

// DebugCreateNode 直接建一个调试节点，仅用于联调 ACL / 路由。
func (s *WeftService) DebugCreateNode(user, key, name, routes string) (string, error) {
	d, err := s.cp.DebugCreateNode(user, key, name, splitList(routes))
	if err != nil {
		return "", err
	}
	s.store.AddAudit("调试建节点", name)
	return wrapOne(d)
}

// ---------- 成员 ----------

func (s *WeftService) ListUsers() (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.User]{Items: []model.User{}, Source: "none", Error: msg})
	}
	return cachedList(s.store, s.cacheKey("users"), s.cp.Users)
}

func (s *WeftService) CreateUser(ctx context.Context, name, displayName, email, pictureURL string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	u, err := s.cp.CreateUser(name, displayName, email, pictureURL)
	if err != nil {
		return "", err
	}
	s.store.AddAudit("创建成员", u.RawName)
	return wrapOne(u)
}

func (s *WeftService) RenameUser(ctx context.Context, id, name string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.RenameUser(id, name); err != nil {
		return err
	}
	s.store.AddAudit("重命名成员", id+" → "+name)
	return nil
}

func (s *WeftService) DeleteUser(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteUser(id); err != nil {
		return err
	}
	s.store.AddAudit("删除成员", id)
	return nil
}

// ---------- 授权 Key ----------

func (s *WeftService) ListAuthKeys() (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.AuthKey]{Items: []model.AuthKey{}, Source: "none", Error: msg})
	}
	return cachedList(s.store, s.cacheKey("authkeys"), s.cp.AuthKeys)
}

func (s *WeftService) CreateAuthKey(ctx context.Context, user string, reusable, ephemeral bool, days int, tags string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	key, err := s.cp.CreateAuthKey(user, reusable, ephemeral, days, normalizeTags(tags))
	if err != nil {
		return "", err
	}
	s.store.AddAudit("生成授权 Key", user)
	return key, nil
}

func (s *WeftService) ExpireAuthKey(id string) error {
	if err := s.cp.ExpireAuthKey(id); err != nil {
		return err
	}
	s.store.AddAudit("失效授权 Key", id)
	return nil
}

func (s *WeftService) DeleteAuthKey(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteAuthKey(id); err != nil {
		return err
	}
	s.store.AddAudit("删除授权 Key", id)
	return nil
}

// ---------- API Key ----------

func (s *WeftService) ListAPIKeys() (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.APIKey]{Items: []model.APIKey{}, Source: "none", Error: msg})
	}
	return cachedList(s.store, s.cacheKey("apikeys"), s.cp.APIKeys)
}

func (s *WeftService) CreateAPIKey(ctx context.Context, days int) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	key, err := s.cp.CreateAPIKey(days)
	if err != nil {
		return "", err
	}
	s.store.AddAudit("创建 API Key", fmt.Sprintf("%d 天", days))
	return key, nil
}

func (s *WeftService) ExpireAPIKey(prefix, id string) error {
	if err := s.cp.ExpireAPIKey(prefix, id); err != nil {
		return err
	}
	s.store.AddAudit("失效 API Key", prefix)
	return nil
}

func (s *WeftService) DeleteAPIKey(ctx context.Context, prefix, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteAPIKey(prefix, id); err != nil {
		return err
	}
	s.store.AddAudit("删除 API Key", prefix)
	return nil
}

// ---------- 策略 ----------

func (s *WeftService) GetPolicy() (string, error) {
	p, err := s.cp.Policy()
	if err != nil {
		return "", err
	}
	return wrapOne(p)
}

func (s *WeftService) SavePolicy(ctx context.Context, content string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	// 先把线上现有的策略记下来，再覆盖。历史里存的是「曾经生效过」的版本，
	// 存新版本没有意义——它就在服务端上。
	old := ""
	if p, err := s.cp.Policy(); err == nil {
		old = p.Content
	}
	if err := s.cp.SavePolicy(content); err != nil {
		return err
	}
	s.store.AddAudit("保存 ACL 策略", fmt.Sprintf("%d 字节", len(content)))
	if old != "" && strings.TrimSpace(old) != strings.TrimSpace(content) {
		_ = s.store.AddPolicyHistory(old, "")
		_ = s.store.PrunePolicyHistory(20)
	}
	return nil
}

// ListPolicyHistory 返回最近保存前的版本，新的在前。
func (s *WeftService) ListPolicyHistory() (string, error) {
	items, err := s.store.ListPolicyHistory(20)
	if err != nil {
		return "", err
	}
	return wrapOne(items)
}

// GetPolicyVersion 取某一版原文，用于查看和对比。
func (s *WeftService) GetPolicyVersion(id int64) (string, error) {
	body, err := s.store.GetPolicyVersion(id)
	if err != nil {
		return "", err
	}
	return wrapOne(model.Policy{Content: body})
}

// RestorePolicy 把历史版本写回服务端。写之前当前版本会进历史，所以回滚也能后悔。
func (s *WeftService) RestorePolicy(ctx context.Context, id int64) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	body, err := s.store.GetPolicyVersion(id)
	if err != nil {
		return err
	}
	if err := s.SavePolicy(ctx, body); err != nil {
		return err
	}
	s.store.AddAudit("回滚 ACL 策略", fmt.Sprintf("回到第 %d 版", id))
	return nil
}

// CheckPolicy 只校验语法，不写入服务端。
func (s *WeftService) CheckPolicy(content string) error {
	return s.cp.CheckPolicy(content)
}

// ---------- 服务端状态 ----------

func (s *WeftService) GetHealth() (string, error) {
	return wrap(s.cp.Health())
}

// ---------- 节点认证 ----------

func (s *WeftService) RegisterAuth(user, authID string) (string, error) {
	d, err := s.cp.RegisterAuth(user, authID)
	if err != nil {
		return "", err
	}
	s.store.AddAudit("认证并注册节点", d.Name)
	return wrapOne(d)
}

func (s *WeftService) ApproveAuth(authID string) error {
	if err := s.cp.ApproveAuth(authID); err != nil {
		return err
	}
	s.store.AddAudit("批准节点认证", authID)
	return nil
}

func (s *WeftService) RejectAuth(authID string) error {
	if err := s.cp.RejectAuth(authID); err != nil {
		return err
	}
	s.store.AddAudit("拒绝节点认证", authID)
	return nil
}

// ---------- 通用助手 ----------

// wrapOne 把单个对象序列化成 JSON 字符串。
func wrapOne[T any](v T) (string, error) { return toJSON(v) }

// hasNodeIdentity 判断是否曾经真正连上过。
// 不用 tsnet 的 state 文件——它在 Start 时就会创建，会把「从没连成功」
// 误判成已注册，导致每次启动都自动去弹授权。
func hasNodeIdentity(c model.Config) bool {
	p := engine.ConnectedMarkPath(c)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// hasCredential 判断管理 API Key 是否已写入钥匙串。
func hasCredential(c model.Config) bool {
	k := strings.TrimSpace(secret.Get(secret.Main))
	if k == "" {
		return false
	}
	// 历史上可能把节点 Auth Key 填进了管理 Key 栏，这种情况不算已配置，
	// 否则每次都会拿它去请求管理 API，必然 401。
	if strings.HasPrefix(k, "hskey-auth-") || strings.HasPrefix(k, "tskey-auth-") {
		return false
	}
	return true
}

func splitList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// normalizeTags 补上 tag: 前缀，用户只输 server 也能用。
func normalizeTags(raw string) []string {
	list := splitList(raw)
	out := make([]string, 0, len(list))
	for _, t := range list {
		if !strings.HasPrefix(t, "tag:") {
			t = "tag:" + t
		}
		out = append(out, t)
	}
	return out
}

// ---------- 应用管理 ----------

func (s *WeftService) ListApps() (string, error) {
	return wrap(s.store.ListApps())
}

func (s *WeftService) SaveApp(raw string) error {
	var a model.AppBookmark
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return err
	}
	if a.ID == "" {
		a.ID = fmt.Sprintf("app-%d", time.Now().UnixNano())
	}
	return s.store.SaveApp(a)
}

func (s *WeftService) DeleteApp(id string) error { return s.store.DeleteApp(id) }

// OpenApp 用 Wails v3 多窗口新开独立 WebView，不走 iframe（会被 X-Frame-Options 拦截）。
// appTargetURL 校验书签地址并补上协议，只放行 http/https。
// 书签会从对端同步过来（apps_sync.go），不是纯本地输入；
// `file://` 会被 WebView 当本地文件打开，`-a Calculator` 这种值
// 传给 `open` / `rundll32` 会被当成参数而不是网址。
func appTargetURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("书签地址是空的")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("书签地址无效：%s", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("书签只支持 http / https：%s", raw)
	}
	return s, nil
}

func (s *WeftService) OpenApp(id string) error {
	apps, err := s.store.ListApps()
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.ID != id {
			continue
		}
		if s.app == nil {
			return fmt.Errorf("应用窗口尚未就绪")
		}
		// 标记「用系统浏览器打开」的书签，直接走浏览器：
		// 应用内是 WebView，加载不了浏览器扩展（如密码管理器自动填充）。
		if a.Browser {
			return s.OpenInBrowser(id)
		}
		raw, err := appTargetURL(a.URL)
		if err != nil {
			return err
		}
		target, err := s.engine.ResolveURL(raw)
		if err != nil {
			return err
		}
		// 先渲染一个加载页再导航：目标站点在内网，首字节可能要等一两秒，
		// 直接给 URL 会先白屏一段。直接给 http:// 也有概率被当成资源路径处理。
		w := s.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:            a.Name + " · 云栈",
			Width:            1100,
			Height:           720,
			HTML:             appLoadingHTML(a.Name),
			BackgroundColour: application.NewRGB(244, 246, 249),
		})
		if w == nil || w.ID() == 0 {
			return fmt.Errorf("创建应用窗口失败")
		}
		// 页面里塞一个浮动菜单：刷新 / 后退 / 复制地址 / 调试器 / 关窗
		s.attachOverlay(w)
		w.Center()
		w.Show()
		w.Focus()
		go func(win *application.WebviewWindow, u string) {
			// 让加载动画先完整露脸，再开始真正的导航
			time.Sleep(700 * time.Millisecond)
			win.SetURL(u)
		}(w, target)
		s.store.AddAudit("打开应用", a.Name)
		return nil
	}
	return fmt.Errorf("未找到应用: %s", id)
}

func (s *WeftService) OpenInBrowser(id string) error {
	apps, err := s.store.ListApps()
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.ID != id {
			continue
		}
		// 系统浏览器同样走本地代理，避免依赖浏览器自身的路由。
		raw, err := appTargetURL(a.URL)
		if err != nil {
			return err
		}
		openURL, err := s.engine.ResolveURL(raw)
		if err != nil {
			return err
		}
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", openURL)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", openURL)
		default:
			cmd = exec.Command("xdg-open", openURL)
		}
		return cmd.Start()
	}
	return fmt.Errorf("未找到应用: %s", id)
}

func (s *WeftService) ListAudit() (string, error) {
	return wrap(s.store.ListAudit())
}

// ---------- 本地端口转发：让系统里的 SSH / 数据库等外部工具也能进 tailnet ----------

// startForwards 在隧道起来后调用。AutoForward 关闭时交由用户手动点「启动转发」。
func (s *WeftService) startForwards() error {
	s.mu.Lock()
	auto := s.cfg.AutoForward
	s.mu.Unlock()
	if !auto {
		return nil
	}
	s.mu.Lock()
	s.forwardOn = true
	s.mu.Unlock()
	return s.syncForwards()
}

func (s *WeftService) stopForwards() {
	s.mu.Lock()
	s.forwardOn = false
	s.mu.Unlock()
	_ = s.syncForwards()
}

func (s *WeftService) syncForwards() error {
	s.mu.Lock()
	cfg, on, fw := s.cfg, s.forwardOn, s.fw
	s.mu.Unlock()
	if fw == nil {
		return nil
	}
	if !on {
		_ = fw.Apply(nil, false, cfg.SocksPort, cfg.ForwardBind)
		return nil
	}
	rules, err := s.store.ListForwards()
	if err != nil {
		return err
	}
	return fw.Apply(rules, cfg.SocksEnabled, cfg.SocksPort, cfg.ForwardBind)
}

func (s *WeftService) ListForwards() (string, error) {
	rules, err := s.store.ListForwards()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	cfg, on, fw, eng := s.cfg, s.forwardOn, s.fw, s.engine.Name()
	s.mu.Unlock()
	statuses := []model.ForwardStatus{}
	socks := model.ForwardStatus{ID: "socks5", State: "disabled"}
	if fw != nil {
		statuses, socks = fw.Statuses(rules, cfg.SocksEnabled && on, cfg.SocksPort)
	}
	return toJSON(model.ForwardView{
		Rules: rules, Statuses: statuses, Socks: socks,
		SocksEnabled: cfg.SocksEnabled, SocksPort: cfg.SocksPort,
		AutoForward: cfg.AutoForward, Power: on,
		Bind:   netio.NormalizeBind(cfg.ForwardBind),
		Engine: eng,
	})
}

func (s *WeftService) SaveForward(raw string) error {
	var r model.ForwardRule
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return err
	}
	if r.ID == "" {
		r.ID = fmt.Sprintf("fwd-%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Target) == "" {
		return fmt.Errorf("名称和目标地址不能为空")
	}
	if r.TargetPort < 1 || r.TargetPort > 65535 || r.LocalPort < 1 || r.LocalPort > 65535 {
		return fmt.Errorf("端口必须是 1–65535")
	}
	if r.Kind == "" {
		r.Kind = "custom"
	}
	if err := s.store.SaveForward(r, int(time.Now().UnixNano()/1e6)); err != nil {
		return err
	}
	s.store.AddAudit("保存端口映射", r.Name)
	return s.syncForwards()
}

func (s *WeftService) DeleteForward(id string) error {
	if err := s.store.DeleteForward(id); err != nil {
		return err
	}
	s.store.AddAudit("删除端口映射", id)
	return s.syncForwards()
}

func (s *WeftService) ToggleForward(id string, enabled bool) error {
	rules, err := s.store.ListForwards()
	if err != nil {
		return err
	}
	for i, r := range rules {
		if r.ID == id {
			r.Enabled = enabled
			if err = s.store.SaveForward(r, i); err != nil {
				return err
			}
			return s.syncForwards()
		}
	}
	return fmt.Errorf("未找到规则: %s", id)
}

// SetForwardOptions 一次保存三个全局开关，避免多次写库。
func (s *WeftService) SetForwardOptions(autoForward, socksEnabled bool, socksPort int) error {
	if socksPort < 1 || socksPort > 65535 {
		return fmt.Errorf("SOCKS5 端口必须是 1–65535")
	}
	s.mu.Lock()
	c := s.cfg
	c.AutoForward, c.SocksEnabled, c.SocksPort = autoForward, socksEnabled, socksPort
	s.mu.Unlock()
	if err := s.store.SaveConfig(c); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	return s.syncForwards()
}

// SetForwardBind 切换本地监听范围：仅本机（127.0.0.1）或同网段可达（0.0.0.0）。
// 切换会重建所有监听器，已经建立的连接会断。
func (s *WeftService) SetForwardBind(bind string) error {
	bind = netio.NormalizeBind(bind)
	s.mu.Lock()
	c := s.cfg
	c.ForwardBind = bind
	s.mu.Unlock()
	if err := s.store.SaveConfig(c); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	s.store.AddAudit("转发监听", map[string]string{"127.0.0.1": "仅本机", "0.0.0.0": "同网段可达"}[bind])
	return s.syncForwards()
}

// SetForwardPower 是「启动 / 停止转发」总开关，不影响引擎连接状态。
func (s *WeftService) SetForwardPower(on bool) error {
	s.mu.Lock()
	s.forwardOn = on
	s.mu.Unlock()
	s.store.AddAudit("转发开关", map[bool]string{true: "启动", false: "停止"}[on])
	return s.syncForwards()
}

func (s *WeftService) ForwardSnippet(id string) (string, error) {
	rules, err := s.store.ListForwards()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	port := s.cfg.SocksPort
	s.mu.Unlock()
	for _, r := range rules {
		if r.ID == id {
			return netio.Snippets(r, port)
		}
	}
	return "", fmt.Errorf("未找到规则: %s", id)
}

// TestPeer 用 TCP 直接探目标端口。默认走系统路由（TUN），
// 也可以指定 netstack 走进程内栈，用来区分「TUN 层不通」和「网络层不通」。
func (s *WeftService) TestPeer(host string, port int, via string) (string, error) {
	if host == "" || port < 1 || port > 65535 {
		return "", fmt.Errorf("请填主机与端口")
	}
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng == nil {
		return "", fmt.Errorf("引擎尚未就绪")
	}
	var d model.DialFunc
	label := "系统 TUN"
	if via == "netstack" {
		label = "netstack 用户态"
		if nd, ok := eng.(interface{ NetstackDialer() model.DialFunc }); ok {
			d = nd.NetstackDialer()
		}
	}
	if d == nil {
		d = eng.Dialer()
		if via == "netstack" {
			label = eng.Name() + " 引擎"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := d(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("%s:%d 连接失败（%v，%s）：%w",
			host, port, time.Since(start).Round(time.Millisecond), label, err)
	}
	_ = conn.Close()
	return fmt.Sprintf("%s:%d 连接成功，耗时 %v（%s）",
		host, port, time.Since(start).Round(time.Millisecond), label), nil
}

// ---------- 系统提权助手 ----------

// GetHelperStatus 让前端知道「打开就能连」是否已经成立。
func (s *WeftService) GetHelperStatus() (string, error) {
	return toJSON(engine.HelperStatus())
}

// InstallHelper 安装常驻助手：一次授权，之后连接零弹窗。
func (s *WeftService) InstallHelper() (string, error) {
	if err := engine.InstallHelper(); err != nil {
		return "", err
	}
	s.store.AddAudit("安装系统助手", helperLabelSafe())
	st, _ := toJSON(engine.HelperStatus())
	return st, nil
}

func (s *WeftService) UninstallHelper() (string, error) {
	if err := engine.UninstallHelper(); err != nil {
		return "", err
	}
	s.store.AddAudit("移除系统助手", helperLabelSafe())
	st, _ := toJSON(engine.HelperStatus())
	return st, nil
}

func helperLabelSafe() string {
	if h := engine.HelperStatus(); h.Installed {
		return "已安装"
	}
	return "未安装"
}

// appLoadingHTML 是应用内窗口在目标站点加载前显示的过渡页。
// 全部内联，不依赖外部资源，窗口一出现就能画出来。
func appLoadingHTML(name string) string {
	safe := html.EscapeString(name)
	icon := ""
	if len(appIcon) > 0 {
		icon = "data:image/png;base64," + base64.StdEncoding.EncodeToString(appIcon)
	}
	return `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><style>
html,body{margin:0;height:100%;background:#F4F6F9;font-family:-apple-system,BlinkMacSystemFont,"PingFang SC",system-ui,sans-serif;-webkit-font-smoothing:antialiased}
#l{position:fixed;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;
background:radial-gradient(560px 360px at 50% 12%,rgba(74,118,216,.13),transparent 70%),#F4F6F9}
.mark{width:60px;height:60px;border-radius:14px;object-fit:contain;
filter:drop-shadow(0 14px 26px rgba(16,26,60,.35));animation:lift 2s ease-in-out infinite}
.name{margin-top:16px;font-size:13px;color:#0F172A;font-weight:600}
.hint{margin-top:6px;font-size:11px;color:#8A94A3}
.bar{position:relative;margin-top:18px;width:140px;height:3px;border-radius:3px;background:#E3E8EF;overflow:hidden}
.bar i{position:absolute;top:0;bottom:0;left:0;width:46%;border-radius:3px;
background:linear-gradient(90deg,rgba(58,95,208,0),#3A5FD0 55%,#5A82E8);
animation:slide 1.2s cubic-bezier(.55,.1,.35,.9) infinite}
@keyframes lift{0%,100%{transform:translateY(0) scale(1)}50%{transform:translateY(-3px) scale(1.035)}}
@keyframes slide{0%{transform:translateX(-110%)}100%{transform:translateX(238%)}}
</style></head><body><div id="l">
<img class="mark" src="` + icon + `" alt="">
<div class="name">` + safe + `</div>
<div class="hint">正在加载内网地址…</div>
<div class="bar"><i></i></div>
</div></body></html>`
}
