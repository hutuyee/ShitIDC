<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NDrawer, NDrawerContent, NInput, NInputNumber, NModal, NTabPane, NTabs, NTag, useDialog, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const message = useMessage()
const dialog = useDialog()
const users = ref<any[]>([])
const query = ref('')
const loading = ref(false)

// 点开一个用户就看到他的全部信息：不用再跳好几个页面拼线索。
const detailOpen = ref(false)
const detail = ref<any>(null)
const detailLoading = ref(false)
const detailTab = ref('overview')
const fieldValues = ref<any[]>([])

const adjustOpen = ref(false)
const amount = ref<number | null>(null)
const reason = ref('')
const busy = ref(false)

// 实名资料单独拉取（含脱敏/完整两种模式）。
const profileOpen = ref(false)
const profile = ref<any>(null)
const profileUnmasked = ref(false)

async function load() {
  loading.value = true
  try {
    users.value = dataOf(await api.get('/admin/users', { params: { query: query.value, limit: 200 } }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取用户失败')
  } finally {
    loading.value = false
  }
}

async function openDetail(u: any) {
  detailOpen.value = true
  detail.value = null
  detailTab.value = 'overview'
  detailLoading.value = true
  try {
    detail.value = dataOf<any>(await api.get(`/admin/users/${u.id}/detail`))
    api.get(`/admin/users/${u.id}/custom-fields`).then(x => { fieldValues.value = dataOf<any>(x).list || [] }).catch(() => { fieldValues.value = [] })
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取用户详情失败')
  } finally { detailLoading.value = false }
}

async function refreshDetail() {
  if (!detail.value) return
  try { detail.value = dataOf<any>(await api.get(`/admin/users/${detail.value.id}/detail`)) }
  catch { /* 刷新失败保留旧数据，不打断操作 */ }
}

function openAdjust() {
  amount.value = null
  reason.value = ''
  adjustOpen.value = true
}

async function submitAdjust() {
  if (!detail.value) return
  if (!amount.value || amount.value === 0) { message.error('金额不能为 0'); return }
  if (!reason.value.trim()) { message.error('必须填写调账原因'); return }
  busy.value = true
  try {
    await api.post('/admin/wallet/adjust', {
      user_id: detail.value.id,
      currency: detail.value.wallets?.[0]?.currency || 'CNY',
      amount_cents: Math.round(amount.value * 100),
      reason: reason.value.trim(),
    })
    message.success('调账完成，已写入流水与审计日志')
    adjustOpen.value = false
    await Promise.all([load(), refreshDetail()])
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '调账失败')
  } finally { busy.value = false }
}

// 列表行与抽屉共用同一套启停逻辑，避免两处提示文案与行为不一致。
function confirmToggle(u: any) {
  const disabling = u.status === 'active'
  dialog.warning({
    title: disabling ? '禁用用户' : '启用用户',
    content: disabling
      ? `确认禁用 ${u.email}？该用户的所有登录会话会立即吊销。`
      : `确认恢复 ${u.email} 的登录？`,
    positiveText: disabling ? '确认禁用' : '确认启用',
    negativeText: '取消',
    async onPositiveClick() {
      try {
        await api.post(`/admin/users/${u.id}/status`, { active: !disabling })
        message.success(disabling ? '已禁用并吊销会话' : '已启用')
        await Promise.all([load(), refreshDetail()])
      } catch (e: any) {
        message.error(e?.response?.data?.error?.message || '操作失败')
      }
    },
  })
}

function toggleStatus() { if (detail.value) confirmToggle(detail.value) }
function toggleStatusFor(u: any) { confirmToggle(u) }

async function openProfile() {
  if (!detail.value) return
  profileOpen.value = true
  profile.value = null
  try {
    const d = dataOf<any>(await api.get(`/admin/users/${detail.value.id}/profile`))
    profile.value = d
    profileUnmasked.value = Boolean(d.unmasked)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取资料失败')
  }
}

const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const fmtDate = (v?: string) => (v ? new Date(v).toLocaleString('zh-CN') : '—')

const statusText: Record<string, string> = {
  active: '正常', pending: '待开通', provisioning: '开通中', suspended: '已暂停',
  terminated: '已终止', failed: '开通失败', unpaid: '未付款', paid: '已付款',
  processing: '处理中', cancelled: '已取消', refunded: '已退款',
}
const statusType = (s: string) =>
  s === 'active' || s === 'paid' ? 'success'
    : s === 'failed' || s === 'terminated' ? 'error'
      : s === 'suspended' || s === 'unpaid' ? 'warning' : 'default'

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">用户管理</div>
        <h1>用户列表</h1>
        <p>点击任意一行查看该用户的余额、机器、订单与授信，并可直接调账或禁用账号。所有资金操作都会写入流水与审计日志。</p>
      </div>
    </div>

    <section class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="query" placeholder="搜索邮箱 / UID，回车确认" clearable @keyup.enter="load" />
        <NButton type="primary" :loading="loading" @click="load">搜索</NButton>
      </div>

      <div v-if="users.length" class="table-scroll"><div class="user-table">
        <div class="user-row user-head"><span>UID</span><span>邮箱</span><span>状态</span><span>余额</span><span>订单</span><span>最近活跃</span><span>注册时间</span><span>操作</span></div>
        <div
          v-for="u in users"
          :key="u.id"
          class="user-row user-row-clickable"
          title="点击查看该用户详情"
          @click="openDetail(u)"
        >
          <span><b class="uid-badge">{{ u.uid }}</b></span>
          <span class="user-email">{{ u.email }}<small v-if="u.email_verified">已验证</small><small v-else class="warn-text">未验证</small></span>
          <span><NTag size="small" :type="u.status === 'active' ? 'success' : 'error'">{{ u.status === 'active' ? '正常' : u.status }}</NTag></span>
          <span class="money">{{ money(u.balance_cents, u.currency) }}</span>
          <span>{{ u.order_count }}</span>
          <span class="muted">{{ fmtDate(u.last_login_at) }}</span>
          <span class="muted">{{ fmtDate(u.created_at) }}</span>
          <!-- 行本身可点；按钮停掉冒泡，避免点按钮又弹一次抽屉。 -->
          <span class="row" style="gap:6px;flex-wrap:wrap" @click.stop>
            <NButton size="tiny" type="primary" secondary @click="openDetail(u)">详情</NButton>
            <NButton v-if="auth.permissions['user.write']" size="tiny" :type="u.status === 'active' ? 'error' : 'success'" secondary @click="toggleStatusFor(u)">{{ u.status === 'active' ? '禁用' : '启用' }}</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">没有匹配的用户。</div>
    </section>

    <!-- 用户详情抽屉：一屏看完这个人的全部情况，并提供常用操作。 -->
    <NDrawer v-model:show="detailOpen" :width="720" placement="right">
      <NDrawerContent :native-scrollbar="false" closable>
        <template #header>
          <div v-if="detail" class="detail-head">
            <b>{{ detail.email }}</b>
            <NTag size="small" :type="detail.status === 'active' ? 'success' : 'error'">
              {{ detail.status === 'active' ? '正常' : detail.status }}
            </NTag>
            <NTag size="small" round>UID {{ detail.uid }}</NTag>
          </div>
          <span v-else>用户详情</span>
        </template>

        <div v-if="detailLoading" class="empty-box">加载中…</div>
        <div v-else-if="!detail" class="empty-box">没有读取到用户信息。</div>
        <div v-else class="stack">
          <!-- 常用操作放在最上面：调账是后台最高频的动作。 -->
          <div class="detail-actions">
            <NButton type="primary" size="small" @click="openAdjust">余额调账</NButton>
            <NButton size="small" tertiary @click="openProfile">实名资料</NButton>
            <NButton
              v-if="auth.permissions['user.write']"
              size="small"
              :type="detail.status === 'active' ? 'error' : 'success'"
              tertiary
              @click="toggleStatus"
            >{{ detail.status === 'active' ? '禁用账号' : '启用账号' }}</NButton>
          </div>

          <div class="detail-stats">
            <div class="stat"><span class="muted">钱包余额</span>
              <b v-if="detail.wallets?.length">
                <template v-for="(w, i) in detail.wallets" :key="w.currency">
                  <template v-if="i > 0"> · </template>{{ money(w.balance_cents, w.currency) }}
                </template>
              </b>
              <b v-else class="muted">—</b>
            </div>
            <div class="stat"><span class="muted">订单数</span><b>{{ detail.order_count }}</b></div>
            <div class="stat"><span class="muted">服务数</span><b>{{ detail.service_count }}<em class="muted"> · 活跃 {{ detail.active_services }}</em></b></div>
            <div class="stat"><span class="muted">累计已付</span><b>{{ money(detail.paid_total_cents) }}</b></div>
          </div>

          <NTabs v-model:value="detailTab" type="line" animated>
            <NTabPane name="overview" tab="概览">
              <div class="stack">
                <div class="mini-row"><span class="muted">邮箱验证</span><b>{{ detail.email_verified ? '已验证' : '未验证' }}</b></div>
                <div class="mini-row"><span class="muted">手机号</span><b>{{ detail.phone || '—' }}<em v-if="detail.phone && !detail.phone_verified" class="warn-text"> 未验证</em></b></div>
                <div class="mini-row"><span class="muted">客户组</span><b>{{ detail.group_name || '—' }}<em v-if="detail.group_discount" class="muted"> · 折扣 {{ detail.group_discount }}%</em></b></div>
                <div class="mini-row"><span class="muted">最近登录</span><b>{{ fmtDate(detail.last_login_at) }}</b></div>
                <div class="mini-row"><span class="muted">注册时间</span><b>{{ fmtDate(detail.created_at) }}</b></div>
                <div v-if="detail.credit?.enabled" class="credit-box">
                  <b>后付费授信</b>
                  <div class="mini-row"><span class="muted">额度</span><b>{{ money(detail.credit.limit_cents) }} · 账期 {{ detail.credit.credit_days }} 天</b></div>
                  <div class="mini-row"><span class="muted">已占用</span><b>{{ money(detail.credit.used_cents) }}</b></div>
                  <div class="mini-row"><span class="muted">可用</span><b>{{ money(detail.credit.available_cents) }}</b></div>
                  <div v-if="detail.credit.overdue_cents > 0" class="mini-row">
                    <span class="muted">逾期</span><b class="warn-text">{{ money(detail.credit.overdue_cents) }}（{{ detail.credit.overdue_count }} 笔）</b>
                  </div>
                </div>
              </div>
            </NTabPane>

            <NTabPane name="services" :tab="`机器 (${detail.services?.length || 0})`">
              <div v-if="!detail.services?.length" class="empty-box" style="margin:0">该用户还没有服务。</div>
              <div v-else class="mini-list">
                <div v-for="s in detail.services" :key="s.id" class="mini-item">
                  <div class="mini-item-main">
                    <b>{{ s.product_name }}</b>
                    <small class="muted">{{ s.billing_cycle || '—' }} · 到期 {{ fmtDate(s.expires_at) }}</small>
                  </div>
                  <NTag size="tiny" :type="statusType(s.status)">{{ statusText[s.status] || s.status }}</NTag>
                </div>
              </div>
            </NTabPane>

            <NTabPane name="orders" :tab="`订单 (${detail.orders?.length || 0})`">
              <div v-if="!detail.orders?.length" class="empty-box" style="margin:0">该用户还没有订单。</div>
              <div v-else class="mini-list">
                <div v-for="o in detail.orders" :key="o.id" class="mini-item">
                  <div class="mini-item-main">
                    <b>{{ money(o.total_cents, o.currency) }}</b>
                    <small class="muted">{{ o.kind }} · {{ o.pay_method === 'postpaid' ? '后付费' : '预付费' }} · {{ fmtDate(o.created_at) }}</small>
                  </div>
                  <NTag size="tiny" :type="statusType(o.status)">{{ statusText[o.status] || o.status }}</NTag>
                </div>
              </div>
            </NTabPane>

            <NTabPane name="fields" :tab="`自定义字段 (${fieldValues.filter(f => f.has_value).length})`">
              <div v-if="!fieldValues.length" class="empty-box" style="margin:0">没有启用的自定义字段。</div>
              <div v-else class="stack">
                <div v-for="f in fieldValues" :key="f.id" class="mini-row">
                  <span class="muted">{{ f.name }}<em v-if="f.admin_only" class="muted">（管理员可见）</em></span>
                  <b v-if="f.type === 'password'">{{ f.has_value ? '已设置' : '—' }}</b>
                  <b v-else-if="f.type === 'tickbox'">{{ f.value === '1' ? '是' : '否' }}</b>
                  <b v-else>{{ f.value || '—' }}</b>
                </div>
              </div>
            </NTabPane>
          </NTabs>

        </div>
      </NDrawerContent>
    </NDrawer>

    <NModal v-model:show="adjustOpen" preset="card" :title="`余额调账 · UID ${detail?.uid ?? ''}`" style="width:min(420px,92vw)">
      <div class="stack" v-if="detail">
        <div class="muted" style="font-size:12px">{{ detail.email }} · 当前余额 {{ money(detail.wallets?.[0]?.balance_cents || 0) }}</div>
        <NInputNumber v-model:value="amount" :precision="2" placeholder="正数充值 / 负数扣减" style="width:100%">
          <template #prefix>¥</template>
        </NInputNumber>
        <NInput v-model:value="reason" placeholder="调账原因（必填，写入审计日志）" />
        <div class="security-note">操作会生成一条钱包流水（冲正请用反向调账），并写入审计日志。</div>
        <NButton type="primary" block :loading="busy" @click="submitAdjust">确认调账</NButton>
      </div>
    </NModal>

    <NModal v-model:show="profileOpen" preset="card" title="实名资料" style="width:min(480px,92vw)">
      <div class="stack" v-if="profile">
        <NTag size="small" :type="profileUnmasked ? 'warning' : 'default'">{{ profileUnmasked ? '完整信息（已写入审计日志）' : '敏感字段已脱敏' }}</NTag>
        <div class="mini-row"><span class="muted">昵称</span><b>{{ profile.profile?.nickname || '—' }}</b></div>
        <div class="mini-row"><span class="muted">真实姓名</span><b>{{ profile.profile?.real_name || '—' }}</b></div>
        <div class="mini-row"><span class="muted">手机号</span><b>{{ profile.profile?.phone || '—' }}</b></div>
        <div class="mini-row"><span class="muted">QQ</span><b>{{ profile.profile?.qq || '—' }}</b></div>
        <div class="mini-row"><span class="muted">公司</span><b>{{ profile.profile?.company || '—' }}</b></div>
        <div class="mini-row"><span class="muted">地区</span><b>{{ [profile.profile?.country, profile.profile?.province, profile.profile?.city].filter(Boolean).join(' / ') || '—' }}</b></div>
        <div class="mini-row"><span class="muted">地址</span><b>{{ profile.profile?.address || '—' }}</b></div>
        <div class="security-note">手机号/QQ/地址默认脱敏；持有 pii.read.full 权限的管理员查看完整信息时，每次都会记录审计日志。</div>
      </div>
      <div v-else class="empty-box">加载中…</div>
    </NModal>
  </div>
</template>

<style scoped>
.user-row-clickable { cursor: pointer; }
.user-row-clickable:hover { background: var(--panel-soft, #f7f9ff); }

.detail-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.detail-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.detail-stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 10px; }
.detail-stats .stat {
  display: flex; flex-direction: column; gap: 4px;
  border: 1px solid var(--border, #e5e8f0); border-radius: 10px; padding: 10px 12px;
}
.detail-stats .stat b { font-size: 16px; }
.detail-stats .stat em { font-style: normal; font-size: 12px; font-weight: 400; }

.credit-box {
  border: 1px solid var(--border, #e5e8f0); border-radius: 10px; padding: 10px 12px;
  display: flex; flex-direction: column; gap: 6px;
}
.mini-list { display: flex; flex-direction: column; gap: 8px; }
.mini-item {
  display: flex; justify-content: space-between; align-items: center; gap: 10px;
  border: 1px solid var(--border, #e5e8f0); border-radius: 10px; padding: 9px 12px;
}
.mini-item-main { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.mini-item small { font-size: 12px; }
</style>
