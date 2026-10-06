<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NDatePicker, NInput, NModal, NSelect, NTag, NTreeSelect, useMessage } from 'naive-ui'
import { useRouter } from 'vue-router'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'

// 用户工单高级版（对齐魔方 CBAP TicketPremium 插件）：
// 列表支持关键词 / 类型 / 状态 / 用户 / 跟进人 / 领取人 / 时间范围筛选与自动刷新；
// 支持客服代建、接单、处理完成（可同时关闭）、关闭 / 重开与转内部工单；
// 详情页在 AdminTicketPremiumDetail.vue，配置与统计在对应页面。

const message = useMessage()
const router = useRouter()

const rows = ref<any[]>([])
const total = ref(0)
const busy = ref(false)
const departments = ref<any[]>([])
const statuses = ref<any[]>([])
const staff = ref<any[]>([])
const config = reactive({
  refresh_time: '180', ticket_receive_reply: '0', ticket_follow_reply: '0',
  ticket_notice_open: '0', ticket_notice_description: '',
})

const filters = reactive({
  keywords: '',
  type_ids: [] as (number | string)[],
  status: [] as string[],
  client_id: '',
  last_reply_admin_id: null as number | null,
  admin_id: null as number | null,
  range: null as [number, number] | null,
  page: 1,
  limit: 20,
})

const typeTree = computed(() => departments.value.map((d: any) => ({
  label: d.name,
  key: `d-${d.id}`,
  value: `d-${d.id}`,
  disabled: true,
  children: (d.type || []).map((t: any) => ({
    label: `${t.name}（${t.processing_limit}h）`,
    key: t.id,
    value: t.id,
  })),
})))
const staffOptions = computed(() => staff.value.map((s: any) => ({ label: s.name, value: s.id })))
const statusOptions = computed(() => statuses.value.map((s: any) => ({ label: s.name, value: s.key })))

async function load() {
  busy.value = true
  try {
    const p: Record<string, any> = {
      keywords: filters.keywords.trim(),
      ticket_type_ids: filters.type_ids.filter((v) => typeof v === 'number').join(','),
      status: filters.status.join(','),
      client_id: filters.client_id.trim(),
      last_reply_admin_id: filters.last_reply_admin_id || '',
      admin_id: filters.admin_id || '',
      page: filters.page,
      limit: filters.limit,
    }
    if (filters.range) {
      p.start_time = Math.floor(filters.range[0] / 1000)
      p.end_time = Math.floor(filters.range[1] / 1000)
    }
    const d = dataOf<any>(await api.get('/admin/ticket-premium/tickets', { params: p }))
    rows.value = d?.list || []
    total.value = d?.total || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取工单失败')
  } finally {
    busy.value = false
  }
}

async function loadBase() {
  try {
    const [dept, st, sf, cfg] = await Promise.all([
      api.get('/admin/ticket-premium/department'),
      api.get('/admin/ticket-premium/status'),
      api.get('/admin/ticket-premium/staff'),
      api.get('/admin/ticket-premium/config'),
    ])
    departments.value = dataOf<any>(dept)?.list || []
    statuses.value = dataOf<any>(st)?.list || []
    staff.value = dataOf<any>(sf)?.list || []
    Object.assign(config, dataOf<any>(cfg) || {})
  } catch {
    message.error('读取工单基础数据失败')
  }
}

function resetAndLoad() {
  filters.page = 1
  load()
}
function resetFilters() {
  filters.keywords = ''
  filters.type_ids = []
  filters.status = []
  filters.client_id = ''
  filters.last_reply_admin_id = null
  filters.admin_id = null
  filters.range = null
  resetAndLoad()
}

const now = ref(Date.now())
function timeoutMark(row: any) {
  if (row.finished || !row.due_time) return ''
  const due = new Date(row.due_time).getTime()
  if (due <= now.value) return '超'
  if (row.created_at && due - now.value <= (due - new Date(row.created_at).getTime()) * 0.15) return '⚠'
  return ''
}
function timeoutTitle(row: any) {
  if (row.finished) return '已处理完成'
  if (!row.due_time) return ''
  const due = new Date(row.due_time).getTime()
  if (due <= now.value) return '已超时'
  if (row.created_at && due - now.value <= (due - new Date(row.created_at).getTime()) * 0.15) return '即将超时'
  return ''
}
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '--')
const statusColor = (row: any) => row.color || '#909399'
const statusName = (row: any) => row.status_name || row.status

// ---- 行操作 ----
const working = ref<string>('')
async function accept(row: any) {
  working.value = row.id
  try {
    await api.post(`/admin/ticket-premium/tickets/${row.id}/receive`, {})
    message.success('接单成功')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '接单失败')
  } finally {
    working.value = ''
  }
}
async function setStatus(row: any, status: string) {
  working.value = row.id
  try {
    await api.post(`/admin/ticket-premium/tickets/${row.id}/status`, { status })
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  } finally {
    working.value = ''
  }
}
const processedOpen = ref(false)
const processedRow = ref<any>(null)
const processedClose = ref(false)
function openProcessed(row: any) {
  processedRow.value = row
  processedClose.value = false
  processedOpen.value = true
}
async function submitProcessed() {
  if (!processedRow.value) return
  working.value = processedRow.value.id
  try {
    await api.post(`/admin/ticket-premium/tickets/${processedRow.value.id}/processed`, { close: processedClose.value })
    message.success('已标记处理完成')
    processedOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  } finally {
    working.value = ''
  }
}

