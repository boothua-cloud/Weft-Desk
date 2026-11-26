<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type State, type Device, type User, type AuthKey, type APIKey } from '../api'
import Pick from '../components/Pick.vue'
import { reloadTick } from '../bus'

const props = defineProps<{ tab: string; state: State | null }>()
const emit = defineEmits(['notify', 'navigate'])
const devices = ref<Device[]>([])
const users = ref<User[]>([])
const keys = ref<AuthKey[]>([])
const apiKeys = ref<APIKey[]>([])
const keyword = ref('')
const busy = ref(false)

// 抽屉：新建 Key / 设备详情 / 注册设备 / 批准认证 / 新建 API Key
type Drawer = '' | 'key' | 'device' | 'register' | 'auth' | 'apikey' | 'debug'
const drawer = ref<Drawer>('')
const generated = ref('')
const newUser = ref('')
const form = ref({ user: '', reusable: true, ephemeral: false, days: 90 })

const sel = ref<Device | null>(null)
const edit = ref({ name: '', tags: '', routes: '', expiry: '', confirmDelete: false })
const reg = ref({ user: '', key: '' })
const auth = ref({ user: '', authID: '' })
const debugForm = ref({ user: '', key: '', name: '', routes: '' })
const apiKeyForm = ref({ days: 90 })

const userOptions = computed(() => users.value.map((u) => ({ value: u.name, label: u.rawName || u.name })))
const dayOptions = [1, 7, 30, 90, 180, 365].map((d) => ({ value: d, label: d + ' 天' }))

const canCreateUser = computed(() => props.state?.capabilities?.canCreateUser !== false)
const shownDevices = computed(() => devices.value.filter(d =>
  !keyword.value || (d.name + d.user + d.ip + d.tags).toLowerCase().includes(keyword.value.toLowerCase())))
const shownKeys = computed(() => keys.value.filter(k =>
  !keyword.value || (k.key + k.user).toLowerCase().includes(keyword.value.toLowerCase())))

async function load() {
  try {
    if (props.tab === 'devices') devices.value = (await api.devices()).items
    else if (props.tab === 'users') users.value = (await api.users()).items
    else if (props.tab === 'keys') {
      keys.value = (await api.authKeys()).items
      if (!users.value.length) users.value = (await api.users()).items
    } else apiKeys.value = (await api.apiKeys()).items
  } catch (err: any) {
    emit('notify', '加载失败：' + (err?.message ?? err))
  }
}
watch(() => props.tab, load, { immediate: true })
watch(reloadTick, load)

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
  edit.value.confirmDelete = false
}

// ---------- 设备 ----------

