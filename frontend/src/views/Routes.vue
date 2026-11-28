<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type Device, type State } from '../api'
import { reloadTick } from '../bus'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { useConfirm } from '../confirm'

defineProps<{ state: State | null }>()
const emit = defineEmits(['notify'])

const devices = ref<Device[]>([])
const loadErr = ref('')
const loading = ref(false)
const busy = ref('')
// 回退到本地缓存时，页面顶部要说明手里这份是哪天的
const cacheInfo = ref<{ updatedAt: string; error?: string } | null>(null)
// 缓存态下批准 / 撤销都不要发：服务端那边现在什么都不认
const stale = computed(() => !!cacheInfo.value)
const { confirmBox, askConfirm, closeConfirm } = useConfirm()

function splitList(s?: string): string[] {
  return (s || '').split(/[,\s]+/).map((x) => x.trim()).filter(Boolean)
}
const isExit = (r: string) => r === '0.0.0.0/0' || r === '::/0'

type Row = {
  key: string
  prefix: string
  dev: Device
  approved: boolean
}

const rows = computed<Row[]>(() => {
  const out: Row[] = []
  for (const d of devices.value) {
    const approved = new Set(splitList(d.approvedRoutes))
    const seen = new Set<string>()
    // 通告在前、只在批准列表里的排后面：后者是本机已经不再通告、
    // 但服务端还留着记录，撤销时也得能点到
    for (const r of [...splitList(d.advertisedRoutes), ...splitList(d.approvedRoutes)]) {
      if (seen.has(r)) continue
      seen.add(r)
      out.push({ key: d.id + '|' + r, prefix: r, dev: d, approved: approved.has(r) })
    }
  }
  // 待批准的排前面，同状态按设备名，不用来回找
  return out.sort((a, b) => {
    if (a.approved !== b.approved) return a.approved ? 1 : -1
    return a.dev.name.localeCompare(b.dev.name)
  })
})

const pending = computed(() => rows.value.filter((r) => !r.approved).length)
const approvedCount = computed(() => rows.value.length - pending.value)

