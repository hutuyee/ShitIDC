<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInputNumber, NModal, NTag, useMessage } from 'naive-ui'
import { useRouter } from 'vue-router'
import { api, dataOf } from '../api'

const products = ref<any[]>([])
const groups = ref<any[]>([])
const promotions = ref<any[]>([])
const message = useMessage()
const router = useRouter()
const keyword = ref('')

// ---- purchase dialog: backend recalculates the price from product + cycle (§13) ----
const buyOpen = ref(false)
const buyProduct = ref<any>(null)
const buyPrices = ref<any[]>([])
const buyCycle = ref('')
const buyQty = ref(1)
const buying = ref(false)
const addingToCart = ref(false)
const couponCode = ref('')
const couponDiscount = ref(0)
const couponChecking = ref(false)
const couponMsg = ref('')
const voucherCode = ref('')
const voucherDiscount = ref(0)
const voucherChecking = ref(false)
const voucherMsg = ref('')

async function checkCoupon() {
  if (!couponCode.value.trim()) { return }
  couponChecking.value = true
  try {
    const d = dataOf<{ discount_cents: number }>(await api.post('/coupons/validate', {
      code: couponCode.value.trim(), product_id: buyProduct.value.id, billing_cycle: buyCycle.value, quantity: buyQty.value,
    }))
    couponDiscount.value = d.discount_cents
    couponMsg.value = '已优惠 ' + money(d.discount_cents, buyProduct.value.currency)
  } catch (e: any) {
    couponDiscount.value = 0
    couponMsg.value = e?.response?.data?.error?.message || '优惠码无效'
  } finally { couponChecking.value = false }
}

const buyTotalAfterCoupon = computed(() => Math.max(0, buyTotal.value - couponDiscount.value))
const buyTotalAfterDiscount = computed(() => Math.max(0, buyTotal.value - couponDiscount.value - voucherDiscount.value))

// 活动促销（对齐 EventPromotion）：满足商品 / 周期条件的活动自动生效，这里只做展示。
const cycleOfProduct = (p: any) => (buyProduct.value?.id === p?.id ? buyCycle.value : (p?.billing_cycle || 'monthly'))
function promotionFor(p: any) {
  if (!p) return null
  return promotions.value.find(v => {
    const ids = v.products || []
    if (ids.length && !ids.includes(p.id)) return false
    if (v.cycle_limit && (v.cycle || []).length) {
      const cycles = (v.cycle || []).map((c: string) => (c === 'annually' ? 'yearly' : c))
      if (!cycles.includes(cycleOfProduct(p))) return false
    }
    return true
  }) || null
}
const promoText = (v: any) => (v ? (v.type === 'percent'
  ? `${v.value}% 折扣`
  : `满 ${Number(v.full || 0).toFixed(2)} 减 ${Number(v.value || 0).toFixed(2)}`) : '')
const buyPromotion = computed(() => promotionFor(buyProduct.value))

async function checkVoucher() {
  if (!voucherCode.value.trim()) { return }
  voucherChecking.value = true
  try {
    const d = dataOf<{ discount_cents: number }>(await api.post('/vouchers/preview', {
      code: voucherCode.value.trim(), product_id: buyProduct.value.id, billing_cycle: buyCycle.value, quantity: buyQty.value,
    }))
    voucherDiscount.value = d.discount_cents
    voucherMsg.value = '可抵扣 ' + money(d.discount_cents, buyProduct.value.currency)
  } catch (e: any) {
    voucherDiscount.value = 0
    voucherMsg.value = e?.response?.data?.error?.message || '代金券不可用'
  } finally { voucherChecking.value = false }
}

async function load() {
  const [p, g, promo] = await Promise.all([
    api.get('/products').then(r => dataOf<any[]>(r)).catch(() => []),
    api.get('/product-groups').then(r => dataOf<any[]>(r)).catch(() => []),
    api.get('/promotions/active').then(r => dataOf<any[]>(r)).catch(() => [])
  ])
  products.value = p
  groups.value = g
  promotions.value = promo
}

async function openBuy(p: any) {
  buyProduct.value = p
  buyQty.value = 1
  couponCode.value = ''; couponDiscount.value = 0; couponMsg.value = ''
  voucherCode.value = ''; voucherDiscount.value = 0; voucherMsg.value = ''
  try {
    buyPrices.value = dataOf<any[]>(await api.get(`/products/${p.id}/prices`))
  } catch { buyPrices.value = [] }
  buyCycle.value = buyPrices.value[0]?.billing_cycle || p.billing_cycle || 'monthly'
  await Promise.all([loadProductConfig(p.id), loadCurrencyOptions(p.id)])
  buyOpen.value = true
}

