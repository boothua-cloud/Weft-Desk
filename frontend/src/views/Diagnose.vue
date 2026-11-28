<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, type State, type PeerInfo, type DiagReport } from '../api'

const props = defineProps<{ state: State | null }>()
const emit = defineEmits(['notify', 'navigate'])

// 检查哪个端口。22 是最常见的「连不连得上」判据，可以改。
const port = ref(22)
const reports = ref<Record<string, DiagReport>>({})
const running = ref<Record<string, boolean>>({})
const failures = ref<Record<string, string>>({})

const peers = computed(() => props.state?.peers ?? [])
const onlineCount = computed(() => peers.value.filter((p) => p.online).length)
const busy = computed(() => Object.values(running.value).some(Boolean))

function pathOf(p: PeerInfo): string {
  return p.curAddr ? '直连' : 'DERP'
}

async function run(p: PeerInfo) {
  if (!p.ip) return
  const key = p.ip
  running.value = { ...running.value, [key]: true }
  failures.value = { ...failures.value, [key]: '' }
  try {
    const r = await api.diagnose(p.ip, port.value || 22)
    // hints 可能是 null（Go 的 nil 切片），模板里要当数组用，这里补上
    reports.value = { ...reports.value, [key]: { ...r, hints: r.hints ?? [] } }
    emit('notify', r.dialErr ? '检查完成：不通' : '检查完成：通路正常')
  } catch (err: any) {
    failures.value = { ...failures.value, [key]: String(err?.message ?? err) }
    emit('notify', '检查失败')
  } finally {
    running.value = { ...running.value, [key]: false }
  }
}

// 结论用颜色分：全通绿、部分通黄、不通红
function verdictTone(r: DiagReport): string {
  if (!r.dialErr) return r.pingLost === 3 ? 'warn' : 'ok'
  if (!r.online) return 'bad'
  return 'bad'
}

function fmtKb(n: number): string {
  if (!n) return '0'
  const units = ['B', 'KB', 'MB', 'GB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return (i === 0 ? v : v.toFixed(1)) + ' ' + units[i]
}
</script>

<template>
  <div class="page-flex diag-page">
    <div class="diag-fixed">
      <div class="spread diag-bar">
        <div class="diag-self">
          <div class="diag-self-item">
            <span>本机</span>
            <b class="mono">{{ state?.selfIP || '—' }}</b>
          </div>
          <i></i>
          <div class="diag-self-item">
            <span>对端</span>
            <b>{{ onlineCount }} / {{ peers.length }} 在线</b>
          </div>
          <i></i>
          <div class="diag-self-item">
            <span>子网路由</span>
            <b>{{ state?.routes?.length || 0 }} 条</b>
          </div>
        </div>
        <div class="row" style="gap:8px">
          <label class="diag-port">
            <span>检查端口</span>
            <input v-model.number="port" class="input mono" type="number" min="1" max="65535" style="width:74px" />
          </label>
          <button class="btn" :disabled="!state?.connected || busy" @click="peers.forEach(run)">
            <span v-if="busy" class="spinner"></span>{{ busy ? '检查中…' : '全部检查' }}
          </button>
        </div>
      </div>

      <div v-if="!state?.connected" class="notice warn diag-notice">
        还没连上，没有可测的链路。先去总览页连接。
      </div>
      <div v-else-if="state?.lastError" class="notice warn diag-notice">
        引擎报告：{{ state.lastError }}
      </div>
      <div v-if="state?.netErr" class="notice warn diag-notice">{{ state.netErr }}</div>
    </div>

    <div class="diag-scroll">
      <div v-if="!peers.length && state?.connected" class="card diag-empty">
        这段 tailnet 里还没有其他设备。对端上线后会自动出现在这里。
      </div>

      <div v-for="p in peers" :key="p.ip" class="card tight peer-card">
        <div class="peer-head">
          <div class="peer-id">
            <div class="peer-name">
              {{ p.name || p.ip }}
              <span class="tag" :class="p.online ? 'green' : ''">{{ p.online ? '在线' : '离线' }}</span>
            </div>
            <div class="peer-meta">
              <span class="mono">{{ p.ip }}</span>
              <template v-if="p.os"> · {{ p.os }}</template>
              <template v-if="p.lastSeen"> · 上次 {{ p.lastSeen }}</template>
            </div>
          </div>
          <div v-if="p.online" class="peer-path">
            <span class="path-pill" :class="p.curAddr ? 'direct' : 'relay'">{{ pathOf(p) }}</span>
            <span v-if="p.relay && !p.curAddr" class="faint relay-note">{{ p.relay }}</span>
          </div>
          <button class="btn sm" :disabled="running[p.ip] || !state?.connected" @click="run(p)">
            <span v-if="running[p.ip]" class="spinner"></span>{{ running[p.ip] ? '检查中…' : '检查' }}
          </button>
        </div>

        <div v-if="failures[p.ip]" class="notice warn" style="margin-top:10px">{{ failures[p.ip] }}</div>

        <div v-if="reports[p.ip]" class="report">
          <div class="verdict" :class="verdictTone(reports[p.ip])">
            <span class="verdict-dot"></span>
            <span>{{ reports[p.ip].verdict }}</span>
          </div>

          <div class="facts">
            <div class="fact">
              <span>路径</span>
              <b>{{ reports[p.ip].path }}<template v-if="reports[p.ip].relay"> · {{ reports[p.ip].relay }}</template></b>
            </div>
            <div class="fact">
              <span>握手</span>
              <b>{{ reports[p.ip].handshake }}</b>
            </div>
            <div class="fact">
              <span>探测延迟</span>
              <b>{{ reports[p.ip].pingMs >= 0 ? Math.round(reports[p.ip].pingMs) + ' ms' : '—' }}</b>
            </div>
            <div class="fact">
              <span>丢包</span>
              <b :class="{ bad: reports[p.ip].pingLost }">{{ reports[p.ip].pingLost }} / 3</b>
            </div>
            <div class="fact">
              <span>拨测</span>
              <b :class="{ bad: reports[p.ip].dialErr }">
                {{ reports[p.ip].dialErr ? '失败' : Math.round(reports[p.ip].dialMs) + ' ms' }}
              </b>
            </div>
            <div class="fact">
              <span>ACL</span>
              <b v-if="!reports[p.ip].aclChecked" class="faint">没读到</b>
              <b v-else :class="{ bad: !reports[p.ip].aclAllowed }">
                {{ reports[p.ip].aclAllowed ? '放行了这个端口' : '没有对应规则' }}
              </b>
            </div>
            <div class="fact">
              <span>收发</span>
              <b class="mono">{{ fmtKb(reports[p.ip].txBytes) }} / {{ fmtKb(reports[p.ip].rxBytes) }}</b>
            </div>
            <div v-if="reports[p.ip].version" class="fact">
              <span>对端版本</span>
              <b class="mono">{{ reports[p.ip].version }}</b>
            </div>
          </div>

          <ul v-if="reports[p.ip].hints?.length" class="hints">
            <li v-for="(h, i) in reports[p.ip].hints" :key="i">{{ h }}</li>
          </ul>

          <div class="report-foot">
            <span class="faint">检查于 {{ reports[p.ip].at.slice(11, 19) }}</span>
            <button v-if="state?.hasApiKey && reports[p.ip].aclChecked && !reports[p.ip].aclAllowed"
                    class="link-btn" @click="emit('navigate', 'acl')">去改 ACL</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.diag-fixed {
  flex-shrink: 0;
  padding-bottom: 10px;
}
.diag-bar {
  gap: 14px;
  flex-wrap: wrap;
  padding: 10px 14px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r);
}
.diag-self {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
}
.diag-self i {
  width: 1px;
  height: 18px;
  background: var(--line);
}
.diag-self-item {
  display: flex;
  align-items: baseline;
  gap: 6px;
  font-size: 11.5px;
}
.diag-self-item span { color: var(--faint); }
.diag-self-item b { font-weight: 600; }
.diag-port {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--faint);
}
.diag-notice { margin-top: 10px; }

