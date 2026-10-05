<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NModal, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 流量包（对齐魔方 CBAP FlowPacket 插件）。
// 两个页签与插件一致：流量包订单（列表 / 删除）、流量包管理（增删改查 / 启停 / 库存开关）。

const message = useMessage()
const tab = ref<'orders' | 'manage'>('orders')

// ---- 流量包订单 ----
const orders = ref<any[]>([])
const orderTotal = ref(0)
const orderKeyword = ref('')
const orderStatus = ref<string | null>(null)
const ordersBusy = ref(false)
const orderStatusOptions = [
  { label: '未付款', value: 'unpaid' },
  { label: '已付款', value: 'paid' },
  { label: '已取消', value: 'cancelled' },
  { label: '已退款', value: 'refunded' },
]
const statusText: Record<string, string> = { unpaid: '未付款', paid: '已付款', cancelled: '已取消', refunded: '已退款' }
const statusType = (s: string) => ({ paid: 'success', unpaid: 'warning' } as any)[s] || 'default'
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const shortID = (id?: string) => (id ? String(id).slice(0, 8) : '—')

async function loadOrders() {
  ordersBusy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/flow-packet-orders', {
      params: { keyword: orderKeyword.value.trim(), status: orderStatus.value || '', limit: 100 },
    }))
    orders.value = d?.list || []
    orderTotal.value = d?.count || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取流量包订单失败')
  } finally {
    ordersBusy.value = false
  }
}
async function removeOrder(r: any) {
  try {
    await api.delete(`/admin/flow-packet-orders/${r.public_id}`)
    message.success('已删除')
    await loadOrders()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 流量包管理 ----
const packets = ref<any[]>([])
const products = ref<any[]>([])
const listKeyword = ref('')
const listStatus = ref<string | null>(null)
const packetsBusy = ref(false)
const formOpen = ref(false)
const editingID = ref('')
const form = reactive({
  name: '',
  capacity_gb: 0,
  price: 0,
  stock: 0,
  stock_enable: false,
  product_ids: [] as string[],
  notes: '',
})

const productOptions = computed(() => products.value.map(p => ({ label: p.name, value: p.id })))
const packetProductsText = (p: any) => (p.products || []).map((x: any) => x.name).join(' / ') || '—'
const stockText = (p: any) => (p.stock_enable ? String(p.stock) : '不限')

async function loadProducts() {
  try {
    products.value = dataOf(await api.get('/admin/products')) || []
  } catch {
    products.value = []
  }
}
async function loadPackets() {
  packetsBusy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/flow-packets', {
      params: { keyword: listKeyword.value.trim(), status: listStatus.value || '', limit: 100 },
    }))
    packets.value = d?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取流量包失败')
  } finally {
    packetsBusy.value = false
  }
}
function resetForm() {
  editingID.value = ''
  form.name = ''
  form.capacity_gb = 0
  form.price = 0
  form.stock = 0
  form.stock_enable = false
  form.product_ids = []
  form.notes = ''
}
function openCreate() {
  resetForm()
  formOpen.value = true
}
function openEdit(p: any) {
  editingID.value = p.public_id
  form.name = p.name
  form.capacity_gb = Number(p.capacity_gb || 0)
  form.price = Number(p.price_cents || 0) / 100
  form.stock = Number(p.stock || 0)
  form.stock_enable = !!p.stock_enable
  form.product_ids = (p.products || []).map((x: any) => x.id)
  form.notes = p.notes || ''
  formOpen.value = true
}
async function save() {
  if (!form.name.trim()) { message.error('请填写流量包名称'); return }
  if (!form.product_ids.length) { message.error('请至少选择一个关联商品'); return }
  packetsBusy.value = true
  try {
    const payload = {
      name: form.name.trim(),
      capacity_gb: Math.max(0, Math.round(form.capacity_gb || 0)),
      price_cents: Math.round((form.price || 0) * 100),
      stock: Math.max(0, Math.round(form.stock || 0)),
      stock_enable: form.stock_enable,
      product_ids: form.product_ids,
      notes: form.notes,
    }
    if (editingID.value) await api.put(`/admin/flow-packets/${editingID.value}`, payload)
    else await api.post('/admin/flow-packets', payload)
    message.success(editingID.value ? '流量包已更新' : '流量包已创建')
    formOpen.value = false
    await loadPackets()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    packetsBusy.value = false
  }
}
async function toggle(p: any) {
  try {
    await api.put(`/admin/flow-packets/${p.public_id}/status`, { active: !p.active })
    message.success(p.active ? '已关闭' : '已开启')
    await loadPackets()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}
async function removePacket(p: any) {
  try {
    await api.delete(`/admin/flow-packets/${p.public_id}`)
    message.success('已删除')
    await loadPackets()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}
onMounted(() => { loadOrders(); loadPackets(); loadProducts() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>流量包</h1>
        <p>对齐魔方「流量包」插件：维护流量包（名称 / 流量 / 售价 / 库存 / 关联商品）并查看购买订单；用户端在「流量包」页为名下关联产品下单，余额支付。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary :loading="tab === 'orders' ? ordersBusy : packetsBusy" @click="tab === 'orders' ? loadOrders() : loadPackets()">刷新</NButton>
      </div>
    </div>

    <div class="row" style="gap:8px;margin-bottom:12px">
      <NButton :type="tab === 'orders' ? 'primary' : 'default'" @click="tab = 'orders'">流量包订单</NButton>
      <NButton :type="tab === 'manage' ? 'primary' : 'default'" @click="tab = 'manage'">流量包管理</NButton>
    </div>

    <section v-if="tab === 'orders'" class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="orderKeyword" clearable placeholder="用户邮箱 / 流量包名称 / 产品ID" style="max-width:300px" @keyup.enter="loadOrders" />
        <NSelect v-model:value="orderStatus" :options="orderStatusOptions" placeholder="支付状态" clearable style="width:140px" />
        <NButton type="primary" :loading="ordersBusy" @click="loadOrders">查询</NButton>
        <span class="muted" style="align-self:center">共 {{ orderTotal }} 条</span>
      </div>

      <div v-if="orders.length" class="table-scroll"><div class="user-table">
        <div class="user-row fp-order-row user-head">
          <span>ID</span><span>用户</span><span>流量包</span><span>关联产品</span><span>下单时间</span><span>下单金额</span><span>支付状态</span><span>操作</span>
        </div>
        <div v-for="o in orders" :key="o.public_id" class="user-row fp-order-row">
          <span class="muted">{{ o.id }}</span>
          <span><b>{{ o.username }}</b><small class="muted">#{{ o.user_uid }}</small></span>
          <span>{{ o.packet_name }}<small class="muted"> {{ o.capacity_gb }}GB</small></span>
          <span class="muted">{{ o.service_name }}<small>（{{ shortID(o.service_id) }}）</small></span>
          <span class="muted">{{ fmt(o.created_at) }}</span>
          <span>{{ money(o.amount_cents) }}</span>
          <span><NTag :type="statusType(o.status)" size="tiny" round>{{ statusText[o.status] || o.status }}</NTag></span>
          <span><NButton size="tiny" tertiary type="error" @click="removeOrder(o)">删除</NButton></span>
        </div>
      </div></div>
      <div v-else class="empty-box">没有匹配的流量包订单。</div>
    </section>

    <section v-else class="panel">
      <div class="users-toolbar">
        <NButton type="primary" @click="openCreate">＋ 新增流量包</NButton>
        <NInput v-model:value="listKeyword" clearable placeholder="名称 / 备注" style="max-width:240px" @keyup.enter="loadPackets" />
        <NSelect v-model:value="listStatus" :options="[{ label: '开启', value: '1' }, { label: '关闭', value: '0' }]" placeholder="状态" clearable style="width:120px" />
        <NButton secondary :loading="packetsBusy" @click="loadPackets">查询</NButton>
      </div>

      <div v-if="packets.length" class="table-scroll"><div class="user-table">
        <div class="user-row fp-packet-row user-head">
          <span>ID</span><span>名称</span><span>流量</span><span>售价</span><span>关联商品</span><span>状态</span><span>库存</span><span>备注</span><span>操作</span>
        </div>
        <div v-for="p in packets" :key="p.public_id" class="user-row fp-packet-row">
          <span class="muted">{{ p.id }}</span>
          <span><b>{{ p.name }}</b></span>
          <span>{{ p.capacity_gb }} GB</span>
          <span>{{ money(p.price_cents) }}</span>
          <span class="muted">{{ packetProductsText(p) }}</span>
          <span><NSwitch size="small" :value="!!p.active" @update:value="toggle(p)" /></span>
          <span :class="{ muted: !p.stock_enable }">{{ stockText(p) }}</span>
          <span class="muted">{{ p.notes || '—' }}</span>
          <span class="row" style="gap:6px">
            <NButton size="tiny" tertiary @click="openEdit(p)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removePacket(p)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有流量包，点击「新增流量包」创建。</div>
    </section>

    <NModal v-model:show="formOpen" preset="card" :title="editingID ? '编辑流量包' : '新增流量包'" style="width:min(560px,94vw)">
      <div class="form-grid">
        <label class="full"><span>名称</span><NInput v-model:value="form.name" placeholder="流量包名称" /></label>
        <label><span>流量（GB）</span><NInputNumber v-model:value="form.capacity_gb" :min="0" :precision="0" style="width:100%" /></label>
        <label><span>售价（元）</span><NInputNumber v-model:value="form.price" :min="0" :precision="2" style="width:100%" /></label>
        <label class="full"><span>可用库存（右侧开关关闭时表示不限库存）</span>
          <div class="row" style="gap:10px;align-items:center">
            <NInputNumber v-model:value="form.stock" :min="form.stock_enable ? 1 : 0" :precision="0" style="flex:1" />
            <NSwitch v-model:value="form.stock_enable" />
            <span class="muted" style="font-size:12px">{{ form.stock_enable ? '按库存售卖，售罄后不可下单' : '不限库存' }}</span>
          </div>
        </label>
        <label class="full"><span>关联商品（可多选）</span>
          <NSelect v-model:value="form.product_ids" :options="productOptions" filterable multiple clearable placeholder="选择可用此流量包的商品" />
        </label>
        <label class="full"><span>备注</span><NInput v-model:value="form.notes" type="textarea" :rows="3" placeholder="备注" /></label>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" :loading="packetsBusy" @click="save">{{ editingID ? '保存修改' : '添加' }}</NButton>
        <NButton secondary @click="formOpen = false">取消</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.fp-order-row { grid-template-columns: 60px minmax(150px, .9fr) minmax(140px, 1fr) minmax(150px, 1fr) 150px 110px 90px 70px; }
.fp-packet-row { grid-template-columns: 60px minmax(130px, .9fr) 80px 100px minmax(180px, 1.2fr) 80px 70px minmax(120px, .8fr) 130px; }
</style>
