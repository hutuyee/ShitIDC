<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { darkTheme, NBadge, NButton, NConfigProvider, NDialogProvider, NGlobalStyle, NLoadingBarProvider, NMessageProvider, NModalProvider, NNotificationProvider, NPopover } from 'naive-ui'
import BrandMark from './components/BrandMark.vue'
import { api, dataOf } from './api'
import { useAuthStore } from './stores/auth'
import { ADMIN_PATH } from './adminPath'
import { applyTheme, currentTheme, themeState } from './theme'

// ---- notification center (站内通知) ----
const notifications = ref<any[]>([])
const unread = ref(0)
async function loadNotifications() {
  try {
    const d = dataOf<{ items: any[]; unread: number }>(await api.get('/notifications'))
    notifications.value = d.items || []
    unread.value = d.unread || 0
  } catch { /* ignore */ }
}
async function markRead(id: string) {
  try { await api.post(`/notifications/${id}/read`) } catch { /* ignore */ }
  loadNotifications()
}
onMounted(() => { if (auth.user) loadNotifications() })

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const isAdmin = computed(() => Boolean(
  auth.permissions['ticket.manage'] || auth.permissions['product.write'] ||
  auth.permissions['wallet.adjust'] || auth.permissions['provider.manage'] ||
  auth.permissions['user.read'] || auth.permissions['security.audit.read'] ||
  auth.permissions['service.manage'] || auth.permissions['webhook.manage'] ||
  auth.permissions['announcement.manage'] || auth.permissions['coupon.manage'] ||
  auth.permissions['agent.manage'] || auth.permissions['extension.manage'] ||
  auth.permissions['theme.manage'] || auth.permissions['finance.report']
))
const adminLayout = computed(() => route.path === ADMIN_PATH || route.path.startsWith(ADMIN_PATH + '/'))
// Naive UI 组件主题跟随 CSS 变量主题（dark 主题下弹窗/表格/下拉不再保持浅色）
const naiveTheme = computed(() => (themeState.value === 'dark' ? darkTheme : null))
const nickname = computed(() => auth.user?.email?.split('@')[0] || '用户')
const avatar = computed(() => (nickname.value.trim()[0] || 'S').toUpperCase())

async function logout() {
  await auth.logout()
  router.push('/login')
}

function toggleTheme() {
  applyTheme(currentTheme() === 'dark' ? 'default' : 'dark')
}
</script>

