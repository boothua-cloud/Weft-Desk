<script setup lang="ts">
import { ref } from 'vue'

// 确认框只负责「问一句再执行」，具体做什么由 action 决定，
// 失败提示由 action 自己 emit，这里不吞异常也不重复提示。
const props = defineProps<{
  title: string
  desc?: string
  ok?: string
  cancel?: string
  tone?: 'danger' | 'primary'
  action: () => Promise<void> | void
}>()
const emit = defineEmits(['close'])

const busy = ref(false)

async function go() {
  if (busy.value) return
  busy.value = true
  try {
    await props.action()
  } finally {
    busy.value = false
    emit('close')
  }
}
</script>

<template>
  <div class="modal-mask" style="z-index:70" @click.self="!busy && emit('close')">
    <div class="modal modal-confirm">
      <b class="confirm-title">{{ title }}</b>
      <p v-if="desc" class="confirm-desc">{{ desc }}</p>
      <div class="row" style="justify-content:flex-end;gap:8px;margin-top:20px">
        <button class="btn" :disabled="busy" @click="emit('close')">{{ cancel || '取消' }}</button>
        <button class="btn" :class="tone === 'primary' ? 'primary' : 'danger'" :disabled="busy" @click="go">
          <span v-if="busy" class="spinner" style="margin-right:6px"></span>{{ ok || '删除' }}
        </button>
      </div>
    </div>
  </div>
</template>
