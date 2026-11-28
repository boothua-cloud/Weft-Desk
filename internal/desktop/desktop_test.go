package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boothua/weft/internal/controlplane"
	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/store"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// WeftService 的单测：调用来源校验、配置档案、策略历史、自检、运行时长。

// fakeWin 只实现 ID()，其它方法用不到。
// 嵌入 nil 接口，是为了不用把 Window 的几十个方法都写一遍。
type fakeWin struct {
	application.Window
	id uint
}

func (f fakeWin) ID() uint { return f.id }

// 应用内窗口加载的是第三方页面，Wails 不校验消息来源，
// 所以高危方法要按调用窗口再挡一道。
func TestCallerAllowed(t *testing.T) {
	svc := &WeftService{mainWindowID: 7}
	with := func(id uint) context.Context {
		return context.WithValue(context.Background(), application.WindowKey, fakeWin{id: id})
	}

	if !svc.callerAllowed(context.Background()) {
		t.Error("没有窗口信息（托盘、启动流程）应放行")
	}
	if !svc.callerAllowed(with(7)) {
		t.Error("主窗口应放行")
	}
	if svc.callerAllowed(with(8)) {
		t.Error("应用内窗口不该放行")
	}

	// 主窗口还没建好时不要误伤，否则启动阶段的调用全失败
	fresh := &WeftService{}
	if !fresh.callerAllowed(with(8)) {
		t.Error("还不知道主窗口 ID 时应放行")
	}
}

