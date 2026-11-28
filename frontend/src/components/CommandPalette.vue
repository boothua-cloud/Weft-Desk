<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { api, type AppBookmark, type Device, type State } from '../api'
import { pendingDeviceQuery } from '../bus'

// ⌘K 面板：页面、动作、设备、应用都在一个框里找。
// 只做「跳过去 / 做一下」这两类事，参数多的操作仍然回原页面，
// 不然面板会长成第二个界面。
const props = defineProps<{ state: State | null }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'navigate', page: string): void
  (e: 'command', name: string): void
  (e: 'notify', msg: string): void
}>()

const q = ref('')
const active = ref(0)
const inputEl = ref<HTMLInputElement | null>(null)
const listEl = ref<HTMLElement | null>(null)
const devices = ref<Device[]>([])
const apps = ref<AppBookmark[]>([])

type Group = '页面' | '动作' | '设备' | '应用'
interface Item {
  key: string
  group: Group
  label: string
  sub?: string
  hint?: string
  run: () => void
}

const PAGES: { key: string; label: string }[] = [
  { key: 'overview', label: '隧道总览' },
  { key: 'devices', label: '设备管理' },
  { key: 'routes', label: '子网路由' },
  { key: 'users', label: '人员管理' },
  { key: 'keys', label: '授权 Key' },
  { key: 'api', label: 'API Key' },
  { key: 'acl', label: 'ACL 策略' },
  { key: 'apps', label: '应用管理' },
  { key: 'forwards', label: '端口转发' },
  { key: 'diagnose', label: '连接诊断' },
  { key: 'audit', label: '操作记录' },
  { key: 'settings', label: '连接设置' },
]

const items = computed<Item[]>(() => {
  const out: Item[] = []
  for (const p of PAGES) {
    out.push({
      key: 'page:' + p.key,
      group: '页面',
      label: p.label,
      sub: '打开这个页面',
      run: () => emit('navigate', p.key),
    })
  }

  const connected = !!props.state?.connected
  const act = (name: string, label: string, sub: string, hint?: string) => {
    out.push({ key: 'cmd:' + name, group: '动作', label, sub, hint, run: () => emit('command', name) })
  }
  act('refresh', '刷新', '重新读隧道状态和各页列表', '⌘R')
  if (props.state?.connecting) {
    act('disconnect', '取消连接', '断开当前正在建立的连接')
  } else if (connected) {
    act('disconnect', '断开连接', '停掉隧道，本机不再占用 100.x')
  } else {
    act('connect', '连接', '建立隧道，之后 ssh、mysql 能直连 100.x')
  }
  act('theme', '切换主题', '浅色和深色之间换一下')
  act('bundle', '生成诊断包', '脱敏后打包成 zip，可以直接发给别人')
  act('setup', '初始化向导', '重新填一遍服务端地址和两把钥匙')
  act('help', '帮助手册', '每一页在做什么、怎么配')
  act('update', '检查更新', '有新版本会提示，也可以手动查')

  for (const d of devices.value) {
    out.push({
      key: 'dev:' + d.id,
      group: '设备',
      label: d.name || d.hostname || d.ip,
      sub: [d.ip, d.user, d.status].filter(Boolean).join(' · '),
      run: () => {
        // 跳到设备页并把搜索框填上，省得再打一遍名字
        pendingDeviceQuery.value = d.name || d.ip
        emit('navigate', 'devices')
      },
    })
  }

  for (const a of apps.value) {
    out.push({
      key: 'app:' + a.id,
      group: '应用',
      label: a.name,
      sub: a.url,
      run: async () => {
        emit('close')
        try {
          await api.openApp(a.id)
        } catch (err: any) {
          emit('notify', '打开失败：' + (err?.message ?? err))
        }
      },
    })
  }
  return out
})

// 命中规则：包含 > 子序列。子序列让「设备管」也能找到「设备管理」，
// 输入设备名里断开的几个字同样管用。
function score(text: string, s: string): number {
  if (!s) return 1
  const t = text.toLowerCase()
  const i = t.indexOf(s)
  if (i >= 0) return 1000 - i
  let ti = 0
  for (const ch of s) {
    const k = t.indexOf(ch, ti)
    if (k < 0) return -1
    ti = k + 1
  }
  return 100 - t.length
}

const GOAL: Record<Group, number> = { 动作: 3, 页面: 2, 设备: 1, 应用: 0 }

const matched = computed<Item[]>(() => {
  const s = q.value.trim().toLowerCase()
  if (!s) {
    // 空输入时给最常用的：动作排前面，页面跟在后面，设备应用不铺
    return items.value.filter((i) => i.group === '动作' || i.group === '页面')
  }
  const scored = items.value
    .map((it) => ({ it, n: score(`${it.label} ${it.sub ?? ''}`, s) }))
    .filter((x) => x.n >= 0)
    .map((x) => ({ ...x, n: x.n + GOAL[x.it.group] }))
    .sort((a, b) => b.n - a.n)
  return scored.slice(0, 24).map((x) => x.it)
})

