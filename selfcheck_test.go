package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeDevices struct {
	devs []Device
	err  error
}

func (f fakeDevices) Devices(string) ([]Device, error) { return f.devs, f.err }

func TestRouteVerdictFourSteps(t *testing.T) {
	cases := []struct {
		name string
		in   RouteCheck
		want string
		tone string
	}{
		{"没批准", RouteCheck{Known: true, Advertised: true}, "服务端还没批准这条路由", "warn"},
		{"批准了没接受", RouteCheck{Known: true, Approved: true}, "已批准，本机还没接受", "warn"},
		{"接受了但路由器离线", RouteCheck{Known: true, Approved: true, Accepted: true}, "提供这条路由的设备离线", "bad"},
		{"全通", RouteCheck{Known: true, Approved: true, Accepted: true, PeerOnline: true}, "可用", "ok"},
		{"没 Key 时只看本机", RouteCheck{Accepted: true, PeerOnline: true}, "可用", "ok"},
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
	rows := []RouteCheck{{Prefix: "10.0.0.0/24", Peer: "a", PeerIP: "100.64.0.3", Accepted: true, PeerOnline: true}}
	cp := fakeDevices{devs: []Device{
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
	rows := []RouteCheck{{Prefix: "10.0.0.0/24", Accepted: true}}
	out := mergeAdvertised(rows, fakeDevices{err: errors.New("boom")})
	if len(out) != 1 || out[0].Known {
		t.Fatalf("应保持原样: %+v", out)
	}
}

func TestFinishSelfNotRunning(t *testing.T) {
	got := finishSelf(SelfReport{Engine: "system", Backend: "未启动"})
	if got.Tone != "bad" || !strings.Contains(got.Verdict, "没在运行") {
		t.Fatalf("引擎没起来要说清楚: %q %q", got.Verdict, got.Tone)
	}
}

// 界面把 health / routes / problems 当数组用，序列化成 null 就整页白屏。
func TestSelfReportJSONShape(t *testing.T) {
	healthy := SelfReport{
		Engine: "system", Backend: "Running", LoggedIn: true,
		TUN: "utun3", SelfIP: "100.64.0.13",
		Control: ControlStat{OK: true, Version: "0.23.0"},
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
	store, err := OpenStore()
	if err != nil {
		t.Skip("没有本地数据库，跳过: ", err)
	}
	defer store.Close()
	svc := NewWeftService(store)
	svc.engine = nil
	raw, err := svc.SelfCheck()
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	var rep SelfReport
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Tone == "" || len(rep.Problems) == 0 {
		t.Fatalf("至少要说一句为什么: %+v", rep)
	}
}
