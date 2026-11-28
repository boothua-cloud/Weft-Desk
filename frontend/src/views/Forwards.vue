<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type ForwardRule, type ForwardView, type State } from '../api'
import Pick from '../components/Pick.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { useConfirm } from '../confirm'
import { reloadTick } from '../bus'

defineProps<{ state: State | null }>()
const emit = defineEmits(['notify'])

const view = ref<ForwardView | null>(null)
const loading = ref(false)
const busy = ref(false)
const drawer = ref(false)
const snippet = ref('')
const snippetOpen = ref(false)
const form = ref<ForwardRule>({ id: '', name: '', kind: 'ssh', target: '', targetPort: 22, localPort: 2222, enabled: true })
const { confirmBox, askConfirm, closeConfirm } = useConfirm()

const kinds = [
  { value: 'ssh', label: 'SSH' }, { value: 'mysql', label: 'MySQL' },
  { value: 'postgres', label: 'PostgreSQL' }, { value: 'redis', label: 'Redis' },
  { value: 'rdp', label: 'RDP' }, { value: 'custom', label: '自定义' },
]
const statusMap = computed(() => {
  const m: Record<string, any> = {}
  for (const s of view.value?.statuses ?? []) m[s.id] = s
  return m
})

async function load() {
  loading.value = true
  try { view.value = await api.forwards() }
  catch (err: any) { emit('notify', '加载失败：' + (err?.message ?? err)) }
  finally { loading.value = false }
}
onMounted(load)
watch(reloadTick, load)

function edit(r?: ForwardRule) {
  form.value = r ? { ...r } : { id: '', name: '', kind: 'ssh', target: '', targetPort: 22, localPort: 2222, enabled: true }
  drawer.value = true
}
async function submit() {
  if (!form.value.name.trim() || !form.value.target.trim()) { emit('notify', '名称与目标地址必填'); return }
  busy.value = true
  try {
    await api.saveForward(form.value)
    drawer.value = false
    await load()
    emit('notify', '已保存')
  } catch (err: any) { emit('notify', '保存失败：' + (err?.message ?? err)) }
  finally { busy.value = false }
}
async function act(fn: () => Promise<any>, msg: string) {
  busy.value = true
  try { await fn(); await load(); emit('notify', msg) }
  catch (err: any) { emit('notify', '操作失败：' + (err?.message ?? err)) }
  finally { busy.value = false }
}
function removeRule(r: ForwardRule) {
  askConfirm({
    title: '删除这条转发规则？',
    desc: `${r.name}\n本地 ${r.localPort} → ${r.target}:${r.targetPort}\n删除后监听端口立即释放，正在用的连接会断开。`,
    ok: '删除',
    action: () => act(() => api.deleteForward(r.id), '已删除'),
  })
}
async function showSnippet(r: ForwardRule) {
  try { snippet.value = await api.forwardSnippet(r.id); snippetOpen.value = true }
  catch (err: any) { emit('notify', '生成失败：' + (err?.message ?? err)) }
}
async function setBind(bind: string) {
  if (view.value?.bind === bind) return
  await act(() => api.setForwardBind(bind), bind === '0.0.0.0' ? '已对内网开放' : '已限制为本机')
}

async function setOptions(patch: { autoForward?: boolean; socksEnabled?: boolean; socksPort?: number }) {
  const v = view.value
  if (!v) return
  try {
    await api.setForwardOptions(patch.autoForward ?? v.autoForward, patch.socksEnabled ?? v.socksEnabled, patch.socksPort ?? v.socksPort)
    await load()
  } catch (err: any) { emit('notify', '设置失败：' + (err?.message ?? err)) }
}
function stateTag(st?: string) {
  if (st === 'listening') return 'green'
  if (st === 'error') return 'red'
  return ''
}
// 原始字节数看不出量级，直接给 4.6 MB 这种
function fmtBytes(n?: number): string {
  let v = n || 0
  if (v < 1024) return v + ' B'
  const units = ['KB', 'MB', 'GB', 'TB']
  let i = 0
  v /= 1024
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + ' ' + units[i]
}
</script>

