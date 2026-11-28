// Package appsync 是应用书签在两台设备之间的同步。
//
// 本机在 tailnet 地址上开一个小 HTTP 服务，对端凭同步码拉取或推送；
// 两边按地址合并，取更新时间较新的那条。删除不传播。
package appsync

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boothua/weft/internal/appinfo"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/store"
)

// 同步码字母表去掉 I O 0 1，念给另一台设备或手输时不容易错。
const syncAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Place 返回本机在 tailnet 里的地址和名字，引擎没起来时返回空。
type Place func() (ip, host string)

// Server 是本机在 tailnet 上的监听，只在开关打开且隧道起来后存在。
type Server struct {
	st     *store.Store
	token  func() string
	place  Place
	listen func(port int) (net.Listener, error)

	mu     sync.Mutex
	ln     net.Listener
	srv    *http.Server
	cancel context.CancelFunc
	err    string
}

func New(st *store.Store, token func() string, place Place, listen func(port int) (net.Listener, error)) *Server {
	return &Server{st: st, token: token, place: place, listen: listen}
}

// Start 开始监听。节点没上线时会等，成功后才真正服务。
func (s *Server) Start(port int) {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.err = cancel, ""
	s.mu.Unlock()

	srv := &http.Server{Handler: s.Mux(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		ln, err := s.listen(port)
		if err != nil {
			s.fail(err)
			return
		}
		if ctx.Err() != nil {
			_ = ln.Close()
			return
		}
		s.mu.Lock()
		s.ln, s.srv = ln, srv
		s.mu.Unlock()
		log.Printf("[sync] 已在 tailnet 监听 :%d", port)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.fail(err)
		}
	}()
}

func (s *Server) fail(err error) {
	log.Printf("[sync] 监听失败: %v", err)
	s.mu.Lock()
	s.err = err.Error()
	s.ln, s.srv, s.cancel = nil, nil, nil
	s.mu.Unlock()
}

func (s *Server) Stop() {
	s.mu.Lock()
	cancel, h, ln := s.cancel, s.srv, s.ln
	s.cancel, s.srv, s.ln = nil, nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if h != nil {
		_ = h.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
}

func (s *Server) State() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel != nil && s.ln != nil, s.err
}

// ---------- 同步码 ----------

// NewToken 生成 16 位同步码，形如 7KQ2-M4XR-9TPD-HB8N。
func NewToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	out := make([]byte, 16)
	for i := range out {
		out[i] = syncAlphabet[int(b[i])%len(syncAlphabet)]
	}
	v := string(out)
	return v[0:4] + "-" + v[4:8] + "-" + v[8:12] + "-" + v[12:16]
}

// NormalizeToken 去掉分隔符和大小写差异，输入时不用管格式。
func NormalizeToken(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(raw)) {
		if r == '-' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ---------- 对端接口（本机是被拉取的一方） ----------

func (s *Server) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/info", s.guard(func(w http.ResponseWriter, r *http.Request) {
		_, host := s.place()
		apps, _ := s.st.ListApps()
		writeJSON(w, map[string]any{
			"app":      "云栈",
			"version":  appinfo.Version,
			"hostname": host,
			"count":    len(apps),
			"enabled":  true,
		})
	}))
	mux.HandleFunc("/v1/apps", s.guard(s.handleApps))
	return mux
}

// guard 校验同步码。对端要么带 Authorization: Bearer，要么带 X-Weft-Token。
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Weft-Token")
		if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			got = v
		}
		want := s.token()
		if want == "" || subtle.ConstantTimeCompare([]byte(NormalizeToken(got)), []byte(NormalizeToken(want))) != 1 {
			writeErr(w, http.StatusUnauthorized, "同步码不对")
			return
		}
		next(w, r)
	}
}

type payload struct {
	Host    string              `json:"host"`
	Version string              `json:"version"`
	Apps    []model.AppBookmark `json:"apps"`
	At      string              `json:"at"`
}

// stripLocalOnly 抹掉不该跟着同步跑的两样：
//   - IconData 是本机抓的图片缓存，一个图标几十上百 KB，几十个书签
//     就能把 1MB 的请求体顶满；
//   - LastOpen 是「本机什么时候点过」，换台设备就失去意义。
//
// 对端收到后自己会补图标，打开时间从零开始记。
func stripLocalOnly(apps []model.AppBookmark) []model.AppBookmark {
	out := make([]model.AppBookmark, len(apps))
	for i, a := range apps {
		a.IconData, a.IconError, a.LastOpen = "", "", ""
		out[i] = a
	}
	return out
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		apps, err := s.st.ListApps()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_, host := s.place()
		writeJSON(w, payload{Host: host, Version: appinfo.Version, Apps: stripLocalOnly(apps), At: time.Now().Format(time.RFC3339)})

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var p payload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, "内容读不出来: "+err.Error())
			return
		}
		local, err := s.st.ListApps()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		merged, added, updated, skipped := Merge(local, p.Apps)
		if err := s.saveAll(merged); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if added+updated > 0 {
			s.st.AddAudit("", "接收应用同步", fmt.Sprintf("来自 %s：新增 %d、更新 %d", p.Host, added, updated), nil)
		}
		writeJSON(w, model.SyncResult{Peer: p.Host, Added: added, Updated: updated, Skipped: skipped, Total: len(merged)})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "只支持 GET 和 POST")
	}
}

