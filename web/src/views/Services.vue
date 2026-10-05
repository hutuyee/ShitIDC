<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NModal, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const router = useRouter()
const message = useMessage()
const services = ref<any[]>([])
const renewing = ref('')
// detail modal (GET /services/:id) with the provisioned instance config
const detail = ref<any | null>(null)
const loadingDetail = ref(false)

const statusText: Record<string, string> = {
  pending: '待开通', provisioning: '开通中', active: '生效中', suspending: '暂停中',
  suspended: '已暂停', terminating: '删除中', terminated: '已删除', failed: '开通失败',
}
const statusType = (s: string) => ({ active: 'success', provisioning: 'info', pending: 'default', suspended: 'warning', failed: 'error', terminated: 'default' } as any)[s] || 'default'
const cycleText = (v: string) => ({ monthly: '月付', quarterly: '季付', semiannually: '半年付', yearly: '年付' } as any)[v] || v
const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleDateString() : '—')
const expiringSoon = (s: any) => s.expires_at && new Date(s.expires_at).getTime() - Date.now() < 14 * 86400000

async function load() {
  services.value = dataOf(await api.get('/services'))
}

async function renew(s: any) {
  renewing.value = s.id
  try {
    await api.post(`/services/${s.id}/renew`)
    message.success('续费订单已创建，请完成支付')
    router.push('/orders')
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '创建续费订单失败')
  } finally {
    renewing.value = ''
  }
}

// ---- 升降级（魔方 shd_upgrades）：按剩余天数折算差价 ----
const upgradeOpen = ref(false)
const upgradeService = ref<any>(null)
const upgradePlans = ref<any[]>([])
const upgradeTarget = ref('')
const upgradeQuote = ref<any>(null)
const upgradeBusy = ref(false)
const upgradeVoucher = ref('')

const planOptions = computed(() => upgradePlans.value.map(pl => ({
  label: pl.product_name + '（' + cycleText(pl.billing_cycle) + '）' +
    (pl.diff_cents > 0 ? ' 需补 ' + money(pl.diff_cents) : ' 无需补款'),
  value: pl.product_id + '|' + pl.billing_cycle,
})))

async function openUpgrade(s: any) {
  upgradeService.value = s
  upgradeTarget.value = ''
  upgradeQuote.value = null
  upgradePlans.value = []
  upgradeVoucher.value = ''
  upgradeOpen.value = true
  try {
    upgradePlans.value = dataOf<any[]>(await api.get(`/services/${s.id}/upgrade-plans`))
    if (!upgradePlans.value.length) message.info('暂时没有可升级到的其它方案')
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取升级方案失败')
  }
}

async function loadQuote() {
  upgradeQuote.value = null
  if (!upgradeTarget.value || !upgradeService.value) return
  const parts = upgradeTarget.value.split('|')
  try {
    upgradeQuote.value = dataOf(await api.post(`/services/${upgradeService.value.id}/upgrade/quote`, {
      product_id: parts[0], billing_cycle: parts[1],
    }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '报价失败')
  }
}

async function confirmUpgrade() {
  if (!upgradeTarget.value) { message.error('请选择目标方案'); return }
  const parts = upgradeTarget.value.split('|')
  upgradeBusy.value = true
  try {
    const r = dataOf<any>(await api.post(`/services/${upgradeService.value.id}/upgrade`, {
      product_id: parts[0], billing_cycle: parts[1],
      voucher_code: upgradeVoucher.value.trim() || undefined,
    }))
    upgradeOpen.value = false
    if (r && r.payable) {
      message.success('升级订单已创建，付款后生效')
      router.push('/orders')
    } else {
      message.success('降级或等价变更已立即生效')
      await load()
    }
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '升级失败')
  } finally { upgradeBusy.value = false }
}
async function open(s: any) {
  loadingDetail.value = true
  detail.value = null
  try {
    detail.value = dataOf(await api.get(`/services/${s.id}`))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取服务详情失败')
  } finally { loadingDetail.value = false }
}

