<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NInputNumber, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 产品自助转移（对齐魔方主程序附属插件 product_divert）。
// 上半是「基础配置」（是否启用 / 转出有效期 / 双方费用 / 保护期 / 可转移商品范围），
// 下半是插件后台的「转移列表」（全部用户的转移记录）。
// 发起与接收在用户端「产品转移」页完成；管理员转移请用「服务管理」的产品转移。

const message = useMessage()
const cfg = ref<any>({ is_open: false, validity_period_days: 7, push_cost_cents: 0, pull_cost_cents: 0, protection_period_days: 0, product_ids: [] })
const products = ref<any[]>([])
const productOptions = computed(() => products.value.map((p: any) => ({ label: p.name, value: p.id })))
const saving = ref(false)

const records = ref<any[]>([])
const total = ref(0)
const keyword = ref('')
const statusFilter = ref<number | null>(null)
const busy = ref(false)

const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string | null) => (v ? new Date(v).toLocaleString() : '—')
const statusText: Record<number, string> = { 1: '待接收', 2: '已完成', 3: '已关闭', 4: '已拒绝' }
const statusType = (s: number) => ({ 1: 'warning', 2: 'success' } as any)[s] || 'default'

async function loadConfig() {
  try { cfg.value = dataOf<any>(await api.get('/admin/product-divert/config')) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取配置失败') }
}
async function loadProducts() {
  try { products.value = dataOf<any[]>(await api.get('/admin/products')) || [] }
  catch { products.value = [] }
}
async function saveConfig() {
  saving.value = true
  try {
    await api.put('/admin/product-divert/config', cfg.value)
    message.success('配置已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally { saving.value = false }
}

async function loadRecords() {
  busy.value = true
  try {
    const params: any = { keyword: keyword.value.trim(), limit: 50 }
    if (statusFilter.value) params.status = statusFilter.value
    const d = dataOf<any>(await api.get('/admin/product-diverts', { params }))
    records.value = d?.list || []
    total.value = d?.count || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取转移记录失败')
  } finally { busy.value = false }
}

onMounted(() => { loadConfig(); loadProducts(); loadRecords() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>产品自助转移</h1>
        <p>对齐魔方「产品转移」插件（product_divert）：用户之间自助转移产品，双方按配置支付费用；此处维护基础配置并查看全部转移记录。管理员代操作请用「服务管理」的产品转移。</p>
      </div>
      <NButton secondary :loading="busy" @click="loadRecords">刷新记录</NButton>
    </div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>基础配置</h2><span>费用、有效期与商品范围在用户端「产品转移」页生效</span></div></div>
      <div class="stack" style="gap:14px;max-width:640px">
        <label class="row" style="gap:10px;align-items:center"><NSwitch v-model:value="cfg.is_open" /><span>启用产品自助转移</span></label>
        <label class="row" style="gap:10px;align-items:center">
          <span style="width:120px">转出有效期（天）</span>
          <NInputNumber v-model:value="cfg.validity_period_days" :min="0" :max="365" style="width:160px" />
          <span class="muted" style="font-size:12px">超过该时间未接受的转出自动关闭，0 = 不自动关闭</span>
        </label>
        <label class="row" style="gap:10px;align-items:center">
          <span style="width:120px">转出费用（元）</span>
          <NInputNumber :value="cfg.push_cost_cents / 100" :min="0" :precision="2" style="width:160px" @update:value="(v: number | null) => cfg.push_cost_cents = Math.round((v || 0) * 100)" />
        </label>
        <label class="row" style="gap:10px;align-items:center">
          <span style="width:120px">转入费用（元）</span>
          <NInputNumber :value="cfg.pull_cost_cents / 100" :min="0" :precision="2" style="width:160px" @update:value="(v: number | null) => cfg.pull_cost_cents = Math.round((v || 0) * 100)" />
        </label>
        <label class="row" style="gap:10px;align-items:center">
          <span style="width:120px">保护期（天）</span>
          <NInputNumber v-model:value="cfg.protection_period_days" :min="0" :max="3650" style="width:160px" />
          <span class="muted" style="font-size:12px">产品订购后多久才能转移，0 = 不限制</span>
        </label>
        <label class="stack" style="gap:6px">
          <span>可自助转移的商品范围</span>
          <NSelect v-model:value="cfg.product_ids" multiple filterable clearable :options="productOptions" placeholder="不选 = 全部商品都可自助转移" />
        </label>
        <div><NButton type="primary" :loading="saving" @click="saveConfig">保存配置</NButton></div>
      </div>
    </section>

    <section class="panel">
      <div class="panel-title-row"><div><h2>转移列表</h2><span>关键词匹配商品名称 / 产品ID / 双方邮箱</span></div></div>
      <div class="users-toolbar">
        <NInput v-model:value="keyword" clearable placeholder="商品 / 产品ID / 邮箱" style="max-width:260px" @keyup.enter="loadRecords" />
        <NSelect v-model:value="statusFilter" clearable :options="[{ label: '待接收', value: 1 }, { label: '已完成', value: 2 }, { label: '已关闭', value: 3 }, { label: '已拒绝', value: 4 }]" placeholder="状态" style="max-width:140px" @update:value="loadRecords" />
        <NButton type="primary" :loading="busy" @click="loadRecords">查询</NButton>
        <span class="muted" style="align-self:center">共 {{ total }} 条</span>
      </div>
      <div v-if="records.length" class="table-scroll"><div class="user-table">
        <div class="user-row divert-row">
          <span>产品</span><span>转出方</span><span>转入方</span><span>发起时间</span><span>完成时间</span><span>费用</span><span>状态</span>
        </div>
        <div v-for="r in records" :key="r.public_id" class="user-row divert-row">
          <span><b>{{ r.product_name }}</b><small class="muted"> {{ String(r.service_id || '').slice(0, 8) }}</small></span>
          <span class="muted">{{ r.push_email }}<small>#{{ r.push_uid }}</small></span>
          <span class="muted">{{ r.pull_email }}<small>#{{ r.pull_uid }}</small></span>
          <span class="muted">{{ fmt(r.created_at) }}</span>
          <span class="muted">{{ fmt(r.end_at) }}</span>
          <span class="muted">转出 {{ money(r.push_cost_cents) }} / 转入 {{ money(r.pull_cost_cents) }}</span>
          <span><NTag :type="statusType(r.status)" size="tiny" round>{{ statusText[r.status] || r.status }}</NTag></span>
        </div>
      </div></div>
      <div v-else class="empty-box">没有匹配的转移记录。</div>
    </section>
  </div>
</template>

<style scoped>
.divert-row { grid-template-columns: minmax(180px, 1.2fr) minmax(160px, .9fr) minmax(160px, .9fr) 150px 150px 170px 90px; }
</style>
