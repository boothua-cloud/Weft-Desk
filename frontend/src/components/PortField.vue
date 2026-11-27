<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { SERVICES, normalizePorts, portLabel, portShort } from '../acl'

// 端口选择器：把「22/443」翻译成「SSH/HTTPS」，常用服务点一下就选中，
// 特殊端口仍然能手填（含 8000-8100 这种范围）。
const props = defineProps<{ modelValue: string[]; disabled?: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string[]): void }>()

const open = ref(false)
const draft = ref('')
const anchor = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLInputElement | null>(null)
// up 为真时用 bottom 定位：从锚点往上长，菜单再高也不会越过窗口上沿
const pos = ref({ top: 0, bottom: 0, left: 0, width: 250, maxH: 360, up: false })

const selected = computed(() => new Set(props.modelValue))
const isEmpty = computed(() => !props.modelValue.length)

// 菜单以前是「写死 296px 高 + 按 270px 猜往上还是往下」，窗口一小，
// 下半截（全部端口、自定义输入框）就被窗口底边切掉，而菜单是 fixed 的，
// 页面怎么滚都露不出来。现在按实际可用高度算，菜单整体不超过窗口。
function measure() {
  const el = anchor.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const width = 260
  let left = r.left
  if (left + width > window.innerWidth - 12) left = window.innerWidth - width - 12
  const gap = 6
  const margin = 10
  const below = window.innerHeight - r.bottom - gap - margin
  const above = r.top - gap - margin
  const want = 360
  const up = below < want && above > below
  // 至少留 180：窗口特别矮时宁可自己滚，也别把内容藏起来
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
  draft.value = ''
  if (open.value) {
    measure()
    await nextTick()
    inputEl.value?.focus()
  }
}

function close() { open.value = false }

function pick(port: string) {
  // 「全部端口」和其他端口互斥，选它就清空其余
  if (port === '*') {
    emit('update:modelValue', selected.value.has('*') ? [] : ['*'])
    return
  }
  if (selected.value.has('*')) {
    emit('update:modelValue', [port])
    return
  }
  const next = selected.value.has(port)
    ? props.modelValue.filter((v) => v !== port)
    : [...props.modelValue, port]
  emit('update:modelValue', normalizePorts(next))
}

function remove(port: string) {
  emit('update:modelValue', props.modelValue.filter((v) => v !== port))
}

function commitDraft() {
  const raw = draft.value.trim()
  if (!raw) return
  // 支持 "8080" / "8000-8100" / "22,80" 一次填多个
  const parts = raw.split(/[,，\s]+/).filter(Boolean)
  const ok = parts.filter((p) => /^(\*|\d{1,5}(-\d{1,5})?)$/.test(p))
  if (!ok.length) return
  emit('update:modelValue', normalizePorts([...props.modelValue, ...ok]))
  draft.value = ''
}

function onDocDown(e: MouseEvent) {
  if (!open.value) return
  const t = e.target as Node
  if (anchor.value?.contains(t)) return
  if ((t as HTMLElement).closest?.('.pf-menu')) return
  close()
}
function onKey(e: KeyboardEvent) { if (e.key === 'Escape' && open.value) close() }
// 页面里能滚的是 .content 这类容器，不是 window，所以用捕获阶段监听；
// 弹层自己内部的滚动不算
function onScroll(e: Event) {
  if (!open.value) return
  const t = e.target as Element | null
  if (t?.closest?.('.pf-menu')) return
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
  <div ref="anchor" class="pf">
    <div class="pf-box" :class="{ open, empty: isEmpty }">
      <span
        v-for="p in modelValue"
        :key="p"
        class="pf-chip"
        :class="{ all: p === '*' }"
        :title="portLabel(p)"
      >
        {{ portShort(p) }}
        <button v-if="!disabled" type="button" class="pf-x" :title="'移除 ' + portLabel(p)" @click.stop="remove(p)">×</button>
      </span>
      <button v-if="!disabled" type="button" class="pf-add" @click="toggle">
        {{ isEmpty ? '选择端口' : '+' }}
      </button>
      <span v-else-if="isEmpty" class="pf-none">未设置</span>
    </div>

    <Teleport to="body">
      <div
        v-if="open"
        class="pf-menu"
        :style="{
          top: pos.up ? 'auto' : pos.top + 'px',
          bottom: pos.up ? pos.bottom + 'px' : 'auto',
          left: pos.left + 'px',
          width: pos.width + 'px',
          maxHeight: pos.maxH + 'px',
        }"
        @mousedown.stop
      >
        <div class="pf-group">常用服务</div>
        <div class="pf-list">
          <button
            v-for="s in SERVICES"
            :key="s.port"
            type="button"
            class="pf-item"
            :class="{ on: selected.has(s.port) }"
            @click="pick(s.port)"
          >
            <span class="pf-item-label">{{ s.label }}</span>
            <span class="pf-item-port">{{ s.port }}</span>
            <span class="pf-check">{{ selected.has(s.port) ? '✓' : '' }}</span>
          </button>
        </div>

        <div class="pf-sep"></div>
        <button type="button" class="pf-item wide" :class="{ on: selected.has('*') }" @click="pick('*')">
          <span class="pf-item-label">全部端口</span>
          <span class="pf-item-port">不限制</span>
          <span class="pf-check">{{ selected.has('*') ? '✓' : '' }}</span>
        </button>

        <div class="pf-custom">
          <input
            ref="inputEl"
            v-model="draft"
            class="pf-input"
            placeholder="自定义：8080 或 8000-8100，回车"
            spellcheck="false"
            @keydown.enter.prevent="commitDraft"
            @keydown.esc.prevent="close"
          />
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.pf { min-width: 0; }

