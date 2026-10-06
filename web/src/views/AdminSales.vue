<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NModal, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import EntityPicker from '../components/EntityPicker.vue'

// 业务经理（对齐魔方 CBAP IdcsmartSale 插件）。
// 四个页签：成员管理（销售 = 后台账号 + 姓名 / 编号 / 邮箱）、用户绑定、
// 提成设置（全局 / 商品级：新购 / 续费 / 复购 / 升降级，固定金额或比例；
// 大额订单奖励 + 确认天数）、统计（销售汇总 + 消费排名 + 提成详情）。
// 插件的「任务奖励」与「充值提成」前端契约不足，不编造。

const message = useMessage()
const tab = ref<'sales' | 'clients' | 'settings' | 'stats'>('sales')
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string | null) => (v ? new Date(v).toLocaleString() : '—')
const modeText = (m: string, v: number) => (m === 'percent' ? `${(v / 100).toFixed(2)}%` : money(v))

// ---- 成员管理 ----
const sales = ref<any[]>([])
const salesKeyword = ref('')
const salesBusy = ref(false)
const saleDialog = ref(false)
const saleEditing = ref('')
const saleForm = reactive({ admin_uid: null as number | null, admin_name: '', name: '', num: '', email: '' })

async function loadSales() {
  salesBusy.value = true
  try {
    sales.value = (dataOf<any>(await api.get('/admin/sales', { params: { keyword: salesKeyword.value.trim() } }))?.list) || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取销售成员失败')
  } finally {
    salesBusy.value = false
  }
}
function openSale(s?: any) {
  saleEditing.value = s?.id || ''
  saleForm.admin_uid = s?.admin_uid || null
  saleForm.admin_name = s?.admin_name || ''
  saleForm.name = s?.name || ''
  saleForm.num = s?.num || ''
  saleForm.email = s?.email || ''
  saleDialog.value = true
}
async function saveSale() {
  if (!saleForm.name.trim() || !saleForm.admin_uid) { message.error('请填写销售姓名并选择后台账号'); return }
  salesBusy.value = true
  try {
    const payload = { ...saleForm, name: saleForm.name.trim() }
    if (saleEditing.value) await api.put(`/admin/sales/${saleEditing.value}`, payload)
    else await api.post('/admin/sales', payload)
    message.success('销售成员已保存')
    saleDialog.value = false
    await loadSales()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    salesBusy.value = false
  }
}
async function toggleSale(s: any) {
  try {
    await api.put(`/admin/sales/${s.id}/status`, { active: !s.active })
    await loadSales()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}
async function removeSale(s: any) {
  try {
    await api.delete(`/admin/sales/${s.id}`)
    message.success('销售成员已删除')
    await loadSales()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 用户绑定 ----
const clients = ref<any[]>([])
const clientKeyword = ref('')
const clientSaleFilter = ref<string | null>(null)
async function loadClients() {
  try {
    clients.value = (dataOf<any>(await api.get('/admin/sale-clients', { params: { sale_id: clientSaleFilter.value || '', keyword: clientKeyword.value.trim() } }))?.list) || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取用户绑定失败')
  }
}
const bindSale = ref('')
const bindUser = ref('')
const salesPickUser = ref<any>(null)
async function bind() {
  if (!bindSale.value || !bindUser.value) { message.error('请选择销售与用户'); return }
  try {
    const user = salesPickUser.value
    await api.post('/admin/sale-clients', { sale_id: bindSale.value, user_uid: user?.uid })
    message.success('已绑定')
    bindUser.value = ''
    bindSale.value = ''
    await loadClients()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '绑定失败')
  }
}
async function unbind(c: any) {
  try {
    await api.delete(`/admin/sale-clients/${c.user_uid}`)
    message.success('已解除绑定')
    await loadClients()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}

// ---- 提成设置 ----
const settings = reactive({ confirm_wait_day: 0, big_min: 0, big_max: 0, big_mode: 'fixed', big_value: 0 })
const configs = ref<any[]>([])
const products = ref<any[]>([])
const configDialog = ref(false)
const modeOptions = [{ label: '固定金额（元）', value: 'fixed' }, { label: '比例（%）', value: 'percent' }]
const bindUserTmp = ref('')
const configForm = reactive({
  scope: 'global', product_id: '', active: true,
  new_mode: 'fixed', new_value: 0, renew_mode: 'fixed', renew_value: 0,
  repurchase_mode: 'fixed', repurchase_value: 0, upgrade_mode: 'fixed', upgrade_value: 0,
})

async function loadSettings() {
  try {
    const d = dataOf<any>(await api.get('/admin/sale-settings'))
    settings.confirm_wait_day = Number(d?.confirm_wait_day || 0)
    settings.big_min = Number(d?.big_min || 0)
    settings.big_max = Number(d?.big_max || 0)
    settings.big_mode = d?.big_mode || 'fixed'
    settings.big_value = Number(d?.big_value || 0)
  } catch { /* 默认值 */ }
  try {
    configs.value = (dataOf<any>(await api.get('/admin/sale-commission-configs'))?.list) || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取提成规则失败')
  }
}
async function saveSettings() {
  try {
    await api.put('/admin/sale-settings', settings)
    message.success('销售设置已保存')
    await loadSettings()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
async function loadProducts() {
  try {
    products.value = dataOf<any[]>(await api.get('/admin/products')) || []
  } catch { products.value = [] }
}
const productOptions = computed(() => products.value.map(p => ({ label: p.name, value: p.id })))
function openConfig() {
  Object.assign(configForm, {
    scope: 'global', product_id: '', active: true,
    new_mode: 'fixed', new_value: 0, renew_mode: 'fixed', renew_value: 0,
    repurchase_mode: 'fixed', repurchase_value: 0, upgrade_mode: 'fixed', upgrade_value: 0,
  })
  configDialog.value = true
}
function editConfig(c: any) {
  Object.assign(configForm, {
    scope: c.scope, product_id: c.product_id || '', active: !!c.active,
    new_mode: c.new_mode, new_value: modeIn(c.new_mode, c.new_value),
    renew_mode: c.renew_mode, renew_value: modeIn(c.renew_mode, c.renew_value),
    repurchase_mode: c.repurchase_mode, repurchase_value: modeIn(c.repurchase_mode, c.repurchase_value),
    upgrade_mode: c.upgrade_mode, upgrade_value: modeIn(c.upgrade_mode, c.upgrade_value),
  })
}
const modeIn = (m: string, v: number) => (m === 'percent' ? v / 100 : v / 100)
async function saveConfig() {
  try {
    await api.post('/admin/sale-commission-configs', configForm)
    message.success('提成规则已保存')
    configDialog.value = false
    await loadSettings()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
async function removeConfig(c: any) {
  try {
    await api.delete(`/admin/sale-commission-configs/${c.id}`)
    message.success('提成规则已删除')
    await loadSettings()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 统计 ----
const stats = ref<any[]>([])
const ranking = ref<any[]>([])
const commissions = ref<any[]>([])
const commissionTotal = ref(0)
const statsRange = ref<[number, number] | null>(null)
const commissionFilter = reactive({ sale_id: '', type: '', status: '' })

const rangeParams = () => {
  const end = new Date()
  const start = new Date(end.getTime() - 30 * 86400000)
  if (statsRange.value) return { start: new Date(statsRange.value[0]).toISOString(), end: new Date(statsRange.value[1]).toISOString() }
  return { start: start.toISOString(), end: end.toISOString() }
}
async function loadStats() {
  const p = rangeParams()
  try {
    const [a, b] = await Promise.all([
      api.get('/admin/sale/statistics', { params: p }),
      api.get('/admin/sale/client-ranking', { params: p }),
    ])
    stats.value = dataOf<any>(a)?.list || []
    ranking.value = dataOf<any>(b)?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取统计失败')
  }
}
const typeText: Record<string, string> = { new: '新购', renew: '续费', repurchase: '复购', upgrade: '升降级', big_order: '大额订单' }
const statusText: Record<string, string> = { pending: '待确认', active: '已生效', invalid: '无效' }
const statusType = (s: string) => ({ pending: 'warning', active: 'success', invalid: 'error' } as any)[s] || 'default'
async function loadCommissions() {
  try {
    const d = dataOf<any>(await api.get('/admin/sale-commissions', {
      params: { sale_id: commissionFilter.sale_id, type: commissionFilter.type, status: commissionFilter.status, limit: 100 },
    }))
    commissions.value = d?.list || []
    commissionTotal.value = d?.count || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取提成记录失败')
  }
}
async function invalidateCommission(row: any) {
  try {
    await api.put(`/admin/sale-commissions/${row.id}/invalid`, {})
    message.success('已置为无效')
    await loadCommissions()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}

onMounted(() => { loadSales(); loadClients(); loadSettings(); loadProducts(); loadStats(); loadCommissions() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>业务经理</h1>
        <p>对齐魔方「销售系统」插件：销售成员（挂后台账号）、用户绑定、提成规则（新购 / 续费 / 复购 / 升降级 + 大额订单奖励）、销售统计与提成详情。支付成功自动按规则记提成，确认天数后生效。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary @click="tab === 'sales' ? loadSales() : tab === 'clients' ? loadClients() : tab === 'settings' ? loadSettings() : loadStats()">刷新</NButton>
      </div>
    </div>

    <div class="row" style="gap:8px;margin-bottom:12px">
      <NButton :type="tab === 'sales' ? 'primary' : 'default'" @click="tab = 'sales'">成员管理</NButton>
      <NButton :type="tab === 'clients' ? 'primary' : 'default'" @click="tab = 'clients'">用户绑定</NButton>
      <NButton :type="tab === 'settings' ? 'primary' : 'default'" @click="tab = 'settings'">提成设置</NButton>
      <NButton :type="tab === 'stats' ? 'primary' : 'default'" @click="tab = 'stats'">统计</NButton>
    </div>

    <!-- 成员管理 -->
    <section v-if="tab === 'sales'" class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="salesKeyword" clearable placeholder="姓名 / 编号 / 邮箱" style="max-width:220px" @keyup.enter="loadSales" />
        <NButton type="primary" :loading="salesBusy" @click="loadSales">查询</NButton>
        <NButton type="primary" secondary @click="openSale()">＋ 添加销售</NButton>
      </div>
      <div v-if="sales.length" class="table-scroll"><div class="user-table">
        <div class="user-row sale-row user-head">
          <span>销售姓名</span><span>销售编号</span><span>后台账号</span><span>绑定客户</span><span>状态</span><span>操作</span>
        </div>
        <div v-for="s in sales" :key="s.id" class="user-row sale-row">
          <span><b>{{ s.name }}</b></span>
          <span class="muted">{{ s.num || '—' }}</span>
          <span class="muted">{{ s.admin_name }} #{{ s.admin_uid }}</span>
          <span class="muted">{{ s.client_num }}</span>
          <span><NSwitch size="small" :value="!!s.active" @update:value="toggleSale(s)" /></span>
          <span class="row" style="gap:4px">
            <NButton size="tiny" tertiary @click="openSale(s)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeSale(s)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有销售成员，点击「添加销售」创建。</div>
    </section>

    <!-- 用户绑定 -->
    <section v-else-if="tab === 'clients'" class="panel">
      <div class="users-toolbar">
        <NSelect v-model:value="bindSale" :options="sales.filter(s => s.active).map(s => ({ label: s.name, value: s.id }))" placeholder="选择销售" clearable style="width:160px" />
        <EntityPicker v-model="bindUser" kind="user" placeholder="点击搜索用户" title="搜索用户" @picked="(u: any) => (salesPickUser = u)" />
        <NButton type="primary" @click="bind">绑定</NButton>
        <NInput v-model:value="clientKeyword" clearable placeholder="用户邮箱 / UID" style="max-width:200px" @keyup.enter="loadClients" />
        <NButton secondary @click="loadClients">查询</NButton>
      </div>
      <div v-if="clients.length" class="table-scroll"><div class="user-table">
        <div class="user-row sale-client-row user-head">
          <span>用户</span><span>所属销售</span><span>绑定时间</span><span>操作</span>
        </div>
        <div v-for="c in clients" :key="c.user_uid" class="user-row sale-client-row">
          <span><b>{{ c.user_email }}</b><small class="muted"> #{{ c.user_uid }}</small></span>
          <span class="muted">{{ c.sale_name }}</span>
          <span class="muted">{{ fmt(c.created_at) }}</span>
          <span><NButton size="tiny" tertiary type="error" @click="unbind(c)">解除绑定</NButton></span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有绑定客户。</div>
    </section>

    <!-- 提成设置 -->
    <section v-else-if="tab === 'settings'" class="panel stack">
      <div class="panel-title-row"><div><h2>销售设置</h2><span>提成确认天数与大额订单奖励</span></div></div>
      <div class="form-grid">
        <label><span>确认天数（0 = 立即生效）</span><NInputNumber v-model:value="settings.confirm_wait_day" :min="0" :precision="0" style="width:100%" /></label>
        <label><span>大额订单下限（元）</span><NInputNumber v-model:value="settings.big_min" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>大额订单上限（元，0 = 不限）</span><NInputNumber v-model:value="settings.big_max" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>奖励方式</span>
          <NSelect v-model:value="settings.big_mode" :options="[{ label: '固定金额（元）', value: 'fixed' }, { label: '按订单比例（%）', value: 'percent' }]" />
        </label>
        <label><span>奖励值</span><NInputNumber v-model:value="settings.big_value" :min="0" :precision="2" style="width:100%" /></label>
        <NButton type="primary" style="align-self:end" @click="saveSettings">保存设置</NButton>
      </div>
      <div class="panel-title-row" style="margin-top:10px"><div><h2>提成规则</h2><span>商品级规则优先于全局兜底；比例按基数百分比</span></div>
        <NButton type="primary" @click="openConfig">＋ 添加规则</NButton></div>
      <div v-if="configs.length" class="table-scroll"><div class="user-table">
        <div class="user-row sale-config-row user-head">
          <span>范围</span><span>新购</span><span>续费</span><span>复购</span><span>升降级</span><span>状态</span><span>操作</span>
        </div>
        <div v-for="c in configs" :key="c.id" class="user-row sale-config-row">
          <span><b>{{ c.scope === 'global' ? '全局' : c.product_name }}</b></span>
          <span class="muted">{{ modeText(c.new_mode, c.new_value) }}</span>
          <span class="muted">{{ modeText(c.renew_mode, c.renew_value) }}</span>
          <span class="muted">{{ modeText(c.repurchase_mode, c.repurchase_value) }}</span>
          <span class="muted">{{ modeText(c.upgrade_mode, c.upgrade_value) }}</span>
          <span><NTag :type="c.active ? 'success' : 'default'" size="tiny" round>{{ c.active ? '启用' : '停用' }}</NTag></span>
          <span class="row" style="gap:4px">
            <NButton size="tiny" tertiary @click="() => { editConfig(c); configDialog = true }">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeConfig(c)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有提成规则。</div>
    </section>

    <!-- 统计 -->
    <section v-else class="panel stack">
      <div class="panel-title-row"><div><h2>销售汇总（近 30 天）</h2><span>订单数 / 销售额 / 提成</span></div></div>
      <div v-if="stats.length" class="table-scroll"><div class="user-table">
        <div class="user-row sale-stat-row user-head">
          <span>销售</span><span>订单数</span><span>销售额</span><span>已生效提成</span><span>待确认提成</span>
        </div>
        <div v-for="r in stats" :key="r.sale_id" class="user-row sale-stat-row">
          <span><b>{{ r.sale_name }}</b></span>
          <span class="muted">{{ r.order_count }}</span>
          <span>{{ money(r.sales_cents) }}</span>
          <span class="muted">{{ money(r.active_cents) }}</span>
          <span class="muted">{{ money(r.pending_cents) }}</span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有销售统计数据。</div>
      <div class="panel-title-row" style="margin-top:10px"><div><h2>消费总金额排名</h2><span>按销售名下客户消费汇总</span></div></div>
      <div v-if="ranking.length" class="table-scroll"><div class="user-table">
        <div class="user-row sale-rank-row user-head"><span>名次</span><span>销售</span><span>客户数</span><span>消费总金额</span></div>
        <div v-for="(r, i) in ranking" :key="r.sale_id" class="user-row sale-rank-row">
          <span><b>{{ i + 1 }}</b></span>
          <span>{{ r.sale_name }}</span>
          <span class="muted">{{ r.client_count }}</span>
          <span>{{ money(r.sales_cents) }}</span>
        </div>
      </div></div>
      <div class="panel-title-row" style="margin-top:10px"><div><h2>提成详情</h2><span>共 {{ commissionTotal }} 条</span></div>
        <NButton secondary @click="loadCommissions">刷新</NButton></div>
      <div class="users-toolbar">
        <NSelect v-model:value="commissionFilter.sale_id" :options="sales.map(s => ({ label: s.name, value: s.id }))" placeholder="销售" clearable style="width:150px" @update:value="loadCommissions" />
        <NSelect v-model:value="commissionFilter.type" :options="Object.entries(typeText).map(([v, l]) => ({ label: l, value: v }))" placeholder="类型" clearable style="width:130px" @update:value="loadCommissions" />
        <NSelect v-model:value="commissionFilter.status" :options="Object.entries(statusText).map(([v, l]) => ({ label: l, value: v }))" placeholder="状态" clearable style="width:130px" @update:value="loadCommissions" />
      </div>
      <div v-if="commissions.length" class="table-scroll"><div class="user-table">
        <div class="user-row sale-comm-row user-head">
          <span>时间</span><span>销售</span><span>客户</span><span>订单</span><span>类型</span><span>基数</span><span>提成</span><span>状态</span><span>操作</span>
        </div>
        <div v-for="r in commissions" :key="r.id" class="user-row sale-comm-row">
          <span class="muted">{{ fmt(r.created_at) }}</span>
          <span>{{ r.sale_name }}</span>
          <span class="muted">{{ r.user_email }}</span>
          <span class="muted">{{ (r.order_id || '—').slice(0, 8) }}</span>
          <span>{{ typeText[r.type] || r.type }}</span>
          <span class="muted">{{ money(r.base_cents) }}<small>（{{ modeText(r.mode, r.value) }}）</small></span>
          <span><b>{{ money(r.amount_cents) }}</b></span>
          <span><NTag :type="statusType(r.status)" size="tiny" round>{{ statusText[r.status] || r.status }}</NTag></span>
          <span v-if="r.status !== 'invalid'"><NButton size="tiny" tertiary type="error" @click="invalidateCommission(r)">置无效</NButton></span>
          <span v-else class="muted">—</span>
        </div>
      </div></div>
      <div v-else class="empty-box">没有匹配的提成记录。</div>
    </section>

    <!-- 销售成员表单 -->
    <NModal v-model:show="saleDialog" preset="card" :title="saleEditing ? '编辑销售' : '添加销售'" style="width:min(520px,94vw)">
      <div class="form-grid">
        <label class="full"><span>后台账号</span>
          <EntityPicker v-if="!saleEditing" v-model="bindUserTmp" kind="user" placeholder="点击搜索后台账号" title="搜索用户" @picked="(u: any) => { saleForm.admin_uid = u?.uid || null; bindUserTmp = u?.id || '' }" />
          <NInput v-else :value="saleForm.admin_name || ''" disabled />
        </label>
        <label><span>销售姓名</span><NInput v-model:value="saleForm.name" placeholder="销售姓名" /></label>
        <label><span>销售编号</span><NInput v-model:value="saleForm.num" placeholder="销售编号" /></label>
        <label class="full"><span>邮箱</span><NInput v-model:value="saleForm.email" placeholder="邮箱" /></label>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" :loading="salesBusy" @click="saveSale">保存</NButton>
        <NButton secondary @click="saleDialog = false">取消</NButton>
      </div>
    </NModal>

    <!-- 提成规则表单 -->
    <NModal v-model:show="configDialog" preset="card" title="提成规则" style="width:min(640px,94vw)">
      <div class="form-grid">
        <label><span>范围</span>
          <NSelect v-model:value="configForm.scope" :options="[{ label: '全局', value: 'global' }, { label: '指定商品', value: 'product' }]" />
        </label>
        <label v-if="configForm.scope === 'product'"><span>商品</span>
          <NSelect v-model:value="configForm.product_id" :options="productOptions" filterable placeholder="选择商品" />
        </label>
        <label class="row" style="gap:8px;align-items:center"><NSwitch v-model:value="configForm.active" /><span style="font-size:13px">启用</span></label>
        <label><span>新购方式</span><NSelect v-model:value="configForm.new_mode" :options="modeOptions" /></label>
        <label><span>新购值</span><NInputNumber v-model:value="configForm.new_value" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>续费方式</span><NSelect v-model:value="configForm.renew_mode" :options="modeOptions" /></label>
        <label><span>续费值</span><NInputNumber v-model:value="configForm.renew_value" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>复购方式</span><NSelect v-model:value="configForm.repurchase_mode" :options="modeOptions" /></label>
        <label><span>复购值</span><NInputNumber v-model:value="configForm.repurchase_value" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>升降级方式</span><NSelect v-model:value="configForm.upgrade_mode" :options="modeOptions" /></label>
        <label><span>升降级值</span><NInputNumber v-model:value="configForm.upgrade_value" :min="0" :precision="2" style="width:100%" /></label>
      </div>
      <div class="security-note">固定金额以元填写；比例以 % 填写（如 5 表示 5%）。值为 0 表示该类型不提成。</div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" @click="saveConfig">保存</NButton>
        <NButton secondary @click="configDialog = false">取消</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.sale-row { grid-template-columns: minmax(110px, .9fr) 90px minmax(150px, 1fr) 80px 70px 120px; }
.sale-client-row { grid-template-columns: minmax(180px, 1.2fr) minmax(120px, .8fr) 150px 100px; }
.sale-config-row { grid-template-columns: minmax(120px, .9fr) repeat(4, minmax(80px, .7fr)) 70px 110px; }
.sale-stat-row { grid-template-columns: minmax(120px, 1fr) 80px minmax(110px, .9fr) minmax(110px, .9fr) minmax(110px, .9fr); }
.sale-rank-row { grid-template-columns: 60px minmax(120px, 1fr) 80px minmax(120px, .9fr); }
.sale-comm-row { grid-template-columns: 150px 90px minmax(140px, 1fr) 90px 80px minmax(120px, .9fr) minmax(90px, .7fr) 80px 70px; }
</style>
