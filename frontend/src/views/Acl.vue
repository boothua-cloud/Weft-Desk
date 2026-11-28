<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { api, type State, type PolicyDoc, type Device, type User, type PolicyVersion } from '../api'
import { reloadTick } from '../bus'
import ChipsInput from '../components/ChipsInput.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import PermissionMatrix from '../components/PermissionMatrix.vue'
import PortField from '../components/PortField.vue'
import TokenField from '../components/TokenField.vue'
import { useConfirm } from '../confirm'
import { diffStat, keepContext, lineDiff, type DiffLine } from '../diff'
import {
  auditAcl,
  explainPolicyError,
  flattenDst,
  groupDst,
  groupKey,
  parseAcl,
  policyKeyHints,
  ruleSummary,
  serializeAcl,
  tagKey,
  tokenLabel,
  userToken,
  validateAcl,
  type AccessPeer,
  type AclForm,
  type AclIssue,
  type DstGroup,
  type Risk,
} from '../acl'

const props = defineProps<{ state: State | null }>()
const emit = defineEmits(['notify'])
const { confirmBox, askConfirm, closeConfirm } = useConfirm()

const doc = ref<PolicyDoc>({ content: '', updatedAt: '' })
// 源码模式的缓冲区；表单模式以 form 为准
const text = ref('')
const form = ref<AclForm | null>(null)
const parseError = ref('')
const hadComments = ref(false)
const mode = ref<'form' | 'scope' | 'source'>('scope')
// 矩阵默认收成一条结论条：编辑区优先，需要看结果时点一下。
// 有「整行 / 整列完全不通」这种写漏时自动展开一次，之后用户自己说了算。
const matrixMin = ref(true)
const matrixAutoOpened = ref(false)
const matrixSum = ref<{
  rows: number; cols: number; reachable: number
  deadRows: number; deadCols: number; openAll: boolean
} | null>(null)
watch(matrixSum, (v) => {
  if (!v || !v.rows || !v.cols) return
  // 只有「某个成员哪儿都去不了」才算真写漏；某台设备没人能访问，多半是故意的隔离，
  // 不当异常处理，否则一条 tag 规则就会把整个矩阵弹出来喊「5 列完全不通」。
  if (window.innerWidth < 1100) return
  if (matrixMin.value && !matrixAutoOpened.value && v.deadRows > 0) {
    matrixMin.value = false
    matrixAutoOpened.value = true
  }
})

// 校验结果默认只露一条，多了再展开
const issuesOpen = ref(false)
const baseline = ref('')
const check = ref<{ kind: 'ok' | 'warn' | 'err'; text: string } | null>(null)
const busy = ref(false)
const loading = ref(false)
const checking = ref(false)
// 服务端 policy.mode: file 时接口只能读不能写。存一次就记住，
// 免得用户反复点「保存并应用」看同一个 500。
const policyLocked = ref(false)

// 每条规则的「目标分组」编辑态。dst 里带端口，界面上要按端口归组才好读，
// 所以单独存一份，改完立刻写回 r.dst，保证序列化时只有一个真相。
const dstEdits = ref<DstGroup[][]>([])

const devices = ref<Device[]>([])
const users = ref<User[]>([])

const readOnly = computed(() => props.state?.capabilities?.canManageACL === false)
// 策略里 hosts 定义的别名。dst 只能写「服务端认得的名字」，
// 别名和真实设备名一起作为白名单参与校验，也直接列进目标选择器
const hostAliases = computed<string[]>(() =>
  (form.value?.hosts ?? []).map((h) => h.name.trim()).filter(Boolean),
)

const issues = computed<AclIssue[]>(() =>
  form.value
    ? validateAcl(form.value, {
        hosts: hostAliases.value,
        devices: peers.value.map((p) => p.name),
        members: memberNames.value,
        routes: subnetRoutes.value.map((r) => r.cidr),
      })
    : [],
)
const errorCount = computed(() => issues.value.filter((i) => i.level === 'error').length)
// 首条优先取错误，免得「有 1 条错误」却在摘要行里先看到一条提示
const issuesLead = computed(
  () => issues.value.find((i) => i.level === 'error') ?? issues.value[0],
)
const formMode = computed(() => mode.value !== 'source')

// 风险体检：只报「这条规则等于没设防」，和上面的语法校验分开显示。
// 源码模式不解析表单，这里也不算，避免拿着旧 form 报错位。
const risks = computed<Risk[]>(() => (formMode.value && form.value ? auditAcl(form.value) : []))
const highRisks = computed(() => risks.value.filter((r) => r.level === 'high').length)
const riskOpen = ref(false)
const currentText = computed(() => (formMode.value && form.value ? serializeAcl(form.value) : text.value))
const dirty = computed(() => currentText.value.trim() !== baseline.value.trim())

const updatedLabel = computed(() => {
  const raw = doc.value.updatedAt
  if (!raw) return '—'
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
})

function splitTags(s: string): string[] {
  return (s || '').split(/[,\s]+/).map((x) => x.trim()).filter(Boolean)
}

const peers = computed<AccessPeer[]>(() =>
  devices.value.map((d) => ({
    name: d.name || d.hostname || d.ip,
    hostname: d.hostname,
    user: d.user,
    tags: splitTags(d.tags),
    ip: d.ip || d.ips?.[0] || '',
    online: d.online,
    // 生效中的路由优先；还没生效但有批准的也列出来，方便先把规则写好
    routes: splitTags(d.subnetRoutes || d.approvedRoutes || ''),
  })),
)

// 子网路由在 ACL 里必须按网段写（写路由器名字只能到路由器本身），
// 所以目标选择器单独列一组，界面上一眼能看到「这台设备背后还有哪些网段」
const subnetRoutes = computed<{ cidr: string; via: string }[]>(() => {
  const out: { cidr: string; via: string }[] = []
  const seen = new Set<string>()
  for (const p of peers.value) {
    for (const r of p.routes ?? []) {
      if (!r || seen.has(r)) continue
      seen.add(r)
      out.push({ cidr: r, via: p.name })
    }
  }
  return out
})

const memberNames = computed<string[]>(() => {
  // 必须用 headscale 里的用户名（rawName）。name 是 displayName，只适合展示：
  // 把它写进策略，服务端会当成一个不存在的用户。
  const fromUsers = users.value.map((u) => u.rawName || u.name).filter(Boolean)
  if (fromUsers.length) return fromUsers
  return [...new Set(peers.value.map((p) => p.user).filter(Boolean))]
})

// 策略里的人是「用户名@」，界面上的匹配、chip、矩阵都按这个形式走
const memberTokens = computed(() => memberNames.value.map(userToken))

// 自动批准能写标签、组、成员三种身份，补全里都给出来
const approverSuggestions = computed(() => [
  ...(form.value?.tagOwners ?? []).map((t) => tagKey(t.name)).filter(Boolean),
  ...(form.value?.groups ?? []).map((g) => groupKey(g.name)).filter(Boolean),
  ...memberTokens.value,
])

function syncEdits() {
  dstEdits.value = (form.value?.acls ?? []).map((r) => groupDst(r.dst))
}

function parseInto(t: string): boolean {
  const r = parseAcl(t)
  if (!r.ok) {
    form.value = null
    parseError.value = r.error
    dstEdits.value = []
    return false
  }
  form.value = r.form
  parseError.value = ''
  hadComments.value = r.form.hadComments
  syncEdits()
  return true
}

