<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { NButton, NInput, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const route = useRoute()
const message = useMessage()
const ticket = ref<any | null>(null)
const reply = ref('')
const sending = ref(false)
const working = ref(false)
const notFound = ref(false)

const statusText: Record<string, string> = { open: '待客服处理', pending: '客服已回复，等你确认', closed: '已关闭' }
const statusType = (s: string) => ({ open: 'warning', pending: 'info', closed: 'default' } as any)[s] || 'default'
const priorityText: Record<string, string> = { low: '低', normal: '普通', high: '高', urgent: '紧急' }
const fmt = (v: string) => new Date(v).toLocaleString()

const closed = computed(() => ticket.value?.status === 'closed')

async function load() {
  try {
    ticket.value = dataOf<any>(await api.get(`/tickets/${route.params.id}`))
  } catch {
    notFound.value = true
  }
}

async function send() {
  if (!reply.value.trim()) return
  sending.value = true
  try {
    await api.post(`/tickets/${route.params.id}/reply`, { body: reply.value })
    reply.value = ''
    message.success('回复已发送')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '发送失败')
  } finally {
    sending.value = false
  }
}

async function setStatus(status: string) {
  working.value = true
  try {
    await api.post(`/tickets/${route.params.id}/close`, { status })
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  } finally {
    working.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div v-if="notFound" class="empty-box">工单不存在或没有权限查看。<router-link to="/tickets">返回工单列表</router-link></div>

    <template v-else-if="ticket">
      <div class="dashboard-heading">
        <div>
          <div class="eyebrow">工单 #{{ ticket.id.slice(0, 8) }}</div>
          <h1>{{ ticket.subject }}</h1>
          <p>优先级：{{ priorityText[ticket.priority] || ticket.priority }} · 提交于 {{ fmt(ticket.created_at) }}</p>
        </div>
        <div class="stack" style="align-items:flex-end">
          <NTag :type="statusType(ticket.status)" round>{{ statusText[ticket.status] || ticket.status }}</NTag>
          <NButton v-if="!closed" size="small" quaternary type="error" :loading="working" @click="setStatus('closed')">关闭工单</NButton>
          <NButton v-else size="small" quaternary :loading="working" @click="setStatus('open')">重新打开</NButton>
        </div>
      </div>

      <div class="card stack ticket-thread">
        <div v-for="m in ticket.messages" :key="m.id" class="ticket-msg" :class="{ staff: m.is_staff }">
          <div class="ticket-msg-head">
            <b>{{ m.is_staff ? '客服' : (m.sender_email || '我') }}</b>
            <span class="muted">{{ fmt(m.created_at) }}</span>
          </div>
          <div class="ticket-msg-body">{{ m.body }}</div>
        </div>
      </div>

      <div class="card stack">
        <NInput v-model:value="reply" type="textarea" :rows="4" :disabled="false" placeholder="回复内容（工单关闭后回复会自动重新打开）" />
        <div class="form-actions">
          <NButton type="primary" :loading="sending" @click="send">发送回复</NButton>
        </div>
      </div>
    </template>
  </div>
</template>
