<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type State, type AppBookmark, type AuditEvent, type Device, type APIKey, type AuthKey } from '../api'
import Pick from '../components/Pick.vue'
import { reloadTick } from '../bus'

const props = defineProps<{ state: State | null }>()
const emit = defineEmits(['refresh', 'notify', 'navigate'])
const apps = ref<AppBookmark[]>([])
const audit = ref<AuditEvent[]>([])
const devices = ref<Device[]>([])
const apiKeys = ref<APIKey[]>([])
const authKeys = ref<AuthKey[]>([])
const busy = ref(false)
const opening = ref('')

// 连接/断开过程中：本地按钮状态 + 后端建卡状态，任一为真就显示加载态。
// state 还是 null 说明首次状态还没取回来，这时也得显示「加载中」，
// 否则启动瞬间会先闪一下「未连接」再跳到「正在连接」。
const connecting = computed(() => !props.state || busy.value || !!props.state?.connecting)
const disconnecting = ref(false)
const busyLabel = ref('')

const peers = computed(() => props.state?.peers ?? [])
// 没有管理 API Key 时，服务端相关的东西（设备到期、管理密钥）都不显示，
// 那些卡片拿到数据也只会是空的
const hasAPIKey = computed(() => !!props.state?.hasApiKey)
const online = computed(() => peers.value.filter(p => p.online).length)
const total = computed(() => peers.value.length)
const routes = computed(() => props.state?.routes ?? [])
const skipped = computed(() => props.state?.skipped ?? [])

// 到期提醒：节点密钥过期会直接掉线，管理密钥过期就管不了服务端
const EXPIRY_WARN_DAYS = 7
function daysLeft(ts?: string): number {
  if (!ts || ts.startsWith('0001') || ts.includes('永不过期')) return Infinity
  const d = new Date(ts.replace(' ', 'T'))
  if (isNaN(d.getTime())) return Infinity
  return Math.ceil((d.getTime() - Date.now()) / 86400000)
}
const selfKey = computed(() => apiKeys.value.find(k => k.isCurrent) ?? null)
const keyDays = computed(() => {
  const k = selfKey.value
  if (!k) return null
  const n = daysLeft(k.expires)
  return n === Infinity ? null : n
})
const keyExpires = computed(() => selfKey.value?.expires || '')
const renewing = ref(false)
const renewOpen = ref(false)
const renewDays = ref(90)
const renewOptions = [30, 60, 90, 180, 365].map(d => ({ value: d, label: d + ' 天' }))
function openRenew() { renewDays.value = 90; renewOpen.value = true }
async function renewAPIKey() {
  renewing.value = true
  try {
    await api.renewAPIKey(renewDays.value)
    renewOpen.value = false
    emit('notify', `管理密钥已续期，有效期 ${renewDays.value} 天`)
    await load()
  } catch (err: any) {
    emit('notify', '续期失败：' + (err?.message ?? err))
  } finally { renewing.value = false }
}

// ---------- 巡检 ----------

type Alert = {
  id: string
  level: 'warn' | 'bad'
  text: string
  sub?: string
  page: string
  action?: string
  renewId?: string
}

const OFFLINE_DAYS = 7
const UNUSED_KEY_DAYS = 7

function splitList(v?: string): string[] {
  return (v || '').split(/[,\s]+/).map((x) => x.trim()).filter(Boolean)
}

// 服务端给的时间是「2026-11-28 10:00」这种本地时间，直接 new Date 解析不了
function parseTS(ts?: string): number | null {
  if (!ts || ts.startsWith('0001') || ts.includes('永不过期')) return null
  const d = new Date(ts.replace(' ', 'T'))
  return Number.isNaN(d.getTime()) ? null : d.getTime()
}

