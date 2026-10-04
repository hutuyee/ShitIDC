<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInputNumber, NModal, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const orders = ref<any[]>([])
const methods = ref<any[]>([])
const message = useMessage()
const loaded = ref(false)

const payModal = ref(false)
const payOrder = ref<any>(null)
const payChoice = ref<string | null>(null)
const busy = ref(false)

const payOptions = computed(() => {
  const opts: { label: string; value: string }[] = []
  const label: Record<string, string> = { alipay: '支付宝', wxpay: '微信支付', qqpay: 'QQ 钱包', bank: '网银', jiedebao: '捷德宝', paypal: 'PayPal', usdt: 'USDT', epay: '余额/通用' }
  for (const m of methods.value) {
    for (const t of m.pay_types || []) opts.push({ label: `${m.name} · ${label[t] || t}`, value: `${m.id}|${t}` })
  }
  return opts
})

async function load() {
  try {
    const [o, m] = await Promise.all([
      dataOf<any>(await api.get('/orders')),
      api.get('/payment-methods').then(r => dataOf<any>(r)).catch(() => [] as any[]),
    ])
    orders.value = o || []
    methods.value = m || []
  } finally {
    loaded.value = true
  }
}

async function payWallet(o: any) {
  try {
    await api.post(`/orders/${o.id}/pay`, {}, { headers: { 'Idempotency-Key': crypto.randomUUID() } })
    message.success('支付成功，正在开通服务')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '支付失败')
  }
}

function openOnlinePay(o: any) {
  payOrder.value = o
  payChoice.value = payOptions.value[0]?.value || null
  payModal.value = true
}

async function submitOnlinePay() {
  if (!payOrder.value || !payChoice.value) return
  const [provider, payType] = payChoice.value.split('|')
  busy.value = true
  try {
    const r = dataOf<{ pay_url: string }>(await api.post(`/orders/${payOrder.value.id}/pay/online`, { provider_id: provider, pay_type: payType }))
    if (r?.pay_url) window.location.href = r.pay_url
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '创建支付失败')
  } finally {
    busy.value = false
  }
}

async function cancelOrder(o: any) {
  try {
    await api.post(`/orders/${o.id}/cancel`)
    message.success('订单已取消')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '取消失败')
  }
}

const statusText: Record<string, string> = {
  unpaid: '待支付', paid: '已支付', processing: '开通中', completed: '已完成', cancelled: '已取消', refunded: '已退款', pending: '待处理',
}
const statusType = (s: string) => ({ unpaid: 'warning', processing: 'info', paid: 'info', completed: 'success', cancelled: 'default', refunded: 'error', pending: 'default' } as any)[s] || 'default'
const payTypeLabel: Record<string, string> = { alipay: '支付宝', wxpay: '微信支付', qqpay: 'QQ钱包' }
const methodText = (o: any) => {
  if (!o.payment) return ''
  const base = ({ wallet: '余额支付', epay: '在线支付' } as Record<string, string>)[o.payment.method] || o.payment.method
  const t = o.payment.type ? '（' + (payTypeLabel[o.payment.type] || o.payment.type) + '）' : ''
  return base + t
}
const cycleText = (v: string) => ({ monthly: '月付', quarterly: '季付', semiannually: '半年付', yearly: '年付' } as any)[v] || v
const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const shortID = (id: string) => id?.slice(0, 8)

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading"><div><div class="eyebrow">订单中心</div><h1>我的订单</h1><p>包含购买的商品、配置、金额与支付方式；未支付订单可取消，超过 24 小时未支付会自动关闭。</p></div><router-link to="/products" class="soft-action">＋ 新购产品</router-link></div>

    <div v-if="loaded && !orders.length" class="empty-box">还没有订单，去产品中心挑选一个套餐吧。</div>

    <div class="stack">
      <article v-for="o in orders" :key="o.id" class="panel order-card">
        <header class="order-head">
          <div class="order-head-left">
            <b>订单 #{{ shortID(o.id) }}</b>
            <small class="muted">下单时间：{{ new Date(o.created_at).toLocaleString() }}</small>
          </div>
          <div class="order-head-right">
            <NTag :type="statusType(o.status)" size="small" round>{{ statusText[o.status] || o.status }}</NTag>
            <span class="money order-total">{{ money(o.total_cents, o.currency) }}</span>
          </div>
        </header>

        <div class="order-items">
          <div v-for="(item, i) in o.items || []" :key="i" class="order-item">
            <div class="order-item-main">
              <b>{{ item.product_name }}</b>
              <small class="muted">{{ cycleText(item.billing_cycle) }} · 数量 ×{{ item.quantity }}</small>
            </div>
            <span class="muted order-item-price">{{ money(item.unit_price_cents, o.currency) }} × {{ item.quantity }}</span>
            <strong>{{ money(item.subtotal_cents, o.currency) }}</strong>
          </div>
          <div v-if="!(o.items || []).length" class="muted order-item">（无商品明细）</div>
        </div>

        <footer class="order-foot">
          <small class="muted">
            <template v-if="o.payment">支付方式：{{ methodText(o) }}<template v-if="o.payment.paid_at"> · 支付于 {{ new Date(o.payment.paid_at).toLocaleString() }}</template></template>
            <template v-else-if="o.status === 'unpaid'">未支付 · 请尽快完成支付</template>
            <template v-else-if="o.status === 'cancelled' && o.cancelled_at">取消时间：{{ new Date(o.cancelled_at).toLocaleString() }}</template>
          </small>
          <div class="order-actions">
            <NButton v-if="o.status === 'unpaid'" size="small" type="primary" @click="payWallet(o)">余额支付</NButton>
            <NButton v-if="o.status === 'unpaid' && payOptions.length" size="small" type="primary" secondary @click="openOnlinePay(o)">在线支付</NButton>
            <NButton v-if="o.status === 'unpaid'" size="small" quaternary type="error" @click="cancelOrder(o)">取消订单</NButton>
          </div>
        </footer>
      </article>
    </div>

    <NModal v-model:show="payModal" preset="card" title="在线支付" style="width:min(420px,92vw)">
      <div class="stack">
        <div v-if="payOrder" class="muted">订单金额：<b class="money">{{ money(payOrder.total_cents, payOrder.currency) }}</b></div>
        <NSelect v-model:value="payChoice" :options="payOptions" placeholder="选择支付渠道" />
        <div class="security-note">点击确认后会跳转到支付平台完成付款，支付完成后自动返回本站并开通服务。</div>
        <NButton type="primary" block :loading="busy" :disabled="!payChoice" @click="submitOnlinePay">跳转支付</NButton>
      </div>
    </NModal>
  </div>
</template>
