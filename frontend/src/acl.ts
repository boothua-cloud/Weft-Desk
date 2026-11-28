// ACL 策略的「表单 ↔ 文本」转换。
//
// 服务端存的是 HuJSON（headscale 还接受 YAML），表单只认 JSON/HuJSON，
// 所以这里做三件事：
//   1. 解析前先把 // 与 /* */ 注释、尾逗号去掉，再交给 JSON.parse；
//   2. 只接管 groups / tagOwners / acls 三段，其余字段（hosts、ssh、
//      autoApprovers、nodeAttrs、tests…）原样保留，保存时一起写回；
//   3. 规则里表单不认识的字段（比如用 ip 代替 dst）也原样保留，不丢配置。

export interface AclEntry {
  name: string
  values: string[]
}

export interface AclRule {
  action: string
  src: string[]
  dst: string[]
  // 表单不解析的字段，保存时按原样写回
  extra: Record<string, unknown>
}

export interface AclForm {
  groups: AclEntry[]
  tagOwners: AclEntry[]
  acls: AclRule[]
  extra: Record<string, unknown>
  // 原文里有注释 / 尾逗号，保存表单会把它们抹掉，需要提示用户
  hadComments: boolean
  // 原文是否显式写了 acls。空策略和「显式空 acls」语义不同，保存时不能凭空补一个
  hasAclsKey: boolean
  hasIPRules: boolean
}

export interface AclIssue {
  level: 'error' | 'warn'
  text: string
}

export type ParseResult = { ok: true; form: AclForm } | { ok: false; error: string }

// ---------- HuJSON 预处理 ----------

// 逐字符扫描，跳过字符串字面量里的 // 和 /*，其余注释替换成空格（保留换行）。
export function stripComments(text: string): { text: string; hadComments: boolean } {
  let out = ''
  let hadComments = false
  let i = 0
  let inString = false
  let escaped = false
  while (i < text.length) {
    const c = text[i]
    const next = text[i + 1]
    if (inString) {
      out += c
      if (escaped) escaped = false
      else if (c === '\\') escaped = true
      else if (c === '"') inString = false
      i++
      continue
    }
    if (c === '"') {
      inString = true
      out += c
      i++
      continue
    }
    if (c === '/' && next === '/') {
      hadComments = true
      while (i < text.length && text[i] !== '\n') i++
      continue
    }
    if (c === '/' && next === '*') {
      hadComments = true
      i += 2
      while (i < text.length && !(text[i] === '*' && text[i + 1] === '/')) {
        if (text[i] === '\n') out += '\n'
        i++
      }
      i += 2
      continue
    }
    out += c
    i++
  }
  return { text: out, hadComments }
}

// 去掉 ] 与 } 前的尾逗号。逗号只可能出现在字符串外（字符串内的逗号后接引号）。
function stripTrailingCommas(text: string): string {
  let prev = ''
  let cur = text
  while (prev !== cur) {
    prev = cur
    cur = cur.replace(/,(\s*[}\]])/g, '$1')
  }
  return cur
}

// ---------- 解析 ----------

