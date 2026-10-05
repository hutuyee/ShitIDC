<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NModal, NRate, NSelect, NTag, NTreeSelect, useMessage } from 'naive-ui'
import { useRouter } from 'vue-router'
import { api, dataOf } from '../api'
import { useAuthStore } from '../stores/auth'
import { ADMIN_PATH } from '../adminPath'

// 内部工单（对齐魔方 CBAP TicketInternalPremium 插件）。
// 列表支持关键词 / 类型 / 状态 / 发起人 / 跟进人 / 领取人筛选与自动刷新；
// 行操作：接单（配置开启时）、转单、关闭、评分（发起人或部门主管）。
// 详情页在 AdminTicketInternalDetail.vue。

const message = useMessage()
const router = useRouter()
const auth = useAuthStore()

const rows = ref<any[]>([])
const total = ref(0)
const busy = ref(false)
const departments = ref<any[]>([])
const statuses = ref<any[]>([])
const staff = ref<any[]>([])
const config = reactive<{ order_button: string; follow_limit: string; will_timeout_notice: string; refresh_time: string }>({
  order_button: '0', follow_limit: '0', will_timeout_notice: '0', refresh_time: '180',
})

const filters = reactive({
  keywords: '',
  type_ids: [] as number[],
  status_ids: [] as number[],
  post_admin_id: null as number | null,
  last_reply_admin_id: null as number | null,
  order_admin_id: null as number | null,
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
// NTreeSelect 只返回叶子类型 ID；部门 ID 只用于展示，不参与筛选。
const staffOptions = computed(() => staff.value.map((s: any) => ({ label: `${s.name}`, value: s.id })))
const statusOptions = computed(() => statuses.value.map((s: any) => ({ label: s.name, value: s.id })))

async function load() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/ticket-internal/tickets', {
      params: {
        keywords: filters.keywords.trim(),
        type_ids: filters.type_ids.filter((v) => typeof v === 'number').join(','),
        status_ids: filters.status_ids.join(','),
        post_admin_id: filters.post_admin_id || '',
        last_reply_admin_id: filters.last_reply_admin_id || '',
        order_admin_id: filters.order_admin_id || '',
        page: filters.page,
        limit: filters.limit,
      },
    }))
    rows.value = d?.list || []
    total.value = d?.count || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取内部工单失败')
  } finally {
    busy.value = false
  }
}

async function loadBase() {
  try {
    const [dept, st, sf, cfg] = await Promise.all([
      api.get('/admin/ticket-internal/department'),
      api.get('/admin/ticket-internal/status'),
      api.get('/admin/ticket-internal/staff'),
      api.get('/admin/ticket-internal/config'),
    ])
    departments.value = dataOf<any>(dept)?.list || []
    statuses.value = dataOf<any>(st)?.list || []
    staff.value = dataOf<any>(sf) || []
    Object.assign(config, dataOf<any>(cfg) || {})
  } catch {
    message.error('读取内部工单基础数据失败')
  }
}

function resetAndLoad() {
  filters.page = 1
  load()
}
function statusTag(row: any) {
  return { background: row.color || '#0052D9', color: '#fff' }
}
function timeoutMark(row: any) {
  if (row.finished) return row.timeout === 1 ? '超' : '✓'
  if (row.timeout === 1) return '超'
  if (row.timeout === 2) return '⚠'
  return ''
}
function timeoutTitle(row: any) {
  if (row.timeout === 1) return '已超时'
  if (row.timeout === 2) return '即将超时'
  return row.finished ? '工单已完成' : ''
}
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '--')

let timer: number | null = null
const refreshMinutes = ref(0)
function applyRefresh(minutes: number) {
  refreshMinutes.value = minutes
  if (timer) { clearInterval(timer); timer = null }
  if (minutes > 0) timer = window.setInterval(load, minutes * 60 * 1000)
}
onMounted(async () => { await loadBase(); load() })
onBeforeUnmount(() => { if (timer) clearInterval(timer) })

