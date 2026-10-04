<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, dataOf } from '../api'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const wallet = ref<any>({ balance_cents: 0, currency: 'CNY' })
const orders = ref<any[]>([])
const invoices = ref<any[]>([])
const services = ref<any[]>([])
const tickets = ref<any[]>([])
const transactions = ref<any[]>([])
const announcements = ref<any[]>([])
const loaded = ref(false)

async function safeGet(path: string, fallback: any) {
  try { const d = dataOf<any>(await api.get(path)); return d ?? fallback } catch { return fallback }
}

onMounted(async () => {
  const [w, o, i, s, t, tx, ann] = await Promise.all([
    safeGet('/wallet', { balance_cents: 0, currency: 'CNY' }),
    safeGet('/orders', []),
    safeGet('/invoices', []),
    safeGet('/services', []),
    safeGet('/tickets', []),
    safeGet('/wallet/transactions', []),
    safeGet('/announcements', [])
  ])
  wallet.value = w; orders.value = o; invoices.value = i; services.value = s; tickets.value = t; transactions.value = tx
  announcements.value = ann.slice(0, 5)
  loaded.value = true
})

// 首页公告只露一行摘要：完整内容在详情页，避免长公告把仪表板撑得很长。
function summaryOf(body: string) {
  const flat = String(body || '').replace(/\s+/g, ' ').trim()
  return flat.length > 60 ? flat.slice(0, 60) + '…' : flat
}

const fmtWhen = (v: string) => {
  const d = new Date(v); const diff = Date.now() - d.getTime()
  if (diff < 3600e3) return Math.max(1, Math.floor(diff / 60e3)) + ' 分钟前'
  if (diff < 86400e3) return Math.floor(diff / 3600e3) + ' 小时前'
  return d.toLocaleDateString()
}

