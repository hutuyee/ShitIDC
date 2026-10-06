<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NSelect, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'

// 推介计划 · 推介配置（对齐魔方 IdcsmartRecommend 插件后台配置页）：
// 初始奖励存款 / 确认天数 / 最低提现金额 / 提现手续费 / 默认推介链接 /
// 可生成自定义链接的系统页面 / 商品新购与续费奖励比例 / 主打推介产品（会员中心展示）。

interface RatioRow {
  product_id: number | null
  ratio: number
  amount_yuan: number
  renew_ratio: number
  renew_amount_yuan: number
}

const message = useMessage()
const busy = ref(false)
const loading = ref(true)
const products = ref<any[]>([])
const ratios = ref<RatioRow[]>([])
const mainProducts = ref<number[]>([])
const form = reactive({
  awards_yuan: 0,
  confirm_days: 14,
  withdraw_min_yuan: 0,
  withdraw_handling_fee: 0,
  default_url: '',
  system_urls: ''
})

const productOptions = computed(() => products.value.map((p: any) => ({ label: p.name, value: p.id })))
const productName = (id: number | null) => productOptions.value.find(o => o.value === id)?.label || '—'
const usedProductIDs = () => ratios.value.map(r => Number(r.product_id)).filter(Boolean)
const availableOptions = computed(() => productOptions.value.filter(o => !usedProductIDs().includes(Number(o.value))))

function addRatio() {
  ratios.value.push({ product_id: null, ratio: 0, amount_yuan: 0, renew_ratio: 0, renew_amount_yuan: 0 })
}
function removeRatio(i: number) {
  ratios.value.splice(i, 1)
}
function moveMain(i: number, delta: number) {
  const j = i + delta
  if (j < 0 || j >= mainProducts.value.length) return
  const next = mainProducts.value.slice()
  const tmp = next[i]
  next[i] = next[j]
  next[j] = tmp
  mainProducts.value = next
}

async function load() {
  loading.value = true
  try {
    if (!products.value.length) {
      try { products.value = dataOf<any>(await api.get('/admin/products')) } catch { products.value = [] }
    }
    const d = dataOf<any>(await api.get('/admin/recommend/config'))
    const cfg = d.config || {}
    form.awards_yuan = Number(cfg.awards_cents || 0) / 100
    form.confirm_days = Number(cfg.confirm_days ?? 14)
    form.withdraw_min_yuan = Number(cfg.withdraw_min_cents || 0) / 100
    form.withdraw_handling_fee = Number(cfg.withdraw_handling_fee || 0)
    form.default_url = cfg.default_url || ''
    form.system_urls = (cfg.system_urls || []).join('\n')
    ratios.value = (d.ratios || []).map((r: any) => ({
      product_id: r.product_id,
      ratio: Number(r.ratio || 0),
      amount_yuan: Number(r.amount_cents || 0) / 100,
      renew_ratio: Number(r.renew_ratio || 0),
      renew_amount_yuan: Number(r.renew_amount_cents || 0) / 100
    }))
    mainProducts.value = (d.products || []).map((p: any) => p.product_id)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取推介配置失败')
  } finally { loading.value = false }
}

