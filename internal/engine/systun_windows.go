//go:build windows

package engine

import (
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	wgtun "github.com/tailscale/wireguard-go/tun"
	"golang.org/x/sys/windows"
	wgwin "golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

const winIfName = "Weft"

// weftAdapterGUID 是云栈自己的 WinTun 适配器 GUID。
//
// 必须显式指定，不能用包默认值：tailscale.com/net/tstun 的 init() 会把
// tun.WintunTunnelType 改成 "Tailscale"，并把 tun.WintunStaticRequestedGUID
// 固定成 Tailscale for Windows 的 {37217669-42DA-4657-A55B-0D995D328250}。
// 我们 import 了 tsnet，这套包级变量就跟着被改掉了 —— 建卡时实际是在创建
// 「和已安装的 Tailscale 同一个 GUID 的适配器」，SetupDi 直接返回
// ERROR_ALREADY_EXISTS，表现为连不上、日志里 0x800700B7。
var weftAdapterGUID = windows.GUID{
	Data1: 0x8E1C2F6A, Data2: 0x3F0B, Data3: 0x4C7C,
	Data4: [8]byte{0x9E, 0x2D, 0x5B, 0x4A, 0x71, 0xC3, 0xD8, 0xF1},
}

func init() {
	// 本包 init 在所有依赖之后执行，能覆盖掉 tstun 改过的值。
	wgtun.WintunTunnelType = "Weft"
	wgtun.WintunStaticRequestedGUID = &weftAdapterGUID
}

// Windows 没有 Unix socket 传句柄这条路，WinTun 又必须管理员权限，
// 所以直接要求进程本身已提权；未提权时把 ErrNeedElevation 交回上层，
// 由它弹一次 UAC 并以管理员身份重启，而不是让用户自己去找「以管理员运行」。
func newOSRouter() osRouter { return &winRouter{} }

func MaybeRunHelper() bool { return false }

type winRouter struct {
	mu      sync.Mutex
	ifName  string
	luid    wgwin.LUID
	hasLUID bool
	dev     wgtun.Device
	applied []netip.Prefix
}

func (r *winRouter) Prepare() (wgtun.Device, error) {
	if !IsElevated() {
		return nil, fmt.Errorf("%w：虚拟网卡由系统网络服务创建，Windows 要求进程具备管理员权限", ErrNeedElevation)
	}
	if err := ensureWintunDLL(); err != nil {
		return nil, err
	}
	// WinTun 建卡需要管理员；同名适配器会被复用，旧会话残留的地址与路由
	// 不用先清——Configure 里的 SetIPAddresses / SetRoutes 是整组替换的。
	dev, err := wgtun.CreateTUN(winIfName, tunMTU)
	if err != nil {
		return nil, fmt.Errorf("创建虚拟网卡 %s 失败: %w", winIfName, err)
	}
	luid, ok := deviceLUID(dev)
	if !ok {
		_ = dev.Close()
		return nil, fmt.Errorf("拿不到网卡 %s 的接口标识（LUID），无法配置地址与路由", winIfName)
	}
	r.dev, r.ifName, r.luid, r.hasLUID = dev, winIfName, luid, true
	return dev, nil
}

// deviceLUID 取 WinTun 适配器的 LUID。NativeTun 直接带 LUID，
// 万一是别的实现就退回按网卡名在接口表里找。
func deviceLUID(dev wgtun.Device) (wgwin.LUID, bool) {
	if nt, ok := dev.(*wgtun.NativeTun); ok {
		if l := nt.LUID(); l != 0 {
			return wgwin.LUID(l), true
		}
	}
	rows, err := wgwin.GetIfTable2Ex(wgwin.MibIfEntryNormal)
	if err != nil {
		return 0, false
	}
	for _, row := range rows {
		if strings.EqualFold(row.Alias(), winIfName) || strings.EqualFold(row.Description(), winIfName) {
			return row.InterfaceLUID, true
		}
	}
	return 0, false
}

// Configure 用 IP Helper API 写地址与路由。
// 之前用 netsh 有两个硬伤：中文系统上输出无法解析，重复添加会直接报
// 「对象已存在」把整次连接判成失败；SetRoutes 是整组替换，天然幂等。
func (r *winRouter) Configure(dev wgtun.Device, selfIP netip.Addr, routes []netip.Prefix) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hasLUID {
		return fmt.Errorf("网卡尚未就绪，无法配置地址")
	}

	addr := netip.PrefixFrom(selfIP, 32)
	if err := r.luid.SetIPAddresses([]netip.Prefix{addr}); err != nil {
		return fmt.Errorf("设置网卡地址 %s 失败: %w", addr, err)
	}

	// 接口 metric 压到 5：与本机已有路由同前缀时优先走隧道，而不是被物理网卡抢先。
	if row, err := r.luid.IPInterface(windows.AF_INET); err == nil {
		row.UseAutomaticMetric = false
		row.Metric = 5
		if err := row.Set(); err != nil {
			log.Printf("[tunnel] 设置网卡 metric 失败（不影响连通）: %v", err)
		}
	} else {
		log.Printf("[tunnel] 读取网卡 metric 失败（不影响连通）: %v", err)
	}

	base, _ := netip.ParsePrefix(tailnetPrefix)
	data := make([]*wgwin.RouteData, 0, len(routes))
	for _, p := range routes {
		// 前缀路由的网关写网卡自己的地址，与 Tailscale 在 Windows 上的做法一致
		// （wgengine/router/osrouter/ifconfig_windows.go）：这样 Windows 会把报文
		// 直接交给这块虚拟网卡，不依赖 on-link / ARP（WinTun 不响应 ARP）。
		// 只有目标是本机地址或 tailnet 单节点这类 /32 才用 0.0.0.0 on-link。
		nextHop := selfIP
		if p.IsSingleIP() && (p.Addr() == selfIP || base.Contains(p.Addr())) {
			nextHop = netip.IPv4Unspecified()
		}
		data = append(data, &wgwin.RouteData{
			Destination: p.Masked(),
			NextHop:     nextHop,
			Metric:      0,
		})
	}
	if err := r.luid.SetRoutes(data); err != nil {
		return fmt.Errorf("安装路由失败: %w", err)
	}
	r.applied = append([]netip.Prefix(nil), routes...)
	return nil
}

