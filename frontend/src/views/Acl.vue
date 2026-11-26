<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { api, type State, type PolicyDoc } from '../api'
import { reloadTick } from '../bus'

const props = defineProps<{ state: State | null }>()
const emit = defineEmits(['notify'])
const doc = ref<PolicyDoc>({ content: '', updatedAt: '' })
const check = ref('')
const busy = ref(false)

async function load() {
  try { doc.value = await api.policy() }
  catch (err: any) { emit('notify', '读取 ACL 失败：' + (err?.message ?? err)) }
}
onMounted(load)
watch(reloadTick, load)

function validate() {
  try {
    JSON.parse(doc.value.content)
    check.value = 'JSON 语法通过。服务端仍会做一次 HuJSON 与语义校验。'
    emit('notify', '校验通过')
  } catch (err: any) {
    check.value = '语法错误：' + (err?.message ?? err) + '。若使用了 // 注释（HuJSON），前端校验会失败，可交给服务端校验。'
    emit('notify', '校验未通过')
  }
}
async function save() {
  busy.value = true
  try {
    await api.savePolicy(doc.value.content)
    emit('notify', '策略已保存（已写入本地版本历史）')
    await load()
  } catch (err: any) {
    emit('notify', '保存失败：' + (err?.message ?? err))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <div class="spread" style="margin-bottom:14px">
      <div class="faint" style="font-size:11.5px">
        更新时间 {{ doc.updatedAt || '—' }}
      </div>
      <div class="row">
        <button class="btn" @click="validate">校验</button>
        <button class="btn primary" :disabled="busy" @click="save">保存并应用</button>
      </div>
    </div>

    <div style="display:grid;grid-template-columns:1fr 250px;gap:14px">
      <div>
        <textarea class="codearea" v-model="doc.content" spellcheck="false"
                  placeholder="在此编辑 HuJSON / JSON 策略…"></textarea>
        <div v-if="check" class="notice" :class="check.startsWith('JSON') ? 'info' : 'warn'" style="margin-top:10px">{{ check }}</div>
      </div>
      <div>
        <div class="card tight" style="margin-bottom:12px">
          <h3 class="sec">策略说明</h3>
          <div class="faint" style="font-size:11px;line-height:1.9">
            groups 定义用户组<br>
            tagOwners 定义标签归属<br>
            acls 定义访问规则<br>
            ssh / nodeAttrs 等高级规则以服务端支持为准
          </div>
        </div>
        <div class="notice warn">
          保存会立即影响整张 tailnet。建议先在「设备管理」确认目标设备，再小步修改规则。
        </div>
      </div>
    </div>
  </div>
</template>