// 书签地址只认 http/https：同步过来的 `file://` 或 `-a Calculator` 不能被当成网址。
func TestAppTargetURL(t *testing.T) {
	ok := map[string]string{
		"http://100.64.0.3:5000":  "http://100.64.0.3:5000",
		"100.64.0.3:5000":         "http://100.64.0.3:5000",
		"https://nas.internal/a":  "https://nas.internal/a",
		"  http://nas.internal/ ": "http://nas.internal/",
	}
	for in, want := range ok {
		got, err := appTargetURL(in)
		if err != nil {
			t.Errorf("appTargetURL(%q) 出错: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("appTargetURL(%q) = %q, want %q", in, got, want)
		}
	}
	bad := []string{"", "file:///etc/passwd", "-a Calculator", "javascript:alert(1)", "://nohost"}
	for _, in := range bad {
		if _, err := appTargetURL(in); err == nil {
			t.Errorf("appTargetURL(%q) 应该拒绝", in)
		}
	}
}

// 服务地址只接受带协议的 http/https，明文要在调用方那里被标出来。
func TestValidateBaseURL(t *testing.T) {
	if got, plain, err := controlplane.ValidateBaseURL("https://hs.example.com/"); err != nil || plain || got != "https://hs.example.com" {
		t.Errorf("https 应放行并去掉末尾斜杠: %q %v %v", got, plain, err)
	}
	if got, plain, err := controlplane.ValidateBaseURL("http://10.0.0.2:8080"); err != nil || !plain || got != "http://10.0.0.2:8080" {
		t.Errorf("http 应放行但标记明文: %q %v %v", got, plain, err)
	}
	for _, in := range []string{"", "hs.example.com", "ftp://hs", "https://"} {
		if _, _, err := controlplane.ValidateBaseURL(in); err == nil {
			t.Errorf("controlplane.ValidateBaseURL(%q) 应该拒绝", in)
		}
	}
}

// 假 headscale：GET 给当前策略，PUT 换掉。
type fakePolicyServer struct {
	content string
	saves   int
}

func (f *fakePolicyServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/policy", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]string{
				"policy": f.content, "updatedAt": "2026-11-28T10:00:00Z",
			})
		case http.MethodPut:
			var body struct {
				Policy string `json:"policy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.content = body.Policy
			f.saves++
		}
	})
	return mux
}

func newPolicyTestService(t *testing.T, current string) (*WeftService, *fakePolicyServer) {
	t.Helper()
	fake := &fakePolicyServer{content: current}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	cfg := model.Config{BaseURL: srv.URL, ControlPlane: srv.URL}
	svc := &WeftService{store: store, cfg: cfg, cp: controlplane.New(cfg)}
	return svc, fake
}

func TestSavePolicyKeepsPreviousVersion(t *testing.T) {
	oldBody := `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`
	svc, fake := newPolicyTestService(t, oldBody)

	newBody := "{\n  \"acls\": []\n}\n"
	if err := svc.SavePolicy(context.Background(), newBody); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if fake.content != newBody {
		t.Fatalf("服务端内容没换: %q", fake.content)
	}

	items, err := svc.store.ListPolicyHistory(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("历史应该只有 1 版, 有 %d", len(items))
	}
	if items[0].Size != len(oldBody) || items[0].Lines != 1 {
		t.Fatalf("摘要不对: %+v", items[0])
	}
	got, err := svc.store.GetPolicyVersion(items[0].ID)
	if err != nil || got != oldBody {
		t.Fatalf("取回的旧版本不对: %q %v", got, err)
	}
}

func TestSavePolicySameContentNoHistory(t *testing.T) {
	body := "{\"acls\":[]}"
	svc, _ := newPolicyTestService(t, body)
	if err := svc.SavePolicy(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.store.ListPolicyHistory(20)
	if len(items) != 0 {
		t.Fatalf("内容没变不该记历史, 有 %d 版", len(items))
	}
}

func TestRestorePolicy(t *testing.T) {
	first := "{\"acls\":[{\"action\":\"accept\",\"src\":[\"*\"],\"dst\":[\"*:*\"]}]}"
	svc, fake := newPolicyTestService(t, first)

	// 改成空策略，旧的那版进历史
	if err := svc.SavePolicy(context.Background(), "{\"acls\":[]}"); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.store.ListPolicyHistory(20)
	if len(items) != 1 {
		t.Fatalf("应该有一版历史, 有 %d", len(items))
	}

	// 回滚
	if err := svc.RestorePolicy(context.Background(), items[0].ID); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if strings.TrimSpace(fake.content) != first {
		t.Fatalf("服务端没回到旧策略: %q", fake.content)
	}
	// 回滚前的空策略也应该进历史，回滚本身可以后悔
	items, _ = svc.store.ListPolicyHistory(20)
	if len(items) != 2 {
		t.Fatalf("回滚后应该有 2 版历史, 有 %d", len(items))
	}
}

func TestPrunePolicyHistory(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 25; i++ {
		if err := store.AddPolicyHistory("v"+string(rune('a'+i)), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PrunePolicyHistory(20); err != nil {
		t.Fatal(err)
	}
	items, _ := store.ListPolicyHistory(100)
	if len(items) != 20 {
		t.Fatalf("应该留 20 版, 留了 %d", len(items))
	}
	// 最老的那批被删掉，最新的还在
	body, err := store.GetPolicyVersion(items[0].ID)
	if err != nil || body != "vy" {
		t.Fatalf("最新一版不对: %q %v", body, err)
	}
}

type fakeDevices struct {
	devs []model.Device
	err  error
}

func (f fakeDevices) Devices(string) ([]model.Device, error) { return f.devs, f.err }

func TestRouteVerdictFourSteps(t *testing.T) {
	cases := []struct {
		name string
		in   model.RouteCheck
		want string
		tone string
	}{
		{"没批准", model.RouteCheck{Known: true, Advertised: true}, "服务端还没批准这条路由", "warn"},
		{"批准了没接受", model.RouteCheck{Known: true, Approved: true}, "已批准，本机还没接受", "warn"},
		{"接受了但路由器离线", model.RouteCheck{Known: true, Approved: true, Accepted: true}, "提供这条路由的设备离线", "bad"},
		{"全通", model.RouteCheck{Known: true, Approved: true, Accepted: true, PeerOnline: true}, "可用", "ok"},
		{"没 Key 时只看本机", model.RouteCheck{Accepted: true, PeerOnline: true}, "可用", "ok"},
	}
	for _, c := range cases {
		v, tone := routeVerdict(c.in)
		if v != c.want || tone != c.tone {
			t.Errorf("%s: got %q/%q want %q/%q", c.name, v, tone, c.want, c.tone)
		}
	}
}

// 本机只看得见「已接受」的路由，没批准的那些要靠管理接口补。
// 补完得把「通告了但没接受」也列出来，否则用户永远只看到「内网不通」。
func TestMergeAdvertisedAddsUnaccepted(t *testing.T) {
	rows := []model.RouteCheck{{Prefix: "10.0.0.0/24", Peer: "a", PeerIP: "100.64.0.3", Accepted: true, PeerOnline: true}}
	cp := fakeDevices{devs: []model.Device{
		{Name: "a", IP: "100.64.0.3", Online: true, AdvertisedRoutes: "10.0.0.0/24", ApprovedRoutes: "10.0.0.0/24"},
		{Name: "b", IP: "100.64.0.5", Online: true, AdvertisedRoutes: "192.168.5.0/24"},
	}}
	out := mergeAdvertised(rows, cp)
	if len(out) != 2 {
		t.Fatalf("应补出未接受的一条，得到 %d 条: %+v", len(out), out)
	}
	if !out[0].Known || !out[0].Approved {
		t.Errorf("已接受的那条应标成已批准: %+v", out[0])
	}
	if out[1].Accepted || out[1].Approved {
		t.Errorf("b 的通告没批准也没接受: %+v", out[1])
	}
}

// 列表接口挂了就原样返回，不能把「问不到」当成「没有路由」。
func TestMergeAdvertisedKeepsRowsOnError(t *testing.T) {
	rows := []model.RouteCheck{{Prefix: "10.0.0.0/24", Accepted: true}}
	out := mergeAdvertised(rows, fakeDevices{err: errors.New("boom")})
	if len(out) != 1 || out[0].Known {
		t.Fatalf("应保持原样: %+v", out)
	}
}

func TestFinishSelfNotRunning(t *testing.T) {
	got := finishSelf(model.SelfReport{Engine: "system", Backend: "未启动"})
	if got.Tone != "bad" || !strings.Contains(got.Verdict, "没在运行") {
		t.Fatalf("引擎没起来要说清楚: %q %q", got.Verdict, got.Tone)
	}
}

// 界面把 health / routes / problems 当数组用，序列化成 null 就整页白屏。
func TestSelfReportJSONShape(t *testing.T) {
	healthy := model.SelfReport{
		Engine: "system", Backend: "Running", LoggedIn: true,
		TUN: "utun3", SelfIP: "100.64.0.13",
		Control: model.ControlStat{OK: true, Version: "0.23.0"},
	}
	b, err := json.Marshal(finishSelf(healthy))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"health":[]`, `"routes":[]`, `"problems":[]`, `"tone":"ok"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("缺 %s: %s", want, b)
		}
	}
}

func TestTidyNetErr(t *testing.T) {
	cases := map[string]string{
		"dial tcp: context deadline exceeded":               "连接超时",
		"dial tcp 1.2.3.4:443: connect: connection refused": "端口拒绝连接",
	}
	for in, want := range cases {
		if got := tidyNetErr(errors.New(in)); !strings.Contains(got, want) {
			t.Errorf("%q → %q，缺少 %q", in, got, want)
		}
	}
}

// 未连接时 SelfCheck 也必须给出结论，而不是返回错误——这正是这一页存在的意义。
func TestSelfCheckWithoutEngine(t *testing.T) {
	store, err := store.Open()
	if err != nil {
		t.Skip("没有本地数据库，跳过: ", err)
	}
	defer store.Close()
	svc := New(store)
	svc.engine = nil
	raw, err := svc.SelfCheck()
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	var rep model.SelfReport
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Tone == "" || len(rep.Problems) == 0 {
		t.Fatalf("至少要说一句为什么: %+v", rep)
	}
}

// 截图里的那一幕：同一条 10.0.1.0/24 出现两行，一行「可用」一行
// 「本机还没接受」。根因是 Device.IP 把 v4 和 v6 拼成了一个字符串，
// 跟 netmap 里的 100.x 对不上，管理接口那条就当成新路由又加了一行。
func TestMergeAdvertisedCollapsesSameRoute(t *testing.T) {
	rows := []model.RouteCheck{{
		Prefix: "10.0.1.0/24", Peer: "ubuntu", PeerIP: "100.64.0.5",
		Accepted: true, PeerOnline: true,
	}}
	cp := fakeDevices{devs: []model.Device{{
		Name: "ubuntu", IP: "100.64.0.5", IPs: []string{"100.64.0.5", "fd7a::5"},
		Online: true, AdvertisedRoutes: "10.0.1.0/24", ApprovedRoutes: "10.0.1.0/24",
	}}}
	out := mergeAdvertised(rows, cp)
	if len(out) != 1 {
		t.Fatalf("同一条路由只能有一行，得到 %d 行: %+v", len(out), out)
	}
	r := out[0]
	if !r.Known || !r.Advertised || !r.Approved || !r.Accepted {
		t.Fatalf("四步都该是通: %+v", r)
	}
	if v, tone := routeVerdict(r); v != "可用" || tone != "ok" {
		t.Fatalf("结论应该是可用: %q %q", v, tone)
	}
}

// 设备在服务端改过名：netmap 给 hostname、管理接口给 givenName。
// 这时只有 IP 能对上，也不能再冒第二行。
func TestMergeAdvertisedMatchesByIPNotName(t *testing.T) {
	rows := []model.RouteCheck{{Prefix: "10.0.1.0/24", Peer: "old-name", PeerIP: "100.64.0.5", Accepted: true}}
	cp := fakeDevices{devs: []model.Device{{
		Name: "ubuntu", IP: "100.64.0.5", Online: true,
		AdvertisedRoutes: "10.0.1.0/24", ApprovedRoutes: "10.0.1.0/24",
	}}}
	if out := mergeAdvertised(rows, cp); len(out) != 1 || !out[0].Known {
		t.Fatalf("按 IP 就该认出来: %+v", out)
	}
}

// 一个节点两个地址时，比较用的那一个必须是 100.x
func TestPrimaryIP(t *testing.T) {
	if got := controlplane.PrimaryIP([]string{"fd7a:115c:a1e0::5", "100.64.0.5"}); got != "100.64.0.5" {
		t.Fatalf("优先取 IPv4: %q", got)
	}
	if got := controlplane.PrimaryIP(nil); got != "" {
		t.Fatalf("没有地址要返回空: %q", got)
	}
}

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

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d time.Duration
		w string
	}{
		{0, "0 秒"},
		{9 * time.Second, "9 秒"},
		{90 * time.Second, "1 分 30 秒"},
		{42 * time.Minute, "42 分 0 秒"},
		{3*time.Hour + 12*time.Minute, "3 小时 12 分"},
		{5 * time.Hour, "5 小时"},
		{50*time.Hour + 3*time.Minute, "2 天 2 小时"},
		{-5 * time.Second, "0 秒"},
	}
	for _, c := range cases {
		if got := formatUptime(c.d); got != c.w {
			t.Fatalf("formatUptime(%v) = %q, want %q", c.d, got, c.w)
		}
	}
}