const buyUnit = computed(() => {
  // 多币种下优先用选中币种的价格；否则回退到当前周期的价格档。
  if (buyCurrencyAmount.value !== null) return buyCurrencyAmount.value
  const hit = buyPrices.value.find(x => x.billing_cycle === buyCycle.value)
  return hit ? hit.amount_cents : buyProduct.value?.price_cents ?? 0
})

// ---- 多币种：同一商品在不同币种下可以是完全独立的价格 ----
const buyCurrencies = ref<{ currency: string; amount_cents: number }[]>([]);
const buyCurrency = ref('');

// 该商品在当前周期上可售的币种（每个币种一套价，不用汇率折算）。
async function loadCurrencyOptions(productId: string) {
  buyCurrencies.value = [];
  buyCurrency.value = '';
  try {
    const rows = dataOf<any[]>(await api.get(`/products/${productId}/price-currencies`));
    const hit = rows.filter((r: any) => r.billing_cycle === buyCycle.value);
    buyCurrencies.value = hit;
    if (hit.length) buyCurrency.value = hit[0].currency;
  } catch { /* 单币种商品没有这个接口数据也正常 */ }
}

// 当前选中币种下的价格；没选就回退到价格档本身。
const buyCurrencyAmount = computed(() => {
  const hit = buyCurrencies.value.find(c => c.currency === buyCurrency.value);
  return hit ? hit.amount_cents : null;
});
// 基础价 + 配置加价（按件）+ 一次性初装费，再乘数量。
const buyTotal = computed(() => (buyUnit.value + configCents.value.sum) * Math.max(1, buyQty.value) + configCents.value.setup)

async function confirmBuy() {
  buying.value = true
  try {
    const cfg = buildConfigPayload()
    await api.post('/orders', {
      product_id: buyProduct.value.id, billing_cycle: buyCycle.value, quantity: buyQty.value,
      coupon_code: couponCode.value.trim() || undefined,
      voucher_code: voucherCode.value.trim() || undefined,
      currency: buyCurrency.value || undefined,
      config: cfg.config, custom_fields: cfg.custom_fields,
    })
    message.success('订单已创建，请完成支付')
    buyOpen.value = false
    router.push('/orders')
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '下单失败') } finally { buying.value = false }
}

// 加入购物车：与「创建订单」共用同一份配置与自定义字段，价格结算时仍会重算。
async function addToCart() {
  if (!buyProduct.value) return
  addingToCart.value = true
  try {
    const cfg = buildConfigPayload()
    await api.post('/cart/items', {
      product_id: buyProduct.value.id, billing_cycle: buyCycle.value, quantity: buyQty.value,
      currency: buyCurrency.value || undefined,
      config: cfg.config, custom_fields: cfg.custom_fields,
    })
    message.success('已加入购物车')
    buyOpen.value = false
    router.push('/cart')
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '加入购物车失败') } finally { addingToCart.value = false }
}

const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const cycle = (v: string) => ({ monthly: '月', yearly: '年', quarterly: '季', semiannually: '半年' } as any)[v] || v
const filtered = computed(() => products.value.filter(p => !keyword.value || `${p.name} ${p.description}`.toLowerCase().includes(keyword.value.toLowerCase())))

// ---- 配置项与自定义字段（魔方可配置选项）----
const configOptions = ref<any[]>([]);
const customFields = ref<any[]>([]);
const stock = ref<any>({ stock_control: false, available: -1, allow_qty: true, max_per_customer: 0 });
// 买家选择：下拉/单选/开关存 valueId；数量型存数量。
const choices = ref<Record<string, string>>({});
const quantities = ref<Record<string, number>>({});
const fieldValues = ref<Record<string, string>>({});

function resetConfig() {
  choices.value = {};
  quantities.value = {};
  fieldValues.value = {};
}

async function loadProductConfig(productId: string) {
  configOptions.value = [];
  customFields.value = [];
  resetConfig();
  stock.value = { stock_control: false, available: -1, allow_qty: true, max_per_customer: 0 };
  try {
    const d = dataOf<any>(await api.get(`/products/${productId}/config`));
    configOptions.value = d.config_options || [];
    customFields.value = d.custom_fields || [];
    stock.value = d;
    // 预置默认值：优先 is_default，否则第一个可见候选项。
    for (const o of configOptions.value) {
      if (o.option_type === 4) {
        quantities.value[o.id] = o.qty_min || 1;
      } else if (o.option_type === 3) {
        choices.value[o.id] = '';
      } else {
        const def = (o.values || []).find((v: any) => v.is_default) || (o.values || [])[0];
        choices.value[o.id] = def ? def.id : '';
      }
    }
  } catch { /* 该商品没有配置项 */ }
}

