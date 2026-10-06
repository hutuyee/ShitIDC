<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NInput, NModal, NRate, NSelect, NTag, NTreeSelect, useMessage } from 'naive-ui'
import { useRoute, useRouter } from 'vue-router'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'
import { useAuthStore } from '../stores/auth'

// 用户工单详情（对齐魔方 CBAP TicketPremium 插件）：
// 沟通记录 + 内部备注 + 操作日志，支持预设回复、编辑 / 删除、类型 / 状态 /
// 关联产品保存、接单、处理完成（可同时关闭）、关闭 / 重开与转内部工单。

const message = useMessage()
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const id = String(route.params.id || '')

const detail = ref<any>(null)
const departments = ref<any[]>([])
const statuses = ref<any[]>([])
const prereplies = ref<any[]>([])
const busy = ref(false)

const typeTree = computed(() => departments.value.map((d: any) => ({
  label: d.name,
  key: `d-${d.id}`,
  value: `d-${d.id}`,
  disabled: true,
  children: (d.type || []).map((t: any) => ({ label: `${t.name}（${t.processing_limit}h）`, key: t.id, value: t.id })),
})))
const statusOptions = computed(() => statuses.value.map((s: any) => ({ label: s.name, value: s.key })))
const hostOptions = computed(() => (detail.value?.hosts || []).map((h: any) => ({
  label: h.product_name, value: Number(String(h.id).replace(/^s-/, '')),
})))

const form = reactive({ ticket_type_id: null as number | null, status: '', host_ids: [] as number[] })
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '--')
const statusColor = computed(() => detail.value?.color || '#909399')
const statusName = computed(() => detail.value?.status_name || detail.value?.status || '')
const priorityText: Record<string, string> = { low: '低', normal: '普通', high: '高', urgent: '紧急' }
const dueText = computed(() => {
  const d = detail.value
  if (!d?.due_time) return '--'
  const late = new Date(d.due_time).getTime() < Date.now()
  return (late ? '已超时（' : '') + fmt(d.due_time) + (late ? '）' : '')
})

async function load() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get(`/admin/ticket-premium/tickets/${id}`))
    detail.value = d
    form.ticket_type_id = d?.ticket_type_id || null
    form.status = d?.status || ''
    form.host_ids = (d?.hosts || []).map((h: any) => Number(String(h.id).replace(/^s-/, '')))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取工单失败')
  } finally {
    busy.value = false
  }
}
async function loadBase() {
  try {
    const [dept, st, pp] = await Promise.all([
      api.get('/admin/ticket-premium/department'),
      api.get('/admin/ticket-premium/status'),
      api.get('/admin/ticket-premium/prereply'),
    ])
    departments.value = dataOf<any>(dept)?.list || []
    statuses.value = dataOf<any>(st)?.list || []
    prereplies.value = dataOf<any>(pp)?.list || []
  } catch {
    message.error('读取配置失败')
  }
}
onMounted(async () => { await loadBase(); load() })

async function saveForm() {
  try {
    await api.post(`/admin/ticket-premium/tickets/${id}/save`, {
      ticket_type_id: form.ticket_type_id || 0,
      status: form.status,
      host_ids: form.host_ids,
    })
    message.success('保存成功')
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}

// ---- 接单 / 处理完成 / 状态 ----
const working = ref(false)
async function accept() {
  working.value = true
  try {
    await api.post(`/admin/ticket-premium/tickets/${id}/receive`, {})
    message.success('接单成功')
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '接单失败')
  } finally {
    working.value = false
  }
}
const processedOpen = ref(false)
const processedClose = ref(false)
async function submitProcessed() {
  working.value = true
  try {
    await api.post(`/admin/ticket-premium/tickets/${id}/processed`, { close: processedClose.value })
    message.success('已标记处理完成')
    processedOpen.value = false
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  } finally {
    working.value = false
  }
}
async function setStatus(status: string) {
  working.value = true
  try {
    await api.post(`/admin/ticket-premium/tickets/${id}/status`, { status })
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  } finally {
    working.value = false
  }
}

