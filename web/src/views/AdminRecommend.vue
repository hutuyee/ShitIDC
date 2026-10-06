<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NModal, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'

// 推介计划 · 奖励记录（对齐魔方 IdcsmartRecommend 插件后台）：
// 列表筛选（推介人 / 被推介人 / 商品 / 状态）、改奖励金额、确认 / 冻结 / 解冻 /
// 置无效（预设回复或自定义原因）/ 删除，预设无效回复管理，以及提现审核。

const message = useMessage()
const tab = ref<'awards' | 'withdrawals'>('awards')
const busy = ref(false)

const filters = reactive({ status: '', query: '', product_id: '', page: 1, limit: 20 })
const awards = ref<any[]>([])
const count = ref(0)
const products = ref<any[]>([])

const prereplies = ref<any[]>([])
const preVisible = ref(false)
const preEditing = ref(0)
const preContent = ref('')

const invalidVisible = ref(false)
const invalidRow = ref<any>(null)
const invalidReasonID = ref<number | string>('')
const invalidCustom = ref('')

const editingID = ref(0)
const editingAmount = ref(0)

const wFilters = reactive({ status: '', page: 1, limit: 20 })
const ws = ref<any[]>([])
const wCount = ref(0)
const rejectVisible = ref(false)
const rejectRow = ref<any>(null)
const rejectReason = ref('')

const productOptions = computed(() => products.value.map((p: any) => ({ label: p.name, value: p.id })))
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const statusText: Record<string, string> = { Pending: '确认中', Active: '已确认', Invalid: '无效', Frozen: '已冻结' }
const statusType = (s: string) => (s === 'Active' ? 'success' : s === 'Pending' ? 'warning' : s === 'Frozen' ? 'error' : 'default')
const typeText = (t: string) => (t === 'renew' ? '续费' : t === 'init' ? '奖励存款' : '新购')
const withdrawStatus = ['待审核', '待打款', '审核驳回', '已打款']
const pageText = (page: number, limit: number, total: number) => `第 ${page} / ${Math.max(1, Math.ceil(total / limit))} 页`

function dms(secs: number) {
  const s = Number(secs || 0)
  if (s <= 0) return '—'
  const days = Math.floor(s / 86400)
  const hours = Math.floor((s % 86400) / 3600)
  const minutes = Math.floor((s % 3600) / 60)
  return `${days}天${hours}时${minutes}分`
}