// flatten the provisioned instance payload into display rows; nested objects
// are shown as indented JSON so credentials/addresses stay readable
const configRows = computed(() => {
  const rows: { key: string; value: string }[] = []
  const cfg = detail.value?.config
  if (!cfg || typeof cfg !== 'object') return rows
  for (const [k, v] of Object.entries(cfg)) {
    if (v == null || k === 'transition_from' || k === 'transition_to') continue
    rows.push({ key: k, value: typeof v === 'object' ? JSON.stringify(v, null, 2) : String(v) })
  }
  return rows
})

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">产品与服务</div>
        <h1>我的服务</h1>
        <p>已开通的资源实例与到期时间；到期前可直接续费，支付成功后到期时间自动顺延一个周期。</p>
      </div>
      <router-link to="/products" class="soft-action">＋ 新购产品</router-link>
    </div>

    <div v-if="!services.length" class="empty-box">还没有服务，去产品中心购买一个套餐吧。</div>
    <div class="stack">
      <div class="card row" v-for="s in services" :key="s.id" style="justify-content:space-between;align-items:center;cursor:pointer" @click="open(s)">
        <div>
          <b>{{ s.product_name }}</b>
          <div class="muted">
            {{ s.id.slice(0, 8) }} · {{ cycleText(s.billing_cycle) }}
            <template v-if="s.price_cents"> · {{ money(s.price_cents, s.currency) }}/{{ cycleText(s.billing_cycle) || '周期' }}</template>
            · 到期：{{ fmt(s.expires_at) }}
          </div>
        </div>
        <div class="row" style="gap:8px;align-items:center">
          <NTag v-if="expiringSoon(s) && s.status === 'active'" type="warning" size="small" round>即将到期</NTag>
          <NTag :type="statusType(s.status)" round>{{ statusText[s.status] || s.status }}</NTag>
          <NButton
            v-if="['active', 'suspended'].includes(s.status)"
            size="small" type="primary" secondary :loading="renewing === s.id" @click.stop="renew(s)">
            续费
          </NButton>
          <NButton
            v-if="['active', 'suspended'].includes(s.status)"
            size="small" secondary @click.stop="openUpgrade(s)">
            升降级
          </NButton>
        </div>
      </div>
    </div>

    <Teleport to="body">
      <div v-if="detail || loadingDetail" class="svc-mask" @click.self="detail = null; loadingDetail = false">
        <div class="svc-card">
          <template v-if="loadingDetail"><div class="empty-box">加载中…</div></template>
          <template v-else-if="detail">
            <div class="svc-head">
              <div><div class="eyebrow">服务详情</div><h2>{{ detail.product_name }}</h2></div>
              <NTag :type="statusType(detail.status)" round>{{ statusText[detail.status] || detail.status }}</NTag>
            </div>
            <div class="svc-grid">
              <div><span>服务 ID</span><b>{{ detail.id.slice(0, 8) }}</b></div>
              <div><span>计费周期</span><b>{{ cycleText(detail.billing_cycle) || '—' }}</b></div>
              <div><span>到期时间</span><b>{{ fmt(detail.expires_at) }}</b></div>
              <div><span>开通类型</span><b>{{ detail.provider_type }}</b></div>
              <div v-if="detail.provider_ref"><span>实例标识</span><b>{{ detail.provider_ref }}</b></div>
            </div>
            <div v-if="configRows.length" class="svc-config">
              <div class="svc-config-title">实例信息 / 开通配置</div>
              <div v-for="row in configRows" :key="row.key" class="svc-config-row">
                <code class="svc-key">{{ row.key }}</code>
                <pre class="svc-val">{{ row.value }}</pre>
              </div>
            </div>
            <div v-else class="empty-box">实例还在开通中，配置信息稍后会出现在这里。</div>
            <div class="svc-actions">
              <NButton v-if="['active', 'suspended'].includes(detail.status)" type="primary" @click.stop="renew(detail)">续费此服务</NButton>
              <NButton tertiary @click="detail = null">关闭</NButton>
            </div>

    <NModal v-model:show="upgradeOpen" preset="card" title="升降级" style="width:min(560px,94vw)">
      <div class="stack">
        <p class="muted" style="font-size:12px;margin:0">
          按剩余天数把当前方案未使用的价值折算成金额，再与新方案差价结算：差价为正需要补款，
          为 0 或负数（降级）则立即生效、不退款。
        </p>
        <div>
          <b class="muted" style="font-size:12px">目标方案</b>
          <NSelect v-model:value="upgradeTarget" :options="planOptions" placeholder="选择要升级到的商品与周期"
            style="margin-top:6px" @update:value="loadQuote" />
        </div>
        <div v-if="upgradeQuote" class="quote-box">
          <div><span>剩余天数</span><b>{{ upgradeQuote.days_remaining }} / {{ upgradeQuote.days_in_cycle }} 天</b></div>
          <div><span>当前方案剩余价值</span><b>{{ money(upgradeQuote.remaining_value_cents, upgradeQuote.currency) }}</b></div>
          <div><span>新方案价格</span><b>{{ money(upgradeQuote.new_price_cents, upgradeQuote.currency) }}</b></div>
          <div v-if="upgradeQuote.payable">
            <span>代金券码（可选）</span>
            <input v-model="upgradeVoucher" class="catalog-search" style="max-width:200px;text-align:right" placeholder="8 位券码" />
          </div>
          <div class="quote-total">
            <span>{{ upgradeQuote.payable ? '需补差价' : '无需补款' }}</span>
            <b>{{ money(Math.max(0, upgradeQuote.diff_cents), upgradeQuote.currency) }}</b>
          </div>
        </div>
        <NButton block type="primary" size="large" :loading="upgradeBusy" :disabled="!upgradeTarget" @click="confirmUpgrade">
          {{ upgradeQuote && upgradeQuote.payable ? '创建升级订单' : '立即切换方案' }}
        </NButton>
      </div>
    </NModal>
          </template>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.svc-mask { position: fixed; inset: 0; background: rgba(10, 12, 20, .5); display: flex; align-items: center; justify-content: center; z-index: 1000; padding: 16px; }
.svc-card { background: var(--panel, #fff); border-radius: 16px; padding: 22px; width: min(560px, 94vw); max-height: 86vh; overflow: auto; }
.svc-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px; }
.svc-head h2 { margin: 2px 0 0; }
.svc-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-bottom: 16px; }
.svc-grid span { display: block; color: var(--muted, #8a93a6); font-size: 12px; }
.svc-grid b { font-size: 14px; word-break: break-all; }
.svc-config-title { font-weight: 700; margin-bottom: 8px; }
.svc-config-row { margin-bottom: 8px; }
.svc-key { display: inline-block; background: var(--panel, #f2f4fa); border-radius: 6px; padding: 2px 8px; font-size: 12px; margin-bottom: 4px; }
.svc-val { margin: 0; background: var(--panel, #f2f4fa); border-radius: 8px; padding: 8px 10px; font-size: 12px; white-space: pre-wrap; word-break: break-all; }
.svc-actions { margin-top: 16px; display: flex; justify-content: flex-end; gap: 8px; }
</style>
