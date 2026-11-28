<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type State, type AppBookmark, type SyncInfo } from '../api'
import Pick from '../components/Pick.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { useConfirm } from '../confirm'
import { reloadTick } from '../bus'

const props = defineProps<{ state: State | null }>()
const emit = defineEmits(['notify'])
const apps = ref<AppBookmark[]>([])
const allowInsecure = ref(true)
const hasHTTPS = computed(() => apps.value.some((a) => a.url.trim().toLowerCase().startsWith('https://')))
const keyword = ref('')
const groupFilter = ref('')
const groupOptions = computed(() => [{ value: '', label: '全部分组' }]
  .concat(groups.value.map(g => ({ value: g, label: g }))))
const groups = computed(() => {
  const set = new Set<string>()
  apps.value.forEach(a => { if (a.group) set.add(a.group) })
  return [...set].sort()
})
const drawer = ref(false)
const form = ref<AppBookmark>({ id: '', name: '', url: '', group: '常用', icon: '#0F6E56', iconData: '', browser: false, updatedAt: '', lastOpen: '' })
// 记下进抽屉时的地址：保存后只有「新建」或「地址变了」才重新抓图标，
// 改个名字就重新抓一遍，等于每次编辑都多等几秒
const editingUrl = ref('')
const colors = ['#0F6E56', '#185FA5', '#378ADD', '#D85A30', '#BA7517', '#534AB7']
const { confirmBox, askConfirm, closeConfirm } = useConfirm()

// 自定义顺序和最近使用是两种看法：一个是你排的，一个是系统记的。
// 拖动只在前者、且没有筛选时开——筛过的列表里拖动，落点和用户
// 看到的不是同一张表，重排结果会莫名其妙。
const sortMode = ref<'custom' | 'recent'>('custom')
const sortOptions = [{ value: 'custom', label: '自定义顺序' }, { value: 'recent', label: '最近使用' }]
const canDrag = computed(() => sortMode.value === 'custom' && !keyword.value.trim() && !groupFilter.value)

const shown = computed(() => {
  const list = apps.value.filter(a => {
    const kw = keyword.value.trim().toLowerCase()
    if (kw && !(a.name + a.url + a.group).toLowerCase().includes(kw)) return false
    if (groupFilter.value && a.group !== groupFilter.value) return false
    return true
  })
  if (sortMode.value === 'recent') {
    // 没打开过的排最后；RFC3339 直接按字符串倒序就是时间倒序
    return [...list].sort((a, b) => (b.lastOpen || '').localeCompare(a.lastOpen || ''))
  }
  return list
})

const missingIcons = computed(() => apps.value.filter(a => !a.iconData).length)
const filling = ref(false)

// 「3 天前用过」只在有记录时出现；超过一个月就不提了，
// 那时候说「上个月用过」跟没说差不多
function usedText(iso: string): string {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (!t) return ''
  const mins = Math.floor((Date.now() - t) / 60000)
  if (mins < 1) return '刚刚用过'
  if (mins < 60) return `${mins} 分钟前用过`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours} 小时前用过`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days} 天前用过`
  return ''
}

async function load() {
  try { apps.value = await api.apps() }
  catch (err: any) { emit('notify', '读取应用失败：' + (err?.message ?? err)) }
}
onMounted(load)
watch(reloadTick, load)

function openForm(a?: AppBookmark) {
  form.value = a
    ? { ...a }
    : { id: '', name: '', url: '', group: '常用', icon: colors[0], iconData: '', browser: false, updatedAt: '', lastOpen: '' }
  editingUrl.value = a?.url ?? ''
  drawer.value = true
}
async function submit() {
  if (!form.value.name || !form.value.url) {
    emit('notify', '请填写名称与地址')
    return
  }
  try {
    const saved = await api.saveApp(form.value)
    drawer.value = false
    await load()
    emit('notify', '应用已保存')
    // 新建或者换了地址才重新抓图标。抓取在后台跑，抓到了再刷一次列表；
    // 抓不到就留着色块，不影响用户继续点别的东西
    if (!form.value.id || saved.url !== editingUrl.value) {
      api.fetchAppIcon(saved.id).then(load).catch(() => {})
    }
  } catch (err: any) { emit('notify', '保存失败：' + (err?.message ?? err)) }
}

