<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Events } from '@wailsio/runtime'
import { api, type State, type UpdateProgress, type UpdateStatus } from '../api'

const props = defineProps<{ state?: State | null }>()
const emit = defineEmits(['close', 'notify'])

// checking：正在查；idle：查完了（看 st 决定显示最新还是可用）
// downloading：下载中；manual：安装位置不可写，只能给安装包
const phase = ref<'checking' | 'idle' | 'downloading' | 'manual' | 'error'>('checking')
const st = ref<UpdateStatus | null>(null)
const errMsg = ref('')
const percent = ref(0)
const rateText = ref('')
let offProgress: (() => void) | undefined

const current = computed(() => st.value?.currentVersion || props.state?.appVersion || '')
const hasUpdate = computed(() => !!st.value?.hasUpdate)
const canAuto = computed(() => !!st.value?.canAutoUpdate)
const ready = computed(() => !!st.value?.readyToRestart)

// Release 的正文是 Markdown。这里不引渲染库，把标记去掉按纯文本读，
// 对更新说明这种短文本足够，也避免给 webview 塞一个 md 解析器。
const notes = computed(() => {
  const raw = st.value?.notes || ''
  return raw
    .split('\n')
    .map((line) =>
      line
        .replace(/^#{1,6}\s*/, '')
        .replace(/^\s*[-*+]\s+/, '· ')
        .replace(/\*\*(.+?)\*\*/g, '$1')
        .replace(/`([^`]+)`/g, '$1')
        .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1'),
    )
    .join('\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim()
})

function fmtSize(n: number) {
  if (!n) return ''
  return n >= 1048576 ? `${(n / 1048576).toFixed(1)} MB` : `${Math.round(n / 1024)} KB`
}

function msg(e: any) {
  return String(e?.message ?? e ?? '未知错误')
}

async function check() {
  phase.value = 'checking'
  errMsg.value = ''
  try {
    const r = await api.checkUpdate()
    st.value = r
    if (r.state === 'error' && !r.hasUpdate) {
      phase.value = 'error'
      errMsg.value = r.error || '检查失败'
      return
    }
    phase.value = 'idle'
  } catch (e) {
    phase.value = 'error'
    errMsg.value = msg(e)
  }
}

// 自动更新：下载 → 暂存 → 重启生效。整个过程由框架的后台助手完成，
// 本进程在重启那一步退出，重启后就是新版本。
async function download() {
  phase.value = 'downloading'
  percent.value = 0
  rateText.value = ''
  try {
    const r = await api.downloadUpdate()
    st.value = r
    phase.value = 'idle'
  } catch (e) {
    phase.value = 'error'
    errMsg.value = msg(e)
  }
}

async function restart() {
  try {
    await api.applyUpdate()
    // 进程马上会被助手结束，这里不用再改界面
  } catch (e) {
    phase.value = 'error'
    errMsg.value = msg(e)
  }
}

// 安装位置不可写时（mac 的 /Applications、deb 的 /usr/bin）只能下安装包，
// 交给系统安装器；装完用户自己重开。
async function installer() {
  phase.value = 'downloading'
  percent.value = 0
  rateText.value = ''
  try {
    const path = await api.downloadInstaller()
    phase.value = 'manual'
    emit('notify', '安装包已下载：' + path)
  } catch (e) {
    phase.value = 'error'
    errMsg.value = msg(e)
  }
}

function openPage() {
  api.openReleasePage().catch((e: any) => emit('notify', '打开失败：' + msg(e)))
}

onMounted(async () => {
  try {
    offProgress = Events.On('weft:update-progress', (ev: any) => {
      const p = (ev?.data ?? {}) as UpdateProgress
      if (typeof p.percent === 'number') percent.value = Math.min(100, Math.max(0, p.percent))
      rateText.value = p.rate > 0 ? `${(p.rate / 1048576).toFixed(1)} MB/s` : ''
    })
  } catch { /* 事件系统不可用时只是没有进度，不影响流程 */ }
  await check()
})

onUnmounted(() => {
  try { offProgress?.() } catch { /* 忽略 */ }
})
</script>

<template>
  <div class="modal-mask" @click.self="emit('close')">
    <div class="modal modal-update">
      <button class="modal-x" @click="emit('close')">✕</button>

      <div class="upd-head">
        <img src="/appicon.png" alt="云栈" class="upd-logo" draggable="false" />
        <div>
          <b class="upd-title">检查更新</b>
          <div class="faint" style="font-size:11px;margin-top:3px">当前版本 v{{ current }}</div>
        </div>
      </div>

      <!-- 正在查 -->
      <div v-if="phase === 'checking'" class="upd-body upd-center">
        <span class="spinner"></span>
        <span class="faint" style="margin-left:10px">正在检查新版本…</span>
      </div>

      <!-- 已经是最新 -->
      <div v-else-if="phase === 'idle' && !hasUpdate" class="upd-body upd-center">
        <span class="upd-ok">✓</span>
        <div>
          <b style="font-size:13px">已是最新版本</b>
          <div class="faint" style="font-size:11px;margin-top:4px">v{{ current }} 就是当前发布的最新版</div>
        </div>
      </div>

      <!-- 下载完成，等重启 -->
      <template v-else-if="ready">
        <div class="upd-body upd-center">
          <span class="upd-ok">✓</span>
          <div>
            <b style="font-size:13px">v{{ st?.latestVersion }} 已准备好</b>
            <div class="faint" style="font-size:11px;margin-top:4px">
              重启后生效。隧道会短暂断开，重连后自动恢复。
            </div>
          </div>
        </div>
        <div class="upd-foot">
          <button class="btn ghost sm" @click="emit('close')">稍后</button>
          <button class="btn primary sm" @click="restart">立即重启</button>
        </div>
      </template>

      <!-- 发现新版本 -->
      <template v-else-if="phase === 'idle' && hasUpdate">
        <div class="upd-body">
          <div class="upd-ver">
            <b>发现新版本 v{{ st?.latestVersion }}</b>
            <span v-if="st?.publishedAt" class="faint">{{ st.publishedAt }}</span>
          </div>
          <div v-if="notes" class="upd-notes">{{ notes }}</div>
          <div v-else class="faint" style="font-size:11.5px">这个版本没有附更新说明。</div>

          <div v-if="!canAuto" class="upd-hint">
            <template v-if="st?.manualReason === 'permission'">
              安装位置没有写权限（{{ state?.platform === 'darwin' ? '通常在 /Applications' : '通常是系统目录' }}），
              会下载安装包，按提示装完重新打开即可。
            </template>
            <template v-else>
              这个版本没有提供自动更新包，会下载安装包手动安装。
            </template>
          </div>
          <div v-else class="upd-hint">
            下载 <span class="mono">{{ st?.assetName }}</span>
            <template v-if="st?.assetSize">（{{ fmtSize(st.assetSize) }}）</template>，
            完成后重启生效。
          </div>
        </div>
        <div class="upd-foot">
          <button class="btn ghost sm" @click="openPage">发布页</button>
          <button class="btn ghost sm" @click="check">重新检查</button>
          <button v-if="canAuto" class="btn primary sm" @click="download">下载并安装</button>
          <button v-else class="btn primary sm" @click="installer">下载安装包</button>
        </div>
      </template>

      <!-- 下载中 -->
      <template v-else-if="phase === 'downloading'">
        <div class="upd-body">
          <div class="upd-bar">
            <i :style="{ width: percent.toFixed(1) + '%' }"></i>
          </div>
          <div class="upd-progress-line">
            <span class="faint">{{ percent.toFixed(0) }}%</span>
            <span class="faint">{{ rateText }}</span>
          </div>
        </div>
      </template>

      <!-- 安装包已下载 -->
      <template v-else-if="phase === 'manual'">
        <div class="upd-body upd-center">
          <span class="upd-ok">✓</span>
          <div>
            <b style="font-size:13px">安装包已下载并打开</b>
            <div class="faint" style="font-size:11px;margin-top:4px">
              按安装程序的提示完成，然后重新打开云栈。
            </div>
          </div>
        </div>
        <div class="upd-foot">
          <button class="btn ghost sm" @click="openPage">发布页</button>
          <button class="btn primary sm" @click="emit('close')">完成</button>
        </div>
      </template>

      <!-- 出错 -->
      <template v-else>
        <div class="upd-body">
          <b style="font-size:13px;color:var(--danger)">检查更新失败</b>
          <p class="upd-err">{{ errMsg }}</p>
          <p class="faint" style="font-size:11px;margin-top:8px">
            离线或网络受限时可以到发布页手动下载。
          </p>
        </div>
        <div class="upd-foot">
          <button class="btn ghost sm" @click="openPage">发布页</button>
          <button class="btn primary sm" @click="check">重试</button>
        </div>
      </template>
    </div>
  </div>
</template>