.diag-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding-bottom: 20px;
}
.diag-empty { color: var(--muted); font-size: 12px; text-align: center; padding: 40px 0; }

.peer-card { margin-bottom: 10px; }
.peer-head {
  display: flex;
  align-items: center;
  gap: 14px;
}
.peer-id { flex: 1; min-width: 0; }
.peer-name {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
}
.peer-meta {
  margin-top: 4px;
  font-size: 11px;
  color: var(--faint);
}
.peer-path {
  display: flex;
  align-items: baseline;
  gap: 6px;
  flex-shrink: 0;
}
.path-pill {
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 10.5px;
  border: 1px solid var(--line);
  background: var(--surface-3);
  color: var(--muted);
}
.path-pill.direct {
  color: var(--brand);
  border-color: var(--brand-line);
  background: var(--ok-soft);
}
.path-pill.relay {
  color: var(--warn);
  border-color: color-mix(in srgb, var(--warn) 25%, transparent);
  background: var(--warn-soft);
}
.relay-note { font-size: 10.5px; }

.report {
  margin-top: 12px;
  padding-top: 11px;
  border-top: 1px dashed var(--line-soft);
}
.verdict {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  font-weight: 600;
}
.verdict-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--muted);
  flex: none;
}
.verdict.ok { color: var(--brand); }
.verdict.ok .verdict-dot { background: var(--brand); }
.verdict.warn { color: var(--warn); }
.verdict.warn .verdict-dot { background: var(--warn); }
.verdict.bad { color: var(--danger); }
.verdict.bad .verdict-dot { background: var(--danger); }

.facts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 20px;
  margin-top: 10px;
}
.fact {
  display: flex;
  align-items: baseline;
  gap: 6px;
  font-size: 11px;
}
.fact span { color: var(--faint); }
.fact b { font-weight: 500; }
.fact b.bad { color: var(--danger); }

.hints {
  margin: 11px 0 0;
  padding-left: 16px;
  font-size: 11.5px;
  color: var(--muted);
  line-height: 1.85;
}
.hints li::marker { color: var(--faint); }

.report-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 10px;
  font-size: 10.5px;
}
</style>
