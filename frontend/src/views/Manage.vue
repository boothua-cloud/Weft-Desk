<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type State, type Device, type User, type AuthKey, type APIKey } from '../api'
import Pick from '../components/Pick.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { useConfirm } from '../confirm'
import { reloadTick } from '../bus'

const props = defineProps<{ tab: string; state: State | null }>()
const emit = defineEmits(['notify', 'navigate'])
const devices = ref<Device[]>([])
const users = ref<User[]>([])
const keys = ref<AuthKey[]>([])
const apiKeys = ref<APIKey[]>([])
const keyword = ref('')
// 条件筛选：设备页用状态/成员/标签，Key 页用状态/成员
const statusFilter = ref('')
const userFilter = ref('')
const tagFilter = ref('')
const busy = ref(false)
const loading = ref(false)
// 吊销 / 删除都不可逆，统一先弹确认
const { confirmBox, askConfirm, closeConfirm } = useConfirm()

// 抽屉：新建 Key / 设备详情 / 注册设备 / 批准认证 / 新建 API Key
type Drawer = '' | 'key' | 'device' | 'register' | 'auth' | 'apikey' | 'debug' | 'user' | 'useredit'
const drawer = ref<Drawer>('')
const generated = ref('')
const newUser = ref('')
// headscale 的 user 有 name / displayName / email / pictureUrl 四个字段
const userForm = ref({ name: '', displayName: '', email: '', pictureUrl: '' })
const form = ref({ user: '', reusable: true, ephemeral: false, days: 90, tags: '' })

const sel = ref<Device | null>(null)
const edit = ref({ name: '', tags: '', routes: '', expiry: '' })
const reg = ref({ user: '', key: '' })
const auth = ref({ user: '', authID: '' })
const debugForm = ref({ user: '', key: '', name: '', routes: '' })
const apiKeyForm = ref({ days: 90 })

const userOptions = computed(() => users.value.map((u) => ({ value: u.name, label: u.rawName || u.name })))
const statusOptions = computed(() => {
  const base = [{ value: '', label: '全部状态' }]
  const items = props.tab === 'devices'
    ? ['在线', '离线', '已过期']
    : ['有效', '已使用', '已过期']
  return base.concat(items.map(v => ({ value: v, label: v })))
})
const shownApiKeys = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return apiKeys.value.filter(k => {
    if (kw && !(k.prefix + k.status).toLowerCase().includes(kw)) return false
    if (statusFilter.value && k.status !== statusFilter.value) return false
    return true
  })
})
const tagOptions = computed(() => [{ value: '', label: '全部标签' }].concat(allTags.value.map(t => ({ value: t, label: t }))))
const memberOptions = computed(() => [{ value: '', label: '全部成员' }].concat(userOptions.value))
const dayOptions = [1, 7, 30, 90, 180, 365].map((d) => ({ value: d, label: d + ' 天' }))

const canCreateUser = computed(() => props.state?.capabilities?.canCreateUser !== false)
// headscale 只给节点元数据，直连还是中继得问本机 tsnet 的对端信息
function connKind(d: Device) {
  const peers = props.state?.peers ?? []
  const hit = peers.find(p => p.ip === d.ip || (d.ips ?? []).includes(p.ip))
  if (!hit) return ''
  if (hit.curAddr) return '直连'
  return hit.relay ? '中继' : ''
}

const deviceStat = computed(() => {
  const m = new Map<string, { total: number; online: number }>()
  devices.value.forEach(d => {
    const k = d.user || ''
    const cur = m.get(k) ?? { total: 0, online: 0 }
    cur.total++
    if (d.online) cur.online++
    m.set(k, cur)
  })
  return m
})
const deviceSummary = computed(() => {
  const online = devices.value.filter(d => d.status === '在线').length
  const expired = devices.value.filter(d => d.status === '已过期').length
  return { online, expired, offline: devices.value.length - online - expired }
})
function statOf(u: User) {
  return deviceStat.value.get(u.rawName || u.name) ?? { total: 0, online: 0 }
}
const allTags = computed(() => {
  const set = new Set<string>()
  devices.value.forEach(d => splitList(d.tags).forEach(t => set.add(t)))
  return [...set].sort()
})
const shownDevices = computed(() => devices.value.filter(d => {
  const kw = keyword.value.trim().toLowerCase()
  if (kw && !(d.name + d.user + d.ip + d.tags + d.hostname).toLowerCase().includes(kw)) return false
  if (statusFilter.value && d.status !== statusFilter.value) return false
  if (userFilter.value && d.user !== userFilter.value) return false
  if (tagFilter.value && !splitList(d.tags).includes(tagFilter.value)) return false
  return true
}))
const shownKeys = computed(() => keys.value.filter(k => {
  const kw = keyword.value.trim().toLowerCase()
  if (kw && !(k.key + k.user).toLowerCase().includes(kw)) return false
  if (userFilter.value && k.user !== userFilter.value) return false
  if (statusFilter.value && k.status !== statusFilter.value) return false
  return true
}))

