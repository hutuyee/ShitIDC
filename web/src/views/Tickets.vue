<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 用户工单（对齐魔方 CBAP TicketPremium 插件）：部门 / 类型（处理时限）、
// 关联产品、工单通知、催单与评分入口；附件在工单详情页上传。

const message = useMessage()
const tickets = ref<any[]>([])
const subject = ref('')
const body = ref('')
const priority = ref('normal')
const departmentId = ref<number | null>(null)
const ticketTypeId = ref<number | null>(null)
const hostIds = ref<number[]>([])
const creating = ref(false)
const loading = ref(false)

const meta = ref<any>({ statuses: [], notice_open: false, notice_description: '' })
const departments = ref<any[]>([])
const hosts = ref<any[]>([])

const typeOptions = computed(() => {
  const d = departments.value.find((x: any) => x.id === departmentId.value)
  return (d?.type || []).map((t: any) => ({ label: `${t.name}（${t.processing_limit}h）`, value: t.id }))
})
const hostOptions = computed(() => hosts.value.map((h: any) => ({
  label: h.product_name ? `${h.product_name}${h.status ? ' · ' + h.status : ''}` : String(h.id),
  value: Number(String(h.id).replace(/^s-/, '')),
})))
const priorityText: Record<string, string> = { low: '低', normal: '普通', high: '高', urgent: '紧急' }
const statusText = (t: any) => t.status_name || t.status
const statusColor = (t: any) => t.color || '#909399'

const now = ref(Date.now())
setInterval(() => { now.value = Date.now() }, 30000)
function dueState(t: any) {
  if (t.finished || !t.due_time) return ''
  const due = new Date(t.due_time).getTime()
  if (due <= now.value) return '已超时'
  if (t.created_at && due - now.value <= (due - new Date(t.created_at).getTime()) * 0.15) return '即将超时'
  return ''
}

async function load() {
  loading.value = true
  try {
    tickets.value = dataOf(await api.get('/tickets')) || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取工单失败')
  } finally {
    loading.value = false
  }
}

async function loadBase() {
  try {
    const [m, d, h] = await Promise.all([
      api.get('/tickets/meta'),
      api.get('/tickets/departments'),
      api.get('/tickets/hosts'),
    ])
    meta.value = dataOf<any>(m) || meta.value
    departments.value = dataOf<any[]>(d) || []
    hosts.value = dataOf<any[]>(h) || []
  } catch {
    message.error('读取工单配置失败')
  }
}

async function create() {
  if (subject.value.trim().length < 2 || body.value.trim().length < 2) {
    message.error('主题和问题描述至少 2 个字符')
    return
  }
  if (!departmentId.value || !ticketTypeId.value) {
    message.error('请选择工单部门与类型')
    return
  }
  creating.value = true
  try {
    await api.post('/tickets', {
      subject: subject.value,
      message: body.value,
      priority: priority.value,
      department_id: departmentId.value,
      ticket_type_id: ticketTypeId.value,
      host_ids: hostIds.value,
    })
    subject.value = ''
    body.value = ''
    priority.value = 'normal'
    ticketTypeId.value = null
    hostIds.value = []
    message.success('工单已提交，客服会尽快回复')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '创建失败')
  } finally {
    creating.value = false
  }
}

const urging = ref<number | null>(null)
async function urge(t: any) {
  urging.value = t.id
  try {
    await api.post(`/tickets/${t.id}/urge`, {})
    message.success('已催单，客服会尽快处理')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '催单失败')
  } finally {
    urging.value = null
  }
}

const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '')

onMounted(async () => { await Promise.all([load(), loadBase()]) })
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

    <div v-if="meta.notice_open" class="card" style="margin-bottom:12px;border-left:4px solid #2080f0">
      <b>工单须知</b>
      <div class="muted" style="margin-top:4px">{{ meta.notice_description || '提交前请先描述清楚问题，客服会尽快处理。' }}</div>
    </div>

    <div class="card stack">
      <NInput v-model:value="subject" placeholder="主题：一句话描述问题" />
      <div class="row" style="gap:10px;flex-wrap:wrap">
        <NSelect v-model:value="departmentId" :options="departments.map((d: any) => ({ label: d.name, value: d.id }))" placeholder="工单部门" style="min-width:200px"
          @update:value="ticketTypeId = null" />
        <NSelect v-model:value="ticketTypeId" :options="typeOptions" placeholder="工单类型" style="min-width:200px" />
        <NSelect v-model:value="priority" :options="[
          { label: '低', value: 'low' },
          { label: '普通', value: 'normal' },
          { label: '高', value: 'high' },
          { label: '紧急', value: 'urgent' },
        ]" style="width:140px" />
      </div>
      <NSelect v-if="hosts.length" v-model:value="hostIds" :options="hostOptions" multiple clearable placeholder="关联产品（可多选）" />
      <NInput v-model:value="body" type="textarea" :rows="5" placeholder="详细描述问题：涉及的服务、报错内容、期望的处理方式" />
      <div class="row" style="justify-content:flex-end">
        <NButton type="primary" :loading="creating" @click="create">提交工单</NButton>
      </div>
    </div>

    <h2 class="section-title">我的工单</h2>
    <div v-if="!tickets.length" class="empty-box">还没有工单，有问题就直接提交。</div>
    <div class="stack">
      <router-link v-for="t in tickets" :key="t.id" :to="`/tickets/${t.id}`" class="card row ticket-row">
        <div>
          <b>{{ t.ticket_num ? '#' + t.ticket_num + ' ' : '' }}{{ t.subject }}</b>
          <div class="muted">
            <template v-if="t.department_name">{{ t.department_name }}<template v-if="t.name"> / {{ t.name }}</template> · </template>
            创建于 {{ fmt(t.created_at) }}<template v-if="t.last_reply_at"> · 最后回复 {{ fmt(t.last_reply_at) }}</template>
          </div>
        </div>
        <div class="ticket-row-side">
          <NTag v-if="dueState(t)" :type="dueState(t) === '已超时' ? 'error' : 'warning'" size="small" round>{{ dueState(t) }}</NTag>
          <NTag v-if="t.is_score" size="small" type="success" round>已评分</NTag>
          <NTag v-else-if="t.finished" size="small" type="info" round>待评分</NTag>
          <NTag size="small">{{ priorityText[t.priority] || t.priority }}</NTag>
          <NTag size="small" round :style="{ background: statusColor(t), color: '#fff' }">{{ statusText(t) }}</NTag>
          <NButton v-if="!t.finished" size="tiny" tertiary :loading="urging === t.id" @click.prevent.stop="urge(t)">催单</NButton>
        </div>
      </router-link>
    </div>
  </div>
</template>