async function load() {
  loading.value = true
  const t0 = Date.now()
  try {
    const [p, dv, us] = await Promise.all([
      api.policy(),
      api.devices().catch(() => null),
      api.users().catch(() => null),
    ])
    doc.value = p
    devices.value = dv?.items ?? []
    users.value = us?.items ?? []
    text.value = doc.value.content
    if (parseInto(doc.value.content)) {
      // 不强行切回「规则」：用户停在哪个页签就留在那儿，从源码态加载成功才回到表单
      if (mode.value === 'source') mode.value = 'scope'
      // 以归一化后的表单作为「已保存」基准，带注释的原文不算改动
      baseline.value = serializeAcl(form.value as AclForm)
    } else {
      mode.value = 'source'
      baseline.value = doc.value.content
    }
    check.value = null
    matrixAutoOpened.value = false
  } catch (err: any) {
    emit('notify', '读取 ACL 失败：' + (err?.message ?? err))
  } finally {
    const rest = 320 - (Date.now() - t0)
    if (rest > 0) await new Promise((r) => setTimeout(r, rest))
    loading.value = false
  }
}
onMounted(load)
watch(reloadTick, load)

// 风险条目点一下跳到那条规则：先切回规则页签，再滚到卡片并闪一下边
function jumpRule(i: number) {
  if (mode.value !== 'form') setMode('form')
  nextTick(() => {
    const el = document.getElementById('acl-rule-' + i)
    if (!el) return
    el.scrollIntoView({ behavior: 'smooth', block: 'center' })
    el.classList.add('risk-flash')
    window.setTimeout(() => el.classList.remove('risk-flash'), 1400)
  })
}

// 规则 / 组与标签 共用同一份表单数据，源码是另一份缓冲区，切换时互相搬运。
// 从源码往表单切必须先解析成功，否则界面会拿旧表单去覆盖用户刚写的内容。
function setMode(m: 'form' | 'scope' | 'source') {
  if (m === mode.value) return
  check.value = null
  if (mode.value === 'source') {
    if (!parseInto(text.value)) {
      emit('notify', '这份策略没法用表单打开：' + parseError.value)
      return
    }
    mode.value = m
    return
  }
  if (m === 'source' && form.value) text.value = serializeAcl(form.value)
  mode.value = m
}

function formatSource() {
  const r = parseAcl(text.value)
  if (!r.ok) {
    check.value = { kind: 'err', text: '格式化失败：' + r.error }
    return
  }
  text.value = serializeAcl(r.form)
  check.value = { kind: 'warn', text: '已格式化。注释和尾逗号没了，保存过去是纯 JSON。' }
}

async function validate() {
  const payload = currentText.value
  const local = form.value ? validateAcl(form.value) : []
  const localErr = local.filter((i) => i.level === 'error').length
  // 源码模式不替用户改写内容，但可以把「服务端肯定不认」的键名提前说出来
  const hints = mode.value === 'source' ? policyKeyHints(payload) : []
  checking.value = true
  try {
    await api.checkPolicy(payload)
    if (hints.length) {
      check.value = { kind: 'warn', text: `服务端过了，但源码写法不规范：${hints[0]}。点「格式化」修一下。` }
    } else if (localErr) {
      check.value = { kind: 'warn', text: `服务端过了，本地还有 ${localErr} 处错误，多为组/标签引用对不上。` }
    } else if (local.length) {
      check.value = { kind: 'warn', text: `服务端过了，本地有 ${local.length} 条提示，见下方。` }
    } else {
      check.value = { kind: 'ok', text: '服务端校验通过，可以保存。' }
    }
    emit('notify', '校验通过')
  } catch (err: any) {
    const raw = String(err?.message ?? err)
    const why = explainPolicyError(raw)
    const hint = hints.length ? `（源码里 ${hints[0]}，点「格式化」可自动修正）` : ''
    check.value = { kind: 'err', text: '服务端校验未通过：' + (why ? `${why}（服务端原文：${raw}）` : raw) + hint }
    emit('notify', '校验未通过')
  } finally {
    checking.value = false
  }
}

async function save() {
  if (readOnly.value) return
  if (formMode.value) {
    if (!form.value) return
    if (errorCount.value) {
      check.value = { kind: 'err', text: `还有 ${errorCount.value} 处错误，改完再保存。` }
      emit('notify', '规则有错误，未保存')
      return
    }
  }
  // 风险不挡保存，但要人在按下之前知道自己在放什么过去
  if (risks.value.length) {
    const high = highRisks.value
    askConfirm({
      title: `有 ${risks.value.length} 条风险规则`,
      desc: `${risks.value[0].title}。${high ? `其中 ${high} 条是高危。` : ''}保存后立即生效。`,
      ok: '仍然保存',
      tone: 'primary',
      action: () => doSave(),
    })
    return
  }
  await doSave()
}

async function doSave() {
  const payload = currentText.value
  busy.value = true
  const hints = mode.value === 'source' ? policyKeyHints(payload) : []
  try {
    const lost = hadComments.value
    await api.savePolicy(payload)
    policyLocked.value = false
    emit('notify', lost ? '策略已保存，HuJSON 注释已移除' : '策略已保存并应用')
    await load()
    check.value = { kind: 'ok', text: '已保存并应用。' }
  } catch (err: any) {
    const raw = String(err?.message ?? err)
    const why = explainPolicyError(raw)
    if (raw.includes("update is disabled for modes other than 'database'")) policyLocked.value = true
    const hint = hints.length ? `（源码里 ${hints[0]}，点「格式化」可自动修正）` : ''
    check.value = { kind: 'err', text: '保存失败：' + (why ? `${why}（服务端原文：${raw}）` : raw) + hint }
    emit('notify', '保存失败')
  } finally {
    busy.value = false
  }
}

// ---------- 策略历史 ----------

const histOpen = ref(false)
const hist = ref<PolicyVersion[]>([])
const histPick = ref<PolicyVersion | null>(null)
const histDiff = ref<DiffLine[]>([])
const histBusy = ref(false)
const histErr = ref('')

const histStat = computed(() => diffStat(histDiff.value))

