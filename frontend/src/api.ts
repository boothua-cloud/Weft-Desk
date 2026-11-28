import { WeftService } from '../bindings/github.com/boothua/weft/internal/desktop'

async function parse<T>(p: any): Promise<T> {
  const raw: string = await p
  return JSON.parse(raw) as T
}

export interface Capabilities {
  version: string
  apiStyle: string
  canCreateUser: boolean
  canManageAPIKeys: boolean
  canManageACL: boolean
  canManageOAuth: boolean
  database: boolean
  probeMessage: string
}
export interface PeerInfo {
  name: string; ip: string; os: string; online: boolean
  lastSeen: string; routes: string; allowed: string; relay: string; curAddr: string
}
// DiagReport 是一次连通性检查的结果，字段和后端 model.go 对应。
export interface DiagReport {
  ip: string
  name: string
  port: number
  online: boolean
  path: string        // 直连 / DERP
  relay: string
  curAddr: string
  handshake: string
  version: string
  pingMs: number
  pingWorst: number
  pingLost: number
  pingErr: string
  dialMs: number
  dialErr: string
  // refused / timeout / noroute / other：拨测失败的类型。
  // refused 表示对端回了拒绝，包到了，ACL 必然放行。
  dialCode: string
  aclChecked: boolean
  // allow / deny / unknown。unknown 是「规则里写了客户端算不出来的表达式」，
  // 不能当成没放行。
  aclState: string
  aclNote: string
  verdict: string
  hints: string[]
  at: string
}

// SelfReport 是本机自检，连不上时也能出结果。
export interface SelfReport {
  at: string
  engine: string
  backend: string
  loggedIn: boolean
  tun: string
  selfIP: string
  hostname: string
  relay: string
  direct: boolean
  acceptRoutes: boolean
  control: ControlStat
  health: string[]
  problems: string[]
  verdict: string
  tone: 'ok' | 'warn' | 'bad'
  routes: RouteCheck[]
}

export interface ControlStat {
  url: string
  ok: boolean
  ms: number
  version: string
  err: string
}

// 一条子网路由的四步：通告 → 批准 → 接受 → 可达
export interface RouteCheck {
  prefix: string
  peer: string
  peerIP: string
  known: boolean
  advertised: boolean
  approved: boolean
  accepted: boolean
  peerOnline: boolean
  verdict: string
  tone: 'ok' | 'warn' | 'bad'
}

// 设备上登记过的服务，来自应用书签和转发规则
export interface PeerPort {
  ip: string
  port: number
  label: string
  kind: 'app' | 'forward'
}
// PingSample 是延迟曲线上的一次采样，ms 为负表示这次没回应。
export interface PingSample {
  ms: number
  at: string
  err: string
}
// TrafficTotals 是累计收发字节。速率由界面按两次采样的时间差自己算，
// 后端只负责给一个准数。
export interface TrafficTotals {
  up: number
  down: number
  at: string
  err: string
}

// PolicyVersion 是策略历史里的一版，和 Go 侧 model.go 对应。
export interface PolicyVersion {
  id: number
  at: string
  size: number
  lines: number
}

