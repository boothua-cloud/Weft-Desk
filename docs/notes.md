# 实现笔记

> 这些是开发过程中记下来的细节，比 README 更偏实现。CI 那一节写的是早期 Gitea 方案，现在已经迁到 GitHub Actions，仅供参考。

名字取自李白《蜀道难》「天梯石栈相钩连」：在没有路的地方凿出一条通路。可执行文件、安装目录、包名统一用 `weft`（Windows 为 `weft.exe`，macOS 为 `Weft.app`，Linux 为 `/usr/local/bin/weft`）；`云栈` 只作为界面里的显示名，不出现在任何文件路径上。

## 功能

管理端完整对接 Headscale `/api/v1`（按服务端 swagger）：

| 模块 | 能力 |
|---|---|
| 节点 | 列表 / 详情 / 注册 / 重命名 / 标签 / 路由审批 / 强制登出 / 取消到期 / 删除 / 补齐 IP / 调试建节点 |
| 节点认证 | 按 Auth ID 注册到成员、批准、拒绝 |
| 成员 | 列表 / 创建 / 重命名 / 删除 |
| 授权 Key | 列表 / 生成（可复用、临时、ACL 标签、有效期）/ 失效 / 删除 |
| API Key | 列表 / 创建 / 失效 / 删除 |
| 策略 | 读取 / 校验 / 保存 |
| 健康 | `/api/v1/health` 数据库连通性 |
| 应用 | 内网 Web 书签，应用内窗口或系统浏览器打开 |
| 端口转发 | 本地端口映射 + SOCKS5 |

服务端报错时直接显示原文，不做假成功。

## 界面

左侧分组导航 + 右侧页头与内容区，亮色 / 暗色两套主题：

- 顶栏月亮/太阳图标一键切换，设置页「外观主题」可选跟随系统 / 亮色 / 暗色
- 主题记在本地，重开应用保持；跟随系统时随系统外观实时切换
- 总览页是仪表盘：连接状态与节点地址、在线设备、已接入网段、运行时长、本节点信息、快捷应用、最近事件
- 快捷应用可直接点击打开，不需要先进「应用管理」

「能力位」「API 风格」这类兼容性探测信息不展示：只对接 Headscale，不做 Tailscale 官方 API 的差异化适配。

## 网络接管方式

真实 TUN + 系统路由：`100.64/10` 与已批准子网都走系统网络栈，`ssh`、`mysql`、`psql` 等外部工具直接填 `100.x` / 内网 IP 就能连，不需要任何代理。需要管理员权限（装过系统助手后只在首次）。

流程：助手建 TUN → 句柄回传主进程 → 注入 `tsnet.Server{Tun: dev}` → 上线后配地址、装路由。子网路由自动接受（等价 `--accept-routes`）；**只有本机已存在完全相同前缀时才跳过**，其余按最长前缀匹配共存。不做默认路由、不做 Exit Node。

## 系统助手（一次授权，之后免密）

| 平台 | 方式 | socket |
|---|---|---|
| macOS | LaunchDaemon + `/Library/PrivilegedHelperTools` | `/var/run/weft.sock` |
| Linux | systemd unit `weft-helper` + `/usr/local/lib/weft/weft` | `/var/run/weft.sock` |
| Windows | 登录任务（最高权限）启动本体，进程内建卡 | 无 |

设置页「系统助手」可安装/移除；未安装时 macOS/Linux 回退到一次性授权。

### Windows 的权限模型

界面本身**不提权**（清单是 `asInvoker`）：双击打开不弹 UAC，WebView 也不在管理员上下文里跑。
提权只发生在两个时刻，都是点一下按钮触发的，用户不需要自己去右键「以管理员身份运行」：

| 动作 | 发生什么 | 之后 |
|---|---|---|
| 点「连接」 | 弹一次系统授权（`ShellExecute runas`），授权后以管理员身份重启并自动连接 | 未装任务时每次点连接都要授权一次 |
| 点「安装」系统助手 | 弹一次授权，由提权后的自己创建计划任务 `Weft`（登录时 · 最高权限） | 每次登录都由任务以管理员拉起，不再弹 UAC |

几点实现约束，改动前请先看：

