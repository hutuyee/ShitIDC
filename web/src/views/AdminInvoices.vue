<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NInput, NInputNumber, NModal, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 发票管理后台（对齐魔方 CBAP 插件 IdcsmartInvoice）。
//
// 申请列表：按状态 / 关键词筛选；详情里审核通过、驳回、上传发票文件、发出
// （纸质票填快递单号并可附快递单照片，电子票需先上传文件）、冲红与删除文件。
// 发票设置：功能开关、预开票 / 跨年开票开关、快递方式（名称 + 价格）。
// 发票项目：名称加上普票 / 专票的票面税率与收税比例（收税比例决定向客户收取
// 的税金，票面税率仅用于发票展示）。
// 用户抬头 / 收件地址：全站查看与批量删除。

const message = useMessage()
const tab = ref('requests')

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

// ---------- 申请列表 ----------
const items = ref<any[]>([])
const total = ref(0)
const page = ref(1)
const keywords = ref('')
const status = ref('')
const busy = ref(false)
const pages = computed(() => Math.max(1, Math.ceil(total.value / 20)))

async function load() {
  busy.value = true
  try {
    const params: any = { page: page.value, limit: 20 }
    if (status.value) params.status = status.value
    if (keywords.value.trim()) params.keywords = keywords.value.trim()
    const res = dataOf<any>(await api.get('/admin/invoice', { params }))
    items.value = res?.list || []
    total.value = res?.count || 0
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取发票申请失败') }
  finally { busy.value = false }
}

const detail = ref<any>(null)
const detailOpen = computed({ get: () => Boolean(detail.value), set: (v: boolean) => { if (!v) detail.value = null } })
const reviewNotes = ref('')
const rejectReason = ref('')
const sendForm = reactive({ parcel_number: '' })
const sendFile = ref<File | null>(null)
const uploadInput = ref<HTMLInputElement | null>(null)
const uploadMode = ref('invoice')

async function openDetail(row: any) {
  detail.value = row
  reviewNotes.value = ''
  rejectReason.value = ''
  sendForm.parcel_number = ''
  sendFile.value = null
  try { detail.value = dataOf(await api.get(`/admin/invoice/${row.id}`)) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取发票申请失败') }
}
async function refreshDetail() {
  const id = detail.value?.id
  if (id) await openDetail({ id })
  await load()
}

async function confirmRequest() {
  if (!detail.value) return
  try {
    await api.post(`/admin/invoice/${detail.value.id}/confirm`, { review_notes: reviewNotes.value })
    message.success('已通过，等待发出')
    await refreshDetail()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}
async function rejectRequest() {
  if (!detail.value) return
  if (!rejectReason.value.trim()) { message.error('请填写驳回原因'); return }
  try {
    await api.post(`/admin/invoice/${detail.value.id}/reject`, { reason: rejectReason.value.trim() })
    message.success('已驳回')
    await refreshDetail()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}
async function sendRequest() {
  const d = detail.value
  if (!d) return
  const paper = d.rec_type === 'paper'
  if (paper && !sendForm.parcel_number.trim()) { message.error('纸质发票请填写快递单号'); return }
  try {
    if (paper && sendFile.value) {
      const fd = new FormData()
      fd.append('parcel_number', sendForm.parcel_number.trim())
      fd.append('file', sendFile.value)
      await api.post(`/admin/invoice/${d.id}/send`, fd)
    } else {
      await api.post(`/admin/invoice/${d.id}/send`, { parcel_number: sendForm.parcel_number.trim() })
    }
    message.success('已发出')
    await refreshDetail()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '发出失败') }
}
async function flushRequest() {
  if (!detail.value) return
  if (!window.confirm('确认冲红该发票？冲红后订单可重新申请，操作不可撤销。')) return
  try {
    await api.post(`/admin/invoice/${detail.value.id}/flush`, {})
    message.success('已冲红')
    await refreshDetail()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '冲红失败') }
}
function pickUpload(mode: string) {
  uploadMode.value = mode
  uploadInput.value?.click()
}
async function onUploadPicked(ev: Event) {
  const input = ev.target as HTMLInputElement
  const f = input.files?.[0]
  input.value = ''
  if (!f || !detail.value) return
  if (uploadMode.value === 'parcel') {
    sendFile.value = f
    message.success(`已选择快递单照片：${f.name}`)
    return
  }
  const fd = new FormData()
  fd.append('file', f)
  try {
    await api.post(`/admin/invoice/${detail.value.id}/upload`, fd)
    message.success('发票文件已上传')
    await refreshDetail()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '上传失败') }
}
async function deleteFile() {
  if (!detail.value) return
  if (!window.confirm('确认删除已上传的发票文件？')) return
  try {
    await api.delete(`/admin/invoice/${detail.value.id}/invoice_filename`)
    message.success('已删除')
    await refreshDetail()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}
function downloadFile(id: string) { window.open(`/api/v1/admin/invoice/${id}/invoice_filename`, '_blank') }
function viewParcelImage(id: string) { window.open(`/api/v1/admin/invoice/${id}/parcel_image`, '_blank') }

// ---------- 发票设置 ----------
const cfg = reactive<any>({ invoice_manage: false, pre_invoice: false, across_year_invoice: false, parcel: [] })

async function loadConfig() {
  try {
    const v = dataOf<any>(await api.get('/admin/invoice_config'))
    Object.assign(cfg, {
      invoice_manage: Boolean(v?.invoice_manage),
      pre_invoice: Boolean(v?.pre_invoice),
      across_year_invoice: Boolean(v?.across_year_invoice),
      parcel: (v?.parcel || []).map((p: any) => ({ id: p.id || '', name: p.name || '', price: Number(p.price_cents || 0) / 100 })),
    })
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取发票设置失败') }
}
function addParcel() { cfg.parcel.push({ id: '', name: '', price: 0 }) }
function removeParcel(index: number) { cfg.parcel.splice(index, 1) }
async function saveConfig() {
  try {
    await api.put('/admin/invoice_config', {
      invoice_manage: cfg.invoice_manage,
      pre_invoice: cfg.pre_invoice,
      across_year_invoice: cfg.across_year_invoice,
      parcel: (cfg.parcel || []).filter((p: any) => String(p.name || '').trim()).map((p: any) => ({ id: p.id || '', name: String(p.name).trim(), price_cents: Math.round(Number(p.price || 0) * 100) })),
    })
    message.success('设置已保存')
    await loadConfig()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存设置失败') }
}

// ---------- 发票项目 ----------
const projects = ref<any[]>([])
const projectModal = ref(false)
const projectEditing = ref('')
const projectForm = reactive<any>({ name: '', normal_tax_rate: 0, normal_tax_price: 0, special_tax_switch: false, special_tax_rate: 0, special_tax_price: 0 })

async function loadProjects() {
  try { projects.value = dataOf<any>(await api.get('/admin/invoice_project'))?.list || [] }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取发票项目失败') }
}
function openProjectCreate() {
  projectEditing.value = ''
  Object.assign(projectForm, { name: '', normal_tax_rate: 0, normal_tax_price: 0, special_tax_switch: false, special_tax_rate: 0, special_tax_price: 0 })
  projectModal.value = true
}
function openProjectEdit(p: any) {
  projectEditing.value = p.id
  Object.assign(projectForm, { name: p.name, normal_tax_rate: p.normal_tax_rate, normal_tax_price: p.normal_tax_price, special_tax_switch: p.special_tax_switch, special_tax_rate: p.special_tax_rate, special_tax_price: p.special_tax_price })
  projectModal.value = true
}
async function saveProject() {
  if (!projectForm.name.trim()) { message.error('请填写发票项目名称'); return }
  const payload = {
    name: projectForm.name.trim(),
    normal_tax_rate: Number(projectForm.normal_tax_rate) || 0,
    normal_tax_price: Number(projectForm.normal_tax_price) || 0,
    special_tax_switch: Boolean(projectForm.special_tax_switch),
    special_tax_rate: Number(projectForm.special_tax_rate) || 0,
    special_tax_price: Number(projectForm.special_tax_price) || 0,
  }
  try {
    if (projectEditing.value) await api.put(`/admin/invoice_project/${projectEditing.value}`, payload)
    else await api.post('/admin/invoice_project', payload)
    message.success('已保存')
    projectModal.value = false
    await loadProjects()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
}
async function removeProject(p: any) {
  if (!window.confirm(`确认删除发票项目「${p.name}」？`)) return
  try { await api.delete(`/admin/invoice_project/${p.id}`); message.success('已删除'); await loadProjects() }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---------- 用户抬头 / 收件地址 ----------
const titles = ref<any[]>([])
const titleKeywords = ref('')
const titleChecked = ref<string[]>([])
async function loadTitles() {
  try {
    const params: any = {}
    if (titleKeywords.value.trim()) params.keywords = titleKeywords.value.trim()
    titles.value = dataOf<any>(await api.get('/admin/invoice_title', { params }))?.list || []
    titleChecked.value = []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取发票抬头失败') }
}
function toggleTitle(id: string, checked: boolean) {
  if (checked) { if (!titleChecked.value.includes(id)) titleChecked.value = [...titleChecked.value, id] }
  else titleChecked.value = titleChecked.value.filter(x => x !== id)
}
async function removeTitles() {
  if (!titleChecked.value.length) { message.error('请先勾选要删除的抬头'); return }
  if (!window.confirm(`确认删除选中的 ${titleChecked.value.length} 条抬头？`)) return
  try {
    const res = dataOf<any>(await api.delete('/admin/invoice_title', { data: { ids: titleChecked.value } }))
    message.success(`已删除 ${res?.deleted ?? 0} 条`)
    await loadTitles()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '批量删除失败') }
}

const addresses = ref<any[]>([])
const addrKeywords = ref('')
const addrChecked = ref<string[]>([])
async function loadAddresses() {
  try {
    const params: any = {}
    if (addrKeywords.value.trim()) params.keywords = addrKeywords.value.trim()
    addresses.value = dataOf<any>(await api.get('/admin/invoice_address', { params }))?.list || []
    addrChecked.value = []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取收件地址失败') }
}
function toggleAddr(id: string, checked: boolean) {
  if (checked) { if (!addrChecked.value.includes(id)) addrChecked.value = [...addrChecked.value, id] }
  else addrChecked.value = addrChecked.value.filter(x => x !== id)
}
async function removeAddresses() {
  if (!addrChecked.value.length) { message.error('请先勾选要删除的地址'); return }
  if (!window.confirm(`确认删除选中的 ${addrChecked.value.length} 条收件地址？`)) return
  try {
    const res = dataOf<any>(await api.delete('/admin/invoice_address', { data: { ids: addrChecked.value } }))
    message.success(`已删除 ${res?.deleted ?? 0} 条`)
    await loadAddresses()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '批量删除失败') }
}

onMounted(async () => {
  await Promise.all([load(), loadConfig(), loadProjects(), loadTitles(), loadAddresses()])
})
</script>
<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>发票管理</h1>
        <p>对齐魔方「发票管理」插件：审核 / 驳回用户提交的发票申请，上传电子发票、登记纸质发票快递单与照片，冲红已发出的发票，并维护发票设置、发票项目与用户抬头 / 收件地址。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary :loading="busy" @click="load">刷新</NButton>
      </div>
    </div>

    <NTabs v-model:value="tab" type="line" animated>
      <NTabPane name="requests" tab="发票申请">
        <div class="audit-toolbar" style="grid-template-columns:minmax(120px,180px) minmax(180px,320px) auto">
          <NSelect v-model:value="status" :options="statusOptions" @update:value="() => { page = 1; load() }" />
          <NInput v-model:value="keywords" clearable placeholder="用户邮箱 / 抬头 / 申请号" @keyup.enter="() => { page = 1; load() }" />
          <NButton secondary :loading="busy" @click="() => { page = 1; load() }">查询</NButton>
        </div>
        <section class="panel">
          <div class="panel-title-row"><div><h2>申请列表</h2><span>共 {{ total }} 条</span></div></div>
          <div v-if="items.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head" style="grid-template-columns:90px 150px minmax(140px,1.1fr) 120px 100px 100px 130px 90px minmax(120px,auto)"><span>申请号</span><span>用户</span><span>抬头</span><span>发票项目</span><span>票面金额</span><span>需支付</span><span>收件 / 格式</span><span>状态</span><span>操作</span></div>
            <div v-for="v in items" :key="v.id" class="audit-row" style="grid-template-columns:90px 150px minmax(140px,1.1fr) 120px 100px 100px 130px 90px minmax(120px,auto)">
              <span class="mono">{{ shortID(v.id) }}</span>
              <span>{{ v.user_email }}<small class="muted">{{ shortID(v.user_id) }}</small></span>
              <span>{{ v.title }}<small class="muted">{{ invoiceTypeText(v.invoice_type) }}</small></span>
              <span>{{ v.invoice_project }}</span>
              <span><b>{{ money(v.total_cents) }}</b></span>
              <span>{{ money(v.fee_cents) }}</span>
              <span>{{ recTypeText(v.rec_type) }} / {{ formatText(v.invoice_format) }}</span>
              <span><NTag :type="statusMeta[v.status]?.type || 'default'" size="tiny" round>{{ statusMeta[v.status]?.text || v.status }}</NTag></span>
              <span class="row" style="gap:6px">
                <NButton size="tiny" tertiary @click="openDetail(v)">详情</NButton>
              </span>
            </div>
          </div></div>
          <div v-else class="empty-box">{{ busy ? '加载中…' : '暂无发票申请。' }}</div>
          <div class="row" style="justify-content:flex-end;gap:8px;margin-top:10px">
            <NButton size="small" secondary :disabled="page <= 1" @click="() => { page--; load() }">上一页</NButton>
            <span class="muted" style="align-self:center">{{ page }} / {{ pages }}</span>
            <NButton size="small" secondary :disabled="page >= pages" @click="() => { page++; load() }">下一页</NButton>
          </div>
        </section>
      </NTabPane>

      <NTabPane name="config" tab="发票设置">
        <section class="panel">
          <div class="panel-title-row"><div><h2>功能开关</h2><span>关闭后用户端发票功能整体不可用。</span></div></div>
          <div class="form-grid">
            <label class="full"><span>发票功能</span><NCheckbox v-model:checked="cfg.invoice_manage">开启用户端发票功能</NCheckbox></label>
            <label class="full"><span>预开票</span><NCheckbox v-model:checked="cfg.pre_invoice">允许未支付订单申请发票</NCheckbox></label>
            <label class="full"><span>跨年开票</span><NCheckbox v-model:checked="cfg.across_year_invoice">允许开具往年订单的发票</NCheckbox></label>
          </div>
          <div class="panel-title-row"><div><h2>快递方式</h2><span>纸质发票可选的快递方式与价格（元）。</span></div><NButton secondary @click="addParcel">添加快递方式</NButton></div>
          <div v-if="cfg.parcel.length" class="stack">
            <div v-for="(p, i) in cfg.parcel" :key="i" class="row" style="gap:10px;align-items:center">
              <NInput v-model:value="p.name" placeholder="快递名称，如 顺丰" style="max-width:260px" />
              <NInputNumber v-model:value="p.price" :min="0" :precision="2" style="width:170px">
                <template #prefix>¥</template>
              </NInputNumber>
              <NButton tertiary type="error" size="small" @click="removeParcel(i)">删除</NButton>
            </div>
          </div>
          <div v-else class="empty-box">还没有快递方式；需要邮寄纸质发票时请先添加。</div>
          <NButton type="primary" style="margin-top:14px" @click="saveConfig">保存设置</NButton>
        </section>
      </NTabPane>

      <NTabPane name="projects" tab="发票项目">
        <section class="panel">
          <div class="panel-title-row"><div><h2>发票项目</h2><span>票面税率用于发票展示，收税比例决定向客户收取的税金；专票需单独开启。</span></div><NButton type="primary" @click="openProjectCreate">新增项目</NButton></div>
          <div v-if="projects.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head" style="grid-template-columns:minmax(140px,1.2fr) 100px 120px 90px 100px 120px minmax(110px,auto)"><span>名称</span><span>普票税率</span><span>普票收税比例</span><span>专票</span><span>专票税率</span><span>专票收税比例</span><span>操作</span></div>
            <div v-for="p in projects" :key="p.id" class="audit-row" style="grid-template-columns:minmax(140px,1.2fr) 100px 120px 90px 100px 120px minmax(110px,auto)">
              <span>{{ p.name }}</span>
              <span>{{ p.normal_tax_rate }}%</span>
              <span>{{ p.normal_tax_price }}%</span>
              <span><NTag :type="p.special_tax_switch ? 'success' : 'default'" size="tiny" round>{{ p.special_tax_switch ? '已开启' : '未开启' }}</NTag></span>
              <span>{{ p.special_tax_switch ? p.special_tax_rate + '%' : '—' }}</span>
              <span>{{ p.special_tax_switch ? p.special_tax_price + '%' : '—' }}</span>
              <span class="row" style="gap:6px">
                <NButton size="tiny" tertiary @click="openProjectEdit(p)">编辑</NButton>
                <NButton size="tiny" tertiary type="error" @click="removeProject(p)">删除</NButton>
              </span>
            </div>
          </div></div>
          <div v-else class="empty-box">还没有发票项目，点击右上角「新增项目」创建。</div>
        </section>
      </NTabPane>
      <NTabPane name="titles" tab="用户抬头">
        <section class="panel">
          <div class="audit-toolbar" style="grid-template-columns:minmax(180px,320px) auto auto">
            <NInput v-model:value="titleKeywords" clearable placeholder="抬头 / 用户邮箱 / 税号" @keyup.enter="loadTitles" />
            <NButton secondary @click="loadTitles">查询</NButton>
            <NButton type="error" secondary :disabled="!titleChecked.length" @click="removeTitles">批量删除</NButton>
          </div>
          <div v-if="titles.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head" style="grid-template-columns:34px 150px minmax(150px,1.3fr) 80px 100px minmax(130px,1fr) 150px"><span></span><span>用户</span><span>抬头</span><span>类型</span><span>发票类型</span><span>税务登记号</span><span>创建时间</span></div>
            <div v-for="t in titles" :key="t.id" class="audit-row" style="grid-template-columns:34px 150px minmax(150px,1.3fr) 80px 100px minmax(130px,1fr) 150px">
              <span><NCheckbox :checked="titleChecked.includes(t.id)" @update:checked="v => toggleTitle(t.id, v)" /></span>
              <span>{{ t.user_email }}<small class="muted">{{ shortID(t.user_id) }}</small></span>
              <span>{{ t.title }}</span>
              <span>{{ t.title_type === 'company' ? '公司' : '个人' }}</span>
              <span>{{ t.invoice_type === 'special' ? '专票' : '普票' }}</span>
              <span class="muted">{{ t.tax || '—' }}</span>
              <span class="muted">{{ fmt(t.created_at) }}</span>
            </div>
          </div></div>
          <div v-else class="empty-box">没有匹配的发票抬头。</div>
        </section>
      </NTabPane>

      <NTabPane name="addresses" tab="收件地址">
        <section class="panel">
          <div class="audit-toolbar" style="grid-template-columns:minmax(180px,320px) auto auto">
            <NInput v-model:value="addrKeywords" clearable placeholder="收件人 / 用户邮箱 / 地址" @keyup.enter="loadAddresses" />
            <NButton secondary @click="loadAddresses">查询</NButton>
            <NButton type="error" secondary :disabled="!addrChecked.length" @click="removeAddresses">批量删除</NButton>
          </div>
          <div v-if="addresses.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head" style="grid-template-columns:34px 150px 110px 100px minmax(200px,1.5fr) 120px 80px 150px"><span></span><span>用户</span><span>收件人</span><span>方式</span><span>地址 / 邮箱</span><span>电话</span><span>默认</span><span>创建时间</span></div>
            <div v-for="a in addresses" :key="a.id" class="audit-row" style="grid-template-columns:34px 150px 110px 100px minmax(200px,1.5fr) 120px 80px 150px">
              <span><NCheckbox :checked="addrChecked.includes(a.id)" @update:checked="v => toggleAddr(a.id, v)" /></span>
              <span>{{ a.user_email }}<small class="muted">{{ shortID(a.user_id) }}</small></span>
              <span>{{ a.rec_name }}</span>
              <span>{{ recTypeText(a.rec_type) }}</span>
              <span>{{ a.rec_type === 'paper' ? [a.province, a.city, a.region, a.address].filter(Boolean).join(' ') : (a.email || a.rec_url) }}</span>
              <span class="muted">{{ a.phone || '—' }}</span>
              <span>{{ a.is_default ? '是' : '—' }}</span>
              <span class="muted">{{ fmt(a.created_at) }}</span>
            </div>
          </div></div>
          <div v-else class="empty-box">没有匹配的收件地址。</div>
        </section>
      </NTabPane>
    </NTabs>

    <NModal v-model:show="detailOpen" preset="card" title="发票申请详情" style="width:min(780px,95vw)">
      <div v-if="detail" class="stack">
        <div class="row" style="justify-content:space-between;align-items:center">
          <b class="mono">#{{ shortID(detail.id) }}</b>
          <NTag :type="statusMeta[detail.status]?.type || 'default'" round>{{ statusMeta[detail.status]?.text || detail.status }}</NTag>
        </div>
        <div class="inv-grid">
          <div><span>用户</span><b>{{ detail.user_email }}（{{ shortID(detail.user_id) }}）</b></div>
          <div><span>发票抬头</span><b>{{ detail.title }}</b></div>
          <div><span>发票类型</span><b>{{ invoiceTypeText(detail.invoice_type) }}</b></div>
          <div><span>税务登记号</span><b>{{ detail.tax || '—' }}</b></div>
          <div><span>发票项目</span><b>{{ detail.invoice_project }}</b></div>
          <div><span>发票格式</span><b>{{ formatText(detail.invoice_format) }}</b></div>
          <div><span>票面金额</span><b>{{ money(detail.amount_cents) }}</b></div>
          <div><span>票面税率 / 收税比例</span><b>{{ detail.tax_rate }}% / {{ detail.tax_fee }}%</b></div>
          <div><span>税金</span><b>{{ money(detail.tax_cents) }}</b></div>
          <div v-if="detail.parcel_name"><span>快递（{{ detail.parcel_name }}）</span><b>{{ money(detail.parcel_price_cents) }}</b></div>
          <div><span>票面合计</span><b>{{ money(detail.total_cents) }}</b></div>
          <div><span>需支付</span><b>{{ money(detail.fee_cents) }}</b></div>
          <div><span>支付单</span><b>{{ detail.fee_order_id ? shortID(detail.fee_order_id) + ' · ' + detail.fee_order_status : '—' }}</b></div>
          <div><span>收件方式</span><b>{{ recTypeText(detail.rec_type) }}</b></div>
          <div v-if="detail.rec_type === 'paper'"><span>收件信息</span><b>{{ detail.rec_name }} · {{ detail.rec_phone }} · {{ detail.rec_address }}</b></div>
          <div v-else><span>接收邮箱 / 网址</span><b>{{ detail.rec_email || detail.rec_url || '—' }}</b></div>
          <div v-if="detail.parcel_number"><span>快递单号</span><b>{{ detail.parcel_number }}</b></div>
          <div v-if="detail.review_notes"><span>审核备注</span><b>{{ detail.review_notes }}</b></div>
          <div v-if="detail.reject_reason"><span>驳回原因</span><b>{{ detail.reject_reason }}</b></div>
          <div><span>申请时间</span><b>{{ fmt(detail.created_at) }}</b></div>
          <div v-if="detail.sent_at"><span>发出时间</span><b>{{ fmt(detail.sent_at) }}</b></div>
          <div v-if="detail.flushed_at"><span>冲红时间</span><b>{{ fmt(detail.flushed_at) }}</b></div>
        </div>
        <div v-if="(detail.orders || []).length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head" style="grid-template-columns:110px minmax(200px,1.5fr) 120px"><span>订单号</span><span>商品</span><span>金额</span></div>
          <div v-for="o in detail.orders" :key="o.id" class="audit-row" style="grid-template-columns:110px minmax(200px,1.5fr) 120px">
            <span class="mono">{{ shortID(o.id) }}</span>
            <span>{{ (o.items || []).map((x: any) => x.product_name).join('、') || '—' }}</span>
            <span>{{ money(o.total_cents) }}</span>
          </div>
        </div></div>

        <div class="inv-actions-block">
          <div v-if="detail.status === 'pending'" class="stack">
            <NInput v-model:value="reviewNotes" type="textarea" :rows="2" placeholder="审核备注（选填）" />
            <div class="row" style="gap:8px;justify-content:flex-end">
              <NButton type="primary" @click="confirmRequest">审核通过</NButton>
            </div>
          </div>
          <div v-if="['pending', 'wait_send'].includes(detail.status)" class="stack" style="margin-top:12px">
            <NInput v-model:value="rejectReason" type="textarea" :rows="2" placeholder="驳回原因（驳回必填）" />
            <div class="row" style="gap:8px;justify-content:flex-end">
              <NButton type="error" secondary @click="rejectRequest">驳回申请</NButton>
            </div>
          </div>
          <div v-if="detail.status === 'wait_send'" class="stack" style="margin-top:12px">
            <NInput v-if="detail.rec_type === 'paper'" v-model:value="sendForm.parcel_number" placeholder="快递单号" />
            <div class="row" style="gap:8px;flex-wrap:wrap">
              <NButton v-if="detail.rec_type === 'paper'" secondary @click="pickUpload('parcel')">选择快递单照片</NButton>
              <span v-if="sendFile" class="muted">已选：{{ sendFile.name }}</span>
              <NButton secondary @click="pickUpload('invoice')">上传发票文件</NButton>
              <NButton type="primary" @click="sendRequest">确认发出</NButton>
            </div>
            <p class="muted" style="margin:0">电子发票需先上传发票文件（pdf / ofd / xml / zip），纸质发票需填写快递单号。</p>
          </div>
          <div class="row" style="gap:8px;flex-wrap:wrap;margin-top:12px">
            <NButton v-if="detail.has_file" tertiary @click="downloadFile(detail.id)">下载发票文件</NButton>
            <NButton v-if="detail.has_file" tertiary type="error" @click="deleteFile">删除发票文件</NButton>
            <NButton v-if="detail.parcel_image" tertiary @click="viewParcelImage(detail.id)">查看快递单照片</NButton>
            <NButton v-if="detail.status === 'sent'" tertiary type="warning" @click="flushRequest">冲红发票</NButton>
          </div>
        </div>
      </div>
    </NModal>

    <NModal v-model:show="projectModal" preset="card" :title="projectEditing ? '编辑发票项目' : '新增发票项目'" style="width:min(560px,95vw)">
      <div class="form-grid">
        <label class="full"><span>项目名称</span><NInput v-model:value="projectForm.name" placeholder="如 信息技术服务" /></label>
        <label><span>普票税率（%）</span><NInputNumber v-model:value="projectForm.normal_tax_rate" :min="0" :max="100" :precision="2" style="width:100%" /></label>
        <label><span>普票收税比例（%）</span><NInputNumber v-model:value="projectForm.normal_tax_price" :min="0" :max="100" :precision="2" style="width:100%" /></label>
        <label class="full"><span>增值税专用发票</span><NCheckbox v-model:checked="projectForm.special_tax_switch">开启后该项目的抬头可选择专票</NCheckbox></label>
        <label><span>专票税率（%）</span><NInputNumber v-model:value="projectForm.special_tax_rate" :min="0" :max="100" :precision="2" style="width:100%" /></label>
        <label><span>专票收税比例（%）</span><NInputNumber v-model:value="projectForm.special_tax_price" :min="0" :max="100" :precision="2" style="width:100%" /></label>
      </div>
      <NButton type="primary" block @click="saveProject">保存</NButton>
    </NModal>

    <input ref="uploadInput" type="file" style="display:none" @change="onUploadPicked" />
  </div>
</template>

<style scoped>
.inv-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.inv-grid span { display: block; color: var(--muted, #8a93a6); font-size: 12px; }
.inv-grid b { font-size: 13px; word-break: break-all; }
.inv-actions-block { border-top: 1px dashed var(--border, #e5e8f0); padding-top: 12px; }
@media (max-width: 720px) { .inv-grid { grid-template-columns: 1fr; } }
</style>
