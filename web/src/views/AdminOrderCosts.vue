<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NCheckbox, NDatePicker, NInput, NInputNumber, NModal, NSelect, NSwitch, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'

// 成本支出（对齐魔方 cost_pay 插件）：按订单登记支出，自定义字段可管理。
// 契约：GET/POST /admin/orders/:id/cost-pay、GET/PUT/DELETE /admin/cost-pay/:id、
//       /admin/cost-pay/self-defined-field 增删改 + show-list + drag、/admin/cost-pay/summary。

const message = useMessage()
const route = useRoute()
const router = useRouter()

const orderID = computed(() => String(route.query.order_id || '').trim())
const order = ref<any>(null)
const list = ref<any[]>([])
const count = ref(0)
const fields = ref<any[]>([])
const owners = ref<string[]>([])
const summary = ref<any[]>([])
const loading = ref(false)
const busy = ref(false)

const orders = ref<any[]>([])
const pickOrderID = ref('')

const filters = reactive({
  keywords: '',
  owner: '',
  costRange: null as [number, number] | null,
  createRange: null as [number, number] | null,
  page: 1,
  limit: 10,
})

const statusText = (s: string) => ({ completed: '已完成', processing: '开通中', paid: '已支付', unpaid: '未支付', cancelled: '已取消', refunded: '已退款' } as any)[s] || s
const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const fmtTime = (ts: any) => (ts ? new Date(ts).toLocaleString('zh-CN', { hour12: false }) : '—')
const seconds = (ms: number) => Math.floor(ms / 1000)

const visibleFields = computed(() => fields.value.filter((f: any) => f.show_list))
const colTemplate = computed(() => {
  const dyn = visibleFields.value.map(() => '140px')
  return ['170px', '150px', '130px', '100px', ...dyn, '180px', '160px', '96px'].join(' ')
})
const ownerOptions = computed(() => owners.value.map((o) => ({ label: o, value: o })))
const orderOptions = computed(() => orders.value.map((o: any) => ({
  value: o.id,
  label: `${String(o.id).slice(0, 8)} · ${statusText(o.status)} · ${money(o.total_cents, o.currency)}`,
})))

async function loadOrders() {
  try { orders.value = dataOf<any[]>(await api.get('/admin/orders', { params: { limit: 200 } })) || [] }
  catch { orders.value = [] }
}
function applyOrder() {
  if (!pickOrderID.value) { message.warning('请先选择订单'); return }
  router.replace({ path: ADMIN_PATH + '/order-costs', query: { order_id: pickOrderID.value } })
}
async function loadSummary() {
  try { summary.value = dataOf<any[]>(await api.get('/admin/cost-pay/summary')) || [] }
  catch { summary.value = [] }
}
async function load() {
  if (!orderID.value) return
  loading.value = true
  try {
    const params: any = { page: filters.page, limit: filters.limit }
    if (filters.keywords.trim()) params.keywords = filters.keywords.trim()
    if (filters.owner) params.owner = filters.owner
    if (filters.costRange) { params.start_cost_time = seconds(filters.costRange[0]); params.end_cost_time = seconds(filters.costRange[1]) }
    if (filters.createRange) { params.start_create_time = seconds(filters.createRange[0]); params.end_create_time = seconds(filters.createRange[1]) }
    const res = dataOf<any>(await api.get(`/admin/orders/${orderID.value}/cost-pay`, { params }))
    order.value = res.order || null
    list.value = res.list || []
    count.value = Number(res.count || 0)
    owners.value = res.owner || []
    fields.value = res.self_defined_field || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取支出失败')
  } finally { loading.value = false }
}
function optionList(f: any) {
  return String(f.field_option || '').split(',').map((x: string) => x.trim()).filter(Boolean).map((x: string) => ({ label: x, value: x }))
}
function cancelFieldEdit() {
  fieldForm.id = ''
  fieldForm.field_name = ''
  fieldForm.field_type = 'text'
  fieldForm.is_required = false
  fieldForm.field_option = ''
}
function search() { filters.page = 1; load() }
function resetFilters() {
  filters.keywords = ''; filters.owner = ''; filters.costRange = null; filters.createRange = null; filters.page = 1; load()
}
function changePage(delta: number) {
  const max = Math.max(1, Math.ceil(count.value / filters.limit))
  const next = Math.min(max, Math.max(1, filters.page + delta))
  if (next !== filters.page) { filters.page = next; load() }
}

// ---- 支出记录 新增 / 编辑 ----
const costModal = ref(false)
const costForm = reactive({ id: '', name: '', owner: '', cost: 0, costTime: Date.now(), notes: '', values: {} as Record<string, string> })