// ---- 客服代建 ----
const createOpen = ref(false)
const createBusy = ref(false)
const createHosts = ref<any[]>([])
const createForm = reactive({
  client_ref: '',
  department_id: null as number | null,
  ticket_type_id: null as number | null,
  title: '',
  priority: 'normal',
  host_ids: [] as number[],
  content: '',
})
const createTypes = computed(() => {
  const d = departments.value.find((x: any) => x.id === createForm.department_id)
  return (d?.type || []).map((t: any) => ({ label: `${t.name}（${t.processing_limit}h）`, value: t.id }))
})
const createHostOptions = computed(() => createHosts.value.map((h: any) => ({
  label: h.product_name ? `${h.product_name}${h.status ? ' · ' + h.status : ''}` : String(h.id),
  value: Number(String(h.id).replace(/^s-/, '')),
})))
async function loadCreateHosts() {
  if (!createForm.client_ref.trim()) return
  try {
    const d = dataOf<any>(await api.get('/admin/ticket-premium/hosts', { params: { user_id: createForm.client_ref.trim() } }))
    createHosts.value = d?.list || []
  } catch {
    createHosts.value = []
    message.error('读取用户产品失败')
  }
}
async function submitCreate() {
  if (!createForm.client_ref.trim()) { message.error('请填写用户（邮箱 / 用户ID / 公开ID）'); return }
  if (!createForm.department_id || !createForm.ticket_type_id) { message.error('请选择工单部门与类型'); return }
  if (createForm.title.trim().length < 2 || createForm.content.trim().length < 2) { message.error('标题与描述至少 2 个字'); return }
  createBusy.value = true
  try {
    await api.post('/admin/ticket-premium/tickets', {
      client_id: createForm.client_ref.trim(),
      department_id: createForm.department_id,
      ticket_type_id: createForm.ticket_type_id,
      title: createForm.title,
      priority: createForm.priority,
      host_ids: createForm.host_ids,
      content: createForm.content,
    })
    message.success('工单已创建')
    createOpen.value = false
    Object.assign(createForm, { client_ref: '', department_id: null, ticket_type_id: null, title: '', priority: 'normal', host_ids: [], content: '' })
    createHosts.value = []
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '创建失败')
  } finally {
    createBusy.value = false
  }
}