function isStringArray(v: unknown): v is string[] {
  return Array.isArray(v) && v.every((x) => typeof x === 'string')
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

// groups 的键必须写成 group:xxx，tagOwners 的键必须写成 tag:xxx，
// 服务端解析策略时会直接校验前缀（group must start with 'group:'）。
// 所以两边进出都过一遍，只补前缀、不改名字本身。
export function groupKey(name: string): string {
  const t = name.trim()
  if (!t) return ''
  return t.startsWith('group:') ? t : 'group:' + t
}

export function tagKey(name: string): string {
  const t = name.trim()
  if (!t) return ''
  return t.startsWith('tag:') ? t : 'tag:' + t
}

function readEntries(raw: unknown, key: string, kind: 'group' | 'tag'): { entries: AclEntry[]; error?: string } {
  if (raw === undefined) return { entries: [] }
  if (!isObject(raw)) return { entries: [], error: `${key} 不是对象，无法用表单编辑` }
  const fix = kind === 'group' ? groupKey : tagKey
  const entries: AclEntry[] = []
  for (const [name, v] of Object.entries(raw)) {
    if (!isStringArray(v)) return { entries: [], error: `${key} 里「${name}」的值不是字符串数组，无法用表单编辑` }
    const fixed = fix(name)
    // groups / tagOwners 里的值都是身份（成员、特殊组），统一成带 @ 的写法
    const values = v.map(userToken)
    const same = entries.find((e) => e.name === fixed)
    // 原本就有 group:x 又写了一个 x 的情况：合并成一个，别丢成员
    if (same) {
      for (const t of values) if (!same.values.includes(t)) same.values.push(t)
    } else {
      entries.push({ name: fixed, values })
    }
  }
  return { entries }
}

// 组名 → 完整 token。规则里可能把组写成了裸名字（admin 而不是 group:admin），
// 键补前缀之后这些引用也得跟着改，否则服务端找不到组。
function groupRefs(entries: AclEntry[]): Map<string, string> {
  const m = new Map<string, string>()
  for (const e of entries) {
    if (!e.name) continue
    m.set(e.name, e.name)
    m.set(e.name.slice('group:'.length), e.name)
  }
  return m
}

function refToken(token: string, refs: Map<string, string>): string {
  const t = token.trim()
  if (!t) return t
  if (t === '*' || t.startsWith('group:') || t.startsWith('tag:') || t.startsWith('autogroup:')) return t
  // 裸名字可能被 userToken 补了 @，比对时去掉
  const hit = refs.get(t.replace(/@$/, ''))
  return hit ?? t
}

function readRules(raw: unknown, refs: Map<string, string> = new Map()): { rules: AclRule[]; error?: string; hasIP: boolean } {
  if (raw === undefined) return { rules: [], hasIP: false }
  if (!Array.isArray(raw)) return { rules: [], error: 'acls 不是数组，无法用表单编辑', hasIP: false }
  const rules: AclRule[] = []
  let hasIP = false
  for (let i = 0; i < raw.length; i++) {
    const r = raw[i]
    const at = `acls 第 ${i + 1} 条`
    if (!isObject(r)) return { rules: [], error: `${at}不是对象，无法用表单编辑`, hasIP }
    const src = r.src
    const dst = r.dst
    if (src !== undefined && !isStringArray(src)) return { rules: [], error: `${at}的 src 不是字符串数组`, hasIP }
    if (dst !== undefined && !isStringArray(dst)) return { rules: [], error: `${at}的 dst 不是字符串数组`, hasIP }
    if (dst === undefined && r.ip !== undefined) hasIP = true
    const extra: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(r)) {
      if (k === 'action' || k === 'src' || k === 'dst') continue
      extra[k] = v
    }
    rules.push({
      action: typeof r.action === 'string' ? r.action : 'accept',
      src: src ? src.map((s) => userToken(refToken(s, refs))) : [],
      dst: dst ? [...dst] : [],
      extra,
    })
  }
  return { rules, hasIP }
}

export function parseAcl(text: string): ParseResult {
  if (!text.trim()) {
    return {
      ok: true,
      form: { groups: [], tagOwners: [], acls: [], extra: {}, hadComments: false, hasAclsKey: false, hasIPRules: false },
    }
  }
  const stripped = stripComments(text)
  let root: unknown
  try {
    root = JSON.parse(stripTrailingCommas(stripped.text))
  } catch (err: any) {
    return { ok: false, error: '策略不是合法 JSON：' + (err?.message ?? err) }
  }
  if (!isObject(root)) return { ok: false, error: '策略根节点不是对象' }

  const groups = readEntries(root.groups, 'groups', 'group')
  if (groups.error) return { ok: false, error: groups.error }
  const tagOwners = readEntries(root.tagOwners, 'tagOwners', 'tag')
  if (tagOwners.error) return { ok: false, error: tagOwners.error }
  const rules = readRules(root.acls, groupRefs(groups.entries))
  if (rules.error) return { ok: false, error: rules.error }

  const extra: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(root)) {
    if (k === 'groups' || k === 'tagOwners' || k === 'acls') continue
    extra[k] = v
  }

  return {
    ok: true,
    form: {
      groups: groups.entries,
      tagOwners: tagOwners.entries,
      acls: rules.rules,
      extra,
      hadComments: stripped.hadComments,
      hasAclsKey: 'acls' in root,
      hasIPRules: rules.hasIP,
    },
  }
}

// ---------- 序列化 ----------

