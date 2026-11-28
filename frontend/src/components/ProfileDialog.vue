<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../api'

type Row = { id: string; name: string; baseURL: string; hostname?: string; allowInsecure?: boolean; caCert?: string; controlInsecure?: boolean }

const props = defineProps<{ profile: Row | null }>()
const emit = defineEmits(['close', 'saved', 'notify'])

const name = ref('')
const baseURL = ref('')
const hostname = ref('')
const apiKey = ref('')
const authKey = ref('')
const allowInsecure = ref(false)
// 控制面是自签证书：贴了 CA 就只认这份 CA，没贴就跳过校验
const selfSigned = ref(false)
const caCert = ref('')
const saving = ref(false)
// 已存的密钥默认不显示，点「显示」才从钥匙串读明文
const shownKey = ref<Record<string, string>>({})
// 「显示」失败的原因是字段级的（比如根本没存过），
// 所以贴在字段下面，不走全局提示条 —— 那条在弹窗遮罩底下看不见。
const revealErr = ref<Record<string, string>>({})
// 「留空不改」没法表达「删掉」，所以清空要单独打标记，保存时告诉后端真的删
const clearApi = ref(false)
const clearAuth = ref(false)

watch(
  () => props.profile,
  (p) => {
    name.value = p?.name ?? ''
    baseURL.value = p?.baseURL ?? ''
    hostname.value = p?.hostname ?? ''
    allowInsecure.value = !!p?.allowInsecure
    caCert.value = p?.caCert ?? ''
    selfSigned.value = !!p?.caCert || !!p?.controlInsecure
    apiKey.value = ''
    authKey.value = ''
    shownKey.value = {}
    revealErr.value = {}
    clearApi.value = false
    clearAuth.value = false
  },
  { immediate: true },
)
// 一旦开始输入新的钥匙，就不再是「清空」的意思
watch(apiKey, (v) => { if (v.trim()) clearApi.value = false })
watch(authKey, (v) => { if (v.trim()) clearAuth.value = false })

// 只看形状，不联网：明显不像钥匙的当场提示，省得保存完再去服务端页面看 401。
// 真的行不行仍然由服务端说了算（保存后状态里会带出结论）。
const keyHint = computed(() => {
  const v = apiKey.value.trim()
  if (!v) return ''
  if (v.startsWith('hskey-auth-') || v.startsWith('tskey-auth-')) return 'auth'
  if (!/^(hskey|tskey)-/.test(v)) return 'shape'
  return ''
})

