<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NModal, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 流量包（对齐魔方 CBAP FlowPacket 插件）。
// 选择名下适用产品下单，余额支付；余额不足时订单保留为未付款，可充值后再支付。

const message = useMessage()
const packets = ref<any[]>([])
const orders = ref<any[]>([])
const wallet = ref<any | null>(null)
const busy = ref(false)

const buyOpen = ref(false)
const buyPacket = ref<any | null>(null)
const buyService = ref('')
const buying = ref(false)

const statusText: Record<string, string> = { unpaid: '未付款', paid: '已付款', cancelled: '已取消', refunded: '已退款' }
const statusType = (s: string) => ({ paid: 'success', unpaid: 'warning' } as any)[s] || 'default'
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const serviceOptions = computed(() => (buyPacket.value?.services || []).map((s: any) => ({ label: s.name, value: s.id })))
const soldOut = (p: any) => p.stock_enable && Number(p.stock || 0) <= 0

async function load() {
  busy.value = true
  try {
    packets.value = dataOf<any>(await api.get('/flow-packets'))?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取流量包失败')
  } finally {
    busy.value = false
  }
  await Promise.all([loadOrders(), loadWallet()])
}
async function loadOrders() {
  try {
    orders.value = dataOf<any>(await api.get('/flow-packets/orders')) || []
  } catch {
    orders.value = []
  }
}
async function loadWallet() {
  try {
    wallet.value = dataOf<any>(await api.get('/wallet'))
  } catch {
    wallet.value = null
  }
}

function openBuy(p: any) {
  if (soldOut(p)) { message.warning('该流量包已售罄'); return }
  if (!(p.services || []).length) { message.warning('你名下没有适用于该流量包的产品'); return }
  buyPacket.value = p
  buyService.value = p.services[0].id
  buyOpen.value = true
}
async function confirmBuy() {
  if (!buyService.value) { message.error('请选择要购买的产品'); return }
  buying.value = true
  try {
    const order = dataOf<any>(await api.post(`/flow-packets/${buyPacket.value.public_id}/purchase`, { service_id: buyService.value }))
    try {
      const paid = dataOf<any>(await api.post(`/flow-packets/orders/${order.public_id}/pay`))
      message.success(`购买成功：${paid.capacity_gb}GB 流量包已支付 ${money(paid.amount_cents)}`)
    } catch (e: any) {
      if (e?.response?.status === 402) message.warning('订单已创建，余额不足；请先充值，再到下方「我的流量包订单」中支付')
      else message.warning(e?.response?.data?.error?.message || '订单已创建，请在下方完成支付')
    }
    buyOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '购买失败')
  } finally {
    buying.value = false
  }
}
async function payOrder(o: any) {
  try {
    const paid = dataOf<any>(await api.post(`/flow-packets/orders/${o.public_id}/pay`))
    message.success(`支付成功：${money(paid.amount_cents)}`)
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '支付失败')
  }
}
async function cancelOrder(o: any) {
  try {
    await api.post(`/flow-packets/orders/${o.public_id}/cancel`)
    message.success('订单已取消')
    await loadOrders()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '取消失败')
  }
}
onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">增值服务</div>
        <h1>流量包</h1>
        <p>为名下产品购买流量包，余额支付；余额不足时可先创建订单，充值后再支付。</p>
      </div>
      <span class="muted" v-if="wallet">余额：{{ money(wallet.balance_cents) }}</span>
    </div>

    <div v-if="!packets.length" class="empty-box">{{ busy ? '加载中…' : '暂时没有可购买的流量包。' }}</div>
    <div class="stack">
      <div class="card" v-for="p in packets" :key="p.public_id" style="display:flex;justify-content:space-between;gap:12px;align-items:center">
        <div>
          <b>{{ p.name }}</b>
          <span class="muted"> · {{ p.capacity_gb }}GB · {{ money(p.price_cents) }}</span>
          <div class="muted" style="font-size:12px">
            适用商品：{{ (p.products || []).map((x: any) => x.name).join(' / ') || '—' }}
            <template v-if="p.notes"> · {{ p.notes }}</template>
            <template v-if="p.stock_enable"> · 剩余库存 {{ p.stock }}</template>
          </div>
        </div>
        <div class="row" style="gap:8px;align-items:center">
          <NTag v-if="soldOut(p)" type="default" size="small" round>已售罄</NTag>
          <NTag v-else-if="!(p.services || []).length" type="default" size="small" round>暂不适用</NTag>
          <NButton type="primary" :disabled="soldOut(p) || !(p.services || []).length" @click="openBuy(p)">购买</NButton>
        </div>
      </div>
    </div>

    <h2 style="margin-top:26px">我的流量包订单</h2>
    <div v-if="!orders.length" class="empty-box">还没有流量包订单。</div>
    <div v-else class="table-scroll"><div class="order-table">
      <div class="order-row user-head">
        <span>流量包</span><span>关联产品</span><span>金额</span><span>状态</span><span>下单时间</span><span>操作</span>
      </div>
      <div v-for="o in orders" :key="o.public_id" class="order-row">
        <span><b>{{ o.packet_name }}</b><small class="muted"> {{ o.capacity_gb }}GB</small></span>
        <span class="muted">{{ o.service_name }}<small>（{{ String(o.service_id || '').slice(0, 8) }}）</small></span>
        <span>{{ money(o.amount_cents) }}</span>
        <span><NTag :type="statusType(o.status)" size="tiny" round>{{ statusText[o.status] || o.status }}</NTag></span>
        <span class="muted">{{ fmt(o.created_at) }}</span>
        <span class="row" style="gap:6px" v-if="o.status === 'unpaid'">
          <NButton size="tiny" type="primary" @click="payOrder(o)">支付</NButton>
          <NButton size="tiny" tertiary @click="cancelOrder(o)">取消</NButton>
        </span>
        <span v-else class="muted">—</span>
      </div>
    </div></div>

    <NModal v-model:show="buyOpen" preset="card" title="购买流量包" style="width:min(520px,94vw)">
      <div class="stack" v-if="buyPacket">
        <p class="muted" style="font-size:13px;margin:0">
          {{ buyPacket.name }} · {{ buyPacket.capacity_gb }}GB · {{ money(buyPacket.price_cents) }}
          <template v-if="wallet">（当前余额 {{ money(wallet.balance_cents) }}）</template>
        </p>
        <div>
          <b class="muted" style="font-size:12px">选择要充值的产品</b>
          <NSelect v-model:value="buyService" :options="serviceOptions" placeholder="选择产品" style="margin-top:6px" />
        </div>
        <div class="row" style="gap:8px">
          <NButton type="primary" :loading="buying" @click="confirmBuy">确认购买并支付</NButton>
          <NButton secondary @click="buyOpen = false">取消</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.order-row { display: grid; grid-template-columns: minmax(140px, 1fr) minmax(150px, 1.2fr) 100px 90px 150px 120px; gap: 8px; align-items: center; padding: 8px 10px; }
</style>
