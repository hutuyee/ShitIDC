<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NModal, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const tickets = ref<any[]>([])
const status = ref<string | null>(null)
const loaded = ref(false)

const detail = ref<any | null>(null)
const detailShow = ref(false)
const reply = ref('')
const sending = ref(false)
const working = ref(false)

const statusText: Record<string, string> = { open: '待处理', pending: '已回复待确认', closed: '已关闭' }
const statusType = (s: string) => ({ open: 'warning', pending: 'info', closed: 'default' } as any)[s] || 'default'
const priorityText: Record<string, string> = { low: '低', normal: '普通', high: '高', urgent: '紧急' }
const priorityType = (s: string) => ({ low: 'default', normal: 'default', high: 'warning', urgent: 'error' } as any)[s] || 'default'
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '')

const filterOptions = [
  { label: '全部状态', value: '' },
  { label: '待处理（用户等待回复）', value: 'open' },
  { label: '已回复待用户确认', value: 'pending' },
  { label: '已关闭', value: 'closed' },
]

const waitingCount = computed(() => tickets.value.filter(t => t.status === 'open').length)

async function load() {
  const q = status.value ? `?status=${status.value}` : ''
  tickets.value = dataOf(await api.get(`/admin/tickets${q}`))
  loaded.value = true
}

async function openTicket(t: any) {
  try {
    detail.value = dataOf<any>(await api.get(`/admin/tickets/${t.id}`))
    detailShow.value = true
    reply.value = ''
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取工单失败')
  }
}

async function send() {
  if (!detail.value || !reply.value.trim()) return
  sending.value = true
  try {
    await api.post(`/admin/tickets/${detail.value.id}/reply`, { body: reply.value })
    reply.value = ''
    message.success('已回复，等待用户确认')
    detail.value = dataOf<any>(await api.get(`/admin/tickets/${detail.value.id}`))
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '回复失败')
  } finally {
    sending.value = false
  }
}

async function setStatus(status: string) {
  if (!detail.value) return
  working.value = true
  try {
    await api.post(`/admin/tickets/${detail.value.id}/status`, { status })
    detail.value = dataOf<any>(await api.get(`/admin/tickets/${detail.value.id}`))
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
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">客服工作台</div>
        <h1>工单管理</h1>
        <p>回复用户工单、处理状态。这里只需要「客服工单」权限，不需要开放整个管理后台。</p>
      </div>
      <div class="stack" style="align-items:flex-end">
        <NTag v-if="waitingCount" type="warning" round>{{ waitingCount }} 个工单等待回复</NTag>
        <NSelect v-model:value="status" :options="filterOptions" style="width:220px" @update:value="load" />
      </div>
    </div>

    <div v-if="loaded && !tickets.length" class="empty-box">没有符合条件的工单。</div>

    <div class="stack">
      <article v-for="t in tickets" :key="t.id" class="panel order-card" style="cursor:pointer" @click="openTicket(t)">
        <header class="order-head">
          <div class="order-head-left">
            <b>{{ t.subject }}</b>
            <small class="muted">用户 #{{ t.user_uid }} · {{ t.user_email }} · 创建于 {{ fmt(t.created_at) }}</small>
          </div>
          <div class="order-head-right">
            <NTag :type="priorityType(t.priority)" size="small" round>{{ priorityText[t.priority] || t.priority }}</NTag>
            <NTag :type="statusType(t.status)" size="small" round>{{ statusText[t.status] || t.status }}</NTag>
          </div>
        </header>
        <footer class="order-foot">
          <small class="muted">
            <template v-if="t.last_reply_at">最后回复：{{ fmt(t.last_reply_at) }} · {{ t.last_reply_is_staff ? '客服' : '用户' }}</template>
            <template v-else>尚无回复</template>
          </small>
          <NButton size="small" type="primary" secondary @click.stop="openTicket(t)">查看 / 回复</NButton>
        </footer>
      </article>
    </div>

    <NModal v-model:show="detailShow" preset="card" :title="detail ? `工单：${detail.subject}` : '工单'" style="width:min(720px,94vw)">
      <div v-if="detail" class="stack">
        <div class="muted">
          用户 #{{ detail.user_uid }} · {{ detail.user_email }} · 优先级 {{ priorityText[detail.priority] || detail.priority }} ·
          <NTag :type="statusType(detail.status)" size="small" round>{{ statusText[detail.status] || detail.status }}</NTag>
        </div>
        <div class="ticket-thread">
          <div v-for="m in detail.messages" :key="m.id" class="ticket-msg" :class="{ staff: m.is_staff }">
            <div class="ticket-msg-head">
              <b>{{ m.is_staff ? '客服（我方）' : (m.sender_email || '用户') }}</b>
              <span class="muted">{{ fmt(m.created_at) }}</span>
            </div>
            <div class="ticket-msg-body">{{ m.body }}</div>
          </div>
        </div>
        <NInput v-model:value="reply" type="textarea" :rows="4" placeholder="以客服身份回复用户（会发送邮件通知用户）" />
        <div class="form-actions">
          <NButton type="primary" :loading="sending" @click="send">回复工单</NButton>
          <NButton v-if="detail.status !== 'closed'" :loading="working" @click="setStatus('closed')">关闭工单</NButton>
          <NButton v-if="detail.status === 'closed'" :loading="working" @click="setStatus('open')">重新打开</NButton>
          <NButton v-if="detail.status !== 'pending'" tertiary :loading="working" @click="setStatus('pending')">标记为已回复</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>