<template>
  <NConfigProvider :theme="naiveTheme" :inline-theme-disabled="false">
    <NGlobalStyle />
    <NMessageProvider>
    <NDialogProvider>
    <NNotificationProvider>
    <NLoadingBarProvider>
    <NModalProvider>
      <template v-if="auth.user">
        <div v-if="!adminLayout" class="client-shell">
          <header class="topbar">
            <div class="topbar-inner">
              <BrandMark />
              <nav class="topnav" aria-label="用户中心导航">
                <router-link to="/">总览</router-link>
                <router-link to="/products">产品</router-link>
                <router-link to="/cart">购物车</router-link>
                <router-link to="/orders">订单</router-link>
                <router-link to="/wallet">财务</router-link>
                <router-link to="/invoice">发票</router-link>
                <router-link to="/vouchers">代金券</router-link>
                <router-link to="/services">服务</router-link>
                <router-link to="/flow-packets">流量包</router-link>
                <router-link to="/messages">消息</router-link>
                <router-link to="/tickets">工单</router-link>
                <router-link to="/referral">推广</router-link>
                <router-link to="/profile">账户</router-link>
              </nav>
              <div class="topbar-actions">
                <router-link to="/cart" class="icon-button" title="购物车">🛒</router-link>
                <router-link to="/tickets" class="icon-button" title="工单">🔔</router-link>
                <button class="icon-button plain-button" title="切换主题" @click="toggleTheme">◐</button>
                <router-link v-if="isAdmin" :to="ADMIN_PATH" class="admin-entry">管理后台</router-link>
                <div class="user-chip">
                  <span class="user-avatar">{{ avatar }}</span>
                  <span class="user-chip-text"><b>{{ nickname }}</b><small>{{ auth.user.email }}</small></span>
                </div>
                <button class="logout-link" @click="logout">退出</button>
              </div>
            </div>
          </header>
          <main class="client-content"><router-view /></main>
        </div>

        <div v-else class="admin-shell">
          <aside class="admin-sidebar">
            <BrandMark />
            <div class="admin-label">管理后台</div>
            <router-link :to="ADMIN_PATH">控制台</router-link>
            <router-link v-if="auth.permissions['ticket.manage']" :to="ADMIN_PATH + '/tickets'">工单客服</router-link>
            <router-link v-if="auth.permissions['ticket.manage']" :to="ADMIN_PATH + '/tickets/settings'">工单配置</router-link>
            <router-link v-if="auth.permissions['ticket.manage']" :to="ADMIN_PATH + '/tickets/stats'">工单统计</router-link>
            <router-link v-if="auth.permissions['ticket_internal.manage']" :to="ADMIN_PATH + '/ticket-internal'">内部工单</router-link>
            <router-link v-if="auth.permissions['user.read']" :to="ADMIN_PATH + '/users'">用户列表</router-link>
            <router-link v-if="auth.permissions['user.read']" :to="ADMIN_PATH + '/client-fields'">用户字段</router-link>
            <router-link v-if="auth.permissions['user.manage']" :to="ADMIN_PATH + '/certifications'">实名审核</router-link>
            <router-link v-if="auth.permissions['service.manage']" :to="ADMIN_PATH + '/services'">服务管理</router-link>
            <router-link v-if="auth.permissions['service.manage']" :to="ADMIN_PATH + '/service-transfers'">产品转移</router-link>
            <router-link v-if="auth.permissions['flow_packet.manage']" :to="ADMIN_PATH + '/flow-packets'">流量包</router-link>
            <router-link v-if="auth.permissions['client_care.manage']" :to="ADMIN_PATH + '/client-care'">客户关怀</router-link>
            <router-link v-if="auth.permissions['finance.report']" :to="ADMIN_PATH + '/stats'">财务统计</router-link>
            <router-link v-if="auth.permissions['order.read']" :to="ADMIN_PATH + '/orders'">订单中心</router-link>
            <router-link v-if="auth.permissions['coupon.manage']" :to="ADMIN_PATH + '/coupons'">优惠券</router-link>
            <router-link v-if="auth.permissions['voucher.manage']" :to="ADMIN_PATH + '/vouchers'">代金券</router-link>
            <router-link v-if="auth.permissions['promotion.manage']" :to="ADMIN_PATH + '/promotions'">活动促销</router-link>
            <router-link v-if="auth.permissions['cycle_order.manage']" :to="ADMIN_PATH + '/cycle-orders'">周期人工订单</router-link>
            <router-link v-if="auth.permissions['invoice.manage']" :to="ADMIN_PATH + '/invoices'">发票管理</router-link>
            <router-link v-if="auth.permissions['email_notice.manage']" :to="ADMIN_PATH + '/email-notice'">邮件通知</router-link>
            <router-link v-if="auth.permissions['product.write']" :to="ADMIN_PATH + '/cashbacks'">商品返现</router-link>
            <router-link v-if="auth.permissions['wallet.adjust']" :to="ADMIN_PATH + '/recommend'">推介计划</router-link>
            <router-link v-if="auth.permissions['wallet.adjust']" :to="ADMIN_PATH + '/recommend/config'">推介配置</router-link>
            <router-link v-if="auth.permissions['finance.report']" :to="ADMIN_PATH + '/order-costs'">成本支出</router-link>
            <router-link v-if="auth.permissions['agent.manage']" :to="ADMIN_PATH + '/agents'">代理分组</router-link>
            <router-link v-if="auth.permissions['extension.manage']" :to="ADMIN_PATH + '/ext-themes'">扩展 / 主题</router-link>
            <router-link v-if="auth.permissions['product.write']" :to="ADMIN_PATH + '/products'">商品与分组</router-link>
            <router-link v-if="auth.permissions['product.write']" :to="ADMIN_PATH + '/product-limits'">商品限购</router-link>
            <router-link v-if="auth.permissions['announcement.manage']" :to="ADMIN_PATH + '/announcements'">站内公告</router-link>
            <router-link v-if="auth.permissions['security.audit.read']" :to="ADMIN_PATH + '/logs'">日志中心</router-link>
            <router-link v-if="auth.permissions['wallet.adjust']" :to="ADMIN_PATH + '/payments'">支付方式</router-link>
            <router-link v-if="auth.permissions['webhook.manage']" :to="ADMIN_PATH + '/webhooks'">Webhook 推送</router-link>
            <router-link v-if="auth.permissions['finance.report']" :to="ADMIN_PATH + '/export'">导出中心</router-link>
            <router-link v-if="auth.permissions['service.manage']" :to="ADMIN_PATH + '/expired-ip-logs'">到期IP记录</router-link>
            <router-link v-if="auth.permissions['service.manage']" :to="ADMIN_PATH + '/inspection-records'">异常巡查</router-link>
            <router-link v-if="auth.permissions['provider.manage']" :to="ADMIN_PATH + '/settings'">邮件 / SMTP</router-link>
            <router-link v-if="auth.permissions['provider.manage']" :to="ADMIN_PATH + '/providers'">供应商 / 上游</router-link>
            <div class="grow"></div>
            <router-link to="/">← 返回用户中心</router-link>
            <NButton secondary @click="toggleTheme">切换主题</NButton>
            <div class="email">{{ auth.user.email }}</div>
            <NButton secondary @click="logout">退出</NButton>
          </aside>
          <main class="admin-content"><router-view /></main>
        </div>
      </template>
      <router-view v-else />
    </NModalProvider>
    </NLoadingBarProvider>
    </NNotificationProvider>
    </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>

<style>
.notif-pop { display: flex; flex-direction: column; gap: 6px; }
.notif-item { display: flex; flex-direction: column; gap: 2px; padding: 8px 10px; border-radius: 8px; cursor: pointer; }
.notif-item:hover { background: rgba(128, 128, 128, .12); }
.notif-item.unread { border-left: 3px solid var(--primary, #4f46e5); }
.notif-item small { font-size: 12px; }
.bell-badge .n-badge-sup { pointer-events: none; }
</style>