// ---- 新建 ----
const createOpen = ref(false)
const createBusy = ref(false)
const hosts = ref<any[]>([])
const createForm = reactive({
  title: '',
  priority: 'normal',
  department_id: null as number | null,
  type_id: null as number | null,
  client_ref: '',
  source_ticket: '',
  host_id: [] as string[],
  content: '',
  notes: '',
})
const createTypes = computed(() => {
  const d = departments.value.find((x: any) => x.id === createForm.department_id)
  return (d?.type || []).map((t: any) => ({ label: `${t.name}（${t.processing_limit}h）`, value: t.id }))
})
const hostOptions = computed(() => hosts.value.map((h: any) => ({
  label: `${h.product_name}（${h.service_id.slice(0, 8)}）`,
  value: h.id,
})))
function openCreate() {
  Object.assign(createForm, {
    title: '', priority: 'normal', department_id: null, type_id: null,
    client_ref: '', source_ticket: '', host_id: [], content: '', notes: '',
  })
  hosts.value = []
  createOpen.value = true
}
async function loadHosts() {
  const ref = createForm.client_ref.trim()
  if (!ref) { hosts.value = []; return }
  try {
    hosts.value = dataOf<any>(await api.get('/admin/ticket-internal/hosts', { params: { user_id: ref } })) || []
    if (!hosts.value.length) message.info('该用户名下暂无可关联产品')
  } catch (e: any) {
    hosts.value = []
    message.error(e?.response?.data?.error?.message || '未找到该用户')
  }
}
async function submitCreate() {
  if (!createForm.title.trim()) { message.error('请填写工单标题'); return }
  if (!createForm.department_id || !createForm.type_id) { message.error('请选择工单部门与类型'); return }
  createBusy.value = true
  try {
    const hostIDs = createForm.host_id.map((v) => Number(String(v).replace(/^s-/, '')))
    const r = dataOf<any>(await api.post('/admin/ticket-internal/tickets', {
      title: createForm.title.trim(),
      priority: createForm.priority,
      department_id: createForm.department_id,
      type_id: createForm.type_id,
      client_id: createForm.client_ref.trim(),
      ticket_id: createForm.source_ticket.trim(),
      host_id: hostIDs,
      content: createForm.content,
      notes: createForm.notes,
      attachment: [],
    }))
    message.success(`已创建 ${r?.ticket_num || '内部工单'}`)
    createOpen.value = false
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '创建失败')
  } finally {
    createBusy.value = false
  }
}

// ---- 接单 / 关闭 ----
async function accept(row: any) {
  try {
    await api.post(`/admin/ticket-internal/tickets/${row.id}/accept`)
    message.success('接单成功')
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '接单失败')
  }
}
const closeRow = ref<any>(null)
const closeOpen = ref(false)
function askClose(row: any) { closeRow.value = row; closeOpen.value = true }
async function doClose() {
  if (!closeRow.value) return
  try {
    await api.post(`/admin/ticket-internal/tickets/${closeRow.value.id}/close`)
    message.success('工单已关闭')
    closeOpen.value = false
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '关闭失败')
  }
}

// ---- 转单 ----
const forwardOpen = ref(false)
const forwardBusy = ref(false)
const forwardRow = ref<any>(null)
const forwardForm = reactive({ department_id: null as number | null, type_id: null as number | null, admin_id: null as number | null, notes: '' })
const forwardTypes = computed(() => {
  const d = departments.value.find((x: any) => x.id === forwardForm.department_id)
  return (d?.type || []).map((t: any) => ({ label: t.name, value: t.id }))
})
const forwardAdmins = computed(() => {
  const d = departments.value.find((x: any) => x.id === forwardForm.department_id)
  return (d?.admin || []).map((s: any) => ({ label: s.name, value: s.id }))
})
function openForward(row: any) {
  forwardRow.value = row
  Object.assign(forwardForm, { department_id: row.department_id, type_id: row.type_id, admin_id: null, notes: '' })
  forwardOpen.value = true
}
async function submitForward() {
  if (!forwardRow.value) return
  if (!forwardForm.department_id || !forwardForm.type_id) { message.error('请选择转交部门与类型'); return }
  forwardBusy.value = true
  try {
    await api.post(`/admin/ticket-internal/tickets/${forwardRow.value.id}/forward`, {
      department_id: forwardForm.department_id,
      type_id: forwardForm.type_id,
      admin_id: forwardForm.admin_id || 0,
      notes: forwardForm.notes,
    })
    message.success('转单成功')
    forwardOpen.value = false
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '转单失败')
  } finally {
    forwardBusy.value = false
  }
}

// ---- 评分 ----
const scoreOpen = ref(false)
const scoreBusy = ref(false)
const scoreRow = ref<any>(null)
const scoreForm = reactive({ satisfaction: 0, attitude: 0, processing_time: 0 })
function openScore(row: any) {
  scoreRow.value = row
  Object.assign(scoreForm, { satisfaction: 0, attitude: 0, processing_time: 0 })
  scoreOpen.value = true
}
async function submitScore() {
  if (!scoreRow.value) return
  if (!scoreForm.satisfaction || !scoreForm.attitude || !scoreForm.processing_time) {
    message.warning('请完成三项评分（最低半星）')
    return
  }
  scoreBusy.value = true
  try {
    await api.post(`/admin/ticket-internal/tickets/${scoreRow.value.id}/score`, { ...scoreForm })
    message.success('评分成功')
    scoreOpen.value = false
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '评分失败')
  } finally {
    scoreBusy.value = false
  }
}

