<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NModal, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const router = useRouter()
const message = useMessage()

const cart = ref<any>({ items: [], count: 0, currency: 'CNY', items_total_cents: 0, setup_total_cents: 0, total_cents: 0, payable: true })
const loading = ref(false)
const busyItem = ref('')
const clearing = ref(false)
const couponCode = ref('')
const voucherCode = ref('')
const checkingOut = ref(false)
const paying = ref(false)
const showCheckout = ref(false)
const checkout = ref<any | null>(null)

const cycleText = (v: string) => ({ monthly: '月付', quarterly: '季付', semiannually: '半年付', yearly: '年付' } as any)[v] || v
const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`

async function load() {
  loading.value = true
  try { cart.value = dataOf(await api.get('/cart')) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取购物车失败') }
  finally { loading.value = false }
}

// 改数量复用加购接口：同（商品, 周期, 币种）是覆盖语义，配置与自定义字段原样回传。
async function setQty(item: any, qty: number) {
  const n = Math.max(1, Math.min(100, Math.floor(qty || 1)))
  if (n === item.quantity || busyItem.value) return
  busyItem.value = item.id
  try {
    cart.value = dataOf(await api.post('/cart/items', {
      product_id: item.product_id, billing_cycle: item.billing_cycle, quantity: n,
      currency: item.currency, config: item.config || [], custom_fields: item.custom_fields || {},
    }))
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '修改数量失败') }
  finally { busyItem.value = '' }
}

async function removeItem(item: any) {
  if (busyItem.value) return
  busyItem.value = item.id
  try { cart.value = dataOf(await api.delete(`/cart/items/${item.id}`)) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '移除失败') }
  finally { busyItem.value = '' }
}

async function clearCart() {
  clearing.value = true
  try { await api.delete('/cart'); await load(); message.success('购物车已清空') }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '清空失败') }
  finally { clearing.value = false }
}

// 结算只生成待付款订单（整批一个 checkout group），不收钱。
async function doCheckout() {
  if (!cart.value.count || !cart.value.payable) return
  checkingOut.value = true
  try {
    checkout.value = dataOf<any>(await api.post('/cart/checkout', {
      coupon_code: couponCode.value.trim() || undefined,
      voucher_code: voucherCode.value.trim() || undefined,
    }))
    showCheckout.value = true
    couponCode.value = ''
    voucherCode.value = ''
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '结算失败') }
  finally { checkingOut.value = false }
}

// 余额一次性支付整批（幂等键防重试重复扣款）；余额不足引导去钱包充值。
async function payWithWallet() {
  if (!checkout.value?.checkout_id) return
  paying.value = true
  try {
    const idem = `cart-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
    const res = dataOf<any>(await api.post(`/checkouts/${checkout.value.checkout_id}/pay`, {}, { headers: { 'Idempotency-Key': idem } }))
    message.success(`支付成功，${res.order_ids?.length || 0} 笔订单已进入开通流程`)
    showCheckout.value = false
    router.push('/services')
  } catch (e: any) {
    if (e?.response?.data?.error?.code === 'INSUFFICIENT_BALANCE') {
      message.warning('余额不足，已为你打开钱包页面')
      showCheckout.value = false
      router.push('/wallet')
      return
    }
    message.error(e?.response?.data?.error?.message || '支付失败')
  } finally { paying.value = false }
}