async function loadAwards() {
  try {
    const d = dataOf<any>(await api.get('/admin/recommend', {
      params: {
        page: filters.page,
        limit: filters.limit,
        status: filters.status,
        query: filters.query,
        product_id: filters.product_id || undefined,
        orderby: 'id',
        sort: 'desc'
      }
    }))
    awards.value = d.list || []
    count.value = d.count || 0
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取奖励记录失败') }
}
async function loadProducts() {
  try { products.value = dataOf<any>(await api.get('/admin/products')) } catch { products.value = [] }
}
async function loadPrereplies() {
  try { prereplies.value = dataOf<any>(await api.get('/admin/recommend/prereplies')).list || [] } catch { prereplies.value = [] }
}
async function loadWithdrawals() {
  try {
    const d = dataOf<any>(await api.get('/admin/recommend/withdrawals', { params: { page: wFilters.page, limit: wFilters.limit, status: wFilters.status } }))
    ws.value = d.list || []
    wCount.value = d.count || 0
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取提现记录失败') }
}
function searchAwards() { filters.page = 1; loadAwards() }
function searchWithdrawals() { wFilters.page = 1; loadWithdrawals() }

function editAmount(row: any) {
  editingID.value = row.id
  editingAmount.value = Number(row.awards_amount_cents || 0) / 100
}
async function saveAmount(row: any) {
  busy.value = true
  try {
    await api.put(`/admin/recommend/awards/${row.id}/awards_amount`, { awards_amount_cents: Math.round(Number(editingAmount.value || 0) * 100) })
    message.success('奖励金额已保存')
    editingID.value = 0
    await loadAwards()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}
async function op(row: any, action: string, label: string) {
  busy.value = true
  try {
    await api.put(`/admin/recommend/awards/${row.id}/${action}`)
    message.success(label)
    await loadAwards()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
  finally { busy.value = false }
}
async function removeAward(row: any) {
  busy.value = true
  try {
    await api.delete(`/admin/recommend/awards/${row.id}`)
    message.success('已删除')
    await loadAwards()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
  finally { busy.value = false }
}
function openInvalid(row: any) {
  invalidRow.value = row
  invalidReasonID.value = ''
  invalidCustom.value = ''
  if (!prereplies.value.length) loadPrereplies()
  invalidVisible.value = true
}
async function submitInvalid() {
  let reason = ''
  if (invalidReasonID.value === 'custom') reason = invalidCustom.value.trim()
  else {
    const hit = prereplies.value.find((p: any) => Number(p.id) === Number(invalidReasonID.value))
    reason = hit?.content || ''
  }
  if (!reason) { message.warning('请选择或填写无效原因'); return }
  busy.value = true
  try {
    await api.put(`/admin/recommend/awards/${invalidRow.value.id}/invalid`, { invalid_reason: reason })
    message.success('已置为无效推介')
    invalidVisible.value = false
    await loadAwards()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
  finally { busy.value = false }
}

function openPrereply(row?: any) {
  preEditing.value = row?.id || 0
  preContent.value = row?.content || ''
  preVisible.value = true
}
async function savePrereply() {
  const content = preContent.value.trim()
  if (!content) { message.warning('请填写回复内容'); return }
  busy.value = true
  try {
    if (preEditing.value) await api.put(`/admin/recommend/prereplies/${preEditing.value}`, { content })
    else await api.post('/admin/recommend/prereplies', { content })
    message.success('预设回复已保存')
    preEditing.value = 0
    preContent.value = ''
    await loadPrereplies()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}
async function removePrereply(row: any) {
  busy.value = true
  try {
    await api.delete(`/admin/recommend/prereplies/${row.id}`)
    message.success('已删除')
    await loadPrereplies()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
  finally { busy.value = false }
}

async function review(row: any, status: number, label: string) {
  busy.value = true
  try {
    await api.put(`/admin/recommend/withdrawals/${row.id}`, { status })
    message.success(label)
    await loadWithdrawals()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
  finally { busy.value = false }
}
function openReject(row: any) {
  rejectRow.value = row
  rejectReason.value = ''
  rejectVisible.value = true
}
async function submitReject() {
  const reason = rejectReason.value.trim()
  if (!reason) { message.warning('请填写驳回原因'); return }
  busy.value = true
  try {
    await api.put(`/admin/recommend/withdrawals/${rejectRow.value.id}`, { status: 2, reason })
    message.success('已驳回')
    rejectVisible.value = false
    await loadWithdrawals()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
  finally { busy.value = false }
}

onMounted(() => { loadAwards(); loadProducts(); loadPrereplies(); loadWithdrawals() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>推介计划</h1>
        <p>对齐魔方「IdcsmartRecommend」插件：被推介用户购买商品并支付成功后按商品比例生成奖励记录，确认后计入可提现余额；支持冻结 / 无效 / 改奖励金额与提现审核。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary @click="openPrereply()">预设无效回复</NButton>
        <NButton secondary tag="a" :href="`${ADMIN_PATH}/recommend/config`">推介配置</NButton>
      </div>
    </div>

    <div class="users-toolbar">
      <NButton :type="tab === 'awards' ? 'primary' : 'default'" size="small" @click="tab = 'awards'">奖励记录</NButton>
      <NButton :type="tab === 'withdrawals' ? 'primary' : 'default'" size="small" @click="tab = 'withdrawals'">提现审核</NButton>
    </div>

    <section v-if="tab === 'awards'" class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="filters.query" clearable style="width:220px" placeholder="推介人 / 被推介人邮箱" @keyup.enter="searchAwards" />
        <NSelect v-model:value="filters.product_id" clearable filterable style="width:220px" :options="productOptions" placeholder="商品" />
        <NSelect v-model:value="filters.status" clearable style="width:150px" :options="[
          { label: '确认中', value: 'Pending' },
          { label: '已确认', value: 'Active' },
          { label: '无效', value: 'Invalid' },
          { label: '已冻结', value: 'Frozen' }
        ]" placeholder="状态" />
        <NButton size="small" @click="searchAwards">查询</NButton>
      </div>

      <div v-if="awards.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head">
          <span>推介人</span><span>被推介用户</span><span>商品名称</span><span>类型</span>
          <span>购买金额</span><span>奖励比例</span><span>奖励金额</span><span>剩余确认时间</span>
          <span>状态</span><span>操作</span>
        </div>
        <div v-for="r in awards" :key="r.id" class="audit-row">
          <span>{{ r.promoter }}</span>
          <span>{{ r.username || '—' }}</span>
          <span>{{ r.product_name || '—' }}</span>
          <span>{{ typeText(r.type) }}</span>
          <span>{{ money(r.buy_amount_cents) }}</span>
          <span>{{ Number(r.ratio || 0) }}%</span>
          <span>
            <template v-if="editingID === r.id">
              <NInputNumber v-model:value="editingAmount" size="small" :min="0" :precision="2" style="width:130px" />
            </template>
            <template v-else>{{ money(r.awards_amount_cents) }}</template>
          </span>
          <span class="muted">{{ dms(r.surplus_seconds) }}</span>
          <span>
            <NTag :type="statusType(r.status)" size="tiny" round>{{ statusText[r.status] || r.status }}</NTag>
            <span v-if="r.status === 'Invalid' && r.invalid_reason" class="muted">（{{ r.invalid_reason }}）</span>
          </span>
          <span class="row" style="gap:6px;flex-wrap:wrap">
            <template v-if="editingID === r.id">
              <NButton size="tiny" type="primary" :loading="busy" @click="saveAmount(r)">保存</NButton>
              <NButton size="tiny" secondary @click="editingID = 0">取消</NButton>
            </template>
            <template v-else>
              <NButton size="tiny" tertiary @click="editAmount(r)">改金额</NButton>
              <NButton v-if="r.status === 'Pending'" size="tiny" tertiary @click="op(r, 'active', '已确认')">立即确认</NButton>
              <NButton v-if="r.status === 'Pending' || r.status === 'Active'" size="tiny" tertiary @click="op(r, 'frozen', '已冻结')">冻结</NButton>
              <NButton v-if="r.status === 'Frozen'" size="tiny" tertiary @click="op(r, 'unfrozen', '已解冻')">解冻</NButton>
              <NButton v-if="r.status !== 'Invalid'" size="tiny" tertiary type="warning" @click="openInvalid(r)">无效</NButton>
              <NButton size="tiny" tertiary type="error" @click="removeAward(r)">删除</NButton>
            </template>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有推介奖励记录。</div>

      <div class="row" style="gap:8px;justify-content:flex-end;margin-top:12px">
        <span class="muted">{{ pageText(filters.page, filters.limit, count) }}</span>
        <NButton size="small" secondary :disabled="filters.page <= 1" @click="filters.page -= 1; loadAwards()">上一页</NButton>
        <NButton size="small" secondary :disabled="filters.page * filters.limit >= count" @click="filters.page += 1; loadAwards()">下一页</NButton>
      </div>
    </section>

    <section v-else class="panel">
      <div class="users-toolbar">
        <NSelect v-model:value="wFilters.status" clearable style="width:160px" :options="[
          { label: '待审核', value: '0' },
          { label: '待打款', value: '1' },
          { label: '审核驳回', value: '2' },
          { label: '已打款', value: '3' }
        ]" placeholder="状态" />
        <NButton size="small" @click="searchWithdrawals">查询</NButton>
      </div>

      <div v-if="ws.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>推介人</span><span>提现日期</span><span>提现金额</span><span>手续费</span><span>提现方式</span><span>状态</span><span>操作</span></div>
        <div v-for="r in ws" :key="r.id" class="audit-row">
          <span>{{ r.promoter }}</span>
          <span class="muted">{{ fmt(r.created_at) }}</span>
          <span>{{ money(r.amount_cents) }}</span>
          <span>{{ money(r.fee_cents) }}</span>
          <span>{{ r.method || '—' }}</span>
          <span>
            <NTag :type="r.status === 3 ? 'success' : r.status === 2 ? 'error' : 'warning'" size="tiny" round>
              {{ withdrawStatus[r.status] || r.status }}
            </NTag>
            <span v-if="r.status === 2 && r.reason" class="muted">（{{ r.reason }}）</span>
          </span>
          <span class="row" style="gap:6px">
            <NButton v-if="r.status === 0" size="tiny" tertiary @click="review(r, 1, '已通过，等待打款')">通过</NButton>
            <NButton v-if="r.status === 1" size="tiny" tertiary @click="review(r, 3, '已标记打款')">标记打款</NButton>
            <NButton v-if="r.status === 0 || r.status === 1" size="tiny" tertiary type="error" @click="openReject(r)">驳回</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有提现记录。</div>

      <div class="row" style="gap:8px;justify-content:flex-end;margin-top:12px">
        <span class="muted">{{ pageText(wFilters.page, wFilters.limit, wCount) }}</span>
        <NButton size="small" secondary :disabled="wFilters.page <= 1" @click="wFilters.page -= 1; loadWithdrawals()">上一页</NButton>
        <NButton size="small" secondary :disabled="wFilters.page * wFilters.limit >= wCount" @click="wFilters.page += 1; loadWithdrawals()">下一页</NButton>
      </div>
    </section>

    <NModal v-model:show="invalidVisible" preset="card" title="无效推介" style="max-width: 560px">
      <div class="stack">
        <p class="muted">推介人 {{ invalidRow?.promoter }}，被推介用户 {{ invalidRow?.username || '—' }}，商品 {{ invalidRow?.product_name || '—' }}。</p>
        <label class="stack"><span class="muted">无效原因</span>
          <NSelect v-model:value="invalidReasonID" :options="[
            ...prereplies.map((p: any) => ({ label: p.content, value: p.id })),
            { label: '自定义', value: 'custom' }
          ]" placeholder="请选择无效原因" />
        </label>
        <label v-if="invalidReasonID === 'custom'" class="stack"><span class="muted">自定义原因</span>
          <NInput v-model:value="invalidCustom" type="textarea" :rows="3" placeholder="请输入无效原因" />
        </label>
      </div>
      <template #footer>
        <div class="row" style="gap:8px;justify-content:flex-end">
          <NButton secondary @click="invalidVisible = false">取消</NButton>
          <NButton type="primary" :loading="busy" @click="submitInvalid">确认无效</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="preVisible" preset="card" title="预设无效回复" style="max-width: 720px">
      <div v-if="prereplies.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>回复内容</span><span>来源</span><span>操作</span></div>
        <div v-for="p in prereplies" :key="p.id" class="audit-row">
          <span>{{ p.content }}</span>
          <span><NTag size="tiny" :type="p.system ? 'success' : 'default'" round>{{ p.system ? '内置' : '自定义' }}</NTag></span>
          <span class="row" style="gap:6px">
            <template v-if="!p.system">
              <NButton size="tiny" tertiary @click="openPrereply(p)">编辑</NButton>
              <NButton size="tiny" tertiary type="error" @click="removePrereply(p)">删除</NButton>
            </template>
            <span v-else class="muted">—</span>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有预设回复。</div>
      <div class="stack" style="margin-top:14px">
        <label class="stack"><span class="muted">{{ preEditing ? '编辑回复内容' : '新增回复内容' }}</span>
          <NInput v-model:value="preContent" type="textarea" :rows="3" placeholder="请输入回复内容" />
        </label>
        <div class="row" style="gap:8px;justify-content:flex-end">
          <NButton v-if="preEditing" secondary @click="preEditing = 0; preContent = ''">取消编辑</NButton>
          <NButton type="primary" :loading="busy" @click="savePrereply">保存预设回复</NButton>
        </div>
      </div>
    </NModal>

    <NModal v-model:show="rejectVisible" preset="card" title="驳回提现" style="max-width: 480px">
      <label class="stack"><span class="muted">驳回原因</span>
        <NInput v-model:value="rejectReason" type="textarea" :rows="3" placeholder="请输入驳回原因" />
      </label>
      <template #footer>
        <div class="row" style="gap:8px;justify-content:flex-end">
          <NButton secondary @click="rejectVisible = false">取消</NButton>
          <NButton type="error" :loading="busy" @click="submitReject">确认驳回</NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>
