<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type State, type PeerInfo, type DiagReport, type SelfReport, type PeerPort } from '../api'

const props = defineProps<{ state: State | null }>()
const emit = defineEmits(['notify', 'navigate'])

// 临时端口：没有登记过服务的设备才用它。有书签/转发规则的设备优先点它自己的服务。
const port = ref(22)
const reports = ref<Record<string, DiagReport>>({})
const running = ref<Record<string, boolean>>({})
const failures = ref<Record<string, string>>({})
const self = ref<SelfReport | null>(null)
const selfBusy = ref(false)
const ports = ref<PeerPort[]>([])

const peers = computed(() => props.state?.peers ?? [])
const onlineCount = computed(() => peers.value.filter((p) => p.online).length)
const busy = computed(() => Object.values(running.value).some(Boolean))

// 一次「全部检查」之后要能一眼看出几台通几台不通
const summary = computed(() => {
  const done = Object.values(reports.value)
  if (!done.length) return ''
  const ok = done.filter((r) => !r.dialErr).length
  return ok === done.length ? `${ok} 台全通` : `${ok} 通 / ${done.length - ok} 不通`
})

function pathOf(p: PeerInfo): string {
  return p.curAddr ? '直连' : 'DERP'
}

function portsFor(ip: string): PeerPort[] {
  return ports.value.filter((s) => s.ip === ip)
}

async function runSelf() {
  selfBusy.value = true
  try {
    self.value = await api.selfCheck()
  } catch (err: any) {
    emit('notify', '自检失败：' + String(err?.message ?? err))
  } finally {
    selfBusy.value = false
  }
}

async function loadPorts() {
  try {
    ports.value = (await api.peerPorts()) ?? []
  } catch {
    ports.value = []
  }
}

async function run(p: PeerInfo, at?: number) {
  if (!p.ip) return
  const key = p.ip
  const target = at ?? (port.value || 22)
  running.value = { ...running.value, [key]: true }
  failures.value = { ...failures.value, [key]: '' }
  try {
    const r = await api.diagnose(p.ip, target)
    // hints 可能是 null（Go 的 nil 切片），模板里要当数组用，这里补上
    reports.value = { ...reports.value, [key]: { ...r, hints: r.hints ?? [] } }
    emit('notify', `${p.name || key}:${target} ` + (r.dialErr ? '不通' : '通路正常'))
  } catch (err: any) {
    failures.value = { ...failures.value, [key]: String(err?.message ?? err) }
    emit('notify', '检查失败')
  } finally {
    running.value = { ...running.value, [key]: false }
  }
}

function checkPort(p: PeerInfo, s: PeerPort) {
  port.value = s.port
  run(p, s.port)
}

function isActive(p: PeerInfo, s: PeerPort): boolean {
  const r = reports.value[p.ip]
  return !!r && r.port === s.port
}

// 结论用颜色分：全通绿、部分通黄、不通红
function verdictTone(r: DiagReport): string {
  if (!r.dialErr) return r.pingLost === 3 ? 'warn' : 'ok'
  if (!r.online) return 'bad'
  return 'bad'
}

// 抖动 = 三次探测里最慢减最快。这里原来放的是「收发」，可那是这台设备
// 开机以来的累计流量：摆在一个 3 毫秒的探测旁边，等于说「ping 传了 39 KB」。
function jitter(p: PeerInfo): number {
  const r = reports.value[p.ip]
  if (!r || r.pingMs < 0 || r.pingWorst < 0) return 0
  return Math.max(0, Math.round(r.pingWorst - r.pingMs))
}

function jitterText(p: PeerInfo): string {
  const r = reports.value[p.ip]
  if (!r || r.pingMs < 0) return '—'
  return jitter(p) + ' ms'
}

// 没配管理 Key 就改不了策略，别让人去点一个改不了的页面。
function hintList(r: DiagReport): string[] {
  if (props.state?.hasApiKey) return r.hints
  return r.hints.map((h) =>
    h.includes('去 ACL 页') ? '让管理员在服务端策略里加一条，保存后立刻生效' : h,
  )
}