export interface State {
  connected: boolean
  connecting: boolean
  mode: string
  controlPlane: string
  endpoint: string
  selfIP: string
  hostname: string
  engine: string
  engineReady: boolean
  cliReady: boolean
  configured: boolean
  // 是否配了能用的管理 API Key（空值或节点 Auth Key 都是 false）。
  // 为 false 时服务端管理相关的入口整块不显示。
  hasApiKey: boolean
  // 钥匙串里有没有节点注册密钥。首次启动向导用它区分「新装的」和
  // 「走浏览器授权、只是没填管理 Key」的老用户
  hasAuthKey: boolean
  // 为什么不算配了：填的是 Auth Key、形状不对、或者服务端 401 拒绝。
  // 空串表示没配（就是没填），界面据此决定要不要显示一条提示
  apiKeyIssue: string
  loginURL: string
  lastError: string
  platform: string
  appVersion: string
  dataDir: string
  routes: string[]
  peers: PeerInfo[]
  skipped: string[]
  netErr: string
  uptime: string
  message: string
  capabilities: Capabilities
  // 本地名字解析：把设备名写进 /etc/hosts，ssh/curl 能用裸名
  nameSync: boolean
  nameSyncCount: number
  nameSyncSkipped: string[]
  nameSyncErr: string
}
// UpdateStatus 是「检查更新」的返回，和 Go 侧的 updater.UpdateStatus 对应。
export interface UpdateStatus {
  currentVersion: string
  latestVersion: string
  hasUpdate: boolean
  notes: string
  publishedAt: string
  releaseURL: string
  assetName: string
  assetSize: number
  // 安装位置不可写（mac 的 /Applications、deb 的 /usr/bin）时为 false，
  // 只能下载安装包让用户手动装。
  canAutoUpdate: boolean
  // "permission" 表示安装位置没写权限；空串表示别的降级原因（缺更新包）。
  manualReason: 'permission' | ''
  readyToRestart: boolean
  state: 'idle' | 'latest' | 'available' | 'ready' | 'error'
  error: string
}

// 下载进度，来自 weft:update-progress 事件。
export interface UpdateProgress {
  written: number
  total: number
  percent: number
  rate: number
}

export interface Config {
  controlPlane: string
  baseURL: string
  tailnet: string
  authMode: string
  apiStyle: string
  tunnelEngine: string
  hostname: string
  allowInsecure: boolean
  authKey: string
  autoConnect: boolean
  autoReconnect: boolean
  minimizeToTray: boolean
  devTools: boolean
  socksEnabled: boolean
  socksPort: number
  autoForward: boolean
  nameSync: boolean
}
export interface Device {
  id: string
  name: string
  hostname: string
  givenName: string
  user: string
  userId: string
  ip: string
  ips: string[]
  tags: string
  lastSeen: string
  createdAt: string
  online: boolean
  status: string
  advertisedRoutes: string
  approvedRoutes: string
  subnetRoutes: string
  expiry: string
  registerMethod: string
  machineKey: string
  nodeKey: string
  discoKey: string
}
export interface User {
  id: string; name: string; rawName: string; displayName: string
  email: string; provider: string; pictureUrl: string; createdAt: string
}
export interface AuthKey {
  id: string; key: string; user: string; reusable: boolean; ephemeral: boolean
  used: boolean; expires: string; created: string; aclTags: string; status: string
}
export interface APIKey {
  id: string; prefix: string; expires: string; created: string; lastSeen: string; status: string
  isCurrent?: boolean
}
export interface PolicyDoc { content: string; updatedAt: string; source?: string }
export interface HealthInfo { database: boolean }
export interface AppBookmark {
  id: string
  name: string
  url: string
  group: string
  icon: string
  // 抓下来的站点图标，data URL。空表示还没抓到，界面退回色块 + 首字
  iconData: string
  // 上一次抓取失败的原因，抓成功会自动清空。只在本机有意义，不参与同步
  iconError: string
  browser: boolean
  updatedAt: string
  // 最近一次打开的时间，只在本机有意义，不参与同步
  lastOpen: string
}
// SyncInfo 是应用书签同步的当前状态，和 Go 侧 SyncInfo 对应。
export interface SyncInfo {
  enabled: boolean
  port: number
  token: string
  listening: boolean
  selfIP: string
  hostname: string
  count: number
  error: string
}
export interface SyncResult { peer: string; added: number; updated: number; skipped: number; total: number }
// IconFailure 是某一个应用没抓到的原因
export interface IconFailure { id: string; name: string; reason: string }
// IconFillResult 是批量抓取的汇总。scope 为 all 表示连已有图标的也重抓了
export interface IconFillResult { scope: string; total: number; done: number; failed: IconFailure[] }
export interface AuditEvent {
  time: string; action: string; detail: string
  profile: string; target: string
  // ok / fail；失败时 error 是服务端或本机的原文
  result: string; error: string
}
export interface BatchFail { id: string; name: string; error: string }
export interface BatchResult { done: number; failed: BatchFail[] }
// 批量处理 Key：吊销认 id，删除认前缀，两个都带上省得后端猜
export interface KeyRef { prefix: string; id: string; name: string }
export interface PolicyTemplate { id: number; name: string; content: string; source: string; at: string }
export interface Cached<T> { items: T[]; source: string; updatedAt: string; error?: string }
export interface HelperStatus {
  installed: boolean; running: boolean; socket: string; version: string; note: string; logTail: string
}
export interface ForwardRule {
  id: string; name: string; kind: string; target: string; targetPort: number
  localPort: number; enabled: boolean
}
export interface ForwardStatus {
  id: string; state: string; active: number; bytes: number; error: string; listenAddr: string
}
export interface ForwardView {
  rules: ForwardRule[]; statuses: ForwardStatus[]; socks: ForwardStatus
  socksEnabled: boolean; socksPort: number; autoForward: boolean
  power: boolean; bind: string; engine: string
}

