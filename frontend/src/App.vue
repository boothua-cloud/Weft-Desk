<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { Events } from '@wailsio/runtime'
import { api, type State } from './api'
import { requestReload } from './bus'
import { darkMode, setTheme } from './theme'
import Overview from './views/Overview.vue'
import Manage from './views/Manage.vue'
import Routes from './views/Routes.vue'
import Acl from './views/Acl.vue'
import Apps from './views/Apps.vue'
import Forwards from './views/Forwards.vue'
import Diagnose from './views/Diagnose.vue'
import Audit from './views/Audit.vue'
import Settings from './views/Settings.vue'
import InfoDialog from './components/InfoDialog.vue'
import CommandPalette from './components/CommandPalette.vue'
import UpdateDialog from './components/UpdateDialog.vue'
import SetupWizard from './components/SetupWizard.vue'

const page = ref('overview')
// 侧栏折叠：看矩阵这类宽表格时把左边那一条收起来。记在本地，下次打开还是收着的
const SIDE_KEY = 'weft.side.collapsed'
function readSide() {
  try { return localStorage.getItem(SIDE_KEY) === '1' } catch { return false }
}
const sideCollapsed = ref(readSide())
function toggleSide() {
  closeMenus()
  sideCollapsed.value = !sideCollapsed.value
  try { localStorage.setItem(SIDE_KEY, sideCollapsed.value ? '1' : '0') } catch { /* 隐私模式写不了 */ }
}
function toggleTheme() {
  setTheme(darkMode.value ? 'light' : 'dark')
}

// 关于 / 帮助 弹窗，托盘菜单和左下角「更多」都能唤起
const info = ref<'' | 'about' | 'help'>('')

// 检查更新弹窗；updateHint 表示后台已经发现新版但用户还没点开看过
const updateOpen = ref(false)
const updateHint = ref(false)
function openUpdate() {
  menuOpen.value = false
  info.value = ''
  updateHint.value = false
  updateOpen.value = true
}

// 左下角「更多」菜单：把不常用的操作收进来，不占地方
const menuOpen = ref(false)
// 侧栏带着 backdrop-filter，遮罩留在里面只能盖住侧栏自己，
// 所以遮罩挂到最外层，点页面任何地方都能收起来
function closeMenus() { switchOpen.value = false; menuOpen.value = false }
function closeMenu() { menuOpen.value = false }
// 两个菜单只能开一个：切换服务端和「更多」都在左下角，
// 同时弹出来会叠在一起，还得挨个点掉
function toggleSwitch() {
  menuOpen.value = false
  switchOpen.value = !switchOpen.value
}
function toggleMore() {
  switchOpen.value = false
  menuOpen.value = !menuOpen.value
}
function pickPage(key: string) { closeMenus(); page.value = key }
function refreshFromMenu() {
  closeMenu()
  refreshAll()
}
// 出问题时最常用的动作：把版本、配置、自检、日志打成一个包，
// 生成过程不再发网络请求，断网也能出
async function exportBundle() {
  closeMenus()
  try {
    const path = await api.exportDiag()
    notify(path ? '诊断包已保存到 ' + path : '已取消')
  } catch (err: any) {
    notify('生成诊断包失败：' + (err?.message ?? err))
  }
}
// ⌘K 面板：页面、动作、设备、应用都在里面找。面板自己渲染，
// 但动作真正执行的地方在 App，状态和页面切换都在这里
const paletteOpen = ref(false)

