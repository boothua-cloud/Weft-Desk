<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, type State } from '../api'

// 首次启动向导：一台新机器上要填的东西一共三样——服务地址、两把钥匙。
// 都在设置页里能找到，但那是「已经知道要填什么」的人才找得到的地方。
// 这里按顺序问一遍，跳过随时可以，跳过后不再自动出现。
defineProps<{ state: State | null }>()

const emit = defineEmits<{
  (e: 'close'): void // 跳过 / 关掉
  (e: 'done'): void
  (e: 'notify', msg: string): void
}>()

const step = ref(0)
const name = ref('')
const baseURL = ref('')
const selfSigned = ref(false)
const apiKey = ref('')
const authKey = ref('')
const saving = ref(false)
const testing = ref(false)
const testText = ref('')
const testOk = ref(false)
const connecting = ref(false)

const titles = ['服务端地址', '两把钥匙', '检查一下']
const leads = [
  '填 Headscale 的地址，通常长这样：https://headscale.公司.com 。不知道就问管理员。',
  '都可以先跳过。管理 Key 决定能不能看到设备、人员和策略；注册 Key 只影响本机怎么入网。',
  '先确认这个地址真的连得上。连不上也不用重来，可以在「更多 → 连接设置」里改。',
]

const plainHTTP = computed(() => /^http:\/\//i.test(baseURL.value.trim()))
// 名字留空就用域名，不用逼人现场起名字
const autoName = computed(() => {
  const raw = baseURL.value.trim().replace(/^https?:\/\//i, '').replace(/\/.*$/, '')
  return raw.replace(/:\d+$/, '') || '默认服务端'
})
const displayName = computed(() => name.value.trim() || autoName.value)

function skip() {
  emit('close')
}

async function next() {
  if (step.value === 0) {
    const v = baseURL.value.trim()
    if (!/^https?:\/\/[^\s/]+/i.test(v)) {
      emit('notify', '服务地址要带 http:// 或 https://，例如 https://headscale.公司.com')
      return
    }
    step.value = 1
    return
  }
  if (step.value === 1) {
    await save()
    return
  }
  emit('done')
}

async function save() {
  if (saving.value) return
  saving.value = true
  try {
    const id = await api.saveProfileRaw(JSON.stringify({
      id: '',
      name: displayName.value,
      baseURL: baseURL.value.trim(),
      hostname: '',
      apiKey: apiKey.value.trim(),
      authKey: authKey.value.trim(),
      allowInsecure: selfSigned.value,
      caCert: '',
      // 打开「自签证书」但没贴 CA：只跳过校验。贴 CA 的路径在设置页的编辑弹窗里
      controlInsecure: selfSigned.value,
    }))
    // 新建服务端默认只是加进列表，这里要明确切过去，否则向导填的东西不生效
    if (id) await api.switchProfile(id)
    name.value = displayName.value
    step.value = 2
    window.dispatchEvent(new CustomEvent('weft:profiles-changed'))
    test()
  } catch (err: any) {
    emit('notify', '保存失败：' + (err?.message ?? err))
  } finally {
    saving.value = false
  }
}

async function test() {
  testing.value = true
  testText.value = ''
  try {
    testText.value = await api.testConnection()
    testOk.value = true
  } catch (err: any) {
    testText.value = String(err?.message ?? err)
    testOk.value = false
  } finally {
    testing.value = false
  }
}

async function connect() {
  connecting.value = true
  try {
    await api.connect()
    emit('notify', '正在建立隧道，首次入网可能要你去浏览器确认')
    emit('done')
  } catch (err: any) {
    emit('notify', '连接失败：' + (err?.message ?? err))
  } finally {
    connecting.value = false
  }
}
</script>

<template>
  <div class="modal-mask" @click.self="skip">
    <div class="modal wiz">
      <header class="wiz-head">
        <div class="wiz-top">
          <span class="wiz-badge">第 {{ step + 1 }} / 3 步</span>
          <button class="modal-x" title="跳过" @click="skip">✕</button>
        </div>
        <h2>{{ titles[step] }}</h2>
        <p class="wiz-lead">{{ leads[step] }}</p>
      </header>

      <section v-if="step === 0" class="wiz-body">
        <div class="wiz-field">
          <label class="field-label">服务地址 <span class="wiz-en">Base URL</span></label>
          <input class="input mono" v-model="baseURL" autofocus
                 placeholder="https://headscale.example.com"
                 @keyup.enter="next" />
          <p v-if="plainHTTP" class="wiz-hint warn">
            明文 http：管理 Key 会以请求头的形式在网络上直传，只在完全可信的内网里这么填。
          </p>
        </div>

        <div class="wiz-field">
          <label class="field-label">显示名称 <span class="wiz-en">可选</span></label>
          <input class="input" v-model="name" :placeholder="autoName" />
        </div>

        <div class="wiz-row">
          <div style="min-width:0">
            <span class="wiz-switch-title">服务端用自签证书</span>
            <p class="wiz-hint">先跳过校验。有 CA 的话保存后去「连接设置」里贴，只认那一份</p>
          </div>
          <button class="switch" :class="{ on: selfSigned }" @click="selfSigned = !selfSigned"></button>
        </div>
      </section>

      <section v-else-if="step === 1" class="wiz-body">
        <div class="wiz-field">
          <label class="field-label">管理 API Key <span class="wiz-en">可选</span></label>
          <input class="input mono" v-model="apiKey" placeholder="hskey-api-…" @keyup.enter="next" />
          <p class="wiz-hint">
            服务端上执行 <code>headscale apikeys create</code> 生成，<code>hskey-</code> 开头。
            填了才会出现设备管理、人员管理、授权 Key 和 ACL 策略
          </p>
        </div>

        <div class="wiz-field">
          <label class="field-label">节点注册密钥 <span class="wiz-en">Auth Key · 可选</span></label>
          <input class="input mono" v-model="authKey" placeholder="留空就走浏览器授权" @keyup.enter="next" />
          <p class="wiz-hint">
            留空也能用：总览页会给一个登录链接，在浏览器里点确认就完成入网。
            服务端如果关掉了网页登录，再来这里填 <code>headscale preauthkeys create</code> 生成的 Key
          </p>
        </div>
      </section>

      <section v-else class="wiz-body">
        <div class="wiz-check" :class="testing ? '' : testOk ? 'ok' : 'bad'">
          <div class="wiz-check-head">
            <span v-if="testing" class="spinner"></span>
            <b>{{ testing ? '正在测试服务端…' : testOk ? '服务端可达' : '连不上这个地址' }}</b>
            <button class="btn sm" style="margin-left:auto" :disabled="testing" @click="test">重测</button>
          </div>
          <p class="wiz-check-text">{{ testText || (testing ? '稍等，正在握手…' : '') }}</p>
          <p v-if="!testing && !testOk" class="wiz-hint">
            地址写错、证书不对、或者服务端没起来，都会卡在这里。可以先跳过，去「更多 → 连接设置」里慢慢改。
          </p>
        </div>

        <div class="wiz-summary">
          <div><span>服务端</span><b>{{ displayName }}</b></div>
          <div><span>地址</span><b class="mono">{{ baseURL.trim() }}</b></div>
          <div><span>管理 Key</span><b :class="{ faint: !apiKey.trim() }">{{ apiKey.trim() ? '已填' : '没填，管理功能不可见' }}</b></div>
          <div><span>注册方式</span><b :class="{ faint: !authKey.trim() }">{{ authKey.trim() ? 'Auth Key' : '浏览器授权' }}</b></div>
        </div>
      </section>

      <footer class="wiz-foot">
        <button v-if="step < 2" class="wiz-skip" @click="skip">跳过，稍后再配</button>
        <div class="row" style="gap:9px;margin-left:auto">
          <button v-if="step === 1" class="btn" @click="step = 0">上一步</button>
          <button v-if="step === 0" class="btn primary" @click="next">下一步</button>
          <button v-if="step === 1" class="btn primary" :disabled="saving" @click="next">
            <span v-if="saving" class="spinner"></span>{{ saving ? '保存中…' : '保存并测试' }}
          </button>
          <template v-if="step === 2">
            <button v-if="!state?.connected && !state?.connecting" class="btn" :disabled="connecting" @click="connect">
              <span v-if="connecting" class="spinner"></span>连接隧道
            </button>
            <span v-else class="faint" style="font-size:10.5px;align-self:center">
              {{ state?.connected ? '隧道已连上' : '正在连接，首次入网要去浏览器确认' }}
            </span>
            <button class="btn primary" @click="emit('done')">开始使用</button>
          </template>
        </div>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.modal.wiz {
  width: min(520px, 100%);
  padding: 26px 28px 20px;
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.wiz-head { display: flex; flex-direction: column; gap: 7px; }
.wiz-top { display: flex; align-items: center; justify-content: space-between; }
.wiz-badge {
  font-size: 10px;
  letter-spacing: .08em;
  color: var(--faint);
  border: 1px solid var(--line);
  border-radius: 999px;
  padding: 2px 9px;
}
.wiz-head h2 { margin: 0; font-size: 16px; font-weight: 650; }
.wiz-lead { margin: 0; font-size: 11.5px; line-height: 1.7; color: var(--faint); }

.wiz-body { display: flex; flex-direction: column; gap: 16px; min-height: 176px; }
.wiz-field .field-label { margin-bottom: 7px; }
.wiz-en { color: var(--faint); font-weight: 400; font-size: 10px; margin-left: 2px; }
.wiz-hint { margin: 7px 0 0; font-size: 10.5px; line-height: 1.7; color: var(--faint); }
.wiz-hint.warn { color: var(--warn); }
.wiz-hint code { font-family: var(--mono); font-size: 10px; }
.wiz-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding-top: 14px;
  border-top: 1px solid var(--line-soft);
}
.wiz-switch-title { font-size: 11.5px; }

.wiz-check {
  border: 1px solid var(--line);
  border-radius: var(--r);
  padding: 12px 14px;
  background: var(--surface-2);
}
.wiz-check.ok { border-color: color-mix(in srgb, var(--ok) 28%, transparent); background: var(--ok-soft); }
.wiz-check.bad { border-color: color-mix(in srgb, var(--danger) 28%, transparent); background: var(--danger-soft); }
.wiz-check-head { display: flex; align-items: center; gap: 8px; font-size: 12px; }
.wiz-check.ok .wiz-check-head b { color: var(--ok); }
.wiz-check.bad .wiz-check-head b { color: var(--danger); }
.wiz-check-text {
  margin: 8px 0 0;
  font-size: 11px;
  line-height: 1.7;
  color: var(--muted);
  word-break: break-word;
  max-height: 96px;
  overflow: auto;
}
.wiz-summary {
  display: grid;
  gap: 7px;
  font-size: 11px;
}
.wiz-summary > div { display: flex; align-items: baseline; gap: 10px; }
.wiz-summary span { color: var(--faint); min-width: 62px; flex: none; }
.wiz-summary b { font-weight: 500; word-break: break-all; }
.wiz-summary b.faint { color: var(--faint); font-weight: 400; }

.wiz-foot { display: flex; align-items: center; gap: 12px; }
.wiz-skip {
  background: none;
  border: 0;
  padding: 0;
  cursor: pointer;
  font-size: 10.5px;
  color: var(--faint);
}
.wiz-skip:hover { color: var(--text); }
</style>
