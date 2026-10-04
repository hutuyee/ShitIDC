<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NModal, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const orders = ref<any[]>([])
const refunds = ref<any[]>([])
const loading = ref(false)

const statusType = (s: string) => ({ completed: 'success', processing: 'info', paid: 'info', unpaid: 'warning', cancelled: 'default', refunded: 'info' } as any)[s] || 'default'
const statusText = (s: string) => ({ completed: '已完成', processing: '开通中', paid: '已支付', unpaid: '未支付', cancelled: '已取消', refunded: '已退款' } as any)[s] || s
const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')

async function load() {
  loading.value = true
  try {
    const [o, r] = await Promise.all([
      api.get('/admin/orders', { params: { limit: 200 } }).then(x => dataOf<any[]>(x)),
      api.get('/admin/refunds', { params: { limit: 100 } }).then(x => dataOf<any[]>(x)).catch(() => [])
    ])
    orders.value = o
    refunds.value = r
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取订单失败')
  } finally { loading.value = false }
}

// ---- refund dialog ----
const refundOpen = ref(false)
const refundOrder = ref<any>(null)
const refundReason = ref('')
const refundBusy = ref(false)

function openRefund(o: any) {
  refundOrder.value = o
  refundReason.value = ''
  refundOpen.value = true
}

async function doRefund() {
  if (!refundReason.value.trim()) { message.error('必须填写退款原因'); return }
  refundBusy.value = true
  try {
    await api.post(`/admin/orders/${refundOrder.value.id}/refund`, { reason: refundReason.value.trim() })
    message.success('退款已以冲正交易入账')
    refundOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '退款失败')
  } finally { refundBusy.value = false }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">订单中心</div><h1>全部订单与退款</h1><p>全站订单流水；钱包支付且已完成的订单可以按冲正交易方式退款，原始资金流水永不修改。</p></div><NButton :loading="loading" secondary @click="load">刷新</NButton></div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>订单列表</h2><span>最近 {{ orders.length }} 条</span></div></div>
      <div v-if="orders.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>订单号</span><span>用户</span><span>内容</span><span>金额</span><span>状态</span><span>支付方式</span><span>时间</span><span>操作</span></div>
        <div v-for="o in orders" :key="o.id" class="audit-row">
          <span class="mono">{{ o.id.slice(0, 8) }}</span>
          <span class="muted">UID {{ o.user_uid }}</span>
          <span>{{ (o.items || []).map((x: any) => `${x.product_name}×${x.quantity}`).join('、') || '—' }}</span>
          <span><b>{{ money(o.total_cents, o.currency) }}</b></span>
          <span><NTag :type="statusType(o.status)" size="small" round>{{ statusText(o.status) || o.status }}</NTag></span>
          <span class="muted">{{ o.payment ? `${o.payment.method}${o.payment.type ? ' / ' + o.payment.type : ''}` : (o.status === 'unpaid' ? '未支付' : '钱包') }}</span>
          <span class="muted">{{ fmt(o.created_at) }}</span>
          <span><NButton v-if="o.status === 'completed'" size="tiny" tertiary type="warning" @click="openRefund(o)">退款</NButton><span v-else class="muted">—</span></span>
        </div>
      </div></div>
      <div v-else class="empty-box">暂无订单。</div>
    </section>

    <section class="panel">
      <div class="panel-title-row"><div><h2>退款记录</h2><span>冲正交易账本</span></div></div>
      <div v-if="refunds.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>退款 ID</span><span>订单</span><span>原支付渠道</span><span>金额</span><span>原因</span><span>时间</span></div>
        <div v-for="r in refunds" :key="r.id" class="audit-row">
          <span class="mono">{{ r.id.slice(0, 8) }}</span>
          <span class="mono">{{ r.order_id.slice(0, 8) }}</span>
          <span>{{ r.method }}</span>
          <span><b>{{ money(r.amount_cents, r.currency) }}</b></span>
          <span class="muted">{{ r.reason }}</span>
          <span class="muted">{{ fmt(r.created_at) }}</span>
        </div>
      </div></div>
      <div v-else class="empty-box">暂无退款记录。</div>
    </section>

    <NModal v-model:show="refundOpen" preset="card" :title="`退款 ${refundOrder?.id?.slice(0, 8) || ''}`" style="width:min(440px,92vw)">
      <div class="stack" v-if="refundOrder">
        <p class="muted" style="margin:0">订单金额 <b>{{ money(refundOrder.total_cents, refundOrder.currency) }}</b>，全额退回用户钱包（冲正交易）。在线支付的订单需在支付平台后台同步操作原路退回。</p>
        <NInput v-model:value="refundReason" type="textarea" :rows="3" placeholder="退款原因（必填，会写入审计日志）" />
        <NButton type="warning" block :loading="refundBusy" @click="doRefund">确认退款</NButton>
      </div>
    </NModal>
  </div>
</template>
