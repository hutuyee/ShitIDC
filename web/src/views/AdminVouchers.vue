<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NModal, NRadio, NRadioGroup, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 代金券（对齐魔方 CBAP 插件 IdcsmartVoucher）。
//
// 与「优惠券」的分工：优惠券是公开的券码折扣；代金券是发放 / 领取式的定额抵扣券，
// 公开券前台可领取、私有券只由后台发放，下单 / 续费 / 升降级时按订单金额核销。
// 券码规则与插件一致：8 位且同时包含大写字母、小写字母与数字。

const message = useMessage()
const items = ref<any[]>([])
const products = ref<any[]>([])
const busy = ref(false)
const filter = reactive({ code: '', status: '' })

const statusOptions = [
  { label: '全部状态', value: '' },
  { label: '已启用', value: 'enable' },
  { label: '已停用', value: 'disable' },
  { label: '未生效', value: 'wait_effect' },
  { label: '已过期', value: 'expired' },
]
const statusMeta: Record<string, { text: string; type: any }> = {
  enable: { text: '启用', type: 'success' },
  disable: { text: '停用', type: 'default' },
  wait_effect: { text: '未生效', type: 'warning' },
  expired: { text: '已过期', type: 'error' },
}
const typeOptions = [
  { label: '私有（仅后台发放）', value: 'private' },
  { label: '公开（前台可领取）', value: 'public' },
]
const userTypeOptions = [
  { label: '不限制', value: 'no_limit' },
  { label: '仅限无产品用户', value: 'no_host' },
  { label: '需存在激活中产品', value: 'need_active' },
]
const cycleOptions = [
  { label: '月付', value: 'monthly' },
  { label: '季付', value: 'quarterly' },
  { label: '半年付', value: 'semiannually' },
  { label: '年付', value: 'yearly' },
  { label: '两年付', value: 'biennially' },
  { label: '三年付', value: 'triennially' },
]

const productOptions = computed(() => products.value.map(p => ({ label: p.name, value: p.id })))
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string | null) => (v ? new Date(v).toLocaleString() : '—')
const userTypeText = (v: string) => userTypeOptions.find(x => x.value === v)?.label || v
const cycleText = (list: string[]) => (Array.isArray(list) && list.length ? list.map(c => cycleOptions.find(x => x.value === c)?.label || c).join('、') : '不限')

const emptyForm = () => ({
  code: '', price: 0, type: 'private', num: 0,
  start_at: null as string | null, end_at: null as string | null,
  product: [] as string[], product_need: [] as string[],
  min_amount: 0, user_type: 'no_limit',
  onetime: false, upgrade_use: false, renew_use: false,
  cycle: [] as string[], notes: '',
})
const form = reactive<any>(emptyForm())
const editing = ref('')

