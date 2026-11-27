<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { dstLabel, dstPrefix, groupKey, tagKey, tokenKind, tokenLabel, tokenPrefix, userToken, type AccessPeer, type AclEntry } from '../acl'

// 来源 / 目标的统一选择器。
// 不用手写 `tag:server` 这种串：点开就是一个分组清单，
// 成员、用户组、标签、设备各一组，勾一下就好；想手写也能在搜索框里回车。
const props = defineProps<{
  modelValue: string[]
  groups: AclEntry[]
  tags: AclEntry[]
  peers: AccessPeer[]
  members: string[]
  /** 策略 hosts 里的别名。dst 只能写设备名、标签或别名，这里列出来省得手打 */
  hosts?: string[]
  /** 已生效的子网路由：dst 里必须直接写网段，写路由器名字是不通的 */
  routes?: { cidr: string; via: string }[]
  mode: 'src' | 'dst'
  disabled?: boolean
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: string[]): void
  (e: 'create', kind: 'group' | 'tag', name: string): void
}>()

const open = ref(false)
const adding = ref<'group' | 'tag' | ''>('')
const kw = ref('')
const anchor = ref<HTMLElement | null>(null)
const searchEl = ref<HTMLInputElement | null>(null)
const pos = ref({ top: 0, bottom: 0, left: 0, width: 260, maxH: 340, up: false })

const selected = computed(() => new Set(props.modelValue))
const chipPrefix = (t: string) => (props.mode === 'dst' ? dstPrefix(t) : tokenPrefix(t))

interface Opt {
  token: string
  label: string
  sub?: string
}
interface Group {
  title: string
  items: Opt[]
}

// 来源和目标的取值空间不一样，这不是疏忽：
// src 是「谁」——成员、用户组、标签、特殊组都行；
// dst 是「哪台设备的哪个端口」——只认设备、标签、hosts 别名、autogroup:self、IP。
// 把人名或组名写进 dst，headscale 会回 host not defined in policy。
const allGroups = computed<Group[]>(() => {
  const out: Group[] = []
  if (props.mode === 'src') {
    out.push({
      title: '特殊',
      items: [
        { token: '*', label: '所有人', sub: '包括今后新加入的成员' },
        { token: 'autogroup:member', label: '所有成员', sub: '不含已打标签的设备' },
      ],
    })

    const memberItems: Opt[] = props.members
      .filter((m) => m)
      .map((m) => {
        // 值必须带 @（headscale 的写法），标签去掉 @ 更好读
        const token = userToken(m)
        return { token, label: tokenLabel(token), sub: '成员' }
      })
    if (memberItems.length) out.push({ title: '成员', items: memberItems })

    if (props.groups.length) {
      out.push({
        title: '用户组',
        items: props.groups
          .filter((g) => g.name.trim())
          .map((g) => {
            const token = groupKey(g.name)
            return { token, label: tokenLabel(token), sub: `${g.values.length} 人` }
          }),
      })
    }
  } else {
    out.push({
      title: '特殊',
      items: [
        { token: 'autogroup:self', label: '本人设备', sub: '每个人只能访问自己名下的设备' },
        { token: '*', label: '所有设备', sub: '不推荐，等于全放开' },
      ],
    })

    // 子网路由要单独一组：写路由器名字只能到路由器本身，
    // 要访问它背后的网段，目标必须直接写成网段
    const rs = props.routes ?? []
    if (rs.length) {
      out.push({
        title: '子网路由',
        items: rs.map((r) => ({ token: r.cidr, label: r.cidr, sub: `经 ${r.via}` })),
      })
    }
  }

  if (props.tags.length) {
    out.push({
      title: '标签',
      items: props.tags
        .filter((t) => t.name.trim())
        .map((t) => {
          const token = tagKey(t.name)
          const n = props.peers.filter((p) => p.tags.map(tagKey).includes(token)).length
          return { token, label: tokenLabel(token), sub: n ? `${n} 台设备` : '暂无设备' }
        }),
    })
  }

  if (props.mode === 'dst' && (props.hosts ?? []).length) {
    out.push({
      title: 'hosts 别名',
      items: (props.hosts ?? []).map((h) => ({ token: h, label: h, sub: '策略 hosts 里定义' })),
    })
  }

  if (props.mode === 'dst' && props.peers.length) {
    out.push({
      title: '设备',
      items: props.peers.map((p) => ({
        token: p.name,
        label: p.name,
        sub: [p.user, p.online ? '在线' : '离线'].filter(Boolean).join(' · '),
      })),
    })
  }
  return out
})

