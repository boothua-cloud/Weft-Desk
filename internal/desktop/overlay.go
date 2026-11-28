package desktop

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boothua/weft/internal/engine"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 应用内窗口加载的是第三方页面，没法往里塞原生按钮，
// 所以注入一段自绘的浮动菜单。菜单里的动作（打开调试器 / 关闭窗口）
// 需要回到 Go 侧执行，走一个只听 127.0.0.1 的小控制端点。

var (
	ctrlOnce  sync.Once
	ctrlPort  int
	ctrlToken string
)

// ensureControlServer 起一个只听 127.0.0.1 的小端点，给应用窗口里的浮动菜单用。
// 端点能关窗口，端口是随机的，但同机任何进程都能扫到，所以再加一道随机 token：
// 只有我们注入进去的那段脚本知道它。
func ensureControlServer(svc *WeftService) int {
	ctrlOnce.Do(func() {
		tok, err := engine.RandHex(16)
		if err != nil {
			log.Printf("[ui] 生成窗口控制令牌失败: %v", err)
			return
		}
		mux := http.NewServeMux()
		handle := func(action string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(tok)) != 1 {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				svc.windowAction(r.URL.Query().Get("w"), action)
				_, _ = w.Write([]byte("ok"))
			}
		}
		mux.HandleFunc("/close", handle("close"))

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Printf("[ui] 应用窗口控制端点启动失败: %v", err)
			return
		}
		ctrlPort = ln.Addr().(*net.TCPAddr).Port
		ctrlToken = tok
		go func() {
			if err := http.Serve(ln, mux); err != nil {
				log.Printf("[ui] 控制端点退出: %v", err)
			}
		}()
	})
	return ctrlPort
}

// randHex 生成 n 字节的十六进制随机串。

// windowAction 按窗口 ID 找到目标窗口再执行动作。
func (s *WeftService) windowAction(id, action string) {
	if s.app == nil || id == "" {
		return
	}
	for _, w := range s.app.Window.GetAll() {
		if fmt.Sprint(w.ID()) != id {
			continue
		}
		if action == "close" {
			w.Close()
		}
		return
	}
}

// attachOverlay 给应用内窗口挂上浮动菜单。
// 页面每次导航都会把 DOM 换掉，注入的节点跟着没了，所以这里定时重注入；
// 脚本自身幂等，已经存在就直接返回，开销只有一次跨进程调用。
func (s *WeftService) attachOverlay(w *application.WebviewWindow) {
	port := ensureControlServer(s)
	if port <= 0 || w == nil {
		return
	}
	js := appOverlayJS(port, ctrlToken, w.ID())
	stop := make(chan struct{})
	w.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		select {
		case <-stop:
		default:
			close(stop)
		}
	})
	go func() {
		w.ExecJS(js)
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				w.ExecJS(js)
			}
		}
	}()
}

