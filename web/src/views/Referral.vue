<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NModal, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 推介计划（对齐魔方 IdcsmartRecommend 插件）：
// 开启计划 → 分享推介链接 / 建议推介产品 / 自定义链接 → 被推介用户下单后产生奖励
// （待确认 → 已确认）→ 申请提现。旧的「推广返佣」为未开启计划时的即时返佣，二者互斥。

const message = useMessage()
const loading = ref(true)
const busy = ref(false)
const info = ref<any>({})
const description = ref<any>({ awards_cents: 0, max_ratio: 0, confirm_days: 14 })
const products = ref<any[]>([])
const links = ref<any[]>([])
const systemUrls = ref<string[]>([])
const policy = ref<{ list: any[]; config: any }>({ list: [], config: {} })
const awards = reactive({ list: [] as any[], count: 0, page: 1, limit: 20, status: '' })
const withdrawals = reactive({ list: [] as any[], count: 0, page: 1, limit: 20, status: '' })
const tab = ref('1')
const policyVisible = ref(false)
const openVisible = ref(false)
const linkVisible = ref(false)
const withdrawVisible = ref(false)
const linkForm = reactive({ system_url: '', custom_url: '' })
const withdrawForm = reactive({ amount: 0, method: '支付宝' })

const opened = computed(() => !!info.value?.user_id)
const withdrawMin = computed(() => Number(policy.value.config?.withdraw_min_cents || 0))
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const statusText: Record<string, string> = { Pending: '确认中', Active: '已确认', Invalid: '无效', Frozen: '冻结' }
const statusType = (s: string) => (s === 'Active' ? 'success' : s === 'Pending' ? 'warning' : s === 'Frozen' ? 'error' : 'default')
const typeText = (t: string) => (t === 'renew' ? '续费' : t === 'init' ? '奖励存款' : '新购')
const withdrawStatus = ['待审核', '待打款', '审核驳回', '已打款']
const methodOptions = ['支付宝', '微信', '银行卡'].map(v => ({ label: v, value: v }))
const linkOptions = computed(() => (systemUrls.value.length ? systemUrls.value : ['']).map(u => ({ label: u, value: u })))

function dms(secs: number) {
  const s = Number(secs || 0)
  if (s <= 0) return '—'
  const days = Math.floor(s / 86400)
  const hours = Math.floor((s % 86400) / 3600)
  const minutes = Math.floor((s % 3600) / 60)
  return `${days}天${hours}时${minutes}分`
}
function copy(text: string) {
  if (!text) return
  navigator.clipboard?.writeText(text)
  message.success('复制成功')
}

async function loadAll() {
  loading.value = true
  try {
    const [p, d, s] = await Promise.all([
      api.get('/recommend/promoter'),
      api.get('/recommend/description'),
      api.get('/recommend/promoter/system_url')
    ])
    info.value = dataOf<any>(p).promoter || {}
    description.value = dataOf<any>(d)
    systemUrls.value = dataOf<any>(s).system_urls || []
    if (opened.value) {
      await Promise.all([loadProducts(), loadLinks(), loadAwards(), loadWithdrawals(), loadPolicy()])
    }
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取推介信息失败')
  } finally {
    loading.value = false
  }
}
async function loadProducts() {
  try { products.value = dataOf<any>(await api.get('/recommend/products')).products || [] } catch { products.value = [] }
}
async function loadLinks() {
  try { links.value = dataOf<any>(await api.get('/recommend/promoter/url')).list || [] } catch { links.value = [] }
}
async function loadAwards() {
  try {
    const d = dataOf<any>(await api.get('/recommend', { params: { page: awards.page, limit: awards.limit, status: awards.status } }))
    awards.list = d.list || []
    awards.count = d.count || 0
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取推介记录失败') }
}
async function loadWithdrawals() {
  try {
    const d = dataOf<any>(await api.get('/recommend/withdrawals', { params: { page: withdrawals.page, limit: withdrawals.limit, status: withdrawals.status } }))
    withdrawals.list = d.list || []
    withdrawals.count = d.count || 0
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取提现记录失败') }
}
async function loadPolicy() {
  try { policy.value = dataOf<any>(await api.get('/recommend/config')) } catch { /* 政策可选展示 */ }
}
async function openPlan() {
  busy.value = true
  try {
    await api.post('/recommend/promoter')
    message.success('已开启推介计划')
    openVisible.value = false
    await loadAll()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '开启失败') }
  finally { busy.value = false }
}
async function copyProduct(row: any) {
  try {
    const d = dataOf<any>(await api.get('/recommend/copy_link', { params: { product_id: row.product_id } }))
    copy(d.url)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '生成链接失败') }
}
function openLinkDialog() {
  linkForm.system_url = systemUrls.value[0] || ''
  linkForm.custom_url = ''
  linkVisible.value = true
}
async function createLink() {
  if (!linkForm.system_url) { message.warning('请选择可推介的页面链接'); return }
  if (!linkForm.custom_url) { message.warning('请输入自定义后缀'); return }
  busy.value = true
  try {
    await api.post('/recommend/promoter/url', { system_url: linkForm.system_url, custom_url: linkForm.custom_url })
    message.success('生成自定义链接成功！')
    linkVisible.value = false
    await loadLinks()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '生成链接失败') }
  finally { busy.value = false }
}
async function removeLink(row: any) {
  try {
    await api.delete(`/recommend/promoter/url/${row.id}`)
    message.success('删除成功！')
    await loadLinks()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}
function openWithdraw() {
  withdrawForm.amount = 0
  withdrawForm.method = '支付宝'
  withdrawVisible.value = true
}
async function submitWithdraw() {
  const cents = Math.round(Number(withdrawForm.amount || 0) * 100)
  if (cents <= 0) { message.warning('请输入提现金额'); return }
  busy.value = true
  try {
    await api.post('/recommend/withdraw', { amount_cents: cents, method: withdrawForm.method })
    message.success('申请提现成功')
    withdrawVisible.value = false
    await loadAll()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '申请提现失败') }
  finally { busy.value = false }
}

