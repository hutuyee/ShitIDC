<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const items = ref<any[]>([])
const products = ref<any[]>([])
const busy = ref(false)
const form = reactive({
  code: '', type: 'percent', value: 10, max_uses: null as number | null,
  max_uses_per_user: 1, min_amount: 0, expires_at: null as string | null,
  product_ids: [] as string[],
})

const typeOptions = [
  { label: '百分比折扣（%）', value: 'percent' },
  { label: '固定金额（分）', value: 'fixed' },
]

const productOptions = computed(() => products.value.map(p => ({ label: p.name, value: p.id })))
const productName = (id: string) => products.value.find(p => p.id === id)?.name || id

const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`

async function load() {
  try { items.value = dataOf(await api.get('/admin/coupons')) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取优惠券失败') }
}

async function loadProducts() {
  try { products.value = dataOf(await api.get('/admin/products')) }
  catch { products.value = [] }
}

async function create() {
  if (!form.code.trim() || form.value <= 0) { message.error('请填写优惠码和优惠值'); return }
  busy.value = true
  try {
    await api.post('/admin/coupons', {
      code: form.code.trim(), type: form.type, value: form.value,
      max_uses: form.max_uses, max_uses_per_user: form.max_uses_per_user,
      min_amount_cents: Math.round(form.min_amount * 100),
      // Empty array = site-wide code; never send null (the column is NOT NULL).
      product_ids: form.product_ids,
      expires_at: form.expires_at ? new Date(form.expires_at).toISOString() : null,
    })
    message.success('优惠码已创建')
    form.code = ''; form.value = form.type === 'percent' ? 10 : 100
    form.product_ids = []
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '创建失败') }
  finally { busy.value = false }
}

async function toggle(x: any) {
  try {
    await api.put(`/admin/coupons/${x.id}`, { active: !x.active })
    message.success(x.active ? '已停用' : '已启用')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}

async function remove(x: any) {
  try {
    await api.delete(`/admin/coupons/${x.id}`)
    message.success('已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const scopeText = (c: any) => (Array.isArray(c.product_ids) && c.product_ids.length
  ? c.product_ids.map(productName).join('、')
  : '全场通用')
onMounted(() => { load(); loadProducts() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">优惠系统</div><h1>优惠券</h1><p>下单时后端原子核销：全局总量、每人限用、最低消费、适用商品与有效期全部在事务内校验。</p></div></div>

    <div class="admin-two-col">
      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>创建优惠码</h2><span>价格单位为「分」</span></div></div>
        <div class="form-grid">
          <label><span>优惠码</span><NInput v-model:value="form.code" placeholder="例如 WELCOME10" /></label>
          <label><span>类型</span><NSelect v-model:value="form.type" :options="typeOptions" /></label>
          <label><span>{{ form.type === 'percent' ? '折扣百分比 (1-100)' : '立减金额（分）' }}</span><NInputNumber v-model:value="form.value" :min="1" style="width:100%" /></label>
          <label><span>每人限用次数</span><NInputNumber v-model:value="form.max_uses_per_user" :min="1" style="width:100%" /></label>
          <label><span>全局总次数（留空不限）</span><NInputNumber v-model:value="form.max_uses" :min="1" style="width:100%" :clearable="true" /></label>
          <label><span>最低消费（元）</span><NInputNumber v-model:value="form.min_amount" :min="0" :precision="2" style="width:100%" /></label>
          <label class="full"><span>适用商品（不选＝全场通用）</span>
            <NSelect v-model:value="form.product_ids" multiple filterable clearable :options="productOptions" placeholder="留空表示所有商品都能用" />
          </label>
          <label class="full"><span>过期时间（可选）</span><input type="datetime-local" class="native-input" v-model="form.expires_at" /></label>
        </div>
        <NButton type="primary" size="large" :loading="busy" @click="create">创建优惠码</NButton>
      </section>

      <section class="panel">
        <div class="panel-title-row"><div><h2>已有优惠码</h2><span>{{ items.length }} 个</span></div></div>
        <div v-if="items.length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head"><span>码</span><span>优惠</span><span>适用范围</span><span>已用</span><span>过期</span><span>操作</span></div>
          <div v-for="c in items" :key="c.id" class="audit-row">
            <span><b>{{ c.code }}</b><small class="ua"><NTag :type="c.active ? 'success' : 'default'" size="tiny" round>{{ c.active ? '启用' : '停用' }}</NTag></small></span>
            <span>{{ c.type === 'percent' ? c.value + '%' : money(c.value) }}</span>
            <span class="muted">{{ scopeText(c) }}</span>
            <span class="muted">{{ c.used_count }}{{ c.max_uses ? ' / ' + c.max_uses : '' }}</span>
            <span class="muted">{{ fmt(c.expires_at) }}</span>
            <span class="row" style="gap:6px">
              <NButton size="tiny" tertiary @click="toggle(c)">{{ c.active ? '停用' : '启用' }}</NButton>
              <NButton size="tiny" tertiary type="error" @click="remove(c)">删除</NButton>
            </span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有优惠券。</div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.native-input { height: 34px; border-radius: 6px; border: 1px solid var(--border, #d5d9e4); background: var(--panel, #fff); color: var(--text, #1c2333); padding: 0 8px; width: 100%; }
</style>