// Cleanup 撤路由、撤地址、关网卡，可重复调用。
// 顺序不能反：先撤路由再掉地址，否则会留下指向已消失网段的路由。
func (r *winRouter) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasLUID {
		if err := r.luid.FlushRoutes(windows.AF_INET); err != nil {
			log.Printf("[tunnel] 清理路由失败: %v", err)
		}
		if err := r.luid.FlushIPAddresses(windows.AF_INET); err != nil {
			log.Printf("[tunnel] 清理网卡地址失败: %v", err)
		}
	}
	r.hasLUID = false
	r.applied = nil
	if r.dev != nil {
		if err := r.dev.Close(); err != nil {
			log.Printf("[tunnel] 关闭网卡失败: %v", err)
		}
		r.dev = nil
	}
}

func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// systemRoutes 补上 Windows 的路由表：判断「本机已存在完全相同前缀」时
// 不能只看网卡地址，物理网卡上的静态/持久路由同样会把流量抢走。
func systemRoutes(exceptIface string) []localRoute {
	rows, err := wgwin.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		log.Printf("[tunnel] 读取系统路由表失败（冲突检测跳过）: %v", err)
		return nil
	}
	names := map[uint32]string{}
	if ifs, err := wgwin.GetIfTable2Ex(wgwin.MibIfEntryNormal); err == nil {
		for _, it := range ifs {
			names[it.InterfaceIndex] = it.Alias()
		}
	}
	out := make([]localRoute, 0, len(rows))
	for _, row := range rows {
		p := row.DestinationPrefix.Prefix()
		if !p.IsValid() || !p.Addr().Is4() {
			continue
		}
		name := names[row.InterfaceIndex]
		if exceptIface != "" && strings.EqualFold(name, exceptIface) {
			continue // 就是我们的隧道网卡，不参与冲突判断
		}
		out = append(out, localRoute{prefix: p.Masked(), iface: name})
	}
	return out
}

// ensureWintunDLL 提前把「缺运行库」这件事说清楚。
// wintun 包按 LoadLibraryEx(APPLICATION_DIR|SYSTEM32) 查找，所以 dll 放在
// 可执行文件同目录即可；缺了的话 WinTun 只会返回一句无法定位模块。
func ensureWintunDLL() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	dir := filepath.Dir(exe)
	if fileExists(filepath.Join(dir, "wintun.dll")) {
		return nil
	}
	if root := os.Getenv("SystemRoot"); root != "" && fileExists(filepath.Join(root, "System32", "wintun.dll")) {
		return nil
	}
	return fmt.Errorf("缺少 wintun.dll：请把 wintun.dll（amd64）放到 %s 同目录。发布包应随程序一起分发该文件", dir)
}
