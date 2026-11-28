package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SelfCheck 是「连接诊断」页顶部的本机自检。
//
// 它存在的理由很直接：连不上的时候，原来那一页只会说一句「还没连上」，
// 什么都不给。可这正是最需要信息的时候——是没登录、TUN 没建、
// 控制面不可达，还是 UDP 被挡只能走 DERP，都能从这几个字段看出来。
func (s *WeftService) SelfCheck() (string, error) {
	s.mu.Lock()
	eng, cfg := s.engine, s.cfg
	s.mu.Unlock()

	rep := SelfReport{At: time.Now().Format(time.RFC3339)}
	if eng == nil {
		rep.Backend = "未启动"
		rep.Verdict, rep.Tone = "引擎还没创建", "bad"
		rep.Problems = []string{"应用刚启动或连接被关掉了，去总览页点连接"}
		return toJSON(finishSelf(rep))
	}
	rep.Engine = eng.Name()

	if snap, ok := eng.(interface {
		Snapshot(context.Context) (SelfReport, error)
	}); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		sr, err := snap.Snapshot(ctx)
		cancel()
		rep.Backend = sr.Backend
		if sr.At != "" {
			rep.At = sr.At
		}
		rep.SelfIP, rep.Hostname, rep.TUN = sr.SelfIP, sr.Hostname, sr.TUN
		rep.LoggedIn, rep.AcceptRoutes = sr.LoggedIn, sr.AcceptRoutes
		rep.Relay, rep.Direct = sr.Relay, sr.Direct
		rep.Health, rep.Routes = sr.Health, sr.Routes
		if err != nil {
			rep.Problems = append(rep.Problems, "读引擎状态失败："+err.Error())
		}
	} else {
		rep.Backend = "不支持自检"
		rep.Problems = append(rep.Problems, "当前引擎没有自检接口")
	}

	rep.Control = probeControl(cfg.BaseURL)

	// 通告与批准只存在服务端，本机 netmap 里只有「已接受」。
	// 控制面都连不上时别硬等这一次请求，界面会卡住十几秒。
	if rep.Control.OK {
		if hasKey, _ := s.apiKeyStatus(); hasKey {
			rep.Routes = mergeAdvertised(rep.Routes, s.cp)
		}
	}
	return toJSON(finishSelf(rep))
}

// probeControl 打一次 /version：这个端点不需要钥匙，能通就说明服务端可达，
// 顺带拿到版本号。失败时把原因写清楚，别只给一个 false。
func probeControl(baseURL string) ControlStat {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	st := ControlStat{URL: base}
	if base == "" {
		st.Err = "没填控制面地址"
		return st
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/version", nil)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	resp, err := clientFor().Do(req)
	st.Ms = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		st.Err = tidyNetErr(err)
		return st
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		st.Err = "服务端回了 HTTP " + resp.Status
		return st
	}
	var v struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(raw, &v)
	st.Version = v.Version
	st.OK = true
	return st
}

// tidyNetErr 把 Go 的网络错误压成一句人话。
// 原始串里一大半是 *url.Error 和 dial 的包装，界面放不下。
func tidyNetErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"):
		return "连接超时，6 秒没响应"
	case strings.Contains(msg, "connection refused"):
		return "端口拒绝连接，服务可能没起来"
	case strings.Contains(msg, "no such host"):
		return "域名解析不了"
	case strings.Contains(msg, "certificate"):
		return "证书校验失败（自建控制面填一下 CA）"
	}
	return msg
}

// deviceLister 只要列表这一个方法：补路由状态不需要整个管理接口，
// 测试也因此不用把几十个方法都实现一遍。
type deviceLister interface {
	Devices(user string) ([]Device, error)
}

