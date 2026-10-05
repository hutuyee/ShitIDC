<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { NButton, NCheckbox, NInput, NInputNumber, NModal, NSelect, NTabPane, NTabs, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 发票管理（对齐魔方 CBAP 插件 IdcsmartInvoice，用户端）。
//
// 申请开票：勾选可开票订单（已有有效申请或跨年不可选的订单置灰），选择抬头、
// 收件地址、发票项目与格式；税金 + 快递费大于 0 时先生成费用单待支付，支付
// 成功后申请进入待审核，否则直接提交待审核。
// 开票记录：查看进度、作废待审核 / 待支付 / 被驳回的申请、下载已发出的发票文件。
// 抬头 / 收件地址：维护申请时使用的开票资料。

const message = useMessage()

const enabled = ref(true)
const config = ref<any>({ invoice_manage: false, pre_invoice: false, across_year_invoice: false, parcel: [] })
const tab = ref('apply')

const statusMeta: Record<string, { text: string; type: any }> = {
  pending: { text: '待审核', type: 'warning' },
  unpaid: { text: '待支付', type: 'warning' },
  wait_send: { text: '待发出', type: 'info' },
  sent: { text: '已发出', type: 'success' },
  reject: { text: '已驳回', type: 'error' },
  cancel: { text: '已作废', type: 'default' },
  flushed: { text: '已冲红', type: 'default' },
}
const statusOptions = [
  { label: '全部状态', value: '' },
  { label: '待审核', value: 'pending' },
  { label: '待支付', value: 'unpaid' },
  { label: '待发出', value: 'wait_send' },
  { label: '已发出', value: 'sent' },
  { label: '已驳回', value: 'reject' },
  { label: '已作废', value: 'cancel' },
  { label: '已冲红', value: 'flushed' },
]
const recTypeText = (t?: string) => (t === 'paper' ? '纸质发票' : '电子发票')
const invoiceTypeText = (t?: string) => (t === 'special' ? '增值税专用发票' : '增值税普通发票')
const formatText = (t?: string) => ({ pdf: 'PDF', ofd: 'OFD', xml: 'XML' } as Record<string, string>)[String(t)] || t || '—'
const money = (cents?: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const shortID = (id?: string) => (id ? String(id).slice(0, 8) : '—')

async function loadConfig() {
  try {
    config.value = dataOf(await api.get('/invoice_config'))
  } catch (e: any) {
    if (e?.response?.status === 403) { enabled.value = false; return }
    message.error(e?.response?.data?.error?.message || '读取发票设置失败')
  }
}
const parcelOptions = computed(() => (config.value.parcel || []).map((p: any) => ({ label: `${p.name}（${money(p.price_cents)}）`, value: p.id })))
const orderFilterOptions = computed(() => {
  const out = [{ label: '已支付订单', value: 'Paid' }]
  if (config.value.pre_invoice) out.push({ label: '未支付订单（预开票）', value: 'Unpaid' })
  return out
})
// ---------- 开票记录 ----------
const requests = ref<any[]>([])
const reqTotal = ref(0)
const reqPage = ref(1)
const reqStatus = ref('')
const reqBusy = ref(false)
const reqPages = computed(() => Math.max(1, Math.ceil(reqTotal.value / 10)))

async function loadRequests() {
  reqBusy.value = true
  try {
    const params: any = { page: reqPage.value, limit: 10 }
    if (reqStatus.value) params.status = reqStatus.value
    const res = dataOf<any>(await api.get('/invoice', { params }))
    requests.value = res?.list || []
    reqTotal.value = res?.count || 0
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取开票记录失败') }
  finally { reqBusy.value = false }
}

const detail = ref<any>(null)
const detailOpen = computed({ get: () => Boolean(detail.value), set: (v: boolean) => { if (!v) detail.value = null } })

async function openDetail(row: any) {
  detail.value = row
  try { detail.value = dataOf(await api.get(`/invoice/${row.id}`)) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取发票详情失败') }
}

async function cancelRequest(row: any) {
  if (!window.confirm('确认作废该发票申请？作废后关联订单可重新申请。')) return
  try {
    await api.delete(`/invoice/${row.id}`)
    message.success('已作废')
    detail.value = null
    await loadRequests()
    await loadOrders()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '作废失败') }
}

function downloadFile(row: any) { window.open(`/api/v1/invoice/${row.id}/invoice_filename`, '_blank') }

async function payFee(row: any) {
  if (!row?.fee_order_id) return
  try {
    await api.post(`/orders/${row.fee_order_id}/pay`, {}, { headers: { 'Idempotency-Key': crypto.randomUUID() } })
    message.success('支付成功，申请已进入待审核')
    await loadRequests()
    await openDetail(row)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '余额支付失败，可前往订单中心选择其他支付方式') }
}

// ---------- 申请开票 ----------
const orderFilter = ref('Paid')
const orders = ref<any[]>([])
const ordersBusy = ref(false)
const selected = ref<string[]>([])

const titles = ref<any[]>([])
const addresses = ref<any[]>([])
const projects = ref<any[]>([])

const form = reactive({ title_id: '', address_id: '', project_id: '', parcel_id: '', invoice_format: 'pdf' })
const quote = ref<any>(null)
const submitting = ref(false)
const titleOptions = computed(() => titles.value.map((t: any) => ({ label: `${t.title}（${t.invoice_type === 'special' ? '专票' : '普票'}）`, value: t.id })))
const addressOptions = computed(() => addresses.value.map((a: any) => ({ label: `${a.rec_name} · ${recTypeText(a.rec_type)}${a.is_default ? '（默认）' : ''}`, value: a.id })))
const projectOptions = computed(() => projects.value.map((p: any) => ({ label: p.name, value: p.id })))
const pickedTitle = computed(() => titles.value.find((t: any) => t.id === form.title_id) || null)
const pickedAddress = computed(() => addresses.value.find((a: any) => a.id === form.address_id) || null)
const isPaper = computed(() => pickedAddress.value?.rec_type === 'paper')

async function loadOrders() {
  ordersBusy.value = true
  try {
    const res = dataOf<any>(await api.get('/invoice_request', { params: { status: orderFilter.value } }))
    orders.value = res?.list || []
    selected.value = []
    quote.value = null
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取可开票订单失败') }
  finally { ordersBusy.value = false }
}

async function loadBase() {
  try {
    const [t, a, p] = await Promise.all([api.get('/invoice_title'), api.get('/invoice_address'), api.get('/invoice_project')])
    titles.value = dataOf<any>(t)?.list || []
    addresses.value = dataOf<any>(a)?.list || []
    projects.value = dataOf<any>(p)?.list || []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取开票资料失败') }
}

const orderDisabled = (o: any) => Boolean(o.invoice_id) || (o.is_cross_year && !config.value.across_year_invoice)
const orderMark = (o: any): { text: string; type: any } | null => {
  if (o.invoice_id) return { text: `已申请（${statusMeta[o.invoice_status]?.text || o.invoice_status}）`, type: 'default' }
  if (o.is_cross_year && !config.value.across_year_invoice) return { text: '跨年订单不可自动开票', type: 'default' }
  return null
}
const orderMarkText = (o: any) => { const m = orderMark(o); return m ? m.text : '可开票' }
const orderMarkType = (o: any) => { const m = orderMark(o); return m ? m.type : 'success' }
function toggleOrder(id: string, checked: boolean) {
  if (checked) { if (!selected.value.includes(id)) selected.value = [...selected.value, id] }
  else selected.value = selected.value.filter(x => x !== id)
}

async function runQuote() {
  if (!selected.value.length || !form.project_id || !pickedTitle.value) { quote.value = null; return }
  try {
    quote.value = dataOf(await api.post('/invoice/price', {
      order_ids: selected.value,
      project_id: form.project_id,
      invoice_type: pickedTitle.value.invoice_type,
      rec_type: pickedAddress.value?.rec_type || '',
      parcel_id: isPaper.value ? form.parcel_id : '',
    }))
  } catch (e: any) { quote.value = null; message.error(e?.response?.data?.error?.message || '试算失败') }
}
watch([() => selected.value.join(','), () => form.title_id, () => form.address_id, () => form.project_id, () => form.parcel_id], () => { runQuote() })

async function submit() {
  if (!selected.value.length) { message.error('请先勾选要开票的订单'); return }
  if (!form.title_id) { message.error('请选择发票抬头（可先到「发票抬头」页签新增）'); return }
  if (!form.address_id) { message.error('请选择收件地址（可先到「收件地址」页签新增）'); return }
  if (!form.project_id) { message.error('请选择发票项目'); return }
  if (isPaper.value && !form.parcel_id) { message.error('纸质发票请选择快递方式'); return }
  submitting.value = true
  try {
    const v = dataOf<any>(await api.post('/invoice', {
      order_ids: selected.value,
      title_id: form.title_id,
      address_id: form.address_id,
      project_id: form.project_id,
      parcel_id: isPaper.value ? form.parcel_id : '',
      invoice_format: form.invoice_format,
    }))
    if (v?.status === 'unpaid') message.warning('申请已创建，请先支付税金 / 快递费')
    else message.success('申请已提交，等待管理员审核')
    tab.value = 'list'
    await loadRequests()
    await loadOrders()
    if (v?.id) openDetail(v)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '提交申请失败') }
  finally { submitting.value = false }
}
// ---------- 发票抬头 ----------
const titleModal = ref(false)
const titleEditing = ref('')
const titleForm = reactive<any>({ title_type: 'company', title: '', invoice_type: 'normal', company_address: '', tax: '', bank: '', bank_user: '' })

function openTitleCreate() {
  titleEditing.value = ''
  Object.assign(titleForm, { title_type: 'company', title: '', invoice_type: 'normal', company_address: '', tax: '', bank: '', bank_user: '' })
  titleModal.value = true
}
function openTitleEdit(t: any) {
  titleEditing.value = t.id
  Object.assign(titleForm, { title_type: t.title_type, title: t.title, invoice_type: t.invoice_type, company_address: t.company_address, tax: t.tax, bank: t.bank, bank_user: t.bank_user })
  titleModal.value = true
}
async function saveTitle() {
  if (!titleForm.title.trim()) { message.error('请填写发票抬头'); return }
  if (titleForm.invoice_type === 'special' && !titleForm.tax.trim()) { message.error('开具增值税专用发票需要填写税务登记号'); return }
  try {
    if (titleEditing.value) await api.put(`/invoice_title/${titleEditing.value}`, { ...titleForm })
    else await api.post('/invoice_title', { ...titleForm })
    message.success('已保存')
    titleModal.value = false
    await loadBase()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
}
async function removeTitle(t: any) {
  if (!window.confirm(`确认删除抬头「${t.title}」？`)) return
  try {
    await api.delete(`/invoice_title/${t.id}`)
    message.success('已删除')
    if (form.title_id === t.id) form.title_id = ''
    await loadBase()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---------- 收件地址 ----------
const addrModal = ref(false)
const addrEditing = ref('')
const addrForm = reactive<any>({ rec_type: 'email', rec_name: '', province: '', city: '', region: '', address: '', phone: '', is_default: false, email: '', rec_url: '', notes: '' })

function openAddrCreate() {
  addrEditing.value = ''
  Object.assign(addrForm, { rec_type: 'email', rec_name: '', province: '', city: '', region: '', address: '', phone: '', is_default: false, email: '', rec_url: '', notes: '' })
  addrModal.value = true
}
function openAddrEdit(a: any) {
  addrEditing.value = a.id
  Object.assign(addrForm, { rec_type: a.rec_type, rec_name: a.rec_name, province: a.province, city: a.city, region: a.region, address: a.address, phone: a.phone, is_default: a.is_default, email: a.email, rec_url: a.rec_url, notes: a.notes })
  addrModal.value = true
}
async function saveAddr() {
  if (!addrForm.rec_name.trim()) { message.error('请填写收件人'); return }
  if (addrForm.rec_type === 'paper') {
    if (!addrForm.address.trim()) { message.error('请填写详细地址'); return }
    if (!addrForm.phone.trim()) { message.error('请填写联系电话'); return }
  } else if (!addrForm.email.trim() && !addrForm.rec_url.trim()) { message.error('请填写接收发票的邮箱或网址'); return }
  try {
    if (addrEditing.value) await api.put(`/invoice_address/${addrEditing.value}`, { ...addrForm })
    else await api.post('/invoice_address', { ...addrForm })
    message.success('已保存')
    addrModal.value = false
    await loadBase()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
}
async function removeAddr(a: any) {
  if (!window.confirm(`确认删除地址「${a.rec_name}」？`)) return
  try {
    await api.delete(`/invoice_address/${a.id}`)
    message.success('已删除')
    if (form.address_id === a.id) form.address_id = ''
    await loadBase()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

onMounted(async () => {
  await loadConfig()
  if (!enabled.value) return
  await Promise.all([loadRequests(), loadOrders(), loadBase()])
})
</script>
<template>
  <div class="dashboard-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">财务中心</div>
        <h1>发票管理</h1>
        <p>为订单申请增值税普通 / 专用发票：勾选订单、维护抬头与收件地址，税金和快递费需另行支付，支付成功后进入管理员审核，纸质发票邮寄后可查询快递单。</p>
      </div>
      <router-link to="/orders" class="soft-action">去订单中心</router-link>
    </div>

    <div v-if="!enabled" class="empty-box">发票功能暂未开启，如有疑问请联系管理员。</div>

    <NTabs v-else v-model:value="tab" type="line" animated>
      <NTabPane name="apply" tab="申请开票">
        <section class="panel">
          <div class="panel-title-row">
            <div><h2>可开票订单</h2><span>勾选需要合并开票的订单；已有有效申请或跨年订单（未开启跨年开票）不可选择。</span></div>
            <NSelect v-model:value="orderFilter" :options="orderFilterOptions" style="width:230px" @update:value="loadOrders" />
          </div>
          <div v-if="orders.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head" style="grid-template-columns:36px 100px minmax(220px,1.6fr) 120px 170px 170px"><span></span><span>订单号</span><span>商品</span><span>金额</span><span>下单时间</span><span>状态</span></div>
            <div v-for="o in orders" :key="o.id" class="audit-row" style="grid-template-columns:36px 100px minmax(220px,1.6fr) 120px 170px 170px">
              <span><NCheckbox :checked="selected.includes(o.id)" :disabled="orderDisabled(o)" @update:checked="v => toggleOrder(o.id, v)" /></span>
              <span class="mono">{{ shortID(o.id) }}</span>
              <span>{{ (o.items || []).map((x: any) => x.product_name).join('、') || '—' }}</span>
              <span><b>{{ money(o.total_cents) }}</b></span>
              <span class="muted">{{ fmt(o.created_at) }}</span>
              <span><NTag :type="orderMarkType(o)" size="tiny" round>{{ orderMarkText(o) }}</NTag></span>
            </div>
          </div></div>
          <div v-else class="empty-box">{{ ordersBusy ? '加载中…' : '暂无可开票订单。' }}</div>
        </section>

        <section class="panel" style="margin-top:14px">
          <div class="panel-title-row">
            <div><h2>开票资料</h2><span>抬头与收件地址可在对应页签维护；专票需要项目开启专票且抬头填写税务登记号。</span></div>
          </div>
          <div class="form-grid">
            <label><span>发票抬头</span><NSelect v-model:value="form.title_id" :options="titleOptions" placeholder="选择发票抬头" /></label>
            <label><span>收件地址</span><NSelect v-model:value="form.address_id" :options="addressOptions" placeholder="选择收件地址" /></label>
            <label><span>发票项目</span><NSelect v-model:value="form.project_id" :options="projectOptions" placeholder="选择发票项目" /></label>
            <label v-if="isPaper"><span>快递方式</span><NSelect v-model:value="form.parcel_id" :options="parcelOptions" placeholder="选择快递方式" /></label>
            <label><span>发票格式</span><NSelect v-model:value="form.invoice_format" :options="[{ label: 'PDF', value: 'pdf' }, { label: 'OFD', value: 'ofd' }, { label: 'XML', value: 'xml' }]" /></label>
            <div v-if="pickedTitle" style="align-self:end;padding-bottom:8px">发票类型：<b>{{ invoiceTypeText(pickedTitle.invoice_type) }}</b></div>
          </div>

          <div v-if="quote" class="inv-quote">
            <div class="table-scroll"><div class="audit-table">
              <div class="audit-row audit-head" style="grid-template-columns:minmax(220px,1.6fr) 120px 120px"><span>订单 / 商品</span><span>订单号</span><span>金额</span></div>
              <div v-for="h in quote.host || []" :key="h.order_id" class="audit-row" style="grid-template-columns:minmax(220px,1.6fr) 120px 120px">
                <span>{{ h.product_name }}</span>
                <span class="mono">{{ shortID(h.order_id) }}</span>
                <span>{{ money(h.amount_cents) }}</span>
              </div>
            </div></div>
            <div class="inv-summary">
              <div><span>票面金额</span><b>{{ money(quote.price) }}</b></div>
              <div><span>票面税率</span><b>{{ quote.tax_rate }}%</b></div>
              <div><span>收税比例</span><b>{{ quote.tax_fee }}%</b></div>
              <div><span>税金</span><b>{{ money(quote.tax_price) }}</b></div>
              <div v-if="quote.parcel_name"><span>快递（{{ quote.parcel_name }}）</span><b>{{ money(quote.parcel_price) }}</b></div>
              <div><span>票面合计</span><b>{{ money(quote.total) }}</b></div>
              <div class="inv-need"><span>需支付</span><b>{{ money(quote.fee) }}</b></div>
            </div>
            <p class="muted" style="margin:10px 0 0">{{ quote.fee > 0 ? '提交后先生成待支付费用单，支付税金 / 快递费后进入管理员审核。' : '无需额外支付费用，提交后直接进入管理员审核。' }}</p>
            <NButton type="primary" block style="margin-top:12px" :loading="submitting" @click="submit">提交开票申请</NButton>
          </div>
          <div v-else class="empty-box" style="margin-top:6px">勾选订单并选择发票项目后自动试算税金与快递费。</div>
        </section>
      </NTabPane>

      <NTabPane name="list" tab="开票记录">
        <section class="panel">
          <div class="audit-toolbar" style="grid-template-columns:minmax(150px,220px) auto">
            <NSelect v-model:value="reqStatus" :options="statusOptions" @update:value="() => { reqPage = 1; loadRequests() }" />
            <NButton secondary :loading="reqBusy" @click="loadRequests">刷新</NButton>
          </div>
          <div v-if="requests.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head" style="grid-template-columns:100px minmax(140px,1fr) 110px 110px 130px 100px 160px minmax(150px,auto)"><span>申请号</span><span>发票项目</span><span>票面金额</span><span>需支付</span><span>收件 / 格式</span><span>状态</span><span>申请时间</span><span>操作</span></div>
            <div v-for="v in requests" :key="v.id" class="audit-row" style="grid-template-columns:100px minmax(140px,1fr) 110px 110px 130px 100px 160px minmax(150px,auto)">
              <span class="mono">{{ shortID(v.id) }}</span>
              <span>{{ v.invoice_project }}</span>
              <span><b>{{ money(v.total_cents) }}</b></span>
              <span>{{ money(v.fee_cents) }}</span>
              <span>{{ recTypeText(v.rec_type) }} / {{ formatText(v.invoice_format) }}</span>
              <span><NTag :type="statusMeta[v.status]?.type || 'default'" size="tiny" round>{{ statusMeta[v.status]?.text || v.status }}</NTag></span>
              <span class="muted">{{ fmt(v.created_at) }}</span>
              <span class="row" style="gap:6px;flex-wrap:wrap">
                <NButton size="tiny" tertiary @click="openDetail(v)">详情</NButton>
                <NButton v-if="v.status === 'unpaid' && v.fee_order_id" size="tiny" tertiary type="primary" @click="payFee(v)">余额支付</NButton>
                <NButton v-if="v.has_file" size="tiny" tertiary @click="downloadFile(v)">下载发票</NButton>
                <NButton v-if="['pending', 'unpaid', 'reject'].includes(v.status)" size="tiny" tertiary type="error" @click="cancelRequest(v)">作废</NButton>
              </span>
            </div>
          </div></div>
          <div v-else class="empty-box">{{ reqBusy ? '加载中…' : '还没有开票记录。' }}</div>
          <div class="row" style="justify-content:flex-end;gap:8px;margin-top:10px">
            <NButton size="small" secondary :disabled="reqPage <= 1" @click="() => { reqPage--; loadRequests() }">上一页</NButton>
            <span class="muted" style="align-self:center">{{ reqPage }} / {{ reqPages }}</span>
            <NButton size="small" secondary :disabled="reqPage >= reqPages" @click="() => { reqPage++; loadRequests() }">下一页</NButton>
          </div>
        </section>
      </NTabPane>
      <NTabPane name="title" tab="发票抬头">
        <section class="panel">
          <div class="panel-title-row">
            <div><h2>发票抬头</h2><span>公司抬头建议填写税务登记号；专票必须填写税号。</span></div>
            <NButton type="primary" @click="openTitleCreate">新增抬头</NButton>
          </div>
          <div v-if="titles.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head" style="grid-template-columns:minmax(160px,1.2fr) 80px 100px minmax(140px,1fr) minmax(140px,1fr) minmax(120px,.8fr)"><span>抬头</span><span>类型</span><span>发票类型</span><span>税务登记号</span><span>开户银行</span><span>操作</span></div>
            <div v-for="t in titles" :key="t.id" class="audit-row" style="grid-template-columns:minmax(160px,1.2fr) 80px 100px minmax(140px,1fr) minmax(140px,1fr) minmax(120px,.8fr)">
              <span>{{ t.title }}</span>
              <span>{{ t.title_type === 'company' ? '公司' : '个人' }}</span>
              <span>{{ t.invoice_type === 'special' ? '专票' : '普票' }}</span>
              <span class="muted">{{ t.tax || '—' }}</span>
              <span class="muted">{{ t.bank }}{{ t.bank_user ? ' / ' + t.bank_user : '' }}</span>
              <span class="row" style="gap:6px">
                <NButton size="tiny" tertiary @click="openTitleEdit(t)">编辑</NButton>
                <NButton size="tiny" tertiary type="error" @click="removeTitle(t)">删除</NButton>
              </span>
            </div>
          </div></div>
          <div v-else class="empty-box">还没有发票抬头，点击右上角「新增抬头」添加。</div>
        </section>
      </NTabPane>

      <NTabPane name="address" tab="收件地址">
        <section class="panel">
          <div class="panel-title-row">
            <div><h2>收件地址</h2><span>纸质发票需要省市区与详细地址；电子发票填写接收邮箱或网址。</span></div>
            <NButton type="primary" @click="openAddrCreate">新增地址</NButton>
          </div>
          <div v-if="addresses.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head" style="grid-template-columns:120px 110px 70px minmax(200px,1.5fr) 130px minmax(120px,.8fr)"><span>收件人</span><span>收件方式</span><span>默认</span><span>地址 / 邮箱</span><span>电话</span><span>操作</span></div>
            <div v-for="a in addresses" :key="a.id" class="audit-row" style="grid-template-columns:120px 110px 70px minmax(200px,1.5fr) 130px minmax(120px,.8fr)">
              <span>{{ a.rec_name }}</span>
              <span>{{ recTypeText(a.rec_type) }}</span>
              <span><NTag v-if="a.is_default" size="tiny" type="success" round>默认</NTag><span v-else class="muted">—</span></span>
              <span>{{ a.rec_type === 'paper' ? [a.province, a.city, a.region, a.address].filter(Boolean).join(' ') : (a.email || a.rec_url) }}</span>
              <span class="muted">{{ a.phone || '—' }}</span>
              <span class="row" style="gap:6px">
                <NButton size="tiny" tertiary @click="openAddrEdit(a)">编辑</NButton>
                <NButton size="tiny" tertiary type="error" @click="removeAddr(a)">删除</NButton>
              </span>
            </div>
          </div></div>
          <div v-else class="empty-box">还没有收件地址，点击右上角「新增地址」添加。</div>
        </section>
      </NTabPane>
    </NTabs>

    <NModal v-model:show="detailOpen" preset="card" title="发票申请详情" style="width:min(720px,95vw)">
      <div v-if="detail" class="stack">
        <div class="row" style="justify-content:space-between;align-items:center">
          <b class="mono">#{{ shortID(detail.id) }}</b>
          <NTag :type="statusMeta[detail.status]?.type || 'default'" round>{{ statusMeta[detail.status]?.text || detail.status }}</NTag>
        </div>
        <div class="inv-grid">
          <div><span>发票抬头</span><b>{{ detail.title }}</b></div>
          <div><span>发票类型</span><b>{{ invoiceTypeText(detail.invoice_type) }}</b></div>
          <div><span>发票项目</span><b>{{ detail.invoice_project }}</b></div>
          <div><span>发票格式</span><b>{{ formatText(detail.invoice_format) }}</b></div>
          <div><span>票面金额</span><b>{{ money(detail.amount_cents) }}</b></div>
          <div><span>税金</span><b>{{ money(detail.tax_cents) }}</b></div>
          <div v-if="detail.parcel_name"><span>快递（{{ detail.parcel_name }}）</span><b>{{ money(detail.parcel_price_cents) }}</b></div>
          <div><span>票面合计</span><b>{{ money(detail.total_cents) }}</b></div>
          <div><span>需支付</span><b>{{ money(detail.fee_cents) }}</b></div>
          <div><span>收件方式</span><b>{{ recTypeText(detail.rec_type) }}</b></div>
          <div v-if="detail.rec_type === 'paper'"><span>收件信息</span><b>{{ detail.rec_name }} {{ detail.rec_phone }}</b></div>
          <div v-else><span>接收邮箱</span><b>{{ detail.rec_email || detail.rec_url || '—' }}</b></div>
          <div v-if="detail.parcel_number"><span>快递单号</span><b>{{ detail.parcel_number }}</b></div>
          <div v-if="detail.review_notes"><span>审核备注</span><b>{{ detail.review_notes }}</b></div>
          <div v-if="detail.reject_reason"><span>驳回原因</span><b>{{ detail.reject_reason }}</b></div>
          <div><span>申请时间</span><b>{{ fmt(detail.created_at) }}</b></div>
          <div v-if="detail.sent_at"><span>发出时间</span><b>{{ fmt(detail.sent_at) }}</b></div>
          <div v-if="detail.flushed_at"><span>冲红时间</span><b>{{ fmt(detail.flushed_at) }}</b></div>
        </div>
        <div v-if="(detail.orders || []).length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head" style="grid-template-columns:110px minmax(180px,1.4fr) 120px"><span>订单号</span><span>商品</span><span>金额</span></div>
          <div v-for="o in detail.orders" :key="o.id" class="audit-row" style="grid-template-columns:110px minmax(180px,1.4fr) 120px">
            <span class="mono">{{ shortID(o.id) }}</span>
            <span>{{ (o.items || []).map((x: any) => x.product_name).join('、') || '—' }}</span>
            <span>{{ money(o.total_cents) }}</span>
          </div>
        </div></div>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton v-if="detail.status === 'unpaid' && detail.fee_order_id" type="primary" @click="payFee(detail)">余额支付</NButton>
          <router-link v-if="detail.status === 'unpaid'" to="/orders"><NButton tertiary>订单中心支付</NButton></router-link>
          <NButton v-if="detail.has_file" tertiary @click="downloadFile(detail)">下载发票文件</NButton>
          <NButton v-if="['pending', 'unpaid', 'reject'].includes(detail.status)" tertiary type="error" @click="cancelRequest(detail)">作废申请</NButton>
        </div>
      </div>
    </NModal>

    <NModal v-model:show="titleModal" preset="card" :title="titleEditing ? '编辑发票抬头' : '新增发票抬头'" style="width:min(560px,95vw)">
      <div class="form-grid">
        <label><span>抬头类型</span><NSelect v-model:value="titleForm.title_type" :options="[{ label: '公司', value: 'company' }, { label: '个人', value: 'person' }]" /></label>
        <label><span>发票类型</span><NSelect v-model:value="titleForm.invoice_type" :options="[{ label: '增值税普通发票', value: 'normal' }, { label: '增值税专用发票', value: 'special' }]" /></label>
        <label class="full"><span>发票抬头</span><NInput v-model:value="titleForm.title" placeholder="公司名称或个人姓名" /></label>
        <label class="full"><span>公司地址</span><NInput v-model:value="titleForm.company_address" placeholder="选填" /></label>
        <label><span>税务登记号</span><NInput v-model:value="titleForm.tax" placeholder="专票必填" /></label>
        <label><span>开户银行</span><NInput v-model:value="titleForm.bank" placeholder="选填" /></label>
        <label><span>开户账号</span><NInput v-model:value="titleForm.bank_user" placeholder="选填" /></label>
      </div>
      <NButton type="primary" block @click="saveTitle">保存</NButton>
    </NModal>

    <NModal v-model:show="addrModal" preset="card" :title="addrEditing ? '编辑收件地址' : '新增收件地址'" style="width:min(560px,95vw)">
      <div class="form-grid">
        <label><span>收件方式</span><NSelect v-model:value="addrForm.rec_type" :options="[{ label: '电子发票（邮箱 / 网址）', value: 'email' }, { label: '纸质发票（快递）', value: 'paper' }]" /></label>
        <label><span>收件人</span><NInput v-model:value="addrForm.rec_name" /></label>
        <template v-if="addrForm.rec_type === 'paper'">
          <label><span>省份</span><NInput v-model:value="addrForm.province" /></label>
          <label><span>城市</span><NInput v-model:value="addrForm.city" /></label>
          <label><span>区 / 县</span><NInput v-model:value="addrForm.region" /></label>
          <label class="full"><span>详细地址</span><NInput v-model:value="addrForm.address" /></label>
          <label><span>联系电话</span><NInput v-model:value="addrForm.phone" /></label>
        </template>
        <template v-else>
          <label><span>接收邮箱</span><NInput v-model:value="addrForm.email" placeholder="与网址至少填一项" /></label>
          <label><span>接收网址</span><NInput v-model:value="addrForm.rec_url" placeholder="选填" /></label>
        </template>
        <label class="full"><span>备注</span><NInput v-model:value="addrForm.notes" placeholder="选填" /></label>
        <label class="full"><span>默认地址</span><NCheckbox v-model:checked="addrForm.is_default">设为默认收件地址</NCheckbox></label>
      </div>
      <NButton type="primary" block @click="saveAddr">保存</NButton>
    </NModal>
  </div>
</template>

<style scoped>
.inv-quote { border-top: 1px dashed var(--border, #e5e8f0); padding-top: 12px; }
.inv-summary { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; margin-top: 12px; }
.inv-summary > div { display: flex; justify-content: space-between; gap: 8px; padding: 8px 10px; border: 1px solid var(--border, #e5e8f0); border-radius: 10px; font-size: 13px; }
.inv-summary .inv-need { border-color: color-mix(in srgb, var(--primary, #4f46e5) 45%, var(--border, #e5e8f0)); }
.inv-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.inv-grid span { display: block; color: var(--muted, #8a93a6); font-size: 12px; }
.inv-grid b { font-size: 13px; word-break: break-all; }
@media (max-width: 720px) { .inv-summary { grid-template-columns: 1fr 1fr; } .inv-grid { grid-template-columns: 1fr; } }
</style>
