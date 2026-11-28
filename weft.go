package main

// WeftService 的装配与生命周期：结构体定义、构造、启动、重连、退出，
// 以及绑定层共用的小工具（JSON 编码、时间格式、缓存键）。
//
// 业务方法按域拆在同目录的 weft_*.go 里，都是同一个类型的方法。
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
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
