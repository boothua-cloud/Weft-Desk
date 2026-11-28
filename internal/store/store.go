package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/boothua/weft/internal/appinfo"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/secret"
	_ "modernc.org/sqlite"
)

// Store 是本地 SQLite 存储。注意：凭证不入库，见 secrets.go。
type Store struct{ db *sql.DB }

func Open() (*Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "Weft")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "weft.db"))
	if err != nil {
		return nil, err
	}
	return NewWithDB(db)
}

// NewWithDB 用给定的连接建 Store。Open 走文件库，测试走内存库。
func NewWithDB(db *sql.DB) (*Store, error) {
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() { _ = s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS apps (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL,
  grp TEXT NOT NULL DEFAULT '', icon TEXT NOT NULL DEFAULT '',
  icon_data TEXT NOT NULL DEFAULT '', icon_error TEXT NOT NULL DEFAULT '',
  last_open TEXT NOT NULL DEFAULT '',
  browser INTEGER NOT NULL DEFAULT 0, sort INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS policy_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT, content TEXT NOT NULL, etag TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, action TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS policy_templates (
  id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, content TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);CREATE TABLE IF NOT EXISTS cache (
  k TEXT PRIMARY KEY, v TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS forwards (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'custom',
  target TEXT NOT NULL, target_port INTEGER NOT NULL, local_port INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0, sort INTEGER NOT NULL DEFAULT 0
);`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// 老库补列：ALTER 在列已存在时会报错，这里直接忽略。
	_, _ = s.db.Exec(`ALTER TABLE apps ADD COLUMN browser INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE apps ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE apps ADD COLUMN icon_data TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE apps ADD COLUMN last_open TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE apps ADD COLUMN icon_error TEXT NOT NULL DEFAULT ''`)
	// 审计补列：profile 记这条操作属于哪台服务端，target 记对象，
	// result/error 记成败。老记录只有 action/detail，读的时候兜底。
	_, _ = s.db.Exec(`ALTER TABLE audit ADD COLUMN profile TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE audit ADD COLUMN target TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE audit ADD COLUMN result TEXT NOT NULL DEFAULT 'ok'`)
	_, _ = s.db.Exec(`ALTER TABLE audit ADD COLUMN error TEXT NOT NULL DEFAULT ''`)
	// 空时间当「现在」处理：升级上来的书签默认算本机最新，
	// 第一次同步不会被对端覆盖掉。
	_, _ = s.db.Exec(`UPDATE apps SET updated_at=? WHERE updated_at=''`, time.Now().Format(time.RFC3339))

	// 不写入任何示例数据：应用书签与转发规则都由用户自己创建。
	// 老版本会补种示例，这里做一次性清理，避免删掉后又出现。
	return s.dropLegacyDemoData()
}

// dropLegacyDemoData 清掉旧版本写入的示例数据，只执行一次。
// 只按固定 ID 删除内置示例，用户自己新增的记录不受影响。
func (s *Store) dropLegacyDemoData() error {
	var v string
	if err := s.db.QueryRow(`SELECT v FROM kv WHERE k='demo.cleaned'`).Scan(&v); err == nil {
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM apps WHERE id IN ('a1','a2','a3','a4','a5','a6')`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM forwards WHERE id IN ('fwd-ssh','fwd-mysql','fwd-pg','fwd-redis')`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO kv(k,v) VALUES('demo.cleaned','1') ON CONFLICT(k) DO UPDATE SET v=excluded.v`)
	return err
}

func (s *Store) GetConfig() (model.Config, error) {
	c := model.Config{
		ControlPlane: "headscale", BaseURL: "https://hs.example.com", Tailnet: "-",
		AuthMode: "apikey", APIStyle: "auto", TunnelEngine: "system", Hostname: appinfo.DefaultHostname(),
		AutoConnect: true, AutoReconnect: true, MinimizeToTray: true, Demo: false,
		SocksEnabled: true, SocksPort: 1080, AutoForward: true, ForwardBind: "127.0.0.1",
	}
	var raw string
	err := s.db.QueryRow(`SELECT v FROM kv WHERE k='config'`).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return c, err
	}
	// 没有存量配置时 c 就是上面那份默认值，同样要走一遍下面的补全逻辑
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return c, err
		}
	}
	// 兼容旧配置文件：新增字段补默认值，否则 SOCKS5 端口会是 0。
	if c.SocksPort == 0 {
		c.SocksPort = 1080
	}
	// 老配置没有这个字段：默认只给本机用，避免升级后突然对内网开放
	if c.ForwardBind != "0.0.0.0" {
		c.ForwardBind = "127.0.0.1"
	}
	if c.SyncPort == 0 {
		c.SyncPort = appinfo.DefaultSyncPort
	}
	// 从单服务端升级上来：把现有配置收成第一个 profile
	if len(c.Profiles) == 0 {
		id := "default"
		c.Profiles = []model.Profile{{
			ID:              id,
			Name:            ProfileLabel(c.BaseURL, "默认服务端"),
			ControlPlane:    c.ControlPlane,
			BaseURL:         c.BaseURL,
			Tailnet:         c.Tailnet,
			AuthMode:        c.AuthMode,
			AllowInsecure:   c.AllowInsecure,
			Hostname:        c.Hostname,
			CACert:          c.CACert,
			ControlInsecure: c.ControlInsecure,
		}}
		c.ActiveID = id
	}
	if c.ActiveID == "" || !hasProfile(c.Profiles, c.ActiveID) {
		c.ActiveID = c.Profiles[0].ID
	}
	c.ProfileID = c.ActiveID
	// 离线预览已移除，旧配置里残留的 demo=true 一律忽略。
	c.Demo = false
	c.AuthKey = ""
	return c, nil
}