// 首次启动向导：新机器上把地址和两把钥匙按顺序问一遍。
// 「跳过」也记一笔，跳过一次就不再自动冒出来；想再跑一回走 ⌘K 里的「初始化向导」
const SETUP_KEY = 'weft.setup.done'
const setupOpen = ref(false)
function setupDone() {
  setupOpen.value = false
  try { localStorage.setItem(SETUP_KEY, '1') } catch { /* 隐私模式写不了 */ }
  refresh()
}
// 已经在用的机器不弹：有管理 Key 或者注册 Key，都说明配过了。
// 只看 configured 的话，走浏览器授权的用户每次升级都会被再问一遍地址。
function maybeSetup() {
  try { if (localStorage.getItem(SETUP_KEY) === '1') return } catch { /* 读不到就按没配过处理 */ }
  const s = state.value
  if (!s || s.mode === 'demo') return
  if (s.configured || s.hasAuthKey) {
    try { localStorage.setItem(SETUP_KEY, '1') } catch { /* 同上 */ }
    return
  }
  setupOpen.value = true
}
async function paletteCommand(name: string) {
  paletteOpen.value = false
  if (name === 'refresh') { refreshAll(); return }
  if (name === 'theme') { toggleTheme(); return }
  if (name === 'help') { info.value = 'help'; return }
  if (name === 'setup') { setupOpen.value = true; return }
  if (name === 'update') { openUpdate(); return }
  if (name === 'bundle') { exportBundle(); return }
  if (name === 'connect' || name === 'disconnect') {
    try {
      state.value = name === 'connect' ? await api.connect() : await api.disconnect()
      notify(name === 'connect' ? '正在建立连接…' : '已断开')
    } catch (err: any) {
      notify('操作失败：' + (err?.message ?? err))
    }
  }
}
function onGlobalKey(e: KeyboardEvent) {
  if (!(e.metaKey || e.ctrlKey) || e.key.toLowerCase() !== 'k') return
  e.preventDefault()
  info.value = ''
  paletteOpen.value = !paletteOpen.value
}
function openPalette(key: string) {
  paletteOpen.value = false
  page.value = key
}

const state = ref<State | null>(null)
const toast = ref('')
let toastTimer: number | undefined

const icons: Record<string, string> = {
  overview: 'M2 2h5v5H2zM9 2h5v5H9zM2 9h5v5H2zM9 9h5v5H9z',
  devices: 'M2 3h12v8H2zM1 13h14',
  users: 'M8 5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5M3 14c0-3 2.2-5 5-5s5 2 5 5',
  keys: 'M5.5 10.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5M7.5 8.5 14 2M11 5l2 2M9 7l2 2',
  api: 'M6 3H5a2 2 0 0 0-2 2v2a2 2 0 0 1-2 2 2 2 0 0 1 2 2v2a2 2 0 0 0 2 2h1M10 3h1a2 2 0 0 1 2 2v2a2 2 0 0 0 2 2 2 2 0 0 0-2 2v2a2 2 0 0 1-2 2h-1',
  acl: 'M8 1.5 14 4v4c0 3.5-2.5 5.5-6 6.5-3.5-1-6-3-6-6.5V4zM5.5 8l1.7 1.7L11 6',
  routes: 'M2 12h3.5c2 0 2.5-4 4.5-4H14M11.5 6 14 8l-2.5 2M2 4h3',
  apps: 'M6.5 2.5h7v7M2.5 6.5h7v7h-7z',
  settings: 'M8 5.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.5 1.5M11.5 11.5 13 13M13 3l-1.5 1.5M4.5 11.5 3 13',
  forwards: 'M2 5h8M8 2l3 3-3 3M14 11H6M8 8l-3 3 3 3',
  diagnose: 'M1 8h3l2-5 3 10 2-5h4',
  audit: 'M3 2.5h10v13H3zM5.5 5.5h5M5.5 8.5h5M5.5 11.5h3',
}
// 「服务端管理」这一组页面全靠管理 API Key，没有就整组不显示。
// 判断以后端为准：空 Key 和节点 Auth Key 都不算（后者管理接口一律 401）。
const managePages = ['devices', 'routes', 'users', 'keys', 'api', 'acl']
const hasAPIKey = computed(() => !!state.value?.hasApiKey)

