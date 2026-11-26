// 主题：auto 跟随系统，light / dark 手动指定。
// 真正的第一帧由 index.html 的内联脚本处理，这里只负责后续切换与持久化。
import { ref } from 'vue'

export type ThemeMode = 'auto' | 'light' | 'dark'

const KEY = 'weft.theme'

// 全局共享，顶栏按钮和设置页看到的是同一个状态
export const darkMode = ref(false)
export const modeRef = ref<ThemeMode>('auto')

export function themeMode(): ThemeMode {
  const v = localStorage.getItem(KEY)
  return v === 'light' || v === 'dark' ? v : 'auto'
}

export function resolveDark(mode: ThemeMode): boolean {
  if (mode === 'dark') return true
  if (mode === 'light') return false
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

export function applyTheme(mode: ThemeMode) {
  const dark = resolveDark(mode)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  darkMode.value = dark
  modeRef.value = mode
}

export function setTheme(mode: ThemeMode) {
  localStorage.setItem(KEY, mode)
  applyTheme(mode)
}

export const isDark = () => darkMode.value

// 跟随系统时，系统切换要实时跟上
export function watchSystemTheme() {
  const mq = window.matchMedia('(prefers-color-scheme: dark)')
  const onChange = () => {
    if (themeMode() === 'auto') applyTheme('auto')
  }
  mq.addEventListener('change', onChange)
  return () => mq.removeEventListener('change', onChange)
}
