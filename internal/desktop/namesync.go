package desktop

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/boothua/weft/internal/engine"
	"github.com/boothua/weft/internal/model"
)

// 本地名字解析：把 tailnet 里的设备名同步进 /etc/hosts，
// 让 ssh / curl / 数据库客户端能直接用裸名，不必记 100.x。
//
// 三条约束决定了这里怎么写：
//  1. /etc/hosts 是系统文件，只有 root 能写。走常驻助手的 hosts 操作，
//     助手自己校验名字和地址，只接触自己那段标记区块。
//  2. 设备列表每 3 秒就可能变（有人上线、改名、换 IP），但重写文件和刷
//     DNS 缓存不该跟着这个频率跑。每轮把当前集合交给助手合并，助手那边
//     内容没变就只读不写，也不刷缓存。
//  3. 断开连接、关闭开关、退出应用都要把区块删掉。留着的后果是：隧道已经
//     没了，名字还能解析到一个连不上的地址，比解析不到更难查。

type nameSyncState struct {
	mu      sync.Mutex
	applied int
	skipped []string
	err     string
	at      time.Time
}

// SetNameSync 打开/关闭本地名字解析。打开时立刻写一次，不用等下一轮轮询。
func (s *WeftService) SetNameSync(ctx context.Context, on bool) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	s.mu.Lock()
	s.cfg.NameSync = on
	cfg := s.cfg
	s.mu.Unlock()
	if err := s.store.SaveConfig(cfg); err != nil {
		return err
	}
	if err := s.syncNames(); err != nil {
		// 打开就失败（多半是没装助手、或助手版本旧）：把开关弹回去，
		// 别让界面显示「已开启」但 hosts 里什么都没有。
		if on {
			s.mu.Lock()
			s.cfg.NameSync = false
			cfg = s.cfg
			s.mu.Unlock()
			_ = s.store.SaveConfig(cfg)
		}
		s.audit("本地名字解析", map[bool]string{true: "开启", false: "关闭"}[on], err)
		return err
	}
	s.audit("本地名字解析", map[bool]string{true: "开启", false: "关闭"}[on], nil)
	return nil
}

// watchNameSync 跟着隧道走：连上就同步，断开就清掉。
// 8 秒一轮：设备改名/IP 变动不需要秒级反映，但也不能等到用户重开应用。
func (s *WeftService) watchNameSync() {
	// 启动时先清掉上次异常退出留下的区块（隧道还没起来，那些名字一定不可达）
	s.mu.Lock()
	enabled := s.cfg.NameSync
	s.mu.Unlock()
	if !enabled && engine.HostsEnabled() {
		if err := engine.ClearHosts(); err != nil {
			log.Printf("[namesync] 清理上次残留失败: %v", err)
		} else {
			log.Printf("[namesync] 已清理上次退出时残留的 hosts 区块")
		}
	}

	t := time.NewTicker(8 * time.Second)
	defer t.Stop()
	for range t.C {
		_ = s.syncNames()
	}
}

// syncNames 是唯一写 hosts 的地方。每轮都跑一遍，助手比对内容后决定
// 要不要落盘，所以这里不需要再记「上次写了什么」。
func (s *WeftService) syncNames() error {
	s.mu.Lock()
	enabled, eng := s.cfg.NameSync, s.engine
	s.mu.Unlock()

	// 以引擎自己的 running 为准，不看 s.connected：连接被动掉线时那个标志
	// 可能还是 true，名字留在 hosts 里就指不到任何地方了。
	running := false
	if eng != nil {
		running, _, _ = eng.Status()
	}

	if !enabled || eng == nil || !running {
		// 不该有区块。先看文件里有没有，没有就不必打搅助手。
		if !engine.HostsEnabled() {
			return nil
		}
		if err := engine.ClearHosts(); err != nil {
			s.setNameSyncErr(err.Error())
			return err
		}
		s.nameSync.mu.Lock()
		s.nameSync.applied, s.nameSync.skipped, s.nameSync.err = 0, nil, ""
		s.nameSync.at = time.Now()
		s.nameSync.mu.Unlock()
		return nil
	}

	// 不比对指纹，每轮都把「当前设备集合」交给助手去合并：助手那边内容没变
	// 就只读不写。这样有人手动删掉区块、或别的工具覆盖了 hosts，下一轮自己
	// 就长回来，不用等设备集合发生变化。
	res, err := engine.SyncHosts(s.nameEntries(eng))
	if err != nil {
		s.setNameSyncErr(err.Error())
		log.Printf("[namesync] 同步 hosts 失败: %v", err)
		return err
	}
	s.nameSync.mu.Lock()
	s.nameSync.applied, s.nameSync.skipped, s.nameSync.err = res.Applied, res.Skipped, ""
	s.nameSync.at = time.Now()
	s.nameSync.mu.Unlock()
	return nil
}

func (s *WeftService) setNameSyncErr(msg string) {
	s.nameSync.mu.Lock()
	s.nameSync.err = msg
	s.nameSync.at = time.Now()
	s.nameSync.mu.Unlock()
}

// nameEntries 从本地 netmap 取「名字 → IPv4」。本机自己也写进去，
// 否则 ssh 回连自己还得记 IP。
func (s *WeftService) nameEntries(eng engine.TunnelEngine) []engine.HostEntry {
	out := make([]engine.HostEntry, 0, 16)
	if pr, ok := eng.(interface{ Peers() []model.PeerInfo }); ok {
		for _, p := range pr.Peers() {
			if p.Name == "" || p.IP == "" {
				continue
			}
			out = append(out, engine.HostEntry{Name: p.Name, IP: p.IP})
		}
	}
	if _, ip, host := eng.Status(); ip != "" && host != "" {
		out = append(out, engine.HostEntry{Name: host, IP: ip})
	}
	return out
}

// fillNameSync 在 GetState 里把运行态塞进 State，界面轮询一次就有数。
func (s *WeftService) fillNameSync(st *model.State) {
	s.mu.Lock()
	st.NameSync = s.cfg.NameSync
	s.mu.Unlock()
	s.nameSync.mu.Lock()
	st.NameSyncCount = s.nameSync.applied
	st.NameSyncErr = s.nameSync.err
	st.NameSyncSkipped = append([]string{}, s.nameSync.skipped...)
	s.nameSync.mu.Unlock()
}