function openDetail(row: any) {
  router.push(`${ADMIN_PATH}/ticket-internal/${row.id}`)
}
const canManage = computed(() => !!auth.permissions['ticket_internal.manage'])
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">客服工具</div>
        <h1>内部工单</h1>
        <p>管理端内部流转工单：接单、回复、转单、处理完成与评分，按部门类型时限统计超时。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary @click="router.push(ADMIN_PATH + '/ticket-internal/settings')">工单配置</NButton>
        <NButton secondary @click="router.push(ADMIN_PATH + '/ticket-internal/cron')">定时工单</NButton>
        <NButton secondary @click="router.push(ADMIN_PATH + '/ticket-internal/stats')">工单统计</NButton>
        <NButton v-if="canManage" type="primary" @click="openCreate">＋ 新建内部工单</NButton>
      </div>
    </div>

    <div class="users-toolbar" style="flex-wrap:wrap;row-gap:8px">
      <NInput v-model:value="filters.keywords" clearable placeholder="内部工单编号、工单标题" style="max-width:220px" @keyup.enter="resetAndLoad" />
      <NTreeSelect v-model:value="filters.type_ids" :options="typeTree" multiple clearable checkable cascade
        placeholder="工单部门 / 类型" style="min-width:220px" />
      <NSelect v-model:value="filters.status_ids" :options="statusOptions" multiple clearable placeholder="工单状态" style="min-width:170px" />
      <NSelect v-model:value="filters.post_admin_id" :options="staffOptions" filterable clearable placeholder="发起人" style="width:150px" />
      <NSelect v-model:value="filters.last_reply_admin_id" :options="staffOptions" filterable clearable placeholder="跟进人" style="width:150px" />
      <NSelect v-model:value="filters.order_admin_id" :options="staffOptions" filterable clearable placeholder="领取人" style="width:150px" />
      <NButton secondary :loading="busy" @click="resetAndLoad">查询</NButton>
      <NSelect v-model:value="refreshMinutes" :options="[{ label: '自动刷新：关', value: 0 }, { label: '1 分钟', value: 1 }, { label: '3 分钟', value: 3 }, { label: '5 分钟', value: 5 }, { label: '10 分钟', value: 10 }]"
        style="width:150px" @update:value="applyRefresh" />
      <span class="muted" style="font-size:12px">共 {{ total }} 条</span>
    </div>

    <div v-if="rows.length" class="table-scroll"><div class="user-table">
      <div class="user-row ti-row user-head">
        <span>ID</span><span>工单标题</span><span>工单部门</span><span>领取人</span><span>发起人（跟进人）</span><span>最近回复时间</span><span>当前状态</span><span>接单</span><span>操作</span>
      </div>
      <div v-for="r in rows" :key="r.id" class="user-row ti-row">
        <span class="muted">#{{ r.ticket_num }}</span>
        <span>
          <a v-if="timeoutMark(r)" :title="timeoutTitle(r)" class="ti-badge" :class="{ danger: r.timeout === 1, warn: r.timeout === 2 && !r.finished, ok: r.finished && r.timeout === 0 }">{{ timeoutMark(r) }}</a>
          <a href="javascript:;" style="font-weight:600" @click="openDetail(r)">{{ r.title }}</a>
        </span>
        <span>{{ r.department_name }} - {{ r.type_name }}</span>
        <span>{{ r.order_admin_name || '--' }}</span>
        <span>{{ r.post_admin_name }} ({{ r.last_reply_admin_name || '--' }})</span>
        <span class="muted">{{ r.last_reply_time ? fmt(r.last_reply_time) : '--' }}</span>
        <span><NTag :style="statusTag(r)" size="small" round>{{ r.status }}</NTag></span>
        <span>
          <NButton v-if="config.order_button === '1' && !r.finished && !r.order_admin_id" size="tiny" type="primary" @click="accept(r)">接单</NButton>
          <span v-else class="muted">--</span>
        </span>
        <span class="row" style="gap:4px">
          <NButton size="tiny" tertiary @click="openForward(r)">转单</NButton>
          <NButton v-if="r.status !== '已关闭'" size="tiny" tertiary type="error" @click="askClose(r)">关闭</NButton>
          <NButton v-if="r.show_score" size="tiny" tertiary type="warning" @click="openScore(r)">评分</NButton>
        </span>
      </div>
    </div></div>
    <div v-else class="empty-box">{{ busy ? '加载中…' : '还没有内部工单。' }}</div>

    <div v-if="total > filters.limit" class="row" style="justify-content:flex-end;gap:8px;margin-top:10px">
      <NButton size="small" :disabled="filters.page <= 1" @click="filters.page--; load()">上一页</NButton>
      <span class="muted">第 {{ filters.page }} / {{ Math.ceil(total / filters.limit) }} 页</span>
      <NButton size="small" :disabled="filters.page >= Math.ceil(total / filters.limit)" @click="filters.page++; load()">下一页</NButton>
    </div>

    <NModal v-model:show="createOpen" preset="card" title="新建内部工单" style="width:min(860px,96vw)">
      <div class="form-grid">
        <label class="full"><span>工单标题</span><NInput v-model:value="createForm.title" maxlength="150" placeholder="工单标题" /></label>
        <label><span>紧急程度</span>
          <NSelect v-model:value="createForm.priority" :options="[{ label: '一般', value: 'normal' }, { label: '紧急', value: 'urgent' }]" />
        </label>
        <label><span>工单部门</span>
          <NSelect v-model:value="createForm.department_id" :options="departments.map((d: any) => ({ label: d.name, value: d.id }))" placeholder="工单部门"
            @update:value="createForm.type_id = null" />
        </label>
        <label><span>工单类型</span>
          <NSelect v-model:value="createForm.type_id" :options="createTypes" placeholder="工单类型" />
        </label>
        <label><span>关联用户（邮箱 / 用户ID，可留空）</span>
          <div class="row" style="gap:6px">
            <NInput v-model:value="createForm.client_ref" placeholder="输入后点击右侧加载产品" style="flex:1" @blur="loadHosts" />
            <NButton secondary @click="loadHosts">加载产品</NButton>
          </div>
        </label>
        <label class="full"><span>关联产品（可多选）</span>
          <NSelect v-model:value="createForm.host_id" :options="hostOptions" multiple clearable placeholder="先填写关联用户" />
        </label>
        <label class="full"><span>关联工单（用户工单公开 ID，可留空）</span><NInput v-model:value="createForm.source_ticket" placeholder="例如 550e8400-e29b-41d4-a716-446655440000" /></label>
        <label class="full"><span>详细描述</span><NInput v-model:value="createForm.content" type="textarea" :rows="5" placeholder="问题描述（支持 HTML）" /></label>
        <label class="full"><span>备注（内部）</span><NInput v-model:value="createForm.notes" type="textarea" :rows="3" placeholder="备注" /></label>
      </div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="createOpen = false">取消</NButton>
          <NButton type="primary" :loading="createBusy" @click="submitCreate">提交</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="forwardOpen" preset="card" title="转工单" style="width:min(520px,96vw)">
      <div class="form-grid">
        <label><span>转交部门</span>
          <NSelect v-model:value="forwardForm.department_id" :options="departments.map((d: any) => ({ label: d.name, value: d.id }))"
            @update:value="forwardForm.type_id = null; forwardForm.admin_id = null" />
        </label>
        <label><span>工单类型</span><NSelect v-model:value="forwardForm.type_id" :options="forwardTypes" /></label>
        <label><span>转交人员</span><NSelect v-model:value="forwardForm.admin_id" :options="forwardAdmins" filterable clearable /></label>
        <label class="full"><span>转交备注</span><NInput v-model:value="forwardForm.notes" type="textarea" :rows="3" /></label>
      </div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="forwardOpen = false">取消</NButton>
          <NButton type="primary" :loading="forwardBusy" @click="submitForward">保存</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="scoreOpen" preset="card" title="内部工单评分" style="width:min(460px,96vw)">
      <div v-if="scoreRow" class="muted" style="margin-bottom:8px">#{{ scoreRow.ticket_num }} {{ scoreRow.title }}</div>
      <div class="form-grid">
        <label><span>处理满意度</span><NRate v-model:value="scoreForm.satisfaction" allow-half color="#FFC329" /></label>
        <label><span>服务态度</span><NRate v-model:value="scoreForm.attitude" allow-half color="#FFC329" /></label>
        <label><span>处理时效</span><NRate v-model:value="scoreForm.processing_time" allow-half color="#FFC329" /></label>
      </div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="scoreOpen = false">取消</NButton>
          <NButton type="primary" :loading="scoreBusy" @click="submitScore">提交</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="closeOpen" preset="card" title="确认关闭该工单？" style="width:min(420px,96vw)">
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="closeOpen = false">取消</NButton>
          <NButton type="primary" @click="doClose">确定</NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
.ti-row { grid-template-columns: 110px minmax(220px, 1.4fr) 200px 110px 180px 150px 100px 90px 190px; }
.ti-row span { overflow: hidden; text-overflow: ellipsis; }
.ti-badge { display: inline-block; min-width: 18px; text-align: center; border-radius: 4px; font-size: 12px; margin-right: 6px; padding: 0 4px; background: #f0f0f0; color: #666; }
.ti-badge.danger { background: #ffece8; color: #d03050; }
.ti-badge.warn { background: #fff7e6; color: #f0a020; }
.ti-badge.ok { background: #e8f7ee; color: #18a058; }
</style>