function toLocalInput(v?: string | null) {
  if (!v) return null
  const d = new Date(v)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

async function load() {
  busy.value = true
  try {
    const params: any = {}
    if (filter.code.trim()) params.code = filter.code.trim()
    if (filter.status) params.status = filter.status
    items.value = dataOf<any[]>(await api.get('/admin/vouchers', { params })) || []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取代金券失败') }
  finally { busy.value = false }
}

async function loadProducts() {
  try { products.value = dataOf<any[]>(await api.get('/admin/products')) || [] }
  catch { products.value = [] }
}

function resetForm() {
  editing.value = ''
  Object.assign(form, emptyForm())
}

function edit(row: any) {
  editing.value = row.id
  Object.assign(form, {
    code: row.code, price: row.price_cents / 100, type: row.type, num: row.num,
    start_at: toLocalInput(row.start_at), end_at: toLocalInput(row.end_at),
    product: [...(row.product || [])], product_need: [...(row.product_need || [])],
    min_amount: row.min_amount_cents / 100, user_type: row.user_type,
    onetime: !!row.onetime, upgrade_use: !!row.upgrade_use, renew_use: !!row.renew_use,
    cycle: [...(row.cycle || [])], notes: row.notes || '',
  })
}

async function save() {
  const code = form.code.trim()
  if (!editing.value) {
    if (!/^(?=.*[a-z])(?=.*[A-Z])(?=.*\d)[A-Za-z\d]{8}$/.test(code)) {
      message.error('券码须为 8 位且同时包含大写字母、小写字母与数字')
      return
    }
    try {
      const res = dataOf<any>(await api.get('/admin/vouchers/check', { params: { code } }))
      if (res?.exists) { message.error('该券码已存在'); return }
    } catch { /* 校验失败不阻塞保存，后端仍会兜底 */ }
  }
  const payload = {
    code,
    price_cents: Math.round((form.price || 0) * 100),
    type: form.type,
    num: Math.max(0, Math.round(form.num || 0)),
    start_at: form.start_at ? new Date(form.start_at).toISOString() : null,
    end_at: form.end_at ? new Date(form.end_at).toISOString() : null,
    product: form.product,
    product_need: form.product_need,
    min_amount_cents: Math.round((form.min_amount || 0) * 100),
    user_type: form.user_type,
    onetime: !!form.onetime,
    upgrade_use: !!form.upgrade_use,
    renew_use: !!form.renew_use,
    cycle: form.cycle,
    notes: form.notes.trim(),
  }
  busy.value = true
  try {
    if (editing.value) await api.put(`/admin/vouchers/${editing.value}`, payload)
    else await api.post('/admin/vouchers', payload)
    message.success(editing.value ? '代金券已更新' : '代金券已创建')
    resetForm()
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}

async function toggle(row: any) {
  try {
    await api.post(`/admin/vouchers/${row.id}/${row.enabled ? 'disable' : 'enable'}`)
    message.success(row.enabled ? '已停用' : '已启用')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}

async function remove(row: any) {
  if (!window.confirm(`确认删除代金券 ${row.code}？领取与使用记录会一并删除。`)) return
  try {
    await api.delete(`/admin/vouchers/${row.id}`)
    message.success('已删除')
    if (editing.value === row.id) resetForm()
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---------- 发放 ----------
const showSend = ref(false)
const sendTarget = ref<any>(null)
const sendAll = ref(false)
const sendKeyword = ref('')
const sendResults = ref<any[]>([])
const sendPicked = ref<any[]>([])
const sending = ref(false)
const times = ref<any[]>([])

async function openSend(row: any) {
  sendTarget.value = row
  sendAll.value = false
  sendKeyword.value = ''
  sendResults.value = []
  sendPicked.value = []
  times.value = []
  showSend.value = true
  try { times.value = dataOf<any[]>(await api.post(`/admin/vouchers/${row.id}/times`)) || [] }
  catch { times.value = [] }
}

async function searchUsers() {
  const q = sendKeyword.value.trim()
  if (!q) { sendResults.value = []; return }
  try {
    sendResults.value = dataOf<any[]>(await api.get('/admin/search', { params: { q, kind: 'user', limit: 20 } })) || []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '搜索失败') }
}

function addTarget(hit: any) {
  if (sendPicked.value.some(u => u.id === hit.id)) return
  sendPicked.value.push(hit)
}

async function doSend() {
  if (!sendTarget.value) return
  if (!sendAll.value && !sendPicked.value.length) { message.error('请选择发放对象'); return }
  sending.value = true
  try {
    const res = dataOf<any>(await api.post(`/admin/vouchers/${sendTarget.value.id}/send`, {
      client_id: sendAll.value ? 'all' : sendPicked.value.map(u => u.id),
    }))
    message.success(`已发放 ${res?.granted || 0} 张${res?.skipped ? `，${res.skipped} 个对象因数量限制被跳过` : ''}`)
    showSend.value = false
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '发放失败') }
  finally { sending.value = false }
}

// ---------- 领取 / 使用记录 ----------
const showRecords = ref(false)
const recordVoucher = ref<any>(null)
const records = ref<any[]>([])
const recordTotal = ref(0)
const recordPage = ref(1)
const recordUse = ref('')
const recordKeyword = ref('')
const recordBusy = ref(false)

const useOptions = [
  { label: '全部', value: '' },
  { label: '未使用', value: '0' },
  { label: '已使用', value: '1' },
]

async function loadRecords() {
  if (!recordVoucher.value) return
  recordBusy.value = true
  try {
    const res = dataOf<any>(await api.get('/admin/vouchers/record', {
      params: {
        voucher_id: recordVoucher.value.id, keywords: recordKeyword.value.trim(),
        use: recordUse.value, page: recordPage.value, limit: 20,
      },
    }))
    records.value = res?.list || []
    recordTotal.value = res?.total || 0
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取记录失败') }
  finally { recordBusy.value = false }
}

function openRecords(row: any) {
  recordVoucher.value = row
  records.value = []
  recordTotal.value = 0
  recordPage.value = 1
  recordUse.value = ''
  recordKeyword.value = ''
  showRecords.value = true
  loadRecords()
}

async function removeRecord(r: any) {
  if (!window.confirm('确认删除这条记录？已用于订单的记录删除后不可恢复。')) return
  try {
    await api.delete(`/admin/vouchers/record/${r.id}`)
    message.success('已删除')
    await loadRecords()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

const recordPages = computed(() => Math.max(1, Math.ceil(recordTotal.value / 20)))

onMounted(() => { load(); loadProducts() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>代金券</h1>
        <p>对齐魔方「代金券」插件：后台创建并按用户发放，公开券可在用户中心领取；下单 / 续费 / 升降级时按订单金额核销（最多抵到 0，不找零），订单取消不返还。</p>
      </div>
    </div>

    <div class="audit-toolbar" style="grid-template-columns:minmax(160px,240px) minmax(140px,180px) auto">
      <NInput v-model:value="filter.code" clearable placeholder="按券码搜索" @keyup.enter="load" />
      <NSelect v-model:value="filter.status" :options="statusOptions" />
      <NButton secondary :loading="busy" @click="load">刷新</NButton>
    </div>

    <div class="admin-two-col">
      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>{{ editing ? '编辑代金券' : '创建代金券' }}</h2><span>券码 / 类型 / 面额创建后不可改（与插件一致）</span></div></div>
        <div class="form-grid">
          <label><span>券码</span><NInput v-model:value="form.code" :disabled="!!editing" placeholder="8 位含大小写与数字" maxlength="8" /></label>
          <label><span>类型</span><NSelect v-model:value="form.type" :disabled="!!editing" :options="typeOptions" /></label>
          <label><span>面额（元）</span><NInputNumber v-model:value="form.price" :min="0" :precision="2" :disabled="!!editing" style="width:100%" /></label>
          <label><span>可发放数量（0=不限）</span><NInputNumber v-model:value="form.num" :min="0" style="width:100%" /></label>
          <label><span>生效时间（留空=立即）</span><input type="datetime-local" class="native-input" v-model="form.start_at" /></label>
          <label><span>截止时间（留空=长期）</span><input type="datetime-local" class="native-input" v-model="form.end_at" /></label>
          <label><span>最低使用金额（元）</span><NInputNumber v-model:value="form.min_amount" :min="0" :precision="2" style="width:100%" /></label>
          <label><span>用户限制</span><NSelect v-model:value="form.user_type" :options="userTypeOptions" /></label>
          <label class="full"><span>适用商品（不选＝不限）</span>
            <NSelect v-model:value="form.product" multiple filterable clearable :options="productOptions" placeholder="留空表示所有商品都能用" />
          </label>
          <label class="full"><span>需先拥有的激活商品（不选＝不限）</span>
            <NSelect v-model:value="form.product_need" multiple filterable clearable :options="productOptions" placeholder="用户需拥有这些商品中的任一激活服务" />
          </label>
          <label class="full"><span>适用计费周期（不选＝不限）</span>
            <NSelect v-model:value="form.cycle" multiple filterable clearable :options="cycleOptions" placeholder="留空表示所有周期都能用" />
          </label>
          <label class="full"><span>备注</span><NInput v-model:value="form.notes" type="textarea" :rows="2" placeholder="仅后台可见" /></label>
        </div>
        <div class="form-grid" style="grid-template-columns:repeat(3,minmax(0,1fr))">
          <label class="row" style="gap:8px"><NSwitch v-model:value="form.onetime" size="small" /><span>每人限用一次</span></label>
          <label class="row" style="gap:8px"><NSwitch v-model:value="form.upgrade_use" size="small" /><span>可用于升降级</span></label>
          <label class="row" style="gap:8px"><NSwitch v-model:value="form.renew_use" size="small" /><span>可用于续费</span></label>
        </div>
        <div class="row" style="gap:8px">
          <NButton type="primary" :loading="busy" @click="save">{{ editing ? '保存修改' : '创建' }}</NButton>
          <NButton v-if="editing" secondary @click="resetForm">取消编辑</NButton>
        </div>
      </section>

      <section class="panel">
        <div class="panel-title-row"><div><h2>代金券</h2><span>{{ items.length }} 张</span></div></div>
        <div v-if="items.length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head"><span>券码</span><span>面额</span><span>类型</span><span>领取 / 使用</span><span>有效期</span><span>操作</span></div>
          <div v-for="v in items" :key="v.id" class="audit-row">
            <span>
              <b>{{ v.code }}</b>
              <small><NTag :type="statusMeta[v.status]?.type || 'default'" size="tiny" round>{{ statusMeta[v.status]?.text || v.status }}</NTag></small>
            </span>
            <span>{{ money(v.price_cents) }}<small>满 {{ money(v.min_amount_cents) }} 可用</small></span>
            <span>{{ v.type === 'public' ? '公开' : '私有' }}<small>{{ userTypeText(v.user_type) }}</small></span>
            <span>{{ v.claim_count }} / {{ v.used_count }}<small>{{ v.num > 0 ? `总量 ${v.num}` : '总量不限' }}</small></span>
            <span class="muted">{{ fmt(v.start_at) }}<small>至 {{ fmt(v.end_at) }}</small></span>
            <span class="row" style="gap:6px;flex-wrap:wrap">
              <NButton size="tiny" tertiary @click="edit(v)">编辑</NButton>
              <NButton size="tiny" tertiary @click="toggle(v)">{{ v.enabled ? '停用' : '启用' }}</NButton>
              <NButton size="tiny" tertiary @click="openSend(v)">发放</NButton>
              <NButton size="tiny" tertiary @click="openRecords(v)">记录</NButton>
              <NButton size="tiny" tertiary type="error" @click="remove(v)">删除</NButton>
            </span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有代金券。</div>
        <p class="muted" style="margin:10px 0 0">
          额外限制：{{ '适用商品 / 需拥有商品 / 计费周期' }} 均在核销时按券面配置校验；「每人限用一次」按已使用记录判断。
        </p>
      </section>
    </div>

    <NModal v-model:show="showSend" preset="card" :title="`发放代金券 ${sendTarget?.code || ''}`" style="width:min(620px,94vw)">
      <NRadioGroup v-model:value="sendAll" style="margin-bottom:12px">
        <NRadio :value="false">指定用户</NRadio>
        <NRadio :value="true">全部用户</NRadio>
      </NRadioGroup>
      <template v-if="!sendAll">
        <div class="row" style="gap:8px">
          <NInput v-model:value="sendKeyword" placeholder="按邮箱 / 昵称 / UID 搜索用户" @keyup.enter="searchUsers" />
          <NButton secondary @click="searchUsers">搜索</NButton>
        </div>
        <div v-if="sendResults.length" class="table-scroll" style="max-height:180px;overflow-y:auto;margin-top:10px">
          <div class="audit-table">
            <div v-for="u in sendResults" :key="u.id" class="audit-row" style="grid-template-columns:minmax(140px,1fr) minmax(120px,.8fr) 90px">
              <span>{{ u.label }}</span>
              <span class="muted">{{ u.sub || '—' }}</span>
              <span><NButton size="tiny" tertiary @click="addTarget(u)">添加</NButton></span>
            </div>
          </div>
        </div>
        <div v-if="sendPicked.length" class="row" style="gap:6px;flex-wrap:wrap;margin-top:10px">
          <NTag v-for="u in sendPicked" :key="u.id" closable @close="sendPicked = sendPicked.filter(x => x.id !== u.id)">{{ u.label }}</NTag>
        </div>
        <p class="muted" style="margin:8px 0 0">未选到用户？可以直接在用户列表复制 UID 后按 UID 搜索。</p>
      </template>
      <div v-if="times.length" style="margin-top:14px">
        <p class="muted" style="margin:0 0 6px">已发放次数（前 20）</p>
        <div class="table-scroll" style="max-height:150px;overflow-y:auto">
          <div class="audit-table">
            <div v-for="t in times.slice(0, 20)" :key="t.client_id" class="audit-row" style="grid-template-columns:minmax(140px,1fr) minmax(120px,.8fr) 70px">
              <span>{{ t.username }}</span>
              <span class="muted">{{ t.email }}</span>
              <span>{{ t.num }} 张</span>
            </div>
          </div>
        </div>
      </div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton secondary @click="showSend = false">取消</NButton>
          <NButton type="primary" :loading="sending" @click="doSend">确认发放</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="showRecords" preset="card" :title="`使用记录 ${recordVoucher?.code || ''}`" style="width:min(860px,96vw)">
      <div class="row" style="gap:8px;flex-wrap:wrap">
        <NInput v-model:value="recordKeyword" placeholder="按昵称 / 邮箱 / 手机号搜索" style="max-width:260px" @keyup.enter="() => { recordPage = 1; loadRecords() }" />
        <NSelect v-model:value="recordUse" :options="useOptions" style="width:140px" @update:value="() => { recordPage = 1; loadRecords() }" />
        <NButton secondary @click="() => { recordPage = 1; loadRecords() }">刷新</NButton>
        <span class="muted">共 {{ recordTotal }} 条</span>
      </div>
      <div v-if="records.length" class="table-scroll" style="margin-top:10px"><div class="audit-table">
        <div class="audit-row audit-head" style="grid-template-columns:minmax(130px,1fr) 80px 80px minmax(120px,.8fr) 140px 150px"><span>用户</span><span>来源</span><span>状态</span><span>订单</span><span>领取时间</span><span>使用时间 / 操作</span></div>
        <div v-for="r in records" :key="r.id" class="audit-row" style="grid-template-columns:minmax(130px,1fr) 80px 80px minmax(120px,.8fr) 140px 150px">
          <span>{{ r.username }}<small>{{ r.email }}{{ r.phone ? ' · ' + r.phone : '' }}</small></span>
          <span>{{ r.source === 'claim' ? '领取' : '发放' }}</span>
          <span><NTag :type="r.used ? 'success' : 'default'" size="tiny" round>{{ r.used ? '已使用' : '未使用' }}</NTag></span>
          <span class="muted">{{ r.order_id || '—' }}</span>
          <span class="muted">{{ fmt(r.created_at) }}</span>
          <span class="muted">{{ r.used_at ? fmt(r.used_at) : '—' }}<small><NButton size="tiny" tertiary type="error" :disabled="r.used" @click="removeRecord(r)">删除</NButton></small></span>
        </div>
      </div></div>
      <div v-else class="empty-box">{{ recordBusy ? '正在加载…' : '还没有领取或使用记录。' }}</div>
      <div class="row" style="justify-content:flex-end;gap:8px;margin-top:10px">
        <NButton size="small" secondary :disabled="recordPage <= 1" @click="() => { recordPage--; loadRecords() }">上一页</NButton>
        <span class="muted" style="align-self:center">{{ recordPage }} / {{ recordPages }}</span>
        <NButton size="small" secondary :disabled="recordPage >= recordPages" @click="() => { recordPage++; loadRecords() }">下一页</NButton>
      </div>
    </NModal>
  </div>
</template>