- 建卡与写路由都走 Windows IP Helper API（`winipcfg`），不用 `netsh`：`netsh` 在中文系统上输出无法解析，重复添加还会直接报「对象已存在」。
- 适配器 GUID 必须显式指定。`tailscale.com/net/tstun` 的 `init()` 会把 `tun.WintunStaticRequestedGUID` 改成 Tailscale for Windows 的 `{37217669-…}`，本项目 import 了 tsnet 就继承了它 —— 结果建卡时在创建「和已安装的 Tailscale 同一个 GUID 的适配器」，SetupDi 直接 `ERROR_ALREADY_EXISTS`（0x800700B7）。详见 `systun_windows.go` 里的 `weftAdapterGUID`。
- 控制台子进程（`schtasks`）带 `CreateNoWindow`，否则界面每 5 秒轮询助手状态会不停闪黑框。
- 同一时刻只允许一个实例（`Local\WeftDesk.SingleInstance` 命名互斥）：登录任务与手动双击会撞在一起，两个进程抢同一张网卡和同一个 SQLite。
- 任务里记的 exe 路径会写在数据目录的 `helper-target.txt`；程序换过目录时设置页会提示「登录自启指向旧位置，请重新安装」。

## 配置

1. 「设置」填 Headscale 地址与管理 API Key
2. 隧道另填 Auth Key（留空则首次走浏览器授权）
3. 测试连接 → 保存 → 回总览点连接

设置页的开关（开机自动连接、断线自动重连、托盘图标、窗口调试）拨一下即时生效并落库，不用再点保存。

凭证只写系统钥匙串。书签、端口转发规则、配置、审计与列表缓存存在本地 SQLite：
`~/Library/Application Support/Weft/weft.db`（Linux `~/.local/share/Weft`，Windows `%AppData%\Weft`）。
目录名保留 `Weft`，改名不影响已有配置。
**没有任何预置数据**，应用与转发规则都由你自己创建。

## 关闭窗口与托盘

关闭窗口（红叉 / `⌘W`）只是把窗口收起，应用继续在后台保持隧道，不会退出。

点菜单栏 / 托盘图标弹出菜单：

| 菜单项 | 结果 |
|---|---|
| 状态行 | `已连接 · 100.x` / `未连接 · 控制面地址`，随隧道实时变化 |
| 连接 / 断开连接 | 不回到窗口也能连或断 |
| 显示主窗口 | 重新显示主窗口（macOS 也可点 Dock 图标） |
| 设置… | 显示窗口并直接跳到连接设置 |
| 退出云栈 | 真正退出应用（`⌘Q` 等同） |

「设置 → 在系统托盘显示图标」控制是否显示托盘图标。关掉后仍可关窗口不退出；
Windows / Linux 因为只有托盘能找回窗口，关掉托盘时关闭窗口即为退出。

### 两个 Key 不是一回事

| 用途 | Key | 生成方式 | 填在哪 |
|---|---|---|---|
| 本机加入 tailnet | `hskey-auth-…` | `headscale preauthkeys create` | 隧道 Auth Key |
| 管理设备 / 成员 / 路由 | `hskey-apikey-…` | `headscale apikeys create` | 管理 API Key |

## 平台

| | macOS | Linux | Windows |
|---|---|---|---|
| TUN | `utunN` | `weft0`（`/dev/net/tun`） | `Weft`（需 `wintun.dll` 同目录） |
| 提权 | `osascript` | `pkexec` / `sudo -n` | UAC（`ShellExecute runas`，按需） |
| 路由 | `route` | `ip route` | IP Helper API（`winipcfg`） |

macOS 与 Windows 已真机验证（Windows：建卡、地址、路由、ping 内网与 tailnet 节点、退出清理）。
Linux 仅交叉编译与类型检查，提权与路由清理需真机确认。

## 构建与运行

流水线（`.gitea/workflows/`）一次产出三个包，都是「先构建前端再编译」的同一套流程：

| 目标 | 产出 | 构建机 |
|---|---|---|
| Windows | `weft-0.1.0-windows-amd64.zip`（`weft.exe` + `wintun.dll`） | Linux 交叉编译（`CGO_ENABLED=0`） |
| Windows 安装包 | `云栈-windows-amd64-setup.exe`（NSIS，装到 `%LOCALAPPDATA%\Programs\Weft`） | Linux + `makensis`，仅打 tag 时 |
| Linux | `云栈_0.1.0-1_amd64.deb` + `云栈-0.1.0-linux-amd64.tar.gz` | Linux 原生（`CGO_ENABLED=1 -tags gtk3`） |
| macOS | `云栈-0.1.0-macos-arm64.dmg` | 必须 macOS 主机，需要 `macos-latest` runner |

流水线里 `wails3` 用 `CGO_ENABLED=0 go install` 安装：CLI 在 `linux+cgo` 下会编进
`internal/operatingsystem/webkit_linux.go`，硬要 `pkg-config gtk4 webkitgtk-6.0`，
而我们按 gtk3 构建，为了装个打包工具去拉 GTK4 头文件没道理。关掉 CGO 后
`generate syso` / `generate .desktop` / `tool package` 都正常。

