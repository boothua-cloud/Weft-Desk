import { ref } from 'vue'

// 各页面共用的二次确认状态。用法：
//   const { confirmBox, askConfirm } = useConfirm()
//   askConfirm({ title, desc, ok, action })
export type ConfirmBox = {
  title: string
  desc?: string
  ok?: string
  cancel?: string
  action: () => Promise<void> | void
}

export function useConfirm() {
  const confirmBox = ref<ConfirmBox | null>(null)
  const askConfirm = (box: ConfirmBox) => { confirmBox.value = box }
  const closeConfirm = () => { confirmBox.value = null }
  return { confirmBox, askConfirm, closeConfirm }
}
