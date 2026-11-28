<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps<{
  modelValue: string[]
  placeholder?: string
  suggestions?: string[]
  disabled?: boolean
  /** 提交时把用户手打的值变成规范写法，例如成员名补上 headscale 要求的 @ */
  normalize?: (v: string) => string
  /** 展示用：值可能是 alice@ / group:devs，显示成更好读的样子 */
  display?: (v: string) => string
}>()
const emit = defineEmits(['update:modelValue'])

const draft = ref('')
const focused = ref(false)
const open = ref(false)
const active = ref(0)
// 用户按过上下键才把回车当成「选中建议」，否则回车就是「确认自己打的字」
const navigating = ref(false)
const root = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLInputElement | null>(null)
const items = computed(() => props.modelValue ?? [])
const norm = (v: string) => (props.normalize ? props.normalize(v) : v)
const label = (v: string) => (props.display ? props.display(v) : v)

const options = computed(() => {
  const chosen = new Set(items.value)
  const kw = draft.value.trim().toLowerCase()
  const seen = new Set<string>()
  const pool: string[] = []
  for (const raw of props.suggestions ?? []) {
    const s = raw.trim()
    if (!s || chosen.has(s) || seen.has(s)) continue
    if (s === draft.value.trim()) continue
    // 搜索按展示名匹配：打字时不会去打那个 @
    if (kw && !(label(s).toLowerCase().includes(kw) || s.toLowerCase().includes(kw))) continue
    seen.add(s)
    pool.push(s)
  }
  // 前缀命中的排前面，其余保持原顺序
  const head = pool.filter((s) => label(s).toLowerCase().startsWith(kw))
  const tail = pool.filter((s) => !label(s).toLowerCase().startsWith(kw))
  return [...head, ...tail].slice(0, 8)
})
const showMenu = computed(() => open.value && focused.value && !props.disabled && options.value.length > 0)

function commit() {
  const parts = draft.value.split(/[,，\n]/).map((s) => norm(s.trim())).filter(Boolean)
  draft.value = ''
  navigating.value = false
  if (!parts.length) return
  const next = items.value.slice()
  for (const p of parts) if (!next.includes(p)) next.push(p)
  if (next.length !== items.value.length) emit('update:modelValue', next)
}

function pick(value: string) {
  const v = norm(value)
  if (!items.value.includes(v)) emit('update:modelValue', [...items.value, v])
  draft.value = ''
  // 选中项会从候选里消失，列表整体上移一格；高亮跟着停在原位置，
  // 不能重置成 0，否则鼠标没动、高亮却跳回顶部
  active.value = Math.min(active.value, Math.max(0, options.value.length - 1))
  navigating.value = false
  open.value = true
  inputEl.value?.focus()
}

function onInput() {
  open.value = true
  navigating.value = false
  active.value = 0
}

function onKeydown(e: KeyboardEvent) {
  if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && showMenu.value) {
    e.preventDefault()
    const n = options.value.length
    active.value = (active.value + (e.key === 'ArrowDown' ? 1 : -1) + n) % n
    navigating.value = true
    return
  }
  if (e.key === 'Enter') {
    e.preventDefault()
    if (navigating.value && showMenu.value) pick(options.value[active.value])
    else commit()
    return
  }
  if (e.key === ',') {
    e.preventDefault()
    commit()
    return
  }
  if (e.key === 'Escape') {
    open.value = false
    return
  }
  if (e.key === 'Tab') {
    commit()
    return
  }
  if (e.key === 'Backspace' && !draft.value && items.value.length) {
    emit('update:modelValue', items.value.slice(0, -1))
  }
}

function onPaste(e: ClipboardEvent) {
  const text = e.clipboardData?.getData('text') ?? ''
  if (/[,，\n]/.test(text)) {
    e.preventDefault()
    draft.value = text
    commit()
  }
}

function remove(i: number) {
  const next = items.value.slice()
  next.splice(i, 1)
  emit('update:modelValue', next)
}

function onDocDown(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) {
    open.value = false
    focused.value = false
  }
}
onMounted(() => document.addEventListener('mousedown', onDocDown))
onBeforeUnmount(() => document.removeEventListener('mousedown', onDocDown))
</script>

<template>
  <div ref="root" class="chips-wrap">
    <div class="chips" :class="{ focused, disabled }" @click="inputEl?.focus()">
      <span v-for="(v, i) in items" :key="v" class="chip-item" :title="v">
        {{ label(v) }}
        <button v-if="!disabled" type="button" class="chip-x" :title="'移除 ' + v" @click.stop="remove(i)">×</button>
      </span>
      <input
        ref="inputEl"
        v-model="draft"
        class="chip-input"
        :disabled="disabled"
        :placeholder="items.length ? '' : (placeholder ?? '输入后回车')"
        autocomplete="off"
        spellcheck="false"
        @input="onInput"
        @keydown="onKeydown"
        @focus="focused = true; open = true; active = 0"
        @blur="focused = false; commit()"
        @paste="onPaste"
      />
    </div>

    <div v-if="showMenu" class="chips-menu" @mousedown.stop>
      <button
        v-for="(o, i) in options"
        :key="o"
        type="button"
        class="chips-opt"
        :class="{ active: i === active, used: items.includes(o) }"
        @mousedown.prevent="pick(o)"
        @mouseenter="active = i"
      >
        <span class="chips-opt-text">{{ label(o) }}</span>
        <span class="chips-opt-ico">↵</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.chips-wrap {
  position: relative;
  min-width: 0;
}

.chips {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 5px;
  min-height: 36px;
  padding: 4px 7px;
  border: 1px solid var(--line);
  border-radius: var(--r-sm);
  background: var(--surface);
  cursor: text;
  transition: border-color 0.15s, box-shadow 0.15s;
}
.chips.focused {
  border-color: var(--brand);
  box-shadow: 0 0 0 3px var(--brand-soft);
}
.chips.disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.chip-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 100%;
  padding: 2px 5px 2px 8px;
  border-radius: 6px;
  background: var(--surface-2);
  border: 1px solid var(--line);
  font-family: var(--mono);
  font-size: 10.5px;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chip-x {
  border: 0;
  background: none;
  padding: 0;
  width: 14px;
  height: 14px;
  line-height: 1;
  font-size: 12px;
  color: var(--faint);
  border-radius: 4px;
}
.chip-x:hover {
  color: var(--danger);
  background: var(--danger-soft);
}
.chip-input {
  flex: 1;
  min-width: 76px;
  height: 24px;
  border: 0;
  outline: none;
  background: transparent;
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text);
}
.chip-input::placeholder {
  font-family: inherit;
  color: var(--faint);
}

.chips-menu {
  position: absolute;
  z-index: 40;
  left: 0;
  top: calc(100% + 4px);
  width: 100%;
  min-width: 200px;
  padding: 4px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 9px;
  box-shadow: var(--shadow-pop);
  max-height: 236px;
  overflow: auto;
  /* 滚到尽头别把动作传给底下的页面 */
  overscroll-behavior: contain;
}
.chips-opt {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: 30px;
  padding: 0 9px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--text);
  text-align: left;
  cursor: pointer;
}
.chips-opt.active {
  background: var(--brand-soft);
  color: var(--brand);
}
.chips-opt-text {
  font-family: var(--mono);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chips-opt-ico {
  margin-left: auto;
  font-size: 9px;
  color: var(--faint);
  opacity: 0;
}
.chips-opt.active .chips-opt-ico {
  opacity: 0.9;
}
</style>