export function serializeAcl(form: AclForm): string {
  const out: Record<string, unknown> = {}
  const groups: Record<string, string[]> = {}
  for (const g of form.groups) {
    const k = groupKey(g.name)
    if (k) groups[k] = g.values.map(userToken)
  }
  if (Object.keys(groups).length) out.groups = groups

  const tagOwners: Record<string, string[]> = {}
  for (const t of form.tagOwners) {
    const k = tagKey(t.name)
    if (k) tagOwners[k] = t.values.map(userToken)
  }
  if (Object.keys(tagOwners).length) out.tagOwners = tagOwners

  const refs = new Map<string, string>()
  for (const k of Object.keys(groups)) {
    refs.set(k, k)
    refs.set(k.slice('group:'.length), k)
  }

  // 原本没有 acls 且一条规则都没有时保持「不写 acls」：空策略（全放行）
  // 与 "acls": []（全拒绝）是两回事，不能替用户做这个决定
  if (form.acls.length || form.hasAclsKey) {
    out.acls = form.acls.map((r) => {
      const rule: Record<string, unknown> = { action: r.action || 'accept' }
      rule.src = r.src.map((s) => userToken(refToken(s, refs)))
      // dst 留空且原规则用 ip 字段时不写 dst，避免伪造出一个空目标
      if (r.dst.length || !('ip' in r.extra)) rule.dst = r.dst
      return Object.assign(rule, r.extra)
    })
  }

  for (const [k, v] of Object.entries(form.extra)) out[k] = v
  return JSON.stringify(out, null, 2) + '\n'
}

// ---------- 规则检查 ----------

// 本地只做「明显写错」的检查，语义仍以服务端校验为准。
// ctx 提供「服务端认得的设备名和 hosts 别名」：dst 里只能写这些，
// 写成人名 / 组名服务端会直接回 host not defined in policy。
export interface ValidateCtx {
  hosts?: string[]
  devices?: string[]
  /** 成员名。写进 dst 是最常见的错法，单独认出来给一句人话 */
  members?: string[]
  /** 已生效的子网路由。tailscale 只放行 dst 里写到的目的地，网段不写就收不到这条路由 */
  routes?: string[]
}

const IP_LIKE = /^(\d{1,3}(\.\d{1,3}){3}|[0-9a-fA-F:]*:[0-9a-fA-F:]*)(\/\d{1,3})?$/

