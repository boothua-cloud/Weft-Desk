<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { Events } from '@wailsio/runtime'
import { api, type Config, type HelperStatus, type State } from '../api'
import { reloadTick } from '../bus'
import { modeRef, setTheme, type ThemeMode } from '../theme'
import ProfileDialog from '../components/ProfileDialog.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { useConfirm } from '../confirm'

defineProps<{ state: State | null }>()
const emit = defineEmits(['refresh', 'notify'])

const cfg = ref<Config>({
  controlPlane: 'headscale', baseURL: 'https://hs.example.com', tailnet: '-',
  authMode: 'apikey', apiStyle: 'auto', tunnelEngine: 'system', hostname: 'weft',
  allowInsecure: false, authKey: '',
  autoConnect: true, autoReconnect: true, minimizeToTray: true,
  socksEnabled: false, socksPort: 1080, autoForward: false, devTools: false,
})
const secret = ref('')
const authKey = ref('')
const testResult = ref('')
const busy = ref(false)
const helper = ref<HelperStatus | null>(null)
const themes: { value: ThemeMode; label: string }[] = [
  { value: 'auto', label: '跟随系统' },
  { value: 'light', label: '亮色' },
  { value: 'dark', label: '暗色' },
]
const helperBusy = ref(false)

// 服务端列表：多个 Headscale 之间切换
type ProfileRow = { id: string; name: string; baseURL: string; hostname?: string; allowInsecure?: boolean }
const profiles = ref<ProfileRow[]>([])
const activeProfileID = ref('')
const newProfileName = ref('')
const switching = ref('')
// 行内重命名：点「重命名」把名字变成输入框，回车保存、Esc 取消
const renaming = ref('')
// 服务端编辑弹窗：null 表示关闭，{id:''} 表示新增
const dialogFor = ref<ProfileRow | null>(null)
const dialogOpen = ref(false)
function openCreate() {
  dialogFor.value = { id: '', name: '', baseURL: '', hostname: '' }
  dialogOpen.value = true
}
function openEdit(p: ProfileRow) {
  dialogFor.value = p
  dialogOpen.value = true
}
async function afterSaved() {
  dialogOpen.value = false
  await loadProfiles()
  await loadCfg()
  emit('refresh')
  emit('notify', '服务端已保存')
}
// 删除服务端是不可逆的：弹框确认过才执行
const { confirmBox, askConfirm, closeConfirm } = useConfirm()
const renameText = ref('')

function startRename(p: ProfileRow) {
  renaming.value = p.id
  renameText.value = p.name
}
async function commitRename(p: ProfileRow) {
  const name = renameText.value.trim()
  renaming.value = ''
  if (!name || name === p.name) return
  try {
    await api.renameProfile(p.id, name)
    await loadProfiles()
    emit('notify', '已改名为 ' + name)
  } catch (err: any) {
    emit('notify', '改名失败：' + (err?.message ?? err))
  }
}

async function loadProfiles() {
  try {
    const raw = await api.profilesView()
    const v = JSON.parse(raw) as { items: ProfileRow[] | null; active: string }
    profiles.value = v.items ?? []
    activeProfileID.value = v.active
    window.dispatchEvent(new CustomEvent('weft:profiles-changed'))
  } catch { /* 忽略 */ }
}

async function saveAsProfile() {
  const name = newProfileName.value.trim()
  if (!name) { emit('notify', '给这个服务端起个名字'); return }
  switching.value = 'new'
  try {
    await api.saveProfile(name)
    newProfileName.value = ''
    await loadProfiles()
    await loadCfg()
    emit('refresh')
    emit('notify', '已新增并切换到 ' + name)
  } catch (err: any) {
    emit('notify', '新增失败：' + (err?.message ?? err))
  } finally {
    switching.value = ''
  }
}

async function switchTo(p: ProfileRow) {
  if (p.id === activeProfileID.value) return
  switching.value = p.id
  try {
    await api.switchProfile(p.id)
    await loadProfiles()
    await loadCfg()
    emit('refresh')
    emit('notify', '已切换到 ' + p.name)
  } catch (err: any) {
    emit('notify', '切换失败：' + (err?.message ?? err))
  } finally {
    switching.value = ''
  }
}

function removeProfile(p: ProfileRow) {
  askConfirm({
    title: '删除这个服务端？',
    desc: `${p.name}\n${p.baseURL || '未填地址'}\n该服务端保存的地址、API Key、节点身份会一并删除，无法恢复。`,
    ok: '删除',
    action: async () => {
      switching.value = p.id
      try {
        await api.deleteProfile(p.id)
        await loadProfiles()
        await loadCfg()
        emit('refresh')
        emit('notify', '已删除 ' + p.name)
      } catch (err: any) {
        emit('notify', '删除失败：' + (err?.message ?? err))
      } finally {
        switching.value = ''
      }
    },
  })
}

