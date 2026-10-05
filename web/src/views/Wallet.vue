<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInputNumber, NModal, NSelect, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import { useRoute } from 'vue-router'

const wallet = ref<any>({ balance_cents: 0, currency: 'CNY' })
const txs = ref<any[]>([])
const methods = ref<any[]>([])
const message = useMessage()
const route = useRoute()

const rechargeOpen = ref(false)
const amount = ref<number | null>(null)
const choice = ref<string | null>(null)
const busy = ref(false)
const presets = [1000, 5000, 10000, 50000]

const label: Record<string, string> = { alipay: '支付宝', wxpay: '微信支付', qqpay: 'QQ 钱包', bank: '网银', jiedebao: '捷德宝', paypal: 'PayPal', usdt: 'USDT', epay: '通用' }
const payOptions = computed(() => {
  const opts: { label: string; value: string }[] = []
  for (const m of methods.value) {
    for (const t of m.pay_types || []) {
      if (t === 'manual') continue
      opts.push({ label: `${m.name} · ${label[t] || t}`, value: `${m.id}|${t}` })
    }
  }
  return opts
})

async function load() {
  try {
    const [w, t, m] = await Promise.all([
      dataOf<any>(await api.get('/wallet')),
      dataOf<any>(await api.get('/wallet/transactions')),
      api.get('/payment-methods').then(r => dataOf<any>(r)).catch(() => [] as any[]),
    ])
    wallet.value = w || { balance_cents: 0, currency: 'CNY' }
    txs.value = t || []
    methods.value = m || []
  } catch { /* wallet stays usable without payment methods */ }
}

function openRecharge() {
  amount.value = 5000
  choice.value = payOptions.value[0]?.value || null
  rechargeOpen.value = true
}

async function submitRecharge() {
  if (!amount.value || !choice.value) return
  const [provider, payType] = choice.value.split('|')
  busy.value = true
  try {
    const r = dataOf<{ pay_url: string }>(await api.post('/wallet/recharge', { amount_cents: Math.round(amount.value), provider_id: provider, pay_type: payType }))
    rechargeOpen.value = false
    if (r?.pay_url) window.location.href = r.pay_url
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '创建充值订单失败')
  } finally {
    busy.value = false
  }
}

const money = (cents: number, currency = wallet.value.currency || 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const txTypeText = (t: string) => ({ credit: '收入', debit: '支出' } as any)[t] || t
const refText = (t: string) => ({ order: '订单', recharge: '充值', admin_adjustment: '管理员调账' } as any)[t] || t

onMounted(async () => {
  await load()
  if (route.query.recharge === 'return') {
    message.info('支付已提交，到账后余额会自动更新；如有疑问请稍后刷新查看流水。')
  }
})
</script>

<template>
  <div class="wallet-page">
    <div class="dashboard-heading"><div><div class="eyebrow">财务中心</div><h1>钱包</h1><p>余额、充值与完整资金流水；所有资金变动均有对应流水记录。</p></div><NButton v-if="payOptions.length" type="primary" @click="openRecharge">在线充值</NButton></div>

    <div class="admin-two-col">
      <section class="panel">
        <div class="panel-title-row"><div><h2>资金流水</h2><span>最近 200 条</span></div></div>
        <div v-if="txs.length">
          <div v-for="t in txs" :key="t.id" class="tx-row">
            <div>
              <b>{{ t.description || txTypeText(t.type) }}</b>
              <small>{{ refText(t.reference_type) }} · {{ new Date(t.created_at).toLocaleString() }}</small>
            </div>
            <div style="text-align:right">
              <span :class="t.amount_cents >= 0 ? 'tx-plus' : 'tx-minus'" style="font-weight:800">{{ t.amount_cents >= 0 ? '+' : '' }}{{ money(t.amount_cents, t.currency) }}</span>
              <small class="muted" style="display:block">余额 {{ money(t.balance_after_cents, t.currency) }}</small>
            </div>
          </div>
        </div>
        <div v-else class="empty-box">暂无资金流水。</div>
      </section>

      <aside class="stack">
        <div class="panel">
          <div class="panel-title-row"><div><h2>可用余额</h2><span>用于支付订单</span></div></div>
          <div class="metric">{{ money(wallet.balance_cents) }}</div>
          <div class="muted" style="font-size:11px;margin-top:8px">充值后会实时入账；如需人工调账请联系管理员。</div>
        </div>
        <div v-if="!payOptions.length" class="panel">
          <div class="panel-title-row"><div><h2>在线充值</h2><span>暂未开放</span></div></div>
          <p class="muted" style="font-size:12px;margin:0">管理员尚未配置在线支付方式，可联系管理员通过余额调账充值。</p>
        </div>
      </aside>
    </div>

    <NModal v-model:show="rechargeOpen" preset="card" title="余额充值" style="width:min(440px,92vw)">
      <div class="stack">
        <div class="recharge-grid">
          <div v-for="p in presets" :key="p" class="recharge-amount" :class="{ active: amount === p }" @click="amount = p">{{ money(p) }}</div>
        </div>
        <NInputNumber v-model:value="amount" :min="1" :precision="2" placeholder="自定义金额（元）" style="width:100%">
          <template #prefix>¥</template>
        </NInputNumber>
        <NSelect v-model:value="choice" :options="payOptions" placeholder="选择支付渠道" />
        <div class="security-note">确认后将跳转支付平台完成付款，支付成功后余额自动到账。</div>
        <NButton type="primary" block :loading="busy" :disabled="!amount || !choice" @click="submitRecharge">跳转支付</NButton>
      </div>
    </NModal>
  </div>
</template>