export function validateAcl(form: AclForm, ctx: ValidateCtx = {}): AclIssue[] {
  const issues: AclIssue[] = []
  const groupNames = new Set(form.groups.map((g) => groupKey(g.name)).filter(Boolean))
  const tagNames = new Set(form.tagOwners.map((t) => tagKey(t.name)).filter(Boolean))

  const dup = new Set<string>()
  for (const g of form.groups) {
    const n = g.name.trim()
    if (!n) { issues.push({ level: 'error', text: '有用户组没填名字' }); continue }
    const k = groupKey(n)
    if (k !== n) issues.push({ level: 'warn', text: `「${n}」会补成 ${k}（服务端要求 group: 开头）` })
    if (dup.has(k)) issues.push({ level: 'error', text: `用户组「${k}」重复定义` })
    dup.add(k)
    if (!g.values.length) issues.push({ level: 'warn', text: `用户组「${k}」没有成员` })
  }

  const dupTag = new Set<string>()
  for (const t of form.tagOwners) {
    const n = t.name.trim()
    if (!n) { issues.push({ level: 'error', text: '有标签没填名字' }); continue }
    const k = tagKey(n)
    if (k !== n) issues.push({ level: 'warn', text: `「${n}」会补成 ${k}（服务端要求 tag: 开头）` })
    if (dupTag.has(k)) issues.push({ level: 'error', text: `标签「${k}」重复定义` })
    dupTag.add(k)
    if (!t.values.length) issues.push({ level: 'warn', text: `标签「${k}」没有归属` })
  }

  const members = new Set<string>()
  for (const v of [...(ctx.members ?? []), ...form.groups.flatMap((g) => g.values)]) {
    const t = v.trim()
    if (!t) continue
    // dst 里可能手打成 joey 或 joey@，两种都算「这是个人名」
    members.add(t)
    members.add(userToken(t))
    members.add(t.replace(/@$/, ''))
  }
  // 服务端认得的成员（统一成 joey@ 的形式），用来抓「名字写错了」这类错
  const known = new Set((ctx.members ?? []).map(userToken).filter(Boolean))
  // 最常见的一种错法：把显示名（Alice）当用户名写进策略，服务端里其实叫 alice。
  // 名字对不上时顺手把服务端那个名字带出来，用户知道该换成谁。
  const near = (v: string): string => {
    const bare = v.replace(/@$/, '').toLowerCase()
    for (const k of known) if (tokenLabel(k).toLowerCase() === bare) return tokenLabel(k)
    return ''
  }
  const unknownMember = (v: string): string => {
    const hint = near(v)
    return `「${tokenLabel(v)}」服务端不认识，可能写错了${hint ? `（服务端里叫「${hint}」）` : ''}`
  }
  const knownDst = new Set([...(ctx.hosts ?? []), ...(ctx.devices ?? [])].map((v) => v.trim()).filter(Boolean))

  for (const g of form.groups) {
    const n = g.name.trim()
    if (!n) continue
    for (const v of g.values) {
      if (v.startsWith('autogroup:') || v.startsWith('group:') || v.startsWith('tag:')) continue
      if (known.size && !known.has(v)) {
        issues.push({ level: 'warn', text: `用户组「${n}」的成员${unknownMember(v)}` })
      }
    }
  }

  form.acls.forEach((r, i) => {
    const at = `第 ${i + 1} 条规则`
    if (!r.src.length) issues.push({ level: 'error', text: `${at}没有来源` })
    if (!r.dst.length && !('ip' in r.extra)) issues.push({ level: 'error', text: `${at}没有目标` })
    if (r.action !== 'accept') issues.push({ level: 'warn', text: `${at}的动作是「${r.action}」，目前只支持 accept` })
    for (const s of r.src) {
      if (s.startsWith('group:')) {
        if (!groupNames.has(s)) issues.push({ level: 'warn', text: `${at}引用了没定义的 ${s}` })
        continue
      }
      if (s.startsWith('tag:')) {
        if (!tagNames.has(s)) issues.push({ level: 'warn', text: `${at}引用了 tagOwners 里没有的 ${s}` })
        continue
      }
      if (s === '*' || s.startsWith('autogroup:')) continue
      // 组名没写前缀（admin 而不是 group:admin）时，保存会自动补，这里说明一下
      const bareRef = groupNames.has('group:' + s.replace(/@$/, ''))
      if (bareRef) {
        issues.push({ level: 'warn', text: `${at}的来源「${tokenLabel(s)}」会按 group:${tokenLabel(s)} 处理` })
        continue
      }
      if (known.size && !known.has(s)) {
        issues.push({ level: 'warn', text: `${at}的来源${unknownMember(s)}` })
      }
    }
    for (const d of r.dst) {
      if (d === '*:*') continue
      // 用 parseDstEntry 拆，否则 IPv6（fd7a::1）会被当成「host:port」
      const { host, ports } = parseDstEntry(d)
      // 只有连冒号都没有才算「忘了写端口」；IPv6 本身带冒号，不能误判
      if (!ports.length && !d.includes(':')) {
        issues.push({ level: 'warn', text: `${at}的目标「${d}」缺端口，应写成 host:port` })
      }
      if (host.startsWith('tag:') && !tagNames.has(host)) {
        issues.push({ level: 'warn', text: `${at}的目标 ${host} 没在 tagOwners 里定义` })
      }
      // dst 只认设备 / 标签 / autogroup:self / IP，写成组或人名服务端必然报错
      if (host.startsWith('group:')) {
        issues.push({ level: 'error', text: `${at}的目标不能写用户组「${host}」。改成设备或标签，例如 tag:server:22` })
        continue
      }
      if (host === '*' || host.startsWith('autogroup:') || host.startsWith('tag:')) continue
      if (IP_LIKE.test(host)) continue
      if (knownDst.has(host)) continue
      if (members.has(host)) {
        issues.push({ level: 'error', text: `${at}的目标「${host}」是成员名，服务端不认。目标只能写设备名、标签或 hosts 别名` })
        continue
      }
      if (ctx.devices?.length || ctx.hosts?.length) {
        issues.push({ level: 'error', text: `${at}的目标「${host}」不是已知设备或 hosts 别名，服务端会报 host not defined` })
      }
    }
  })

  // 子网路由最容易漏：dst 只写了设备/标签，网段没写，客户端就收不到这条路由。
  // 这条提示正好对应「配完 ACL 后访问不到内网」这个现象。
  const routes = (ctx.routes ?? []).map((r) => r.trim()).filter(Boolean)
  if (routes.length && form.acls.length) {
    const covered = new Set<string>()
    for (const r of form.acls) for (const d of r.dst) covered.add(parseDstEntry(d).host)
    for (const cidr of [...new Set(routes)]) {
      if (covered.has(cidr)) continue
      issues.push({
        level: 'warn',
        text: `子网路由 ${cidr} 没写进任何目标，连上后不会生效。在目标里加一组 ${cidr}`,
      })
    }
  }

  return issues
}

