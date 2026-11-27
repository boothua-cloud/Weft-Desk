<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type State, type PolicyDoc, type Device, type User } from '../api'
import { reloadTick } from '../bus'
import ChipsInput from '../components/ChipsInput.vue'
import PermissionMatrix from '../components/PermissionMatrix.vue'
import PortField from '../components/PortField.vue'
import TokenField from '../components/TokenField.vue'
import {
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
} from '../acl'

const props = defineProps<{ state: State | null }>()
const emit = defineEmits(['notify'])

const doc = ref<PolicyDoc>({ content: '', updatedAt: '' })
// 源码模式的缓冲区；表单模式以 form 为准
const text = ref('')
const form = ref<AclForm | null>(null)
const parseError = ref('')
const hadComments = ref(false)
const mode = ref<'form' | 'source'>('form')
const baseline = ref('')
const check = ref<{ kind: 'ok' | 'warn' | 'err'; text: string } | null>(null)
const busy = ref(false)
const loading = ref(false)
const checking = ref(false)
const showScope = ref(false)
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
const hostAliases = computed<string[]>(() => {
  const h = form.value?.extra?.hosts
  if (!h || typeof h !== 'object' || Array.isArray(h)) return []
  return Object.keys(h as Record<string, unknown>)
})

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
const currentText = computed(() => (mode.value === 'form' && form.value ? serializeAcl(form.value) : text.value))
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
      mode.value = 'form'
      // 以归一化后的表单作为「已保存」基准，带注释的原文不算改动
      baseline.value = serializeAcl(form.value as AclForm)
    } else {
      mode.value = 'source'
      baseline.value = doc.value.content
    }
    check.value = null
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

function setMode(m: 'form' | 'source') {
  if (m === mode.value) return
  check.value = null
  if (m === 'source') {
    if (form.value) text.value = serializeAcl(form.value)
    mode.value = 'source'
    return
  }
  if (parseInto(text.value)) {
    mode.value = 'form'
  } else {
    emit('notify', '当前策略无法用表单表示：' + parseError.value)
  }
}

