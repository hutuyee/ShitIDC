<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { NButton, NInput, NRate, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 工单详情（对齐魔方 CBAP TicketPremium 插件）：状态 / 部门类型 / 处理时限、
// 沟通记录（含附件）、催单、处理完成后 3 项评分与用户关闭 / 重开。

const route = useRoute()
const message = useMessage()
const ticket = ref<any | null>(null)
const reply = ref('')
const sending = ref(false)
const working = ref(false)
const urging = ref(false)
const notFound = ref(false)
const picked = ref<File[]>([])

const priorityText: Record<string, string> = { low: '低', normal: '普通', high: '高', urgent: '紧急' }
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '')
const closed = computed(() => ticket.value?.status === 'closed')
const statusColor = computed(() => ticket.value?.color || '#909399')
const statusName = computed(() => ticket.value?.status_name || ticket.value?.status || '')
const dueText = computed(() => {
  const t = ticket.value
  if (!t?.due_time) return ''
  const due = new Date(t.due_time)
  const late = due.getTime() < Date.now()
  return (late ? '已超时（' : '处理时限：') + fmt(t.due_time) + (late ? '）' : '')
})

async function load() {
  try {
    ticket.value = dataOf<any>(await api.get(`/tickets/${route.params.id}`))
  } catch {
    notFound.value = true
  }
}

function pickFiles(e: Event) {
  const input = e.target as HTMLInputElement
  picked.value = Array.from(input.files || [])
}

async function uploadPicked(): Promise<string[]> {
  const ids: string[] = []
  for (const file of picked.value) {
    const fd = new FormData()
    fd.append('file', file)
    const att = dataOf<any>(await api.post(`/tickets/${route.params.id}/attachments`, fd))
    if (att?.id) ids.push(att.id)
  }
  return ids
}

async function send() {
  if (!reply.value.trim()) return
  sending.value = true
  try {
    const attachment = await uploadPicked()
    await api.post(`/tickets/${route.params.id}/reply`, { body: reply.value, attachment })
    reply.value = ''
    picked.value = []
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

async function urge() {
  urging.value = true
  try {
    await api.post(`/tickets/${route.params.id}/urge`, {})
    message.success('已催单，客服会尽快处理')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '催单失败')
  } finally {
    urging.value = false
  }
}

const score = ref({ satisfaction: 5, attitude: 5, processing_time: 5 })
const scoring = ref(false)
async function submitScore() {
  scoring.value = true
  try {
    await api.post(`/tickets/${route.params.id}/score`, score.value)
    message.success('感谢评分')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '评分失败')
  } finally {
    scoring.value = false
  }
}

const isImage = (name: string) => /\.(png|jpe?g|webp|gif)$/i.test(name || '')
function attachmentName(id: string) {
  const a = (ticket.value?.attachments || []).find((x: any) => x.id === id)
  return a?.filename || id
}
const downloadURL = (id: string) => `/api/v1/attachments/${id}`

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div v-if="notFound" class="empty-box">工单不存在或没有权限查看。<router-link to="/tickets">返回工单列表</router-link></div>

    <template v-else-if="ticket">
      <div class="dashboard-heading">
        <div>
          <div class="eyebrow">工单 {{ ticket.ticket_num ? '#' + ticket.ticket_num : '' }}</div>
          <h1>{{ ticket.subject }}</h1>
          <p>
            <template v-if="ticket.department_name">{{ ticket.department_name }}<template v-if="ticket.name"> / {{ ticket.name }}</template> · </template>
            优先级：{{ priorityText[ticket.priority] || ticket.priority }} · 提交于 {{ fmt(ticket.created_at) }}
            <template v-if="ticket.due_time"> · {{ dueText }}</template>
          </p>
        </div>
        <div class="stack" style="align-items:flex-end">
          <NTag round :style="{ background: statusColor, color: '#fff' }">{{ statusName }}</NTag>
          <NButton v-if="!ticket.finished" size="small" secondary :loading="urging" @click="urge">催单</NButton>
          <NButton v-if="!closed" size="small" quaternary type="error" :loading="working" @click="setStatus('closed')">关闭工单</NButton>
          <NButton v-else size="small" quaternary :loading="working" @click="setStatus('open')">重新打开</NButton>
        </div>
      </div>

      <div v-if="ticket.hosts && ticket.hosts.length" class="card" style="margin-bottom:12px">
        <b>关联产品</b>
        <div class="row" style="gap:8px;flex-wrap:wrap;margin-top:6px">
          <NTag v-for="h in ticket.hosts" :key="h.id" size="small">{{ h.product_name }}</NTag>
        </div>
      </div>

      <div v-if="ticket.finished && !ticket.is_score" class="card" style="margin-bottom:12px;border-left:4px solid #18a058">
        <b>客服已处理完成，请对本次服务评分</b>
        <div class="form-grid" style="margin-top:8px">
          <label><span>处理满意度</span><NRate v-model:value="score.satisfaction" allow-half color="#FFC329" /></label>
          <label><span>服务态度</span><NRate v-model:value="score.attitude" allow-half color="#FFC329" /></label>
          <label><span>处理时效</span><NRate v-model:value="score.processing_time" allow-half color="#FFC329" /></label>
        </div>
        <div class="row" style="justify-content:flex-end">
          <NButton type="primary" :loading="scoring" @click="submitScore">提交评分</NButton>
        </div>
      </div>

      <div v-else-if="ticket.is_score" class="card" style="margin-bottom:12px">
        <b>我的评分</b>
        <div class="muted" style="margin-top:4px">
          满意度 {{ Number(ticket.satisfaction || 0).toFixed(1) }} · 态度 {{ Number(ticket.attitude || 0).toFixed(1) }} · 时效 {{ Number(ticket.score_processing || 0).toFixed(1) }}
        </div>
      </div>

      <div class="card stack ticket-thread">
        <div v-for="m in ticket.messages" :key="m.id" class="ticket-msg" :class="{ staff: m.is_staff }">
          <div class="ticket-msg-head">
            <b>{{ m.is_staff ? (m.sender_name || '客服') : (m.sender_name || m.sender_email || '我') }}</b>
            <span class="muted">{{ fmt(m.created_at) }}</span>
          </div>
          <div class="ticket-msg-body">{{ m.body }}</div>
          <div v-if="m.attachment && m.attachment.length" class="row" style="gap:10px;flex-wrap:wrap;margin-top:6px">
            <template v-for="aid in m.attachment" :key="aid">
              <a v-if="isImage(attachmentName(aid))" :href="downloadURL(aid)" target="_blank">
                <img :src="downloadURL(aid)" :alt="attachmentName(aid)" style="max-width:220px;max-height:160px;border-radius:6px" />
              </a>
              <a v-else :href="downloadURL(aid)" target="_blank" class="muted">📎 {{ attachmentName(aid) }}</a>
            </template>
          </div>
        </div>
      </div>

      <div class="card stack">
        <NInput v-model:value="reply" type="textarea" :rows="4" placeholder="回复内容（工单关闭后回复会自动重新打开）" />
        <div class="row" style="justify-content:space-between;align-items:center">
          <label class="muted" style="cursor:pointer">
            📎 添加附件（单个不超过 5MB）
            <input type="file" multiple style="display:none" @change="pickFiles" />
          </label>
          <div class="row" style="gap:8px">
            <span v-if="picked.length" class="muted">{{ picked.length }} 个文件待上传</span>
            <NButton type="primary" :loading="sending" @click="send">发送回复</NButton>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
