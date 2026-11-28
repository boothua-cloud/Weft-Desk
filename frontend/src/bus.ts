import { ref } from 'vue'

// 顶栏「刷新」按钮的全局信号：计数变化时各页面重新拉自己的数据。
// 用模块级 ref 而不是 props，避免每个页面都多接一个属性。
export const reloadTick = ref(0)

export function requestReload() {
  reloadTick.value++
}

// 命令面板搜到设备后，把名字交给设备页填进搜索框。
// 页签切换和搜索框都在 Manage 里，命令面板够不着，只能走这里。
export const pendingDeviceQuery = ref('')