const nav = computed(() => {
  const groups: { label: string; items: { key: string; label: string }[] }[] = [
    { label: '总览', items: [{ key: 'overview', label: '隧道总览' }] },
  ]
  // 没有管理 API Key 时，这几个页面打开也只会是一串 401，
  // 不如整组收起来，省得让人以为功能坏了
  if (hasAPIKey.value) {
    groups.push({
      label: '服务端管理',
      items: [
        { key: 'devices', label: '设备管理' },
        { key: 'routes', label: '子网路由' },
        { key: 'users', label: '人员管理' },
        { key: 'keys', label: '授权 Key' },
        { key: 'api', label: 'API Key' },
        { key: 'acl', label: 'ACL 策略' },
      ],
    })
  }
  groups.push({
    label: '工作台',
    items: [
      { key: 'apps', label: '应用管理' },
      { key: 'forwards', label: '端口转发' },
    ],
  })
  return groups
})
// 诊断、记录、设置这三页不是天天点的，归到「更多」里。侧栏只留日常要用的，
// 以后再加功能也不至于把导航拉成一长条
const morePages = [
  { key: 'diagnose', label: '连接诊断' },
  { key: 'audit', label: '操作记录' },
  { key: 'settings', label: '连接设置' },
]
// 当前页在「更多」里的时候，那枚按钮自己亮起来，不然侧栏看不出人在哪
const moreActive = computed(() => morePages.some((p) => p.key === page.value))
const isMac = /Mac|iPhone|iPad/.test(navigator.userAgent)
const meta: Record<string, [string, string]> = {
  overview: ['隧道', '总览'], devices: ['服务端管理', '设备管理'],
  routes: ['服务端管理', '子网路由'],
  users: ['服务端管理', '人员管理'], keys: ['服务端管理', '密钥管理'],
  api: ['服务端管理', '密钥管理'], acl: ['服务端管理', 'ACL 策略'],
  apps: ['工作台', '应用管理'], forwards: ['工作台', '端口转发'],
  diagnose: ['工作台', '连接诊断'], audit: ['工作台', '操作记录'],
  settings: ['设置', '连接设置'],
}
// 这几个页面是列表型：头部与筛选条固定，只有表格自己滚动
const fixedLayout = computed(() => ['devices', 'users', 'keys', 'api', 'apps', 'acl', 'diagnose', 'audit'].includes(page.value))
const crumb = computed(() => meta[page.value]?.[0] ?? '总览')
const title = computed(() => meta[page.value]?.[1] ?? '总览')
const manageTab = computed(() => ['devices', 'users', 'keys', 'api'].includes(page.value) ? page.value : 'devices')
// 密钥被删、换到没配 Key 的服务端、或者填的是 Auth Key：当前页可能正好停在
// 一个已经不该出现的地方，退回总览。
watch(hasAPIKey, (ok) => {
  if (!ok && managePages.includes(page.value)) page.value = 'overview'
})
// 多服务端：顶栏那个胶囊点开就是切换器
type Profile = { id: string; name: string; baseURL: string }
const profiles = ref<Profile[]>([])
const activeID = ref('')
const switchOpen = ref(false)
const switching = ref('')

async function loadProfiles() {
  try {
    const raw = await api.profilesView()
    const v = JSON.parse(raw) as { items: Profile[] | null; active: string }
    profiles.value = v.items ?? []
    activeID.value = v.active
  } catch { /* 未就绪时忽略 */ }
}
async function pickProfile(p: Profile) {
  if (p.id === activeID.value || switching.value) { switchOpen.value = false; return }
  switching.value = p.id
  switchOpen.value = false
  try {
    await api.switchProfile(p.id)
    await loadProfiles()
    await refresh()
    // 换了服务端，列表缓存全是上家的，必须让当前页面重拉
    requestReload()
    notify('已切换到 ' + p.name)
  } catch (err: any) {
    notify('切换失败：' + (err?.message ?? err))
  } finally {
    switching.value = ''
  }
}