// mergeAdvertised 用管理接口把「通告 / 批准」两列补上，并且把
// 「通告了但本机没接受」的路由也拉进列表——这一类最常见：
// 服务端批准了，客户端因为别的原因没拿到，用户只看到「内网不通」。
func mergeAdvertised(rows []RouteCheck, cp deviceLister) []RouteCheck {
	devs, err := cp.Devices("")
	if err != nil {
		return rows
	}
	byIP := make(map[string]Device, len(devs))
	for _, d := range devs {
		if d.IP != "" {
			byIP[d.IP] = d
		}
	}
	seen := make(map[string]bool, len(rows))
	for i := range rows {
		seen[rows[i].Prefix+"|"+rows[i].PeerIP] = true
		d, ok := byIP[rows[i].PeerIP]
		if !ok {
			continue
		}
		rows[i].Known = true
		// headscale 返回的 approved 是「批准且生效」的那部分，没批准的
		// 只出现在 available 里。两个字段对不上时以 approved 为准。
		rows[i].Approved = hasRoute(d.ApprovedRoutes, rows[i].Prefix)
		rows[i].Advertised = rows[i].Approved || hasRoute(d.AdvertisedRoutes, rows[i].Prefix)
	}
	for _, d := range devs {
		for _, p := range splitList(d.AdvertisedRoutes) {
			if seen[p+"|"+d.IP] {
				continue
			}
			rows = append(rows, RouteCheck{
				Prefix: p, Peer: d.Name, PeerIP: d.IP,
				Known:      true,
				Advertised: true,
				Approved:   hasRoute(d.ApprovedRoutes, p),
				Accepted:   false,
				PeerOnline: d.Online,
			})
		}
	}
	return rows
}

func hasRoute(list, prefix string) bool {
	for _, r := range splitList(list) {
		if r == prefix {
			return true
		}
	}
	return false
}

// routeVerdict 按四步往下走，停在哪一步就说哪一步的事。
func routeVerdict(r RouteCheck) (string, string) {
	switch {
	case r.Known && !r.Approved:
		return "服务端还没批准这条路由", "warn"
	case !r.Accepted:
		return "已批准，本机还没接受", "warn"
	case !r.PeerOnline:
		return "提供这条路由的设备离线", "bad"
	}
	return "可用", "ok"
}

// finishSelf 把散落的字段收成一句结论。顺序按「越致命越先说」：
// 引擎没起来 > 没登录 > TUN 没建 > 控制面不通 > 健康告警。
func finishSelf(rep SelfReport) SelfReport {
	var bad, warn []string
	if rep.Backend == "未启动" || rep.Backend == "读不到状态" || rep.Backend == "不支持自检" {
		bad = append(bad, "引擎没在运行（"+rep.Backend+"）")
	} else if !rep.LoggedIn {
		bad = append(bad, "节点还没登录，去总览页完成授权")
	}
	if rep.TUN == "" && rep.Backend != "未启动" {
		warn = append(warn, "没读到 TUN 接口，流量可能没走隧道")
	}
	if rep.Control.Err != "" {
		bad = append(bad, "控制面连不上："+rep.Control.Err)
	}
	for _, h := range rep.Health {
		warn = append(warn, h)
	}
	for i := range rep.Routes {
		rep.Routes[i].Verdict, rep.Routes[i].Tone = routeVerdict(rep.Routes[i])
	}
	for _, r := range rep.Routes {
		switch r.Tone {
		case "bad":
			bad = append(bad, "路由 "+r.Prefix+" "+r.Verdict)
		case "warn":
			warn = append(warn, "路由 "+r.Prefix+" "+r.Verdict)
		}
	}
	rep.Problems = append(rep.Problems, bad...)
	rep.Problems = append(rep.Problems, warn...)
	switch {
	case len(bad) > 0:
		rep.Verdict, rep.Tone = bad[0], "bad"
	case len(warn) > 0:
		// 提醒不止一条时只给个数，具体原因在下面那几行里，别把 pill 撑爆
		rep.Verdict, rep.Tone = fmt.Sprintf("%d 处要注意", len(warn)), "warn"
	case rep.Backend == "Running":
		rep.Verdict, rep.Tone = "一切正常", "ok"
	default:
		rep.Verdict, rep.Tone = rep.Backend, "warn"
	}
	if rep.Problems == nil {
		rep.Problems = []string{}
	}
	if rep.Health == nil {
		rep.Health = []string{}
	}
	if rep.Routes == nil {
		rep.Routes = []RouteCheck{}
	}
	return rep
}
