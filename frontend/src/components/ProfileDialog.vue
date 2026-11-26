<script setup lang="ts">
import { ref, watch } from 'vue'
import { api } from '../api'

type Row = { id: string; name: string; baseURL: string; hostname?: string; allowInsecure?: boolean }

const props = defineProps<{ profile: Row | null }>()
const emit = defineEmits(['close', 'saved', 'notify'])

const name = ref('')
const baseURL = ref('')
const hostname = ref('')
const apiKey = ref('')
const authKey = ref('')
const allowInsecure = ref(false)
const saving = ref(false)

watch(
  () => props.profile,
  (p) => {
    name.value = p?.name ?? ''
    baseURL.value = p?.baseURL ?? ''
    hostname.value = p?.hostname ?? ''
    allowInsecure.value = !!p?.allowInsecure
    apiKey.value = ''
    authKey.value = ''
  },
  { immediate: true },
)

async function save() {
  if (saving.value) return
  if (!name.value.trim()) { emit('notify', '给这个服务端起个名字'); return }
  if (!baseURL.value.trim()) { emit('notify', '服务地址不能为空'); return }
  saving.value = true
  try {
    await api.saveProfileRaw(JSON.stringify({
      id: props.profile?.id ?? '',
      name: name.value.trim(),
      baseURL: baseURL.value.trim(),
      hostname: hostname.value.trim(),
      apiKey: apiKey.value,
      authKey: authKey.value,
      allowInsecure: allowInsecure.value,
    }))
    emit('saved')
  } catch (err: any) {
    emit('notify', '保存失败：' + (err?.message ?? err))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="modal-mask" @click.self="emit('close')">
    <div class="modal" style="width:min(520px,100%)">
      <button class="modal-x" @click="emit('close')">✕</button>
      <h2 class="help-title" style="margin-bottom:6px">{{ profile?.id ? '编辑服务端' : '新增服务端' }}</h2>
      <p class="help-lead" style="margin:0 0 18px">
        {{ profile?.id ? '改完保存会立即重连（如果改的是当前用的那个）' : '每个服务端有独立的节点身份，互不影响' }}
      </p>

      <div class="grid2" style="gap:12px;margin-bottom:12px">
        <div>
          <label class="field-label">显示名称</label>
          <input class="input" v-model="name" placeholder="公司内网 / 测试环境" />
        </div>
        <div>
          <label class="field-label">本节点名称 Hostname</label>
          <input class="input mono" v-model="hostname" placeholder="留空用本机名" />
        </div>
      </div>

      <div style="margin-bottom:12px">
        <label class="field-label">服务地址 Base URL</label>
        <input class="input mono" v-model="baseURL" placeholder="https://headscale.example.com" />
      </div>

      <div class="grid2" style="gap:12px;margin-bottom:12px">
        <div>
          <label class="field-label">管理 API Key</label>
          <input class="input" type="password" v-model="apiKey"
                 :placeholder="profile?.id ? '留空表示不修改' : 'hskey-apikey-…'" />
        </div>
        <div>
          <label class="field-label">隧道注册 Auth Key</label>
          <input class="input" type="password" v-model="authKey"
                 :placeholder="profile?.id ? '留空表示不修改' : '可留空走浏览器授权'" />
        </div>
      </div>

      <div class="spread" style="padding:10px 0;border-top:1px solid var(--line-soft);margin-top:4px">
        <div>
          <span style="font-size:11.5px">信任自签 HTTPS 证书</span>
          <div class="faint" style="font-size:10.5px;margin-top:3px">内网应用用自签证书时，应用内窗口不报错</div>
        </div>
        <button class="switch" :class="{ on: allowInsecure }" @click="allowInsecure = !allowInsecure"></button>
      </div>

      <div class="row" style="justify-content:flex-end;gap:8px;margin-top:18px">
        <button class="btn" @click="emit('close')">取消</button>
        <button class="btn primary" :disabled="saving" @click="save">
          <span v-if="saving" class="spinner"></span>{{ saving ? '保存中…' : '保存' }}
        </button>
      </div>
    </div>
  </div>
</template>