const nickname = computed(() => auth.user?.email?.split('@')[0] || '用户')
const userInitial = computed(() => (nickname.value[0] || 'S').toUpperCase())
const unpaidInvoices = computed(() => invoices.value.filter(x => x.status === 'unpaid'))
const activeServices = computed(() => services.value.filter(x => x.status === 'active'))
const expiringServices = computed(() => {
  const limit = Date.now() + 30 * 24 * 3600 * 1000
  return services.value.filter(x => x.expires_at && new Date(x.expires_at).getTime() <= limit && new Date(x.expires_at).getTime() >= Date.now())
})
const openTickets = computed(() => tickets.value.filter(x => x.status !== 'closed'))
const monthSpend = computed(() => {
  const now = new Date()
  return transactions.value.filter(x => {
    const d = new Date(x.created_at)
    return d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && x.amount_cents < 0
  }).reduce((sum, x) => sum + Math.abs(Number(x.amount_cents || 0)), 0)
})
const money = (cents: number, currency = wallet.value.currency || 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const recentOrders = computed(() => orders.value.slice(0, 4))
const displayUserID = computed(() => auth.user?.uid ? String(auth.user.uid) : (auth.user?.id ? auth.user.id.slice(0, 8) : '—'))

const chartPoints = computed(() => {
  const values: number[] = []
  for (let offset = 6; offset >= 0; offset--) {
    const d = new Date(); d.setHours(0, 0, 0, 0); d.setDate(d.getDate() - offset)
    const next = new Date(d); next.setDate(next.getDate() + 1)
    const spent = transactions.value.filter(x => x.amount_cents < 0 && new Date(x.created_at).getTime() >= d.getTime() && new Date(x.created_at).getTime() < next.getTime())
      .reduce((sum, x) => sum + Math.abs(Number(x.amount_cents)), 0)
    values.push(spent)
  }
  const max = Math.max(...values, 1)
  return values.map((v, i) => `${(i / 6) * 100},${88 - (v / max) * 70}`).join(' ')
})
</script>

<template>
  <div class="dashboard-page">
    <div class="dashboard-heading">
      <div><div class="eyebrow">总览</div><h1>我的仪表板</h1><p>欢迎回来，{{ nickname }}。这里汇总你的资源、财务与支持状态。</p></div>
      <router-link to="/products" class="soft-action">＋ 添加产品</router-link>
    </div>

    <div class="dashboard-layout" :class="{ loading: !loaded }">
      <section class="dashboard-main">
        <div class="panel resource-panel">
          <div class="panel-title-row"><div><h2>资源概览</h2><span>当前账户下的服务状态</span></div><router-link to="/services">产品管理 →</router-link></div>
          <div class="resource-stats">
            <div><span class="resource-icon">◫</span><strong>{{ services.length }}</strong><small>全部服务</small></div>
            <div><span class="resource-icon good">✓</span><strong>{{ activeServices.length }}</strong><small>运行中</small></div>
            <div><span class="resource-icon warn">◷</span><strong>{{ expiringServices.length }}</strong><small>30 天内到期</small></div>
          </div>
          <router-link class="add-product-card" to="/products"><span>＋</span><div><b>添加产品</b><small>浏览套餐并创建新的服务</small></div></router-link>
        </div>

        <div class="panel">
          <div class="panel-title-row"><div><h2>快捷导航</h2><span>常用操作一键到达</span></div></div>
          <div class="quick-grid">
            <router-link to="/services"><span>▣</span><div><b>服务管理</b><small>查看实例、状态和到期时间</small></div></router-link>
            <router-link to="/tickets"><span>◉</span><div><b>提交工单</b><small>联系技术支持与售后</small></div></router-link>
            <router-link to="/wallet"><span>▤</span><div><b>财务中心</b><small>余额与交易流水</small></div></router-link>
            <router-link to="/tokens"><span>⌘</span><div><b>API 管理</b><small>创建和撤销访问凭证</small></div></router-link>
          </div>
        </div>

        <div class="panel">
          <div class="panel-title-row"><div><h2>公告</h2><span>平台动态与维护信息</span></div><router-link v-if="announcements.length" to="/announcements">查看全部（{{ announcements.length }}）→</router-link></div>
          <div v-if="announcements.length" class="notice-list">
            <!-- 每条都点得开：首页只做入口，正文在 /announcements/:id 里完整展示。 -->
            <router-link v-for="a in announcements" :key="a.id" :to="`/announcements/${a.id}`" class="notice-item">
              <span class="notice-tag">{{ a.pinned ? '置顶' : '系统公告' }}</span>
              <div class="notice-main">
                <b>{{ a.title }}</b>
                <small v-if="a.body">{{ summaryOf(a.body) }}</small>
              </div>
              <time>{{ fmtWhen(a.created_at) }}</time>
            </router-link>
          </div>
          <div v-else class="empty-box" style="margin:0">暂无公告。</div>
        </div>

        <div class="panel">
          <div class="panel-title-row"><div><h2>最近订单</h2><span>你的最新购买记录</span></div><router-link to="/orders">查看全部 →</router-link></div>
          <div v-if="recentOrders.length" class="mini-list">
            <div v-for="o in recentOrders" :key="o.id" class="mini-row"><span class="status-dot" :class="o.status"></span><div><b>#{{ o.id.slice(0, 8) }}</b><small>{{ new Date(o.created_at).toLocaleString() }}</small></div><span class="grow"></span><span>{{ o.status }}</span><strong>{{ money(o.total_cents, o.currency) }}</strong></div>
          </div>
          <div v-else class="empty-box">暂无订单，去产品中心看看可用套餐。</div>
        </div>
      </section>

      <aside class="dashboard-side">
        <div class="panel account-panel">
          <div class="profile-head"><span class="profile-avatar">{{ userInitial }}</span><div><h3>{{ nickname }}</h3><p>ID: {{ displayUserID }}</p></div><router-link to="/tokens">账户中心</router-link></div>
          <div class="profile-badges"><span class="badge amber">待实名认证</span><span class="badge green">已绑定邮箱</span></div>
          <div class="account-kpis">
            <div><span>待支付账单</span><strong>{{ unpaidInvoices.length }}</strong></div>
            <div><span>30天内到期资源</span><strong>{{ expiringServices.length }}</strong></div>
            <div><span>待处理工单</span><strong>{{ openTickets.length }}</strong></div>
          </div>
        </div>

        <div class="panel finance-panel">
          <div class="panel-title-row"><div><h2>费用信息</h2><span>账户资金概览</span></div><router-link to="/wallet">财务中心</router-link></div>
          <div class="finance-grid"><div><span>可用余额</span><strong>{{ money(wallet.balance_cents) }}</strong></div><div><span>本月消费</span><strong>{{ money(monthSpend) }}</strong></div><div><span>待支付</span><strong>{{ money(unpaidInvoices.reduce((s,x)=>s+Number(x.total_cents||0),0)) }}</strong></div><div><span>订单数量</span><strong>{{ orders.length }}</strong></div></div>
          <div class="chart-block"><div class="chart-title">近 7 天消费趋势</div><svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-label="近七天消费趋势"><line x1="0" y1="88" x2="100" y2="88" class="chart-axis"/><line x1="0" y1="53" x2="100" y2="53" class="chart-grid"/><line x1="0" y1="18" x2="100" y2="18" class="chart-grid"/><polyline :points="chartPoints" class="chart-line"/></svg><div class="chart-days"><span>6天前</span><span>3天前</span><span>今天</span></div></div>
        </div>
      </aside>
    </div>
  </div>
</template>