// 四步里每一步的状态：on 通过、off 断在这、unk 没有数据（没配管理 Key）
function stepTone(r: { known: boolean; advertised: boolean; approved: boolean; accepted: boolean; peerOnline: boolean }, step: number): string {
  switch (step) {
    case 1:
      return r.known ? (r.advertised ? 'on' : 'off') : 'unk'
    case 2:
      return r.known ? (r.approved ? 'on' : 'off') : 'unk'
    case 3:
      return r.accepted ? 'on' : 'off'
    default:
      // 本机还没接受这条路由时，「可达」无从谈起，别给一个看着像结论的绿点
      return !r.accepted ? 'unk' : r.peerOnline ? 'on' : 'off'
  }
}

// 排查问题时最省事的方式是把这一屏发给别人，所以拼一段纯文本
// ACL 的三种说法。refused 不查规则表也能定论：包到过对端，就是放行了。
function aclLabel(r: DiagReport): string {
  if (r.dialCode === 'refused') return '已放行（对端回了拒绝）'
  if (!r.aclChecked) return '没读到规则'
  if (r.aclState === 'allow') return '放行了'
  if (r.aclState === 'deny') return '规则里没有这条'
  return '判不了：' + (r.aclNote || '规则里有客户端算不出来的写法')
}

function diagText(): string {
  const L: string[] = []
  const s = self.value
  L.push(`云栈 诊断  ${new Date().toLocaleString('zh-CN')}`)
  if (s) {
    L.push(`引擎 ${s.engine || '—'} · ${s.backend} · 登录${s.loggedIn ? '正常' : '未完成'}`)
    L.push(`TUN ${s.tun || '未建立'} · 本机 ${s.selfIP || '—'}${s.hostname ? ' (' + s.hostname + ')' : ''}`)
    L.push(`出口 ${s.direct ? '直连' : 'DERP ' + (s.relay || '?')} · 子网接管 ${s.acceptRoutes ? '开' : '关'}`)
    const c = s.control
    L.push(`服务端 ${c.url || '—'} ${c.ok ? Math.round(c.ms) + 'ms Headscale ' + (c.version || '?') : '不可达：' + c.err}`)
    for (const r of s.routes ?? []) L.push(`路由 ${r.prefix} → ${r.peer || r.peerIP}：${r.verdict}`)
    for (const h of s.health ?? []) L.push('健康告警：' + h)
  } else {
    L.push('（还没做本机自检）')
  }
  L.push('')
  for (const p of peers.value) {
    const r = reports.value[p.ip]
    L.push(`[${p.name || p.ip}] ${p.ip} ${p.online ? '在线' : '离线'}`)
    if (!r) {
      L.push('  未检查')
      continue
    }
    L.push(`  ${r.verdict}`)
    L.push(`  端口 ${r.port} · ${r.path}${r.relay ? ' ' + r.relay : ''} · 握手 ${r.handshake}`)
    L.push(`  探测 ${r.pingMs >= 0 ? Math.round(r.pingMs) + 'ms' : '失败'}（丢 ${r.pingLost}/3） · 拨测 ${r.dialErr ? '失败 ' + r.dialErr : Math.round(r.dialMs) + 'ms'}`)
    if (r.dialErr) L.push('  ACL ' + aclLabel(r))
    for (const h of hintList(r)) L.push('  - ' + h)
  }
  return L.join('\n')
}

async function copyAll() {
  const text = diagText()
  try {
    await navigator.clipboard.writeText(text)
    emit('notify', '诊断信息已复制，可以直接发给别人')
  } catch {
    emit('notify', '复制失败，系统不允许访问剪贴板')
  }
}

onMounted(() => {
  runSelf()
  loadPorts()
})