export const api = {
  // 连接与状态
  state: () => parse<State>(WeftService.GetState()),
  // 主窗口启动时是隐藏的，前端把启动画面铺好后调这里让窗口出现
  revealWindow: () => WeftService.RevealWindow(),
  openDevTools: () => WeftService.OpenDevTools(),
  config: () => parse<Config>(WeftService.GetConfig()),
  saveConfig: (c: Config) => WeftService.SaveConfig(JSON.stringify(c)),
  // 只改界面偏好开关，立刻落库生效，不触发引擎重建
  setUIPrefs: (p: Partial<Config>) => parse<Config>(WeftService.SetUIPrefs(JSON.stringify(p))),
  setCredential: (kind: string, value: string) => WeftService.SetCredential(kind, value),
  renewAPIKey: (days: number): Promise<void> => (WeftService as any).RenewAPIKey(days),
  revealCredential: (kind: string): Promise<string> => (WeftService as any).RevealCredential(kind),
  testConnection: () => WeftService.TestConnection(),
  connect: () => parse<State>(WeftService.Connect()),
  disconnect: () => parse<State>(WeftService.Disconnect()),
  // 没配授权 Key 时手动要一次网页授权链接
  loginURL: (): Promise<string> => (WeftService as any).GetLoginURL(),
  health: () => parse<HealthInfo>(WeftService.GetHealth()),
  audit: () => parse<AuditEvent[]>(WeftService.ListAudit()),
  exportText: (filename: string, content: string): Promise<string> =>
    WeftService.ExportText(filename, content),
  exportDiag: (): Promise<string> => WeftService.ExportDiagBundle(),
  testPeer: (host: string, port: number, via: string) => WeftService.TestPeer(host, port, via),
  diagnose: (ip: string, port: number) => parse<DiagReport>((WeftService as any).Diagnose(ip, port)),
  selfCheck: () => parse<SelfReport>((WeftService as any).SelfCheck()),
  peerPorts: () => parse<PeerPort[]>((WeftService as any).PeerPorts()),
  // 单次 disco 探测，给延迟曲线用。失败也走返回值（ms = -1），不抛错
  pingPeer: (ip: string) => parse<PingSample>((WeftService as any).PingPeer(ip)),
  // 累计收发字节，给总览页的流量曲线做差分
  traffic: () => parse<TrafficTotals>((WeftService as any).Traffic()),
  // 本地名字解析：开关会真的去改 /etc/hosts（要系统助手），失败会抛错
  setNameSync: (on: boolean): Promise<void> => (WeftService as any).SetNameSync(on),

  // 节点
  devices: () => parse<Cached<Device>>(WeftService.ListDevices()),
  devicesByUser: (user: string) => parse<Cached<Device>>(WeftService.ListDevicesByUser(user)),
  device: (id: string) => parse<Device>(WeftService.GetDevice(id)),
  registerNode: (user: string, key: string) => parse<Device>(WeftService.RegisterNode(user, key)),
  deleteDevice: (id: string) => WeftService.DeleteDevice(id),
  renameDevice: (id: string, name: string) => WeftService.RenameDevice(id, name),
  setDeviceTags: (id: string, tags: string) => WeftService.SetDeviceTags(id, tags),
  expireDevice: (id: string) => WeftService.ExpireDevice(id),
  disableDeviceExpiry: (id: string) => WeftService.DisableDeviceExpiry(id),
  approveRoutes: (id: string, routes: string) => WeftService.ApproveRoutes(id, routes),
  // 批量：后端逐个发请求，失败不中断，最后给汇总
  tagDevices: (ids: string[], tags: string, add: boolean) =>
    parse<BatchResult>(WeftService.TagDevices(ids, tags, add)),
  batchExpiry: (ids: string[], expire: boolean) =>
    parse<BatchResult>(WeftService.BatchExpiry(ids, expire)),
  backfillIPs: (confirmed: boolean) => WeftService.BackfillIPs(confirmed),
  debugCreateNode: (user: string, key: string, name: string, routes: string) =>
    parse<Device>(WeftService.DebugCreateNode(user, key, name, routes)),

  // 节点认证
  registerAuth: (user: string, authID: string) => parse<Device>(WeftService.RegisterAuth(user, authID)),
  approveAuth: (authID: string) => WeftService.ApproveAuth(authID),
  rejectAuth: (authID: string) => WeftService.RejectAuth(authID),

  // 成员
  users: () => parse<Cached<User>>(WeftService.ListUsers()),
  createUser: (name: string, displayName: string, email: string, pictureUrl: string) =>
    parse<User>(WeftService.CreateUser(name, displayName, email, pictureUrl)),
  renameUser: (id: string, name: string) => WeftService.RenameUser(id, name),
  deleteUser: (id: string) => WeftService.DeleteUser(id),
  deleteUsers: (ids: string[]) => parse<BatchResult>(WeftService.DeleteUsers(ids)),

  // 授权 Key
  authKeys: () => parse<Cached<AuthKey>>(WeftService.ListAuthKeys()),
  createAuthKey: (user: string, reusable: boolean, ephemeral: boolean, days: number, tags: string) =>
    WeftService.CreateAuthKey(user, reusable, ephemeral, days, tags),
  expireAuthKey: (id: string) => WeftService.ExpireAuthKey(id),
  deleteAuthKey: (id: string) => WeftService.DeleteAuthKey(id),
  batchAuthKeys: (ids: string[], op: 'expire' | 'delete') =>
    parse<BatchResult>(WeftService.BatchAuthKeys(ids, op)),

  // API Key
  apiKeys: () => parse<Cached<APIKey>>(WeftService.ListAPIKeys()),
  createAPIKey: (days: number) => WeftService.CreateAPIKey(days),
  expireAPIKey: (prefix: string, id: string) => WeftService.ExpireAPIKey(prefix, id),
  deleteAPIKey: (prefix: string, id: string) => WeftService.DeleteAPIKey(prefix, id),
  batchAPIKeys: (keys: KeyRef[], op: 'expire' | 'delete') =>
    parse<BatchResult>(WeftService.BatchAPIKeys(keys, op)),

  policyHistory: () => parse<PolicyVersion[]>(WeftService.ListPolicyHistory()),
  policyVersion: (id: number) =>
    parse<{ content: string; updatedAt: string }>(WeftService.GetPolicyVersion(id)),
  restorePolicy: (id: number) => WeftService.RestorePolicy(id),

  // 策略
  policy: () => parse<PolicyDoc>(WeftService.GetPolicy()),
  savePolicy: (content: string) => WeftService.SavePolicy(content),
  checkPolicy: (content: string) => WeftService.CheckPolicy(content),

  // 策略模板：存在本地库，跨服务端可用
  policyTemplates: () => parse<PolicyTemplate[]>(WeftService.ListPolicyTemplates()),
  savePolicyTemplate: (name: string, content: string) =>
    parse<{ id: number; name: string }>(WeftService.SavePolicyTemplate(name, content)),
  deletePolicyTemplate: (id: number) => WeftService.DeletePolicyTemplate(id),

  // 应用与书签
  apps: () => parse<AppBookmark[]>(WeftService.ListApps()),
  saveApp: (a: AppBookmark) => parse<AppBookmark>(WeftService.SaveApp(JSON.stringify(a))),
  deleteApp: (id: string) => WeftService.DeleteApp(id),
  // 拖动排序：传排好的 id 数组
  reorderApps: (ids: string[]) => WeftService.ReorderApps(JSON.stringify(ids)),
  // 抓单个图标（不管原来有没有），返回更新后的书签；抓不到会 reject
  fetchAppIcon: (id: string) => parse<AppBookmark>((WeftService as any).FetchAppIcon(id)),
  // 批量抓：missing 只补空的，all 连已有的也重抓
  fillIcons: (scope: 'missing' | 'all') => parse<IconFillResult>((WeftService as any).FillIcons(scope)),
  // 去掉图标，退回色块 + 首字
  clearAppIcon: (id: string) => (WeftService as any).ClearAppIcon(id),
  openApp: (id: string) => WeftService.OpenApp(id),
  openInBrowser: (id: string) => WeftService.OpenInBrowser(id),

  // 应用同步：在本机与同 tailnet 的另一台设备之间传书签
  syncInfo: () => parse<SyncInfo>(WeftService.GetSync()),
  setSync: (enabled: boolean, port: number) => parse<SyncInfo>(WeftService.SetSync(enabled, port)),
  rotateSyncToken: () => parse<SyncInfo>(WeftService.RotateSyncToken()),
  pullApps: (target: string, token: string) => parse<SyncResult>(WeftService.PullAppsFrom(target, token)),
  pushApps: (target: string, token: string) => parse<SyncResult>(WeftService.PushAppsTo(target, token)),

  // 端口转发
  forwards: () => parse<ForwardView>(WeftService.ListForwards()),
  saveForward: (r: ForwardRule) => WeftService.SaveForward(JSON.stringify(r)),
  deleteForward: (id: string) => WeftService.DeleteForward(id),
  toggleForward: (id: string, enabled: boolean) => WeftService.ToggleForward(id, enabled),
  forwardSnippet: (id: string) => WeftService.ForwardSnippet(id),
  setForwardPower: (on: boolean) => WeftService.SetForwardPower(on),
  setForwardOptions: (autoForward: boolean, socksEnabled: boolean, socksPort: number) =>
    WeftService.SetForwardOptions(autoForward, socksEnabled, socksPort),
  setForwardBind: (bind: string) => WeftService.SetForwardBind(bind),
  profilesView: () => WeftService.ProfilesView(),
  switchProfile: (id: string) => WeftService.SwitchProfile(id),
  saveProfile: (name: string) => WeftService.SaveProfile(name),
  deleteProfile: (id: string) => WeftService.DeleteProfile(id),
  renameProfile: (id: string, name: string) => WeftService.RenameProfile(id, name),
  saveProfileRaw: (raw: string) => WeftService.SaveProfileRaw(raw),

  // 自动更新
  checkUpdate: () => parse<UpdateStatus>(WeftService.CheckUpdate()),
  downloadUpdate: () => parse<UpdateStatus>(WeftService.DownloadUpdate()),
  applyUpdate: () => WeftService.ApplyUpdate(),
  updateInfo: () => parse<UpdateStatus>(WeftService.UpdateInfo()),
  downloadInstaller: () => WeftService.DownloadInstaller(),
  openReleasePage: () => WeftService.OpenReleasePage(),

  // 助手
  helperStatus: () => parse<HelperStatus>(WeftService.GetHelperStatus()),
  installHelper: () => parse<HelperStatus>(WeftService.InstallHelper()),
  uninstallHelper: () => parse<HelperStatus>(WeftService.UninstallHelper()),
}