// 左下角那张卡优先显示服务端名字（用户起的），没有名字才退回地址
const activeProfile = computed(() => profiles.value.find((p) => p.id === activeID.value) || null)
function hostOf(url: string) {
  return url.replace(/^https?:\/\//, '').replace(/\/+$/, '')
}
const serverHost = computed(() => hostOf(state.value?.endpoint || ''))
const serverName = computed(() => activeProfile.value?.name || serverHost.value || '未配置服务端')
const statusText = computed(() => {
  const s = state.value
  if (!s) return '正在读取…'
  if (s.connecting) return '正在连接…'
  return s.connected ? '隧道已连接' : '未连接'
})

function notify(msg: string) {
  toast.value = msg
  window.clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => { toast.value = '' }, 3400)
}
async function refresh() {
  try { state.value = await api.state() }
  catch (e: any) { notify('读取状态失败：' + (e?.message ?? e)) }
}
const spinning = ref(false)
// 刷完之后打个勾。状态本来就每 3 秒轮询一次，点一下看不出区别，
// 只能靠这个确认「点到了、也回来了」。
const refreshed = ref(false)
let refreshFlash: number | undefined
// 顶栏按钮：既刷新隧道状态，也让当前页面重新拉自己的列表
async function refreshAll() {
  if (spinning.value) return
  spinning.value = true
  refreshed.value = false
  requestReload()
  const started = Date.now()
  await refresh()
  // 本地服务端二三十毫秒就回来了，转瞬即逝，看着跟没点一样。
  // 给它走完半秒，页面也有时间把新数据画上去。
  const rest = 520 - (Date.now() - started)
  if (rest > 0) await new Promise((r) => window.setTimeout(r, rest))
  spinning.value = false
  refreshed.value = true
  window.clearTimeout(refreshFlash)
  refreshFlash = window.setTimeout(() => { refreshed.value = false }, 1200)
}
// tsnet 节点异步上线（首次授权可能要几分钟），定时刷新状态与登录链接。
let pollTimer: number | undefined
let offNavigate: (() => void) | undefined
let offUpdate: (() => void) | undefined
let offRefresh: (() => void) | undefined
// 启动遮罩写在 index.html 里，这里等界面首帧真正绘制完再淡出，
// 否则会出现「遮罩撤了、页面还没画」的一瞬间空白。
function dismissBoot() {
  const el = document.getElementById('boot')
  if (!el) return
  el.classList.add('hide')
  window.setTimeout(() => el.remove(), 340)
}
onMounted(async () => {
  window.addEventListener('keydown', onGlobalKey)
  // 托盘菜单「设置…」直接跳到连接设置页。没有事件通道也不能挡住启动流程。
  try {
    offNavigate = Events.On('weft:navigate', (ev) => {
      const target = String(ev.data ?? '')
      if (target === 'about' || target === 'help') info.value = target
      else if (target === 'update') openUpdate()
      else if (target === 'profile-new') {
        page.value = 'settings'
        window.setTimeout(() => window.dispatchEvent(new CustomEvent('weft:open-profile')), 900)
      } else if (['overview', 'apps', 'forwards', 'acl', 'devices', 'routes', 'users', 'keys', 'api', 'settings'].includes(target)) {
        if (managePages.includes(target) && !hasAPIKey.value) {
          const why = state.value?.apiKeyIssue
          notify(why
            ? `管理 API Key 不可用：${why}`
            : '这台服务端没配管理 API Key，设备、人员、密钥和 ACL 暂时不显示')
          page.value = 'overview'
        } else {
          page.value = target
        }
      }
    })
  } catch { /* 忽略 */ }

  // 后台查到的更新：给个轻提示，并在「更多」上加一个小红点，
  // 不主动弹窗打断正在做的事。
  try {
    offUpdate = Events.On('weft:update-available', (ev) => {
      const v = (ev?.data as any)?.latestVersion
      updateHint.value = true
      notify(v ? `有新版本 v${v}，去「更多 → 检查更新」` : '有新版本，去「更多 → 检查更新」')
    })
  } catch { /* 忽略 */ }

  // 帮助里写的 ⌘R 走这里：快捷键在后端注册，前端只负责接
  try {
    offRefresh = Events.On('weft:refresh', () => { refreshAll() })
  } catch { /* 忽略 */ }

  // 窗口此刻还是隐藏的：先把窗口叫出来，#boot 已在 DOM 里，第一帧就是启动动画，
  // 用户看不到 WebView 加载期间那块没有任何内容的底色。
  const revealedAt = Date.now()
  try {
    api.revealWindow().catch(() => { /* 兜底：启动慢时后端会自己显示窗口 */ })
  } catch { /* 同上 */ }

  // 首次状态到齐再撤遮罩，避免先闪一下「未连接 / —」再跳成真实数据；
  // 万一状态读取卡住，最多等 2.5 秒也必须把界面露出来。
  loadProfiles()
  window.addEventListener('weft:profiles-changed', loadProfiles)
  const first = refresh()
  pollTimer = window.setInterval(refresh, 3000)
  await Promise.race([first, new Promise((r) => window.setTimeout(r, 2500))])
  // 状态到手之后才知道该不该问：没配过的新机器才弹向导
  maybeSetup()

  // 启动动画至少露脸这么久，否则一闪而过，看着还是「没加载过程」。
  const rest = 900 - (Date.now() - revealedAt)
  if (rest > 0) await new Promise((r) => window.setTimeout(r, rest))

  await nextTick()
  // 双 rAF 保证首帧已经画出来；窗口不可见时 rAF 可能不回调，用定时器兜底。
  let dismissed = false
  const fallback = () => {
    if (dismissed) return
    dismissed = true
    dismissBoot()
  }
  requestAnimationFrame(() => requestAnimationFrame(fallback))
  window.setTimeout(fallback, 160)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onGlobalKey)
  try { offUpdate?.() } catch { /* 忽略 */ }
  try { offRefresh?.() } catch { /* 忽略 */ }
  window.clearInterval(pollTimer)
  window.clearTimeout(refreshFlash)
  offNavigate?.()
})
</script>

