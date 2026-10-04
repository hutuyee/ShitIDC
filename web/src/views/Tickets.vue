<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const tickets = ref<any[]>([])
const subject = ref('')
const body = ref('')
const priority = ref('normal')
const creating = ref(false)
const message = useMessage()

const statusText: Record<string, string> = { open: '待处理', pending: '已回复', closed: '已关闭' }
const statusType = (s: string) => ({ open: 'warning', pending: 'info', closed: 'default' } as any)[s] || 'default'
const priorityText: Record<string, string> = { low: '低', normal: '普通', high: '高', urgent: '紧急' }

async function load() {
  tickets.value = dataOf(await api.get('/tickets'))
}

async function create() {
  if (subject.value.trim().length < 2 || body.value.trim().length < 2) {
    message.error('主题和问题描述至少 2 个字符')
    return
  }
  creating.value = true
  try {
    await api.post('/tickets', { subject: subject.value, message: body.value, priority: priority.value })
    subject.value = ''
    body.value = ''
    priority.value = 'normal'
    message.success('工单已提交，客服会尽快回复')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '创建失败')
  } finally {
    creating.value = false
  }
}

const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '')

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">技术支持</div>
        <h1>提交工单</h1>
        <p>遇到问题、需要协助或申请开通资源，都可以提交工单；客服回复后这里和邮件都会更新。</p>
      </div>
    </div>

    <div class="card stack">
      <NInput v-model:value="subject" placeholder="主题：一句话描述问题" />
      <NSelect v-model:value="priority" :options="[
        { label: '低', value: 'low' },
        { label: '普通', value: 'normal' },
        { label: '高', value: 'high' },
        { label: '紧急', value: 'urgent' },
      ]" />
      <NInput v-model:value="body" type="textarea" :rows="5" placeholder="详细描述问题：涉及的服务、报错内容、期望的处理方式" />
      <NButton type="primary" :loading="creating" @click="create">提交工单</NButton>
    </div>

    <h2 class="section-title">我的工单</h2>
    <div v-if="!tickets.length" class="empty-box">还没有工单，有问题就直接提交。</div>
    <div class="stack">
      <router-link v-for="t in tickets" :key="t.id" :to="`/tickets/${t.id}`" class="card row ticket-row">
        <div>
          <b>{{ t.subject }}</b>
          <div class="muted">创建于 {{ fmt(t.created_at) }}<template v-if="t.last_reply_at"> · 最后回复 {{ fmt(t.last_reply_at) }}</template></div>
        </div>
        <div class="ticket-row-side">
          <NTag size="small">{{ priorityText[t.priority] || t.priority }}</NTag>
          <NTag :type="statusType(t.status)" size="small" round>{{ statusText[t.status] || t.status }}</NTag>
        </div>
      </router-link>
    </div>
  </div>
</template>
