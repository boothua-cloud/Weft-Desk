<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type State, type AppBookmark } from '../api'
import { reloadTick } from '../bus'

const props = defineProps<{ state: State | null }>()
const emit = defineEmits(['notify'])
const apps = ref<AppBookmark[]>([])
const allowInsecure = ref(true)
const hasHTTPS = computed(() => apps.value.some((a) => a.url.trim().toLowerCase().startsWith('https://')))
const keyword = ref('')
const drawer = ref(false)
const form = ref<AppBookmark>({ id: '', name: '', url: '', group: '常用', icon: '#0F6E56', browser: false })
const colors = ['#0F6E56', '#185FA5', '#378ADD', '#D85A30', '#BA7517', '#534AB7']

const shown = computed(() => apps.value.filter(a =>
  !keyword.value || (a.name + a.url + a.group).toLowerCase().includes(keyword.value.toLowerCase())))

async function load() {
  try { apps.value = await api.apps() }
  catch (err: any) { emit('notify', '读取应用失败：' + (err?.message ?? err)) }
}
onMounted(load)
watch(reloadTick, load)

function openForm(a?: AppBookmark) {
  form.value = a ? { ...a } : { id: '', name: '', url: '', group: '常用', icon: colors[0], browser: false }
  drawer.value = true
}
async function submit() {
  if (!form.value.name || !form.value.url) {
    emit('notify', '请填写名称与地址')
    return
  }
  try {
    await api.saveApp(form.value)
    drawer.value = false
    await load()
    emit('notify', '应用已保存到本地数据库')
  } catch (err: any) { emit('notify', '保存失败：' + (err?.message ?? err)) }
}
async function remove(a: AppBookmark) {
  try { await api.deleteApp(a.id); await load(); emit('notify', '已删除 ' + a.name) }
  catch (err: any) { emit('notify', '删除失败：' + (err?.message ?? err)) }
}
async function open(a: AppBookmark) {
  try {
    await api.openApp(a.id)
    emit('notify', '已在新窗口打开 ' + a.name)
  } catch (err: any) { emit('notify', '打开失败：' + (err?.message ?? err)) }
}
async function external(a: AppBookmark) {
  try { await api.openInBrowser(a.id) }
  catch (err: any) { emit('notify', '打开失败：' + (err?.message ?? err)) }
}
</script>

<template>
  <div>
    <div class="spread" style="margin-bottom:18px">
      <div class="faint" style="font-size:11.5px">共 {{ apps.length }} 个内网应用 · 书签保存在本地 SQLite</div>
      <button class="btn primary" @click="openForm()">＋ 添加应用</button>
    </div>

    <div v-if="hasHTTPS && !allowInsecure" class="notice warn" style="margin-bottom:14px">
      有应用使用 HTTPS：自签证书在应用窗口内会被直接拒绝加载（白屏）。到「连接设置」打开「信任自签 HTTPS 证书」即可。
    </div>

    <div class="card tight row" style="margin-bottom:14px">
      <input class="input" style="width:260px" v-model="keyword" placeholder="搜索名称 / 地址 / 分组…" />
      <span class="faint" style="font-size:11px">「应用内打开」由 Wails v3 新开独立 WebView，经 127.0.0.1 本地代理转发到 tailnet，不使用 iframe</span>
    </div>

    <div class="grid3">
      <div v-for="a in shown" :key="a.id" class="appcard">
        <div class="row">
          <div class="ico" :style="{ background: a.icon || '#0F6E56' }">{{ a.name.slice(0, 2) }}</div>
          <div style="min-width:0">
            <b>{{ a.name }}</b>
            <div class="url mono" style="color:var(--faint);font-size:10.5px;margin-top:3px">{{ a.url }}</div>
          </div>
          <span v-if="a.browser" class="tag amber" style="margin-left:auto" title="用系统浏览器打开">浏览器</span>
          <span v-else class="tag" style="margin-left:auto">{{ a.group }}</span>
        </div>
        <div class="row" style="gap:6px">
          <button class="btn sm" style="flex:1" @click="open(a)">{{ a.browser ? '浏览器打开' : '应用内打开' }}</button>
          <button class="btn sm ghost" title="系统浏览器" @click="external(a)">↗</button>
          <button class="btn sm ghost" title="编辑" @click="openForm(a)">✎</button>
          <button class="btn sm ghost" title="删除" @click="remove(a)">✕</button>
        </div>
      </div>
      <div class="appcard" style="border-style:dashed;background:var(--surface-2);cursor:pointer;align-items:center;justify-content:center;min-height:130px"
           @click="openForm()">
        <div style="font-size:22px;color:var(--faint)">＋</div>
        <b>添加应用</b>
        <div class="faint" style="font-size:10.5px">名称 · 地址 · 分组 · 图标</div>
      </div>
    </div>

    <template v-if="drawer">
      <div class="mask" @click="drawer = false"></div>
      <div class="drawer">
        <div class="spread" style="margin-bottom:20px">
          <b style="font-size:14px">{{ form.id ? '编辑应用' : '添加应用' }}</b>
          <button class="btn ghost sm" @click="drawer = false">✕</button>
        </div>
        <div class="field"><label class="field-label">应用名称</label><input class="input" v-model="form.name" placeholder="群晖 NAS" /></div>
        <div class="field"><label class="field-label">访问地址</label><input class="input mono" v-model="form.url" placeholder="http://100.64.0.3:5000" /></div>
        <div class="spread card tight" style="margin-bottom:14px">
          <div>
            <b style="font-size:12px">用系统浏览器打开</b>
            <div class="faint" style="font-size:10.5px;margin-top:3px">需要浏览器扩展（如密码管理器）时开启</div>
          </div>
          <button class="switch" :class="{ on: form.browser }" @click="form.browser = !form.browser"></button>
        </div>
        <div class="field"><label class="field-label">分组</label><input class="input" v-model="form.group" placeholder="常用" /></div>
        <div class="field">
          <label class="field-label">图标颜色</label>
          <div class="row">
            <button v-for="c in colors" :key="c" class="swatch" :class="{ active: form.icon === c }"
                    :style="{ background: c }" @click="form.icon = c"></button>
          </div>
        </div>
        <button class="btn primary" style="width:100%;height:38px" @click="submit">保存</button>
        <div class="notice info" style="margin-top:16px">
          地址支持 100.x IP、MagicDNS 名称或自定义域名。打开时 Go 侧会自动分配一个 127.0.0.1 端口做反向代理；系统浏览器按钮同样走该代理，只有云栈在运行期间有效。
        </div>
      </div>
    </template>
  </div>
</template>