async function loadHelper() {
  try { helper.value = await api.helperStatus() } catch { /* 未就绪时静默 */ }
}

async function loadCfg() {
  try { cfg.value = await api.config() } catch (err: any) {
    emit('notify', '读取配置失败：' + (err?.message ?? err))
  }
}

onMounted(async () => {
  window.addEventListener('weft:open-profile', openCreate)
  const offNav = Events.On('weft:navigate', (ev) => {
    if (ev.data === 'profile-new') openCreate()
  })
  onUnmounted(() => offNav?.())
  loadProfiles()
  await loadCfg()
  loadHelper()
})
watch(reloadTick, () => { loadCfg(); loadHelper() })
let helperTimer: number | undefined
onMounted(() => { helperTimer = window.setInterval(loadHelper, 5000) })
onUnmounted(() => {
  window.clearInterval(helperTimer)
  window.removeEventListener('weft:open-profile', openCreate)
})

async function save() {
  const k = secret.value.trim()
  if (k.startsWith('hskey-auth-') || k.startsWith('tskey-auth-')) {
    emit('notify', '这是节点注册的 Auth Key。管理接口要用 headscale apikeys create 生成的 Key')
    return
  }
  busy.value = true
  try {
    if (k) await api.setCredential('main', k)
    if (authKey.value.trim()) await api.setCredential('auth', authKey.value.trim())
    await api.saveConfig(cfg.value)
    secret.value = ''
    authKey.value = ''
    emit('refresh')
    emit('notify', '配置已保存')
  } catch (err: any) { emit('notify', '保存失败：' + (err?.message ?? err)) }
  finally { busy.value = false }
}

async function test() {
  try { testResult.value = await api.testConnection() }
  catch (err: any) { testResult.value = '连接失败：' + (err?.message ?? err) }
}

// 这几个开关与隧道无关，拨一下就直接落库，不用再点「保存配置」。
// 之前只改本地状态，切页或刷新时会被后端值覆盖，看着就像「开了又自己关掉」。
type PrefKey = 'autoConnect' | 'autoReconnect' | 'minimizeToTray' | 'devTools'
const prefBusy = ref('')
async function togglePref(key: PrefKey) {
  if (prefBusy.value) return
  const next = !cfg.value[key]
  cfg.value[key] = next
  prefBusy.value = key
  try {
    const patch: Partial<Config> = {}
    patch[key] = next
    const saved = await api.setUIPrefs(patch)
    cfg.value[key] = saved[key]
    if (key === 'devTools' && next) emit('notify', '已开启，重开应用后生效')
    if (key === 'minimizeToTray') emit('notify', next ? '托盘图标已显示' : '托盘图标已隐藏')
  } catch (err: any) {
    cfg.value[key] = !next
    emit('notify', '设置保存失败：' + (err?.message ?? err))
  } finally { prefBusy.value = '' }
}

async function installHelper() {
  helperBusy.value = true
  try { helper.value = await api.installHelper(); emit('notify', '系统助手已就绪') }
  catch (err: any) { emit('notify', '安装失败：' + (err?.message ?? err)) }
  finally { helperBusy.value = false }
}
async function uninstallHelper() {
  helperBusy.value = true
  try { helper.value = await api.uninstallHelper(); emit('notify', '已移除') }
  catch (err: any) { emit('notify', '移除失败：' + (err?.message ?? err)) }
  finally { helperBusy.value = false }
}
</script>