const filtered = computed<Group[]>(() => {
  const k = kw.value.trim().toLowerCase()
  if (!k) return allGroups.value
  return allGroups.value
    .map((g) => ({ ...g, items: g.items.filter((o) => (o.label + ' ' + o.token + ' ' + (o.sub ?? '')).toLowerCase().includes(k)) }))
    .filter((g) => g.items.length)
})

// 跟 PortField 一样按实际可用高度定位：写死高度 + 猜方向，窗口一小就会
// 把菜单下半截顶到窗口外面，而 fixed 定位又滚不到
function measure() {
  const el = anchor.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const width = Math.max(280, Math.min(r.width + 40, 360))
  let left = r.left
  if (left + width > window.innerWidth - 12) left = window.innerWidth - width - 12
  const gap = 6
  const margin = 10
  const below = window.innerHeight - r.bottom - gap - margin
  const above = r.top - gap - margin
  const want = 340
  const up = below < want && above > below
  const maxH = Math.max(180, Math.min(want, up ? above : below))
  pos.value = {
    top: up ? 0 : r.bottom + gap,
    bottom: up ? window.innerHeight - r.top + gap : 0,
    left,
    width,
    maxH,
    up,
  }
}

async function toggle() {
  if (props.disabled) return
  open.value = !open.value
  kw.value = ''
  adding.value = ''
  if (open.value) {
    measure()
    await nextTick()
    searchEl.value?.focus()
  }
}

function close() {
  open.value = false
  adding.value = ''
}

function pick(token: string) {
  const next = selected.value.has(token)
    ? props.modelValue.filter((v) => v !== token)
    : [...props.modelValue, token]
  emit('update:modelValue', next)
}

function remove(token: string) {
  emit('update:modelValue', props.modelValue.filter((v) => v !== token))
}

// 搜索框回车：已经能匹配到唯一一项就选中，否则按原样加入（支持手写成员名）
function commitRaw() {
  const k = kw.value.trim()
  if (!k) return
  const items = filtered.value.flatMap((g) => g.items)
  if (items.length === 1) {
    pick(items[0].token)
    kw.value = ''
    return
  }
  const exact = items.find((o) => o.token === k || o.label === k)
  if (exact) {
    pick(exact.token)
    kw.value = ''
    return
  }
  if (k.includes(' ')) return
  // 来源里手打的名字按 headscale 的写法补上 @；目标只认设备名，不能补
  const token = props.mode === 'src' ? userToken(k) : k
  if (!selected.value.has(token)) emit('update:modelValue', [...props.modelValue, token])
  kw.value = ''
}

async function startCreate(kind: 'group' | 'tag') {
  adding.value = kind
  kw.value = ''
  await nextTick()
  searchEl.value?.focus()
}

function commitCreate() {
  const raw = kw.value.trim()
  if (!raw) {
    adding.value = ''
    return
  }
  const prefix = adding.value === 'group' ? 'group:' : 'tag:'
  const name = raw.startsWith(prefix) ? raw : prefix + raw.replace(/^(group|tag):/, '')
  emit('create', adding.value as 'group' | 'tag', name)
  if (!selected.value.has(name)) emit('update:modelValue', [...props.modelValue, name])
  adding.value = ''
  kw.value = ''
}