// ---- 回复 / 备注 ----
const conversation = computed(() => {
  const d = detail.value
  if (!d) return []
  const items: any[] = []
  for (const m of d.messages || []) items.push({ ...m, kind: 'reply', content: m.body })
  for (const n of d.notes || []) items.push({ ...n, kind: 'note', content: n.content })
  items.sort((a: any, b: any) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
  return items
})
const replyText = ref('')
const replyBusy = ref(false)
const picked = ref<File[]>([])
function pickFiles(e: Event) {
  const input = e.target as HTMLInputElement
  picked.value = Array.from(input.files || [])
}
async function uploadPicked(): Promise<string[]> {
  const ids: string[] = []
  for (const file of picked.value) {
    const fd = new FormData()
    fd.append('file', file)
    const att = dataOf<any>(await api.post(`/tickets/${id}/attachments`, fd))
    if (att?.id) ids.push(att.id)
  }
  return ids
}
async function sendReply() {
  if (!replyText.value.trim()) { message.warning('请输入回复内容'); return }
  replyBusy.value = true
  try {
    const attachment = await uploadPicked()
    await api.post(`/admin/ticket-premium/tickets/${id}/reply`, { body: replyText.value, attachment })
    replyText.value = ''
    picked.value = []
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '回复失败')
  } finally {
    replyBusy.value = false
  }
}
const noteOpen = ref(false)
const noteText = ref('')
async function addNote() {
  if (!noteText.value.trim()) { message.warning('请输入备注内容'); return }
  try {
    await api.post(`/admin/ticket-premium/tickets/${id}/notes`, { content: noteText.value })
    noteText.value = ''
    noteOpen.value = false
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '添加备注失败')
  }
}
const prereplyOpen = ref(false)
function usePrereply(p: any) { replyText.value = p.content; prereplyOpen.value = false }