产物不走 Actions 的 artifact（这台 Gitea 的 artifact 接口固定回 429，v3/v4 都传不上去），
也不再用中间分支：每个 job 构建完，直接调 `scripts/gitea-release.sh` 把文件挂到同一个
Release 上。三个 job 并发，脚本对「release 已存在」做了幂等处理（409 就按 tag 查回来）。

**发版**：先在 `CHANGELOG.md` 写下本版本的 `## [x.y.z]` 小节（发版说明会从这里截取），
再 `git tag -a v0.1.1 -m "..." && git push origin v0.1.1`。
tag 触发时附件挂到 `v0.1.1` 这个发布；手动触发时用 `build-<commit8>` 建一个临时发布。
`installer` 打 NSIS 安装包（仅 tag）；`macos` 需要 macOS runner。

> runner 侧的两个环境要求（本机 Docker runner 已配好）：容器要能访问 github.com 拉
> action 源码（配置了走网关代理，`NO_PROXY` 放行 `git.boothua.cn`，避免出口 IP 被
> 仓库前面的 WAF 限流）；`HOME=/data` 让 action 缓存落在挂载卷上，重启不用重拉。

本机手动构建：

```bash
# Windows（产物 bin/云栈.exe，需 bin/wintun.dll 同目录）
bash scripts/fetch-wintun.sh amd64
wails3 build -tags devtools

# Linux（原生，CGO + gtk3；直接出 deb 与 tar.gz）
bash scripts/build-linux.sh

# macOS
wails3 build -tags devtools && ./scripts/make-app.sh
```

三个必须注意的点：

- **Linux 一定要带 `gtk3` 标签**。Wails v3.0 默认是 GTK4 + WebKitGTK 6.0，Debian 12 上
  没有 `webkitgtk-6.0`，编译期 `pkg-config` 就找不到库；`-tags gtk3` 后链的是
  `gtk+-3.0` + `webkit2gtk-4.1`，deb 的 `Depends` 也必须是 `libgtk-3-0` / `libwebkit2gtk-4.1-0`。
- **Windows 必须带 `wintun.dll`**。缺失时点「连接」只会得到 `Unable to load library: wintun.dll`；
  取运行库用 `scripts/fetch-wintun.sh`，发布包要随 exe 一起分发。`project.nsi` 里已经把它
  改成硬依赖：找不到就直接 `!error` 中断，不会静默出一个装了也连不上的安装包。
- **在 Linux 上跑 makensis，`File` 必须给绝对路径**。POSIX 版 makensis 传相对路径
  （`../../../bin/weft.exe`）会直接段错误，且不给任何提示；`OutFile` 的相对路径反而是正常的。
  所以 CI 里把 `ARG_WAILS_AMD64_BINARY` 与 `WINTUN_DLL` 都算成绝对路径再传进去。

界面按 `design/weft-ui-design.html` 自绘，无第三方组件库依赖。

`devtools` 标签用于把 WebView 检查器编译进来；不加时 Wails 会把 `OpenDevTools` 编译成空实现。

### 应用窗口调试

设置 → 「应用窗口调试」打开后重开应用，`F12` 或 `⌘⌥I` 打开检查器（需 `devtools` 构建）。
注意：该标签会让 WebView 标记为可检查，属进程内可见性变化，仅自用建议开启。

### 连通性诊断

```bash
./bin/weft --diag 100.64.0.3:22 10.0.1.1:443
```

不进 GUI，输出每个对端的 `relay / 直连 / 握手 / 收发字节 / 能力位 / 版本`，以及 TUN 与 netstack 两条通道的 TCP 结果。

## 已知限制

- 只支持 Headscale，不含 Tailscale 官方 API
- 不做 MagicDNS（一律用 IP）、不做 Exit Node、不做 Taildrop / Taildrive
- 子网路由需服务端已批准；本机已占用同一前缀时该段跳过
- **`tailscale.com` 固定 v1.104.7，不要升级**：1.106+ 的 hybrid 后量子握手与 1.104 以下对端不兼容
- Windows 需随程序分发 `wintun.dll`（与 exe 同目录；缺失时启动即有明确提示，不会等到点连接才报一句英文）
- Windows 的提权粒度是整个进程：WinTun 没有「传句柄给非提权进程」的路径，所以提权后 GUI 也在管理员上下文里（代价是资源管理器拖放不可用）
- 应用内窗口是系统 WebView，**不支持加载浏览器扩展**（Bitwarden 等）；需要扩展时用「浏览器打开」，或在书签里勾选「用系统浏览器打开」