// ---------- 图标 ----------
async function fillIcons() {
  if (filling.value) return
  filling.value = true
  try {
    const r = await api.fillIcons()
    await load()
    if (r.total === 0) emit('notify', '所有应用都有图标了')
    else if (r.failed?.length) emit('notify', `抓到 ${r.done} 个，${r.failed.length} 个没抓到`)
    else emit('notify', `${r.done} 个图标都抓到了`)
  } catch (err: any) {
    emit('notify', '抓取失败：' + (err?.message ?? err))
  } finally { filling.value = false }
}

// ---------- 拖动排序 ----------
const dragId = ref('')
const overId = ref('')

function onDragStart(e: DragEvent, a: AppBookmark) {
  if (!canDrag.value) { e.preventDefault(); return }
  dragId.value = a.id
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', a.id)
  }
}
function onDragOver(e: DragEvent, a: AppBookmark) {
  if (!dragId.value || dragId.value === a.id) return
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
  overId.value = a.id
}
async function onDrop(e: DragEvent, target: AppBookmark) {
  e.preventDefault()
  const from = dragId.value
  dragId.value = ''
  overId.value = ''
  if (!from || from === target.id) return
  const list = [...apps.value]
  const i = list.findIndex(x => x.id === from)
  const j = list.findIndex(x => x.id === target.id)
  if (i < 0 || j < 0) return
  const [moved] = list.splice(i, 1)
  list.splice(j, 0, moved)
  // 先本地重排，不等写入：拖完立刻看到结果，失败再拉回真实顺序
  apps.value = list
  try {
    await api.reorderApps(list.map(x => x.id))
  } catch (err: any) {
    emit('notify', '保存顺序失败：' + (err?.message ?? err))
    await load()
  }
}
function onDragEnd() {
  dragId.value = ''
  overId.value = ''
}
function remove(a: AppBookmark) {
  askConfirm({
    title: '删除这个应用？',
    desc: `${a.name}\n${a.url}\n只删本地书签，不影响内网里的服务本身。`,
    ok: '删除',
    action: async () => {
      try {
        await api.deleteApp(a.id)
        await load()
        emit('notify', '已删除 ' + a.name)
      } catch (err: any) { emit('notify', '删除失败：' + (err?.message ?? err)) }
    },
  })
}
async function open(a: AppBookmark) {
  try {
    await api.openApp(a.id)
    emit('notify', '已在新窗口打开 ' + a.name)
  } catch (err: any) { emit('notify', '打开失败：' + (err?.message ?? err)) }
}
async function external(a: AppBookmark) {
  try { await api.openInBrowser(a.id) }
  catch (err: any) { emit('notify', '打开失败：' + (err?.message ?? err)) }
}

// ---------- 设备间同步 ----------
const syncOpen = ref(false)
const syncInfo = ref<SyncInfo | null>(null)
const syncTarget = ref('')
const syncCode = ref('')
const syncBusy = ref('')
const syncMsg = ref('')
const showToken = ref(false)
const syncPort = ref(4160)

// 只列在线且有 100.x 地址的，自己不算。
const peers = computed(() => (props.state?.peers ?? [])
  .filter(p => p.online && p.ip && p.ip !== props.state?.selfIP))

const tokenShown = computed(() => {
  const t = syncInfo.value?.token ?? ''
  if (!t) return '打开开关后生成'
  return showToken.value ? t : t.replace(/[A-Z0-9]/g, '•')
})