function fmtWhen(raw: string): string {
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function fmtSize(n: number): string {
  return n >= 1024 ? (n / 1024).toFixed(1) + ' KB' : `${n} B`
}

async function openHistory() {
  histOpen.value = true
  histErr.value = ''
  histPick.value = null
  histDiff.value = []
  try {
    hist.value = await api.policyHistory()
  } catch (err: any) {
    histErr.value = '读取历史失败：' + (err?.message ?? err)
  }
}

async function pickVersion(v: PolicyVersion) {
  histPick.value = v
  histDiff.value = []
  try {
    const doc = await api.policyVersion(v.id)
    // 方向是「线上当前 → 这一版」：绿色是回滚后会出现的行，红色是会消失的行
    histDiff.value = keepContext(lineDiff(baseline.value || currentText.value, doc.content))
  } catch (err: any) {
    histErr.value = '读取这一版失败：' + (err?.message ?? err)
  }
}

function rollback() {
  const v = histPick.value
  if (!v) return
  askConfirm({
    title: '回到这一版？',
    desc: `${fmtWhen(v.at)} 的策略会覆盖线上当前策略。现在这版也会进历史，反悔了还能回来。`,
    ok: '回滚',
    action: async () => {
      histBusy.value = true
      try {
        await api.restorePolicy(v.id)
        emit('notify', '已回滚到 ' + fmtWhen(v.at))
        histOpen.value = false
        await load()
      } catch (err: any) {
        histErr.value = '回滚失败：' + (err?.message ?? err)
      } finally {
        histBusy.value = false
      }
    },
  })
}

// ---------- 规则操作 ----------

function commitDst(i: number) {
  const f = form.value
  if (!f || !f.acls[i]) return
  f.acls[i].dst = flattenDst(dstEdits.value[i] ?? [])
}

function addRule() {
  const f = form.value
  if (!f) return
  f.acls.push({ action: 'accept', src: [], dst: ['*:22'], extra: {} })
  dstEdits.value.push([{ hosts: ['*'], ports: ['22'] }])
}

function removeRule(i: number) {
  const f = form.value
  if (!f) return
  f.acls.splice(i, 1)
  dstEdits.value.splice(i, 1)
}

function setDstHosts(i: number, gi: number, hosts: string[]) {
  const g = dstEdits.value[i]
  if (!g || !g[gi]) return
  g[gi].hosts = hosts
  commitDst(i)
}

function setDstPorts(i: number, gi: number, ports: string[]) {
  const g = dstEdits.value[i]
  if (!g || !g[gi]) return
  g[gi].ports = ports
  commitDst(i)
}

function addDstGroup(i: number) {
  const g = dstEdits.value[i]
  if (!g) return
  g.push({ hosts: [], ports: ['22'] })
}

function removeDstGroup(i: number, gi: number) {
  const g = dstEdits.value[i]
  if (!g) return
  g.splice(gi, 1)
  commitDst(i)
}

function onCreate(kind: 'group' | 'tag', name: string) {
  const f = form.value
  if (!f) return
  if (kind === 'group') f.groups.push({ name, values: [] })
  else f.tagOwners.push({ name, values: [] })
  emit('notify', `${name} 已创建，去「用户组与标签」加成员才生效`)
}

// ---------- 模板 ----------

function applyTemplate(kind: 'open' | 'self' | 'admin') {
  const f = form.value
  if (!f) return
  if (kind === 'open') {
    f.acls = []
    f.hasAclsKey = false
  } else if (kind === 'self') {
    f.acls = [{ action: 'accept', src: ['autogroup:member'], dst: ['autogroup:self:*'], extra: {} }]
    f.hasAclsKey = true
  } else {
    f.acls = [
      { action: 'accept', src: ['autogroup:admin'], dst: ['*:*'], extra: {} },
      { action: 'accept', src: ['autogroup:member'], dst: ['autogroup:self:*'], extra: {} },
    ]
    f.hasAclsKey = true
  }
  syncEdits()
  emit('notify', '模板已套用，检查后保存')
}
</script>

<template>
  <div class="page-flex acl-page">
    <!-- 固定区：页签、统计、校验结果都在这里，滚动只发生在下面 -->
    <div class="acl-fixed">
      <div class="spread acl-bar" style="gap:12px;flex-wrap:wrap">
        <div class="row" style="gap:9px;min-width:0">
          <div class="seg">
            <button type="button" :class="{ on: mode === 'scope' }" @click="setMode('scope')">用户组与标签</button>
            <button type="button" :class="{ on: mode === 'form' }" @click="setMode('form')">规则</button>
            <button type="button" :class="{ on: mode === 'source' }" @click="setMode('source')">源码</button>
          </div>
          <span v-if="dirty" class="tag amber" style="margin:0">未保存</span>
          <span class="faint acl-updated">更新于 {{ updatedLabel }}</span>
        </div>
        <!-- 统计原来独占一行，压进工具栏；窄窗口自动隐去，信息不丢（各页自己也有计数） -->
        <div v-if="formMode && form" class="acl-stats">
          <span><b>{{ form.acls.length }}</b> 规则</span>
          <i></i>
          <span><b>{{ form.groups.length }}</b> 组</span>
          <i></i>
          <span><b>{{ form.tagOwners.length }}</b> 标签</span>
          <i></i>
          <span><b>{{ peers.length }}</b> 设备</span>
        </div>
        <div class="row">
          <span v-if="loading" class="load-hint" style="margin:0 8px 0 0">
            <span class="spinner"></span>读取中…
          </span>
          <button class="btn" :disabled="loading" @click="openHistory">历史</button>
          <button class="btn" :disabled="loading" @click="load">
            <span v-if="loading" class="spinner"></span>{{ loading ? '刷新中…' : '刷新' }}
          </button>
          <button v-if="mode === 'source'" class="btn" :disabled="checking" @click="formatSource">格式化</button>
          <button class="btn" :disabled="checking || loading" @click="validate">
            <span v-if="checking" class="spinner"></span>{{ checking ? '校验中…' : '校验' }}
          </button>
          <button class="btn primary" :disabled="busy || readOnly || !dirty" @click="save">
            {{ busy ? '保存中…' : '保存并应用' }}
          </button>
        </div>
      </div>

      <div v-if="policyLocked" class="notice warn acl-notice">
        这台 headscale 的策略存在文件里（<code>policy.mode: file</code>），接口只能读，这里保存不了。
        要在这里改：把 config.yaml 的 <code>policy.mode</code> 改成 <code>database</code> 并重启，再用
        <code>headscale policy set -f 原文件</code> 导一次。不改配置也行，直接改服务器上的策略文件，然后
        <code>systemctl reload headscale</code>。
      </div>

      <!-- 校验/保存的结果也要在固定区里说，滚到哪儿都看得见 -->
      <div v-if="issues.length" class="notice acl-notice slim" :class="errorCount ? 'warn' : 'info'">
        <div class="slim-head">
          <span class="slim-tag">{{ errorCount ? `${errorCount} 条错误` : `${issues.length} 条提示` }}</span>
          <span class="slim-text">{{ issuesLead?.text }}</span>
          <button v-if="issues.length > 1" type="button" class="link-btn slim-toggle"
                  @click="issuesOpen = !issuesOpen">
            {{ issuesOpen ? '收起' : `还有 ${issues.length - 1} 条` }}
          </button>
        </div>
        <div v-if="issuesOpen && issues.length > 1" class="slim-list">
          <div v-for="(it, i) in issues.slice(1)" :key="i" :style="it.level === 'error' ? 'color:var(--danger)' : ''">
            {{ it.level === 'error' ? '错误' : '提示' }} · {{ it.text }}
          </div>
        </div>
      </div>
      <!-- 风险体检：规则存得下去，但等于没设防。和上面的语法校验分开说 -->
      <div v-if="risks.length" class="notice acl-notice slim risk">
        <div class="slim-head">
          <span class="slim-tag risk-tag">{{ highRisks ? `${highRisks} 条高危` : `${risks.length} 条风险` }}</span>
          <span class="slim-text">
            {{ risks[0].title }}<template v-if="risks.length > 1">，还有 {{ risks.length - 1 }} 条</template>
          </span>
          <button type="button" class="link-btn slim-toggle" @click="riskOpen = !riskOpen">
            {{ riskOpen ? '收起' : '逐条看' }}
          </button>
        </div>
        <div v-if="riskOpen" class="slim-list risk-list">
          <button v-for="(rk, i) in risks" :key="i" type="button" class="risk-row" @click="jumpRule(rk.rule)">
            <span class="risk-dot" :class="rk.level"></span>
            <span class="risk-body">
              <span class="risk-line">
                <b class="risk-title">{{ rk.title }}</b>
                <i class="risk-go">第 {{ rk.rule + 1 }} 条</i>
              </span>
              <span class="risk-detail">{{ rk.detail }}</span>
            </span>
          </button>
        </div>
      </div>
      <div v-if="check" class="notice acl-notice" :class="check.kind === 'ok' ? 'info' : 'warn'">
        {{ check.text }}
      </div>
      <div v-if="hadComments" class="notice warn acl-notice">
        原策略带 HuJSON 注释，在这里保存会把注释去掉。介意就留在源码模式改。
      </div>
      <div v-if="readOnly" class="notice warn acl-notice">
        服务端不让改策略，这里只能看。
      </div>
    </div>

    <!-- 规则页：上面写规则（自己滚），下面矩阵常驻，改完立刻能看到结果 -->
    <div v-if="mode === 'form' && form" class="acl-split">
      <div class="acl-rules-pane">
        <div class="pane-head">
          <h3 class="sec">规则 <span class="acl-count">{{ form.acls.length }}</span></h3>
          <span class="pane-head-note">规则里写组或标签，加人不用改规则</span>
        </div>

        <section v-if="!form.acls.length" class="acl-card start">
          <div class="acl-head">
            <div>
              <h3 class="sec">
                {{ form.hasAclsKey ? '当前谁都不通' : '当前全部互通' }}
              </h3>
              <div class="acl-sub">
                {{ form.hasAclsKey
                  ? '规则是空的，等于谁都进不来。选个起点，再按需放宽。'
                  : '没写规则时 headscale 放行全部设备。要收紧就选个起点。' }}
              </div>
            </div>
          </div>
          <div class="start-grid">
            <button type="button" class="start-card" :disabled="readOnly" @click="applyTemplate('self')">
              <b>只访问自己的设备</b>
              <span>只能连自己名下的设备，彼此隔离。适合个人多设备。</span>
            </button>
            <button type="button" class="start-card" :disabled="readOnly" @click="applyTemplate('admin')">
              <b>管理员全通，其他人各管各的</b>
              <span>管理员能访问全部设备，成员只能访问自己的。</span>
            </button>
            <button type="button" class="start-card" :disabled="readOnly" @click="applyTemplate('open')">
              <b>保持全部互通</b>
              <span>不写规则，所有人互通。只适合完全信得过的内网。</span>
            </button>
          </div>
        </section>

        <section v-for="(r, i) in form.acls" :key="i" :id="'acl-rule-' + i" class="acl-card rule">
          <div class="rule-head">
            <span class="rule-no">规则 {{ i + 1 }}</span>
            <span class="rule-sum">{{ ruleSummary(r) }}</span>
            <button class="btn sm ghost rule-del" :disabled="readOnly" @click="removeRule(i)">删除</button>
          </div>

          <div class="rule-row">
            <span class="rule-label">谁可以访问</span>
            <TokenField
              mode="src"
              :model-value="r.src"
              :groups="form.groups"
              :tags="form.tagOwners"
              :peers="peers"
              :members="memberTokens"
              :disabled="readOnly"
              @update:model-value="r.src = $event"
              @create="onCreate"
            />
          </div>

          <div class="rule-row">
            <span class="rule-label">能访问哪些设备</span>
            <div class="dst-list">
              <div v-for="(g, gi) in dstEdits[i]" :key="gi" class="dst-line">
                <TokenField
                  mode="dst"
                  :model-value="g.hosts"
                  :groups="form.groups"
                  :tags="form.tagOwners"
                  :peers="peers"
                  :members="memberTokens"
                  :hosts="hostAliases"
                  :routes="subnetRoutes"
                  :disabled="readOnly"
                  @update:model-value="setDstHosts(i, gi, $event)"
                />
                <span class="dst-sep">的</span>
                <PortField
                  :model-value="g.ports"
                  :disabled="readOnly"
                  @update:model-value="setDstPorts(i, gi, $event)"
                />
                <button
                  v-if="dstEdits[i].length > 1"
                  class="icon-del"
                  title="删掉这组目标"
                  :disabled="readOnly"
                  @click="removeDstGroup(i, gi)"
                >×</button>
              </div>
              <button v-if="!readOnly" type="button" class="link-btn" @click="addDstGroup(i)">
                + 再加一组目标
              </button>
            </div>
          </div>

          <div v-if="'ip' in r.extra" class="acl-note">
            这条规则用 <code>ip</code> 字段限定网段，界面不改它，保存时原样写回。要改请用源码。
          </div>
        </section>

        <div v-if="!readOnly" class="add-rule-row">
          <button type="button" class="btn add-rule" @click="addRule">+ 添加规则</button>
        </div>

        <p v-if="form.acls.length" class="pane-foot">
          标签只代表设备本身，不含它后面的网段。要让人进子网，得把网段写进目标，比如 <code>192.168.10.0/24</code>。
        </p>

        <div v-if="Object.keys(form.extra).length || form.hasIPRules" class="acl-preserved">
          界面不改、保存时原样写回的字段：
          <span v-for="k in Object.keys(form.extra)" :key="k" class="tag mono" style="margin:0 5px 0 0">{{ k }}</span>
          <span v-if="form.hasIPRules" class="tag mono" style="margin:0">acls.ip</span>
        </div>
      </div>

      <!-- 矩阵：钉在规则下方。收起时只留一条结论，写漏了会自动展开一次 -->
      <section class="acl-matrix-pane" :class="{ min: matrixMin }">
        <div class="mtx-bar">
          <div class="mtx-bar-main">
            <span class="mtx-bar-title">谁能访问谁</span>

            <span v-if="!matrixSum || !matrixSum.rows || !matrixSum.cols" class="mtx-bar-sub">
              还没有设备数据
            </span>
            <span v-else-if="matrixSum.openAll" class="mtx-bar-sub alert">
              策略里没有规则，当前默认全部互通
            </span>
            <span v-else class="mtx-bar-sub" :class="{ alert: matrixSum.deadRows }">
              {{ matrixSum.reachable }} 条可达
              <template v-if="matrixSum.deadRows"> · {{ matrixSum.deadRows }} 行完全不通</template>
              <template v-if="matrixSum.deadCols"> · {{ matrixSum.deadCols }} 列没人访问</template>
              <template v-if="!matrixSum.deadRows && !matrixSum.deadCols"> · 没发现漏配</template>
            </span>
          </div>
          <button type="button" class="btn sm ghost" @click="matrixMin = !matrixMin">
            {{ matrixMin ? '展开结果' : '收起' }}
          </button>
        </div>
        <div v-show="!matrixMin" class="mtx-body">
          <PermissionMatrix
            :form="form"
            :peers="peers"
            :members="memberTokens"
            @summary="matrixSum = $event"
          />
        </div>
      </section>
    </div>

    <!-- 组与标签：定义放一页，两边各占一半，加人加标签不用挤 -->
    <div v-else-if="mode === 'scope' && form" class="acl-scroll">
      <div class="scope-grid">
        <section class="acl-card">
          <div class="acl-head">
            <div>
              <h3 class="sec">用户组 <span class="acl-count">{{ form.groups.length }}</span></h3>
              <div class="acl-sub">组是一批人。规则里写组名，加人只改这里。成员填 headscale 用户名，保存时自动补 @。</div>
            </div>
          </div>
          <div v-if="!form.groups.length" class="acl-empty">还没有用户组。规则里也可以直接写成员名。</div>
          <div v-for="(g, gi) in form.groups" :key="'g' + gi" class="acl-item">
            <div class="acl-item-top">
              <input
                v-model="g.name"
                class="input mono"
                placeholder="devs（自动补 group:）"
                spellcheck="false"
                :disabled="readOnly"
                @blur="g.name = groupKey(g.name)"
              />
              <button class="acl-del" title="删除这一组" :disabled="readOnly" @click="form.groups.splice(gi, 1)">×</button>
            </div>
            <ChipsInput
              v-model="g.values"
              :suggestions="memberTokens"
              :normalize="userToken"
              :display="tokenLabel"
              placeholder="加成员，回车"
              :disabled="readOnly"
            />
          </div>
          <button v-if="!readOnly" type="button" class="link-btn" @click="form.groups.push({ name: '', values: [] })">
            + 新建用户组
          </button>
        </section>

        <section class="acl-card">
          <div class="acl-head">
            <div>
              <h3 class="sec">设备标签 <span class="acl-count">{{ form.tagOwners.length }}</span></h3>
              <div class="acl-sub">标签是一批设备。服务器、NAS 这类固定角色打标签，规则按标签写，换机器不用改规则。</div>
            </div>
          </div>
          <div v-if="!form.tagOwners.length" class="acl-empty">
            还没有标签。新建时会自动补 <code>tag:</code> 前缀。
          </div>
          <div v-for="(t, ti) in form.tagOwners" :key="'t' + ti" class="acl-item">
            <div class="acl-item-top">
              <input
                v-model="t.name"
                class="input mono"
                placeholder="server（自动补 tag:）"
                spellcheck="false"
                :disabled="readOnly"
                @blur="t.name = tagKey(t.name)"
              />
              <button class="acl-del" title="删除这个标签" :disabled="readOnly" @click="form.tagOwners.splice(ti, 1)">×</button>
            </div>
            <ChipsInput
              v-model="t.values"
              :suggestions="[...memberTokens, 'autogroup:admin']"
              :normalize="userToken"
              :display="tokenLabel"
              placeholder="谁能打这个标签"
              :disabled="readOnly"
            />
          </div>
          <button v-if="!readOnly" type="button" class="link-btn" @click="form.tagOwners.push({ name: '', values: [] })">
            + 新建标签
          </button>
        </section>

        <section class="acl-card">
          <div class="acl-head">
            <div>
              <h3 class="sec">地址别名 <span class="acl-count">{{ form.hosts.length }}</span></h3>
              <div class="acl-sub">
                给 IP 起个名字，规则里写 <code>nas:5000</code>，不用记地址。设备换 IP 只改这里。
              </div>
            </div>
          </div>
          <div v-if="!form.hostsEditable" class="acl-empty">
            原文的 <code>hosts</code> 不是「名字 → 地址」的写法，表单不接。去源码里改。
          </div>
          <template v-else>
            <div v-if="!form.hosts.length" class="acl-empty">
              还没有别名。目标里也能直接写设备名或 IP。
            </div>
            <div v-for="(h, hi) in form.hosts" :key="'h' + hi" class="host-row">
              <input v-model="h.name" class="input mono" placeholder="nas" spellcheck="false" :disabled="readOnly" />
              <input v-model="h.addr" class="input mono" placeholder="100.64.0.3" spellcheck="false" :disabled="readOnly" />
              <button class="acl-del" title="删掉这个别名" :disabled="readOnly" @click="form.hosts.splice(hi, 1)">×</button>
            </div>
            <button v-if="!readOnly" type="button" class="link-btn" @click="form.hosts.push({ name: '', addr: '' })">
              + 新建别名
            </button>
          </template>
        </section>

        <section class="acl-card">
          <div class="acl-head">
            <div>
              <h3 class="sec">自动批准</h3>
              <div class="acl-sub">
                这些身份通告的路由、出口节点直接生效，不用到设备页点批准。管理员默认就有这个权限。
              </div>
            </div>
          </div>
          <div v-if="!form.approversEditable" class="acl-empty">
            原文的 <code>autoApprovers</code> 写法不认识，表单不接。去源码里改。
          </div>
          <template v-else>
            <div class="approve-row">
              <span class="rule-label">子网路由</span>
              <ChipsInput
                v-model="form.approvers.routes"
                :suggestions="approverSuggestions"
                :display="tokenLabel"
                placeholder="写 tag: 或成员名，回车"
                :disabled="readOnly"
              />
            </div>
            <div class="approve-row">
              <span class="rule-label">出口节点</span>
              <ChipsInput
                v-model="form.approvers.exitNode"
                :suggestions="approverSuggestions"
                :display="tokenLabel"
                placeholder="写 tag: 或成员名，回车"
                :disabled="readOnly"
              />
            </div>
            <p class="card-hint">
              写人和组只对这个人名下的设备生效；写标签对打了这个标签的设备生效。
            </p>
          </template>
        </section>
      </div>
    </div>

    <!-- 源码模式 -->
    <div v-else class="acl-source">
      <textarea class="codearea" v-model="text" spellcheck="false"
                placeholder="在此编辑 HuJSON / JSON 策略…"></textarea>
      <div v-if="parseError" class="notice warn">
        表单打不开这份策略：{{ parseError }}<br>
        可能是 YAML，或结构不是标准写法。改好再点「规则」。
      </div>
    </div>
    <!-- 策略历史抽屉：放在整条 v-if 链之后，插在中间会让 v-else 断掉 -->
    <template v-if="histOpen">
      <div class="mask" @click="histOpen = false"></div>
      <div class="hist-drawer">
        <div class="spread">
          <h3 class="sec" style="margin:0">策略历史</h3>
          <button class="btn ghost sm" @click="histOpen = false">✕</button>
        </div>
        <p class="hist-note">每次保存前的版本留在这里，最近的 20 版。点一版看它跟线上差了什么。</p>

        <div v-if="histErr" class="notice warn" style="margin-top:10px">{{ histErr }}</div>
        <div v-else-if="!hist.length" class="hist-empty">
          还没有历史。改一次策略并保存之后，旧的那版会出现在这里。
        </div>

        <div class="hist-list">
          <button v-for="v in hist" :key="v.id" type="button" class="hist-item"
                  :class="{ on: histPick?.id === v.id }" @click="pickVersion(v)">
            <span class="hist-time">{{ fmtWhen(v.at) }}</span>
            <span class="faint">{{ v.lines }} 行 · {{ fmtSize(v.size) }}</span>
          </button>
        </div>

        <div v-if="histPick" class="hist-detail">
          <div class="spread hist-detail-head">
            <span class="faint">回滚后相对现在的变化</span>
            <span class="diff-stat">
              <b class="d-add">+{{ histStat.add }}</b>
              <b class="d-del">−{{ histStat.del }}</b>
            </span>
          </div>
          <div v-if="!histStat.add && !histStat.del" class="hist-empty">
            这一版和线上完全一样。
          </div>
          <div v-else class="diff-box">
            <div v-for="(l, i) in histDiff" :key="i" class="diff-line" :class="l.kind">
              <span class="diff-sign">{{ l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : l.kind === 'gap' ? '' : ' ' }}</span>
              <span class="diff-text">{{ l.kind === 'gap' ? '· · ·' : l.text || ' ' }}</span>
            </div>
          </div>
          <button class="btn primary" style="width:100%;margin-top:12px"
                  :disabled="histBusy || readOnly" @click="rollback">
            {{ histBusy ? '回滚中…' : '回到这一版' }}
          </button>
          <p v-if="readOnly" class="card-hint" style="margin-top:8px">服务端不让改策略，回滚也不行。</p>
        </div>
      </div>
    </template>

    <ConfirmDialog v-if="confirmBox" v-bind="confirmBox" @close="closeConfirm" />
  </div>
</template>

<style scoped>
.seg {
  display: inline-flex;
  padding: 2px;
  gap: 2px;
  background: var(--surface-3);
  border: 1px solid var(--line);
  border-radius: 9px;
}
.seg button {
  height: 26px;
  padding: 0 12px;
  border: 0;
  border-radius: 7px;
  background: none;
  font-size: 11.5px;
  color: var(--muted);
}
.seg button:hover { color: var(--text); }
.seg button.on {
  background: var(--surface);
  color: var(--text);
  font-weight: 600;
  box-shadow: var(--shadow-1);
}

/* 概览 */
.sum {
  display: flex;
  align-items: center;
  gap: 22px;
  padding: 11px 16px;
  margin-bottom: 12px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r);
  flex-wrap: wrap;
}
.sum-item { display: flex; align-items: baseline; gap: 5px; }
.sum-item b { font-size: 15px; font-weight: 650; letter-spacing: -.02em; }
.sum-item span { font-size: 10.5px; color: var(--faint); }
.sum-hint { margin-left: auto; font-size: 10.5px; color: var(--faint); }