const payableText = computed(() => cart.value.payable ? '结算（生成待付款订单）' : '存在不可售商品，请先移除')

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">产品中心</div>
        <h1>购物车</h1>
        <p>价格每次打开都按最新定价重算；结算生成待付款订单，余额可整批一次支付，也可以到订单页逐笔支付。</p>
      </div>
      <router-link to="/products" class="soft-action">＋ 继续选购</router-link>
    </div>

    <div v-if="loading" class="empty-box">正在加载购物车…</div>
    <div v-else-if="!cart.count" class="empty-box">购物车还是空的，去产品中心挑选一个套餐吧。</div>

    <div v-else class="stack">
      <div class="card row cart-line" v-for="item in cart.items" :key="item.id" :class="{ off: !item.available }">
        <div class="cart-line-main">
          <b>{{ item.product_name }}</b>
          <div class="muted cart-line-sub">
            {{ cycleText(item.billing_cycle) }} · {{ money(item.unit_cents, item.currency) }}/{{ cycleText(item.billing_cycle) }}
            <s v-if="item.list_cents > item.unit_cents">{{ money(item.list_cents, item.currency) }}</s>
          </div>
          <div class="muted cart-line-sub" v-if="item.config_cents > 0 || item.setup_cents > 0">
            <span v-if="item.config_cents > 0">配置加价 {{ money(item.config_cents, item.currency) }}/件</span>
            <span v-if="item.setup_cents > 0"> · 一次性初装费 {{ money(item.setup_cents, item.currency) }}</span>
          </div>
          <div class="cart-line-bad" v-if="!item.available">{{ item.reason || '当前不可售' }}</div>
        </div>
        <div class="cart-line-actions">
          <NButton size="small" :disabled="item.quantity <= 1 || !!busyItem || !item.available" @click="setQty(item, item.quantity - 1)">－</NButton>
          <span class="cart-qty">{{ item.quantity }}</span>
          <NButton size="small" :disabled="item.quantity >= 100 || !!busyItem || !item.available" @click="setQty(item, item.quantity + 1)">＋</NButton>
          <strong class="cart-subtotal">{{ money(item.subtotal_cents, item.currency) }}</strong>
          <NButton size="small" secondary type="error" :loading="busyItem === item.id" @click="removeItem(item)">移除</NButton>
        </div>
      </div>

      <div class="card">
        <div class="row" style="gap:10px;flex-wrap:wrap">
          <input v-model="couponCode" class="catalog-search" style="flex:1;min-width:200px" placeholder="优惠码（可选，结算时核销）" />
          <input v-model="voucherCode" class="catalog-search" style="flex:1;min-width:200px" placeholder="代金券码（可选，结算时核销）" />
          <NButton secondary :loading="clearing" @click="clearCart">清空购物车</NButton>
        </div>
        <div class="cart-total">
          <span>
            共 {{ cart.count }} 项，小计 {{ money(cart.items_total_cents, cart.currency) }}
            <template v-if="cart.setup_total_cents > 0">（含一次性初装费 {{ money(cart.setup_total_cents, cart.currency) }}）</template>
          </span>
          <strong>{{ money(cart.total_cents, cart.currency) }}</strong>
        </div>
        <NButton block type="primary" size="large" style="margin-top:12px" :loading="checkingOut" :disabled="!cart.payable || !cart.count" @click="doCheckout">{{ payableText }}</NButton>
      </div>
    </div>

    <NModal v-model:show="showCheckout" preset="card" title="结算完成" style="width:min(460px,92vw)">
      <p class="muted" style="margin-top:0">
        已生成 {{ checkout?.order_ids?.length || 0 }} 笔待付款订单，合计 {{ money(checkout?.total_cents || 0, checkout?.currency) }}。
        付款成功后自动进入开通流程。
      </p>
      <NButton block type="primary" size="large" :loading="paying" @click="payWithWallet">用余额一次支付</NButton>
      <NButton block secondary size="large" style="margin-top:10px" @click="showCheckout = false; router.push('/orders')">去订单页逐笔支付</NButton>
    </NModal>
  </div>
</template>

<style scoped>
.cart-line { align-items: center; flex-wrap: wrap; }
.cart-line.off { opacity: .62; }
.cart-line-main { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.cart-line-sub s { margin-left: 6px; }
.cart-line-bad { color: #cf3030; font-size: 12px; margin-top: 3px; }
.cart-line-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.cart-qty { min-width: 28px; text-align: center; }
.cart-subtotal { min-width: 88px; text-align: right; font-variant-numeric: tabular-nums; }
.cart-total { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; padding: 10px 12px; margin-top: 12px; background: color-mix(in srgb, var(--background) 45%, var(--panel)); border: 1px solid var(--border); border-radius: 10px; }
.cart-total strong { font-size: 18px; font-variant-numeric: tabular-nums; }
</style>