// 点「显示」时从钥匙串读一次明文填进输入框；再点一次清空回密码态
async function toggleReveal(kind: 'api' | 'auth') {
  if (shownKey.value[kind]) {
    shownKey.value = { ...shownKey.value, [kind]: '' }
    if (kind === 'api') apiKey.value = ''
    else authKey.value = ''
    return
  }
  try {
    const v = await api.revealCredential(kind)
    if (kind === 'api') apiKey.value = v
    else authKey.value = v
    shownKey.value = { ...shownKey.value, [kind]: v }
    revealErr.value = { ...revealErr.value, [kind]: '' }
  } catch (err: any) {
    const msg = String(err?.message ?? err)
    revealErr.value = {
      ...revealErr.value,
      [kind]: msg.includes('尚未设置')
        ? `这个服务端还没保存 ${kind === 'api' ? 'API Key' : 'Auth Key'}`
        : '读取失败：' + msg,
    }
  }
}

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
      clearApiKey: clearApi.value,
      clearAuthKey: clearAuth.value,
      allowInsecure: allowInsecure.value,
      caCert: selfSigned.value ? caCert.value.trim() : '',
      controlInsecure: selfSigned.value && !caCert.value.trim(),
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
    <div class="modal pf">
      <button class="modal-x" @click="emit('close')">✕</button>

      <header class="pf-head">
        <h2>{{ profile?.id ? '编辑服务端' : '新增服务端' }}</h2>
        <p class="pf-lead">
          {{ profile?.id ? '保存后立即重连（当前服务端）' : '每个服务端有独立的节点身份，互不影响' }}
        </p>
      </header>

      <section class="pf-sec">
        <div class="pf-grid">
          <div class="pf-field">
            <label class="field-label">显示名称</label>
            <input class="input" v-model="name" placeholder="公司内网 / 测试环境" />
          </div>
          <div class="pf-field">
            <label class="field-label">本节点名称 <span class="pf-en">Hostname</span></label>
            <input class="input mono" v-model="hostname" placeholder="留空用本机名" />
          </div>
        </div>
        <div class="pf-field">
          <label class="field-label">服务地址 <span class="pf-en">Base URL</span></label>
          <input class="input mono" v-model="baseURL" placeholder="https://headscale.example.com" />
        </div>
      </section>

      <section class="pf-sec">
        <div class="pf-sec-title">凭证</div>

        <div class="pf-field">
          <div class="pf-label-row">
            <label class="field-label">节点注册密钥 <span class="pf-en">Auth Key</span></label>
            <div class="pf-actions">
              <button v-if="profile?.id" type="button" class="pf-link" @click="toggleReveal('auth')">
                {{ shownKey.auth ? '隐藏' : '显示' }}
              </button>
              <button v-if="profile?.id" type="button" class="pf-link" :class="{ danger: clearAuth }"
                      @click="clearAuth = !clearAuth; clearAuth && (authKey = '')">
                {{ clearAuth ? '取消清空' : '清空' }}
              </button>
            </div>
          </div>
          <input class="input" :type="shownKey.auth ? 'text' : 'password'" v-model="authKey"
                 :disabled="clearAuth"
                 :placeholder="clearAuth ? '保存后清空' : profile?.id ? '留空表示不修改' : '可留空，走浏览器授权'" />
          <p v-if="clearAuth" class="pf-hint warn">保存后删除，下次连接改为浏览器授权</p>
          <p v-else-if="revealErr.auth" class="pf-hint warn">{{ revealErr.auth }}</p>
          <p v-else class="pf-hint">留空则走服务端授权：总览页给出登录链接，在浏览器里确认即可完成注册</p>
        </div>

        <div class="pf-field">
          <div class="pf-label-row">
            <label class="field-label">管理密钥 <span class="pf-en">API Key</span></label>
            <div class="pf-actions">
              <button v-if="profile?.id" type="button" class="pf-link" @click="toggleReveal('api')">
                {{ shownKey.api ? '隐藏' : '显示' }}
              </button>
              <button v-if="profile?.id" type="button" class="pf-link" :class="{ danger: clearApi }"
                      @click="clearApi = !clearApi; clearApi && (apiKey = '')">
                {{ clearApi ? '取消清空' : '清空' }}
              </button>
            </div>
          </div>
          <input class="input" :type="shownKey.api ? 'text' : 'password'" v-model="apiKey"
                 :disabled="clearApi"
                 :placeholder="clearApi ? '保存后清空' : profile?.id ? '留空表示不修改' : 'hskey-api-…'" />
          <p v-if="clearApi" class="pf-hint warn">保存后删除该 Key，设备管理、人员管理、密钥管理和 ACL 策略将不再显示</p>
          <p v-else-if="revealErr.api" class="pf-hint warn">{{ revealErr.api }}</p>
          <p v-else-if="keyHint === 'auth'" class="pf-hint warn">
            这是节点注册用的 Auth Key，管理接口不接受；请填 <code>headscale apikeys create</code> 生成的 Key
          </p>
          <p v-else-if="keyHint === 'shape'" class="pf-hint warn">
            格式不正确：headscale 的管理 Key 以 <code>hskey-</code> 开头，保存后会被服务端拒绝
          </p>
          <p v-else class="pf-hint">填了可用 Key 才会显示设备管理、人员管理、密钥管理和 ACL 策略</p>
        </div>
      </section>

      <section class="pf-sec">
        <div class="pf-sec-title">控制面证书</div>
        <div class="pf-inline" style="padding-top:0;border-top:0">
          <div style="min-width:0">
            <span class="pf-switch-title">服务端用自签证书</span>
            <p class="pf-hint" style="margin-top:4px">地址是 https、证书不是公网 CA 签的，就打开</p>
          </div>
          <button class="switch" :class="{ on: selfSigned }" @click="selfSigned = !selfSigned"></button>
        </div>
        <div v-if="selfSigned" class="pf-field">
          <label class="field-label">CA 证书 <span class="pf-en">PEM</span></label>
          <textarea class="input mono" rows="4" v-model="caCert"
                    placeholder="-----BEGIN CERTIFICATE-----"></textarea>
          <p class="pf-hint">
            贴了只认这份 CA；留空就不校验证书，能连上但挡不住中间人。
            caddy 的 CA 在 <code>~/.local/share/caddy/pki/authorities/local/root.crt</code>，
            mkcert 的用 <code>mkcert -CAROOT</code> 找。
          </p>
        </div>
      </section>

      <section class="pf-sec pf-inline">
        <div style="min-width:0">
          <span class="pf-switch-title">信任内网应用的自签证书</span>
          <p class="pf-hint" style="margin-top:4px">应用窗口打开内网 https 站点时不报错</p>
        </div>
        <button class="switch" :class="{ on: allowInsecure }" @click="allowInsecure = !allowInsecure"></button>
      </section>

      <footer class="pf-foot">
        <button class="btn" @click="emit('close')">取消</button>
        <button class="btn primary" :disabled="saving" @click="save">
          <span v-if="saving" class="spinner"></span>{{ saving ? '保存中…' : '保存' }}
        </button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
