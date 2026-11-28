package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestStore 用内存库，不碰真实配置目录。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestAppKey(t *testing.T) {
	cases := map[string]string{
		"http://100.64.0.3:5000/":  "http://100.64.0.3:5000",
		"HTTP://NAS.local:5000/a":  "http://nas.local:5000/a",
		"  http://nas.local:5000 ": "http://nas.local:5000",
	}
	for in, want := range cases {
		if got := appKey(in); got != want {
			t.Errorf("appKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSyncURL(t *testing.T) {
	cases := map[string]string{
		"100.64.0.7":          "http://100.64.0.7:4160",
		"nas":                 "http://nas:4160",
		"http://nas:9000/":    "http://nas:9000",
		"https://nas/ignored": "https://nas:4160",
	}
	for in, want := range cases {
		got, err := syncURL(in)
		if err != nil {
			t.Fatalf("syncURL(%q) 出错: %v", in, err)
		}
		if got != want {
			t.Errorf("syncURL(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := syncURL("  "); err == nil {
		t.Error("空地址应该报错")
	}
}

func TestNormalizeSyncToken(t *testing.T) {
	if got := normalizeSyncToken(" 7kq2-m4xr "); got != "7KQ2M4XR" {
		t.Errorf("normalizeSyncToken = %q", got)
	}
}

func TestMergeApps(t *testing.T) {
	local := []AppBookmark{
		{ID: "app-1", Name: "NAS", URL: "http://100.64.0.3:5000", Group: "常用", UpdatedAt: "2026-11-20T10:00:00Z"},
		{ID: "app-2", Name: "面板", URL: "http://100.64.0.4", Group: "常用", UpdatedAt: "2026-11-20T10:00:00Z"},
	}
	incoming := []AppBookmark{
		// 同地址不同 ID：算同一条，时间更新则覆盖
		{ID: "x-1", Name: "群晖", URL: "http://100.64.0.3:5000/", Group: "存储", UpdatedAt: "2026-11-25T10:00:00Z"},
		// 时间更旧：跳过
		{ID: "x-2", Name: "旧面板", URL: "http://100.64.0.4", Group: "旧", UpdatedAt: "2026-11-01T10:00:00Z"},
		// 新地址：新增
		{ID: "x-3", Name: "路由器", URL: "http://100.64.0.1", Group: "网络", UpdatedAt: "2026-11-25T10:00:00Z"},
		// 缺字段：跳过
		{Name: "", URL: "http://x"},
	}
	out, added, updated, skipped := mergeApps(local, incoming)
	if added != 1 || updated != 1 || skipped != 2 {
		t.Fatalf("added=%d updated=%d skipped=%d, want 1/1/2", added, updated, skipped)
	}
	if len(out) != 3 {
		t.Fatalf("合并后 %d 条, want 3", len(out))
	}
	// 覆盖时沿用本机 ID，界面上不会出现两条同地址
	if out[0].ID != "app-1" || out[0].Name != "群晖" {
		t.Errorf("第一条 = %+v", out[0])
	}
	// 本机较新的记录不被旧数据覆盖
	if out[1].Name != "面板" {
		t.Errorf("第二条被覆盖了: %+v", out[1])
	}
	// 新增的这条用自己的 ID，不能沿用对端的：对端 ID 撞上本机另一条会把它覆盖掉
	if out[2].ID == "x-3" || out[2].ID == "" {
		t.Errorf("新增记录应生成自己的 ID: %+v", out[2])
	}
}

func TestNewSyncToken(t *testing.T) {
	a, b := newSyncToken(), newSyncToken()
	if len(a) != 19 || a == b {
		t.Fatalf("同步码不对: %q %q", a, b)
	}
	if normalizeSyncToken(a) == "" {
		t.Fatal("同步码归一化后为空")
	}
}

func TestPullAppsFrom(t *testing.T) {
	store := newTestStore(t)
	// 用旧时间写入，模拟对端那条更晚改过
	_ = store.SaveAppRaw(AppBookmark{ID: "local-1", Name: "NAS", URL: "http://100.64.0.3:5000", Group: "常用", UpdatedAt: "2026-11-20T00:00:00Z"})

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ABC123" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"同步码不对"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(syncPayload{Host: "other", Apps: []AppBookmark{
			{ID: "x1", Name: "路由器", URL: "http://100.64.0.1", UpdatedAt: "2026-11-25T00:00:00Z"},
			{ID: "x2", Name: "群晖", URL: "http://100.64.0.3:5000", UpdatedAt: "2026-11-26T00:00:00Z"},
		}})
	}))
	defer peer.Close()

	svc := &WeftService{store: store}
	raw, err := svc.PullAppsFrom(strings.TrimPrefix(peer.URL, "http://"), "abc-123")
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	var res SyncResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if res.Peer != "other" || res.Added != 1 || res.Updated != 1 || res.Total != 2 {
		t.Fatalf("结果不对: %+v", res)
	}
	apps, _ := store.ListApps()
	if len(apps) != 2 {
		t.Fatalf("库里 %d 条, want 2", len(apps))
	}
	if apps[0].Name != "群晖" {
		t.Errorf("同地址没按新时间覆盖: %+v", apps[0])
	}
}

func TestPushAppsTo(t *testing.T) {
	store := newTestStore(t)
	_ = store.SaveApp(AppBookmark{ID: "local-1", Name: "NAS", URL: "http://100.64.0.3:5000"})

	var got syncPayload
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("方法 = %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(SyncResult{Peer: "other", Added: 1, Total: 1})
	}))
	defer peer.Close()

	svc := &WeftService{store: store}
	raw, err := svc.PushAppsTo(strings.TrimPrefix(peer.URL, "http://"), "ABCD1234")
	if err != nil {
		t.Fatalf("推送失败: %v", err)
	}
	var res SyncResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if res.Peer != "other" || res.Added != 1 {
		t.Fatalf("结果不对: %+v", res)
	}
	if len(got.Apps) != 1 || got.Apps[0].Name != "NAS" {
		t.Fatalf("对方收到的内容不对: %+v", got)
	}
}

func TestPullRejectsWrongToken(t *testing.T) {
	store := newTestStore(t)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"同步码不对"}`))
	}))
	defer peer.Close()

	svc := &WeftService{store: store}
	if _, err := svc.PullAppsFrom(strings.TrimPrefix(peer.URL, "http://"), "NOPE"); err == nil {
		t.Fatal("错误码应该变成错误返回")
	} else if !strings.Contains(err.Error(), "同步码") {
		t.Fatalf("错误信息没透传: %v", err)
	}
}