// ---------- 服务与端口 ----------

// 常用服务。ACL 落地时仍然是端口号，这里只是把「22」翻译成「SSH」，
// 免得用户去背端口表。没列到的端口照样可以手填。
export interface ServiceDef {
  port: string
  label: string
  short: string
}

export const SERVICES: ServiceDef[] = [
  { port: '22', label: 'SSH', short: 'SSH' },
  { port: '80', label: 'HTTP', short: 'HTTP' },
  { port: '443', label: 'HTTPS', short: 'HTTPS' },
  { port: '3389', label: '远程桌面', short: 'RDP' },
  { port: '445', label: 'SMB 文件共享', short: 'SMB' },
  { port: '5900', label: 'VNC 远程桌面', short: 'VNC' },
  { port: '3306', label: 'MySQL', short: 'MySQL' },
  { port: '5432', label: 'PostgreSQL', short: 'PG' },
  { port: '6379', label: 'Redis', short: 'Redis' },
  { port: '8080', label: 'HTTP 8080', short: '8080' },
  { port: '53', label: 'DNS', short: 'DNS' },
  { port: '123', label: 'NTP 时间同步', short: 'NTP' },
]

const SERVICE_BY_PORT = new Map(SERVICES.map((s) => [s.port, s]))

export function portLabel(port: string): string {
  if (port === '*') return '全部端口'
  const s = SERVICE_BY_PORT.get(port)
  return s ? `${s.label} · ${port}` : port
}

export function portShort(port: string): string {
  if (port === '*') return '全部端口'
  return SERVICE_BY_PORT.get(port)?.short ?? port
}

// 端口排序：全部端口在前，其余按数字
function portRank(p: string): number {
  if (p === '*') return -1
  const m = /^(\d+)/.exec(p)
  return m ? Number(m[1]) : 99999
}

export function normalizePorts(ports: string[]): string[] {
  const uniq = [...new Set(ports.map((p) => p.trim()).filter(Boolean))]
  if (uniq.includes('*')) return ['*']
  return uniq.sort((a, b) => portRank(a) - portRank(b))
}

// ---------- 目标：host 与端口拆开 ----------

export interface DstEntry {
  host: string
  ports: string[]
}

// 尾部像端口才拆：tag:server 的尾部是 server，整串当 host；
// nas:5000、tag:server:22,80 才拆出 host 与端口。
const PORT_TAIL = /^(\*|\d[\d,\-]*)$/

export function parseDstEntry(s: string): DstEntry {
  const t = s.trim()
  // 裸 `*` 在 headscale 里等价于「所有设备的所有端口」，
  // 归组时要补回端口，否则会被写成不合法的 `*`
  if (t === '*') return { host: '*', ports: ['*'] }
  const i = t.lastIndexOf(':')
  if (i < 0) return { host: t, ports: [] }
  const head = t.slice(0, i)
  const tail = t.slice(i + 1)
  if (!PORT_TAIL.test(tail)) return { host: t, ports: [] }
  // IPv6 字面量（fd7a::1）最后一段也长得像端口，靠冒号数量排除：
  // 只有 tag: / group: / autogroup: 前缀允许带一个冒号，其余多冒号一律当整串 host
  if (head.includes(':') && !/^(tag|group|autogroup):[^:]+$/.test(head)) {
    return { host: t, ports: [] }
  }
  return {
    host: head,
    ports: tail.split(',').map((x) => x.trim()).filter(Boolean),
  }
}

export function formatDstEntry(host: string, ports: string[]): string {
  const p = normalizePorts(ports)
  if (!p.length) return host
  return `${host}:${p.join(',')}`
}

// 一条规则里的目标按「端口相同」归组，界面上就能写成
// 「服务器、CI → SSH/HTTPS」，而不用每个目标重复一遍端口。
export interface DstGroup {
  hosts: string[]
  ports: string[]
}