// 四类都要人管：路由批了才能用、密钥到期会掉线、长期离线多半该清、
// 生成很久没用过的 Key 留着只是风险。
const alerts = computed<Alert[]>(() => {
  const out: Alert[] = []
  const now = Date.now()

  for (const d of devices.value) {
    const adv = splitList(d.advertisedRoutes)
    const approved = new Set(splitList(d.approvedRoutes))
    const pending = adv.filter((r) => !approved.has(r))
    if (pending.length) {
      out.push({
        id: 'route-' + d.id, level: 'warn',
        text: `${d.name} 有 ${pending.length} 段路由没批准`,
        sub: pending.join('、'), page: 'devices', action: '去批准',
      })
    }
  }

  for (const d of devices.value) {
    const n = daysLeft(d.expiry)
    if (n === Infinity || n > EXPIRY_WARN_DAYS) continue
    out.push({
      id: 'exp-' + d.id, level: n <= 0 ? 'bad' : 'warn',
      text: n <= 0 ? `${d.name} 已过期` : `${d.name} ${n} 天后到期`,
      sub: d.expiry, page: 'devices', renewId: d.id,
    })
  }

  for (const d of devices.value) {
    if (d.online) continue
    const seen = parseTS(d.lastSeen)
    if (seen === null) continue
    const days = Math.floor((now - seen) / 86400000)
    if (days >= OFFLINE_DAYS) {
      out.push({
        id: 'off-' + d.id, level: 'warn',
        text: `${d.name} 已离线 ${days} 天`, sub: d.lastSeen, page: 'devices',
      })
    }
  }

  for (const k of authKeys.value) {
    if (k.used || k.status.includes('过期')) continue
    const made = parseTS(k.created)
    if (made === null) continue
    const days = Math.floor((now - made) / 86400000)
    if (days >= UNUSED_KEY_DAYS) {
      out.push({
        id: 'key-' + k.id, level: 'warn',
        text: `授权 Key 生成 ${days} 天还没用过`,
        sub: [k.user, k.key].filter(Boolean).join(' · '), page: 'keys',
      })
    }
  }

  // 已经出问题的排前面，其余按发现顺序
  return out.sort((a, b) => (a.level === 'bad' ? 0 : 1) - (b.level === 'bad' ? 0 : 1))
})

function renewById(id?: string) {
  const d = devices.value.find((x) => x.id === id)
  if (d) renewDevice(d)
}
async function renewDevice(d: Device) {
  try {
    await api.disableDeviceExpiry(d.id)
    emit('notify', d.name + ' 已取消到期时间')
    await load()
  } catch (err: any) { emit('notify', '续期失败：' + (err?.message ?? err)) }
}

async function load() {
  try {
    const [a, e] = await Promise.all([api.apps(), api.audit()])
    apps.value = a
    audit.value = e
    // 到期检查要读服务端列表，没配管理密钥时会失败，忽略即可
    try {
      devices.value = (await api.devices()).items
      apiKeys.value = (await api.apiKeys()).items
      authKeys.value = (await api.authKeys()).items
    } catch { /* 未配置管理密钥 */ }
  } catch (err: any) {
    emit('notify', '加载总览数据失败：' + (err?.message ?? err))
  }
}
onMounted(load)
watch(() => props.state?.connected, load)
watch(reloadTick, load)

// Connect() 返回时只是 TUN 建好了，节点上线还要几秒。
// 加载态必须撑到真正连通，否则按钮一闪就恢复，看着像没生效。
async function waitConnected(timeoutMs = 120000) {
  const started = Date.now()
  while (Date.now() - started < timeoutMs) {
    const st = await api.state()
    if (st.connected) return st
    // 需要浏览器授权时节点不会自己上线，必须告诉用户去点那个链接
    if (st.loginURL) busyLabel.value = '请在浏览器完成授权…'
    else if (st.connecting) busyLabel.value = '正在建卡、等待授权…'
    else busyLabel.value = '等待节点上线…'
    await new Promise((r) => window.setTimeout(r, 800))
  }
  return null
}

async function act(kind: 'connect' | 'disconnect') {
  if (busy.value) return
  busy.value = true
  disconnecting.value = kind === 'disconnect'
  busyLabel.value = kind === 'connect' ? '正在连接…' : '正在断开…'
  try {
    if (kind === 'connect') {
      await api.connect()
      const st = await waitConnected()
      emit('refresh')
      if (st?.connected) {
        emit('notify', '隧道已建立')
      } else {
        emit('notify', st?.lastError
          ? '连接失败：' + st.lastError
          : '等了 2 分钟还没连上。检查服务端地址、Auth Key，或看终端日志里的 [system] 输出')
      }
    } else {
      await api.disconnect()
      emit('refresh')
      emit('notify', '隧道已断开')
    }
    await load()
  } catch (err: any) {
    emit('notify', '操作失败：' + (err?.message ?? err))
  } finally {
    busy.value = false
    disconnecting.value = false
    busyLabel.value = ''
  }
}