async function load() {
  loading.value = true
  const t0 = Date.now()
  try {
    if (props.tab === 'devices') devices.value = (await api.devices()).items
    else if (props.tab === 'users') {
      users.value = (await api.users()).items
      // 列表里要显示每个成员名下的设备数，顺带取一份
      if (!devices.value.length) {
        try { devices.value = (await api.devices()).items } catch { /* 统计不到就不显示 */ }
      }
    }
    else if (props.tab === 'keys') {
      keys.value = (await api.authKeys()).items
      if (!users.value.length) users.value = (await api.users()).items
    } else apiKeys.value = (await api.apiKeys()).items
  } catch (err: any) {
    emit('notify', '加载失败：' + (err?.message ?? err))
  } finally {
    // 服务端响应常在 100ms 内，遮罩一闪而过看着像没刷新。
    // 至少露 320ms，用户才有明确的操作反馈。
    const rest = 320 - (Date.now() - t0)
    if (rest > 0) await new Promise((r) => setTimeout(r, rest))
    loading.value = false
  }
}
watch(() => props.tab, () => {
  keyword.value = ''
  statusFilter.value = ''
  tagFilter.value = ''
  // 从成员页点设备数跳过来时，保留那次筛选；其余情况清空
  userFilter.value = pendingUser.value
  pendingUser.value = ''
  load()
}, { immediate: true })

const pendingUser = ref('')
watch(reloadTick, load)
// 首次挂载时 state 可能还是 null，那时 api 还没配置好，拉回来的是空列表。
// 等 state 首次到达时补一次；之后只有 endpoint 变了（切了服务端）才重拉。
let readyOnce = false
let lastEndpoint = ''
watch(() => props.state, (v) => {
  if (!v) return
  if (!readyOnce) { readyOnce = true; lastEndpoint = v.endpoint; load(); return }
  if (v.endpoint !== lastEndpoint) { lastEndpoint = v.endpoint; load() }
}, { immediate: true })

// 成员列表在多个抽屉里都要用，进设备页时顺手拉一份
watch(() => props.tab, async (t) => {
  if (t === 'devices' && !users.value.length) {
    try { users.value = (await api.users()).items } catch { /* 忽略 */ }
  }
}, { immediate: true })

function close() {
  drawer.value = ''
  generated.value = ''
  sel.value = null
}

// ---------- 设备 ----------

function openDevice(d: Device) {
  sel.value = d
  edit.value = {
    name: d.name,
    tags: d.tags,
    routes: d.advertisedRoutes || d.approvedRoutes || '',
    expiry: d.expiry,
  }
  checkedRoutes.value = new Set(splitList(d.approvedRoutes || ''))
  drawer.value = 'device'
}

async function saveRename() {
  if (!sel.value) return
  const name = edit.value.name.trim()
  if (!name || name === sel.value.name) return
  await run('重命名', async () => {
    await api.renameDevice(sel.value!.id, name)
    await refreshSel()
  })
}

async function saveTags() {
  if (!sel.value) return
  const tags = edit.value.tags.split(/[,\s]+/).map(s => s.trim()).filter(Boolean)
  await run('保存标签', async () => {
    await api.setDeviceTags(sel.value!.id, tags.join(','))
    await refreshSel()
  })
}

// 路由：勾选的才提交，未勾选的会被服务端撤销
const routeItems = computed(() => splitList(edit.value.routes))
const approvedSet = computed(() => new Set(splitList(sel.value?.approvedRoutes || '')))
const checkedRoutes = ref<Set<string>>(new Set())
function toggleRoute(r: string) {
  const next = new Set(checkedRoutes.value)
  if (next.has(r)) next.delete(r)
  else next.add(r)
  checkedRoutes.value = next
}
async function saveRoutes() {
  if (!sel.value) return
  const routes = [...checkedRoutes.value]
  await run('保存路由', async () => {
    await api.approveRoutes(sel.value!.id, routes.join(','))
    await refreshSel()
  })
}

async function expireDev() {
  if (!sel.value) return
  await run('设置到期', async () => {
    await api.expireDevice(sel.value!.id)
    await refreshSel()
  })
}
async function cancelExpiry() {
  if (!sel.value) return
  await run('取消到期', async () => {
    await api.disableDeviceExpiry(sel.value!.id)
    await refreshSel()
  })
}
function removeDev() {
  if (!sel.value) return
  const d = sel.value
  askConfirm({
    title: '删除这台设备？',
    desc: `${d.givenName || d.name || d.id}\n删除后该节点需要重新注册才能接入网络，已批准的路由也会失效。`,
    ok: '删除',
    action: async () => {
      await run('删除设备', async () => {
        await api.deleteDevice(d.id)
        close()
        await load()
      })
    },
  })
}

async function refreshSel() {
  if (!sel.value) return
  const id = sel.value.id
  devices.value = (await api.devices()).items
  const fresh = devices.value.find(d => d.id === id)
  if (fresh) {
    sel.value = fresh
    checkedRoutes.value = new Set(splitList(fresh.approvedRoutes || ''))
    edit.value.routes = fresh.advertisedRoutes || fresh.approvedRoutes || ''
    edit.value.expiry = fresh.expiry
  }
}

// ---------- 注册 / 认证 / 补齐 ----------

function openRegister() {
  reg.value = { user: users.value[0]?.name ?? '', key: '' }
  drawer.value = 'register'
}
async function doRegister() {
  if (!reg.value.user) { emit('notify', '请选择归属成员'); return }
  if (!reg.value.key.trim()) { emit('notify', '请填预授权 Key'); return }
  await run('注册设备', async () => {
    await api.registerNode(reg.value.user, reg.value.key.trim())
    close()
    await load()
  })
}

async function backfill() {
  await run('补齐 IP', async () => {
    const n = await api.backfillIPs(true)
    await load()
    emit('notify', n > 0 ? `已为 ${n} 台节点补上地址` : '所有节点都有 IP，不需要补齐')
  })
}