// 前端报价只用于展示；下单时后端会用同一套规则重算。
const configCents = computed(() => {
  let sum = 0;
  let setup = 0;
  for (const o of configOptions.value) {
    if (o.option_type === 4) {
      const qty = Number(quantities.value[o.id] || 0);
      const unit = (o.values || [])[0];
      if (unit) {
        sum += Number(unit.price_cents || 0) * qty;
        setup += Number(unit.setup_cents || 0);
      }
    } else {
      const v = (o.values || []).find((x: any) => x.id === choices.value[o.id]);
      if (v) {
        sum += Number(v.price_cents || 0);
        setup += Number(v.setup_cents || 0);
      }
    }
  }
  return { sum, setup };
});

// 数量型配置项的候选项列表。
function qtyChoices(o: any) {
  const min = Number(o.qty_min || 1);
  const max = Number(o.qty_max || 10);
  const out: number[] = [];
  for (let i = min; i <= max && i <= min + 20; i++) out.push(i);
  return out;
}

// 提交给后端的配置项选择。
function buildConfigPayload() {
  const config: any[] = [];
  for (const o of configOptions.value) {
    if (o.option_type === 4) {
      config.push({ option_id: o.id, quantity: Number(quantities.value[o.id] || 0) });
    } else {
      config.push({ option_id: o.id, value_id: choices.value[o.id] || '' });
    }
  }
  const custom_fields: Record<string, string> = {};
  for (const f of customFields.value) {
    const v = (fieldValues.value[f.field_key] || '').trim();
    if (v) custom_fields[f.field_key] = v;
  }
  return { config, custom_fields };
}
// group-aware display: ungrouped products first, then each named group
const groupedSections = computed(() => {
  const byGroup = new Map<string, any[]>()
  for (const p of filtered.value) {
    const key = p.group_name || ''
    if (!byGroup.has(key)) byGroup.set(key, [])
    byGroup.get(key)!.push(p)
  }
  const sections: { name: string; weight: number; items: any[] }[] = []
  for (const g of groups.value) {
    if (byGroup.has(g.name)) sections.push({ name: g.name, weight: g.sort_weight ?? 0, items: byGroup.get(g.name)! })
  }
  const loose = byGroup.get('') || []
  if (loose.length) sections.unshift({ name: '', weight: Number.MAX_SAFE_INTEGER, items: loose })
  return sections
})

// The product description is written entirely by the administrator: whatever is
// stored is exactly what the storefront shows (newlines preserved, no
// system-extracted selling points, no placeholder marketing copy).
onMounted(load)
</script>