// 连上/断开之后自检结果就过期了，跟着状态刷一次
watch(
  () => props.state?.connected,
  () => {
    runSelf()
    loadPorts()
  },
)
</script>

<template>
  <div class="page-flex diag-page">
    <div class="diag-fixed">
      <div class="spread diag-bar">
        <div class="diag-self">
          <div class="diag-self-item">
            <span>本机</span>
            <b class="mono">{{ state?.selfIP || '—' }}</b>
          </div>
          <i></i>
          <div class="diag-self-item">
            <span>对端</span>
            <b>{{ onlineCount }} / {{ peers.length }} 在线</b>
          </div>
          <i></i>
          <div class="diag-self-item">
            <span>端口检查</span>
            <b :class="{ faint: !summary }">{{ summary || '还没检查' }}</b>
          </div>
        </div>
        <div class="row" style="gap:8px">
          <label class="diag-port">
            <span>临时端口</span>
            <input v-model.number="port" class="input mono" type="number" min="1" max="65535" style="width:74px" />
          </label>
          <button class="btn" :disabled="!state?.connected || busy" @click="peers.forEach((p) => run(p))">
            <span v-if="busy" class="spinner"></span>{{ busy ? '检查中…' : '全部检查' }}
          </button>
          <button class="btn sm" @click="copyAll">复制诊断</button>
        </div>
      </div>

      <div v-if="!state?.connected" class="notice warn diag-notice">
        还没连上。下面这台机器的自检会说清楚卡在哪一步——是没登录、网卡没建起来，还是服务端根本连不上。
      </div>
      <div v-else-if="state?.lastError" class="notice warn diag-notice">
        引擎报告：{{ state.lastError }}
      </div>
      <div v-if="state?.netErr" class="notice warn diag-notice">{{ state.netErr }}</div>
    </div>

    <div class="diag-scroll">
      <!-- 本机自检：连不上时这一块是唯一有内容的东西 -->
      <div class="card tight self-card">
        <div class="self-head">
          <div class="row" style="gap:9px;min-width:0">
            <span class="self-title">本机</span>
            <span v-if="self" class="state-pill" :class="self.tone">{{ self.verdict }}</span>
            <span v-else-if="selfBusy" class="faint">自检中…</span>
          </div>
          <div class="row" style="gap:10px">
            <span v-if="self" class="faint">{{ self.at.slice(11, 19) }}</span>
            <button class="btn sm" :disabled="selfBusy" @click="runSelf">
              <span v-if="selfBusy" class="spinner"></span>重新自检
            </button>
          </div>
        </div>

        <div v-if="self" class="facts">
          <div class="fact">
            <span>引擎</span>
            <b>{{ self.engine || '—' }} · {{ self.backend }}</b>
          </div>
          <div class="fact">
            <span>网卡</span>
            <b class="mono" :class="{ bad: !self.tun }">{{ self.tun || '没建起来' }}</b>
          </div>
          <div class="fact">
            <span>本机地址</span>
            <b class="mono">{{ self.selfIP || '—' }}</b>
          </div>
          <div class="fact">
            <span>服务端</span>
            <b v-if="self.control.ok">
              {{ Math.round(self.control.ms) }} ms<template v-if="self.control.version"> · {{ self.control.version }}</template>
            </b>
            <b v-else class="bad">{{ self.control.err || '不可达' }}</b>
          </div>
          <div class="fact">
            <span>出口</span>
            <b>{{ self.direct ? '直连' : 'DERP' }}<template v-if="!self.direct && self.relay"> · {{ self.relay }}</template></b>
          </div>
          <div class="fact">
            <span>接受子网路由</span>
            <b :class="{ bad: !self.acceptRoutes }">{{ self.acceptRoutes ? '开' : '关' }}</b>
          </div>
        </div>

        <ul v-if="self?.problems?.length" class="hints problems" :class="self.tone">
          <li v-for="(h, i) in self.problems" :key="i">{{ h }}</li>
        </ul>
      </div>

      <!-- 子网路由：四步分开看，任何一步断了症状都是「内网访问不了」 -->
      <div v-if="self?.routes?.length" class="card tight self-card">
        <div class="self-head">
          <div class="row" style="gap:9px">
            <span class="self-title">子网路由</span>
            <span class="faint">{{ self.routes.length }} 条</span>
          </div>
          <span class="faint">任何一步断了，内网都访问不了</span>
        </div>
        <div v-for="r in self.routes" :key="r.prefix + r.peerIP" class="route-row">
          <span class="mono route-prefix">{{ r.prefix }}</span>
          <span class="faint route-peer">{{ r.peer || r.peerIP }}</span>
          <span class="route-steps">
            <span class="route-step" :class="stepTone(r, 1)">通告</span>
            <span class="route-step" :class="stepTone(r, 2)">批准</span>
            <span class="route-step" :class="stepTone(r, 3)">接受</span>
            <span class="route-step" :class="stepTone(r, 4)">可达</span>
          </span>
          <b class="route-verdict" :class="r.tone">{{ r.verdict }}</b>
        </div>
        <div v-if="self.routes.some((r) => !r.known)" class="faint" style="margin-top:8px;font-size:10.5px">
          服务端的设备列表里没找到这几条路由对应的设备，只能按本机看到的情况判断——多半不在同一个账号下，或管理 Key 权限不够。
        </div>
      </div>

      <div v-if="!peers.length && state?.connected" class="card diag-empty">
        这段 tailnet 里还没有其他设备。对端上线后会自动出现在这里。
      </div>

      <div v-for="p in peers" :key="p.ip" class="card tight peer-card">
        <div class="peer-head">
          <div class="peer-id">
            <div class="peer-name">
              {{ p.name || p.ip }}
              <span class="tag" :class="p.online ? 'green' : ''">{{ p.online ? '在线' : '离线' }}</span>
            </div>
            <div class="peer-meta">
              <span class="mono">{{ p.ip }}</span>
              <template v-if="p.os"> · {{ p.os }}</template>
              <template v-if="p.lastSeen"> · 上次 {{ p.lastSeen }}</template>
            </div>
          </div>
          <div v-if="p.online" class="peer-path">
            <span class="path-pill" :class="p.curAddr ? 'direct' : 'relay'">{{ pathOf(p) }}</span>
            <span v-if="p.relay && !p.curAddr" class="faint relay-note">{{ p.relay }}</span>
          </div>
          <button class="btn sm" :disabled="running[p.ip] || !state?.connected" @click="run(p)">
            <span v-if="running[p.ip]" class="spinner"></span>{{ running[p.ip] ? '检查中…' : '检查' }}
          </button>
        </div>

        <div v-if="portsFor(p.ip).length" class="port-row">
          <span class="faint">这台机器上的服务</span>
          <button v-for="s in portsFor(p.ip)" :key="s.kind + s.port"
                  class="chip chip-btn" :class="{ active: isActive(p, s) }"
                  :disabled="running[p.ip] || !state?.connected"
                  @click="checkPort(p, s)">
            {{ s.label || (s.kind === 'app' ? '应用' : '转发') }} · {{ s.port }}
          </button>
        </div>

        <div v-if="failures[p.ip]" class="notice warn" style="margin-top:10px">{{ failures[p.ip] }}</div>

        <div v-if="reports[p.ip]" class="report">
          <div class="verdict" :class="verdictTone(reports[p.ip])">
            <span class="verdict-dot"></span>
            <span>{{ reports[p.ip].verdict }}</span>
          </div>

          <div class="facts">
            <div class="fact">
              <span>端口</span>
              <b class="mono">{{ reports[p.ip].port }}</b>
            </div>
            <div class="fact">
              <span>路径</span>
              <!-- 直连时对端挂在哪个 DERP 区域没有意义，只会让人以为在走中继 -->
              <b>{{ reports[p.ip].path }}<template v-if="reports[p.ip].path === 'DERP' && reports[p.ip].relay"> · {{ reports[p.ip].relay }}</template></b>
            </div>
            <div class="fact">
              <span>握手</span>
              <b>{{ reports[p.ip].handshake }}</b>
            </div>
            <div class="fact">
              <span>探测延迟</span>
              <b>{{ reports[p.ip].pingMs >= 0 ? Math.round(reports[p.ip].pingMs) + ' ms' : '—' }}</b>
            </div>
            <div class="fact">
              <span>抖动</span>
              <b :class="{ bad: jitter(p) > 50 }">{{ jitterText(p) }}</b>
            </div>
            <div class="fact">
              <span>丢包</span>
              <b :class="{ bad: reports[p.ip].pingLost }">{{ reports[p.ip].pingLost }} / 3</b>
            </div>
            <div class="fact">
              <span>拨测</span>
              <b :class="{ bad: reports[p.ip].dialErr }">
                {{ reports[p.ip].dialErr ? '失败' : Math.round(reports[p.ip].dialMs) + ' ms' }}
              </b>
            </div>
            <!-- ACL 只在没连上的时候才有解释价值。已连上就说明放行了，
                 再去说「规则里没找到这条」只会让人以为出了问题。
                 refused 更硬：对端回了拒绝，包已经到过对端，ACL 必然放行。 -->
            <div v-if="reports[p.ip].dialErr" class="fact">
              <span>ACL</span>
              <b v-if="reports[p.ip].dialCode === 'refused'" class="ok">已放行</b>
              <b v-else-if="!reports[p.ip].aclChecked" class="faint">没读到</b>
              <b v-else-if="reports[p.ip].aclState === 'allow'">放行了这个端口</b>
              <b v-else-if="reports[p.ip].aclState === 'deny'" class="bad"
                 :title="reports[p.ip].aclNote">规则里没有这条</b>
              <b v-else class="warn" :title="reports[p.ip].aclNote">判不了</b>
            </div>
            <div v-if="reports[p.ip].version" class="fact">
              <span>对端版本</span>
              <b class="mono">{{ reports[p.ip].version }}</b>
            </div>
          </div>

          <ul v-if="reports[p.ip].hints?.length" class="hints">
            <li v-for="(h, i) in hintList(reports[p.ip])" :key="i">{{ h }}</li>
          </ul>

          <div class="report-foot">
            <span class="faint">检查于 {{ reports[p.ip].at.slice(11, 19) }}</span>
            <!-- 只在「规则里确实没有」且不是对端主动拒绝时才给这个入口。
                 refused 说明 ACL 没拦，给按钮等于让人去修一条好规则。 -->
            <button v-if="state?.hasApiKey && reports[p.ip].dialCode !== 'refused'
                          && reports[p.ip].aclState === 'deny'"
                    class="link-btn" @click="emit('navigate', 'acl')">去看策略</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.diag-fixed {
  flex-shrink: 0;
  padding-bottom: 10px;
}
.diag-bar {
  gap: 14px;
  flex-wrap: wrap;
  padding: 10px 14px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r);
}
.diag-self {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
}
.diag-self i {
  width: 1px;
  height: 18px;
  background: var(--line);
}
.diag-self-item {
  display: flex;
  align-items: baseline;
  gap: 6px;
  font-size: 11.5px;
}
.diag-self-item span { color: var(--faint); }
.diag-self-item b { font-weight: 600; }
.diag-port {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--faint);
}
.diag-notice { margin-top: 10px; }

