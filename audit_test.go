package main

import (
	"context"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

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
	if got, plain, err := validateBaseURL("https://hs.example.com/"); err != nil || plain || got != "https://hs.example.com" {
		t.Errorf("https 应放行并去掉末尾斜杠: %q %v %v", got, plain, err)
	}
	if got, plain, err := validateBaseURL("http://10.0.0.2:8080"); err != nil || !plain || got != "http://10.0.0.2:8080" {
		t.Errorf("http 应放行但标记明文: %q %v %v", got, plain, err)
	}
	for _, in := range []string{"", "hs.example.com", "ftp://hs", "https://"} {
		if _, _, err := validateBaseURL(in); err == nil {
			t.Errorf("validateBaseURL(%q) 应该拒绝", in)
		}
	}
}