<template>
  <div>
    <div class="spread" style="margin-bottom:14px">
      <div class="faint" style="font-size:11.5px">把本地端口映射到 tailnet 目标，外部工具连本机端口即可</div>
      <div class="row">
        <button class="btn" :disabled="loading" @click="load">
          <span v-if="loading" class="spinner"></span>{{ loading ? '刷新中…' : '刷新' }}
        </button>
        <button class="btn primary" @click="edit()">新建规则</button>
      </div>
    </div>

    <div class="card tight" style="margin-bottom:14px">
      <div class="row" style="gap:14px">
        <span style="font-size:11.5px">转发引擎</span>
        <button class="switch" :class="{ on: view?.power }" @click="act(() => api.setForwardPower(!view?.power), view?.power ? '已停止' : '已启动')"></button>
        <span class="faint">|</span>
        <span style="font-size:11.5px">SOCKS5</span>
        <button class="switch" :class="{ on: view?.socksEnabled }" @click="setOptions({ socksEnabled: !view?.socksEnabled })"></button>
        <input class="input" type="number" style="width:90px" :value="view?.socksPort" @change="(e: any) => setOptions({ socksPort: Number(e.target.value) || 1080 })" />
        <span class="faint">|</span>
        <span style="font-size:11.5px">连接后自动启动</span>
        <button class="switch" :class="{ on: view?.autoForward }" @click="setOptions({ autoForward: !view?.autoForward })"></button>
      </div>
      <div class="spread" style="margin-top:14px;padding-top:14px;border-top:1px solid var(--line-soft)">
        <div>
          <span style="font-size:11.5px">监听范围</span>
          <div class="faint" style="font-size:10.5px;margin-top:3px">
            {{ view?.bind === '0.0.0.0'
              ? '同网段的其他设备可以用本机 IP + 端口连进来；注意内网里谁都能访问'
              : '只有本机能连；想让同网段的设备访问就切到「同网段可达」' }}
          </div>
        </div>
        <div class="row" style="gap:6px">
          <button class="btn sm" :class="{ primary: view?.bind !== '0.0.0.0' }"
                  @click="setBind('127.0.0.1')">仅本机</button>
          <button class="btn sm" :class="{ primary: view?.bind === '0.0.0.0' }"
                  @click="setBind('0.0.0.0')">同网段可达</button>
        </div>
      </div>
    </div>

    <div v-if="view && view.bind !== '0.0.0.0'" class="notice info" style="margin-bottom:14px">
      现在只在 <span class="mono">127.0.0.1</span> 上监听，只有本机能连。要让同网段的其他设备通过 <span class="mono">本机IP:端口</span> 访问，
      把上面的「监听范围」切到「同网段可达」。
    </div>

    <div class="card" style="padding:0;overflow:hidden">
      <table class="tbl">
        <thead><tr><th style="width:16%">名称</th><th>类型</th><th>目标</th><th>本地</th><th>状态</th><th>流量</th><th style="width:180px"></th></tr></thead>
        <tbody>
          <tr v-for="r in view?.rules ?? []" :key="r.id">
            <td class="cell-main">{{ r.name }}</td>
            <td>{{ kinds.find(k => k.value === r.kind)?.label || r.kind }}</td>
            <td class="mono">{{ r.target }}:{{ r.targetPort }}</td>
            <td class="mono">{{ view?.bind === '0.0.0.0' ? '0.0.0.0' : '127.0.0.1' }}:{{ r.localPort }}</td>
            <td>
              <span class="tag" :class="stateTag(statusMap[r.id]?.state)">
                {{ statusMap[r.id]?.state === 'listening' ? '监听中' : statusMap[r.id]?.state === 'error' ? '错误' : (r.enabled ? '未启动' : '已禁用') }}
              </span>
            </td>
            <td class="mono">{{ fmtBytes(statusMap[r.id]?.bytes) }}</td>
            <td>
              <div class="row" style="gap:8px">
                <button class="btn ghost sm" @click="act(() => api.toggleForward(r.id, !r.enabled), r.enabled ? '已禁用' : '已启用')">{{ r.enabled ? '禁用' : '启用' }}</button>
                <button class="btn ghost sm" @click="showSnippet(r)">命令</button>
                <button class="btn ghost sm" @click="edit(r)">编辑</button>
                <button class="btn ghost sm" @click="removeRule(r)">删除</button>
              </div>
            </td>
          </tr>
          <tr v-if="!(view?.rules ?? []).length"><td colspan="7" class="empty">没有转发规则</td></tr>
        </tbody>
      </table>
    </div>

    <template v-if="drawer">
      <div class="mask" @click="drawer = false"></div>
      <div class="drawer">
        <div class="spread" style="margin-bottom:18px">
          <b style="font-size:14px">{{ form.id ? '编辑规则' : '新建规则' }}</b>
          <button class="btn ghost sm" @click="drawer = false">✕</button>
        </div>
        <div class="field"><label class="field-label">名称</label><input class="input" v-model="form.name" placeholder="生产数据库" /></div>
        <div class="field">
          <label class="field-label">类型</label>
          <Pick v-model="form.kind" :options="kinds" />
        </div>
        <div class="field"><label class="field-label">目标地址</label><input class="input mono" v-model="form.target" placeholder="100.64.0.3" /></div>
        <div class="grid2" style="gap:12px;margin-bottom:12px">
          <div><label class="field-label">目标端口</label><input class="input" type="number" v-model.number="form.targetPort" /></div>
          <div><label class="field-label">本地端口</label><input class="input" type="number" v-model.number="form.localPort" /></div>
        </div>
        <div class="spread card tight" style="margin-bottom:14px">
          <span style="font-size:11.5px">启用</span>
          <button class="switch" :class="{ on: form.enabled }" @click="form.enabled = !form.enabled"></button>
        </div>
        <button class="btn primary" style="width:100%;height:38px" :disabled="busy" @click="submit">保存</button>
      </div>
    </template>

    <template v-if="snippetOpen">
      <div class="mask" @click="snippetOpen = false"></div>
      <div class="drawer">
        <div class="spread" style="margin-bottom:14px">
          <b style="font-size:14px">连接命令</b>
          <button class="btn ghost sm" @click="snippetOpen = false">✕</button>
        </div>
        <pre class="codearea mono" style="min-height:auto;white-space:pre-wrap">{{ snippet }}</pre>
      </div>
    </template>

    <ConfirmDialog v-if="confirmBox" v-bind="confirmBox" @close="closeConfirm" />
  </div>
</template>