async function openSync() {
  syncOpen.value = true
  syncMsg.value = ''
  await refreshSync()
}
async function refreshSync() {
  try {
    syncInfo.value = await api.syncInfo()
    syncPort.value = syncInfo.value.port || 4160
  } catch (err: any) {
    syncMsg.value = '读不到同步状态：' + (err?.message ?? err)
  }
}
async function toggleSync() {
  const next = !(syncInfo.value?.enabled ?? false)
  try {
    syncInfo.value = await api.setSync(next, syncPort.value || 4160)
    emit('notify', next ? '其他设备可以同步本机了' : '已停止对外同步')
  } catch (err: any) {
    emit('notify', '设置失败：' + (err?.message ?? err))
  }
}
async function savePort() {
  if (!syncInfo.value?.enabled) return
  const p = Number(syncPort.value)
  if (!p || p < 1024 || p > 65535) {
    syncPort.value = syncInfo.value.port
    emit('notify', '端口要在 1024–65535 之间')
    return
  }
  try {
    syncInfo.value = await api.setSync(true, p)
    emit('notify', '已改用 ' + p + ' 端口')
  } catch (err: any) {
    emit('notify', '换端口失败：' + (err?.message ?? err))
  }
}
async function copyToken() {
  const t = syncInfo.value?.token ?? ''
  if (!t) return
  try {
    await navigator.clipboard.writeText(t)
    emit('notify', '同步码已复制')
  } catch {
    showToken.value = true
  }
}
function rotateToken() {
  askConfirm({
    title: '换一个同步码？',
    desc: '换完之后，已经配好的设备要重新输入新码。',
    ok: '换',
    action: async () => {
      try {
        syncInfo.value = await api.rotateSyncToken()
        emit('notify', '同步码已更换')
      } catch (err: any) { emit('notify', '更换失败：' + (err?.message ?? err)) }
    },
  })
}
async function pull() {
  if (!syncCheck()) return
  syncBusy.value = 'pull'
  syncMsg.value = ''
  try {
    const r = await api.pullApps(syncTarget.value.trim(), syncCode.value)
    await load()
    syncMsg.value = describe(r, '拉取')
  } catch (err: any) {
    syncMsg.value = '拉取失败：' + (err?.message ?? err)
  } finally { syncBusy.value = '' }
}
async function push() {
  if (!syncCheck()) return
  syncBusy.value = 'push'
  syncMsg.value = ''
  try {
    const r = await api.pushApps(syncTarget.value.trim(), syncCode.value)
    syncMsg.value = describe(r, '推送')
  } catch (err: any) {
    syncMsg.value = '推送失败：' + (err?.message ?? err)
  } finally { syncBusy.value = '' }
}
function syncCheck() {
  if (!syncTarget.value.trim()) { syncMsg.value = '先选一台设备，或填对方地址'; return false }
  if (!syncCode.value.trim()) { syncMsg.value = '还差对方的同步码'; return false }
  return true
}
function describe(r: { peer: string; added: number; updated: number; skipped: number; total: number }, verb: string) {
  const parts = [`新增 ${r.added}`, `更新 ${r.updated}`]
  if (r.skipped) parts.push(`跳过 ${r.skipped}`)
  return `${verb}完成：${parts.join('，')}。本机现有 ${r.total} 个应用。`
}
</script>

