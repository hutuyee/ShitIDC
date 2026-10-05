<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInputNumber, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const items = ref<any[]>([])
const products = ref<any[]>([])
const busy = ref(false)
const form = reactive({ product_id: '', price: 10, period_days: 0 })
const editingID = ref('')

const productOptions = computed(() => products.value.map(p => ({ label: p.name, value: p.id })))
const productName = (r: any) => r.product_name || productOptions.value.find(o => o.value === r.product_public_id)?.label || '—'
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const periodText = (d: number) => (Number(d || 0) === 0 ? '永久' : `购买后 ${d} 天内`)

async function load() {
  try { items.value = dataOf(await api.get('/admin/product-cashbacks')) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取商品返现失败') }
}
async function loadProducts() {
  try { products.value = dataOf(await api.get('/admin/products')) }
  catch { products.value = [] }
}

function reset() {
  editingID.value = ''
  form.product_id = ''
  form.price = 10
  form.period_days = 0
}
function edit(r: any) {
  editingID.value = r.id
  form.product_id = r.product_public_id
  form.price = Number(r.price_cents || 0) / 100
  form.period_days = Number(r.period_days || 0)
}
async function save() {
  if (!form.product_id && !editingID.value) { message.error('请选择商品'); return }
  if (form.price <= 0) { message.error('返现金额必须大于 0'); return }
  busy.value = true
  try {
    const payload = { product_id: form.product_id, type: 'fixed', price_cents: Math.round(form.price * 100), period_days: Math.max(0, Math.round(form.period_days || 0)) }
    if (editingID.value) await api.put(`/admin/product-cashbacks/${editingID.value}`, payload)
    else await api.post('/admin/product-cashbacks', payload)
    message.success(editingID.value ? '返现规则已更新' : '返现规则已创建')
    reset()
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}
async function toggle(r: any) {
  try {
    await api.put(`/admin/product-cashbacks/${r.id}/status`, { active: !r.active })
    message.success(r.active ? '已停用' : '已启用')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}
async function remove(r: any) {
  try {
    await api.delete(`/admin/product-cashbacks/${r.id}`)
    message.success('已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}
onMounted(() => { load(); loadProducts() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">附属插件</div><h1>商品返现</h1><p>对齐魔方「商品返现」插件：购买指定商品并支付成功后，返现金额计入账户余额；返现金额超过该商品实付金额时按实付返现，同一订单只返一次。</p></div></div>

    <div class="admin-two-col">
      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>{{ editingID ? '编辑返现规则' : '新增返现规则' }}</h2><span>返现金额单位为「元」</span></div></div>
        <div class="form-grid">
          <label class="full"><span>商品</span>
            <NSelect v-model:value="form.product_id" filterable :disabled="!!editingID" :options="productOptions" placeholder="选择参与返现的商品" />
          </label>
          <label><span>返现类型</span><NSelect value="fixed" disabled :options="[{ label: '固定金额', value: 'fixed' }]" /></label>
          <label><span>返现金额（元）</span><NInputNumber v-model:value="form.price" :min="0.01" :precision="2" style="width:100%" /></label>
          <label><span>可返现期限（天，0=永久）</span><NInputNumber v-model:value="form.period_days" :min="0" style="width:100%" /></label>
        </div>
        <div class="row" style="gap:8px">
          <NButton type="primary" size="large" :loading="busy" @click="save">{{ editingID ? '保存修改' : '添加' }}</NButton>
          <NButton v-if="editingID" secondary size="large" @click="reset">取消编辑</NButton>
        </div>
      </section>

      <section class="panel">
        <div class="panel-title-row"><div><h2>返现规则</h2><span>{{ items.length }} 条</span></div></div>
        <div v-if="items.length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head"><span>商品名称</span><span>返现类型</span><span>返现</span><span>可返现期限</span><span>状态</span><span>操作</span></div>
          <div v-for="r in items" :key="r.id" class="audit-row">
            <span><b>{{ productName(r) }}</b></span>
            <span>固定金额</span>
            <span>{{ money(r.price_cents) }}</span>
            <span class="muted">{{ periodText(r.period_days) }}</span>
            <span><NTag :type="r.active ? 'success' : 'default'" size="tiny" round>{{ r.active ? '启用' : '停用' }}</NTag></span>
            <span class="row" style="gap:6px">
              <NButton size="tiny" tertiary @click="edit(r)">编辑</NButton>
              <NButton size="tiny" tertiary @click="toggle(r)">{{ r.active ? '停用' : '启用' }}</NButton>
              <NButton size="tiny" tertiary type="error" @click="remove(r)">删除</NButton>
            </span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有返现规则。</div>
      </section>
    </div>
  </div>
</template>