<template>
  <div class="app-shell">
    <aside class="side" :class="{ collapsed: sideCollapsed }">
      <div class="brand">
        <img class="logo" src="/appicon.png" alt="云栈" draggable="false" />
        <div><b>云栈</b><small>Headscale 桌面端 v{{ state?.appVersion || '' }}</small></div>
      </div>
      <!-- ⌘K 藏在「更多」里没人会去找，摆到最上面当入口。
           不做成真 input：点一下直接开面板，省得焦点在两边跳 -->
      <button class="side-search" :class="{ on: paletteOpen }" :title="sideCollapsed ? '搜索' : undefined"
              @click="paletteOpen = true">
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"
             stroke-linecap="round"><circle cx="7" cy="7" r="4.3" /><path d="M10.3 10.3 14 14" /></svg>
        <span class="ss-text">搜索</span>
        <kbd class="ss-kbd">{{ isMac ? '⌘K' : 'Ctrl K' }}</kbd>
      </button>
      <div class="side-nav">
        <template v-for="g in nav" :key="g.label">
          <span class="nav-label">{{ g.label }}</span>
          <button v-for="it in g.items" :key="it.key" class="nav-item"
                  :class="{ active: page === it.key }" :title="sideCollapsed ? it.label : undefined"
                  @click="pickPage(it.key)">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.2"
                 stroke-linecap="round" stroke-linejoin="round"><path :d="icons[it.key]" /></svg>
            <span class="nav-text">{{ it.label }}</span>
          </button>
        </template>
      </div>
      <div class="side-foot">
        <!-- 服务端与连接状态。原来挂在右上角的那枚胶囊搬到这里，
             点开是切换器；菜单朝上展开，才不会顶出窗口 -->
        <div class="menu-wrap up">
          <button class="profile-btn" :class="{ active: switchOpen }"
                  :title="`${serverName} · ${statusText}（点击切换服务端）`"
                  @click.stop="loadProfiles(); toggleSwitch()">
            <span class="dot" :class="state?.connecting ? 'warn pulse' : (state?.connected ? 'ok' : 'off')"></span>
            <span class="profile-text">
              <b>{{ serverName }}</b>
              <small>{{ statusText }}</small>
            </span>
            <svg class="chev" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"
                 stroke-linecap="round" stroke-linejoin="round"><path d="M4 10.5 8 6.5l4 4" /></svg>
          </button>
          <template v-if="switchOpen">
            <div class="menu-panel">
              <div class="menu-label">切换服务端</div>
              <button v-for="p in profiles" :key="p.id" class="menu-item profile-item"
                      :class="{ current: p.id === activeID }" @click="pickProfile(p)">
                <span class="profile-item-text">
                  <b>{{ p.name }}</b>
                  <small>{{ p.baseURL || '未填地址' }}</small>
                </span>
                <span v-if="switching === p.id" class="spinner"></span>
              </button>
              <div class="menu-sep"></div>
              <button class="menu-item" @click="switchOpen = false; page = 'settings'">
                <span>管理服务端…</span>
              </button>
            </div>
          </template>
        </div>

        <!-- 一排工具。侧栏收起时只留「更多」，刷新和主题在它的菜单里还有一份 -->
        <div class="side-tools">
          <button class="icon-btn wide-only" :title="darkMode ? '切换到亮色' : '切换到暗色'" @click="toggleTheme">
            <svg v-if="darkMode" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round">
              <circle cx="8" cy="8" r="3.2" />
              <path d="M8 1.2v1.6M8 13.2v1.6M1.2 8h1.6M13.2 8h1.6M3.2 3.2l1.1 1.1M11.7 11.7l1.1 1.1M12.8 3.2l-1.1 1.1M4.3 11.7l-1.1 1.1" />
            </svg>
            <svg v-else viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round">
              <path d="M13.5 9.6A5.9 5.9 0 1 1 6.4 2.5a4.7 4.7 0 0 0 7.1 7.1z" />
            </svg>
          </button>
          <button class="icon-btn wide-only" :class="{ spinning, done: refreshed }"
                  :title="refreshed ? '已刷新' : '刷新'" @click="refreshAll">
            <svg v-if="refreshed" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.9"
                 stroke-linecap="round" stroke-linejoin="round"><path d="M3.2 8.4 6.5 11.7 12.8 4.6" /></svg>
            <svg v-else viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4"
                 stroke-linecap="round" stroke-linejoin="round">
              <path d="M13.4 9.2A5.6 5.6 0 1 1 11.7 4.3" /><path d="M13.5 2.2v3.5h-3.5" />
            </svg>
          </button>
          <button class="icon-btn" :class="{ active: menuOpen || moreActive }" title="更多" @click.stop="toggleMore">
            <svg viewBox="0 0 16 16" fill="currentColor" width="15" height="15">
              <circle cx="3" cy="8" r="1.35" /><circle cx="8" cy="8" r="1.35" /><circle cx="13" cy="8" r="1.35" />
            </svg>
          </button>
          <template v-if="menuOpen">
            <div class="menu-panel">
              <button v-for="p in morePages" :key="p.key" class="menu-item"
                      :class="{ on: page === p.key }" @click="pickPage(p.key)">
                <span>{{ p.label }}</span>
              </button>
              <div class="menu-sep"></div>
              <button class="menu-item" @click="refreshFromMenu">
                <span>刷新数据</span><small>⌘R</small>
              </button>
              <button class="menu-item" @click="exportBundle">
                <span>生成诊断包</span>
              </button>
              <div class="menu-sep"></div>
              <button class="menu-item" @click="toggleTheme">
                <span>{{ darkMode ? '切换到亮色' : '切换到暗色' }}</span>
              </button>
              <button class="menu-item" @click="menuOpen = false; info = 'help'">
                <span>帮助手册</span>
              </button>
              <button class="menu-item" @click="openUpdate">
                <span>检查更新<i v-if="updateHint" class="menu-dot"></i></span>
                <small>v{{ state?.appVersion || '' }}</small>
              </button>
              <button class="menu-item" @click="menuOpen = false; info = 'about'">
                <span>关于云栈</span><small>v{{ state?.appVersion || '' }}</small>
              </button>
            </div>
          </template>
        </div>
      </div>
      <!-- 从面板右边缘长出来的把手：跟侧栏同材质，不是浮在面上的按钮 -->
      <button class="side-toggle" :title="sideCollapsed ? '展开侧栏' : '收起侧栏'" @click="toggleSide">
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"
             stroke-linecap="round" stroke-linejoin="round"><path d="M9.5 3.5 5 8l4.5 4.5" /></svg>
      </button>
    </aside>

    <main class="main">
      <header class="page-head">
        <div class="page-id">
          <h1 class="page-title">{{ title }}</h1>
          <span class="page-kicker">{{ crumb }}</span>
        </div>
      </header>

      <div class="reload-line" :class="{ on: spinning }"><i></i></div>

      <div class="content" :class="{ 'content-fixed': fixedLayout, refreshing: spinning }">
        <Overview v-if="page === 'overview'" :state="state" @refresh="refresh" @notify="notify"
                  @navigate="page = $event" />
        <Manage v-else-if="['devices','users','keys','api'].includes(page)" :tab="manageTab" :state="state"
                @notify="notify" @navigate="page = $event" />
        <Routes v-else-if="page === 'routes'" :state="state" @notify="notify" />
        <Acl v-else-if="page === 'acl'" :state="state" @notify="notify" />
        <Apps v-else-if="page === 'apps'" :state="state" @notify="notify" />
        <Forwards v-else-if="page === 'forwards'" :state="state" @notify="notify" />
        <Diagnose v-else-if="page === 'diagnose'" :state="state" @notify="notify" @navigate="page = $event" />
        <Audit v-else-if="page === 'audit'" @notify="notify" />
        <Settings v-else :state="state" @refresh="refresh" @notify="notify" />
      </div>
    </main>

    <div v-if="switchOpen || menuOpen" class="menu-mask" @click="closeMenus"></div>

    <InfoDialog v-if="info" :kind="info" :state="state" @close="info = ''"
                @check-update="openUpdate" />
    <UpdateDialog v-if="updateOpen" :state="state" @close="updateOpen = false" @notify="notify" />
    <CommandPalette v-if="paletteOpen" :state="state" @close="paletteOpen = false"
                    @navigate="openPalette" @command="paletteCommand" @notify="notify" />
    <SetupWizard v-if="setupOpen" :state="state" @close="setupDone" @done="setupDone" @notify="notify" />
    <div v-if="toast" class="toast">{{ toast }}</div>
  </div>
</template>