function openCreate() {
  costForm.id = ''
  costForm.name = ''
  costForm.owner = ''
  costForm.cost = 0
  costForm.costTime = Date.now()
  costForm.notes = ''
  costForm.values = {}
  for (const f of fields.value) costForm.values[f.id] = ''
  costModal.value = true
}
function openEdit(row: any) {
  costForm.id = row.id
  costForm.name = row.name
  costForm.owner = row.owner
  costForm.cost = Number(row.cost_cents || 0) / 100
  costForm.costTime = new Date(row.cost_time).getTime()
  costForm.notes = row.notes || ''
  costForm.values = {}
  for (const f of fields.value) costForm.values[f.id] = row.self_defined_field?.[f.id] || ''
  costModal.value = true
}
async function saveCost() {
  if (!costForm.name.trim()) { message.error('请填写支出名称'); return }
  if (!costForm.owner.trim()) { message.error('请填写所属主体'); return }
  for (const f of fields.value) {
    if (f.is_required && !String(costForm.values[f.id] || '').trim()) { message.error(`请填写自定义字段「${f.field_name}」`); return }
  }
  busy.value = true
  try {
    const payload = {
      name: costForm.name.trim(),
      owner: costForm.owner.trim(),
      cost_cents: Math.round(Number(costForm.cost || 0) * 100),
      cost_time: seconds(costForm.costTime),
      notes: costForm.notes.trim(),
      self_defined_field: costForm.values,
    }
    if (costForm.id) await api.put(`/admin/cost-pay/${costForm.id}`, payload)
    else await api.post(`/admin/orders/${orderID.value}/cost-pay`, payload)
    message.success(costForm.id ? '支出已更新' : '支出已登记')
    costModal.value = false
    await Promise.all([load(), loadSummary()])
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}
async function removeCost(row: any) {
  try {
    await api.delete(`/admin/cost-pay/${row.id}`)
    message.success('已删除')
    await Promise.all([load(), loadSummary()])
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---- 自定义字段管理 ----
const fieldModal = ref(false)
const fieldForm = reactive({ id: '', field_name: '', field_type: 'text', is_required: false, field_option: '' })
function openFields() {
  fieldForm.id = ''
  fieldForm.field_name = ''
  fieldForm.field_type = 'text'
  fieldForm.is_required = false
  fieldForm.field_option = ''
  fieldModal.value = true
}
function editField(f: any) {
  fieldForm.id = f.id
  fieldForm.field_name = f.field_name
  fieldForm.field_type = f.field_type
  fieldForm.is_required = !!f.is_required
  fieldForm.field_option = f.field_option || ''
}
async function saveField() {
  if (!fieldForm.field_name.trim()) { message.error('请填写字段名称'); return }
  if (fieldForm.field_type === 'dropdown' && !fieldForm.field_option.trim()) { message.error('下拉类型必须填写下拉值（英文逗号分隔）'); return }
  busy.value = true
  try {
    const payload = { field_name: fieldForm.field_name.trim(), field_type: fieldForm.field_type, is_required: fieldForm.is_required, field_option: fieldForm.field_option.trim() }
    if (fieldForm.id) await api.put(`/admin/cost-pay/self-defined-field/${fieldForm.id}`, payload)
    else await api.post('/admin/cost-pay/self-defined-field', payload)
    message.success(fieldForm.id ? '字段已更新' : '字段已新增')
    fieldForm.id = ''
    fieldForm.field_name = ''
    fieldForm.field_type = 'text'
    fieldForm.is_required = false
    fieldForm.field_option = ''
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}
async function removeField(f: any) {
  try {
    await api.delete(`/admin/cost-pay/self-defined-field/${f.id}`)
    message.success('字段已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}
async function toggleShowList(f: any) {
  try {
    await api.put(`/admin/cost-pay/self-defined-field/${f.id}/show-list`, { show_list: !f.show_list })
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}
async function moveField(index: number, dir: -1 | 1) {
  const cur = fields.value[index]
  const prevID = dir === -1 ? (index >= 2 ? fields.value[index - 2].id : '0') : fields.value[index + 1]?.id
  if (!cur || !prevID) return
  try {
    await api.put(`/admin/cost-pay/self-defined-field/${cur.id}/drag`, { prev_id: prevID })
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '排序失败') }
}

onMounted(() => {
  loadSummary()
  if (!orderID.value) loadOrders()
  else load()
})
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>成本支出</h1>
        <p>对齐魔方「成本支出」插件：按订单登记支出（名称 / 所属主体 / 金额 / 支出时间 / 备注），支持自定义字段（文本框、下拉、必填、列表展示、排序）。</p>
      </div>
      <NButton :loading="loading" secondary @click="load">刷新</NButton>
    </div>

    <section v-if="!orderID" class="panel">
      <div class="panel-title-row"><div><h2>选择订单</h2><span>成本支出按订单登记，先选一笔订单</span></div></div>
      <div class="row" style="gap:10px;flex-wrap:wrap">
        <NSelect v-model:value="pickOrderID" filterable :options="orderOptions" placeholder="选择订单（最近 200 笔）" style="width:min(520px,90vw)" />
        <NButton type="primary" @click="applyOrder">进入</NButton>
        <NButton quaternary @click="loadOrders">刷新订单</NButton>
      </div>
      <p class="muted" style="margin:10px 0 0">也可以从 <router-link :to="ADMIN_PATH + '/orders'">订单中心</router-link> 每行的「成本支出」按钮进入。</p>
    </section>

    <template v-else>
      <section class="panel" v-if="order">
        <div class="panel-title-row">
          <div>
            <h2>订单 #{{ String(order.id).slice(0, 8) }}</h2>
            <span>{{ statusText(order.status) }} · 订单金额 {{ money(order.total_cents, order.currency) }} · 创建于 {{ fmtTime(order.created_at) }}</span>
          </div>
          <NButton quaternary @click="router.push(ADMIN_PATH + '/orders')">返回订单中心</NButton>
        </div>
      </section>

      <section class="panel" v-if="summary.length">
        <div class="panel-title-row"><div><h2>支出合计</h2><span>按币种汇总的今日 / 本月 / 今年支出（对应插件看板 widget）</span></div></div>
        <div class="row" style="gap:26px;flex-wrap:wrap">
          <div v-for="s in summary" :key="s.currency">
            <b>{{ s.currency }}</b>
            <div class="muted">今日 {{ money(s.today_cents, s.currency) }} · 本月 {{ money(s.month_cents, s.currency) }} · 今年 {{ money(s.year_cents, s.currency) }}</div>
          </div>
        </div>
      </section>

      <section class="panel">
        <div class="panel-title-row">
          <div><h2>支出记录</h2><span>共 {{ count }} 条</span></div>
          <div class="row" style="gap:8px">
            <NButton secondary @click="openFields">字段管理</NButton>
            <NButton type="primary" @click="openCreate">新增支出</NButton>
          </div>
        </div>

        <div class="row" style="gap:8px;flex-wrap:wrap;margin-bottom:14px">
          <NInput v-model:value="filters.keywords" placeholder="关键词（支出名称 / 备注）" clearable style="width:210px" @keyup.enter="search" />
          <NSelect v-model:value="filters.owner" :options="ownerOptions" placeholder="所属主体" clearable style="width:160px" />
          <NDatePicker v-model:value="filters.costRange" type="datetimerange" clearable placeholder="支出日期" style="width:350px" />
          <NDatePicker v-model:value="filters.createRange" type="datetimerange" clearable placeholder="最近记录时间" style="width:350px" />
          <NButton @click="search">查询</NButton>
          <NButton quaternary @click="resetFilters">重置</NButton>
        </div>

        <div v-if="list.length" class="table-scroll">
          <div class="cost-grid" :style="{ '--cost-cols': colTemplate }">
            <div class="cost-row cost-head">
              <span>支出名称</span>
              <span>支出日期</span>
              <span>所属主体</span>
              <span>支出金额</span>
              <span v-for="f in visibleFields" :key="f.id">{{ f.field_name }}</span>
              <span>最近记录时间（记录人）</span>
              <span>备注</span>
              <span>操作</span>
            </div>
            <div v-for="row in list" :key="row.id" class="cost-row">
              <span><b>{{ row.name }}</b></span>
              <span>{{ fmtTime(row.cost_time) }}</span>
              <span>{{ row.owner }}</span>
              <span>{{ money(row.cost_cents, order?.currency || 'CNY') }}</span>
              <span v-for="f in visibleFields" :key="f.id">{{ row.self_defined_field?.[f.id] || '—' }}</span>
              <span>{{ fmtTime(row.create_time) }}<small>{{ row.admin_name || '—' }}</small></span>
              <span class="muted">{{ row.notes || '—' }}</span>
              <span class="row" style="gap:6px">
                <NButton size="tiny" tertiary @click="openEdit(row)">编辑</NButton>
                <NButton size="tiny" tertiary type="error" @click="removeCost(row)">删除</NButton>
              </span>
            </div>
          </div>
        </div>
        <div v-else class="empty-box">该订单还没有支出记录。</div>

        <div class="row" style="gap:8px;align-items:center;margin-top:12px" v-if="count > filters.limit">
          <NButton size="small" secondary :disabled="filters.page <= 1" @click="changePage(-1)">上一页</NButton>
          <span class="muted">第 {{ filters.page }} / {{ Math.max(1, Math.ceil(count / filters.limit)) }} 页</span>
          <NButton size="small" secondary :disabled="filters.page >= Math.ceil(count / filters.limit)" @click="changePage(1)">下一页</NButton>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.cost-grid{display:flex;flex-direction:column;font-size:12px;min-width:1000px}
.cost-row{display:grid;grid-template-columns:var(--cost-cols);gap:10px;align-items:start;padding:10px 0;border-bottom:1px solid var(--border)}
.cost-head{font-size:10px;font-weight:700;color:var(--muted)}
.cost-row small{display:block;color:var(--muted);font-size:10px;margin-top:2px}
.field-table{display:flex;flex-direction:column;font-size:12px;min-width:560px}
.field-row{display:grid;grid-template-columns:90px minmax(150px,1fr) 100px 100px 140px;gap:10px;align-items:center;padding:9px 0;border-bottom:1px solid var(--border)}
.field-head{font-size:10px;font-weight:700;color:var(--muted)}
.field-row small{display:block;color:var(--muted);font-size:9px;margin-top:2px}
</style>