<template>
  <div class="page-flex">
    <div class="spread" style="margin-bottom:18px;flex-shrink:0">
      <div class="faint" style="font-size:11.5px">共 {{ apps.length }} 个内网应用 · 书签保存在本地 SQLite</div>
      <div class="row" style="gap:8px">
        <button v-if="missingIcons" class="btn" :disabled="filling" @click="fillIcons"
                :title="'从各站点抓 favicon，抓不到就留着色块'">
          <span v-if="filling" class="spinner"></span>{{ filling ? '抓取中…' : `补齐图标 (${missingIcons})` }}
        </button>
        <button class="btn" @click="openSync">同步到其他设备</button>
        <button class="btn primary" @click="openForm()">＋ 添加应用</button>
      </div>
    </div>

    <div v-if="hasHTTPS && !allowInsecure" class="notice warn" style="margin-bottom:14px">
      有应用用了 HTTPS：自签证书在应用窗口里会白屏。去服务端编辑弹窗打开「信任内网应用的自签证书」。
    </div>

    <div class="filter-bar">
      <input class="input" style="width:240px" v-model="keyword" placeholder="搜索名称 / 地址…" />
      <div style="width:132px"><Pick v-model="groupFilter" :options="groupOptions" /></div>
      <div style="width:140px"><Pick v-model="sortMode" :options="sortOptions" /></div>
      <button v-if="keyword || groupFilter" class="btn ghost sm"
              @click="keyword=''; groupFilter=''">清空条件</button>
      <span class="faint" style="margin-left:auto;font-size:11px">
        {{ canDrag ? '拖动卡片可以调整顺序' : `${shown.length} / ${apps.length} 个应用` }}
      </span>
    </div>

    <div class="table-scroll">
    <div class="grid3">
      <div v-for="a in shown" :key="a.id" class="appcard"
           :class="{ movable: canDrag, dragging: dragId === a.id, 'drop-target': overId === a.id }"
           :draggable="canDrag"
           @dragstart="onDragStart($event, a)" @dragover="onDragOver($event, a)"
           @drop="onDrop($event, a)" @dragend="onDragEnd">
        <div class="row">
          <div class="ico" :class="{ 'has-img': !!a.iconData }" :style="a.iconData ? {} : { background: a.icon || '#0F6E56' }">
            <img v-if="a.iconData" :src="a.iconData" alt="" />
            <template v-else>{{ a.name.slice(0, 2) }}</template>
          </div>
          <div style="min-width:0">
            <b>{{ a.name }}</b>
            <div class="url mono" style="color:var(--faint);font-size:10.5px;margin-top:3px">{{ a.url }}</div>
            <div v-if="usedText(a.lastOpen)" class="faint" style="font-size:10px;margin-top:2px">
              {{ usedText(a.lastOpen) }}
            </div>
          </div>
          <span v-if="a.browser" class="tag amber" style="margin-left:auto" title="用系统浏览器打开">浏览器</span>
          <span v-else class="tag" style="margin-left:auto">{{ a.group }}</span>
        </div>
        <div class="row" style="gap:6px">
          <button class="btn sm" style="flex:1" @click="open(a)">{{ a.browser ? '浏览器打开' : '应用内打开' }}</button>
          <button class="btn sm ghost" title="系统浏览器" @click="external(a)">↗</button>
          <button class="btn sm ghost" title="编辑" @click="openForm(a)">✎</button>
          <button class="btn sm ghost" title="删除" @click="remove(a)">✕</button>
        </div>
      </div>
      <div class="appcard" style="border-style:dashed;background:var(--surface-2);cursor:pointer;align-items:center;justify-content:center;min-height:130px"
           @click="openForm()">
        <div style="font-size:22px;color:var(--faint)">＋</div>
        <b>添加应用</b>
        <div class="faint" style="font-size:10.5px">名称 · 地址 · 分组 · 图标</div>
      </div>
    </div>
    </div>

    <template v-if="drawer">
      <div class="mask" @click="drawer = false"></div>
      <div class="drawer">
        <div class="spread" style="margin-bottom:20px">
          <b style="font-size:14px">{{ form.id ? '编辑应用' : '添加应用' }}</b>
          <button class="btn ghost sm" @click="drawer = false">✕</button>
        </div>
        <div class="field"><label class="field-label">应用名称</label><input class="input" v-model="form.name" placeholder="群晖 NAS" /></div>
        <div class="field"><label class="field-label">访问地址</label><input class="input mono" v-model="form.url" placeholder="http://100.64.0.3:5000" /></div>
        <div class="spread card tight" style="margin-bottom:14px">
          <div>
            <b style="font-size:12px">用系统浏览器打开</b>
            <div class="faint" style="font-size:10.5px;margin-top:3px">需要浏览器扩展（如密码管理器）时开启</div>
          </div>
          <button class="switch" :class="{ on: form.browser }" @click="form.browser = !form.browser"></button>
        </div>
        <div class="field"><label class="field-label">分组</label><input class="input" v-model="form.group" placeholder="常用" /></div>
        <div class="field">
          <label class="field-label">图标</label>
          <div class="row" style="align-items:center;gap:10px">
            <div class="ico form-ico" :class="{ 'has-img': !!form.iconData }"
                 :style="form.iconData ? {} : { background: form.icon }">
              <img v-if="form.iconData" :src="form.iconData" alt="" />
              <template v-else>{{ (form.name || '应用').slice(0, 2) }}</template>
            </div>
            <div class="faint" style="font-size:10.5px;line-height:1.7">
              {{ form.iconData ? '已从站点抓到图标' : '保存后自动抓取；抓不到就用下面的颜色和名字首字' }}
            </div>
          </div>
          <div class="row" style="margin-top:10px">
            <button v-for="c in colors" :key="c" class="swatch" :class="{ active: form.icon === c }"
                    :style="{ background: c }" @click="form.icon = c"></button>
          </div>
        </div>
        <button class="btn primary" style="width:100%;height:38px" @click="submit">保存</button>
        <div class="notice info" style="margin-top:16px">
          地址支持 100.x IP、MagicDNS 和自定义域名。打开时在本地起反向代理（127.0.0.1 随机端口），系统浏览器按钮也走它，只在云栈运行时有效。
        </div>
      </div>
    </template>

    <template v-if="syncOpen">
      <div class="mask" @click="syncOpen = false"></div>
      <div class="drawer">
        <div class="spread" style="margin-bottom:16px">
          <b style="font-size:14px">应用同步</b>
          <button class="btn ghost sm" @click="syncOpen = false">✕</button>
        </div>

        <div class="notice info" style="margin-bottom:18px">
          两台设备连着同一个 tailnet 才能同步，数据只在它们之间传，不经过服务器。
        </div>

        <div class="field">
          <label class="field-label">对方设备</label>
          <input class="input mono" v-model="syncTarget" placeholder="100.64.0.7 或 nas" />
          <div v-if="peers.length" class="row" style="gap:6px;flex-wrap:wrap;margin-top:8px">
            <button v-for="p in peers" :key="p.ip" class="btn sm ghost"
                    :class="{ primary: syncTarget === p.ip }" @click="syncTarget = p.ip">
              {{ p.name || p.ip }} · {{ p.ip }}
            </button>
          </div>
          <div v-else class="faint" style="font-size:11px;margin-top:6px">没有其他在线设备</div>
        </div>

        <div class="field">
          <label class="field-label">对方的同步码</label>
          <input class="input mono" v-model="syncCode" placeholder="XXXX-XXXX-XXXX-XXXX" />
        </div>

        <div class="row" style="gap:8px">
          <button class="btn primary" style="flex:1" :disabled="syncBusy === 'pull'" @click="pull">拉取到本机</button>
          <button class="btn" style="flex:1" :disabled="syncBusy === 'push'" @click="push">推送到对方</button>
        </div>

        <div v-if="syncMsg" class="notice info" style="margin-top:14px">{{ syncMsg }}</div>

        <div class="spread card tight" style="margin:22px 0 14px">
          <div>
            <b style="font-size:12px">让其他设备同步本机</b>
            <div class="faint" style="font-size:10.5px;margin-top:3px">开着时，知道同步码的设备能读写本机书签</div>
          </div>
          <button class="switch" :class="{ on: syncInfo?.enabled }" @click="toggleSync"></button>
        </div>

        <template v-if="syncInfo?.enabled">
          <div class="field">
            <label class="field-label">本机同步码</label>
            <div class="row" style="gap:6px">
              <input class="input mono" readonly :value="tokenShown" style="flex:1" />
              <button class="btn sm ghost" @click="showToken = !showToken">{{ showToken ? '隐藏' : '显示' }}</button>
              <button class="btn sm ghost" :disabled="!syncInfo?.token" @click="copyToken">复制</button>
            </div>
            <div class="row" style="gap:8px;margin-top:8px">
              <button class="btn sm ghost" @click="rotateToken">换一个</button>
              <span class="faint" style="font-size:11px">
                {{ syncInfo.listening
                  ? `在 ${syncInfo.selfIP || '100.x'}:${syncInfo.port} 等着`
                  : (syncInfo.error || '连上隧道后开始监听') }}
              </span>
            </div>
          </div>
          <div class="field">
            <label class="field-label">端口</label>
            <input class="input mono" type="number" v-model.number="syncPort" @change="savePort" />
          </div>
        </template>

        <div class="notice info" style="margin-top:16px">
          按地址合并：两台设备上都有同一个地址的应用时，留更新时间较新的那份。删除不会同步，各删各的。
        </div>
      </div>
    </template>

    <ConfirmDialog v-if="confirmBox" v-bind="confirmBox" @close="closeConfirm" />
  </div>
</template>

<style scoped>
/* 抓下来的 favicon 直接铺在色块里，等比缩放、跟着圆角裁 */
.ico { overflow: hidden; }
.ico img { width: 100%; height: 100%; object-fit: contain; display: block; }
/* 有图标时不再用满底色：很多图标是透明底的深色图形，压一块饱和色会糊成一团 */
.ico.has-img { background: var(--surface-2); padding: 2px; }

/* 拖动排序：能拖的时候光标变抓手，拖起来的那张退成半透明，
   落点用品牌色描一圈，比插一条线更好认 */
.appcard.movable { cursor: grab; }
.appcard.movable:active { cursor: grabbing; }
.appcard.dragging { opacity: .45; }
.appcard.drop-target {
  border-color: var(--brand);
  box-shadow: 0 0 0 2px var(--brand-soft);
  transform: none;
}
.form-ico {
  width: 38px;
  height: 38px;
  border-radius: 11px;
  display: grid;
  place-items: center;
  color: #fff;
  font-weight: 700;
  font-size: 13px;
  flex: none;
}
</style>