.acl-card {
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r);
  padding: 14px 16px;
  margin-bottom: 12px;
  box-shadow: var(--shadow-1);
}
.acl-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.acl-head .sec { margin: 0 0 4px; }
.acl-count {
  margin-left: 6px;
  padding: 1px 7px;
  border-radius: 999px;
  background: var(--surface-3);
  font-size: 10px;
  font-weight: 500;
  color: var(--muted);
}
.acl-sub { font-size: 10.5px; color: var(--faint); line-height: 1.7; }
.acl-empty { padding: 6px 2px 10px; font-size: 11px; color: var(--faint); line-height: 1.7; }
.acl-note { margin-top: 8px; font-size: 10.5px; color: var(--faint); line-height: 1.7; }
.acl-note code {
  font-family: var(--mono);
  font-size: 10px;
  padding: 1px 4px;
  border-radius: 4px;
  background: var(--surface-2);
  border: 1px solid var(--line-soft);
}

/* 起点模板 */
.start-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; }
.start-card {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 14px;
  border: 1px solid var(--line);
  border-radius: var(--r);
  background: var(--surface-2);
  text-align: left;
  cursor: pointer;
  transition: border-color .15s, background .15s;
}
.start-card:hover:not(:disabled) { border-color: var(--brand); background: var(--brand-soft); }
.start-card:disabled { opacity: .5; cursor: not-allowed; }
.start-card b { font-size: 12px; font-weight: 600; color: var(--text); }
.start-card span { font-size: 10.5px; color: var(--faint); line-height: 1.7; }

