<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NInput, NModal, NRate, NSelect, NTag, NTreeSelect, useMessage } from 'naive-ui'
import { useRoute, useRouter } from 'vue-router'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'

// 内部工单详情（对齐 TicketInternalPremium 的 ticket_internal_detail）：
// 沟通记录 + 内部备注 + 操作日志，支持预设回复、编辑 / 删除、状态 / 类型 /
// 关联产品保存、处理完成（可同时关闭）与评分。

const message = useMessage()
const route = useRoute()
const router = useRouter()
const id = Number(route.params.id || 0)

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
  children: (d.type || []).map((t: any) => ({ label: t.name, key: t.id, value: t.id })),
})))
const statusOptions = computed(() => statuses.value.map((s: any) => ({ label: s.name, value: s.id })))

const form = reactive({ type_id: null as number | null, status_id: null as number | null, host_id: [] as string[] })
const hostOptions = computed(() => (detail.value?.hosts || []).map((h: any) => ({ label: h.product_name, value: Number(h.id.replace(/^s-/, '')) })))

const conversation = computed(() => {
  const d = detail.value
  if (!d) return []
  const items: any[] = []
  for (const r of d.replies || []) items.push({ ...r, kind: 'reply' })
  for (const n of d.notes_list || []) items.push({ ...n, kind: 'note' })
  items.sort((a: any, b: any) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
  return items
})
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '--')

async function load() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get(`/admin/ticket-internal/tickets/${id}`))
    detail.value = d
    form.type_id = d?.type_id ?? null
    form.status_id = d?.status_id ?? null
    form.host_id = (d?.hosts || []).map((h: any) => Number(String(h.id).replace(/^s-/, '')))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取工单失败')
  } finally {
    busy.value = false
  }
}
async function loadBase() {
  try {
    const [dept, st, pp] = await Promise.all([
      api.get('/admin/ticket-internal/department'),
      api.get('/admin/ticket-internal/status'),
      api.get('/admin/ticket-internal/prereply'),
    ])
    departments.value = dataOf<any>(dept)?.list || []
    statuses.value = dataOf<any>(st)?.list || []
    prereplies.value = dataOf<any>(pp)?.list || []
  } catch {
    message.error('读取配置失败')
  }
}