<template>
  <div class="catalog-page">
    <div class="dashboard-heading catalog-heading"><div><div class="eyebrow">产品中心</div><h1>选择适合你的服务</h1><p>价格和库存最终由服务端校验，前端展示仅作为订购入口。</p></div><input v-model="keyword" class="catalog-search" placeholder="搜索产品或配置…" /></div>

    <template v-if="groupedSections.length">
      <section v-for="sec in groupedSections" :key="sec.name || '_all'" class="catalog-section">
        <div v-if="sec.name" class="catalog-group-head"><h2>{{ sec.name }}</h2><span>{{ sec.items.length }} 个产品</span></div>
        <div class="product-grid-rich">
          <article v-for="p in sec.items" :key="p.id" class="product-card-rich">
            <div class="product-card-top">
              <span class="product-provider">{{ p.group_name || (p.provider_name || (p.provider_type === 'manual' ? 'ShitIDC' : p.provider_type)) }}</span>
              <span class="row" style="gap:6px">
                <span v-if="promotionFor(p)" class="promo-badge">活动 {{ promoText(promotionFor(p)) }}</span>
                <span class="product-status">可订购</span>
              </span>
            </div>
            <h2>{{ p.name }}</h2>
            <p v-if="p.description" class="product-desc">{{ p.description }}</p>
            <div class="product-price"><strong>{{ money(p.price_cents, p.currency) }}</strong><span>/ {{ cycle(p.billing_cycle) }}</span></div>
            <NButton block type="primary" size="large" @click="openBuy(p)">立即购买</NButton>
          </article>
        </div>
      </section>
    </template>
    <div v-else class="panel empty-box">暂无符合条件的产品。</div>

    <NModal v-model:show="buyOpen" preset="card" :title="buyProduct ? `购买 ${buyProduct.name}` : '购买'" style="width:min(440px,92vw)">
      <div class="stack" v-if="buyProduct">
        <div>
          <b class="muted" style="font-size:12px">计费周期</b>
          <div class="cycle-picker">
            <NTag v-for="pr in buyPrices" :key="pr.billing_cycle" size="large" round :type="buyCycle === pr.billing_cycle ? 'primary' : 'default'"
              :class="{ 'cycle-active': buyCycle === pr.billing_cycle }" style="cursor:pointer"
              @click="buyCycle = pr.billing_cycle">
              {{ cycle(pr.billing_cycle) }}付 {{ money(pr.amount_cents, pr.currency) }}
            </NTag>
            <NTag v-if="!buyPrices.length" size="large" round>{{ cycle(buyProduct.billing_cycle) }}付 {{ money(buyProduct.price_cents, buyProduct.currency) }}</NTag>
          </div>
        </div>
        <div>
          <b class="muted" style="font-size:12px">数量</b>
          <NInputNumber v-model:value="buyQty" :min="1" :max="stock.allow_qty === false ? 1 : 100" :disabled="stock.allow_qty === false" style="width:100%;margin-top:6px" />
        </div>
        <div v-if="buyCurrencies.length > 1">
          <b class="muted" style="font-size:12px">结算币种</b>
          <select v-model="buyCurrency" class="native-select" style="margin-top:6px">
            <option v-for="c in buyCurrencies" :key="c.currency" :value="c.currency">
              {{ c.currency }} — {{ money(c.amount_cents, c.currency) }}
            </option>
          </select>
        </div>
        <div v-if="configOptions.length">
          <b class="muted" style="font-size:12px">配置</b>
          <div class="config-list">
            <div v-for="o in configOptions" :key="o.id" class="config-row">
              <label class="config-label">
                <span>{{ o.name }}<em v-if="o.required"> *</em></span>
                <small v-if="o.description">{{ o.description }}</small>
              </label>
              <!-- 1 下拉 / 2 单选 -->
              <select v-if="o.option_type === 1 || o.option_type === 2" v-model="choices[o.id]" class="native-select">
                <option v-for="v in o.values" :key="v.id" :value="v.id">
                  {{ v.label }}<template v-if="v.price_cents > 0"> (+{{ money(v.price_cents) }})</template>
                </option>
              </select>
              <!-- 3 开关 -->
              <select v-else-if="o.option_type === 3" v-model="choices[o.id]" class="native-select">
                <option value="">不需要</option>
                <option v-for="v in o.values" :key="v.id" :value="v.id">
                  {{ v.label }}<template v-if="v.price_cents > 0"> (+{{ money(v.price_cents) }})</template>
                </option>
              </select>
              <!-- 4 数量 -->
              <select v-else v-model.number="quantities[o.id]" class="native-select">
                <option v-for="n in qtyChoices(o)" :key="n" :value="n">
                  {{ n }} × {{ (o.values[0] || {}).label || '单位' }}<template v-if="(o.values[0] || {}).price_cents"> (+{{ money((o.values[0] || {}).price_cents * n) }})</template>
                </option>
              </select>
            </div>
          </div>
        </div>
        <div v-if="customFields.length">
          <b class="muted" style="font-size:12px">补充信息</b>
          <div class="config-list">
            <div v-for="f in customFields" :key="f.id" class="config-row">
              <label class="config-label">
                <span>{{ f.name }}<em v-if="f.required"> *</em></span>
                <small v-if="f.description">{{ f.description }}</small>
              </label>
              <select v-if="f.field_type === 'dropdown'" v-model="fieldValues[f.field_key]" class="native-select">
                <option value="">请选择</option>
                <option v-for="opt in f.options" :key="opt" :value="opt">{{ opt }}</option>
              </select>
              <textarea v-else-if="f.field_type === 'textarea'" v-model="fieldValues[f.field_key]" class="native-input" rows="2" :placeholder="f.placeholder"></textarea>
              <input v-else :type="f.field_type === 'password' ? 'password' : 'text'" v-model="fieldValues[f.field_key]" class="native-input" :placeholder="f.placeholder" />
            </div>
          </div>
        </div>
        <div v-if="configCents.sum > 0 || configCents.setup > 0" class="config-summary">
          <span v-if="configCents.sum > 0">配置加价 {{ money(configCents.sum) }} / 件</span>
          <span v-if="configCents.setup > 0">一次性初装费 {{ money(configCents.setup) }}</span>
        </div>
        <div v-if="stock.stock_control" class="stock-note" :class="{ low: stock.available <= 3 }">
          剩余库存：{{ stock.available }} 件
        </div>
        <div v-if="buyPromotion" class="promo-hint">
          活动促销：{{ promoText(buyPromotion) }}（下单时自动生效，最终金额以服务端重算为准）
        </div>

        <div>
          <b class="muted" style="font-size:12px">优惠码</b>
          <div class="row" style="gap:8px;margin-top:6px">
            <input v-model="couponCode" class="catalog-search" style="flex:1" placeholder="输入优惠码（可选）" @change="checkCoupon" />
            <NButton secondary :loading="couponChecking" @click="checkCoupon">验证</NButton>
          </div>
          <small v-if="couponMsg" :style="{ color: couponDiscount > 0 ? '#1d9e64' : '#cf3030' }">{{ couponMsg }}</small>
        </div>
        <div>
          <b class="muted" style="font-size:12px">代金券</b>
          <div class="row" style="gap:8px;margin-top:6px">
            <input v-model="voucherCode" class="catalog-search" style="flex:1" placeholder="输入 8 位代金券码（可选）" @change="checkVoucher" />
            <NButton secondary :loading="voucherChecking" @click="checkVoucher">验证</NButton>
          </div>
          <small v-if="voucherMsg" :style="{ color: voucherDiscount > 0 ? '#1d9e64' : '#cf3030' }">{{ voucherMsg }}</small>
        </div>
        <div class="buy-total"><span>合计（下单时后端按最新价格重算）</span><strong>{{ money(buyTotalAfterDiscount, buyPrices.find(x => x.billing_cycle === buyCycle)?.currency || buyProduct.currency) }}</strong></div>
        <div class="row" style="gap:10px">
          <NButton secondary size="large" style="flex:1" :loading="addingToCart" @click="addToCart">加入购物车</NButton>
          <NButton type="primary" size="large" style="flex:1" :loading="buying" @click="confirmBuy">创建订单</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.promo-badge { padding: 2px 8px; border-radius: 999px; font-size: 11px; background: color-mix(in srgb, #f0a020 18%, transparent); color: #c07800; border: 1px solid color-mix(in srgb, #f0a020 40%, transparent); }
.promo-hint { font-size: 12px; color: #c07800; background: color-mix(in srgb, #f0a020 12%, transparent); border: 1px solid color-mix(in srgb, #f0a020 30%, transparent); border-radius: 10px; padding: 8px 10px; }
.catalog-section { margin-bottom: 26px; }
.catalog-group-head { display: flex; align-items: baseline; gap: 10px; margin: 0 0 12px; }
.catalog-group-head h2 { margin: 0; font-size: 17px; }
.catalog-group-head span { color: var(--muted, #8a93a6); font-size: 12px; }
.cycle-picker { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 6px; }
.cycle-active { font-weight: 700; }
.buy-total { display: flex; justify-content: space-between; align-items: center; padding: 10px 12px; background: var(--panel, #f6f7fb); border-radius: 10px; }
.buy-total strong { font-size: 18px; }
</style>

<style scoped>
.config-list { display: flex; flex-direction: column; gap: 10px; margin-top: 6px; }
.config-row { display: flex; flex-direction: column; gap: 4px; }
.config-label span { font-size: 12px; color: var(--text, #1c2333); }
.config-label em { color: #cf3030; font-style: normal; }
.config-label small { display: block; color: var(--muted, #8a93a6); font-size: 11px; }
.config-summary { display: flex; gap: 12px; font-size: 12px; color: var(--muted, #8a93a6); }
.stock-note { font-size: 12px; color: #179758; }
.stock-note.low { color: #cf3030; }
.native-select { height: 34px; border-radius: 6px; border: 1px solid var(--border, #d5d9e4); background: var(--panel, #fff); color: var(--text, #1c2333); padding: 0 8px; width: 100%; }
.native-input { border-radius: 6px; border: 1px solid var(--border, #d5d9e4); background: var(--panel, #fff); color: var(--text, #1c2333); padding: 6px 8px; width: 100%; }
</style>
