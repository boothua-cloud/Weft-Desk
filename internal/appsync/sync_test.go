package appsync

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/store"
)

// newTestStore 用内存库，不碰真实配置目录。
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func newTestServer(t *testing.T, st *store.Store) *Server {
	t.Helper()
	return New(st, func() string { return "ABCD-1234" }, func() (string, string) { return "100.64.0.9", "test" }, nil)
}

func TestAppKey(t *testing.T) {
	same := []string{
		"http://100.64.0.1:8080",
		"http://100.64.0.1:8080/",
		" HTTP://100.64.0.1:8080 ",
	}
	for _, a := range same {
		if appKey(a) != appKey(same[0]) {
			t.Errorf("%q 应该和 %q 归一成同一个键", a, same[0])
		}
	}
	if appKey("http://a/x") == appKey("http://b/x") {
		t.Error("不同主机不该归一")
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
		got, err := URL(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeSyncToken(t *testing.T) {
	if got := NormalizeToken(" abcd-1234 "); got != "ABCD1234" {
		t.Fatalf("归一化结果 %q", got)
	}
}

func TestMergeApps(t *testing.T) {
	local := []model.AppBookmark{
		{ID: "l1", Name: "NAS", URL: "http://100.64.0.3:5000", UpdatedAt: "2026-11-26T00:00:00Z"},
		{ID: "l2", Name: "面板", URL: "http://100.64.0.4", UpdatedAt: "2026-11-27T00:00:00Z"},
	}
	incoming := []model.AppBookmark{
		{ID: "x1", Name: "NAS 旧名", URL: "http://100.64.0.3:5000", UpdatedAt: "2026-11-20T00:00:00Z"},
		{ID: "x2", Name: "路由器", URL: "http://100.64.0.1", UpdatedAt: "2026-11-25T00:00:00Z"},
		{ID: "x3", Name: "", URL: "http://bad"},
	}
	out, added, updated, skipped := Merge(local, incoming)
	if added != 1 || updated != 0 || skipped != 2 {
		t.Fatalf("统计不对: added=%d updated=%d skipped=%d", added, updated, skipped)
	}
	if len(out) != 3 {
		t.Fatalf("合并后 %d 条, want 3", len(out))
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

func TestNewToken(t *testing.T) {
	a, b := NewToken(), NewToken()
	if len(a) != 19 || a == b {
		t.Fatalf("同步码不对: %q %q", a, b)
	}
	if NormalizeToken(a) == "" {
		t.Fatal("同步码归一化后为空")
	}
}

func TestPull(t *testing.T) {
	st := newTestStore(t)
	// 用旧时间写入，模拟对端那条更晚改过
	_ = st.SaveAppRaw(model.AppBookmark{ID: "local-1", Name: "NAS", URL: "http://100.64.0.3:5000", Group: "常用", UpdatedAt: "2026-11-20T00:00:00Z"})

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ABC123" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"同步码不对"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(payload{Host: "other", Apps: []model.AppBookmark{
			{ID: "x1", Name: "路由器", URL: "http://100.64.0.1", UpdatedAt: "2026-11-25T00:00:00Z"},
			{ID: "x2", Name: "群晖", URL: "http://100.64.0.3:5000", UpdatedAt: "2026-11-26T00:00:00Z"},
		}})
	}))
	defer peer.Close()

	srv := newTestServer(t, st)
	res, err := srv.Pull(strings.TrimPrefix(peer.URL, "http://"), "abc-123")
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if res.Peer != "other" || res.Added != 1 || res.Updated != 1 || res.Total != 2 {
		t.Fatalf("结果不对: %+v", res)
	}
	apps, _ := st.ListApps()
	if len(apps) != 2 {
		t.Fatalf("库里 %d 条, want 2", len(apps))
	}
	if apps[0].Name != "群晖" {
		t.Errorf("同地址没按新时间覆盖: %+v", apps[0])
	}
}

func TestPush(t *testing.T) {
	st := newTestStore(t)
	_ = st.SaveApp(model.AppBookmark{ID: "local-1", Name: "NAS", URL: "http://100.64.0.3:5000"})

	var got payload
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("方法 = %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(model.SyncResult{Peer: "other", Added: 1, Total: 1})
	}))
	defer peer.Close()

	srv := newTestServer(t, st)
	res, err := srv.Push(strings.TrimPrefix(peer.URL, "http://"), "ABCD1234")
	if err != nil {
		t.Fatalf("推送失败: %v", err)
	}
	if res.Peer != "other" || res.Added != 1 {
		t.Fatalf("结果不对: %+v", res)
	}
	if len(got.Apps) != 1 || got.Apps[0].Name != "NAS" {
		t.Fatalf("对方收到的内容不对: %+v", got)
	}
}

func TestPullRejectsWrongToken(t *testing.T) {
	st := newTestStore(t)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"同步码不对"}`))
	}))
	defer peer.Close()

	srv := newTestServer(t, st)
	if _, err := srv.Pull(strings.TrimPrefix(peer.URL, "http://"), "NOPE"); err == nil {
		t.Fatal("错误码应该变成错误返回")
	} else if !strings.Contains(err.Error(), "同步码") {
		t.Fatalf("错误信息没带上原因: %v", err)
	}
}