// appOverlayJS 生成注入脚本。全部内联，样式带前缀并 !important，
// 避免被目标页面的样式覆盖；脚本自身幂等，重复注入不会叠加按钮。
func appOverlayJS(port int, token string, winID uint) string {
	tpl := `(function(){
if(window.__weftBar)return;window.__weftBar=1;
var base='http://127.0.0.1:{{PORT}}',id='{{ID}}',tk='{{TOKEN}}';
function act(a){try{fetch(base+'/'+a+'?w='+id+'&t='+encodeURIComponent(tk),{mode:'no-cors'})}catch(e){}}
var css='.__weft_wrap{position:fixed!important;top:10px!important;right:10px!important;z-index:2147483647!important;font:12px/1.4 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif!important;text-align:left!important}'
+'.__weft_btn{width:28px!important;height:28px!important;border-radius:8px!important;border:1px solid rgba(0,0,0,.14)!important;background:rgba(255,255,255,.94)!important;color:#3c4250!important;cursor:pointer!important;box-shadow:0 2px 8px rgba(0,0,0,.14)!important;display:flex!important;align-items:center!important;justify-content:center!important;font-size:15px!important;line-height:1!important;padding:0!important;margin:0!important}'
+'.__weft_btn:hover{background:#fff!important;color:#111!important}'
+'.__weft_menu{display:none;position:absolute!important;right:0!important;top:34px!important;min-width:140px!important;background:#fff!important;border:1px solid rgba(0,0,0,.1)!important;border-radius:9px!important;box-shadow:0 10px 28px rgba(0,0,0,.18)!important;padding:4px!important;box-sizing:border-box!important}'
+'.__weft_menu.__weft_open{display:block!important}'
+'.__weft_menu button{display:block!important;width:100%!important;text-align:left!important;padding:7px 10px!important;border:0!important;background:none!important;color:#1f2328!important;font-size:12px!important;cursor:pointer!important;border-radius:6px!important;margin:0!important;line-height:1.5!important;font-family:inherit!important}'
+'.__weft_menu button:hover{background:#f2f4f7!important}';
var st=document.createElement('style');st.textContent=css;(document.head||document.documentElement).appendChild(st);
var wrap=document.createElement('div');wrap.className='__weft_wrap';
var btn=document.createElement('button');btn.className='__weft_btn';btn.title='页面菜单';btn.textContent='⋯';
var menu=document.createElement('div');menu.className='__weft_menu';
[['刷新',function(){location.reload()}],
 ['后退',function(){history.back()}],
 ['复制地址',function(){try{navigator.clipboard.writeText(location.href)}catch(e){}}],
 ['关闭窗口',function(){act('close')}]].forEach(function(it){
  var b=document.createElement('button');b.textContent=it[0];
  b.addEventListener('click',function(e){e.stopPropagation();menu.classList.remove('__weft_open');it[1]()});
  menu.appendChild(b);
});
btn.addEventListener('click',function(e){e.stopPropagation();menu.classList.toggle('__weft_open')});
document.addEventListener('click',function(){menu.classList.remove('__weft_open')});
wrap.appendChild(btn);wrap.appendChild(menu);
(document.body||document.documentElement).appendChild(wrap);
})();`
	js := strings.ReplaceAll(tpl, "{{PORT}}", strconv.Itoa(port))
	js = strings.ReplaceAll(js, "{{TOKEN}}", token)
	return strings.ReplaceAll(js, "{{ID}}", strconv.FormatUint(uint64(winID), 10))
}

// appLoadingHTML 是应用内窗口在目标站点加载前显示的过渡页。
// 全部内联，不依赖外部资源，窗口一出现就能画出来。
func appLoadingHTML(name string) string {
	safe := html.EscapeString(name)
	icon := ""
	if len(Icon) > 0 {
		icon = "data:image/png;base64," + base64.StdEncoding.EncodeToString(Icon)
	}
	return `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><style>
html,body{margin:0;height:100%;background:#F4F6F9;font-family:-apple-system,BlinkMacSystemFont,"PingFang SC",system-ui,sans-serif;-webkit-font-smoothing:antialiased}
#l{position:fixed;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;
background:radial-gradient(560px 360px at 50% 12%,rgba(74,118,216,.13),transparent 70%),#F4F6F9}
.mark{width:60px;height:60px;border-radius:14px;object-fit:contain;
filter:drop-shadow(0 14px 26px rgba(16,26,60,.35));animation:lift 2s ease-in-out infinite}
.name{margin-top:16px;font-size:13px;color:#0F172A;font-weight:600}
.hint{margin-top:6px;font-size:11px;color:#8A94A3}
.bar{position:relative;margin-top:18px;width:140px;height:3px;border-radius:3px;background:#E3E8EF;overflow:hidden}
.bar i{position:absolute;top:0;bottom:0;left:0;width:46%;border-radius:3px;
background:linear-gradient(90deg,rgba(58,95,208,0),#3A5FD0 55%,#5A82E8);
animation:slide 1.2s cubic-bezier(.55,.1,.35,.9) infinite}
@keyframes lift{0%,100%{transform:translateY(0) scale(1)}50%{transform:translateY(-3px) scale(1.035)}}
@keyframes slide{0%{transform:translateX(-110%)}100%{transform:translateX(238%)}}
</style></head><body><div id="l">
<img class="mark" src="` + icon + `" alt="">
<div class="name">` + safe + `</div>
<div class="hint">正在加载内网地址…</div>
<div class="bar"><i></i></div>
</div></body></html>`
}
