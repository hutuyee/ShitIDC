<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NInput, NModal, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import EntityPicker from './EntityPicker.vue'

// 产品转移弹窗（对齐魔方 CBAP HostTransfer 插件）。
// 选择目标用户 + 备注后执行迁移：同一订单的关联产品会一起转移，订单信息不动。

const props = defineProps<{
  show: boolean
  service: any | null
}>()
const emit = defineEmits<{
  (e: 'update:show', v: boolean): void
  (e: 'done', moved: number): void
}>()

const message = useMessage()
const toUser = ref('')
const toUserLabel = ref('')
const remark = ref('')
const busy = ref(false)

const visible = computed({
  get: () => props.show,
  set: (v: boolean) => emit('update:show', v),
})

watch(() => props.show, (v) => {
  if (v) {
    toUser.value = ''
    toUserLabel.value = ''
    remark.value = ''
  }
})

function onPicked(hit: any) {
  toUserLabel.value = hit?.email || hit?.name || hit?.title || ''
}

async function submit() {
  if (!props.service?.id) return
  if (!toUser.value) {
    message.warning('请选择目标用户')
    return
  }
  busy.value = true
  try {
    const d = dataOf<any>(await api.post('/admin/service-transfers', {
      service_id: props.service.id,
      to_user: toUser.value,
      remark: remark.value.trim(),
    }))
    const moved = Number(d?.moved || 0)
    message.success(moved > 1 ? `已转移 ${moved} 个关联产品` : '产品已转移')
    emit('done', moved)
    visible.value = false
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '转移失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <NModal v-model:show="visible" preset="card" title="产品转移" style="width:min(560px,95vw)">
    <div style="display:flex;flex-direction:column;gap:12px">
      <NAlert type="warning" :bordered="false">
        同一订单的关联产品会一起转移；订单、账单与支付记录不迁移。转移后原用户立即失去这些产品。
      </NAlert>
      <div>
        <p class="muted" style="margin:0 0 6px">转移产品</p>
        <div class="transfer-target">
          <b>{{ service?.product_name || '—' }}</b>
          <small class="muted">{{ service?.id ? String(service.id).slice(0, 8) : '' }}{{ service?.user_email ? ' · 当前用户 ' + service.user_email : '' }}</small>
        </div>
      </div>
      <div>
        <p class="muted" style="margin:0 0 6px">目标用户</p>
        <EntityPicker v-model="toUser" kind="user" placeholder="按邮箱 / UID / UUID 搜索用户" :selected-label="toUserLabel" @picked="onPicked" />
      </div>
      <div>
        <p class="muted" style="margin:0 0 6px">备注（可选）</p>
        <NInput v-model:value="remark" type="textarea" :rows="2" :maxlength="200" placeholder="记录转移原因，便于追溯" />
      </div>
      <NButton type="primary" block :loading="busy" @click="submit">确认转移</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.transfer-target {
  display: flex;
  flex-direction: column;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: color-mix(in srgb, var(--background) 45%, var(--panel));
}
.transfer-target small { margin-top: 2px; }
</style>