// SaveConfig 会剥掉凭证字段，凭证只由 SetCredential 写入系统钥匙串。
func (s *Store) SaveConfig(c model.Config) error {
	c.AuthKey = ""
	c.Demo = false
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO kv(k,v) VALUES('config',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, string(raw))
	return err
}

func (s *Store) ListApps() ([]model.AppBookmark, error) {
	rows, err := s.db.Query(`SELECT id,name,url,grp,icon,icon_data,icon_error,browser,updated_at,last_open FROM apps ORDER BY sort`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AppBookmark{}
	for rows.Next() {
		var a model.AppBookmark
		var browser int
		if err = rows.Scan(&a.ID, &a.Name, &a.URL, &a.Group, &a.Icon, &a.IconData, &a.IconError,
			&browser, &a.UpdatedAt, &a.LastOpen); err != nil {
			return nil, err
		}
		a.Browser = browser != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SaveApp(a model.AppBookmark) error {
	a.UpdatedAt = time.Now().Format(time.RFC3339)
	return s.SaveAppRaw(a)
}

// SaveAppRaw 写入时不动 UpdatedAt，供同步合并保留对端的修改时间。
//
// 图标、图标错误、打开时间不在这条语句里：它们各有各的写入方法，
// 表单保存时不该顺手把图标覆盖成空值（表单里的 iconData 只是用来预览）。
func (s *Store) SaveAppRaw(a model.AppBookmark) error {
	browser := 0
	if a.Browser {
		browser = 1
	}
	_, err := s.db.Exec(`INSERT INTO apps(id,name,url,grp,icon,browser,sort,updated_at)
      VALUES(?,?,?,?,?,?, (SELECT COALESCE(MAX(sort),0)+1 FROM apps), ?)
      ON CONFLICT(id) DO UPDATE SET name=excluded.name,url=excluded.url,grp=excluded.grp,
        icon=excluded.icon,browser=excluded.browser,updated_at=excluded.updated_at`,
		a.ID, a.Name, a.URL, a.Group, a.Icon, browser, a.UpdatedAt)
	return err
}

// SetAppIcon 只写图标，不动 UpdatedAt：图标是本机抓的缓存，
// 不该因为它让一次同步把对端的书名改动判成「本机更新」。
// 写成功意味着上一轮的失败记录已经过时，一并清掉。
func (s *Store) SetAppIcon(id, data string) error {
	_, err := s.db.Exec(`UPDATE apps SET icon_data=?, icon_error='' WHERE id=?`, data, id)
	return err
}

// SetAppIconError 记下这次没抓到的原因，图标本身不动：
// 之前抓到过的旧图标比一个空色块有用，用户重试之前还能继续用。
func (s *Store) SetAppIconError(id, reason string) error {
	_, err := s.db.Exec(`UPDATE apps SET icon_error=? WHERE id=?`, reason, id)
	return err
}

// ClearAppIcon 把图标退回色块加首字，同时清掉失败记录。
// 抓到的图标一直不合适时，用户需要一个「不要图标」的出口。
func (s *Store) ClearAppIcon(id string) error {
	_, err := s.db.Exec(`UPDATE apps SET icon_data='', icon_error='' WHERE id=?`, id)
	return err
}

// MarkAppOpened 记一次打开时间。同样不动 UpdatedAt，理由同上。
func (s *Store) MarkAppOpened(id string) error {
	_, err := s.db.Exec(`UPDATE apps SET last_open=? WHERE id=?`,
		time.Now().Format(time.RFC3339), id)
	return err
}

// ReorderApps 按传入的 ID 顺序重写 sort。事务里做，中途失败不会留下
// 一半新一半旧的顺序。
func (s *Store) ReorderApps(ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE apps SET sort=? WHERE id=?`, i+1, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteApp(id string) error {
	_, err := s.db.Exec(`DELETE FROM apps WHERE id=?`, id)
	return err
}

// ---------- 列表缓存 ----------
// 服务端数据落 SQLite：下次打开秒出、断网或 401 时还能看到上次同步到的真实数据。

func (s *Store) CachePut(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO cache(k,v,updated_at) VALUES(?,?,?)
      ON CONFLICT(k) DO UPDATE SET v=excluded.v, updated_at=excluded.updated_at`,
		key, string(b), time.Now().Format(time.RFC3339))
	return err
}

func (s *Store) CacheRead(key string, v any) (string, bool) {
	var raw, ts string
	if err := s.db.QueryRow(`SELECT v,updated_at FROM cache WHERE k=?`, key).Scan(&raw, &ts); err != nil {
		return "", false
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return "", false
	}
	return ts, true
}

// ---------- 端口转发规则 ----------

func (s *Store) ListForwards() ([]model.ForwardRule, error) {
	rows, err := s.db.Query(`SELECT id,name,kind,target,target_port,local_port,enabled FROM forwards ORDER BY sort`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ForwardRule{}
	for rows.Next() {
		var r model.ForwardRule
		var on int
		if err = rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Target, &r.TargetPort, &r.LocalPort, &on); err != nil {
			return nil, err
		}
		r.Enabled = on != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveForward 用固定 sort 保留用户排列；新增时排到末尾。
func (s *Store) SaveForward(r model.ForwardRule, sortHint int) error {
	on := 0
	if r.Enabled {
		on = 1
	}
	_, err := s.db.Exec(`INSERT INTO forwards(id,name,kind,target,target_port,local_port,enabled,sort)
      VALUES(?,?,?,?,?,?,?,?)
      ON CONFLICT(id) DO UPDATE SET name=excluded.name,kind=excluded.kind,target=excluded.target,
        target_port=excluded.target_port,local_port=excluded.local_port,enabled=excluded.enabled`,
		r.ID, r.Name, r.Kind, r.Target, r.TargetPort, r.LocalPort, on, sortHint)
	return err
}

func (s *Store) DeleteForward(id string) error {
	_, err := s.db.Exec(`DELETE FROM forwards WHERE id=?`, id)
	return err
}

func (s *Store) AddPolicyHistory(content, etag string) error {
	_, err := s.db.Exec(`INSERT INTO policy_history(content,etag,created_at) VALUES(?,?,?)`,
		content, etag, time.Now().Format(time.RFC3339))
	return err
}

// AddAudit 记一条写操作。失败也记：排查「点了没反应」时，
// 一条 result=fail 带错误原文的记录比什么线索都没有强。
// err 为 nil 表示成功。
func (s *Store) AddAudit(profile, action, target string, err error) {
	result, msg := "ok", ""
	if err != nil {
		result = "fail"
		msg = err.Error()
		if len(msg) > 600 {
			msg = msg[:600] + "…"
		}
	}
	_, _ = s.db.Exec(`INSERT INTO audit(profile,action,target,result,error,detail,created_at)
      VALUES(?,?,?,?,?,'',?)`,
		profile, action, target, result, msg, time.Now().Format(time.RFC3339))
	// 只留最近 2000 条：再多翻不动，也没人会去翻
	_, _ = s.db.Exec(`DELETE FROM audit WHERE id NOT IN (
      SELECT id FROM audit ORDER BY id DESC LIMIT 2000)`)
}

// ListAudit 返回最近的记录，新的在前。limit<=0 时给 2000 条（够页面自己筛）。
func (s *Store) ListAudit(limit int) ([]model.AuditEvent, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := s.db.Query(`SELECT created_at,action,
      CASE WHEN target<>'' THEN target ELSE detail END,
      profile,target,result,error
      FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditEvent{}
	for rows.Next() {
		var e model.AuditEvent
		if err = rows.Scan(&e.Time, &e.Action, &e.Detail, &e.Profile, &e.Target, &e.Result, &e.Error); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------- 策略模板 ----------

// PolicyTemplate 是一份存下来的策略，可以套到任意服务端上。
type PolicyTemplate struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Source  string `json:"source"`
	At      string `json:"at"`
}

func (s *Store) ListPolicyTemplates() ([]PolicyTemplate, error) {
	rows, err := s.db.Query(`SELECT id,name,content,source,created_at FROM policy_templates ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PolicyTemplate{}
	for rows.Next() {
		var t PolicyTemplate
		if err = rows.Scan(&t.ID, &t.Name, &t.Content, &t.Source, &t.At); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SavePolicyTemplate 同名覆盖：模板不是历史，留一堆同名版本没有意义。
func (s *Store) SavePolicyTemplate(name, content, source string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("模板要有个名字")
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM policy_templates WHERE name=?`, name).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(`UPDATE policy_templates SET content=?,source=?,created_at=? WHERE id=?`,
			content, source, time.Now().Format(time.RFC3339), id)
		return id, err
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO policy_templates(name,content,source,created_at) VALUES(?,?,?,?)`,
		name, content, source, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeletePolicyTemplate(id int64) error {
	_, err := s.db.Exec(`DELETE FROM policy_templates WHERE id=?`, id)
	return err
}

func (s *Store) PolicyTemplate(id int64) (PolicyTemplate, error) {
	var t PolicyTemplate
	err := s.db.QueryRow(`SELECT id,name,content,source,created_at FROM policy_templates WHERE id=?`, id).
		Scan(&t.ID, &t.Name, &t.Content, &t.Source, &t.At)
	if err == sql.ErrNoRows {
		return t, fmt.Errorf("这份模板已经删了")
	}
	return t, err
}

// profileLabel 给服务端起个能认出来的名字：取域名，没有就用兜底。
func ProfileLabel(baseURL, fallback string) string {
	u := strings.TrimSpace(baseURL)
	if u == "" {
		return fallback
	}
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	u = strings.TrimSuffix(u, "/")
	if i := strings.IndexByte(u, '/'); i > 0 {
		u = u[:i]
	}
	if u == "" {
		return fallback
	}
	return u
}

func hasProfile(list []model.Profile, id string) bool {
	for _, p := range list {
		if p.ID == id {
			return true
		}
	}
	return false
}

// profileSecrets 把当前激活的凭证搬到钥匙串里属于该 profile 的条目下。
func ProfileSecrets(id string) {
	if id == "" {
		return
	}
	if v := secret.Get(secret.Main); v != "" {
		_ = secret.Set(secret.Main+"."+id, v)
	}
	if v := secret.Get(secret.Auth); v != "" {
		_ = secret.Set(secret.Auth+"."+id, v)
	}
}

// loadProfileSecrets 把目标 profile 的凭证拷回固定条目，其余代码继续读固定 key。
func LoadProfileSecrets(id string) {
	if id == "" {
		return
	}
	if v := secret.Get(secret.Main + "." + id); v != "" {
		_ = secret.Set(secret.Main, v)
	} else {
		_ = secret.Set(secret.Main, "")
	}
	if v := secret.Get(secret.Auth + "." + id); v != "" {
		_ = secret.Set(secret.Auth, v)
	} else {
		_ = secret.Set(secret.Auth, "")
	}
}

// ---------- 策略历史 ----------

// ListPolicyHistory 返回最近的版本，新的在前。只给摘要，内容单取。
func (s *Store) ListPolicyHistory(limit int) ([]model.PolicyVersion, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id,created_at,content FROM policy_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PolicyVersion{}
	for rows.Next() {
		var v model.PolicyVersion
		var body string
		if err = rows.Scan(&v.ID, &v.At, &body); err != nil {
			return nil, err
		}
		v.Size = len(body)
		v.Lines = strings.Count(body, "\n") + 1
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetPolicyVersion 取某一版的原文。
func (s *Store) GetPolicyVersion(id int64) (string, error) {
	var body string
	err := s.db.QueryRow(`SELECT content FROM policy_history WHERE id=?`, id).Scan(&body)
	if err != nil {
		return "", fmt.Errorf("这一版已经不在了")
	}
	return body, nil
}

// PrunePolicyHistory 只留最近 keep 版。策略文件不大，留着占不了多少地方，
// 但堆到几百版翻起来就废了。
func (s *Store) PrunePolicyHistory(keep int) error {
	if keep <= 0 {
		keep = 20
	}
	_, err := s.db.Exec(`DELETE FROM policy_history WHERE id NOT IN (
      SELECT id FROM policy_history ORDER BY id DESC LIMIT ?)`, keep)
	return err
}