onMounted(loadAll)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">推介计划</div>
        <h1>邀请好友购买，赚取推介奖励</h1>
        <p>通过推介链接邀请尚未注册的用户注册；TA 购买商品支付成功后，你将获得最高 {{ description.max_ratio }}% 的推介奖励，确认后即可提现。</p>
      </div>
      <NButton v-if="opened" secondary @click="policyVisible = true">查看完整推介计划</NButton>
    </div>

    <div v-if="loading" class="panel empty-box">加载中…</div>

    <template v-else>
      <section v-if="!opened" class="panel stack">
        <div class="panel-title-row"><div><h2>商品推介计划</h2><span>开启后即可生成专属推介链接</span></div></div>
        <p class="muted">通过推介链接邀请尚未注册的用户注册；注册成功并购买商品后，你将获得最高 {{ description.max_ratio }}% 的推介奖励。</p>
        <p v-if="description.awards_cents > 0" class="muted">现在开启，即刻享受 {{ money(description.awards_cents) }} 推介奖励（确认期 {{ description.confirm_days }} 天）。</p>
        <div class="row" style="gap:8px">
          <NButton type="primary" @click="openVisible = true">立刻开启</NButton>
          <NButton secondary @click="policyVisible = true">查看完整推介计划</NButton>
        </div>
      </section>

      <template v-else>
        <section class="panel">
          <div class="account-kpis">
            <div><span>可提现奖励金额</span><strong>{{ money(info.withdrawable_cents) }}</strong></div>
            <div><span>已提现奖励金额</span><strong>{{ money(info.withdrawn_cents) }}</strong></div>
            <div><span>待确认金额</span><strong>{{ money(info.pending_cents) }}</strong></div>
            <div>
              <span>已确认金额</span><strong>{{ money(info.active_cents) }}</strong>
              <small v-if="info.frozen_cents > 0" class="muted">冻结 {{ money(info.frozen_cents) }}</small>
            </div>
          </div>
        </section>

        <div class="users-toolbar">
          <NButton :type="tab === '1' ? 'primary' : 'default'" size="small" @click="tab = '1'">推介链接</NButton>
          <NButton :type="tab === '2' ? 'primary' : 'default'" size="small" @click="tab = '2'">推介记录</NButton>
          <NButton :type="tab === '3' ? 'primary' : 'default'" size="small" @click="tab = '3'">提现记录</NButton>
          <div class="grow"></div>
          <NButton v-if="tab === '3'" type="primary" size="small" :disabled="info.withdrawable_cents <= 0" @click="openWithdraw">申请提现</NButton>
        </div>

        <section v-if="tab === '1'" class="panel stack">
          <div class="panel-title-row"><div><h2>推介链接</h2><span>发送推介链接给有需要的人，邀请他人购买商品时，请将以下链接发给被推介者</span></div></div>
          <div class="row" style="gap:8px">
            <NInput :value="info.url" readonly />
            <NButton type="primary" @click="copy(info.url)">复制链接</NButton>
          </div>

          <div class="panel-title-row"><div><h2>建议推介产品</h2><span>通过链接完成订购，即可获得现金返佣</span></div></div>
          <div v-if="products.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head"><span>商品名称</span><span>奖励比例</span><span>操作</span></div>
            <div v-for="p in products" :key="p.product_id" class="audit-row">
              <span>{{ p.name }}</span>
              <span>{{ p.ratio }}%</span>
              <span><NButton size="tiny" tertiary @click="copyProduct(p)">复制链接</NButton></span>
            </div>
          </div></div>
          <div v-else class="empty-box">暂无建议推介产品，请等待站长配置。</div>

          <div class="panel-title-row">
            <div><h2>自定义推介链接</h2><span>为不同推广渠道生成带后缀的链接</span></div>
            <NButton size="small" secondary @click="openLinkDialog">生成链接</NButton>
          </div>
          <div v-if="links.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head"><span>系统页面</span><span>自定义后缀</span><span>完整链接</span><span>操作</span></div>
            <div v-for="l in links" :key="l.id" class="audit-row">
              <span class="muted">{{ l.system_url }}</span>
              <span>{{ l.custom_url }}</span>
              <span class="muted">{{ l.url }}</span>
              <span class="row" style="gap:6px">
                <NButton size="tiny" tertiary @click="copy(l.url)">复制</NButton>
                <NButton size="tiny" tertiary type="error" @click="removeLink(l)">删除</NButton>
              </span>
            </div>
          </div></div>
          <div v-else class="empty-box">还没有自定义链接。</div>
        </section>

        <section v-if="tab === '2'" class="panel stack">
          <div class="users-toolbar">
            <NSelect v-model:value="awards.status" clearable style="width:160px" :options="[
              { label: '确认中', value: 'Pending' },
              { label: '已确认', value: 'Active' },
              { label: '无效', value: 'Invalid' },
              { label: '冻结', value: 'Frozen' }
            ]" placeholder="当前状态" />
            <NButton size="small" @click="awards.page = 1; loadAwards()">查询</NButton>
          </div>
          <div v-if="awards.list.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head"><span>被推介用户</span><span>商品名称</span><span>奖励类型</span><span>奖励金额</span><span>剩余确认时间</span><span>当前状态</span></div>
            <div v-for="r in awards.list" :key="r.id" class="audit-row">
              <span>{{ r.username || '—' }}</span>
              <span>{{ r.product_name || '—' }}</span>
              <span>{{ typeText(r.type) }}</span>
              <span>{{ money(r.awards_amount_cents) }}</span>
              <span class="muted">{{ dms(r.surplus_seconds) }}</span>
              <span>
                <NTag :type="statusType(r.status)" size="tiny" round>{{ statusText[r.status] || r.status }}</NTag>
                <span v-if="r.status === 'Invalid' && r.invalid_reason" class="muted">（{{ r.invalid_reason }}）</span>
              </span>
            </div>
          </div></div>
          <div v-else class="empty-box">还没有推介记录。</div>
          <div class="row" style="gap:8px;justify-content:flex-end">
            <span class="muted">第 {{ awards.page }} / {{ Math.max(1, Math.ceil(awards.count / awards.limit)) }} 页</span>
            <NButton size="small" secondary :disabled="awards.page <= 1" @click="awards.page = 1; loadAwards()">上一页</NButton>
            <NButton size="small" secondary :disabled="awards.page * awards.limit >= awards.count" @click="awards.page += 1; loadAwards()">下一页</NButton>
          </div>
        </section>

        <section v-if="tab === '3'" class="panel stack">
          <div class="users-toolbar">
            <NSelect v-model:value="withdrawals.status" clearable style="width:160px" :options="[
              { label: '待审核', value: '0' },
              { label: '待打款', value: '1' },
              { label: '审核驳回', value: '2' },
              { label: '已打款', value: '3' }
            ]" placeholder="当前状态" />
            <NButton size="small" @click="withdrawals.page = 1; loadWithdrawals()">查询</NButton>
          </div>
          <div v-if="withdrawals.list.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head"><span>提现日期</span><span>提现金额</span><span>提现方式</span><span>当前状态</span></div>
            <div v-for="r in withdrawals.list" :key="r.id" class="audit-row">
              <span class="muted">{{ fmt(r.created_at) }}</span>
              <span>{{ money(r.amount_cents) }}</span>
              <span>{{ r.method || '—' }}</span>
              <span>
                <NTag :type="r.status === 3 ? 'success' : r.status === 2 ? 'error' : 'warning'" size="tiny" round>
                  {{ withdrawStatus[r.status] || r.status }}
                </NTag>
                <span v-if="r.status === 2 && r.reason" class="muted">（{{ r.reason }}）</span>
              </span>
            </div>
          </div></div>
          <div v-else class="empty-box">还没有提现记录。</div>
          <div class="row" style="gap:8px;justify-content:flex-end">
            <span class="muted">第 {{ withdrawals.page }} / {{ Math.max(1, Math.ceil(withdrawals.count / withdrawals.limit)) }} 页</span>
            <NButton size="small" secondary :disabled="withdrawals.page <= 1" @click="withdrawals.page = 1; loadWithdrawals()">上一页</NButton>
            <NButton size="small" secondary :disabled="withdrawals.page * withdrawals.limit >= withdrawals.count" @click="withdrawals.page += 1; loadWithdrawals()">下一页</NButton>
          </div>
        </section>
      </template>
    </template>

    <NModal v-model:show="policyVisible" preset="card" title="完整推介计划" style="max-width: 860px">
      <div class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>商品名称</span><span>奖励比例</span><span>购买最低金额</span><span>续费比例</span><span>续费最低金额</span></div>
        <div v-for="r in policy.list" :key="r.product_id" class="audit-row">
          <span>{{ r.product_name }}</span>
          <span>{{ r.ratio }}%</span>
          <span>{{ money(r.amount_cents) }}</span>
          <span>{{ r.renew_ratio }}%</span>
          <span>{{ money(r.renew_amount_cents) }}</span>
        </div>
      </div></div>
      <p v-if="!policy.list.length" class="empty-box">站长还未配置商品奖励比例。</p>
      <p class="muted" style="margin-top:10px">
        确认期 {{ policy.config?.confirm_days ?? description.confirm_days }} 天（0 天为即刻确认）；最低提现金额 {{ money(policy.config?.withdraw_min_cents || 0) }}，
        提现手续费 {{ policy.config?.withdraw_handling_fee || 0 }}%。同一订单相同商品合计金额达到购买最低金额才触发奖励。
      </p>
    </NModal>

    <NModal v-model:show="openVisible" preset="card" title="开启推介计划" style="max-width: 420px">
      <p>您将开启推介计划，请确认是否继续？</p>
      <template #footer>
        <div class="row" style="gap:8px;justify-content:flex-end">
          <NButton secondary @click="openVisible = false">取消</NButton>
          <NButton type="primary" :loading="busy" @click="openPlan">确定</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="linkVisible" preset="card" title="生成自定义推介链接" style="max-width: 520px">
      <div class="stack">
        <label class="stack"><span class="muted">页面链接</span>
          <NSelect v-model:value="linkForm.system_url" :options="linkOptions" placeholder="请选择可推介的页面链接" />
        </label>
        <label class="stack"><span class="muted">自定义后缀</span>
          <NInput v-model:value="linkForm.custom_url" placeholder="请输入自定义后缀（如 weibo）" />
        </label>
      </div>
      <template #footer>
        <div class="row" style="gap:8px;justify-content:flex-end">
          <NButton secondary @click="linkVisible = false">取消</NButton>
          <NButton type="primary" :loading="busy" @click="createLink">生成链接</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="withdrawVisible" preset="card" title="申请提现" style="max-width: 460px">
      <div class="stack">
        <p class="muted">可提现 {{ money(info.withdrawable_cents) }}，最低提现 {{ money(withdrawMin) }}，手续费 {{ policy.config?.withdraw_handling_fee || 0 }}%。</p>
        <label class="stack"><span class="muted">提现金额（元）</span>
          <NInputNumber v-model:value="withdrawForm.amount" :min="0" :precision="2" style="width:100%" />
        </label>
        <label class="stack"><span class="muted">提现方式</span>
          <NSelect v-model:value="withdrawForm.method" :options="methodOptions" />
        </label>
      </div>
      <template #footer>
        <div class="row" style="gap:8px;justify-content:flex-end">
          <NButton secondary @click="withdrawVisible = false">取消</NButton>
          <NButton type="primary" :loading="busy" @click="submitWithdraw">提交申请</NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>
