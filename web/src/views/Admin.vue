<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'
import { useAuthStore } from '../stores/auth'
const auth = useAuthStore(); const message = useMessage()
const serviceID = ref('')
const refundOrderID = ref(''); const refundReason = ref('')

// 待办事项（对应魔方 widget/ToDo 插件）：按权限聚合各模块待处理数量。
const todos = ref<Record<string, number>>({})
async function loadTodos() {
  try { todos.value = dataOf<Record<string, number>>(await api.get('/admin/todos')) }
  catch { /* 没有任一待办模块权限时静默隐藏 */ }
}
const todoItems = computed(() => {
  const items: { key: string; label: string; count: number; to: string }[] = []
  const push = (key: string, label: string, to: string) => {
    if (todos.value[key] !== undefined) items.push({ key, label, count: todos.value[key], to })
  }
  push('pending_tickets', '待处理工单', ADMIN_PATH + '/tickets')
  push('pending_certifications', '待审实名认证', ADMIN_PATH + '/certifications')
  push('pending_services', '开通中产品', ADMIN_PATH + '/services')
  return items
})

async function retryService() { try { await api.post(`/admin/services/${serviceID.value}/retry`); message.success('已重新入队') } catch (e: any) { message.error(e?.response?.data?.error?.message || '失败') } }
async function refundOrder() {
  if (!refundOrderID.value.trim() || !refundReason.value.trim()) { message.error('订单 ID 与退款原因均必填'); return }
  try {
    const d = dataOf<any>(await api.post(`/admin/orders/${refundOrderID.value.trim()}/refund`, { reason: refundReason.value.trim() }))
    message.success(`已退款 ¥${((d.amount_cents || 0) / 100).toFixed(2)} 回用户余额（冲正流水）`)
    refundOrderID.value = ''; refundReason.value = ''
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '退款失败')
  }
}

onMounted(loadTodos)
</script>
<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">ShitIDC Admin</div><h1>管理控制台</h1><p>用户、订单、商品、公告、日志、支付与邮件设置均已独立成页；魔方上游请进入“供应商 / 上游”统一管理。</p></div></div>
    <section v-if="todoItems.length" class="panel">
      <div class="panel-title-row"><div><h2>待办事项</h2><span>对应魔方「待办事项」小工具（widget/ToDo），点击直达对应页面</span></div></div>
      <div class="todo-grid">
        <router-link v-for="t in todoItems" :key="t.key" :to="t.to" class="todo-card">
          <span class="todo-num">{{ t.count }}</span>
          <span class="todo-label">{{ t.label }}</span>
          <span class="todo-arrow">→</span>
        </router-link>
      </div>
    </section>
    <div class="admin-stat-grid">
      <div class="panel"><span class="admin-stat-icon">💬</span><b>工单客服</b><p>专职客服回复用户工单，无需开放整个管理后台。</p><router-link v-if="auth.permissions['ticket.manage']" :to="ADMIN_PATH + '/tickets'">进入工单台 →</router-link><span v-else class="muted" style="font-size:11px">需要 ticket.manage 权限</span></div>
      <div class="panel"><span class="admin-stat-icon">◉</span><b>用户管理</b><p>用户列表、UID 调账、余额查询。</p><router-link :to="ADMIN_PATH + '/users'">进入用户列表 →</router-link></div>
      <div class="panel"><span class="admin-stat-icon">▤</span><b>订单中心</b><p>全站订单流水、冲正退款与退款记录。</p><router-link :to="ADMIN_PATH + '/orders'">进入订单中心 →</router-link></div>
      <div class="panel"><span class="admin-stat-icon">▦</span><b>日志中心</b><p>审计日志、登录日志、API 请求日志三类留痕。</p><router-link :to="ADMIN_PATH + '/logs'">查看日志 →</router-link></div>
      <div class="panel"><span class="admin-stat-icon">¥</span><b>支付方式</b><p>支付渠道配置，在线支付与充值。</p><router-link :to="ADMIN_PATH + '/payments'">配置支付 →</router-link></div>
      <div class="panel"><span class="admin-stat-icon">✉</span><b>邮件 / SMTP</b><p>自定义 SMTP，注册邮箱验证码防爆破。</p><router-link :to="ADMIN_PATH + '/settings'">配置邮件 →</router-link></div>
      <div class="panel"><span class="admin-stat-icon">↗</span><b>供应商管理</b><p>保存上游网站和 API Key，测试连接后同步商品。</p><router-link :to="ADMIN_PATH + '/providers'">进入供应商 →</router-link></div>
      <div class="panel"><span class="admin-stat-icon">▣</span><b>商品与分组</b><p>商品创建、编辑、上下架与产品分组管理。</p><router-link :to="ADMIN_PATH + '/products'">进入商品管理 →</router-link></div>
      <div class="panel"><span class="admin-stat-icon">📣</span><b>站内公告</b><p>发布维护通知与平台动态到用户仪表板。</p><router-link :to="ADMIN_PATH + '/announcements'">管理公告 →</router-link></div>
    </div>
    <div class="admin-two-col">
      <section v-if="auth.permissions['service.manage']" class="panel stack admin-form-panel"><div class="panel-title-row"><div><h2>重试失败服务</h2><span>只用于已确认上游没有重复创建资源的场景</span></div></div><NInput v-model:value="serviceID" placeholder="Service UUID"/><div class="security-note">如果上游请求曾超时，必须先在上游确认没有已经创建资源，再执行重试，避免生成重复实例。</div><NButton type="warning" @click="retryService">重新入队</NButton></section>
      <section v-if="auth.permissions['wallet.adjust']" class="panel stack admin-form-panel"><div class="panel-title-row"><div><h2>订单退款（冲正）</h2><span>仅钱包支付的订单；在线支付请先在网关侧退款</span></div></div><NInput v-model:value="refundOrderID" placeholder="订单 UUID（可在订单中心复制）"/><NInput v-model:value="refundReason" placeholder="退款原因（写入审计日志）"/><div class="security-note">退款金额原路退回用户余额，以独立「refund」冲正流水记录，原始流水不会被修改；重复退款会被数据库幂等键拦截。</div><NButton type="error" secondary @click="refundOrder">确认退款</NButton></section>
    </div>
  </div>
</template>
