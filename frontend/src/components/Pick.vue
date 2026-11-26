<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

type Opt = { value: string | number; label: string }

const props = defineProps<{
  modelValue: string | number
  options: Opt[]
  placeholder?: string
  disabled?: boolean
}>()
const emit = defineEmits(['update:modelValue'])

const open = ref(false)
const root = ref<HTMLElement | null>(null)
const current = computed(() => props.options.find((o) => o.value === props.modelValue))

function pick(o: Opt) {
  emit('update:modelValue', o.value)
  open.value = false
}
function onDocDown(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false
}
onMounted(() => {
  document.addEventListener('mousedown', onDocDown)
  document.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDocDown)
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div ref="root" class="pick">
    <button
      type="button"
      class="select"
      :class="{ open }"
      :disabled="disabled"
      @click="open = !open"
    >
      <span class="pick-label">{{ current?.label ?? placeholder ?? '请选择' }}</span>
      <span class="pick-caret">▾</span>
    </button>

    <div v-if="open" class="pick-menu">
      <button
        v-for="o in options"
        :key="String(o.value)"
        type="button"
        class="pick-item"
        :class="{ active: o.value === modelValue }"
        @click="pick(o)"
      >
        <span class="pick-text">{{ o.label }}</span>
        <span v-if="o.value === modelValue" class="pick-check">✓</span>
      </button>
      <div v-if="!options.length" class="pick-empty">没有可选项</div>
    </div>
  </div>
</template>

<style scoped>
.pick {
  position: relative;
  width: 100%;
}

.pick-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pick-caret {
  margin-left: auto;
  color: var(--faint);
  font-size: 10px;
  line-height: 1;
  transition: transform 0.15s;
}

.select.open .pick-caret {
  transform: rotate(180deg);
}

.pick-menu {
  position: absolute;
  z-index: 40;
  left: 0;
  right: 0;
  top: 38px;
  padding: 4px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 9px;
  box-shadow: 0 10px 28px rgba(16, 24, 40, 0.12);
  max-height: 240px;
  overflow: auto;
}

.pick-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: 32px;
  padding: 0 9px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  font-size: 12px;
  color: var(--text);
  text-align: left;
  cursor: pointer;
}

.pick-item:hover {
  background: var(--surface-2);
}

.pick-item.active {
  background: var(--brand-soft);
  color: var(--brand);
  font-weight: 600;
}

.pick-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pick-check {
  margin-left: auto;
  font-size: 11px;
}

.pick-empty {
  padding: 10px;
  font-size: 11px;
  color: var(--faint);
  text-align: center;
}
</style>