export function groupDst(dst: string[]): DstGroup[] {
  const out: DstGroup[] = []
  for (const d of dst) {
    const e = parseDstEntry(d)
    const key = normalizePorts(e.ports).join(',')
    const hit = out.find((g) => normalizePorts(g.ports).join(',') === key)
    if (hit) hit.hosts.push(e.host)
    else out.push({ hosts: [e.host], ports: e.ports })
  }
  return out
}

export function flattenDst(groups: DstGroup[]): string[] {
  const out: string[] = []
  for (const g of groups) {
    for (const h of g.hosts) {
      if (!h.trim()) continue
      out.push(formatDstEntry(h, g.ports))
    }
  }
  return out
}

// ---------- 名字 ----------

export type TokenKind = 'all' | 'group' | 'tag' | 'autogroup' | 'member'

export function tokenKind(token: string): TokenKind {
  if (token === '*') return 'all'
  if (token.startsWith('group:')) return 'group'
  if (token.startsWith('tag:')) return 'tag'
  if (token.startsWith('autogroup:')) return 'autogroup'
  return 'member'
}

const AUTOGROUP_LABEL: Record<string, string> = {
  'autogroup:member': '所有成员',
  'autogroup:self': '本人设备',
  'autogroup:admin': '所有管理员',
}

// headscale 的 ACL 里，人一律写成「用户名@」：解析器只检查有没有 @，
// 查找时再把结尾的 @ 去掉（strings.TrimSuffix），不带 @ 直接 400
// username must contain @。所以这里只给「完全没有 @」的名字补一个，
// 邮箱用户名（a@b.com）、域通配（user:*@b.com）原样保留——它们本来就含 @，
// 再补一个反而会破坏 user:*@ 的域匹配。
export function userToken(name: string): string {
  const t = name.trim()
  if (!t) return ''
  if (t === '*' || t.startsWith('group:') || t.startsWith('tag:') || t.startsWith('autogroup:')) return t
  return t.includes('@') ? t : t + '@'
}

// 展示用的短名：group:devs 显示 devs，tag:server 显示 server，
// 成员名去掉结尾的 @（那是 headscale 的记号，不是名字的一部分）。
// 前缀在 chip 的角标里单独标出来。
export function tokenLabel(token: string): string {
  if (token === '*') return '所有人'
  if (token.startsWith('group:')) return token.slice(6)
  if (token.startsWith('tag:')) return token.slice(4)
  if (token.startsWith('autogroup:')) return AUTOGROUP_LABEL[token] ?? token.slice(10)
  return token.replace(/@$/, '')
}

// 目标位置上的 `*` 是「所有设备」，和来源处的「所有人」不是一回事
export function dstLabel(token: string): string {
  if (token === '*') return '所有设备'
  return tokenLabel(token)
}

export function tokenPrefix(token: string): string {
  switch (tokenKind(token)) {
    case 'group': return '组'
    case 'tag': return '标签'
    case 'autogroup': return ''
    case 'all': return ''
    default: return '成员'
  }
}

// 目标位置上的前缀另有说法：那里只有设备、标签、别名，
// 一律标「成员」会误导（组成员本来就不该出现在目标里）
export function dstPrefix(token: string): string {
  return token.startsWith('tag:') ? '标签' : ''
}

// ---------- 权限计算（给矩阵用） ----------

export interface AccessPeer {
  name: string
  hostname?: string
  user: string
  tags: string[]
  ip: string
  online: boolean
  /** 这台设备通告并已生效的子网路由（192.168.10.0/24 这种） */
  routes?: string[]
}

// 一个来源身份能用的 token 集合：自己的名字、所在的组、以及
// 「所有成员」这种自动组。设备作为来源时还有它的标签。
export function ownedTokens(kind: 'member' | 'device', opts: {
  name: string
  groups?: string[]
  tags?: string[]
  hostname?: string
  ip?: string
}): Set<string> {
  const s = new Set<string>()
  if (opts.name) s.add(opts.name)
  if (kind === 'device') {
    if (opts.hostname) s.add(opts.hostname)
    if (opts.ip) s.add(opts.ip)
    for (const t of opts.tags ?? []) if (t) s.add(t)
  } else {
    for (const g of opts.groups ?? []) if (g) s.add(g)
  }
  return s
}