// 键盘走到哪一条，滚动就把它带进视野
watch(active, async () => {
  await nextTick()
  const el = listEl.value?.querySelector<HTMLElement>('.cp-item.on')
  el?.scrollIntoView({ block: 'nearest' })
})
watch(q, () => { active.value = 0 })

function move(step: number) {
  const n = matched.value.length
  if (!n) return
  active.value = (active.value + step + n) % n
}

function run(it?: Item) {
  const item = it ?? matched.value[active.value]
  if (!item) return
  item.run()
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') { e.preventDefault(); move(1) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1) }
  else if (e.key === 'Enter') { e.preventDefault(); run() }
  else if (e.key === 'Escape') { e.preventDefault(); emit('close') }
}

onMounted(async () => {
  inputEl.value?.focus()
  // 数据是附带的，取不到就只剩页面和动作，不影响用
  try { devices.value = (await api.devices()).items } catch { /* 没连上就没有设备 */ }
  try { apps.value = await api.apps() } catch { /* 没配管理 Key 就没有书签 */ }
})
</script>

<template>
  <div class="cp-mask" @mousedown.self="emit('close')">
    <div class="cp" @keydown="onKey">
      <div class="cp-head">
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"
             stroke-linecap="round"><circle cx="7" cy="7" r="4.5"/><path d="M10.4 10.4 14 14"/></svg>
        <input ref="inputEl" v-model="q" class="cp-input" spellcheck="false"
               placeholder="搜页面、设备、应用，或直接说做什么" />
        <button class="cp-x" title="关闭" @click="emit('close')">esc</button>
      </div>

      <div ref="listEl" class="cp-list">
        <template v-for="(it, i) in matched" :key="it.key">
          <div v-if="i === 0 || matched[i - 1].group !== it.group" class="cp-group">{{ it.group }}</div>
          <button type="button" class="cp-item" :class="{ on: i === active }"
                  @mouseenter="active = i" @click="run(it)">
            <span class="cp-label">{{ it.label }}</span>
            <span v-if="it.sub" class="cp-sub">{{ it.sub }}</span>
            <span v-if="it.hint" class="cp-hint">{{ it.hint }}</span>
          </button>
        </template>
        <div v-if="!matched.length" class="cp-none">
          没有对上的东西。试试「设备」「连接」「诊断」，或者直接打设备名。
        </div>
      </div>

      <div class="cp-foot">
        <span><b>↑↓</b> 选</span><span><b>↵</b> 执行</span><span><b>esc</b> 关闭</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cp-mask {
  position: fixed;
  inset: 0;
  z-index: 1000;
  background: rgba(8, 12, 18, .32);
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding-top: 12vh;
  animation: cp-fade .12s ease-out;
}
@keyframes cp-fade { from { opacity: 0 } to { opacity: 1 } }

.cp {
  width: 560px;
  max-width: calc(100vw - 64px);
  max-height: 62vh;
  display: flex;
  flex-direction: column;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 14px;
  box-shadow: 0 24px 60px rgba(8, 12, 18, .28);
  overflow: hidden;
}

.cp-head {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--line-soft);
  color: var(--faint);
}
.cp-input {
  flex: 1;
  min-width: 0;
  border: 0;
  background: none;
  outline: none;
  color: var(--text);
  font-size: 13.5px;
  font-family: inherit;
}
.cp-input::placeholder { color: var(--faint); }
.cp-x {
  border: 1px solid var(--line);
  border-radius: 5px;
  background: none;
  color: var(--faint);
  font-size: 10px;
  padding: 2px 5px;
  font-family: var(--mono);
}
.cp-x:hover { color: var(--text); }

.cp-list {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 4px 6px 6px;
  overscroll-behavior: contain;
}
.cp-group {
  padding: 8px 8px 3px;
  font-size: 9.5px;
  letter-spacing: .08em;
  color: var(--faint);
}
.cp-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 7px 9px;
  border: 0;
  border-radius: 7px;
  background: none;
  text-align: left;
}
.cp-item.on { background: var(--brand-soft); }
.cp-label { font-size: 12.5px; color: var(--text); white-space: nowrap; }
.cp-item.on .cp-label { color: var(--brand); font-weight: 600; }
.cp-sub {
  flex: 1;
  min-width: 0;
  font-size: 11px;
  color: var(--faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cp-hint {
  font-family: var(--mono);
  font-size: 10px;
  color: var(--faint);
  border: 1px solid var(--line);
  border-radius: 4px;
  padding: 1px 4px;
}
.cp-none { padding: 18px 10px 22px; font-size: 12px; color: var(--faint); text-align: center; line-height: 1.7; }

.cp-foot {
  display: flex;
  gap: 14px;
  padding: 7px 14px;
  border-top: 1px solid var(--line-soft);
  background: var(--surface-2);
  font-size: 10.5px;
  color: var(--faint);
}
.cp-foot b { font-weight: 600; color: var(--text); }
</style>
