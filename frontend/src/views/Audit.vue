<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type AuditEvent } from '../api'
import { reloadTick } from '../bus'
import Pick from '../components/Pick.vue'

const emit = defineEmits(['notify'])

const items = ref<AuditEvent[]>([])
const loading = ref(false)
const keyword = ref('')
const result = ref('')
const span = ref('7')
const openRow = ref<number | null>(null)

const resultOptions = [
  { value: '', label: '全部结果' },
  { value: 'ok', label: '成功' },
  { value: 'fail', label: '失败' },
]
const spanOptions = [
  { value: '1', label: '今天' },
  { value: '7', label: '近 7 天' },
  { value: '30', label: '近 30 天' },
  { value: '0', label: '全部' },
]

// 记录里可能有服务端地址、设备名，关键字按整行匹配，省得记不住该搜哪一列
const shown = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  const since = cutoff()
  return items.value
    .map((e, i) => ({ e, i }))
    .filter(({ e }) => {
      if (result.value && e.result !== result.value) return false
      if (since && Date.parse(e.time) < since) return false
      if (!kw) return true
      return `${e.action} ${e.target || e.detail} ${e.profile} ${e.error}`.toLowerCase().includes(kw)
    })
})

function cutoff(): number {
  const days = Number(span.value)
  if (!days) return 0
  const d = new Date()
  d.setHours(0, 0, 0, 0)
  // 「今天」按自然日算，近 7 天算上今天往前 6 天
  d.setDate(d.getDate() - (days - 1))
  return d.getTime()
}

// 后端时间统一 RFC3339，显示成「11-28 15:04:22」，跨年的补上年
function fmtWhen(raw: string): string {
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  const p = (n: number) => String(n).padStart(2, '0')
  const md = `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  return d.getFullYear() === new Date().getFullYear() ? md : `${d.getFullYear()}-${md}`
}

async function load() {
  loading.value = true
  const t0 = Date.now()
  try {
    items.value = await api.audit()
  } catch (err: any) {
    emit('notify', '读取操作记录失败：' + (err?.message ?? err))
  } finally {
    const rest = 300 - (Date.now() - t0)
    if (rest > 0) await new Promise((r) => setTimeout(r, rest))
    loading.value = false
  }
}
onMounted(load)
watch(reloadTick, load)

const failCount = computed(() => items.value.filter((e) => e.result === 'fail').length)

function toggleRow(i: number) {
  openRow.value = openRow.value === i ? null : i
}

// CSV 用 Excel 直接能开的写法：UTF-8 BOM + 引号转义。
// 不加 BOM 的话 Windows 上中文会是乱码。
function csvCell(v: string): string {
  return '"' + (v || '').replace(/"/g, '""') + '"'
}

async function exportCSV() {
  const rows = [['时间', '动作', '对象', '服务端', '结果', '错误']]
  for (const { e } of shown.value) {
    rows.push([fmtWhen(e.time), e.action, e.target || e.detail, e.profile, e.result === 'fail' ? '失败' : '成功', e.error])
  }
  const body = '\ufeff' + rows.map((r) => r.map(csvCell).join(',')).join('\r\n') + '\r\n'
  const name = `weft-audit-${new Date().toISOString().slice(0, 10)}.csv`
  try {
    const path = await api.exportText(name, body)
    emit('notify', path ? `已导出到 ${path}` : '已取消')
  } catch (err: any) {
    emit('notify', '导出失败：' + (err?.message ?? err))
  }
}
</script>

<template>
  <div class="page-flex">
    <div class="spread" style="margin-bottom:14px;flex-shrink:0">
      <div class="row" style="gap:10px">
        <span class="faint" style="font-size:11.5px">
          共 {{ items.length }} 条{{ failCount ? `，其中 ${failCount} 条失败` : '' }}
        </span>
      </div>
      <div class="row">
        <button class="btn" :disabled="loading" @click="load">
          <span v-if="loading" class="spinner"></span>{{ loading ? '刷新中…' : '刷新' }}
        </button>
        <button class="btn primary" :disabled="!shown.length" @click="exportCSV">导出 CSV</button>
      </div>
    </div>

    <div class="filter-bar">
      <input class="input" style="width:260px" v-model="keyword"
             placeholder="搜索动作 / 对象 / 服务端 / 错误…" />
      <div style="width:132px"><Pick v-model="result" :options="resultOptions" /></div>
      <div style="width:132px"><Pick v-model="span" :options="spanOptions" /></div>
      <button v-if="keyword || result || span !== '7'" class="btn ghost sm"
              @click="keyword = ''; result = ''; span = '7'">清空条件</button>
      <span class="faint" style="margin-left:auto;font-size:11px">只留最近 2000 条</span>
    </div>

    <div class="card table-scroll" style="padding:0;position:relative">
      <div v-if="loading" class="load-overlay"><span class="spinner"></span>加载中…</div>
      <table class="tbl">
        <thead><tr>
          <th style="width:130px">时间</th>
          <th style="width:150px">动作</th>
          <th>对象</th>
          <th style="width:150px">服务端</th>
          <th style="width:70px">结果</th>
        </tr></thead>
        <tbody>
          <template v-for="{ e, i } in shown" :key="i">
            <tr :class="{ 'fail-row': e.result === 'fail', clickable: !!e.error }"
                @click="e.error && toggleRow(i)">
              <td class="mono" style="font-size:11px">{{ fmtWhen(e.time) }}</td>
              <td>{{ e.action }}</td>
              <td>
                <div class="cell-main" style="font-size:11.5px">{{ e.target || e.detail || '—' }}</div>
                <div v-if="e.error && openRow !== i" class="cell-sub truncate">{{ e.error }}</div>
              </td>
              <td class="faint" style="font-size:11px">{{ e.profile || '—' }}</td>
              <td>
                <span class="tag" :class="e.result === 'fail' ? 'red' : 'green'">
                  {{ e.result === 'fail' ? '失败' : '成功' }}
                </span>
              </td>
            </tr>
            <tr v-if="e.error && openRow === i">
              <td></td>
              <td colspan="4">
                <div class="err-box">{{ e.error }}</div>
              </td>
            </tr>
          </template>
          <tr v-if="!shown.length">
            <td colspan="5">
              <div class="empty-state">
                <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor"
                     stroke-width="1.4" stroke-linecap="round"><path d="M4 5h16M4 10h16M4 15h10M4 20h6"/></svg>
                <b>{{ items.length ? '没有符合条件的记录' : '还没有操作记录' }}</b>
                <span>{{ items.length ? '换个关键字或时间范围试试' : '改配置、动设备、存策略这些操作都会被记下来' }}</span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.clickable { cursor: pointer; }
.fail-row td { background: color-mix(in srgb, var(--danger) 4%, transparent); }
.truncate {
  max-width: 620px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--danger);
}
.err-box {
  margin: 2px 0 6px;
  padding: 8px 10px;
  border: 1px solid color-mix(in srgb, var(--danger) 22%, transparent);
  border-radius: 7px;
  background: var(--danger-soft);
  color: var(--danger);
  font-family: var(--mono);
  font-size: 10.5px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