async function saveForm() {
  try {
    await api.put(`/admin/ticket-internal/tickets/${id}`, {
      status_id: form.status_id, type_id: form.type_id, host_id: form.host_id,
    })
    message.success('保存成功')
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}

// ---- 回复 / 备注 ----
const replyText = ref('')
const replyBusy = ref(false)
async function sendReply() {
  if (!replyText.value.trim()) { message.warning('请输入回复内容'); return }
  replyBusy.value = true
  try {
    await api.post(`/admin/ticket-internal/tickets/${id}/reply`, { content: replyText.value, attachment: [] })
    replyText.value = ''
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
    await api.post(`/admin/ticket-internal/tickets/${id}/notes`, { content: noteText.value })
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
    if (editItem.value.kind === 'note') await api.put(`/admin/ticket-internal/notes/${editItem.value.id}`, { content: editText.value })
    else await api.put(`/admin/ticket-internal/reply/${editItem.value.id}`, { content: editText.value })
    cancelEdit()
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
async function removeItem(item: any) {
  try {
    if (item.kind === 'note') await api.delete(`/admin/ticket-internal/notes/${item.id}`)
    else await api.delete(`/admin/ticket-internal/reply/${item.id}`)
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 处理完成 / 日志 ----
const finishOpen = ref(false)
const finishClose = ref(false)
async function submitFinish() {
  try {
    await api.post(`/admin/ticket-internal/tickets/${id}/finish`, { close: finishClose.value ? 1 : 0 })
    message.success('处理成功')
    finishOpen.value = false
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}
const logOpen = ref(false)
async function openLog() {
  logOpen.value = true
}

// ---- 评分 ----
const scoreForm = reactive({ satisfaction: 0, attitude: 0, processing_time: 0 })
const scoreBusy = ref(false)
async function submitScore() {
  if (!scoreForm.satisfaction || !scoreForm.attitude || !scoreForm.processing_time) {
    message.warning('请完成三项评分（最低半星）')
    return
  }
  scoreBusy.value = true
  try {
    await api.post(`/admin/ticket-internal/tickets/${id}/score`, { ...scoreForm })
    message.success('评分成功')
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '评分失败')
  } finally {
    scoreBusy.value = false
  }
}
function hasScore(d: any) {
  return d && (d.satisfaction !== null || d.director_satisfaction !== null)
}
function shownScore(d: any) {
  return d?.satisfaction ?? d?.director_satisfaction ?? 0
}

onMounted(async () => { await loadBase(); load() })
</script>

<template>
  <div class="orders-page" v-if="detail">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">内部工单详情</div>
        <h1>#{{ detail.ticket_num }} {{ detail.title }}</h1>
        <p>
          用户：{{ detail.client_name || '--' }} ·
          提交时间：{{ fmt(detail.created_at) }} ·
          上次回复：{{ detail.last_reply_time ? fmt(detail.last_reply_time) : '--' }}
          <a href="javascript:;" style="margin-left:10px" @click="openLog">日志记录</a>
        </p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary @click="router.push(ADMIN_PATH + '/ticket-internal')">返回</NButton>
        <NButton v-if="!detail.finished" type="primary" @click="finishOpen = true">处理完成</NButton>
      </div>
    </div>

    <div class="card" style="margin-bottom:12px">
      <div class="row" style="flex-wrap:wrap;gap:16px;align-items:flex-end">
        <label class="col" style="min-width:260px;gap:4px"><span class="muted">工单部门 - 类型</span>
          <NTreeSelect v-model:value="form.type_id" :options="typeTree" checkable cascade placeholder="工单部门 - 类型" />
        </label>
        <label class="col" style="min-width:180px;gap:4px"><span class="muted">工单状态</span>
          <NSelect v-model:value="form.status_id" :options="statusOptions" placeholder="工单状态" />
        </label>
        <label class="col" style="min-width:240px;gap:4px"><span class="muted">关联产品</span>
          <NSelect v-model:value="form.host_id" :options="hostOptions" multiple clearable placeholder="关联产品" />
        </label>
        <NButton type="primary" @click="saveForm">保存</NButton>
      </div>
      <div class="muted" style="margin-top:8px;font-size:13px">
        关联工单：{{ detail.source_ticket_id ? detail.source_ticket_title : '--' }} ·
        紧急程度：{{ detail.priority === 'urgent' ? '紧急' : '一般' }} ·
        领取人：{{ detail.order_admin_name || '--' }} · 跟进人：{{ detail.last_reply_admin_name || '--' }}
      </div>
    </div>

    <div class="card">
      <h3 style="margin:0 0 10px">沟通记录</h3>
      <div v-for="item in conversation" :key="item.kind + '-' + item.id" class="ti-message" :class="{ note: item.kind === 'note' }">
        <div class="row" style="justify-content:space-between">
          <span>
            <NTag size="tiny" :type="item.kind === 'note' ? 'warning' : 'info'">{{ item.kind === 'note' ? '备注' : '管理' }}</NTag>
            <b style="margin-left:8px">{{ item.admin_name || '--' }}</b>
            <span class="muted" style="margin-left:8px;font-size:12px">{{ fmt(item.created_at) }}</span>
          </span>
          <span class="row" style="gap:4px">
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
        <div v-else class="ti-content" v-html="item.content"></div>
      </div>

      <div class="ti-message" v-if="detail.show_score || hasScore(detail)">
        <div class="row" style="justify-content:space-between;margin-bottom:6px">
          <b>{{ hasScore(detail) ? '本次工单已完成' : '请为本次工单服务评分' }}</b>
          <NButton v-if="detail.show_score" size="small" type="primary" :loading="scoreBusy" @click="submitScore">提交</NButton>
        </div>
        <div class="form-grid">
          <label><span>处理满意度</span><NRate v-model:value="scoreForm.satisfaction" allow-half color="#FFC329" :disabled="hasScore(detail)" /></label>
          <label><span>服务态度</span><NRate v-model:value="scoreForm.attitude" allow-half color="#FFC329" :disabled="hasScore(detail)" /></label>
          <label><span>处理时效</span><NRate v-model:value="scoreForm.processing_time" allow-half color="#FFC329" :disabled="hasScore(detail)" /></label>
        </div>
        <div v-if="hasScore(detail)" class="muted" style="font-size:12px">
          综合分：{{ shownScore(detail) }}（发起人{{ detail.satisfaction ? '已评' : '未评' }} / 主管{{ detail.director_satisfaction ? '已评' : '未评' }}）
        </div>
      </div>

      <div style="margin-top:16px">
        <div class="row" style="justify-content:space-between;margin-bottom:6px">
          <div class="row" style="gap:6px">
            <NButton size="small" secondary @click="prereplyOpen = true">使用预设回复</NButton>
            <NButton size="small" secondary @click="noteOpen = !noteOpen">添加备注</NButton>
          </div>
          <NButton size="small" type="primary" :loading="replyBusy" @click="sendReply">发送</NButton>
        </div>
        <NInput v-model:value="replyText" type="textarea" :rows="5" placeholder="回复内容（支持 HTML）" />
        <div v-if="noteOpen" style="margin-top:8px">
          <NInput v-model:value="noteText" type="textarea" :rows="3" placeholder="内部备注（仅管理员可见）" />
          <div class="row" style="justify-content:flex-end;margin-top:6px">
            <NButton size="small" type="primary" @click="addNote">保存备注</NButton>
          </div>
        </div>
      </div>
    </div>

    <NModal v-model:show="prereplyOpen" preset="card" title="预设回复" style="width:min(640px,96vw)">
      <div v-if="prereplies.length" class="col" style="gap:8px">
        <div v-for="p in prereplies" :key="p.id" class="ti-message" style="cursor:pointer" @click="usePrereply(p)">
          <div class="ti-content" v-html="p.content"></div>
        </div>
      </div>
      <div v-else class="muted">还没有预设回复，可在「工单配置 → 预设回复」中添加。</div>
    </NModal>

    <NModal v-model:show="finishOpen" preset="card" title="确认已处理完成？" style="width:min(420px,96vw)">
      <NCheckbox v-model:checked="finishClose">同时关闭工单</NCheckbox>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="finishOpen = false">取消</NButton>
          <NButton type="primary" @click="submitFinish">确定</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="logOpen" preset="card" title="操作日志" style="width:min(560px,96vw)">
      <div v-if="(detail.logs || []).length" class="col" style="gap:6px">
        <div v-for="l in detail.logs" :key="l.id" style="font-size:13px">
          <span class="muted">{{ fmt(l.create_time) }}</span>
          <b style="margin-left:8px">{{ l.admin_name || '系统' }}</b>
          <span style="margin-left:8px">{{ l.description }}</span>
        </div>
      </div>
      <div v-else class="muted">暂无日志</div>
    </NModal>
  </div>
  <div v-else class="empty-box">{{ busy ? '加载中…' : '工单不存在' }}</div>
</template>

<style scoped>
.ti-message { border: 1px solid #f0f0f0; border-radius: 8px; padding: 10px 12px; margin-bottom: 10px; background: #fafcff; }
.ti-message.note { background: #fffbf0; border-color: #ffe7ba; }
.ti-message .ti-content { margin-top: 6px; line-height: 1.6; word-break: break-word; }
.ti-message .ti-content :deep(img) { max-width: 100%; }
</style>