async function openApp(a: AppBookmark) {
  if (opening.value) return
  opening.value = a.id
  try {
    await api.openApp(a.id)
    emit('notify', '正在打开 ' + a.name)
  } catch (err: any) {
    emit('notify', '打开失败：' + (err?.message ?? err))
  } finally {
    opening.value = ''
  }
}

// 应用没配图标时用名字首字母 + 一个稳定色，看着比清一色绿点舒服
const palette = ['#0F6E56', '#1D6FD8', '#B45309', '#7C3AED', '#0E7490', '#BE185D']
function iconColor(a: AppBookmark, idx: number) {
  if (a.icon) return a.icon
  const seed = a.name || a.id || String(idx)
  let h = 0
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) % 9973
  return palette[h % palette.length]
}
function initial(a: AppBookmark) {
  return (a.name || '?').trim().slice(0, 1).toUpperCase()
}

function shortTime(t: string) {
  return t ? t.replace('T', ' ').slice(5, 16) : ''
}
function copyLogin() {
  if (!props.state?.loginURL) return
  navigator.clipboard?.writeText(props.state.loginURL)
  emit('notify', '登录链接已复制，粘到浏览器打开')
}
</script>

<template>
  <div>
    <!-- 主状态 -->
    <section class="hero">
      <div class="hero-deco"></div>
      <div class="hero-row">
        <div class="hero-state">
          <div class="hero-orb">
            <span class="dot" :class="connecting ? 'warn pulse' : (state?.connected ? 'ok pulse' : 'off')" style="width:12px;height:12px"></span>
          </div>
          <div>
            <div class="hero-title">
              <template v-if="connecting">
                <span class="spinner"></span>{{ disconnecting ? '正在断开…' : '正在连接…' }}
              </template>
              <template v-else>{{ state?.connected ? '隧道已连接' : '未连接' }}</template>
              <span class="status-pill">{{ connecting ? '处理中' : '系统接管 · TUN' }}</span>
            </div>
            <div class="hero-sub">
              <template v-if="connecting">
                <span>{{ busyLabel || '正在恢复上次的连接…' }}</span>
                <i class="hero-sep"></i>
                <!-- Windows 版启动时就已经是管理员，这里不会再弹授权 -->
                <span v-if="state?.platform === 'windows'" class="faint">正在创建虚拟网卡，可能要十几秒</span>
                <span v-else class="faint">首次连接需要管理员授权，可能要十几秒</span>
              </template>
              <template v-else>
              <span>{{ state?.endpoint || '未配置服务端' }}</span>
              <i class="hero-sep"></i>
              <span>运行时长 {{ state?.uptime || '—' }}</span>
              <i class="hero-sep"></i>
              <span>{{ state?.hostname || '—' }}</span>
              </template>
            </div>
          </div>
        </div>
        <div class="hero-actions">
          <button v-if="connecting" class="btn primary" disabled>
            <span class="spinner"></span>{{ disconnecting ? '断开中…' : '连接中…' }}
          </button>
          <template v-else>
            <button class="btn" @click="act('connect')">重连</button>
            <button v-if="state?.connected" class="btn danger" @click="act('disconnect')">断开连接</button>
            <button v-else id="btn-connect" class="btn primary" @click="act('connect')">连接</button>
          </template>
        </div>
      </div>
      <div v-if="!connecting && !state?.connected && state?.lastError" class="notice warn" style="margin-top:14px;position:relative">
        上次启动失败：{{ state.lastError }}
      </div>
      <div class="hero-ip">
        {{ state?.connected ? (state?.selfIP || '—') : '—' }}
        <small>本节点地址</small>
      </div>
    </section>

    <div v-if="hasAPIKey && alerts.length" class="card" style="margin-bottom:14px">
      <div class="spread" style="margin-bottom:6px">
        <h3 class="sec" style="margin:0">需要处理</h3>
        <span class="faint" style="font-size:10.5px">{{ alerts.length }} 项</span>
      </div>
      <div v-for="a in alerts.slice(0, 6)" :key="a.id" class="alert-row">
        <span class="alert-dot" :class="a.level"></span>
        <div class="alert-text">
          <div class="alert-title">{{ a.text }}</div>
          <div v-if="a.sub" class="alert-sub">{{ a.sub }}</div>
        </div>
        <button v-if="a.renewId" class="btn sm ghost" @click="renewById(a.renewId)">取消到期</button>
        <button v-else class="btn sm ghost" @click="emit('navigate', a.page)">{{ a.action || '查看' }}</button>
      </div>
      <button v-if="alerts.length > 6" class="link-btn" style="margin-top:8px"
              @click="emit('navigate', 'devices')">还有 {{ alerts.length - 6 }} 项</button>
    </div>

    <!-- 指标 -->
    <div :class="hasAPIKey ? 'grid4' : 'grid3'" style="margin-bottom:14px">
      <div class="card">
        <div class="metric-top">
          <div class="stat-label" style="margin:0">在线设备</div>
          <div class="metric-ico">
            <span :class="online ? 'dot ok' : 'dot off'" style="width:8px;height:8px"></span>
          </div>
        </div>
        <div class="stat-num">{{ online }}<span style="font-size:13px;color:var(--faint);font-weight:400"> / {{ total }}</span></div>
        <div class="stat-sub">tailnet 内对端节点</div>
      </div>

      <div class="card">
        <div class="metric-top">
          <div class="stat-label" style="margin:0">已接入网段</div>
          <div class="metric-ico">
            <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round">
              <path d="M2 4h12M2 8h12M2 12h7" />
            </svg>
          </div>
        </div>
        <div class="stat-num">{{ routes.length }}</div>
        <div class="stat-sub">{{ skipped.length ? skipped.length + ' 段与本机冲突已跳过' : '全部按最长前缀生效' }}</div>
      </div>

      <div class="card">
        <div class="metric-top">
          <div class="stat-label" style="margin:0">运行时长</div>
          <div class="metric-ico">
            <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round">
              <circle cx="8" cy="8" r="6" /><path d="M8 4.6V8l2.4 1.6" />
            </svg>
          </div>
        </div>
        <div class="stat-num" style="font-size:20px">{{ state?.uptime || '—' }}</div>
        <div class="stat-sub">{{ state?.connected ? '隧道保持中' : '未运行' }}</div>
      </div>

      <div v-if="hasAPIKey" class="card">
        <div class="metric-top">
          <div class="stat-label" style="margin:0">管理密钥</div>
          <button v-if="state?.configured" class="btn sm ghost" :disabled="renewing" @click="openRenew">
            <span v-if="renewing" class="spinner"></span>{{ renewing ? '续期中' : '续期' }}
          </button>
        </div>
        <div class="stat-num">
          {{ keyDays === null ? '—' : keyDays }}
          <span v-if="keyDays !== null" style="font-size:13px;color:var(--faint);font-weight:400"> 天</span>
        </div>
        <div class="stat-sub">
          {{ keyExpires || '未读取到有效期' }}
        </div>
      </div>
    </div>

    <div class="grid2" style="margin-bottom:14px">
      <div class="card">
        <h3 class="sec">本节点</h3>
        <div class="kv"><span>主机名</span><b>{{ state?.hostname || '—' }}</b></div>
        <div class="kv"><span>节点地址</span><b class="mono">{{ state?.selfIP || '—' }}</b></div>
        <div class="kv"><span>服务端</span><b class="mono" style="font-size:10.5px">{{ state?.endpoint || '—' }}</b></div>
        <div class="kv">
          <span>连接状态</span>
          <b :style="{ color: state?.connected ? 'var(--ok)' : 'var(--faint)' }">
            {{ connecting ? '连接中…' : state?.connected ? '已连接' : '未连接' }}
          </b>
        </div>
      </div>

      <div class="card">
        <h3 class="sec">已接入网段</h3>
        <div v-if="!routes.length && !skipped.length" class="faint" style="font-size:11.5px;line-height:1.9">
          还没有子网路由。在 Headscale 批准节点路由后，这里会自动列出。
        </div>
        <div v-if="routes.length" class="route-list" style="margin-bottom:10px">
          <span v-for="r in routes" :key="r" class="route-pill">{{ r }}</span>
        </div>
        <div v-if="skipped.length">
          <div class="faint" style="font-size:10.5px;margin-bottom:6px">与本机已有路由冲突，已跳过：</div>
          <div class="route-list">
            <span v-for="r in skipped" :key="r" class="route-pill off">{{ r }}</span>
          </div>
        </div>
      </div>
    </div>

    <div class="card" style="margin-bottom:14px">
      <div class="spread" style="margin-bottom:12px">
        <h3 class="sec" style="margin:0">快捷应用</h3>
        <button class="btn ghost sm" @click="$emit('notify', '在「应用管理」里增删应用')">管理</button>
      </div>
      <div v-if="apps.length" class="app-grid">
        <button v-for="(a, i) in apps" :key="a.id" class="app-chip" :disabled="!!opening" @click="openApp(a)">
          <span class="ico" :style="{ background: iconColor(a, i) }">{{ initial(a) }}</span>
          <span style="min-width:0">
            <b>{{ a.name }}</b>
            <small>{{ a.group || '未分组' }}</small>
          </span>
        </button>
      </div>
      <div v-else class="faint" style="font-size:11.5px">还没有应用书签，到「应用管理」添加内网地址。</div>
    </div>

    <div class="card">
      <h3 class="sec">最近事件</h3>
      <div v-if="!audit.length" class="faint" style="font-size:11.5px;line-height:1.9">
        暂无记录。连接隧道、生成 Key、保存 ACL 后会写入本地审计日志。
      </div>
      <div v-else class="tl">
        <div v-for="(e, i) in audit.slice(0, 7)" :key="i" class="tl-item">
          <span class="tl-dot"></span>
          <div style="min-width:0;flex:1">
            <div class="tl-body"><b>{{ e.action }}</b><span>{{ shortTime(e.time) }}</span></div>
            <div class="tl-detail">{{ e.detail }}</div>
          </div>
        </div>
      </div>
    </div>

    <div v-if="state?.loginURL" class="notice warn" style="margin-top:14px">
      <div class="spread" style="gap:10px">
        <div style="min-width:0">
          <b style="font-size:12px">首次需要完成授权</b>
          <div class="mono" style="margin-top:5px;word-break:break-all;opacity:.85">{{ state.loginURL }}</div>
        </div>
        <button class="btn sm" @click="copyLogin">复制登录链接</button>
      </div>
      <div style="font-size:11px;margin-top:8px">在浏览器里完成授权后节点自动上线，以后重开不用再登录。</div>
    </div>
    <template v-if="renewOpen">
      <div class="mask" @click="renewOpen = false"></div>
      <div class="drawer">
        <div class="spread" style="margin-bottom:18px">
          <b style="font-size:14px">续期管理密钥</b>
          <button class="btn ghost sm" @click="renewOpen = false">✕</button>
        </div>
        <div class="notice info" style="margin-bottom:16px">
          headscale 没有续期接口，这里是新建一把替换旧的。旧密钥立即吊销，新密钥写进系统钥匙串。
          正在用旧密钥的其他工具会失效。
        </div>
        <div class="field">
          <label class="field-label">新的有效期</label>
          <Pick v-model="renewDays" :options="renewOptions" />
        </div>
        <div v-if="selfKey" class="kv"><span>当前密钥前缀</span><b class="mono" style="font-size:10.5px">{{ selfKey.prefix }}</b></div>
        <div v-if="selfKey" class="kv"><span>当前到期</span><b>{{ selfKey.expires || '—' }}</b></div>
        <button class="btn primary" style="width:100%;height:38px;margin-top:16px" :disabled="renewing" @click="renewAPIKey">
          <span v-if="renewing" class="spinner"></span>{{ renewing ? '续期中…' : '确认续期' }}
        </button>
      </div>
    </template>
  </div>
</template>