async function save() {
  for (const r of ratios.value) {
    if (!r.product_id) { message.warning('请为每一行选择商品'); return }
    if (Number(r.ratio) <= 0 && Number(r.renew_ratio) <= 0) { message.warning(`商品「${productName(r.product_id)}」的新购与续费比例不能同时为 0`); return }
  }
  busy.value = true
  try {
    await api.post('/admin/recommend/config', {
      awards_cents: Math.round(Number(form.awards_yuan || 0) * 100),
      confirm_days: Math.max(0, Math.round(Number(form.confirm_days || 0))),
      withdraw_min_cents: Math.round(Number(form.withdraw_min_yuan || 0) * 100),
      withdraw_handling_fee: Math.max(0, Math.min(100, Math.round(Number(form.withdraw_handling_fee || 0)))),
      default_url: form.default_url.trim(),
      system_urls: form.system_urls.split('\n').map(s => s.trim()).filter(Boolean),
      ratios: ratios.value.map(r => ({
        product_id: r.product_id,
        ratio: Number(r.ratio || 0),
        amount_cents: Math.round(Number(r.amount_yuan || 0) * 100),
        renew_ratio: Number(r.renew_ratio || 0),
        renew_amount_cents: Math.round(Number(r.renew_amount_yuan || 0) * 100)
      })),
      products: mainProducts.value
    })
    message.success('推介配置已保存')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally { busy.value = false }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>推介配置</h1>
        <p>设置开启推介计划的初始奖励存款、奖励确认天数、提现规则与商品奖励比例；商品比例决定被推介用户购买后产生的奖励金额。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary tag="a" :href="`${ADMIN_PATH}/recommend`">奖励记录</NButton>
        <NButton type="primary" :loading="busy" @click="save">保存</NButton>
      </div>
    </div>

    <div v-if="loading" class="panel empty-box">加载中…</div>

    <template v-else>
      <section class="panel stack">
        <div class="panel-title-row"><div><h2>基础配置</h2><span>金额单位为「元」</span></div></div>
        <div class="form-grid">
          <label><span>推介奖励存款</span>
            <NInputNumber v-model:value="form.awards_yuan" :min="0" :precision="2" style="width:100%" />
            <small class="muted">开启推介计划的用户获得的初始奖励金额，0 表示不发放</small>
          </label>
          <label><span>确认推介天数</span>
            <NInputNumber v-model:value="form.confirm_days" :min="0" :max="365" style="width:100%" />
            <small class="muted">奖励需确认后才可提现；输入 0 视为即刻确认，默认为 14 天</small>
          </label>
          <label><span>最低提现金额</span>
            <NInputNumber v-model:value="form.withdraw_min_yuan" :min="0" :precision="2" style="width:100%" />
            <small class="muted">可提现未达到最低金额时，无法提现</small>
          </label>
          <label><span>提现手续费（%）</span>
            <NInputNumber v-model:value="form.withdraw_handling_fee" :min="0" :max="100" style="width:100%" />
            <small class="muted">输入 0 视为无提现手续费</small>
          </label>
          <label class="full"><span>默认推介链接</span>
            <NInput v-model:value="form.default_url" placeholder="请输入用户推介链接默认跳转的页面，不输入则默认系统登录页" />
            <small class="muted">修改默认跳转链接后，所有用户的推介链接都会变动，请及时提醒用户</small>
          </label>
          <label class="full"><span>自定义推介页面（一行一个地址）</span>
            <NInput v-model:value="form.system_urls" type="textarea" :rows="4" placeholder="https://example.com/ 会员中心页面链接" />
            <small class="muted">用于用户在会员中心生成自定义推介链接；请确保链接与业务系统在同一域名下</small>
          </label>
        </div>
      </section>

      <section class="panel stack">
        <div class="panel-title-row">
          <div><h2>推介奖励比例</h2><span>购买最低金额按同一订单相同商品合计金额计算</span></div>
          <NButton size="small" secondary @click="addRatio">新增 +</NButton>
        </div>
        <div v-if="ratios.length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head"><span>商品</span><span>奖励比例（%）</span><span>购买最低金额（元）</span><span>续费比例（%）</span><span>续费最低金额（元）</span><span>操作</span></div>
          <div v-for="(r, i) in ratios" :key="i" class="audit-row">
            <span><NSelect v-model:value="r.product_id" filterable :options="availableOptions" placeholder="请选择商品" /></span>
            <span><NInputNumber v-model:value="r.ratio" :min="0" :max="100" :precision="2" style="width:110px" /></span>
            <span><NInputNumber v-model:value="r.amount_yuan" :min="0" :precision="2" style="width:130px" /></span>
            <span><NInputNumber v-model:value="r.renew_ratio" :min="0" :max="100" :precision="2" style="width:110px" /></span>
            <span><NInputNumber v-model:value="r.renew_amount_yuan" :min="0" :precision="2" style="width:130px" /></span>
            <span><NButton size="tiny" tertiary type="error" @click="removeRatio(i)">删除</NButton></span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有商品奖励比例。</div>
        <p class="muted">历史数据：有推介成功记录的商品，续费开始按照配置计算续费推介。</p>
      </section>

      <section class="panel stack">
        <div class="panel-title-row"><div><h2>主打推介产品</h2><span>显示在会员中心的建议推介产品，按顺序展示</span></div></div>
        <NSelect v-model:value="mainProducts" multiple filterable :options="productOptions" placeholder="请选择主打推介产品" />
        <div v-if="mainProducts.length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head"><span>排序</span><span>商品名称</span><span>操作</span></div>
          <div v-for="(id, i) in mainProducts" :key="id" class="audit-row">
            <span>{{ i + 1 }}</span>
            <span>{{ productName(id) }}</span>
            <span class="row" style="gap:6px">
              <NButton size="tiny" secondary :disabled="i === 0" @click="moveMain(i, -1)">上移</NButton>
              <NButton size="tiny" secondary :disabled="i === mainProducts.length - 1" @click="moveMain(i, 1)">下移</NButton>
              <NButton size="tiny" tertiary type="error" @click="mainProducts.splice(i, 1)">删除</NButton>
            </span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有主打推介产品。</div>
      </section>
    </template>
  </div>
</template>
