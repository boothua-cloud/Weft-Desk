package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store 是本地 SQLite 存储。注意：凭证不入库，见 secrets.go。
type Store struct{ db *sql.DB }

func OpenStore() (*Store, error) {
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
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err = s.migrate(); err != nil {
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
  browser INTEGER NOT NULL DEFAULT 0, sort INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS policy_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT, content TEXT NOT NULL, etag TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, action TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cache (
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

func (s *Store) GetConfig() (Config, error) {
	c := Config{
		ControlPlane: "headscale", BaseURL: "https://hs.example.com", Tailnet: "-",
		AuthMode: "apikey", APIStyle: "auto", TunnelEngine: "system", Hostname: defaultHostname(),
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
		c.SyncPort = defaultSyncPort
	}
	// 从单服务端升级上来：把现有配置收成第一个 profile
	if len(c.Profiles) == 0 {
		id := "default"
		c.Profiles = []Profile{{
			ID:              id,
			Name:            profileLabel(c.BaseURL, "默认服务端"),
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
func (s *Store) SaveConfig(c Config) error {
	c.AuthKey = ""
	c.Demo = false
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO kv(k,v) VALUES('config',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, string(raw))
	return err
}

func (s *Store) ListApps() ([]AppBookmark, error) {
	rows, err := s.db.Query(`SELECT id,name,url,grp,icon,browser,updated_at FROM apps ORDER BY sort`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppBookmark{}
	for rows.Next() {
		var a AppBookmark
		var browser int
		if err = rows.Scan(&a.ID, &a.Name, &a.URL, &a.Group, &a.Icon, &browser, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.Browser = browser != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SaveApp(a AppBookmark) error {
	a.UpdatedAt = time.Now().Format(time.RFC3339)
	return s.SaveAppRaw(a)
}

// SaveAppRaw 写入时不动 UpdatedAt，供同步合并保留对端的修改时间。
func (s *Store) SaveAppRaw(a AppBookmark) error {
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

func (s *Store) DeleteApp(id string) error {
	_, err := s.db.Exec(`DELETE FROM apps WHERE id=?`, id)
	return err
}

// ---------- 列表缓存 ----------
// 控制面数据落 SQLite：下次打开秒出、断网或 401 时还能看到上次同步到的真实数据。

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

func (s *Store) ListForwards() ([]ForwardRule, error) {
	rows, err := s.db.Query(`SELECT id,name,kind,target,target_port,local_port,enabled FROM forwards ORDER BY sort`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ForwardRule{}
	for rows.Next() {
		var r ForwardRule
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
func (s *Store) SaveForward(r ForwardRule, sortHint int) error {
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

func (s *Store) AddAudit(action, detail string) {
	_, _ = s.db.Exec(`INSERT INTO audit(action,detail,created_at) VALUES(?,?,?)`,
		action, detail, time.Now().Format(time.RFC3339))
}

func (s *Store) ListAudit() ([]AuditEvent, error) {
	rows, err := s.db.Query(`SELECT created_at,action,detail FROM audit ORDER BY id DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		if err = rows.Scan(&e.Time, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// profileLabel 给服务端起个能认出来的名字：取域名，没有就用兜底。
func profileLabel(baseURL, fallback string) string {
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

func hasProfile(list []Profile, id string) bool {
	for _, p := range list {
		if p.ID == id {
			return true
		}
	}
	return false
}

// profileSecrets 把当前激活的凭证搬到钥匙串里属于该 profile 的条目下。
func profileSecrets(id string) {
	if id == "" {
		return
	}
	if v := secretGet(secretMain); v != "" {
		_ = secretSet(secretMain+"."+id, v)
	}
	if v := secretGet(secretAuth); v != "" {
		_ = secretSet(secretAuth+"."+id, v)
	}
}

// loadProfileSecrets 把目标 profile 的凭证拷回固定条目，其余代码继续读固定 key。
func loadProfileSecrets(id string) {
	if id == "" {
		return
	}
	if v := secretGet(secretMain + "." + id); v != "" {
		_ = secretSet(secretMain, v)
	} else {
		_ = secretSet(secretMain, "")
	}
	if v := secretGet(secretAuth + "." + id); v != "" {
		_ = secretSet(secretAuth, v)
	} else {
		_ = secretSet(secretAuth, "")
	}
}

// dataDir 是配置与数据库的真实目录，和 OpenStore 用的是同一套规则。
func dataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "Weft")
}

// dataDirDisplay 是给界面看的版本：macOS/Linux 缩成 ~，Windows 给完整路径。
func dataDirDisplay() string {
	dir := dataDir()
	if dir == "" {
		return ""
	}
	// Windows 上 os.UserConfigDir() 就是 C:\Users\<用户名>\AppData\Roaming，
	// 直接显示完整路径，不用 %AppData% 这种缩写让人猜。
	if runtime.GOOS == "windows" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(dir, home) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}