/* 之前所有字段挤在 520px 里、每格只差 12px，右边那列标签和左边几乎贴在一起。
   这里统一放宽：弹窗加宽到 620px，字段间 14px，分组之间 20px。 */
.modal.pf {
  width: min(620px, 100%);
  padding: 28px 30px 24px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.pf-head h2 { margin: 0; font-size: 15px; font-weight: 650; }
.pf-lead { margin: 7px 0 0; font-size: 11.5px; color: var(--faint); line-height: 1.65; }
.pf-sec { display: flex; flex-direction: column; gap: 16px; }
.pf-sec-title {
  font-size: 10.5px; color: var(--faint); letter-spacing: .08em;
  text-transform: uppercase; margin-bottom: -2px;
}
.pf-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.pf-field { min-width: 0; }
.pf-field .field-label { margin-bottom: 7px; }
.pf-en { color: var(--faint); font-weight: 400; font-size: 10px; margin-left: 2px; }
.pf-label-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 7px; }
.pf-label-row .field-label { margin-bottom: 0; }
.pf-actions { display: flex; gap: 12px; flex: none; }
.pf-link {
  background: none; border: 0; padding: 0; cursor: pointer;
  font-size: 10.5px; color: var(--faint); line-height: 1;
}
.pf-link:hover { color: var(--text); }
.pf-link.danger { color: var(--warn); }
.pf-hint { margin: 7px 0 0; font-size: 10.5px; line-height: 1.65; color: var(--faint); }
.pf-hint.warn { color: var(--warn); }
.pf-hint code { font-family: var(--mono); font-size: 10px; }
.pf-inline {
  flex-direction: row; align-items: center; justify-content: space-between;
  gap: 16px; padding-top: 18px; border-top: 1px solid var(--line-soft);
}
.pf-switch-title { font-size: 11.5px; }
.pf-foot { display: flex; justify-content: flex-end; gap: 9px; margin-top: 2px; }

@media (max-width: 620px) {
  .pf-grid { grid-template-columns: 1fr; }
}
</style>