// 返回这条「来源 → 目标」允许的端口；空数组表示不通。
// self 为真时（目标设备属于来源本人）autogroup:self 才算命中。
export function accessPorts(form: AclForm, srcOwned: Set<string>, dstOwned: Set<string>, self = false): string[] {
  const ports: string[] = []
  for (const r of form.acls) {
    const srcHit = r.src.some((s) => {
      if (s === 'autogroup:self') return self
      // 谁是管理员由服务端判定，这里拿不到，按「所有成员」估算。
      // 宁可多显示也不漏显示：漏了会让人以为已经收紧。
      if (s === '*' || s === 'autogroup:member' || s === 'autogroup:admin') return true
      return srcOwned.has(s)
    })
    if (!srcHit) continue
    for (const d of r.dst) {
      const e = parseDstEntry(d)
      // autogroup:self 表示「自己的设备」，只有目标设备属于本人时才算命中
      if (e.host === 'autogroup:self') {
        if (self) ports.push(...e.ports)
        continue
      }
      if (e.host !== '*' && !dstOwned.has(e.host)) continue
      ports.push(...e.ports)
    }
  }
  return normalizePorts(ports)
}

// 一句话解释一条规则，放在规则卡片底部，方便确认「我到底写了什么」
export function ruleSummary(rule: AclRule): string {
  const src = rule.src.length ? rule.src.map(tokenLabel).join('、') : '（没写来源）'
  const groups = groupDst(rule.dst).filter((g) => g.hosts.length)
  if (!groups.length) return `${src} 可以访问（没写目标）`
  const parts = groups.map((g) => {
    const hosts = g.hosts.map(dstLabel).join('、')
    const ports = g.ports.length ? g.ports.map(portShort).join('、') : '全部端口'
    return `${hosts} 的 ${ports}`
  })
  const verb = rule.action === 'accept' ? '可以访问' : `${rule.action}`
  return `${src} ${verb} ${parts.join('；')}`
}

// 服务端有几类错误是「它的配置问题」，改代码没用，但用户得知道去服务器上动什么。
// 这里把原始报文翻译成一句能照做的话；认不出来就原样返回，别瞎猜。
export function explainPolicyError(message: string): string {
  const m = message ?? ''
  if (m.includes("update is disabled for modes other than 'database'")) {
    return '这台 headscale 把策略存在文件里（policy.mode: file），API 不能改。两条路：把 config.yaml 的 policy.mode 改成 database 后重启，再用 headscale policy set -f 把现有策略导进库；或者保持 file 模式，直接改服务器上的文件，然后 systemctl reload headscale'
  }
  if (m.includes('username must contain @')) {
    return '用户名要写成「名字@」（邮箱用户直接写 a@b.com），以服务端用户名为准'
  }
  if (m.includes("group must start with 'group:'")) {
    return 'groups 的键要以 group: 开头，tagOwners 的键要以 tag: 开头'
  }
  return ''
}

// 源码模式用：直接看 JSON 里的键，指出服务端一定会拒的写法。
// 源码是「所见即所发」，这里只提示，不替用户改。
export function policyKeyHints(text: string): string[] {
  const hints: string[] = []
  let root: unknown
  try {
    root = JSON.parse(stripTrailingCommas(stripComments(text).text))
  } catch {
    return hints
  }
  if (!isObject(root)) return hints
  const check = (raw: unknown, key: string, fix: (s: string) => string) => {
    if (!isObject(raw)) return
    for (const n of Object.keys(raw)) {
      const f = fix(n)
      if (f !== n) hints.push(`${key} 里的「${n}」要写成「${f}」`)
    }
  }
  check(root.groups, 'groups', groupKey)
  check(root.tagOwners, 'tagOwners', tagKey)
  return hints
}

// 文本里出现过的 group: / tag: 名字，用来给输入框做补全
export function collectSymbols(form: AclForm): string[] {
  const set = new Set<string>()
  form.groups.forEach((g) => groupKey(g.name) && set.add(groupKey(g.name)))
  form.tagOwners.forEach((t) => tagKey(t.name) && set.add(tagKey(t.name)))
  form.acls.forEach((r) => [...r.src, ...r.dst].forEach((v) => {
    const host = v.includes(':') ? v.slice(0, v.lastIndexOf(':')) : v
    if (host.startsWith('group:') || host.startsWith('tag:')) set.add(host)
  }))
  form.groups.forEach((g) => g.values.forEach((v) => set.add(v)))
  return [...set].sort()
}