func (s *Server) saveAll(apps []model.AppBookmark) error {
	for _, a := range apps {
		if err := s.st.SaveAppRaw(a); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 本机主动同步 ----------

// Pull 从另一台设备拉取书签并合并进本机。
func (s *Server) Pull(target, token string) (model.SyncResult, error) {
	body, err := s.request(http.MethodGet, target, token, nil)
	if err != nil {
		return model.SyncResult{}, err
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return model.SyncResult{}, fmt.Errorf("对方返回的内容看不懂，确认地址和端口对不对")
	}
	local, err := s.st.ListApps()
	if err != nil {
		return model.SyncResult{}, err
	}
	merged, added, updated, skipped := Merge(local, p.Apps)
	if err := s.saveAll(merged); err != nil {
		return model.SyncResult{}, err
	}
	name := p.Host
	if name == "" {
		name = target
	}
	if added+updated > 0 {
		s.st.AddAudit("", "拉取应用同步", fmt.Sprintf("来自 %s：新增 %d、更新 %d", name, added, updated), nil)
	}
	return model.SyncResult{Peer: name, Added: added, Updated: updated, Skipped: skipped, Total: len(merged)}, nil
}

// Push 把本机书签推到另一台，由对方合并。
func (s *Server) Push(target, token string) (model.SyncResult, error) {
	apps, err := s.st.ListApps()
	if err != nil {
		return model.SyncResult{}, err
	}
	_, host := s.place()
	p := payload{Host: host, Version: appinfo.Version, Apps: stripLocalOnly(apps), At: time.Now().Format(time.RFC3339)}
	raw, err := json.Marshal(p)
	if err != nil {
		return model.SyncResult{}, err
	}
	body, err := s.request(http.MethodPost, target, token, raw)
	if err != nil {
		return model.SyncResult{}, err
	}
	var res model.SyncResult
	if err := json.Unmarshal(body, &res); err != nil {
		return model.SyncResult{}, fmt.Errorf("对方返回的内容看不懂，确认地址和端口对不对")
	}
	if res.Peer == "" {
		res.Peer = target
	}
	if res.Added+res.Updated > 0 {
		s.st.AddAudit("", "推送应用同步", fmt.Sprintf("到 %s：新增 %d、更新 %d", res.Peer, res.Added, res.Updated), nil)
	}
	return res, nil
}

// request 发一次请求。地址可以是 100.x、MagicDNS 名，带不带端口都行。
func (s *Server) request(method, target, token string, body []byte) ([]byte, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("先填同步码")
	}
	u, err := URL(target)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+NormalizeToken(token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 %s：确认对方开着同步、两台都在同一个 tailnet 里", u)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s", e.Error)
		}
		return nil, fmt.Errorf("对方返回 %s", resp.Status)
	}
	return raw, nil
}

// URL 补上协议和默认端口。已经带端口的原样用。
// 显式写了 https 就按 https 走（对端可能挂了反代），不悄悄降级成 http。
func URL(target string) (string, error) {
	t := strings.TrimSpace(target)
	scheme := "http"
	if rest, ok := strings.CutPrefix(t, "https://"); ok {
		scheme, t = "https", rest
	} else {
		t = strings.TrimPrefix(t, "http://")
	}
	t = strings.TrimRight(t, "/")
	if t == "" {
		return "", fmt.Errorf("先选一台设备或填地址")
	}
	if i := strings.IndexByte(t, '/'); i >= 0 {
		t = t[:i]
	}
	if !strings.Contains(t, ":") {
		t = net.JoinHostPort(t, strconv.Itoa(appinfo.DefaultSyncPort))
	}
	return scheme + "://" + t, nil
}

// ---------- 合并 ----------

// appKey 把地址归一成比较用的键：忽略大小写的 host、末尾斜杠、空白。
func appKey(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimRight(u, "/")
	if i := strings.Index(u, "://"); i > 0 {
		scheme := strings.ToLower(u[:i])
		rest := u[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			return scheme + "://" + strings.ToLower(rest[:j]) + rest[j:]
		}
		return scheme + "://" + strings.ToLower(rest)
	}
	return strings.ToLower(u)
}

func appTime(a model.AppBookmark) time.Time {
	t, err := time.Parse(time.RFC3339, a.UpdatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Merge 按地址合并：同一地址在两边算同一条，取更新时间较新的那份。
// 不处理删除：对端没有的记录不会从本机被删掉。
func Merge(local, incoming []model.AppBookmark) (out []model.AppBookmark, added, updated, skipped int) {
	out = append([]model.AppBookmark{}, local...)
	index := make(map[string]int, len(out))
	for i, a := range out {
		if k := appKey(a.URL); k != "" {
			index[k] = i
		}
	}
	for _, in := range incoming {
		in.Name = strings.TrimSpace(in.Name)
		in.URL = strings.TrimSpace(in.URL)
		if in.Name == "" || in.URL == "" {
			skipped++
			continue
		}
		k := appKey(in.URL)
		if i, ok := index[k]; ok {
			cur := out[i]
			if !appTime(in).After(appTime(cur)) {
				skipped++
				continue
			}
			in.ID = cur.ID // 沿用本机 ID，避免同一地址出现两条
			// 图标和打开时间只属于本机：对端传过来的是空值，
			// 直接覆盖会让已经抓好的图标白丢一遍。
			in.IconData, in.IconError, in.LastOpen = cur.IconData, cur.IconError, cur.LastOpen
			out[i] = in
			updated++
			continue
		}
		// ID 一律在本机生成。对端的 ID 只说明它自己库里的那条，
		// 直接沿用的话，两台设备撞上同一个 ID 就会把本机另一条覆盖掉
		// （合并、复制数据库、早期版本的固定 ID 都可能撞）。
		in.ID = fmt.Sprintf("app-%d", time.Now().UnixNano()+int64(added))
		out = append(out, in)
		index[k] = len(out) - 1
		added++
	}
	return out, added, updated, skipped
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
