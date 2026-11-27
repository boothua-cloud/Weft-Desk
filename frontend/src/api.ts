import { WeftService } from '../bindings/github.com/boothua/weft'

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
  loginURL: string
  lastError: string
  platform: string
  dataDir: string
  routes: string[]
  peers: PeerInfo[]
  skipped: string[]
  netErr: string
  uptime: string
  message: string
  capabilities: Capabilities
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
export interface PolicyDoc { content: string; updatedAt: string }
export interface HealthInfo { database: boolean }
export interface AppBookmark { id: string; name: string; url: string; group: string; icon: string; browser: boolean }
export interface AuditEvent { time: string; action: string; detail: string }
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
  health: () => parse<HealthInfo>(WeftService.GetHealth()),
  audit: () => parse<AuditEvent[]>(WeftService.ListAudit()),
  testPeer: (host: string, port: number, via: string) => WeftService.TestPeer(host, port, via),

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

  // 授权 Key
  authKeys: () => parse<Cached<AuthKey>>(WeftService.ListAuthKeys()),
  createAuthKey: (user: string, reusable: boolean, ephemeral: boolean, days: number, tags: string) =>
    WeftService.CreateAuthKey(user, reusable, ephemeral, days, tags),
  expireAuthKey: (id: string) => WeftService.ExpireAuthKey(id),
  deleteAuthKey: (id: string) => WeftService.DeleteAuthKey(id),

  // API Key
  apiKeys: () => parse<Cached<APIKey>>(WeftService.ListAPIKeys()),
  createAPIKey: (days: number) => WeftService.CreateAPIKey(days),
  expireAPIKey: (prefix: string, id: string) => WeftService.ExpireAPIKey(prefix, id),
  deleteAPIKey: (prefix: string, id: string) => WeftService.DeleteAPIKey(prefix, id),

  // 策略
  policy: () => parse<PolicyDoc>(WeftService.GetPolicy()),
  savePolicy: (content: string) => WeftService.SavePolicy(content),
  checkPolicy: (content: string) => WeftService.CheckPolicy(content),

  // 应用与书签
  apps: () => parse<AppBookmark[]>(WeftService.ListApps()),
  saveApp: (a: AppBookmark) => WeftService.SaveApp(JSON.stringify(a)),
  deleteApp: (id: string) => WeftService.DeleteApp(id),
  openApp: (id: string) => WeftService.OpenApp(id),
  openInBrowser: (id: string) => WeftService.OpenInBrowser(id),

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

  // 助手
  helperStatus: () => parse<HelperStatus>(WeftService.GetHelperStatus()),
  installHelper: () => parse<HelperStatus>(WeftService.InstallHelper()),
  uninstallHelper: () => parse<HelperStatus>(WeftService.UninstallHelper()),
}