function openAuth() {
  auth.value = { user: users.value[0]?.name ?? '', authID: '' }
  drawer.value = 'auth'
}
async function approveAuth() {
  if (!auth.value.authID.trim()) { emit('notify', '请填 Auth ID'); return }
  await run('批准认证', async () => {
    await api.approveAuth(auth.value.authID.trim())
    close()
    await load()
  })
}
async function rejectAuth() {
  if (!auth.value.authID.trim()) { emit('notify', '请填 Auth ID'); return }
  await run('拒绝认证', async () => {
    await api.rejectAuth(auth.value.authID.trim())
    close()
  })
}
async function registerAuth() {
  if (!auth.value.user || !auth.value.authID.trim()) { emit('notify', '请填成员与 Auth ID'); return }
  await run('注册认证', async () => {
    await api.registerAuth(auth.value.user, auth.value.authID.trim())
    close()
    await load()
  })
}

function openDebug() {
  debugForm.value = { user: users.value[0]?.name ?? '', key: '', name: '', routes: '' }
  drawer.value = 'debug'
}
async function doDebug() {
  if (!debugForm.value.user) { emit('notify', '请选择归属成员'); return }
  await run('调试建节点', async () => {
    await api.debugCreateNode(
      debugForm.value.user, debugForm.value.key.trim(),
      debugForm.value.name.trim(), debugForm.value.routes.trim())
    close()
    await load()
  })
}

// ---------- 授权 Key ----------

