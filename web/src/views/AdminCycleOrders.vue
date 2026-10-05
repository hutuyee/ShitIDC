<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NInput, NInputNumber, NModal, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 周期人工订单（对齐魔方 CBAP 插件 CycleArtificialOrder）。
//
// 生成规则按「num + unit」的周期在开始 / 结束时间内为用户生成人工订单（首次按
// 订单金额，之后按续费金额）；详情页可筛选、调整价格、标记支付（可勾选优先扣除
// 余额）与批量删除。人工订单支付后不开通服务，交给管理员线下处理。

const message = useMessage()
const items = ref<any[]>([])
const total = ref(0)
const busy = ref(false)
const page = ref(1)
const keywords = ref('')
const pages = computed(() => Math.max(1, Math.ceil(total.value / 20)))

const unitOptions = [
  { label: '天', value: 'day' },
  { label: '月', value: 'month' },
  { label: '年', value: 'year' },
]
const unitText = (u: string) => unitOptions.find(o => o.value === u)?.label || u
const money = (yuan?: number) => `¥${Number(yuan || 0).toFixed(2)}`
const moneyCents = (cents?: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmtUnix = (ts?: number) => (ts ? new Date(ts * 1000).toLocaleString() : '—')
const dayText = (ts?: number) => (ts ? new Date(ts * 1000).toLocaleDateString() : '—')
const userText = (v: any) => `${v?.username || v?.email || '—'}${v?.company ? `（${v.company}）` : ''}`

const statusMeta: Record<string, { text: string; type: any }> = {
  unpaid: { text: '未付款', type: 'warning' },
  paid: { text: '已付款', type: 'info' },
  processing: { text: '开通中', type: 'info' },
  completed: { text: '已完成', type: 'success' },
  cancelled: { text: '已取消', type: 'default' },
  refunded: { text: '已退款', type: 'info' },
}
const statusOptions = [
  { label: '全部状态', value: '' },
  { label: '未付款', value: 'unpaid' },
  { label: '已完成', value: 'completed' },
  { label: '开通中', value: 'processing' },
  { label: '已取消', value: 'cancelled' },
  { label: '已退款', value: 'refunded' },
]

async function load() {
  busy.value = true
  try {
    const params: any = { page: page.value, limit: 20 }
    if (keywords.value.trim()) params.keywords = keywords.value.trim()
    const res = dataOf<any>(await api.get('/admin/cycle-artificial-orders', { params }))
    items.value = res?.list || []
    total.value = res?.count || 0
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取周期人工订单失败') }
  finally { busy.value = false }
}

// ---------- 新增 / 编辑生成规则 ----------
const emptyForm = () => ({
  client_id: '', description: '', amount: 0, renew_amount: 0,
  start_at: null as string | null, end_at: null as string | null, num: 1, unit: 'day',
})
const form = reactive<any>(emptyForm())
const editing = ref('')
const showForm = ref(false)
const pickedUser = ref<any>(null)

function toLocalInput(ts?: number) {
  if (!ts) return null
  const d = new Date(ts * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function resetForm() {
  editing.value = ''
  Object.assign(form, emptyForm())
  pickedUser.value = null
  userKeyword.value = ''
  userResults.value = []
}

function openCreate() {
  resetForm()
  showForm.value = true
}

function openEdit(v: any) {
  resetForm()
  editing.value = v.id
  Object.assign(form, {
    client_id: v.client_id,
    description: v.description,
    amount: Number(v.amount) || 0,
    renew_amount: Number(v.renew_amount) || 0,
    start_at: toLocalInput(v.start_time),
    end_at: v.end_time ? toLocalInput(v.end_time) : null,
    num: v.num,
    unit: v.unit,
  })
  pickedUser.value = { id: v.client_id, label: v.email, sub: v.company || v.username || '', username: v.username, email: v.email, company: v.company }
  showForm.value = true
}

async function save() {
  if (!form.client_id) { message.error('请选择用户'); return }
  if (!form.description.trim()) { message.error('请填写订单描述'); return }
  if (!form.start_at) { message.error('请选择开始时间'); return }
  if (form.end_at && new Date(form.end_at) <= new Date(form.start_at)) { message.error('结束时间必须晚于开始时间'); return }
  if (!form.num || Number(form.num) <= 0) { message.error('生成周期必须为正整数'); return }
  const payload = {
    client_id: form.client_id,
    description: form.description.trim(),
    amount: Number(form.amount) || 0,
    renew_amount: Number(form.renew_amount) || 0,
    start_time: Math.floor(new Date(form.start_at).getTime() / 1000),
    end_time: form.end_at ? Math.floor(new Date(form.end_at).getTime() / 1000) : 0,
    num: Number(form.num) || 1,
    unit: form.unit,
  }
  busy.value = true
  try {
    if (editing.value) await api.put(`/admin/cycle-artificial-orders/${editing.value}`, payload)
    else await api.post('/admin/cycle-artificial-orders', payload)
    message.success(editing.value ? '已更新，生成周期从最近一次生成的日期起重新计算' : '已创建')
    showForm.value = false
    resetForm()
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}

async function removeCycle(v: any) {
  if (!window.confirm(`确认删除「${v.description}」的生成规则？已生成的订单会保留。`)) return
  try {
    await api.delete(`/admin/cycle-artificial-orders/${v.id}`)
    message.success('已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---------- 选择用户 ----------
const userKeyword = ref('')
const userResults = ref<any[]>([])

async function searchUsers() {
  const q = userKeyword.value.trim()
  if (!q) { userResults.value = []; return }
  try {
    userResults.value = dataOf<any[]>(await api.get('/admin/search', { params: { q, kind: 'user', limit: 20 } })) || []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '搜索失败') }
}

function addUser(u: any) {
  form.client_id = u.id
  pickedUser.value = u
  userResults.value = []
}

// ---------- 详情：子订单 ----------
const showDetail = ref(false)
const detail = ref<any>(null)
const children = ref<any[]>([])
const childTotal = ref(0)
const childPage = ref(1)
const childPages = computed(() => Math.max(1, Math.ceil(childTotal.value / 20)))
const childFilter = reactive({ status: '', gateway: '', amount: '' })
const childRange = reactive({ start: '', end: '' })
const selected = ref<string[]>([])

async function openDetail(v: any) {
  detail.value = v
  childPage.value = 1
  childFilter.status = ''
  childFilter.gateway = ''
  childFilter.amount = ''
  childRange.start = ''
  childRange.end = ''
  selected.value = []
  showDetail.value = true
  await loadChildren()
}

async function loadChildren() {
  if (!detail.value) return
  busy.value = true
  try {
    const params: any = { page: childPage.value, limit: 20, orderby: 'create_time', sort: 'desc' }
    if (childFilter.status) params.status = childFilter.status
    if (childFilter.gateway.trim()) params.gateway = childFilter.gateway.trim()
    if (childFilter.amount) params.amount = Number(childFilter.amount) || ''
    if (childRange.start) params.start_time = Math.floor(new Date(`${childRange.start}T00:00:00`).getTime() / 1000)
    if (childRange.end) params.end_time = Math.floor(new Date(`${childRange.end}T23:59:59`).getTime() / 1000)
    const res = dataOf<any>(await api.get(`/admin/cycle-artificial-orders/${detail.value.id}`, { params }))
    children.value = res?.list || []
    childTotal.value = res?.count || 0
    if (res?.cycle_order) detail.value = res.cycle_order
    selected.value = []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取子订单失败') }
  finally { busy.value = false }
}

function toggleSelect(id: string, checked: boolean) {
  if (checked) {
    if (!selected.value.includes(id)) selected.value = [...selected.value, id]
  } else {
    selected.value = selected.value.filter(x => x !== id)
  }
}

// ---------- 调整价格 ----------
const showPrice = ref(false)
const priceForm = reactive({ id: '', amount: 0, description: '' })

function openPrice(row: any) {
  priceForm.id = row.id
  priceForm.amount = Number(row.total_cents || 0) / 100
  priceForm.description = row.items?.[0]?.product_name || ''
  showPrice.value = true
}

async function savePrice() {
  if (!priceForm.description.trim()) { message.error('请填写订单描述'); return }
  busy.value = true
  try {
    await api.put(`/admin/artificial-orders/${priceForm.id}/price`, {
      amount: Number(priceForm.amount) || 0,
      description: priceForm.description.trim(),
    })
    message.success('已调整价格')
    showPrice.value = false
    await loadChildren()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '调整价格失败') }
  finally { busy.value = false }
}

// ---------- 标记支付 ----------
const showPay = ref(false)
const payForm = reactive({ id: '', use_credit: true, amount_cents: 0 })

function openPay(row: any) {
  payForm.id = row.id
  payForm.amount_cents = row.total_cents
  payForm.use_credit = true
  showPay.value = true
}

async function doMarkPaid() {
  busy.value = true
  try {
    const res = dataOf<any>(await api.post(`/admin/artificial-orders/${payForm.id}/mark-paid`, { use_credit: payForm.use_credit }))
    message.success(res?.credit_used_cents ? `已标记支付（扣除余额 ${moneyCents(res.credit_used_cents)}）` : '已标记支付')
    showPay.value = false
    await loadChildren()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '标记支付失败') }
  finally { busy.value = false }
}

// ---------- 删除子订单 ----------
async function removeChild(row: any) {
  if (!window.confirm('确认删除这笔未支付订单？删除后账单作废，已支付订单请走退款流程。')) return
  try {
    await api.delete(`/admin/artificial-orders/${row.id}`)
    message.success('已删除')
    await loadChildren()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

async function batchRemove() {
  if (!selected.value.length) { message.error('请先勾选要删除的订单'); return }
  if (!window.confirm(`确认删除选中的 ${selected.value.length} 笔未支付订单？`)) return
  try {
    const res = dataOf<any>(await api.post('/admin/cycle-artificial-orders/batch-delete', { id: selected.value }))
    message.success(`已删除 ${res?.cancelled ?? 0} 笔`)
    await loadChildren()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '批量删除失败') }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>周期人工订单</h1>
        <p>对齐魔方「周期人工订单」插件：按生成周期（天 / 月 / 年）为用户定期生成人工订单，首次按订单金额、之后按续费金额；生成的订单可筛选、调整价格、标记支付（可优先扣除余额）与批量删除。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton :loading="busy" secondary @click="load">刷新</NButton>
        <NButton type="primary" @click="openCreate">新增</NButton>
      </div>
    </div>

    <div class="audit-toolbar" style="grid-template-columns:minmax(180px,300px) auto">
      <NInput v-model:value="keywords" clearable placeholder="订单描述 / 用户邮箱 / 昵称 / 公司" @keyup.enter="() => { page = 1; load() }" />
      <NButton secondary :loading="busy" @click="() => { page = 1; load() }">查询</NButton>
    </div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>生成规则</h2><span>共 {{ total }} 条</span></div></div>
      <div v-if="items.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>ID</span><span>用户（公司）</span><span>订单描述</span><span>金额</span><span>续费金额</span><span>时间范围</span><span>生成周期</span><span>下次生成</span><span>操作</span></div>
        <div v-for="v in items" :key="v.id" class="audit-row">
          <span class="mono">{{ String(v.id).slice(0, 8) }}</span>
          <span>{{ userText(v) }}<small>{{ v.email }}</small></span>
          <span>{{ v.description }}<small>已生成 {{ v.generated_count }} 笔</small></span>
          <span><b>{{ money(v.amount) }}</b></span>
          <span>{{ money(v.renew_amount) }}</span>
          <span class="muted">{{ dayText(v.start_time) }} - {{ v.end_time ? dayText(v.end_time) : '∞' }}</span>
          <span>{{ v.num }}{{ unitText(v.unit) }}</span>
          <span class="muted">{{ v.next_generate_at ? fmtUnix(v.next_generate_at) : '已结束' }}</span>
          <span class="row" style="gap:6px;flex-wrap:wrap">
            <NButton size="tiny" tertiary @click="openEdit(v)">编辑</NButton>
            <NButton size="tiny" tertiary @click="openDetail(v)">详情</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeCycle(v)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有生成规则。点击右上角「新增」创建第一个周期人工订单。</div>
      <div class="row" style="justify-content:flex-end;gap:8px;margin-top:10px">
        <NButton size="small" secondary :disabled="page <= 1" @click="() => { page--; load() }">上一页</NButton>
        <span class="muted" style="align-self:center">{{ page }} / {{ pages }}</span>
        <NButton size="small" secondary :disabled="page >= pages" @click="() => { page++; load() }">下一页</NButton>
      </div>
    </section>

    <NModal v-model:show="showForm" preset="card" :title="editing ? '编辑生成规则' : '新增周期人工订单'" style="width:min(640px,94vw)">
      <div class="form-grid">
        <label class="full"><span>用户</span>
          <template v-if="editing">
            <NInput :value="userText(pickedUser)" disabled />
          </template>
          <template v-else>
            <div class="row" style="gap:8px">
              <NInput v-model:value="userKeyword" placeholder="按邮箱 / UID / UUID 搜索用户" @keyup.enter="searchUsers" />
              <NButton size="small" secondary @click="searchUsers">搜索</NButton>
            </div>
            <NTag v-if="pickedUser" closable style="margin-top:6px" @close="() => { pickedUser = null; form.client_id = '' }">{{ userText(pickedUser) }}</NTag>
            <div v-if="userResults.length" class="table-scroll" style="max-height:140px;overflow-y:auto;margin-top:6px">
              <div class="audit-table">
                <div v-for="u in userResults" :key="u.id" class="audit-row" style="grid-template-columns:minmax(140px,1fr) minmax(120px,.8fr) 70px">
                  <span>{{ u.label }}</span><span class="muted">{{ u.sub || '—' }}</span>
                  <span><NButton size="tiny" tertiary @click="addUser(u)">选择</NButton></span>
                </div>
              </div>
            </div>
          </template>
        </label>
        <label class="full"><span>订单描述</span><NInput v-model:value="form.description" type="textarea" :rows="2" placeholder="例如：2026 年度托管服务费" /></label>
        <label><span>订单金额（元）</span><NInputNumber v-model:value="form.amount" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>续费金额（元）</span><NInputNumber v-model:value="form.renew_amount" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>开始时间</span><input type="datetime-local" class="native-input" v-model="form.start_at" :disabled="!!editing" /></label>
        <label><span>结束时间（留空＝不限）</span><input type="datetime-local" class="native-input" v-model="form.end_at" /></label>
        <label><span>生成周期</span>
          <div class="row" style="gap:8px">
            <NInputNumber v-model:value="form.num" :min="1" :precision="0" style="flex:1" />
            <NSelect v-model:value="form.unit" :options="unitOptions" style="width:110px" />
          </div>
        </label>
        <label v-if="editing" class="full"><span></span><span class="muted">变更生成周期后，从最近一次已生成订单的日期开始计算。</span></label>
      </div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton secondary @click="showForm = false">取消</NButton>
          <NButton type="primary" :loading="busy" @click="save">{{ editing ? '保存修改' : '创建' }}</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="showDetail" preset="card" :title="`周期人工订单 · ${detail?.description || ''}`" style="width:min(1080px,96vw)">
      <div v-if="detail" class="stack">
        <p class="muted" style="margin:0">
          用户 <b>{{ userText(detail) }}</b> · 订单金额 <b>{{ money(detail.amount) }}</b> · 续费金额 <b>{{ money(detail.renew_amount) }}</b> ·
          时间范围 {{ dayText(detail.start_time) }} - {{ detail.end_time ? dayText(detail.end_time) : '∞' }} ·
          生成周期 <b>{{ detail.num }}{{ unitText(detail.unit) }}</b> ·
          下次生成 {{ detail.next_generate_at ? fmtUnix(detail.next_generate_at) : '已结束' }}（已生成 {{ detail.generated_count }} 笔）
        </p>
        <div class="audit-toolbar" style="grid-template-columns:minmax(120px,150px) minmax(110px,140px) minmax(110px,140px) minmax(130px,160px) minmax(130px,160px) auto auto">
          <NSelect v-model:value="childFilter.status" :options="statusOptions" @update:value="() => { childPage = 1; loadChildren() }" />
          <NInput v-model:value="childFilter.gateway" clearable placeholder="支付方式" @keyup.enter="() => { childPage = 1; loadChildren() }" />
          <NInput v-model:value="childFilter.amount" clearable placeholder="金额（元）" @keyup.enter="() => { childPage = 1; loadChildren() }" />
          <input type="date" class="native-input" v-model="childRange.start" />
          <input type="date" class="native-input" v-model="childRange.end" />
          <NButton secondary :loading="busy" @click="() => { childPage = 1; loadChildren() }">查询</NButton>
          <NButton type="error" secondary :disabled="!selected.length" @click="batchRemove">批量删除</NButton>
        </div>
        <div v-if="children.length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head" style="grid-template-columns:34px 90px minmax(180px,1.4fr) 110px 120px 155px 100px minmax(190px,auto)"><span></span><span>ID</span><span>描述</span><span>金额</span><span>支付方式</span><span>生成时间</span><span>状态</span><span>操作</span></div>
          <div v-for="o in children" :key="o.id" class="audit-row" style="grid-template-columns:34px 90px minmax(180px,1.4fr) 110px 120px 155px 100px minmax(190px,auto)">
            <span><NCheckbox :checked="selected.includes(o.id)" @update:checked="v => toggleSelect(o.id, v)" /></span>
            <span class="mono">{{ o.id.slice(0, 8) }}</span>
            <span>{{ (o.items || []).map((x: any) => x.product_name).join('、') || '—' }}</span>
            <span><b>{{ moneyCents(o.total_cents) }}</b></span>
            <span class="muted">{{ o.payment ? `${o.payment.method}${o.payment.type ? ' / ' + o.payment.type : ''}` : '未支付' }}</span>
            <span class="muted">{{ fmtUnix(Math.floor(Date.parse(o.created_at) / 1000)) }}</span>
            <span><NTag :type="statusMeta[o.status]?.type || 'default'" size="tiny" round>{{ statusMeta[o.status]?.text || o.status }}</NTag></span>
            <span class="row" style="gap:6px;flex-wrap:wrap">
              <NButton v-if="o.status === 'unpaid'" size="tiny" tertiary @click="openPrice(o)">调整价格</NButton>
              <NButton v-if="o.status === 'unpaid'" size="tiny" tertiary type="primary" @click="openPay(o)">标记支付</NButton>
              <NButton v-if="o.status === 'unpaid'" size="tiny" tertiary type="error" @click="removeChild(o)">删除</NButton>
              <span v-if="o.status !== 'unpaid'" class="muted">—</span>
            </span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有生成订单；到达开始时间后由调度器自动生成。</div>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton size="small" secondary :disabled="childPage <= 1" @click="() => { childPage--; loadChildren() }">上一页</NButton>
          <span class="muted" style="align-self:center">{{ childPage }} / {{ childPages }}</span>
          <NButton size="small" secondary :disabled="childPage >= childPages" @click="() => { childPage++; loadChildren() }">下一页</NButton>
        </div>
      </div>
    </NModal>

    <NModal v-model:show="showPrice" preset="card" title="调整价格" style="width:min(460px,94vw)">
      <div class="stack">
        <label><span>订单金额（元）</span><NInputNumber v-model:value="priceForm.amount" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>订单描述</span><NInput v-model:value="priceForm.description" placeholder="会同步到订单明细与账单" /></label>
        <NButton type="primary" block :loading="busy" @click="savePrice">保存</NButton>
      </div>
    </NModal>

    <NModal v-model:show="showPay" preset="card" title="标记支付" style="width:min(460px,94vw)">
      <div class="stack">
        <p class="muted" style="margin:0">可支付余额（用户钱包）<b>{{ moneyCents(detail?.client_credit_cents) }}</b>，待支付余额 <b>{{ moneyCents(payForm.amount_cents) }}</b>。</p>
        <NCheckbox v-model:checked="payForm.use_credit">优先扣除余额（余额不足时扣可用部分，余下记为线下收款）</NCheckbox>
        <p class="muted" style="margin:0">标记支付后订单直接完成，不会开通任何服务。</p>
        <NButton type="primary" block :loading="busy" @click="doMarkPaid">确认标记支付</NButton>
      </div>
    </NModal>
  </div>
</template>