let timer: number | null = null
function applyRefresh() {
  if (timer) { clearInterval(timer); timer = null }
  const seconds = Number(config.refresh_time) || 0
  if (seconds >= 30) timer = window.setInterval(load, seconds * 1000)
}
onMounted(async () => { await loadBase(); applyRefresh(); load() })
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">客服工作台</div>
        <h1>工单管理</h1>
        <p>用户工单高级版：部门 / 类型（处理时限）、接单与跟进、处理完成、备注日志与统计排名。</p>
      </div>
      <div class="row" style="gap:8px;flex-wrap:wrap">
        <NButton secondary @click="router.push(ADMIN_PATH + '/tickets/settings')">工单配置</NButton>
        <NButton secondary @click="router.push(ADMIN_PATH + '/tickets/stats')">工单统计</NButton>
        <NButton type="primary" @click="createOpen = true">＋ 代客建单</NButton>
      </div>
    </div>

    <div class="users-toolbar" style="flex-wrap:wrap;row-gap:8px">
      <NInput v-model:value="filters.keywords" placeholder="关键词 / 工单编号" style="width:200px" @keyup.enter="resetAndLoad" />
      <NTreeSelect v-model:value="filters.type_ids" :options="typeTree" multiple clearable placeholder="工单类型" style="min-width:220px" />
      <NSelect v-model:value="filters.status" :options="statusOptions" multiple clearable placeholder="工单状态" style="min-width:180px" />
      <NInput v-model:value="filters.client_id" placeholder="用户（邮箱 / ID）" style="width:180px" />
      <NSelect v-model:value="filters.last_reply_admin_id" :options="staffOptions" clearable filterable placeholder="最后回复人" style="width:160px" />
      <NSelect v-model:value="filters.admin_id" :options="staffOptions" clearable filterable placeholder="领取人" style="width:160px" />
      <NDatePicker v-model:value="filters.range" type="daterange" clearable style="width:260px" />
      <NButton type="primary" :loading="busy" @click="resetAndLoad">搜索</NButton>
      <NButton @click="resetFilters">重置</NButton>
    </div>

    <div v-if="!rows.length && !busy" class="empty-box">没有符合条件的工单。</div>

    <div v-else class="table-scroll"><div class="user-table">
      <div class="user-row tp-row user-head">
        <span>编号</span><span>标题</span><span>部门 / 类型</span><span>用户</span><span>领取人</span>
        <span>最后回复</span><span>创建时间</span><span>状态</span><span>操作</span>
      </div>
      <div v-for="t in rows" :key="t.id" class="user-row tp-row">
        <span>
          <b>{{ t.ticket_num || '--' }}</b>
          <em v-if="timeoutMark(t)" class="ti-badge" :class="timeoutMark(t) === '超' ? 'danger' : 'warn'" :title="timeoutTitle(t)">{{ timeoutMark(t) }}</em>
        </span>
        <span><a style="cursor:pointer" @click="router.push(ADMIN_PATH + '/tickets/' + t.id)">{{ t.subject }}</a></span>
        <span>{{ t.department_name || '--' }}<template v-if="t.name"> / {{ t.name }}</template></span>
        <span>{{ t.user_email || '--' }}</span>
        <span>{{ t.admin_name || '未领取' }}</span>
        <span>{{ t.last_reply_admin_name || '--' }}<br /><small class="muted">{{ fmt(t.last_reply_at) }}</small></span>
        <span>{{ fmt(t.created_at) }}</span>
        <span><NTag size="small" round :style="{ background: statusColor(t), color: '#fff' }">{{ statusName(t) }}</NTag></span>
        <span class="row" style="gap:4px;flex-wrap:wrap">
          <NButton size="tiny" tertiary @click="router.push(ADMIN_PATH + '/tickets/' + t.id)">详情</NButton>
          <NButton v-if="config.ticket_receive_reply === '1' && !t.admin_id" size="tiny" type="primary" :loading="working === t.id" @click="accept(t)">接单</NButton>
          <NButton v-if="!t.finished" size="tiny" secondary :loading="working === t.id" @click="openProcessed(t)">处理完成</NButton>
          <NButton v-if="t.status !== 'closed'" size="tiny" tertiary type="error" :loading="working === t.id" @click="setStatus(t, 'closed')">关闭</NButton>
          <NButton v-else size="tiny" tertiary :loading="working === t.id" @click="setStatus(t, 'open')">重开</NButton>
        </span>
      </div>
    </div></div>

    <div class="row" style="justify-content:flex-end;margin-top:10px;gap:8px">
      <span class="muted">共 {{ total }} 条</span>
      <NButton size="small" :disabled="filters.page <= 1" @click="filters.page -= 1; load()">上一页</NButton>
      <NButton size="small" :disabled="filters.page * filters.limit >= total" @click="filters.page += 1; load()">下一页</NButton>
    </div>

    <NModal v-model:show="createOpen" preset="card" title="代客建单" style="width:min(760px,96vw)">
      <div class="form-grid">
        <label><span>用户（邮箱 / 用户ID / 公开ID）</span>
          <div class="row" style="gap:6px">
            <NInput v-model:value="createForm.client_ref" placeholder="输入后点击右侧加载产品" style="flex:1" @blur="loadCreateHosts" />
            <NButton secondary @click="loadCreateHosts">加载产品</NButton>
          </div>
        </label>
        <label><span>优先级</span>
          <NSelect v-model:value="createForm.priority" :options="[
            { label: '低', value: 'low' },
            { label: '普通', value: 'normal' },
            { label: '高', value: 'high' },
            { label: '紧急', value: 'urgent' },
          ]" />
        </label>
        <label><span>工单部门</span>
          <NSelect v-model:value="createForm.department_id" :options="departments.map((d: any) => ({ label: d.name, value: d.id }))" placeholder="工单部门"
            @update:value="createForm.ticket_type_id = null" />
        </label>
        <label><span>工单类型</span>
          <NSelect v-model:value="createForm.ticket_type_id" :options="createTypes" placeholder="工单类型" />
        </label>
        <label class="full"><span>关联产品（可多选）</span>
          <NSelect v-model:value="createForm.host_ids" :options="createHostOptions" multiple clearable placeholder="先填写用户并加载产品" />
        </label>
        <label class="full"><span>标题</span><NInput v-model:value="createForm.title" placeholder="工单标题" /></label>
        <label class="full"><span>详细描述</span><NInput v-model:value="createForm.content" type="textarea" :rows="5" placeholder="问题描述" /></label>
      </div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="createOpen = false">取消</NButton>
          <NButton type="primary" :loading="createBusy" @click="submitCreate">提交</NButton>
        </div>
      </template>
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
  </div>
</template>

<style scoped>
.tp-row { grid-template-columns: 110px minmax(200px, 1.4fr) 170px 160px 100px 160px 150px 100px 240px; }
.tp-row span { overflow: hidden; text-overflow: ellipsis; }
.ti-badge { display: inline-block; min-width: 18px; text-align: center; border-radius: 4px; font-size: 12px; margin-left: 6px; padding: 0 4px; background: #f0f0f0; color: #666; font-style: normal; }
.ti-badge.danger { background: #ffece8; color: #d03050; }
.ti-badge.warn { background: #fff7e6; color: #f0a020; }
</style>