function formatSource() {
  const r = parseAcl(text.value)
  if (!r.ok) {
    check.value = { kind: 'err', text: '格式化失败：' + r.error }
    return
  }
  text.value = serializeAcl(r.form)
  check.value = { kind: 'warn', text: '已格式化。注释与尾逗号会被移除，保存后服务端看到的是纯 JSON。' }
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
      check.value = { kind: 'warn', text: `服务端校验通过，但源码里的写法不规范：${hints[0]}。点「格式化」会自动修正。` }
    } else if (localErr) {
      check.value = { kind: 'warn', text: `服务端校验通过，但本地检查有 ${localErr} 处错误（多为组/标签引用对不上），建议先改。` }
    } else if (local.length) {
      check.value = { kind: 'warn', text: `服务端校验通过，本地有 ${local.length} 条提示，见下方列表。` }
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
  const payload = currentText.value
  if (mode.value === 'form') {
    if (!form.value) return
    if (errorCount.value) {
      check.value = { kind: 'err', text: `还有 ${errorCount.value} 处错误没改，先修正再保存。` }
      emit('notify', '规则有错误，未保存')
      return
    }
  }
  busy.value = true
  const hints = mode.value === 'source' ? policyKeyHints(payload) : []
  try {
    const lost = hadComments.value
    await api.savePolicy(payload)
    policyLocked.value = false
    emit('notify', lost ? '策略已保存，HuJSON 注释已移除' : '策略已保存并应用')
    await load()
    check.value = { kind: 'ok', text: '已保存并应用到整张 tailnet。' }
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
  showScope.value = true
  emit('notify', `${name} 已创建，到下方「用户组与标签」里加成员后才生效`)
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
  emit('notify', '已套用模板，确认无误后点「保存并应用」')
}
</script>

<template>
  <div>
    <div class="spread" style="margin-bottom:14px;gap:12px;flex-wrap:wrap">
      <div class="row" style="gap:9px">
        <div class="seg">
          <button type="button" :class="{ on: mode === 'form' }" @click="setMode('form')">规则</button>
          <button type="button" :class="{ on: mode === 'source' }" @click="setMode('source')">源码</button>
        </div>
        <span v-if="dirty" class="tag amber" style="margin:0">未保存</span>
        <span class="faint" style="font-size:11.5px">更新于 {{ updatedLabel }}</span>
      </div>
      <div class="row">
        <span v-if="loading" class="load-hint" style="margin:0 8px 0 0">
          <span class="spinner"></span>读取中…
        </span>
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

    <div v-if="policyLocked" class="notice warn" style="margin:-2px 0 12px">
      这台 headscale 的策略存在文件里（<code>policy.mode: file</code>），接口只能读不能写，所以这个界面保存不了。
      想在这里改：把 config.yaml 的 <code>policy.mode</code> 改成 <code>database</code> 并重启，再用
      <code>headscale policy set -f 原文件</code> 导一次；不想改配置就走文件：改服务器上的 policy 文件后
      <code>systemctl reload headscale</code>。
    </div>

    <!-- 规则模式 -->
    <template v-if="mode === 'form' && form">
      <div class="sum">
        <div class="sum-item"><b>{{ form.acls.length }}</b><span>条规则</span></div>
        <div class="sum-item"><b>{{ form.groups.length }}</b><span>个用户组</span></div>
        <div class="sum-item"><b>{{ form.tagOwners.length }}</b><span>个设备标签</span></div>
        <div class="sum-item"><b>{{ peers.length }}</b><span>台设备</span></div>
        <div class="sum-hint">规则之间的「谁」用组或标签表达，改人不用改规则。</div>
      </div>

      <!-- 校验结果放在最上面：以前在最底下，窗口一小就看不见，
           用户只会看到服务端回的错误 -->
      <div v-if="issues.length" class="notice" :class="errorCount ? 'warn' : 'info'" style="margin-top:12px">
        <div v-for="(it, i) in issues" :key="i" :style="it.level === 'error' ? 'color:var(--danger)' : ''">
          {{ it.level === 'error' ? '错误' : '提示' }} · {{ it.text }}
        </div>
      </div>

      <!-- 空规则时的起点 -->
      <section v-if="!form.acls.length" class="acl-card start">
        <div class="acl-head">
          <div>
            <h3 class="sec">
              {{ form.hasAclsKey ? '当前谁都不通' : '当前全部互通' }}
            </h3>
            <div class="acl-sub">
              {{ form.hasAclsKey
                ? '策略里的规则是空的，等于拒绝所有访问。挑一个起点，再按需放宽。'
                : '策略里没有规则，headscale 会放行全部设备。要收紧权限，就从下面挑一个起点。' }}
            </div>
          </div>
        </div>
        <div class="start-grid">
          <button type="button" class="start-card" :disabled="readOnly" @click="applyTemplate('self')">
            <b>只访问自己的设备</b>
            <span>每个人只能连自己名下的设备，互相隔离。适合个人多设备。</span>
          </button>
          <button type="button" class="start-card" :disabled="readOnly" @click="applyTemplate('admin')">
            <b>管理员全通，其他人各管各的</b>
            <span>管理员可以访问所有设备，普通成员只访问自己的。适合小团队。</span>
          </button>
          <button type="button" class="start-card" :disabled="readOnly" @click="applyTemplate('open')">
            <b>保持全部互通</b>
            <span>不写任何规则，所有人可以访问所有设备。只适合完全信任的内网。</span>
          </button>
        </div>
      </section>

      <!-- 规则列表 -->
      <section v-for="(r, i) in form.acls" :key="i" class="acl-card rule">
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
          <span class="rule-label">访问哪些设备的哪些端口</span>
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
              + 再加一组目标（端口不同时用）
            </button>
          </div>
        </div>

        <div v-if="'ip' in r.extra" class="acl-note">
          这条规则用 <code>ip</code> 字段限定网段，界面上不解析，保存时原样保留；要改请切到源码。
        </div>
      </section>

      <button v-if="!readOnly" type="button" class="btn add-rule" @click="addRule">+ 添加规则</button>

      <!-- 权限矩阵 -->
      <section class="acl-card">
        <div class="acl-head">
          <div>
            <h3 class="sec">谁能访问谁</h3>
            <div class="acl-sub">
              按上面的规则逐条推演，不看你写了什么，只看实际结果。保存前扫一眼就能发现写宽或写漏。
            </div>
          </div>
        </div>
        <PermissionMatrix :form="form" :peers="peers" :members="memberTokens" />
      </section>

      <!-- 组与标签 -->
      <section class="acl-card">
        <div class="acl-head" style="margin-bottom:0">
          <div>
            <h3 class="sec">
              用户组与标签
              <span class="acl-count">{{ form.groups.length + form.tagOwners.length }}</span>
            </h3>
            <div class="acl-sub">
              组是「一批人」，标签是「一批设备」。规则里引用的名字都在这里维护。
              成员名取 headscale 的用户名，写入策略时会自动补上它要求的 @。
            </div>
          </div>
          <button class="btn sm" @click="showScope = !showScope">
            {{ showScope ? '收起' : '展开管理' }}
          </button>
        </div>

        <div v-if="showScope" class="scope">
          <div class="scope-block">
            <div class="scope-title">用户组</div>
            <div v-if="!form.groups.length" class="acl-empty">还没有用户组。规则里也可以直接写成员名。</div>
            <div v-for="(g, gi) in form.groups" :key="'g' + gi" class="acl-row acl-row-list">
              <input
                v-model="g.name"
                class="input mono"
                placeholder="devs（自动补 group:）"
                spellcheck="false"
                :disabled="readOnly"
                @blur="g.name = groupKey(g.name)"
              />
              <ChipsInput
                v-model="g.values"
                :suggestions="memberTokens"
                :normalize="userToken"
                :display="tokenLabel"
                placeholder="加成员，回车"
                :disabled="readOnly"
              />
              <button class="acl-del" title="删除这一组" :disabled="readOnly" @click="form.groups.splice(gi, 1)">×</button>
            </div>
            <button v-if="!readOnly" type="button" class="link-btn" @click="form.groups.push({ name: '', values: [] })">
              + 新建用户组
            </button>
          </div>

          <div class="scope-block">
            <div class="scope-title">设备标签</div>
            <div v-if="!form.tagOwners.length" class="acl-empty">
              还没有标签。服务器、NAS 这类固定角色的设备打个标签，规则按标签写更省事。
            </div>
            <div v-for="(t, ti) in form.tagOwners" :key="'t' + ti" class="acl-row acl-row-list">
              <input
                v-model="t.name"
                class="input mono"
                placeholder="server（自动补 tag:）"
                spellcheck="false"
                :disabled="readOnly"
                @blur="t.name = tagKey(t.name)"
              />
              <ChipsInput
                v-model="t.values"
                :suggestions="[...memberTokens, 'autogroup:admin']"
                :normalize="userToken"
                :display="tokenLabel"
                placeholder="谁能打这个标签"
                :disabled="readOnly"
              />
              <button class="acl-del" title="删除这个标签" :disabled="readOnly" @click="form.tagOwners.splice(ti, 1)">×</button>
            </div>
            <button v-if="!readOnly" type="button" class="link-btn" @click="form.tagOwners.push({ name: '', values: [] })">
              + 新建标签
            </button>
          </div>
        </div>
      </section>

      <div v-if="Object.keys(form.extra).length || form.hasIPRules" class="acl-preserved">
        表单不解析、保存时会原样写回的字段：
        <span v-for="k in Object.keys(form.extra)" :key="k" class="tag mono" style="margin:0 5px 0 0">{{ k }}</span>
        <span v-if="form.hasIPRules" class="tag mono" style="margin:0">acls.ip</span>
      </div>

      <div v-if="check" class="notice" :class="check.kind === 'ok' ? 'info' : 'warn'" style="margin-top:10px">
        {{ check.text }}
      </div>

      <div v-if="hadComments" class="notice warn" style="margin-top:10px">
        原策略里有 HuJSON 注释，用界面保存会把注释去掉。介意的话留在源码模式改。
      </div>
      <div v-if="readOnly" class="notice warn" style="margin-top:10px">
        当前服务端不允许改策略，这里只做展示。
      </div>
    </template>

    <!-- 源码模式 -->
    <div v-else>
      <textarea class="codearea" v-model="text" spellcheck="false"
                placeholder="在此编辑 HuJSON / JSON 策略…"></textarea>
      <div v-if="parseError" class="notice warn" style="margin-top:10px">
        规则模式当前不可用：{{ parseError }}<br>
        可能是策略用了 YAML，或结构不是 groups / tagOwners / acls 的标准写法。改好后点「规则」即可切回。
      </div>
      <div v-if="check" class="notice" :class="check.kind === 'ok' ? 'info' : 'warn'" style="margin-top:10px">
        {{ check.text }}
      </div>
    </div>
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
  font-size: 11px;
  color: var(--faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rule-del { flex: none; height: 24px; padding: 0 8px; font-size: 10.5px; }
.rule-del:hover:not(:disabled) { color: var(--danger); background: var(--danger-soft); }

.rule-row {
  display: grid;
  grid-template-columns: 122px 1fr;
  gap: 12px;
  align-items: start;
  padding: 11px 16px;
}
.rule-row + .rule-row { border-top: 1px solid var(--line-soft); }
.rule-label {
  font-size: 10.5px;
  color: var(--faint);
  line-height: 36px;
  letter-spacing: .02em;
}

.dst-list { display: flex; flex-direction: column; gap: 7px; }
.dst-line {
  display: grid;
  grid-template-columns: 1fr 18px 200px 22px;
  gap: 6px;
  align-items: center;
}
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

.add-rule {
  width: 100%;
  height: 38px;
  margin-bottom: 12px;
  border-style: dashed;
  color: var(--muted);
}

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

.codearea { min-height: 560px; }

@media (max-width: 1150px) {
  .start-grid { grid-template-columns: 1fr; }
  .rule-row { grid-template-columns: 1fr; gap: 6px; }
  .rule-label { line-height: 1.5; }
  .dst-line { grid-template-columns: 1fr 14px 160px 22px; }
}
</style>