// ---- 编辑 / 删除 ----
const editItem = ref<any>(null)
const editText = ref('')
function startEdit(item: any) { editItem.value = item; editText.value = item.content }
function cancelEdit() { editItem.value = null; editText.value = '' }
async function saveEdit() {
  if (!editItem.value) return
  try {
    if (editItem.value.kind === 'note') await api.put(`/admin/ticket-premium/notes/${editItem.value.id}`, { content: editText.value })
    else await api.put(`/admin/ticket-premium/reply/${editItem.value.id}`, { body: editText.value })
    cancelEdit()
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
async function removeItem(item: any) {
  try {
    if (item.kind === 'note') await api.delete(`/admin/ticket-premium/notes/${item.id}`)
    else await api.delete(`/admin/ticket-premium/reply/${item.id}`)
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}
function attachmentName(aid: string) {
  const a = (detail.value?.attachments || []).find((x: any) => x.id === aid)
  return a?.filename || aid
}
const downloadURL = (aid: string) => `/api/v1/attachments/${aid}`

// ---- 日志 ----
const logOpen = ref(false)
const logs = ref<any[]>([])
async function openLog() {
  try {
    const d = dataOf<any>(await api.get(`/admin/ticket-premium/tickets/${id}/log`, { params: { page: 1, limit: 100 } }))
    logs.value = d?.list || []
    logOpen.value = true
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取日志失败')
  }
}

// ---- 转内部工单 ----
const canTurnInternal = computed(() => Boolean(auth.permissions['ticket_internal.manage']))
const internalDepartments = ref<any[]>([])
const turnOpen = ref(false)
const turnBusy = ref(false)
const turnForm = reactive({ title: '', ticket_type_id: null as number | null, priority: 'normal', host_ids: [] as number[] })
const turnTypes = computed(() => {
  for (const d of internalDepartments.value) {
    const t = (d.type || []).find((x: any) => x.id === turnForm.ticket_type_id)
    if (t) return [{ label: `${t.name}（${t.processing_limit}h）`, value: t.id }]
  }
  return internalDepartments.value.flatMap((d: any) => (d.type || []).map((t: any) => ({ label: `${d.name} / ${t.name}`, value: t.id })))
})
async function openTurn() {
  try {
    const d = dataOf<any>(await api.get('/admin/ticket-internal/department'))
    internalDepartments.value = d?.list || []
  } catch {
    internalDepartments.value = []
  }
  turnForm.title = detail.value?.subject || ''
  turnForm.ticket_type_id = null
  turnForm.priority = detail.value?.priority || 'normal'
  turnForm.host_ids = form.host_ids.slice()
  turnOpen.value = true
}
async function submitTurn() {
  if (!turnForm.title.trim() || !turnForm.ticket_type_id) { message.error('请填写标题并选择内部工单类型'); return }
  turnBusy.value = true
  try {
    const d = dataOf<any>(await api.post(`/admin/ticket-premium/tickets/${id}/turn-internal`, {
      title: turnForm.title,
      ticket_type_id: turnForm.ticket_type_id,
      priority: turnForm.priority,
      client_id: detail.value?.user_email || '',
      host_ids: turnForm.host_ids,
    }))
    message.success('已转内部工单 ' + (d?.ticket_num || ''))
    turnOpen.value = false
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '转单失败')
  } finally {
    turnBusy.value = false
  }
}
</script>

<template>
  <div v-if="detail" class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">工单 {{ detail.ticket_num ? '#' + detail.ticket_num : '' }}</div>
        <h1>{{ detail.subject }}</h1>
        <p>
          {{ detail.user_email }} · {{ detail.department_name || '未分配部门' }}<template v-if="detail.name"> / {{ detail.name }}</template> ·
          优先级 {{ priorityText[detail.priority] || detail.priority }}
        </p>
      </div>
      <div class="row" style="gap:8px;flex-wrap:wrap">
        <NButton secondary @click="router.push(ADMIN_PATH + '/tickets')">返回列表</NButton>
        <NButton v-if="!detail.admin_id" type="primary" :loading="working" @click="accept">接单</NButton>
        <NButton v-if="!detail.finished" secondary :loading="working" @click="processedOpen = true">处理完成</NButton>
        <NButton v-if="detail.status !== 'closed'" tertiary type="error" :loading="working" @click="setStatus('closed')">关闭工单</NButton>
        <NButton v-else tertiary :loading="working" @click="setStatus('open')">重新打开</NButton>
        <NButton secondary @click="openLog">操作日志</NButton>
        <NButton v-if="canTurnInternal" secondary @click="openTurn">转内部工单</NButton>
      </div>
    </div>

    <div class="card" style="margin-bottom:12px">
      <div class="row" style="flex-wrap:wrap;gap:20px;align-items:center">
        <NTag round :style="{ background: statusColor, color: '#fff' }">{{ statusName }}</NTag>
        <span class="muted">创建：{{ fmt(detail.created_at) }}</span>
        <span class="muted">处理时限：{{ dueText }}</span>
        <span class="muted">领取人：{{ detail.admin_name || '未领取' }}</span>
        <span class="muted">最后回复：{{ detail.last_reply_admin_name || '--' }} {{ fmt(detail.last_reply_at) }}</span>
        <span class="muted">催单：{{ detail.urge_count || 0 }} 次<template v-if="detail.last_urge_time">（最近 {{ fmt(detail.last_urge_time) }}）</template></span>
        <span v-if="detail.finished" class="muted">完成时间：{{ fmt(detail.finish_time) }}</span>
      </div>
      <div v-if="detail.is_score" class="row" style="gap:16px;margin-top:8px;align-items:center">
        <b>用户评分</b>
        <NRate :value="(Number(detail.satisfaction) + Number(detail.attitude) + Number(detail.score_processing)) / 3" allow-half disabled color="#FFC329" />
        <span class="muted" style="font-size:12px">满意 {{ Number(detail.satisfaction || 0).toFixed(1) }} / 态度 {{ Number(detail.attitude || 0).toFixed(1) }} / 时效 {{ Number(detail.score_processing || 0).toFixed(1) }}</span>
      </div>
    </div>

    <div class="card" style="margin-bottom:12px">
      <div class="form-grid">
        <label><span>工单类型</span><NTreeSelect v-model:value="form.ticket_type_id" :options="typeTree" clearable placeholder="工单类型" /></label>
        <label><span>工单状态</span><NSelect v-model:value="form.status" :options="statusOptions" placeholder="工单状态" /></label>
        <label class="full"><span>关联产品</span><NSelect v-model:value="form.host_ids" :options="hostOptions" multiple clearable placeholder="关联产品" /></label>
      </div>
      <div class="row" style="justify-content:flex-end;margin-top:8px">
        <NButton type="primary" @click="saveForm">保存</NButton>
      </div>
    </div>

    <div class="card stack">
      <h3 style="margin:0">沟通记录</h3>
      <div v-for="item in conversation" :key="item.kind + '-' + item.id" class="ti-message" :class="{ note: item.kind === 'note' }">
        <div class="row" style="justify-content:space-between">
          <span>
            <b>{{ item.kind === 'note' ? '内部备注' : (item.is_staff ? '客服' : '用户') }}</b>
            <span class="muted" style="margin-left:8px">{{ item.sender_name || item.sender_email || item.admin_name || '' }}</span>
            <span class="muted" style="margin-left:8px">{{ fmt(item.created_at) }}</span>
          </span>
          <span class="row" style="gap:6px">
            <NButton size="tiny" tertiary @click="startEdit(item)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeItem(item)">删除</NButton>
          </span>
        </div>
        <div v-if="editItem && editItem.id === item.id && editItem.kind === item.kind" style="margin-top:8px">
          <NInput v-model:value="editText" type="textarea" :rows="4" />
          <div class="row" style="justify-content:flex-end;gap:6px;margin-top:6px">
            <NButton size="tiny" @click="cancelEdit">取消</NButton>
            <NButton size="tiny" type="primary" @click="saveEdit">保存</NButton>
          </div>
        </div>
        <div v-else class="ti-content">{{ item.content }}</div>
        <div v-if="item.kind === 'reply' && item.attachment && item.attachment.length" class="row" style="gap:10px;flex-wrap:wrap;margin-top:6px">
          <a v-for="aid in item.attachment" :key="aid" :href="downloadURL(aid)" target="_blank" class="muted">📎 {{ attachmentName(aid) }}</a>
        </div>
      </div>
      <div v-if="!conversation.length" class="muted">暂无沟通记录</div>
    </div>

    <div style="margin-top:16px">
      <div class="row" style="justify-content:space-between;margin-bottom:6px">
        <div class="row" style="gap:6px">
          <NButton size="small" secondary @click="prereplyOpen = true">使用预设回复</NButton>
          <NButton size="small" secondary @click="noteOpen = !noteOpen">添加备注</NButton>
          <label class="muted" style="cursor:pointer;font-size:13px">
            📎 附件
            <input type="file" multiple style="display:none" @change="pickFiles" />
          </label>
          <span v-if="picked.length" class="muted" style="font-size:12px">{{ picked.length }} 个文件待上传</span>
        </div>
        <NButton size="small" type="primary" :loading="replyBusy" @click="sendReply">发送</NButton>
      </div>
      <NInput v-model:value="replyText" type="textarea" :rows="5" placeholder="回复内容（发送后用户会收到邮件通知）" />
      <div v-if="noteOpen" style="margin-top:8px">
        <NInput v-model:value="noteText" type="textarea" :rows="3" placeholder="内部备注（仅管理员可见）" />
        <div class="row" style="justify-content:flex-end;margin-top:6px">
          <NButton size="small" type="primary" @click="addNote">保存备注</NButton>
        </div>
      </div>
    </div>

    <NModal v-model:show="prereplyOpen" preset="card" title="预设回复" style="width:min(640px,96vw)">
      <div v-if="prereplies.length" class="col" style="gap:8px">
        <div v-for="p in prereplies" :key="p.id" class="ti-message" style="cursor:pointer" @click="usePrereply(p)">
          <div class="ti-content">{{ p.content }}</div>
        </div>
      </div>
      <div v-else class="muted">还没有预设回复，可在「工单配置 → 预设回复」中添加。</div>
    </NModal>

    <NModal v-model:show="processedOpen" preset="card" title="确认已处理完成？" style="width:min(420px,96vw)">
      <NCheckbox v-model:checked="processedClose">同时关闭工单</NCheckbox>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="processedOpen = false">取消</NButton>
          <NButton type="primary" @click="submitProcessed">确定</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="logOpen" preset="card" title="操作日志" style="width:min(560px,96vw)">
      <div v-if="logs.length" class="col" style="gap:6px">
        <div v-for="l in logs" :key="l.id" style="font-size:13px">
          <span class="muted">{{ fmt(l.create_time) }}</span>
          <b style="margin-left:8px">{{ l.admin_name || '系统' }}</b>
          <span style="margin-left:8px">{{ l.description }}</span>
        </div>
      </div>
      <div v-else class="muted">暂无日志</div>
    </NModal>

    <NModal v-model:show="turnOpen" preset="card" title="转内部工单" style="width:min(560px,96vw)">
      <div class="form-grid">
        <label class="full"><span>标题</span><NInput v-model:value="turnForm.title" /></label>
        <label class="full"><span>内部工单类型</span><NTreeSelect v-model:value="turnForm.ticket_type_id" :options="internalDepartments.map((d: any) => ({ label: d.name, key: 'd-' + d.id, value: 'd-' + d.id, disabled: true, children: (d.type || []).map((t: any) => ({ label: t.name, key: t.id, value: t.id })) }))" /></label>
        <label><span>优先级</span>
          <NSelect v-model:value="turnForm.priority" :options="[
            { label: '低', value: 'low' },
            { label: '普通', value: 'normal' },
            { label: '高', value: 'high' },
            { label: '紧急', value: 'urgent' },
          ]" />
        </label>
      </div>
      <div v-if="!internalDepartments.length" class="muted" style="margin-top:8px">没有读取到内部工单部门，请先在「内部工单 → 工单配置」中创建。</div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="turnOpen = false">取消</NButton>
          <NButton type="primary" :loading="turnBusy" @click="submitTurn">提交</NButton>
        </div>
      </template>
    </NModal>
  </div>
  <div v-else class="empty-box">{{ busy ? '加载中…' : '工单不存在' }}</div>
</template>

<style scoped>
.ti-message { border: 1px solid #f0f0f0; border-radius: 8px; padding: 10px 12px; margin-bottom: 10px; background: #fafcff; }
.ti-message.note { background: #fffbf0; border-color: #ffe7ba; }
.ti-message .ti-content { margin-top: 6px; line-height: 1.6; word-break: break-word; white-space: pre-wrap; }
</style>