.pf-box {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  min-height: 36px;
  padding: 5px 7px;
  background: var(--surface-2);
  border: 1px solid var(--line);
  border-radius: 8px;
  transition: border-color .15s, box-shadow .15s;
}
.pf-box.open { border-color: var(--brand); box-shadow: 0 0 0 3px var(--brand-soft); }
.pf-box.empty { border-style: dashed; }

.pf-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 24px;
  padding: 0 5px 0 7px;
  border-radius: 6px;
  background: var(--surface);
  border: 1px solid var(--line);
  font-size: 11px;
  font-family: var(--mono);
  color: var(--text);
  line-height: 1;
}
.pf-chip.all { border-color: color-mix(in srgb, var(--warn) 30%, transparent); background: var(--warn-soft); color: var(--warn); font-weight: 600; }
.pf-x {
  border: 0;
  background: none;
  padding: 0 1px;
  color: inherit;
  opacity: .55;
  font-size: 12px;
  line-height: 1;
}
.pf-x:hover { opacity: 1; }

.pf-add {
  height: 24px;
  min-width: 24px;
  padding: 0 7px;
  border: 1px dashed var(--line);
  border-radius: 6px;
  background: none;
  color: var(--faint);
  font-size: 11px;
}
.pf-add:hover { color: var(--text); border-color: var(--faint); }
.pf-none { font-size: 11px; color: var(--faint); }

.pf-menu {
  position: fixed;
  overscroll-behavior: contain;
  z-index: 900;
  display: flex;
  flex-direction: column;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 10px;
  box-shadow: 0 16px 40px rgba(8, 12, 18, .18);
  overflow: hidden;
}
.pf-group {
  padding: 8px 10px 3px;
  font-size: 9.5px;
  color: var(--faint);
  letter-spacing: .08em;
}
.pf-list {
  /* 高度由菜单的 maxHeight 决定，列表自己吃掉剩余空间并滚动 */
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 0 5px 4px;
  overscroll-behavior: contain;
}
.pf-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: 28px;
  padding: 0 7px;
  border: 0;
  border-radius: 6px;
  background: none;
  text-align: left;
}
.pf-item:hover { background: var(--surface-2); }
.pf-item.on { background: var(--brand-soft); }
.pf-item-label { font-size: 11.5px; color: var(--text); flex: 1; }
.pf-item.on .pf-item-label { color: var(--brand); font-weight: 600; }
.pf-item-port { font-family: var(--mono); font-size: 10px; color: var(--faint); }
.pf-check { font-size: 10px; color: var(--brand); width: 10px; }
.pf-item.wide { margin: 0 5px 4px; width: auto; }
.pf-sep { height: 1px; background: var(--line-soft); margin: 2px 0 4px; }

.pf-custom { padding: 6px 7px 7px; border-top: 1px solid var(--line-soft); background: var(--surface-2); }
.pf-input {
  width: 100%;
  height: 28px;
  padding: 0 8px;
  border: 1px solid var(--line);
  border-radius: 6px;
  background: var(--surface);
  color: var(--text);
  font-size: 11px;
  font-family: var(--mono);
  outline: none;
}
.pf-input:focus { border-color: var(--brand); box-shadow: 0 0 0 3px var(--brand-soft); }
</style>