.diag-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding-bottom: 20px;
}
.diag-empty { color: var(--muted); font-size: 12px; text-align: center; padding: 40px 0; }

/* 本机自检块 */
.self-card { margin-bottom: 10px; }
.self-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
.self-title { font-size: 12.5px; font-weight: 600; }
.state-pill {
  padding: 2px 9px;
  border-radius: 999px;
  font-size: 10.5px;
  border: 1px solid var(--line);
  background: var(--surface-3);
  color: var(--muted);
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.state-pill.ok { color: var(--brand); border-color: var(--brand-line); background: var(--ok-soft); }
.state-pill.warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 25%, transparent); background: var(--warn-soft); }
.state-pill.bad { color: var(--danger); border-color: color-mix(in srgb, var(--danger) 25%, transparent); background: var(--danger-soft); }

/* 子网路由：四步状态 */
.route-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 0;
  border-top: 1px dashed var(--line-soft);
  font-size: 11px;
  flex-wrap: wrap;
}
.route-row:first-of-type { border-top: 0; }
.route-prefix { flex: none; }
.route-peer { min-width: 90px; }
.route-steps { display: flex; gap: 4px; flex-wrap: wrap; flex: none; }
.route-step {
  padding: 1px 7px;
  border-radius: 5px;
  font-size: 10px;
  border: 1px solid var(--line);
  color: var(--faint);
  background: var(--surface-2);
  white-space: nowrap;
}
.route-step.on { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 25%, transparent); background: var(--ok-soft); }
.route-step.off { color: var(--danger); border-color: color-mix(in srgb, var(--danger) 25%, transparent); background: var(--danger-soft); }
.route-step.unk { border-style: dashed; }
.route-verdict { margin-left: auto; font-weight: 500; }
.route-verdict.ok { color: var(--ok); }
.route-verdict.warn { color: var(--warn); }
.route-verdict.bad { color: var(--danger); }

