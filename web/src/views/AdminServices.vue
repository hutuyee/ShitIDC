<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NSelect, NTag, useDialog, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const dialog = useDialog()
const services = ref<any[]>([])
const status = ref('')
const loading = ref(false)
const busy = ref('')

const statusText: Record<string, string> = {
  pending: '待开通', provisioning: '开通中', active: '生效中', suspending: '暂停中',
  unsuspending: '恢复中', suspended: '已暂停', terminating: '删除中', terminated: '已删除', failed: '开通失败',
}
const statusType = (s: string) => ({ active: 'success', provisioning: 'info', pending: 'default', suspended: 'warning', failed: 'error' } as any)[s] || 'default'
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')

const statusOptions = [
  { label: '全部状态', value: '' },
  { label: '生效中', value: 'active' },
  { label: '已暂停', value: 'suspended' },
  { label: '待开通', value: 'pending' },
  { label: '开通中', value: 'provisioning' },
  { label: '开通失败', value: 'failed' },
  { label: '已终止', value: 'terminated' },
]

async function load() {
  loading.value = true
  try {
    services.value = dataOf(await api.get('/admin/services', { params: { status: status.value, limit: 300 } }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取服务失败')
  } finally {
    loading.value = false
  }
}

function act(s: any, action: 'suspend' | 'unsuspend' | 'terminate') {
  const label = { suspend: '暂停', unsuspend: '解除暂停', terminate: '终止并删除' }[action]
  dialog.warning({
    title: `${label}服务`,
    content: `确认对服务 ${s.id.slice(0, 8)}（${s.product_name}，${s.user_email}）执行「${label}」？操作会立即入队并调用上游 Provider。`,
    positiveText: '确认执行',
    negativeText: '取消',
    async onPositiveClick() {
      busy.value = s.id + action
      try {
        await api.post(`/admin/services/${s.id}/${action}`)
        message.success(`已提交「${label}」，完成后状态自动更新`)
        await load()
      } catch (e: any) {
        message.error(e?.response?.data?.error?.message || '操作失败')
      } finally {
        busy.value = ''
      }
    },
  })
}

async function retry(s: any) {
  busy.value = s.id + 'retry'
  try {
    await api.post(`/admin/services/${s.id}/retry`)
    message.success('已重新入队开通')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '重试失败')
  } finally {
    busy.value = ''
  }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">服务管理</div><h1>服务生命周期</h1><p>到期服务会自动暂停；暂停/解除/终止经队列调用上游 Provider，失败会自动回滚状态并记录原因。</p></div></div>

    <section class="panel">
      <div class="users-toolbar">
        <NSelect v-model:value="status" :options="statusOptions" style="max-width:200px" @update:value="load" />
        <NButton type="primary" :loading="loading" @click="load">刷新</NButton>
      </div>

      <div v-if="services.length" class="table-scroll"><div class="user-table">
        <div class="user-row user-head"><span>服务</span><span>用户</span><span>状态</span><span>上游引用</span><span>到期</span><span>创建</span><span>操作</span></div>
        <div v-for="s in services" :key="s.id" class="user-row">
          <span><b>{{ s.product_name }}</b><small class="muted">{{ s.id.slice(0, 8) }}</small></span>
          <span class="muted">{{ s.user_email }}<small>#{{ s.user_uid }}</small></span>
          <span><NTag size="small" :type="statusType(s.status)" round>{{ statusText[s.status] || s.status }}</NTag></span>
          <span class="muted">{{ s.provider_ref || '—' }}</span>
          <span class="muted">{{ fmt(s.expires_at) }}</span>
          <span class="muted">{{ fmt(s.created_at) }}</span>
          <span class="row" style="gap:6px;flex-wrap:wrap">
            <NButton v-if="s.status === 'active'" size="tiny" type="warning" secondary :loading="busy === s.id + 'suspend'" @click="act(s, 'suspend')">暂停</NButton>
            <NButton v-if="s.status === 'suspended'" size="tiny" type="primary" secondary :loading="busy === s.id + 'unsuspend'" @click="act(s, 'unsuspend')">恢复</NButton>
            <NButton v-if="['active', 'suspended'].includes(s.status)" size="tiny" type="error" secondary :loading="busy === s.id + 'terminate'" @click="act(s, 'terminate')">终止</NButton>
            <NButton v-if="s.status === 'failed'" size="tiny" type="info" secondary :loading="busy === s.id + 'retry'" @click="retry(s)">重试开通</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">没有匹配的服务。</div>
    </section>
  </div>
</template>