/* 规则卡片 */
.rule { padding: 0; overflow: hidden; }
.rule-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px 16px 9px;
  border-bottom: 1px solid var(--line-soft);
  background: var(--surface-2);
}
.rule-no {
  flex: none;
  font-size: 10px;
  font-weight: 600;
  color: var(--muted);
  letter-spacing: .04em;
}
.rule-sum {
  flex: 1;
  min-width: 0;
  font-size: 11.5px;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rule-del { flex: none; height: 24px; padding: 0 8px; font-size: 10.5px; }
.rule-del:hover:not(:disabled) { color: var(--danger); background: var(--danger-soft); }

.rule-row {
  display: grid;
  /* auto 列必须配 justify-content: start，否则 grid 会把剩余宽度全塞给它，
     chips 又变成贴在左边的一小撮 */
  grid-template-columns: 112px auto;
  justify-content: start;
  gap: 12px;
  align-items: start;
  padding: 11px 16px;
}
.rule-row .tf { min-width: 320px; max-width: 720px; }

.rule-row + .rule-row { border-top: 1px solid var(--line-soft); }
.rule-label {
  font-size: 11px;
  color: var(--faint);
  line-height: 36px;
  letter-spacing: .02em;
}

.dst-list { display: flex; flex-direction: column; gap: 7px; max-width: 760px; }
.dst-line {
  /* 目标列按内容宽自适应（有上下限）：chips 少时「的」紧跟目标，读起来是一句话；
     多到 520px 就在框内换行，不至于把端口挤出卡片 */
  display: grid;
  grid-template-columns: auto 15px 182px 22px;
  justify-content: start;
  gap: 8px;
  align-items: center;
}
.dst-line .tf { min-width: 210px; max-width: 520px; }
.dst-sep { font-size: 10.5px; color: var(--faint); text-align: center; }
.icon-del {
  width: 22px;
  height: 36px;
  border: 0;
  background: none;
  border-radius: 6px;
  color: var(--faint);
  font-size: 14px;
}
.icon-del:hover:not(:disabled) { color: var(--danger); background: var(--danger-soft); }

.add-rule-row { padding: 2px 0 14px; }
.add-rule {
  height: 36px;
  padding: 0 16px;
  border-style: dashed;
  color: var(--muted);
}
.add-rule:hover { color: var(--text); border-color: var(--line); }

/* 规则区底部说明：填住卡片下面的空，同时把踩过的坑写在看得见的地方 */
.pane-foot {
  max-width: 820px; margin: 2px 0 6px; padding-left: 2px;
  font-size: 11px; line-height: 1.9; color: var(--faint);
}
.pane-foot code {
  font-family: var(--mono); font-size: 10.5px;
  background: var(--surface-3); padding: 1px 5px; border-radius: 4px;
}

/* 区块标题：页面从工具栏直接跳进卡片会缺一层结构，规则多的时候也找不到头 */
.pane-head {
  display: flex; align-items: baseline; gap: 10px;
  padding: 0 2px 10px;
}
.pane-head .sec { margin: 0; }
.pane-head-note { font-size: 11px; color: var(--faint); }
.pane-head-note::before { content: "·"; margin-right: 6px; }

/* 组与标签 */
.scope { margin-top: 14px; display: flex; flex-direction: column; gap: 16px; }
.scope-block { border-top: 1px solid var(--line-soft); padding-top: 12px; }
.scope-title { font-size: 11px; font-weight: 600; color: var(--muted); margin-bottom: 8px; }
.acl-row {
  display: grid;
  gap: 8px;
  align-items: start;
  padding: 6px 0;
}
.acl-row-list { grid-template-columns: minmax(140px, 200px) 1fr 26px; }
.acl-row .input { height: 34px; }
.acl-del {
  width: 26px;
  height: 34px;
  border: 0;
  background: none;
  border-radius: 7px;
  color: var(--faint);
  font-size: 16px;
  line-height: 1;
}
.acl-del:hover:not(:disabled) { color: var(--danger); background: var(--danger-soft); }

.link-btn {
  border: 0;
  background: none;
  padding: 4px 0;
  font-size: 10.5px;
  color: var(--brand);
  text-align: left;
}
.link-btn:hover { text-decoration: underline; }

.acl-preserved {
  margin-top: 8px;
  font-size: 10.5px;
  color: var(--faint);
  line-height: 2.2;
}

/* ---------------- 布局：固定头 + 单栏滚动 ---------------- */

.acl-fixed { flex: none; position: relative; z-index: 2; max-height: 54%; overflow-y: auto; }
/* 滚动内容从下面经过时，用一层渐变把它压暗，避免文字直接贴到统计条上 */
.acl-fixed::after {
  content: ""; position: absolute; left: 0; right: 0; bottom: -12px; height: 12px;
  background: linear-gradient(var(--bg), transparent); pointer-events: none;
}
/* ---- 工具栏：统计压成一行内联，窄了自动隐去 ---- */
.acl-bar { align-items: center; }
.acl-updated { font-size: 11px; white-space: nowrap; }
.acl-stats {
  display: flex; align-items: center; gap: 11px;
  margin-left: auto; margin-right: 14px;
  font-size: 11px; color: var(--faint); white-space: nowrap;
}
.acl-stats b { color: var(--text); font-weight: 600; font-variant-numeric: tabular-nums; }
.acl-stats i { width: 1px; height: 9px; background: currentColor; opacity: .3; }

/* 提示条只占固定区的固定高度，条数多了自己滚，别把下面的内容挤没 */
.acl-notice { margin: 0 0 8px; max-height: 104px; overflow: auto; }

/* ---- 校验结果：默认一行摘要，多了再展开 ---- */
.notice.slim { padding: 8px 12px; overflow: visible; max-height: none; }
.slim-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
.slim-tag { flex: none; font-size: 11px; font-weight: 600; }
.slim-text {
  flex: 1; min-width: 0; font-size: 11.5px; line-height: 1.6;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.slim-toggle { flex: none; font-size: 11px; }
.slim-list {
  display: flex; flex-direction: column; gap: 5px;
  margin-top: 7px; padding-top: 7px;
  border-top: 1px dashed var(--line); font-size: 11.5px; line-height: 1.7;
}

/* ---- 风险体检：橙色，跟黄色的校验提示、红色的错误分开 ---- */
.notice.risk {
  background: var(--warn-soft);
  border-color: color-mix(in srgb, var(--warn) 26%, transparent);
  color: var(--text);
}
.risk-tag { color: var(--warn); }
.risk-list { gap: 2px; }
.risk-row {
  display: grid;
  grid-template-columns: 6px minmax(0, 1fr);
  align-items: start;
  gap: 8px;
  width: 100%; padding: 6px;
  border: 0; border-radius: 7px;
  background: transparent;
  text-align: left;
  font: inherit; font-size: 11.5px; line-height: 1.65;
  color: var(--muted);
  cursor: pointer;
}
.risk-row:hover { background: color-mix(in srgb, var(--warn) 9%, transparent); }
.risk-dot {
  width: 6px; height: 6px; border-radius: 50%;
  margin-top: 7px; flex: none;
}
.risk-dot.high { background: var(--danger); }
.risk-dot.mid { background: var(--warn); }
.risk-body { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
.risk-line { display: flex; align-items: baseline; gap: 10px; }
.risk-title { color: var(--text); font-weight: 500; }
.risk-go { margin-left: auto; color: var(--faint); font-size: 10.5px; white-space: nowrap; }
.risk-detail { color: var(--muted); }
.risk-row:hover .risk-go { color: var(--warn); }
/* 跳转后闪一下边，不然滚过去也看不出是哪一条 */
.acl-card.risk-flash { animation: risk-flash 1.4s ease; }
@keyframes risk-flash {
  0%, 100% { box-shadow: 0 0 0 0 transparent; }
  12%, 55% { box-shadow: 0 0 0 2px color-mix(in srgb, var(--warn) 55%, transparent); }
}

/* 单栏滚动容器（组与标签页用）：顶部 16px 淡出 + 同宽 padding */
.acl-scroll {
  flex: 1; min-height: 0; overflow: auto; padding: 16px 4px 0 0;
  -webkit-mask-image: linear-gradient(to bottom, transparent 0, #000 16px);
  mask-image: linear-gradient(to bottom, transparent 0, #000 16px);
}

/* 规则行的选择器平时不要整块灰底 + 边框：一条规则只有两行控件，
   整宽的空框会读成「两个没填完的表单」。chips 直接坐在卡片上，
   鼠标过去或展开候选时才浮现可编辑区域。 */
.rule-row .tf { max-width: 760px; }
.rule-label { white-space: nowrap; }

.acl-rules-pane :deep(.tf-box) {
  background: transparent;
  border-color: transparent;
}
.acl-rules-pane :deep(.tf-box:hover),
.acl-rules-pane :deep(.tf-box:focus-within),
.acl-rules-pane :deep(.tf-box.open) {
  background: var(--surface-2);
  border-color: var(--line);
}

/* ---- 规则页：上写规则（自己滚）/ 下矩阵（钉住不动） ---- */
.acl-split { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 10px; }
.acl-rules-pane {
  /* flex-grow 留 0：内容不满一屏时让矩阵紧跟在规则后面（整块像一份文档），
     内容超出时才收缩并滚动，矩阵这才回到「钉在底部」 */
  flex: 0 1 auto; min-height: 110px; overflow: auto; padding: 16px 4px 14px 0;
  /* 上下都淡出：卡片滚到边界是被柔化掉的，不会像被刀切了一半 */
  -webkit-mask-image: linear-gradient(to bottom, transparent 0, #000 16px, #000 calc(100% - 18px), transparent 100%);
  mask-image: linear-gradient(to bottom, transparent 0, #000 16px, #000 calc(100% - 18px), transparent 100%);
}
/* 高度随表格内容长，最多吃掉 44vh：行少时把空间让给规则区，行多时自己滚 */
/* 不做成第二张卡：规则卡是白卡，这里直接坐在页面底色上，只用一条顶线断开。
   否则白卡里再套一个带框表格，两层边框 + 上浮阴影，会读成一块浮起来的板子。 */
.acl-matrix-pane {
  flex: none; display: flex; flex-direction: column;
  max-height: 44vh; padding: 2px 0 6px;
  border-top: 1px solid var(--line);
}
.acl-matrix-pane.min { max-height: none; }
.mtx-bar {
  flex: none; display: flex; align-items: center; gap: 10px;
  padding: 7px 2px 6px;
}
.mtx-bar-main { flex: 1; min-width: 0; display: flex; align-items: baseline; gap: 8px; }
.mtx-bar-title { font-size: 12.5px; font-weight: 600; color: var(--text); letter-spacing: .01em; }
.mtx-bar-sub { font-size: 10.5px; color: var(--faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mtx-bar-sub.alert { color: var(--warn); font-weight: 600; }
/* 矩阵自己滚，不把固定区撑高。滚动交给表格框，吸顶表头才有意义 */
.mtx-body { flex: 1; min-height: 0; display: flex; flex-direction: column; overflow: hidden; padding-top: 8px; }
.acl-source { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 10px; padding-top: 10px; }
.acl-source .codearea { flex: 1; min-height: 160px; height: auto; }

/* 组与标签：两栏并排，各自一列到底 */
/* 两张卡等高、底部的「+ 新建…」对齐，否则一行之差会显得排列随意 */
.scope-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; align-items: start; }
.scope-grid .acl-card { margin-bottom: 0; display: flex; flex-direction: column; }
.scope-grid .acl-card > .link-btn:last-child { margin-top: 10px; }
.scope-grid .acl-head { margin-bottom: 8px; }
.acl-item { display: flex; flex-direction: column; gap: 6px; padding: 9px 0; }
.acl-item + .acl-item { border-top: 1px dashed var(--line-soft); }
.acl-item-top { display: flex; align-items: center; gap: 6px; }
.acl-item-top .input { flex: 1; min-width: 0; }
.acl-item .acl-del { width: 26px; height: 34px; }

.card-hint {
  margin: 8px 0 0;
  font-size: 10.5px;
  color: var(--faint);
  line-height: 1.7;
}

.hist-drawer {
  position: absolute;
  right: 0;
  top: 0;
  bottom: 0;
  width: 480px;
  display: flex;
  flex-direction: column;
  background: var(--surface);
  border-left: 1px solid var(--line);
  box-shadow: var(--shadow-pop);
  padding: 20px;
  z-index: 3;
  animation: weft-slide .22s cubic-bezier(.22, .8, .3, 1);
}
.hist-note { margin: 6px 0 0; font-size: 11px; color: var(--faint); line-height: 1.7; }
.hist-empty {
  margin-top: 14px;
  padding: 22px 0;
  text-align: center;
  font-size: 11.5px;
  color: var(--faint);
}
.hist-list {
  margin-top: 10px;
  flex: 1;
  min-height: 60px;
  overflow-y: auto;
}
.hist-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 9px 10px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: none;
  text-align: left;
  font-size: 11.5px;
}
.hist-item:hover { background: var(--surface-3); }
.hist-item.on {
  background: var(--surface-3);
  border-color: var(--line);
}
.hist-time { font-family: var(--mono); font-size: 11px; }
.hist-item + .hist-item { margin-top: 2px; }
.hist-detail {
  flex-shrink: 0;
  max-height: 62%;
  display: flex;
  flex-direction: column;
  margin-top: 10px;
  padding-top: 12px;
  border-top: 1px solid var(--line);
}
.hist-detail-head { font-size: 11px; margin-bottom: 8px; }
.diff-stat { display: flex; gap: 10px; font-family: var(--mono); font-size: 11px; }
.d-add { color: var(--ok); font-weight: 600; }
.d-del { color: var(--danger); font-weight: 600; }
.diff-box {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 8px 0;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--surface-2);
  font-family: var(--mono);
  font-size: 10.5px;
  line-height: 1.7;
}
.diff-line { display: flex; gap: 6px; padding: 0 8px; white-space: pre; }
.diff-line.add { background: color-mix(in srgb, var(--ok) 12%, transparent); color: var(--ok); }
.diff-line.del { background: color-mix(in srgb, var(--danger) 10%, transparent); color: var(--danger); }
.diff-line.gap { color: var(--faint); }
.diff-sign { flex: none; width: 8px; user-select: none; }
.diff-text { flex: 1; min-width: 0; }

.host-row {
  display: grid;
  grid-template-columns: 1fr 1fr 26px;
  gap: 6px;
  align-items: center;
  padding: 6px 0;
}
.host-row + .card-hint {
  margin: 8px 0 0;
  font-size: 10.5px;
  color: var(--faint);
  line-height: 1.7;
}

.hist-drawer {
  position: absolute;
  right: 0;
  top: 0;
  bottom: 0;
  width: 480px;
  display: flex;
  flex-direction: column;
  background: var(--surface);
  border-left: 1px solid var(--line);
  box-shadow: var(--shadow-pop);
  padding: 20px;
  z-index: 3;
  animation: weft-slide .22s cubic-bezier(.22, .8, .3, 1);
}
.hist-note { margin: 6px 0 0; font-size: 11px; color: var(--faint); line-height: 1.7; }
.hist-empty {
  margin-top: 14px;
  padding: 22px 0;
  text-align: center;
  font-size: 11.5px;
  color: var(--faint);
}
.hist-list {
  margin-top: 10px;
  flex: 1;
  min-height: 60px;
  overflow-y: auto;
}
.hist-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 9px 10px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: none;
  text-align: left;
  font-size: 11.5px;
}
.hist-item:hover { background: var(--surface-3); }
.hist-item.on {
  background: var(--surface-3);
  border-color: var(--line);
}
.hist-time { font-family: var(--mono); font-size: 11px; }
.hist-item + .hist-item { margin-top: 2px; }
.hist-detail {
  flex-shrink: 0;
  max-height: 62%;
  display: flex;
  flex-direction: column;
  margin-top: 10px;
  padding-top: 12px;
  border-top: 1px solid var(--line);
}
.hist-detail-head { font-size: 11px; margin-bottom: 8px; }
.diff-stat { display: flex; gap: 10px; font-family: var(--mono); font-size: 11px; }
.d-add { color: var(--ok); font-weight: 600; }
.d-del { color: var(--danger); font-weight: 600; }
.diff-box {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 8px 0;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--surface-2);
  font-family: var(--mono);
  font-size: 10.5px;
  line-height: 1.7;
}
.diff-line { display: flex; gap: 6px; padding: 0 8px; white-space: pre; }
.diff-line.add { background: color-mix(in srgb, var(--ok) 12%, transparent); color: var(--ok); }
.diff-line.del { background: color-mix(in srgb, var(--danger) 10%, transparent); color: var(--danger); }
.diff-line.gap { color: var(--faint); }
.diff-sign { flex: none; width: 8px; user-select: none; }
.diff-text { flex: 1; min-width: 0; }

.host-row { border-top: 1px dashed var(--line-soft); }
.approve-row { display: flex; flex-direction: column; gap: 5px; padding: 6px 0; }
.approve-row + .approve-row { border-top: 1px dashed var(--line-soft); }
.approve-row .rule-label { font-size: 11px; color: var(--muted); }
.scope-grid .acl-card code {
  padding: 1px 4px;
  border-radius: 4px;
  background: var(--surface-3);
  font-size: 10.5px;
}

.codearea { min-height: 560px; }

@media (max-width: 1150px) {
  .start-grid { grid-template-columns: 1fr; }
  .rule-row { grid-template-columns: 1fr; gap: 6px; }
  .rule-label { line-height: 1.5; }
  .dst-line { grid-template-columns: 1fr 14px 160px 22px; }
}

/* 窗口窄了组与标签两栏并排放不下，退回上下 */
@media (max-width: 1080px) {
  .scope-grid { grid-template-columns: 1fr; }
}

/* 统计挤不下就让位给按钮，页签里各自的计数还在 */
@media (max-width: 1240px) {
  .acl-stats { display: none; }
}

/* 窗口矮的时候先把矩阵压到底线，优先保证规则区能用 */
@media (max-height: 720px) {
  .acl-matrix-pane { max-height: 32vh; }
}
</style>
