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
  } catch (err: any) {
    emit('notify', '读取设备失败：' + (err?.message ?? err))
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(reloadTick, load)

async function setRoutes(d: Device, list: string[], msg: string) {
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
        <button class="btn primary" :disabled="!pending" @click="approveAll">全部批准</button>
      </div>
    </div>

    <div v-if="loadErr" class="notice warn" style="margin-bottom:14px">
      管理接口没读到设备：{{ loadErr }}
    </div>

    <div class="card" style="padding:0;overflow:hidden">
      <table class="tbl">
        <thead>
          <tr>
            <th style="width:28%">网段</th>
            <th>设备</th>
            <th style="width:16%">成员</th>
            <th style="width:13%">状态</th>
            <th style="width:140px"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r.key">
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
                <button v-if="r.approved" class="btn ghost sm" :disabled="busy === r.dev.id" @click="revoke(r)">撤销</button>
                <button v-else class="btn primary sm" :disabled="busy === r.dev.id" @click="approve(r)">批准</button>
              </div>
            </td>
          </tr>
          <tr v-if="!rows.length">
            <td colspan="5" class="empty">
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