function openKeyForm() {
  generated.value = ''
  form.value = { user: users.value[0]?.name ?? '', reusable: true, ephemeral: false, days: 90, tags: '' }
  drawer.value = 'key'
}
async function createKey() {
  if (!form.value.user) { emit('notify', '请选择归属用户'); return }
  busy.value = true
  try {
    generated.value = await api.createAuthKey(form.value.user, form.value.reusable, form.value.ephemeral, form.value.days, form.value.tags)
    await load()
    emit('notify', 'Key 已生成，仅显示一次')
  } catch (err: any) { emit('notify', '生成失败：' + (err?.message ?? err)) }
  finally { busy.value = false }
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

function expireKey(k: AuthKey) {
  askConfirm({
    title: '吊销这把授权 Key？',
    desc: `${k.key}\n吊销后立即失效，且无法恢复。已经用它注册过的节点不受影响。`,
    ok: '吊销',
    action: async () => {
      try {
        await api.expireAuthKey(k.id)
        emit('notify', 'Key 已吊销')
        await sleep(600)
        await load()
      } catch (err: any) { emit('notify', '吊销失败：' + (err?.message ?? err)) }
    },
  })
}
function deleteKey(k: AuthKey) {
  askConfirm({
    title: '删除这把授权 Key？',
    desc: `${k.key}\n删除后记录彻底消失，无法恢复。已经用它注册过的节点不受影响。`,
    ok: '删除',
    action: async () => {
      try {
        await api.deleteAuthKey(k.id)
        emit('notify', 'Key 已删除')
        await sleep(400)
        await load()
      } catch (err: any) { emit('notify', '删除失败：' + (err?.message ?? err)) }
    },
  })
}

// ---------- API Key ----------

function openApiKeyForm() {
  generated.value = ''
  apiKeyForm.value = { days: 90 }
  drawer.value = 'apikey'
}
async function createApiKey() {
  busy.value = true
  try {
    generated.value = await api.createAPIKey(apiKeyForm.value.days)
    await load()
    emit('notify', 'API Key 已生成，仅显示一次')
  } catch (err: any) { emit('notify', '生成失败：' + (err?.message ?? err)) }
  finally { busy.value = false }
}
function expireApiKey(k: APIKey) {
  askConfirm({
    title: '吊销这个 API Key？',
    desc: `${k.prefix}\n吊销后立即失效，正用它调用管理接口的客户端会立刻失去权限，且无法恢复。`,
    ok: '吊销',
    action: async () => {
      try {
        await api.expireAPIKey(k.prefix, k.id)
        emit('notify', '已吊销')
        await sleep(600)
        await load()
      } catch (err: any) { emit('notify', '吊销失败：' + (err?.message ?? err)) }
    },
  })
}
function deleteApiKey(k: APIKey) {
  askConfirm({
    title: '删除这个 API Key？',
    desc: `${k.prefix}\n删除后记录彻底消失，无法恢复。如果还想留痕，建议用「吊销」。`,
    ok: '删除',
    action: async () => {
      try {
        await api.deleteAPIKey(k.prefix, '')
        emit('notify', '已删除')
        await sleep(400)
        await load()
      } catch (err: any) { emit('notify', '删除失败：' + (err?.message ?? err)) }
    },
  })
}

// ---------- 成员 ----------

function openUserForm() {
  userForm.value = { name: '', displayName: '', email: '', pictureUrl: '' }
  drawer.value = 'user'
}
async function createUser() {
  const name = userForm.value.name.trim()
  if (!name) { emit('notify', '成员名称不能为空'); return }
  if (!/^[a-zA-Z0-9._-]+$/.test(name)) {
    emit('notify', '名称只能用字母、数字、点、下划线、短横线')
    return
  }
  busy.value = true
  try {
    await api.createUser(name, userForm.value.displayName.trim(),
                         userForm.value.email.trim(), userForm.value.pictureUrl.trim())
    drawer.value = ''
    newUser.value = ''
    await load()
    emit('notify', '已创建成员 ' + name)
  } catch (err: any) {
    emit('notify', '创建失败：' + (err?.message ?? err))
  } finally { busy.value = false }
}
// headscale v1 只有 create / rename / delete 三个用户接口，
// 邮箱和显示名是创建时写进去、之后由 OIDC 提供方维护的，没有修改接口。
const userEdit = ref<{ id: string; rawName: string; displayName: string; email: string; pictureUrl: string } | null>(null)
const editUserNewName = ref('')
const editingUser = ref('')
const editUserName = ref('')
// 从成员行跳到设备页，只显示这个成员的设备
function jumpToUserDevices(u: User) {
  pendingUser.value = u.rawName || u.name
  emit('navigate', 'devices')
}

function openUserEdit(u: User) {
  userEdit.value = {
    id: u.id, rawName: u.rawName || u.name,
    displayName: u.displayName || '', email: u.email || '',
    pictureUrl: u.pictureUrl || '',
  }
  editUserNewName.value = u.rawName || u.name
  drawer.value = 'useredit'
}
async function saveUserName() {
  if (!userEdit.value) return
  const v = editUserNewName.value.trim()
  if (!v) { emit('notify', '名称不能为空'); return }
  if (!/^[a-zA-Z0-9._-]+$/.test(v)) {
    emit('notify', '名称只能用字母、数字、点、下划线、短横线')
    return
  }
  if (v === userEdit.value.rawName) { drawer.value = ''; return }
  busy.value = true
  try {
    await api.renameUser(userEdit.value.id, v)
    drawer.value = ''
    await load()
    emit('notify', '已重命名为 ' + v)
  } catch (err: any) {
    emit('notify', '重命名失败：' + (err?.message ?? err))
  } finally { busy.value = false }
}
function startRename(u: User) {
  editingUser.value = u.id
  editUserName.value = u.rawName || u.name
}
async function commitRename(u: User) {
  const v = editUserName.value.trim()
  editingUser.value = ''
  if (!v || v === (u.rawName || u.name)) return
  try { await api.renameUser(u.id, v); await load(); emit('notify', '已重命名') }
  catch (err: any) { emit('notify', '重命名失败：' + (err?.message ?? err)) }
}
function deleteUser(u: User) {
  const who = u.rawName || u.name || '该成员'
  askConfirm({
    title: '删除这个成员？',
    desc: `${who}\n删除后该成员名下的设备、授权 Key 会一并失去归属，正在使用它登录的客户端会断开，且无法恢复。`,
    ok: '删除',
    action: async () => {
      try {
        await api.deleteUser(u.id)
        await load()
        emit('notify', '已删除 ' + who)
      } catch (err: any) { emit('notify', '删除失败：' + (err?.message ?? err)) }
    },
  })
}

// ---------- 通用 ----------

// 所有写操作走这里：失败就把服务端原文抛出来，不做假成功
async function run(label: string, fn: () => Promise<void>) {
  busy.value = true
  try {
    await fn()
    emit('notify', label + '成功')
  } catch (err: any) {
    emit('notify', label + '失败：' + (err?.message ?? err))
  } finally {
    busy.value = false
  }
}

async function copyKey() {
  try { await navigator.clipboard.writeText(generated.value); emit('notify', '已复制') }
  catch { emit('notify', '复制失败，请手动选中') }
}
function statusTag(s: string) {
  if (s === '在线' || s === '有效' || s === '正常') return 'green'
  if (s === '待授权' || s === '已使用') return 'amber'
  if (s === '已吊销' || s === '已过期') return 'red'
  return ''
}
function splitList(v: string) {
  return (v || '').split(/[,\s]+/).map(s => s.trim()).filter(Boolean)
}
</script>

<template>
  <div class="page-flex">
    <div class="spread" style="margin-bottom:16px;flex-shrink:0">
      <div class="faint" style="font-size:11.5px">实时数据</div>
      <div class="row">
        <button class="btn" :disabled="loading" @click="load">
          <span v-if="loading" class="spinner"></span>{{ loading ? '刷新中…' : '刷新' }}
        </button>
        <template v-if="tab === 'devices'">
          <button class="btn ghost" @click="backfill"
                  title="给 ip_addresses 为空的节点补分配地址。正常注册的节点都有 IP，只有从很老的版本升级上来才可能需要">同步节点 IP</button>
          <button class="btn ghost" @click="openDebug">手动添加节点</button>
          <button class="btn" @click="openAuth">待审批请求</button>
          <button class="btn primary" @click="openRegister">＋ 注册设备</button>
        </template>
        <button v-else-if="tab === 'keys'" class="btn primary" @click="openKeyForm">＋ 新建授权 Key</button>
        <button v-else-if="tab === 'api'" class="btn primary" @click="openApiKeyForm">＋ 新建 API Key</button>
      </div>
    </div>

    <div class="tabs" style="flex-shrink:0">
      <button class="tab" :class="{ active: tab === 'devices' }" @click="emit('navigate', 'devices')">设备管理</button>
      <button class="tab" :class="{ active: tab === 'users' }" @click="emit('navigate', 'users')">人员管理</button>
      <button class="tab" :class="{ active: tab === 'keys' }" @click="emit('navigate', 'keys')">授权 Key</button>
      <button class="tab" :class="{ active: tab === 'api' }" @click="emit('navigate', 'api')">API Key</button>
    </div>

    <div v-if="tab !== 'users'" class="filter-bar">
      <input class="input" style="width:240px" v-model="keyword"
             :placeholder="tab === 'devices' ? '搜索设备名 / IP / 标签…'
               : tab === 'keys' ? '搜索 Key / 成员…' : '搜索 Key 前缀…'" @keyup.enter="load" />

      <div v-if="tab === 'devices'" style="width:132px"><Pick v-model="statusFilter" :options="statusOptions" /></div>
      <div v-if="tab === 'devices' || tab === 'keys'" style="width:132px"><Pick v-model="userFilter" :options="memberOptions" /></div>
      <div v-if="tab === 'devices' && allTags.length" style="width:132px"><Pick v-model="tagFilter" :options="tagOptions" /></div>
      <div v-if="tab === 'keys' || tab === 'api'" style="width:132px"><Pick v-model="statusFilter" :options="statusOptions" /></div>

      <button v-if="keyword || statusFilter || userFilter || tagFilter" class="btn ghost sm"
              @click="keyword=''; statusFilter=''; userFilter=''; tagFilter=''">清空条件</button>

      <span v-if="tab === 'devices'" class="faint" style="margin-left:auto;font-size:11px">
        <span style="color:var(--ok)">在线 {{ deviceSummary.online }}</span> ·
        离线 {{ deviceSummary.offline }} ·
        过期 {{ deviceSummary.expired }} ·
        共 {{ devices.length }} 台
      </span>
      <span v-else class="faint" style="margin-left:auto;font-size:11px">
        {{ tab === 'keys' ? shownKeys.length : shownApiKeys.length }} 条记录
      </span>
    </div>

    <!-- 设备 -->
    <div class="card table-scroll" style="padding:0;position:relative" v-if="tab === 'devices'">
      <div v-if="loading" class="load-overlay"><span class="spinner"></span>加载中…</div>
      <table class="tbl">
        <thead><tr>
          <th style="width:22%">设备</th><th>成员</th><th style="width:130px">IP</th><th style="width:80px">连接</th><th>标签</th><th>最后在线</th><th style="width:70px">状态</th><th style="width:70px"></th>
        </tr></thead>
        <tbody>
          <tr v-for="d in shownDevices" :key="d.id" style="cursor:pointer" @click="openDevice(d)">
            <td><div class="cell-main">{{ d.name }}</div><div class="cell-sub">{{ d.registerMethod }} · {{ d.hostname }}</div></td>
            <td>{{ d.user || '—' }}</td>
            <td class="mono" style="font-size:11px;line-height:1.5">
              <div v-for="(ip, i) in (d.ips && d.ips.length ? d.ips : [d.ip])" :key="i"
                   :class="i === 0 ? '' : 'faint'">{{ ip || '—' }}</div>
            </td>
            <td>
              <span v-if="connKind(d) === '直连'" class="tag green">直连</span>
              <span v-else-if="connKind(d) === '中继'" class="tag amber">中继</span>
              <span v-else class="faint">—</span>
            </td>
            <td><span v-if="d.tags" class="tag blue">{{ d.tags }}</span><span v-else class="faint">—</span></td>
            <td>{{ d.lastSeen || '—' }}</td>
            <td><span class="tag" :class="statusTag(d.status)">{{ d.status }}</span></td>
            <td><button class="btn ghost sm" @click.stop="openDevice(d)">管理</button></td>
          </tr>
          <tr v-if="!shownDevices.length"><td colspan="8">
            <div class="empty-state">
              <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3 10h18M8 15h5"/></svg>
              <b>{{ devices.length ? '没有符合条件的设备' : '还没有设备接入' }}</b>
              <span>{{ devices.length ? '换个筛选条件试试' : '在设备页点「注册设备」，或让终端跑 headscale login' }}</span>
            </div>
          </td></tr>
        </tbody>
      </table>
    </div>

    <!-- 成员 -->
    <div v-else-if="tab === 'users'" style="display:flex;flex-direction:column;min-height:0;flex:1">
      <div class="spread" style="margin-bottom:14px;flex-shrink:0">
        <span class="faint" style="font-size:11px">共 {{ users.length }} 个成员</span>
        <button class="btn primary" :disabled="!canCreateUser" @click="openUserForm">＋ 新建成员</button>
      </div>
      <div class="card table-scroll" style="padding:0;position:relative">
        <div v-if="loading" class="load-overlay"><span class="spinner"></span>加载中…</div>
        <table class="tbl">
          <thead><tr>
            <th style="width:24%">成员 / 命名空间</th><th>显示名称</th><th>邮箱</th>
            <th style="width:100px">设备</th><th>创建时间</th><th style="width:150px"></th>
          </tr></thead>
          <tbody>
            <tr v-for="u in users" :key="u.id">
              <td class="cell-main">
                <input v-if="editingUser === u.id" class="input" style="height:28px;max-width:200px"
                       v-model="editUserName" @keyup.enter="commitRename(u)" @keyup.esc="editingUser = ''" />
                <span v-else class="mono">{{ u.rawName || u.name }}</span>
              </td>
              <td>{{ u.displayName || '—' }}</td>
              <td class="mono">{{ u.email || '—' }}</td>
              <td>
                <button v-if="statOf(u).total" class="link-btn" :class="statOf(u).online ? 'ok' : ''"
                        @click="jumpToUserDevices(u)">
                  {{ statOf(u).online }}/{{ statOf(u).total }}
                </button>
                <span v-else class="faint">0</span>
              </td>
              <td>{{ u.createdAt || '—' }}</td>
              <td class="row" style="justify-content:flex-end">
                <template v-if="editingUser === u.id">
                  <button class="btn ghost sm" @click="commitRename(u)">保存</button>
                  <button class="btn ghost sm" @click="editingUser = ''">取消</button>
                </template>
                <template v-else>
                  <button class="btn ghost sm" :disabled="!canCreateUser" @click="openUserEdit(u)">编辑</button>
                  <button class="btn ghost sm" :disabled="!canCreateUser" @click="deleteUser(u)">删除</button>
                </template>
              </td>
            </tr>
            <tr v-if="!users.length"><td colspan="6">
              <div class="empty-state">
                <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"><circle cx="12" cy="8" r="3.2"/><path d="M5 19c1.2-3.2 3.8-4.6 7-4.6s5.8 1.4 7 4.6"/></svg>
                <b>还没有成员</b>
                <span>点右上角「新建成员」创建一个命名空间</span>
              </div>
            </td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 授权 Key -->
    <div class="card table-scroll" style="padding:0;position:relative" v-else-if="tab === 'keys'">
      <div v-if="loading" class="load-overlay"><span class="spinner"></span>加载中…</div>
      <table class="tbl">
        <thead><tr><th>Key</th><th>成员</th><th>类型</th><th>到期</th><th>创建时间</th><th>状态</th><th style="width:120px"></th></tr></thead>
        <tbody>
          <tr v-for="k in shownKeys" :key="k.id">
            <td>
              <div class="cell-main mono">{{ k.key }}</div>
              <div class="cell-sub">
                {{ k.reusable ? '可复用' : (k.used ? '已用掉' : '未使用') }}
                <template v-if="k.aclTags"> · {{ k.aclTags }}</template>
              </div>
            </td>
            <td>{{ k.user || '—' }}</td>
            <td>{{ k.reusable ? '可复用' : '一次性' }}<span v-if="k.ephemeral" class="tag amber" style="margin-left:5px">临时节点</span></td>
            <td>{{ k.expires || '—' }}</td><td>{{ k.created || '—' }}</td>
            <td><span class="tag" :class="statusTag(k.status)">{{ k.status }}</span></td>
            <td class="row" style="justify-content:flex-end">
              <button class="btn ghost sm" :disabled="k.status !== '有效' && k.status !== '已使用'"
                      title="立即作废，无法恢复；已注册的节点不受影响"
                      @click="expireKey(k)">吊销</button>
              <button class="btn ghost sm" @click="deleteKey(k)">删除</button>
            </td>
          </tr>
          <tr v-if="!shownKeys.length"><td colspan="7">
            <div class="empty-state">
              <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"><circle cx="8" cy="9" r="3.2"/><path d="M10.4 11.4 19 20M16 17l2-2"/></svg>
              <b>{{ keys.length ? '没有符合条件的 Key' : '还没有授权 Key' }}</b>
              <span>{{ keys.length ? '换个筛选条件试试' : '点右上角「新建授权 Key」生成一个' }}</span>
            </div>
          </td></tr>
        </tbody>
      </table>
    </div>

    <!-- API Key -->
    <div class="card table-scroll" style="padding:0;position:relative" v-else>
      <div v-if="loading" class="load-overlay"><span class="spinner"></span>加载中…</div>
      <table class="tbl">
        <thead><tr><th style="width:20%">前缀</th><th>到期</th><th>创建时间</th><th>最后使用</th><th>状态</th><th style="width:120px"></th></tr></thead>
        <tbody>
          <tr v-for="k in shownApiKeys" :key="k.id">
            <td class="cell-main mono">{{ k.prefix }}</td>
            <td>{{ k.expires || '—' }}</td><td>{{ k.created || '—' }}</td><td>{{ k.lastSeen || '—' }}</td>
            <td><span class="tag" :class="statusTag(k.status)">{{ k.status }}</span></td>
            <td class="row" style="justify-content:flex-end">
              <button class="btn ghost sm" :disabled="k.status !== '有效'" title="立即作废，正在使用该 Key 的客户端会失去管理权限"
                      @click="expireApiKey(k)">吊销</button>
              <button class="btn ghost sm" @click="deleteApiKey(k)">删除</button>
            </td>
          </tr>
          <tr v-if="!shownApiKeys.length"><td colspan="6">
            <div class="empty-state">
              <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"><circle cx="8" cy="9" r="3.2"/><path d="M10.4 11.4 19 20M16 17l2-2"/></svg>
              <b>{{ apiKeys.length ? '没有符合条件的 API Key' : '还没有 API Key' }}</b>
              <span>{{ apiKeys.length ? '换个筛选条件试试' : '点右上角「新建 API Key」生成一个' }}</span>
            </div>
          </td></tr>
        </tbody>
      </table>
    </div>

    <template v-if="drawer">
      <div class="mask" @click="close"></div>
      <div class="drawer">
        <div class="spread" style="margin-bottom:18px">
          <b style="font-size:14px">
            {{ drawer === 'device' ? '设备详情' : drawer === 'register' ? '注册设备'
              : drawer === 'auth' ? '认证请求' : drawer === 'debug' ? '调试建节点'
                  : drawer === 'apikey' ? '新建 API Key'
                  : drawer === 'user' ? '新建成员'
                    : drawer === 'useredit' ? '编辑成员' : '新建授权 Key' }}
          </b>
          <button class="btn ghost sm" @click="close">✕</button>
        </div>

        <!-- 设备详情 -->
        <template v-if="drawer === 'device' && sel">
          <div class="kv"><span>节点 ID</span><b class="mono">{{ sel.id }}</b></div>
          <div class="kv"><span>主机名</span><b>{{ sel.hostname || '—' }}</b></div>
          <div class="kv"><span>IP</span><b class="mono">{{ sel.ips?.join(', ') || sel.ip || '—' }}</b></div>
          <div class="kv"><span>成员</span><b>{{ sel.user || '—' }}</b></div>
          <div class="kv"><span>注册方式</span><b>{{ sel.registerMethod || '—' }}</b></div>
          <div class="kv"><span>创建时间</span><b>{{ sel.createdAt || '—' }}</b></div>
          <div class="kv"><span>最后在线</span><b>{{ sel.lastSeen || '—' }}</b></div>
          <div class="kv"><span>到期</span><b>{{ sel.expiry || '—' }}</b></div>
          <div class="kv"><span>Machine Key</span><b class="mono" style="font-size:10px">{{ sel.machineKey || '—' }}</b></div>

          <div class="field" style="margin-top:16px">
            <label class="field-label">重命名</label>
            <div class="row">
              <input class="input" v-model="edit.name" />
              <button class="btn sm" :disabled="busy" @click="saveRename">保存</button>
            </div>
          </div>

          <div class="field">
            <label class="field-label">标签（逗号分隔）</label>
            <div class="row">
              <input class="input mono" v-model="edit.tags" placeholder="tag:server,tag:prod" />
              <button class="btn sm" :disabled="busy" @click="saveTags">保存</button>
            </div>
          </div>

          <div class="field">
            <label class="field-label">子网路由（勾选批准）</label>
            <div v-if="!routeItems.length" class="faint" style="font-size:11px">该节点没有通告任何路由</div>
            <div v-else>
              <div v-for="r in routeItems" :key="r" class="spread" style="padding:6px 0">
                <span class="mono">{{ r }}</span>
                <button class="switch" :class="{ on: checkedRoutes.has(r) }" @click="toggleRoute(r)"></button>
              </div>
              <button class="btn sm" style="margin-top:8px" :disabled="busy" @click="saveRoutes">保存路由</button>
            </div>
          </div>

          <div class="row" style="margin-top:18px;gap:8px">
            <button class="btn sm" :disabled="busy" @click="expireDev">立即到期</button>
            <button class="btn sm" :disabled="busy" @click="cancelExpiry">取消到期</button>
          </div>
          <button class="btn sm danger" style="width:100%;margin-top:10px"
                  :disabled="busy" @click="removeDev">
            删除设备
          </button>
        </template>

        <!-- 编辑成员 -->
        <template v-else-if="drawer === 'useredit' && userEdit">
          <div class="field">
            <label class="field-label">成员名称 <span style="color:var(--danger)">*</span></label>
            <input class="input mono" v-model="editUserNewName" @keyup.enter="saveUserName" />
            <div class="faint" style="font-size:10.5px;margin-top:4px">
              改名会同步到 headscale，节点归属跟着走；该名字也是 ACL 里引用成员的标识
            </div>
          </div>
          <div class="field">
            <label class="field-label">显示名称</label>
            <input class="input" :value="userEdit.displayName || '—'" readonly
                   style="opacity:.6;cursor:not-allowed" />
          </div>
          <div class="field">
            <label class="field-label">邮箱</label>
            <input class="input mono" :value="userEdit.email || '—'" readonly
                   style="opacity:.6;cursor:not-allowed" />
          </div>
          <div class="notice info" style="margin-bottom:14px">
            headscale 的用户接口只有创建、改名、删除三种。邮箱和显示名是
            创建时写入的，之后由 OIDC 提供方（或命令行）维护，没有单独的修改接口 ——
            所以这里只读。要改的话得在提供方那边改，或者删了重建。
          </div>
          <div class="row" style="gap:8px">
            <button class="btn primary" style="flex:1;height:38px" :disabled="busy" @click="saveUserName">
              {{ busy ? '保存中…' : '保存名称' }}
            </button>
            <button class="btn danger" style="flex:none" :disabled="busy"
                    @click="userEdit && deleteUser({ id: userEdit.id, name: userEdit.rawName } as User)">删除</button>
          </div>
        </template>

        <!-- 新建成员 -->
        <template v-else-if="drawer === 'user'">
          <div class="field">
            <label class="field-label">成员名称 <span style="color:var(--danger)">*</span></label>
            <input class="input mono" v-model="userForm.name" placeholder="boothua"
                   @keyup.enter="createUser" />
            <div class="faint" style="font-size:10.5px;margin-top:4px">
              对应 headscale 的 user.name，创建后用作命名空间，只能用字母、数字、点、下划线、短横线
            </div>
          </div>
          <div class="field">
            <label class="field-label">显示名称</label>
            <input class="input" v-model="userForm.displayName" placeholder="选填，界面上显示的名字" />
          </div>
          <div class="field">
            <label class="field-label">邮箱</label>
            <input class="input mono" v-model="userForm.email" placeholder="选填，name@example.com" />
          </div>
          <div class="field">
            <label class="field-label">头像地址</label>
            <input class="input mono" v-model="userForm.pictureUrl" placeholder="选填，https://…" />
          </div>
          <button class="btn primary" style="width:100%;height:38px" :disabled="busy" @click="createUser">
            <span v-if="busy" class="spinner"></span>{{ busy ? '创建中…' : '创建成员' }}
          </button>
          <div class="notice info" style="margin-top:16px">
            创建后可以在成员行上「重命名」或「删除」。删除会连带该成员名下的节点归属，
            headscale 要求先清空节点才能删成员。
          </div>
        </template>

        <!-- 注册设备 -->
        <template v-else-if="drawer === 'register'">
          <div class="field">
            <label class="field-label">归属成员</label>
            <Pick v-model="reg.user" :options="userOptions" placeholder="请先创建成员" />
          </div>
          <div class="field">
            <label class="field-label">预授权 Key</label>
            <input class="input mono" v-model="reg.key" placeholder="hskey-auth-…" />
          </div>
          <button class="btn primary" style="width:100%;height:38px" :disabled="busy" @click="doRegister">注册</button>
        </template>

        <!-- 认证请求 -->
        <template v-else-if="drawer === 'auth'">
          <div class="notice info" style="margin-bottom:14px">
            CLI / OIDC 登录后会产生一个 Auth ID，在这里批准或拒绝。
          </div>
          <div class="field">
            <label class="field-label">Auth ID</label>
            <input class="input mono" v-model="auth.authID" placeholder="用 headscale nodes list 查看" />
          </div>
          <div class="row" style="gap:8px">
            <button class="btn primary" style="flex:1" :disabled="busy" @click="approveAuth">批准</button>
            <button class="btn danger" style="flex:1" :disabled="busy" @click="rejectAuth">拒绝</button>
          </div>
          <div style="height:1px;background:var(--line);margin:18px 0"></div>
          <div class="field">
            <label class="field-label">或用 Auth ID 直接注册到成员</label>
            <Pick v-model="auth.user" :options="userOptions" placeholder="请先创建成员" />
          </div>
          <button class="btn" style="width:100%" :disabled="busy" @click="registerAuth">注册</button>
        </template>

        <!-- 调试建节点 -->
        <template v-else-if="drawer === 'debug'">
          <div class="notice warn" style="margin-bottom:14px">调试接口，直接在后端建一个节点记录，不跑真实客户端。</div>
          <div class="field">
            <label class="field-label">归属成员</label>
            <Pick v-model="debugForm.user" :options="userOptions" placeholder="请先创建成员" />
          </div>
          <div class="field"><label class="field-label">预授权 Key</label><input class="input mono" v-model="debugForm.key" /></div>
          <div class="field"><label class="field-label">节点名</label><input class="input" v-model="debugForm.name" /></div>
          <div class="field"><label class="field-label">路由（逗号分隔，可空）</label><input class="input mono" v-model="debugForm.routes" placeholder="10.0.0.0/24" /></div>
          <button class="btn primary" style="width:100%;height:38px" :disabled="busy" @click="doDebug">创建</button>
        </template>

        <!-- 新建 API Key -->
        <template v-else-if="drawer === 'apikey'">
          <template v-if="!generated">
            <div class="field">
              <label class="field-label">有效期</label>
              <Pick v-model="apiKeyForm.days" :options="dayOptions" />
            </div>
            <button class="btn primary" style="width:100%;height:38px" :disabled="busy" @click="createApiKey">生成</button>
          </template>
          <template v-else>
            <div class="notice warn" style="margin-bottom:14px">仅显示一次，关闭后无法再查看明文。</div>
            <textarea class="area mono" rows="3" readonly :value="generated" style="height:auto"></textarea>
            <button class="btn" style="width:100%;margin-top:10px" @click="copyKey">复制</button>
            <button class="btn primary" style="width:100%;margin-top:10px;height:38px" @click="close">我已保存</button>
          </template>
        </template>

        <!-- 新建授权 Key -->
        <template v-else>
          <template v-if="!generated">
            <div class="field">
              <label class="field-label">归属成员</label>
              <Pick v-model="form.user" :options="userOptions" placeholder="请先创建成员" />
            </div>
            <div class="field">
              <label class="field-label">有效期</label>
              <Pick v-model="form.days" :options="dayOptions" />
            </div>
            <div class="spread" style="margin-bottom:14px">
              <div><b style="font-size:12px">可复用</b><div class="faint" style="font-size:10.5px;margin-top:3px">允许多台设备使用同一 Key</div></div>
              <button class="switch" :class="{ on: form.reusable }" @click="form.reusable = !form.reusable"></button>
            </div>
            <div class="spread" style="margin-bottom:14px">
              <div><b style="font-size:12px">临时节点 Ephemeral</b><div class="faint" style="font-size:10.5px;margin-top:3px">离线后自动删除设备</div></div>
              <button class="switch" :class="{ on: form.ephemeral }" @click="form.ephemeral = !form.ephemeral"></button>
            </div>
            <div class="field">
              <label class="field-label">ACL 标签</label>
              <input class="input mono" v-model="form.tags" placeholder="tag:server, tag:db（可选，逗号分隔）" />
              <div class="faint" style="font-size:10.5px;margin-top:4px">
                用这个 Key 注册的节点会自动带上这些标签，对应 headscale 的 aclTags
              </div>
            </div>
            <button class="btn primary" style="width:100%;height:38px" :disabled="busy" @click="createKey">生成 Key</button>
          </template>
          <template v-else>
            <div class="notice warn" style="margin-bottom:14px">仅显示一次，关闭后无法再查看明文。</div>
            <textarea class="area mono" rows="4" readonly :value="generated" style="height:auto"></textarea>
            <button class="btn" style="width:100%;margin-top:10px" @click="copyKey">复制</button>
            <button class="btn primary" style="width:100%;margin-top:10px;height:38px" @click="close">我已保存，关闭</button>
          </template>
        </template>
      </div>
    </template>

    <!-- 吊销 / 删除的二次确认 -->
    <ConfirmDialog v-if="confirmBox" v-bind="confirmBox" @close="closeConfirm" />
  </div>
</template>