function openDevice(d: Device) {
  sel.value = d
  edit.value = {
    name: d.name,
    tags: d.tags,
    routes: d.advertisedRoutes || d.approvedRoutes || '',
    expiry: d.expiry,
    confirmDelete: false,
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
async function removeDev() {
  if (!sel.value) return
  if (!edit.value.confirmDelete) { edit.value.confirmDelete = true; return }
  await run('删除设备', async () => {
    await api.deleteDevice(sel.value!.id)
    close()
    await load()
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
    await api.backfillIPs(true)
    await load()
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
  form.value = { user: users.value[0]?.name ?? '', reusable: true, ephemeral: false, days: 90 }
  drawer.value = 'key'
}
async function createKey() {
  if (!form.value.user) { emit('notify', '请选择归属用户'); return }
  busy.value = true
  try {
    generated.value = await api.createAuthKey(form.value.user, form.value.reusable, form.value.ephemeral, form.value.days, '')
    await load()
    emit('notify', 'Key 已生成，仅显示一次')
  } catch (err: any) { emit('notify', '生成失败：' + (err?.message ?? err)) }
  finally { busy.value = false }
}
async function expireKey(k: AuthKey) {
  try { await api.expireAuthKey(k.id); await load(); emit('notify', 'Key 已失效') }
  catch (err: any) { emit('notify', '操作失败：' + (err?.message ?? err)) }
}
async function deleteKey(k: AuthKey) {
  try { await api.deleteAuthKey(k.id); await load(); emit('notify', 'Key 已删除') }
  catch (err: any) { emit('notify', '删除失败：' + (err?.message ?? err)) }
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
async function expireApiKey(k: APIKey) {
  try { await api.expireAPIKey(k.prefix, k.id); await load(); emit('notify', '已失效') }
  catch (err: any) { emit('notify', '操作失败：' + (err?.message ?? err)) }
}
async function deleteApiKey(k: APIKey) {
  try { await api.deleteAPIKey(k.prefix, k.id); await load(); emit('notify', '已删除') }
  catch (err: any) { emit('notify', '删除失败：' + (err?.message ?? err)) }
}

// ---------- 成员 ----------

async function createUser() {
  if (!newUser.value.trim()) return
  try {
    await api.createUser(newUser.value.trim(), '', '', '')
    newUser.value = ''
    await load()
    emit('notify', '用户已创建')
  } catch (err: any) { emit('notify', '创建失败：' + (err?.message ?? err)) }
}
const editingUser = ref('')
const editUserName = ref('')
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
async function deleteUser(u: User) {
  try { await api.deleteUser(u.id); await load(); emit('notify', '已删除 ' + u.name) }
  catch (err: any) { emit('notify', '删除失败：' + (err?.message ?? err)) }
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
  <div>
    <div class="spread" style="margin-bottom:16px">
      <div class="faint" style="font-size:11.5px">数据来源：Headscale /api/v1</div>
      <div class="row">
        <button class="btn" @click="load">刷新</button>
        <template v-if="tab === 'devices'">
          <button class="btn ghost" @click="backfill">补齐 IP</button>
          <button class="btn ghost" @click="openDebug">调试建节点</button>
          <button class="btn" @click="openAuth">认证请求</button>
          <button class="btn primary" @click="openRegister">＋ 注册设备</button>
        </template>
        <button v-else-if="tab === 'keys'" class="btn primary" @click="openKeyForm">＋ 新建授权 Key</button>
        <button v-else-if="tab === 'api'" class="btn primary" @click="openApiKeyForm">＋ 新建 API Key</button>
      </div>
    </div>

    <div class="tabs">
      <button class="tab" :class="{ active: tab === 'devices' }" @click="emit('navigate', 'devices')">设备管理</button>
      <button class="tab" :class="{ active: tab === 'users' }" @click="emit('navigate', 'users')">人员管理</button>
      <button class="tab" :class="{ active: tab === 'keys' }" @click="emit('navigate', 'keys')">授权 Key</button>
      <button class="tab" :class="{ active: tab === 'api' }" @click="emit('navigate', 'api')">API Key</button>
    </div>

    <div v-if="tab !== 'users'" class="card tight row" style="margin-bottom:14px;gap:10px">
      <input class="input" style="width:300px" v-model="keyword" placeholder="搜索名称 / 成员 / IP / 标签…" />
      <span class="faint" style="margin-left:auto;font-size:11px">
        {{ tab === 'devices' ? shownDevices.length : tab === 'keys' ? shownKeys.length : apiKeys.length }} 条记录
      </span>
    </div>

    <!-- 设备 -->
    <div class="card" style="padding:0;overflow:hidden" v-if="tab === 'devices'">
      <table class="tbl">
        <thead><tr>
          <th style="width:24%">设备</th><th>成员</th><th>Tailscale IP</th><th>标签</th><th>最后在线</th><th>状态</th><th style="width:80px"></th>
        </tr></thead>
        <tbody>
          <tr v-for="d in shownDevices" :key="d.id" style="cursor:pointer" @click="openDevice(d)">
            <td><div class="cell-main">{{ d.name }}</div><div class="cell-sub">{{ d.registerMethod }} · {{ d.hostname }}</div></td>
            <td>{{ d.user || '—' }}</td><td class="mono">{{ d.ip || '—' }}</td>
            <td><span v-if="d.tags" class="tag blue">{{ d.tags }}</span><span v-else class="faint">—</span></td>
            <td>{{ d.lastSeen || '—' }}</td>
            <td><span class="tag" :class="statusTag(d.status)">{{ d.status }}</span></td>
            <td><button class="btn ghost sm" @click.stop="openDevice(d)">管理</button></td>
          </tr>
          <tr v-if="!shownDevices.length"><td colspan="7" class="empty">没有匹配的设备</td></tr>
        </tbody>
      </table>
    </div>

    <!-- 成员 -->
    <div v-else-if="tab === 'users'">
      <div class="card tight row" style="margin-bottom:14px">
        <input class="input" style="width:220px" v-model="newUser" placeholder="新成员名称" :disabled="!canCreateUser" />
        <button class="btn" :disabled="!canCreateUser" @click="createUser">创建成员</button>
      </div>
      <div class="card" style="padding:0;overflow:hidden">
        <table class="tbl">
          <thead><tr><th>成员 / 命名空间</th><th>来源</th><th>创建时间</th><th style="width:170px"></th></tr></thead>
          <tbody>
            <tr v-for="u in users" :key="u.id">
              <td class="cell-main">
                <input v-if="editingUser === u.id" class="input" style="height:28px;max-width:200px"
                       v-model="editUserName" @keyup.enter="commitRename(u)" @keyup.esc="editingUser = ''" />
                <span v-else>{{ u.rawName || u.name }}</span>
              </td>
              <td><span class="tag" :class="u.provider ? 'blue' : ''">{{ u.provider || '本地' }}</span></td>
              <td>{{ u.createdAt || '—' }}</td>
              <td class="row" style="justify-content:flex-end">
                <template v-if="editingUser === u.id">
                  <button class="btn ghost sm" @click="commitRename(u)">保存</button>
                  <button class="btn ghost sm" @click="editingUser = ''">取消</button>
                </template>
                <template v-else>
                  <button class="btn ghost sm" :disabled="!canCreateUser" @click="startRename(u)">重命名</button>
                  <button class="btn ghost sm" :disabled="!canCreateUser" @click="deleteUser(u)">删除</button>
                </template>
              </td>
            </tr>
            <tr v-if="!users.length"><td colspan="4" class="empty">暂无成员</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 授权 Key -->
    <div class="card" style="padding:0;overflow:hidden" v-else-if="tab === 'keys'">
      <table class="tbl">
        <thead><tr><th>Key</th><th>成员</th><th>类型</th><th>到期</th><th>创建时间</th><th>状态</th><th style="width:120px"></th></tr></thead>
        <tbody>
          <tr v-for="k in shownKeys" :key="k.id">
            <td><div class="cell-main mono">{{ k.key }}</div><div class="cell-sub">已使用 {{ k.used }} 次</div></td>
            <td>{{ k.user || '—' }}</td>
            <td>{{ k.reusable ? '可复用' : '一次性' }}<span v-if="k.ephemeral" class="tag amber" style="margin-left:5px">临时节点</span></td>
            <td>{{ k.expires || '—' }}</td><td>{{ k.created || '—' }}</td>
            <td><span class="tag" :class="statusTag(k.status)">{{ k.status }}</span></td>
            <td class="row" style="justify-content:flex-end">
              <button class="btn ghost sm" @click="expireKey(k)">失效</button>
              <button class="btn ghost sm" @click="deleteKey(k)">删除</button>
            </td>
          </tr>
          <tr v-if="!shownKeys.length"><td colspan="7" class="empty">暂无授权 Key</td></tr>
        </tbody>
      </table>
    </div>

    <!-- API Key -->
    <div class="card" style="padding:0;overflow:hidden" v-else>
      <table class="tbl">
        <thead><tr><th style="width:20%">前缀</th><th>到期</th><th>创建时间</th><th>最后使用</th><th>状态</th><th style="width:120px"></th></tr></thead>
        <tbody>
          <tr v-for="k in apiKeys" :key="k.id">
            <td class="cell-main mono">{{ k.prefix }}</td>
            <td>{{ k.expires || '—' }}</td><td>{{ k.created || '—' }}</td><td>{{ k.lastSeen || '—' }}</td>
            <td><span class="tag" :class="statusTag(k.status)">{{ k.status }}</span></td>
            <td class="row" style="justify-content:flex-end">
              <button class="btn ghost sm" @click="expireApiKey(k)">失效</button>
              <button class="btn ghost sm" @click="deleteApiKey(k)">删除</button>
            </td>
          </tr>
          <tr v-if="!apiKeys.length"><td colspan="6" class="empty">暂无 API Key</td></tr>
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
                : drawer === 'apikey' ? '新建 API Key' : '新建授权 Key' }}
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
            {{ edit.confirmDelete ? '确认删除？再点一次' : '删除设备' }}
          </button>
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
            <div class="spread" style="margin-bottom:20px">
              <div><b style="font-size:12px">临时节点 Ephemeral</b><div class="faint" style="font-size:10.5px;margin-top:3px">离线后自动删除设备</div></div>
              <button class="switch" :class="{ on: form.ephemeral }" @click="form.ephemeral = !form.ephemeral"></button>
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
  </div>
</template>