<template>
  <div>
    <div class="card" style="margin-bottom:14px">
      <div class="spread" style="margin-bottom:12px;gap:14px;flex-wrap:wrap">
        <div>
          <span style="font-size:11.5px">服务端</span>
          <div class="faint" style="font-size:10.5px;margin-top:3px">
            可以配多个 Headscale，切换时隧道会断开重建；每个服务端的节点身份各自独立
          </div>
        </div>
        <button id="btn-add-profile" class="btn sm primary" @click="openCreate">+ 新增服务端</button>
      </div>
      <div v-for="p in profiles" :key="p.id" class="spread"
           style="padding:9px 0;border-bottom:1px solid var(--line-soft)">
        <div style="min-width:0;flex:1">
          <template v-if="renaming === p.id">
            <input class="input" style="height:26px;font-size:12px;max-width:240px" v-model="renameText"
                   autofocus @keyup.enter="commitRename(p)" @keyup.esc="renaming = ''" @blur="commitRename(p)" />
          </template>
          <template v-else>
            <b style="font-size:12px">{{ p.name }}</b>
          </template>
          <div class="faint mono" style="font-size:10.5px;margin-top:2px">{{ p.baseURL || '未填地址' }}</div>
        </div>
        <div class="row" style="flex:none">
          <span v-if="p.id === activeProfileID" class="tag green" style="margin:0">当前</span>
          <span v-if="switching === p.id" class="spinner"></span>
          <button v-else-if="p.id !== activeProfileID" class="btn ghost sm" title="切换过去后可在上方表单修改地址和凭证"
                  @click="switchTo(p)">切换</button>
          <button class="btn ghost sm" @click="openEdit(p)">编辑</button>
          <button class="btn ghost sm" @click="startRename(p)">重命名</button>
          <button class="btn ghost sm" @click="removeProfile(p)">删除</button>
        </div>
      </div>
    </div>

    <!-- 有 Key 但服务端不认（填错、填成 Auth Key）时，入口会整组消失。
         不说一句原因，看起来就像软件坏了。 -->
    <div v-if="state?.apiKeyIssue" class="notice warn" style="margin-bottom:14px">
      <b>管理 API Key 不可用，「服务端管理」已隐藏</b>
      <div style="margin-top:5px">{{ state.apiKeyIssue }}</div>
      <div style="margin-top:5px;font-size:10.5px;opacity:.85">
        在上方服务端点「编辑」更换 Key；不需要管理功能则点「清空」。
      </div>
    </div>

    <!-- 助手在跑就只留一行状态；出问题（没装 / 装了没跑 / 指向旧路径）才展开整块 -->
    <div v-if="helper?.running" class="card tight row" style="margin-bottom:14px">
      <span style="font-size:11.5px">系统助手</span>
      <span class="tag green">运行中</span>
      <span class="faint" style="font-size:10.5px;margin-left:6px">网络接管已就绪，不需要再操作</span>
      <button class="btn ghost sm" style="margin-left:auto" :disabled="helperBusy" @click="uninstallHelper">移除</button>
    </div>
    <div v-else class="card tight" style="margin-bottom:14px">
      <div class="spread" style="margin-bottom:10px">
        <div>
          <b style="font-size:12px">系统助手</b>
          <div class="faint" style="font-size:10.5px;margin-top:3px">{{ helper?.note || '读取中…' }}</div>
        </div>
        <span class="tag" :class="helper?.installed ? 'amber' : ''" style="flex:none">
          {{ helper?.installed ? '未运行' : '未安装' }}
        </span>
      </div>
      <pre v-if="helper?.logTail" class="mono" style="font-size:10px;line-height:1.5;background:var(--surface-2);padding:8px;border-radius:6px;margin:0 0 10px;white-space:pre-wrap;max-height:130px;overflow:auto">{{ helper.logTail }}</pre>
      <div class="row">
        <button id="btn-install-helper" class="btn" :disabled="helperBusy" @click="installHelper">
          {{ helper?.installed ? '重新安装' : '安装登录自启' }}
        </button>
        <span class="faint" style="font-size:10.5px">装好后开机自动启动并接管网络，不用每次确认 UAC</span>
      </div>
    </div>

    <div class="card tight" style="margin-bottom:14px">
      <div class="spread" style="margin-bottom:12px">
        <div>
          <span style="font-size:11.5px">外观主题</span>
          <div class="faint" style="font-size:10.5px;margin-top:3px">暗色适合夜间使用</div>
        </div>
        <div class="row" style="gap:6px">
          <button v-for="t in themes" :key="t.value" class="btn sm"
                  :class="{ primary: modeRef === t.value }"
                  @click="setTheme(t.value)">{{ t.label }}</button>
        </div>
      </div>
      <div class="spread" style="margin-bottom:10px"><span style="font-size:11.5px">开机自动连接</span><button class="switch" :class="{ on: cfg.autoConnect }" @click="togglePref('autoConnect')"></button></div>
      <div class="spread" style="margin-bottom:10px"><span style="font-size:11.5px">断线自动重连</span><button class="switch" :class="{ on: cfg.autoReconnect }" @click="togglePref('autoReconnect')"></button></div>
      <div class="spread" style="margin-bottom:10px">
        <div>
          <span style="font-size:11.5px">在系统托盘显示图标</span>
          <div class="faint" style="font-size:10.5px;margin-top:3px">关闭窗口不退出应用，⌘W 只是收起窗口</div>
        </div>
        <button class="switch" :class="{ on: cfg.minimizeToTray }" @click="togglePref('minimizeToTray')"></button>
      </div>
    </div>

    <div class="notice info" style="margin-bottom:14px">
      凭证只写系统钥匙串，不落数据库。API Key 由 <span class="mono">headscale apikeys create</span> 生成，
      Auth Key 只用于本机入网。
    </div>
  </div>

  <ProfileDialog v-if="dialogOpen" :profile="dialogFor" @close="dialogOpen = false"
                 @saved="afterSaved" @notify="emit('notify', $event)" />
  <ConfirmDialog v-if="confirmBox" v-bind="confirmBox" @close="closeConfirm" />
</template>
