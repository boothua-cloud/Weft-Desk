<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type State, type AppBookmark, type AuditEvent } from '../api'
import { reloadTick } from '../bus'

const props = defineProps<{ state: State | null }>()
const emit = defineEmits(['refresh', 'notify'])
const apps = ref<AppBookmark[]>([])
const audit = ref<AuditEvent[]>([])
const busy = ref(false)
const opening = ref('')

// 连接/断开过程中：本地按钮状态 + 后端建卡状态，任一为真就显示加载态
const connecting = computed(() => busy.value || !!props.state?.connecting)
const disconnecting = ref(false)
const busyLabel = ref('')

const peers = computed(() => props.state?.peers ?? [])
const online = computed(() => peers.value.filter(p => p.online).length)
const total = computed(() => peers.value.length)
const routes = computed(() => props.state?.routes ?? [])
const skipped = computed(() => props.state?.skipped ?? [])
// 指标卡里的小柱状图，用在线比例凑出一点仪表盘的感觉
const bars = computed(() => {
  const n = 16
  const ratio = total.value ? online.value / total.value : 0
  return Array.from({ length: n }, (_, i) => i / n < ratio)
})

async function load() {
  try {
    const [a, e] = await Promise.all([api.apps(), api.audit()])
    apps.value = a
    audit.value = e
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
          : '等了 2 分钟还没连上。检查控制面地址、Auth Key，或看终端日志里的 [system] 输出')
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
  emit('notify', '登录链接已复制，请粘贴到系统浏览器打开')
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
                <span class="faint">首次连接需要管理员授权，可能要十几秒</span>
              </template>
              <template v-else>
              <span>{{ state?.endpoint || '未配置控制面' }}</span>
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

    <!-- 指标 -->
    <div class="grid4" style="margin-bottom:14px">
      <div class="card">
        <div class="metric-top">
          <div class="stat-label" style="margin:0">在线设备</div>
          <div class="metric-spark">
            <i v-for="(on, i) in bars" :key="i" :class="{ on }" :style="{ height: (6 + (i % 5) * 3) + 'px' }"></i>
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

      <div class="card">
        <div class="metric-top">
          <div class="stat-label" style="margin:0">控制面</div>
          <span class="tag green" style="margin:0">{{ state?.capabilities?.version || '—' }}</span>
        </div>
        <div class="stat-num" style="font-size:20px">Headscale</div>
        <div class="stat-sub">{{ state?.configured ? '管理凭证已配置' : '未配置管理 API Key' }}</div>
      </div>
    </div>

    <div class="grid2" style="margin-bottom:14px">
      <div class="card">
        <h3 class="sec">本节点</h3>
        <div class="kv"><span>主机名</span><b>{{ state?.hostname || '—' }}</b></div>
        <div class="kv"><span>节点地址</span><b class="mono">{{ state?.selfIP || '—' }}</b></div>
        <div class="kv"><span>控制面</span><b class="mono" style="font-size:10.5px">{{ state?.endpoint || '—' }}</b></div>
        <div class="kv"><span>状态</span><b>{{ state?.message || '—' }}</b></div>
      </div>

      <div class="card">
        <h3 class="sec">已接入网段</h3>
        <div v-if="!routes.length && !skipped.length" class="faint" style="font-size:11.5px;line-height:1.9">
          还没有子网路由。在 Headscale 里给节点批准路由后，这里会自动列出。
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
      <div style="font-size:11px;margin-top:8px">在系统浏览器打开并完成授权后，节点会自动上线；以后重开 App 不再需要登录。</div>
    </div>
  </div>
</template>