.port-row {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-top: 10px;
  font-size: 10.5px;
}
.chip.chip-btn { font-size: 10.5px; }
.chip.chip-btn:disabled { opacity: .5; cursor: not-allowed; }

.peer-card { margin-bottom: 10px; }
.peer-head {
  display: flex;
  align-items: center;
  gap: 14px;
}
.peer-id { flex: 1; min-width: 0; }
.peer-name {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
}
.peer-meta {
  margin-top: 4px;
  font-size: 11px;
  color: var(--faint);
}
.peer-path {
  display: flex;
  align-items: baseline;
  gap: 6px;
  flex-shrink: 0;
}
.path-pill {
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 10.5px;
  border: 1px solid var(--line);
  background: var(--surface-3);
  color: var(--muted);
}
.path-pill.direct {
  color: var(--brand);
  border-color: var(--brand-line);
  background: var(--ok-soft);
}
.path-pill.relay {
  color: var(--warn);
  border-color: color-mix(in srgb, var(--warn) 25%, transparent);
  background: var(--warn-soft);
}
.relay-note { font-size: 10.5px; }

.report {
  margin-top: 12px;
  padding-top: 11px;
  border-top: 1px dashed var(--line-soft);
}
.verdict {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  font-weight: 600;
}
.verdict-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--muted);
  flex: none;
}
.verdict.ok { color: var(--brand); }
.verdict.ok .verdict-dot { background: var(--brand); }
.verdict.warn { color: var(--warn); }
.verdict.warn .verdict-dot { background: var(--warn); }
.verdict.bad { color: var(--danger); }
.verdict.bad .verdict-dot { background: var(--danger); }

.facts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 20px;
  margin-top: 10px;
}
.fact {
  display: flex;
  align-items: baseline;
  gap: 6px;
  font-size: 11px;
}
.fact span { color: var(--faint); }
.fact b { font-weight: 500; }
.fact b.bad { color: var(--danger); }
.fact b.ok { color: var(--ok); }
.fact b.warn { color: var(--warn); }

.hints {
  margin: 11px 0 0;
  padding-left: 16px;
  font-size: 11.5px;
  color: var(--muted);
  line-height: 1.85;
}
.hints li::marker { color: var(--faint); }
.hints.problems { color: var(--danger); }
.hints.problems li::marker { color: var(--danger); }
.hints.problems.warn { color: var(--warn); }
.hints.problems.warn li::marker { color: var(--warn); }

.report-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 10px;
  font-size: 10.5px;
}
</style>