function onDocDown(e: MouseEvent) {
  if (!open.value) return
  const t = e.target as Node
  if (anchor.value?.contains(t)) return
  if ((t as HTMLElement).closest?.('.tf-menu')) return
  close()
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && open.value) close()
}
// 页面里能滚的是 .content 这类容器，不是 window，所以用捕获阶段监听；
// 弹层自己内部的滚动不算——否则列表一滚就关，接着往下滚就滚到底下的页面了。
function onScroll(e: Event) {
  if (!open.value) return
  const t = e.target as Element | null
  if (t?.closest?.('.tf-menu')) return
  close()
}
onMounted(() => {
  document.addEventListener('mousedown', onDocDown)
  document.addEventListener('keydown', onKey)
  window.addEventListener('resize', onScroll)
  window.addEventListener('scroll', onScroll, true)
})
onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDocDown)
  document.removeEventListener('keydown', onKey)
  window.removeEventListener('resize', onScroll)
  window.removeEventListener('scroll', onScroll, true)
})
</script>

<template>
  <div ref="anchor" class="tf">
    <div class="tf-box" :class="{ open }">
      <span
        v-for="t in modelValue"
        :key="t"
        class="tf-chip"
        :class="'k-' + tokenKind(t)"
        :title="t"
      >
        <span v-if="chipPrefix(t)" class="tf-prefix">{{ chipPrefix(t) }}</span>
        <span class="tf-name">{{ mode === 'dst' ? dstLabel(t) : tokenLabel(t) }}</span>
        <button v-if="!disabled" type="button" class="tf-x" title="移除" @click.stop="remove(t)">×</button>
      </span>
      <button v-if="!disabled" type="button" class="tf-add" @click="toggle">+ 添加</button>
      <span v-else-if="!modelValue.length" class="tf-none">未设置</span>
    </div>

    <Teleport to="body">
      <div
        v-if="open"
        class="tf-menu"
        :style="{
          top: pos.up ? 'auto' : pos.top + 'px',
          bottom: pos.up ? pos.bottom + 'px' : 'auto',
          left: pos.left + 'px',
          width: pos.width + 'px',
          maxHeight: pos.maxH + 'px',
        }"
        @mousedown.stop
      >
        <div class="tf-search">
          <input
            ref="searchEl"
            v-model="kw"
            class="tf-input"
            :placeholder="adding ? '输入名字后回车' : mode === 'dst' ? '搜索设备 / 标签 / 网段，或输入后回车' : '搜索，或直接输入成员名后回车'"
            spellcheck="false"
            @keydown.enter.prevent="adding ? commitCreate() : commitRaw()"
            @keydown.esc.prevent="adding ? (adding = '') : close()"
          />
        </div>

        <div v-if="!adding" class="tf-list">
          <template v-for="g in filtered" :key="g.title">
            <div class="tf-group">{{ g.title }}</div>
            <button
              v-for="o in g.items"
              :key="o.token"
              type="button"
              class="tf-item"
              :class="{ on: selected.has(o.token) }"
              @click="pick(o.token)"
            >
              <span class="tf-item-main">
                <span class="tf-item-label">{{ o.label }}</span>
                <span v-if="o.sub" class="tf-item-sub">{{ o.sub }}</span>
              </span>
              <span class="tf-check">{{ selected.has(o.token) ? '✓' : '' }}</span>
            </button>
          </template>
          <div v-if="!filtered.length" class="tf-empty">没有匹配项，回车按原样添加</div>
        </div>

        <div v-else class="tf-create">
          <span class="tf-create-hint">
            新建{{ adding === 'group' ? '用户组' : '标签' }}：会自动加上
            <code>{{ adding === 'group' ? 'group:' : 'tag:' }}</code> 前缀，回车确认
          </span>
        </div>

        <div class="tf-foot">
          <template v-if="!adding">
            <button type="button" class="tf-foot-btn" @click="startCreate('group')">+ 新建用户组</button>
            <button type="button" class="tf-foot-btn" @click="startCreate('tag')">+ 新建标签</button>
          </template>
          <template v-else>
            <button type="button" class="tf-foot-btn" @click="adding = ''">返回</button>
          </template>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.tf { min-width: 0; }

.tf-box {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 5px;
  min-height: 36px;
  padding: 5px 7px;
  background: var(--surface-2);
  border: 1px solid var(--line);
  border-radius: 8px;
  transition: border-color .15s, box-shadow .15s;
}
.tf-box.open { border-color: var(--brand); box-shadow: 0 0 0 3px var(--brand-soft); }