async function load() {
  loading.value = true
  try {
    const v = await api.devices()
    devices.value = v.items ?? []
    loadErr.value = v.error || ''
    cacheInfo.value = v.source === 'cache' ? { updatedAt: v.updatedAt, error: v.error } : null
    // 列表重拉后行可能已经不在，选中项跟着清掉
    picked.value = new Set()
  } catch (err: any) {
    emit('notify', '读取设备失败：' + (err?.message ?? err))
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(reloadTick, load)

async function setRoutes(d: Device, list: string[], msg: string) {
  if (stale.value) { emit('notify', '服务端连不上，先恢复连接再改'); return }
  busy.value = d.id
  try {
    await api.approveRoutes(d.id, list.join(','))
    await load()
    emit('notify', msg)
  } catch (err: any) {
    emit('notify', '操作失败：' + (err?.message ?? err))
  } finally {
    busy.value = ''
  }
}

function approve(r: Row) {
  setRoutes(r.dev, [...splitList(r.dev.approvedRoutes), r.prefix], '已批准 ' + r.prefix)
}

function revoke(r: Row) {
  const rest = splitList(r.dev.approvedRoutes).filter((x) => x !== r.prefix)
  setRoutes(r.dev, rest, '已撤销 ' + r.prefix)
}

// ---------- 选中 ----------
// 批准和撤销都能批量做，所以两种状态的行都能勾。两个按钮各看各的那部分，
// 勾了待批准的点「撤销选中」不会有反应，也不会误伤。
const picked = ref<Set<string>>(new Set())
const pendingRows = computed(() => rows.value.filter((r) => !r.approved))
const pickedRows = computed(() => rows.value.filter((r) => picked.value.has(r.key)))
const pickedPending = computed(() => pickedRows.value.filter((r) => !r.approved))
const pickedApproved = computed(() => pickedRows.value.filter((r) => r.approved))
const allPicked = computed(() =>
  rows.value.length > 0 && pickedRows.value.length === rows.value.length,
)

function togglePick(key: string) {
  const next = new Set(picked.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  picked.value = next
}

function togglePickAll() {
  picked.value = allPicked.value ? new Set() : new Set(rows.value.map((r) => r.key))
}

// 按设备分组提交：服务端是整份替换，逐条发会把同一台设备之前批准的挤掉
function approvePicked() {
  const byDev = new Map<string, { d: Device; list: string[] }>()
  for (const r of pickedPending.value) {
    const cur = byDev.get(r.dev.id) || { d: r.dev, list: splitList(r.dev.approvedRoutes) }
    if (!cur.list.includes(r.prefix)) cur.list.push(r.prefix)
    byDev.set(r.dev.id, cur)
  }
  const n = pickedPending.value.length
  if (!n) return
  askConfirm({
    title: `批准选中的 ${n} 条路由？`,
    desc: [...byDev.values()].map((v) => `${v.d.name}：${v.list.join('、')}`).join('\n') + '\n\n只改服务端记录，路由器本机还要真的转发。',
    ok: '批准',
    tone: 'primary',
    action: async () => {
      busy.value = '__batch__'
      try {
        for (const { d, list } of byDev.values()) await api.approveRoutes(d.id, list.join(','))
        await load()
        emit('notify', `已批准 ${n} 条`)
      } catch (err: any) {
        await load()
        emit('notify', '操作失败：' + (err?.message ?? err))
      } finally {
        busy.value = ''
      }
    },
  })
}

// 撤销选中：同样按设备分组，把选中的网段从「已批准」里减掉再整份提交。
// 逐条发会把同一台设备上没勾的也一起撤销掉。
function revokePicked() {
  if (stale.value) { emit('notify', '服务端连不上，先恢复连接再改'); return }
  const n = pickedApproved.value.length
  if (!n) return
  const byDev = new Map<string, { d: Device; drop: string[]; list: string[] }>()
  for (const r of pickedApproved.value) {
    const cur = byDev.get(r.dev.id) || { d: r.dev, drop: [], list: splitList(r.dev.approvedRoutes) }
    cur.drop.push(r.prefix)
    byDev.set(r.dev.id, cur)
  }
  for (const v of byDev.values()) v.list = v.list.filter((x) => !v.drop.includes(x))
  askConfirm({
    title: `撤销选中的 ${n} 条路由？`,
    desc: `${[...byDev.values()].map((v) => `${v.d.name}：${v.drop.join('、')}`).join('\n')}\n\n撤销后这些网段从服务端路由表里移除，下次下发后客户端就访问不到了。`,
    ok: '撤销',
    tone: 'danger',
    action: async () => {
      busy.value = '__batch__'
      try {
        for (const { d, list } of byDev.values()) await api.approveRoutes(d.id, list.join(','))
        await load()
        emit('notify', `已撤销 ${n} 条`)
      } catch (err: any) {
        await load()
        emit('notify', '操作失败：' + (err?.message ?? err))
      } finally {
        busy.value = ''
      }
    },
  })
}

// 一次批准所有待批准的路由。按设备分组提交：服务端接口是「整份替换」，
// 逐条发会把同一台设备之前批准的挤掉
function approveAll() {
  const byDev = new Map<string, { d: Device; list: string[] }>()
  for (const r of rows.value) {
    if (r.approved) continue
    const cur = byDev.get(r.dev.id) || { d: r.dev, list: splitList(r.dev.approvedRoutes) }
    cur.list.push(r.prefix)
    byDev.set(r.dev.id, cur)
  }
  const n = pending.value
  askConfirm({
    title: `批准 ${n} 条子网路由？`,
    desc: `${[...byDev.values()].map((v) => `${v.d.name}：${v.list.join('、')}`).join('\n')}\n\n批准后这些网段进入 tailnet 路由表，被 ACL 放行的人就能访问。只改服务端记录，路由器本机还要真的转发。`,
    ok: '全部批准',
    tone: 'primary',
    action: async () => {
      try {
        for (const { d, list } of byDev.values()) await api.approveRoutes(d.id, list.join(','))
        await load()
        emit('notify', `已批准 ${n} 条`)
      } catch (err: any) {
        // 中途失败时前面几台已经批了，列表不能停在旧状态，
        // 不然看上去像「一条都没批」。
        await load()
        emit('notify', '操作失败：' + (err?.message ?? err))
      }
    },
  })
}
</script>

<template>
  <div>
    <div class="spread" style="margin-bottom:14px">
      <div class="row" style="gap:10px">
        <span v-if="pending" class="tag amber">{{ pending }} 条待批准</span>
        <span class="faint" style="font-size:11.5px">{{ approvedCount }} 条已批准</span>
      </div>
      <div class="row">
        <button class="btn" :disabled="loading" @click="load">
          <span v-if="loading" class="spinner"></span>{{ loading ? '刷新中…' : '刷新' }}
        </button>
        <button class="btn" :disabled="!pickedPending.length || !!busy || stale" @click="approvePicked">
          批准选中{{ pickedPending.length ? `（${pickedPending.length}）` : '' }}
        </button>
        <button class="btn" :disabled="!pickedApproved.length || !!busy || stale" @click="revokePicked">
          撤销选中{{ pickedApproved.length ? `（${pickedApproved.length}）` : '' }}
        </button>
        <button class="btn primary" :disabled="!pending || stale" :title="stale ? '服务端连不上，先恢复连接' : ''"
                @click="approveAll">全部批准</button>
      </div>
    </div>

    <div v-if="cacheInfo" class="notice info cache-line">
      <span>服务端连不上，下面是 {{ cacheInfo.updatedAt || '上次' }} 的缓存</span>
      <button class="link-btn" :disabled="loading" @click="load">重试</button>
    </div>
    <div v-if="loadErr && !cacheInfo" class="notice warn" style="margin-bottom:14px">
      管理接口没读到设备：{{ loadErr }}
    </div>

    <div class="card" style="padding:0;overflow:hidden">
      <table class="tbl">
        <thead>
          <tr>
            <th style="width:34px"><button type="button" class="pick-box" :class="{ on: allPicked }"
                  :disabled="!rows.length" :title="allPicked ? '取消全选' : '选中当前列表'"
                  @click="togglePickAll">
              <svg v-if="allPicked" viewBox="0 0 12 12" fill="none" stroke="#fff" stroke-width="1.8"
                   stroke-linecap="round" stroke-linejoin="round"><path d="M2.4 6.3 4.9 8.8 9.6 3.4"/></svg>
            </button></th>
            <th style="width:28%">网段</th>
            <th>设备</th>
            <th style="width:16%">成员</th>
            <th style="width:13%">状态</th>
            <th style="width:140px"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r.key" :class="{ 'picked-row': picked.has(r.key) }">
            <td>
              <button type="button" class="pick-box" :class="{ on: picked.has(r.key) }"
                      :title="picked.has(r.key) ? '取消选中' : '选中'" @click="togglePick(r.key)">
                <svg v-if="picked.has(r.key)" viewBox="0 0 12 12" fill="none" stroke="#fff" stroke-width="1.8"
                     stroke-linecap="round" stroke-linejoin="round"><path d="M2.4 6.3 4.9 8.8 9.6 3.4"/></svg>
              </button>
            </td>
            <td class="mono">
              {{ r.prefix }}
              <span v-if="isExit(r.prefix)" class="tag blue" style="margin-left:6px">出口节点</span>
            </td>
            <td>
              <div class="cell-main">{{ r.dev.name }}</div>
              <div class="cell-sub">{{ r.dev.online ? '在线' : '离线' }} · {{ r.dev.ip }}</div>
            </td>
            <td class="faint">{{ r.dev.user || '—' }}</td>
            <td>
              <span class="tag" :class="r.approved ? 'green' : 'amber'">{{ r.approved ? '已批准' : '待批准' }}</span>
            </td>
            <td>
              <div class="row" style="justify-content:flex-end">
                <button v-if="r.approved" class="btn ghost sm" :disabled="busy === r.dev.id || stale" @click="revoke(r)">撤销</button>
                <button v-else class="btn primary sm" :disabled="busy === r.dev.id || stale" @click="approve(r)">批准</button>
              </div>
            </td>
          </tr>
          <tr v-if="!rows.length">
            <td colspan="6" class="empty">
              没有设备通告子网路由。在路由器上跑一次
              <span class="mono">tailscale up --advertise-routes=192.168.1.0/24</span>
              ，这里就会出现
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="faint" style="font-size:10.5px;margin-top:10px;line-height:1.8">
      出口节点是 <span class="mono">0.0.0.0/0</span> 那条，批准之后别的设备才能拿它当出口。
      这里只改服务端记录，路由器有没有真的转发、本机有没有接受，去「连接诊断」看。
    </div>

    <ConfirmDialog v-if="confirmBox" v-bind="confirmBox" @close="closeConfirm" />
  </div>
</template>