.tf-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 24px;
  padding: 0 6px 0 7px;
  border-radius: 6px;
  background: var(--surface);
  border: 1px solid var(--line);
  font-size: 11.5px;
  color: var(--text);
  line-height: 1;
  max-width: 100%;
}
.tf-chip.k-group { border-color: var(--brand-line); background: var(--brand-soft); color: var(--brand); }
.tf-chip.k-tag { border-color: color-mix(in srgb, var(--info) 30%, transparent); background: var(--info-soft); color: var(--info); }
.tf-chip.k-all { border-color: color-mix(in srgb, var(--warn) 30%, transparent); background: var(--warn-soft); color: var(--warn); font-weight: 600; }
.tf-chip.k-autogroup { border-color: var(--line); background: var(--surface-3); color: var(--muted); }

.tf-prefix {
  font-size: 9px;
  opacity: .72;
  letter-spacing: .02em;
  flex: none;
}
.tf-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--mono);
  font-size: 11px;
}
.tf-x {
  border: 0;
  background: none;
  padding: 0 1px;
  color: inherit;
  opacity: .55;
  font-size: 13px;
  line-height: 1;
  border-radius: 3px;
}
.tf-x:hover { opacity: 1; background: rgba(0, 0, 0, .08); }

.tf-add {
  height: 24px;
  padding: 0 8px;
  border: 1px dashed var(--line);
  border-radius: 6px;
  background: none;
  color: var(--faint);
  font-size: 11px;
}
.tf-add:hover { color: var(--text); border-color: var(--faint); }
.tf-none { font-size: 11px; color: var(--faint); }

.tf-menu {
  position: fixed;
  overscroll-behavior: contain;
  z-index: 900;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 10px;
  box-shadow: 0 16px 40px rgba(8, 12, 18, .18);
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.tf-search { padding: 7px; border-bottom: 1px solid var(--line-soft); }
.tf-input {
  width: 100%;
  height: 30px;
  padding: 0 9px;
  border: 1px solid var(--line);
  border-radius: 7px;
  background: var(--surface-2);
  color: var(--text);
  font-size: 11.5px;
  outline: none;
}
.tf-input:focus { border-color: var(--brand); box-shadow: 0 0 0 3px var(--brand-soft); }

.tf-list {
  overflow: auto;
  padding: 4px 5px 6px;
  flex: 1;
  min-height: 0;
  /* 滚到列表尽头也别把动作传给底下的页面 */
  overscroll-behavior: contain;
}
.tf-group {
  padding: 7px 6px 4px;
  font-size: 9.5px;
  color: var(--faint);
  letter-spacing: .08em;
}

.tf-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 5px 7px;
  border: 0;
  border-radius: 6px;
  background: none;
  text-align: left;
  cursor: pointer;
}
.tf-item:hover { background: var(--surface-2); }
.tf-item.on { background: var(--brand-soft); }
.tf-item-main { display: flex; align-items: baseline; gap: 7px; min-width: 0; flex: 1; }
.tf-item-label {
  font-family: var(--mono);
  font-size: 11.5px;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tf-item-sub { font-size: 9.5px; color: var(--faint); flex: none; }
.tf-check { font-size: 10px; color: var(--brand); width: 10px; flex: none; }
.tf-empty { padding: 14px 8px; font-size: 11px; color: var(--faint); text-align: center; }

.tf-create { padding: 10px 10px 4px; }
.tf-create-hint { font-size: 10.5px; color: var(--faint); line-height: 1.7; }
.tf-create-hint code {
  font-family: var(--mono);
  font-size: 10px;
  padding: 1px 4px;
  border-radius: 4px;
  background: var(--surface-2);
  border: 1px solid var(--line-soft);
  color: var(--text);
}

.tf-foot {
  display: flex;
  gap: 6px;
  padding: 6px 7px;
  border-top: 1px solid var(--line-soft);
  background: var(--surface-2);
}
.tf-foot-btn {
  flex: 1;
  height: 26px;
  border: 1px solid var(--line);
  border-radius: 6px;
  background: var(--surface);
  color: var(--muted);
  font-size: 10.5px;
}
.tf-foot-btn:hover { color: var(--text); border-color: var(--faint); }
</style>
