<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NInput, NInputNumber, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const products = ref<any[]>([])
const groups = ref<any[]>([])
const busy = ref(false)
const editingID = ref('')

const ALL_CYCLES = [
  { key: 'hourly', label: '小时' },
  { key: 'daily', label: '天' },
  { key: 'monthly', label: '月' },
  { key: 'quarterly', label: '季' },
  { key: 'semiannually', label: '半年' },
  { key: 'yearly', label: '年' },
  { key: 'onetime', label: '一次性' },
]
const payTypeOptions = [
  { label: '周期付费', value: 'recurring' },
  { label: '一次性', value: 'onetime' },
  { label: '免费', value: 'free' },
  { label: '试用', value: 'trial' },
]

const form = reactive({
  name: '', description: '', provider_id: '', group_id: '', currency: 'CNY',
  pay_type: 'recurring',
  prices: {} as Record<string, number | null>,
  trial_days: 0, trial_price: 0, auto_terminate_days: 0,
  stock_control: false, stock_qty: 0, allow_qty: true, max_per_customer: 0, is_featured: false,
})

const formProviders = ref<any[]>([])
async function loadFormProviders() {
  try { formProviders.value = dataOf<any[]>(await api.get('/admin/providers')).filter(x => x.active) }
  catch { formProviders.value = [] }
}

const money = (cents: number, currency = 'CNY') => (currency === 'CNY' ? '¥' : currency + ' ') + (Number(cents || 0) / 100).toFixed(2)
const cycleLabel = (k: string) => (ALL_CYCLES.find(c => c.key === k) || ({} as any)).label || k
const payTypeLabel = (v: string) => (payTypeOptions.find(o => o.value === v) || ({} as any)).label || v
const optionTypeLabel = (t: number) => (optionTypeOptions.find(o => o.value === Number(t)) || ({} as any)).label || String(t)

const visibleCycles = computed<string[]>(() => {
  if (form.pay_type === 'onetime') return ['onetime']
  if (form.pay_type === 'free') return ['monthly']
  return ALL_CYCLES.map(c => c.key).filter(k => k !== 'onetime')
})

async function load() {
  try {
    products.value = await api.get(`/admin/products`).then(x => dataOf<any[]>(x))
    groups.value = await api.get(`/admin/product-groups`).then(x => dataOf<any[]>(x))
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取商品失败') }
}

function resetForm() {
  editingID.value = ''
  Object.assign(form, {
    name: '', description: '', provider_id: '', group_id: '', currency: 'CNY', pay_type: 'recurring',
    prices: {}, trial_days: 0, trial_price: 0, auto_terminate_days: 0,
    stock_control: false, stock_qty: 0, allow_qty: true, max_per_customer: 0, is_featured: false,
  })
  configOptions.value = []; customFields.value = []
}

function edit(prod: any) {
  editingID.value = prod.id
  const prices: Record<string, number | null> = {}
  for (const pr of prod.prices || []) prices[pr.billing_cycle] = Number(pr.amount_cents || 0) / 100
  if (!Object.keys(prices).length && prod.billing_cycle) prices[prod.billing_cycle] = Number(prod.price_cents || 0) / 100
  Object.assign(form, {
    name: prod.name || '', description: prod.description || '', provider_id: prod.provider_id || '',
    group_id: prod.group_id || '', currency: prod.currency || 'CNY', pay_type: prod.pay_type || 'recurring',
    prices,
    trial_days: prod.trial_days || 0,
    trial_price: Number(prod.trial_price_cents || 0) / 100,
    auto_terminate_days: prod.auto_terminate_days || 0,
    stock_control: Boolean(prod.stock_control), stock_qty: prod.stock_qty || 0,
    allow_qty: prod.allow_qty !== false, max_per_customer: prod.max_per_customer || 0,
    is_featured: Boolean(prod.is_featured),
  })
  loadProductConfig(prod.id)
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

function buildPrices() {
  const out: { billing_cycle: string; amount_cents: number }[] = []
  for (const k of visibleCycles.value) {
    const v = form.prices[k]
    if (v === null || v === undefined) continue
    out.push({ billing_cycle: k, amount_cents: Math.max(0, Math.round(Number(v) * 100)) })
  }
  if (!out.length) out.push({ billing_cycle: visibleCycles.value[0] || 'monthly', amount_cents: 0 })
  return out
}

async function save() {
  if (!form.name.trim()) { message.error('请填写商品名称'); return }
  const prices = buildPrices()
  if (form.pay_type === 'free' && prices.some(x => x.amount_cents !== 0)) { message.error('免费商品的价格必须为 0'); return }
  if (form.pay_type === 'trial' && form.trial_days <= 0) { message.error('试用商品必须填写试用天数'); return }
  busy.value = true
  try {
    const payload: any = {
      name: form.name, description: form.description, group_id: form.group_id || '',
      currency: form.currency, pay_type: form.pay_type, prices,
      trial_days: form.trial_days, trial_price_cents: Math.round(form.trial_price * 100),
      auto_terminate_days: form.auto_terminate_days,
      stock_control: form.stock_control, stock_qty: form.stock_qty,
      allow_qty: form.allow_qty, max_per_customer: form.max_per_customer, is_featured: form.is_featured,
    }
    if (editingID.value) {
      await api.put(`/admin/products/${editingID.value}`, payload)
      message.success('商品已更新')
    } else {
      payload.provider_id = form.provider_id || ''
      await api.post('/admin/products', payload)
      message.success('商品已创建')
    }
    resetForm(); await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}

async function toggleActive(prod: any) {
  try {
    await api.put(`/admin/products/${prod.id}`, { active: !prod.active })
    message.success(prod.active ? '商品已下架' : '商品已上架')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}

// ---- 配置项（魔方可配置选项）----
const configOptions = ref<any[]>([])
const customFields = ref<any[]>([])
const optForm = reactive({
  name: '', description: '', option_type: 1, required: true, qty_min: 1, qty_max: 10,
  values: [{ label: '', price: 0, setup: 0, is_default: true }] as any[],
})
const fieldForm = reactive({
  name: '', field_key: '', field_type: 'text', options_text: '', description: '', placeholder: '',
  required: false, admin_only: false, show_on_order: true, regex: '',
})

const optionTypeOptions = [
  { label: '下拉选择', value: 1 },
  { label: '单选按钮', value: 2 },
  { label: '是/否开关', value: 3 },
  { label: '数量（乘以单价）', value: 4 },
]
const fieldTypeOptions = [
  { label: '单行文本', value: 'text' },
  { label: '多行文本', value: 'textarea' },
  { label: '下拉选择', value: 'dropdown' },
  { label: '密码', value: 'password' },
]

async function loadProductConfig(productId: string) {
  try {
    configOptions.value = await api.get(`/admin/products/${productId}/config-options`).then(x => dataOf<any[]>(x))
    customFields.value = await api.get(`/admin/products/${productId}/custom-fields`).then(x => dataOf<any[]>(x))
  } catch { configOptions.value = []; customFields.value = [] }
}

function addOptionValue() {
  optForm.values.push({ label: '', price: 0, setup: 0, is_default: false })
}
function removeOptionValue(i: number) {
  if (optForm.values.length <= 1) return
  optForm.values.splice(i, 1)
}

async function createOption() {
  if (!editingID.value) { message.error('请先保存商品，再添加配置项'); return }
  if (!optForm.name.trim()) { message.error('请填写配置项名称'); return }
  const values = optForm.values
    .filter(v => String(v.label).trim() !== '')
    .map((v, i) => ({
      label: String(v.label).trim(),
      price_cents: Math.max(0, Math.round(Number(v.price || 0) * 100)),
      setup_cents: Math.max(0, Math.round(Number(v.setup || 0) * 100)),
      is_default: Boolean(v.is_default),
      sort_weight: i,
    }))
  if (!values.length) { message.error('请至少填写一个候选项'); return }
  if (Number(optForm.option_type) === 4 && values.length !== 1) { message.error('数量型配置项只能有一个计价单位'); return }
  try {
    await api.post(`/admin/products/${editingID.value}/config-options`, {
      name: optForm.name, description: optForm.description, option_type: optForm.option_type,
      required: optForm.required, qty_min: optForm.qty_min, qty_max: optForm.qty_max, values,
    })
    message.success('配置项已添加')
    Object.assign(optForm, { name: '', description: '', option_type: 1, required: true, qty_min: 1, qty_max: 10, values: [{ label: '', price: 0, setup: 0, is_default: true }] })
    await loadProductConfig(editingID.value)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '添加失败') }
}

async function deleteOption(o: any) {
  if (!editingID.value) return
  try {
    await api.delete(`/admin/products/${editingID.value}/config-options/${o.id}`)
    message.success('配置项已删除')
    await loadProductConfig(editingID.value)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

async function createField() {
  if (!editingID.value) { message.error('请先保存商品，再添加自定义字段'); return }
  if (!fieldForm.name.trim() || !fieldForm.field_key.trim()) { message.error('请填写字段名与字段键'); return }
  const options = fieldForm.field_type === 'dropdown'
    ? fieldForm.options_text.split(/[\n,，]/).map((s: string) => s.trim()).filter(Boolean)
    : []
  if (fieldForm.field_type === 'dropdown' && !options.length) { message.error('下拉字段至少要有一个候选项'); return }
  try {
    await api.post(`/admin/products/${editingID.value}/custom-fields`, {
      name: fieldForm.name, field_key: fieldForm.field_key, field_type: fieldForm.field_type,
      options, description: fieldForm.description, placeholder: fieldForm.placeholder,
      required: fieldForm.required, admin_only: fieldForm.admin_only,
      show_on_order: fieldForm.show_on_order, regex: fieldForm.regex,
    })
    message.success('自定义字段已添加')
    Object.assign(fieldForm, { name: '', field_key: '', field_type: 'text', options_text: '', description: '', placeholder: '', required: false, admin_only: false, show_on_order: true, regex: '' })
    await loadProductConfig(editingID.value)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '添加失败') }
}

async function deleteField(f: any) {
  if (!editingID.value) return
  try {
    await api.delete(`/admin/products/${editingID.value}/custom-fields/${f.id}`)
    message.success('字段已删除')
    await loadProductConfig(editingID.value)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---- 产品分组 ----
const groupName = ref('')
const groupWeight = ref(0)
async function createGroup() {
  if (!groupName.value.trim()) { message.error('请填写分组名称'); return }
  try {
    await api.post('/admin/product-groups', { name: groupName.value.trim(), sort_weight: groupWeight.value })
    message.success('分组已创建')
    groupName.value = ''; groupWeight.value = 0
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '创建失败') }
}

async function deleteGroup(g: any) {
  try {
    await api.delete(`/admin/product-groups/${g.id}`)
    message.success('分组已删除，组内商品移至未分组')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

onMounted(() => { load(); loadFormProviders() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">商品管理</div><h1>商品与分组</h1><p>计费类型、周期价格、配置项、自定义字段与库存限购都在这里维护。价格单位为「分」，后端下单时会按最新价格重算。</p></div></div>

    <div class="admin-two-col">
      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>{{ editingID ? '编辑商品' : '创建商品' }}</h2><span>保存后可在下方添加配置项与自定义字段</span></div></div>
        <div class="form-grid">
          <label><span>商品名称</span><NInput v-model:value="form.name" placeholder="例如：香港 VPS 标准型" /></label>
          <label><span>所属分组（可选）</span>
            <select v-model="form.group_id" class="native-select">
              <option value="">未分组</option>
              <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</option>
            </select>
          </label>
          <label><span>计费类型</span><NSelect v-model:value="form.pay_type" :options="payTypeOptions" /></label>
          <label><span>货币</span><NInput v-model:value="form.currency" placeholder="CNY" /></label>
          <label v-if="!editingID"><span>上游供应商（可选）</span>
            <select v-model="form.provider_id" class="native-select">
              <option value="">手动开通</option>
              <option v-for="pv in formProviders" :key="pv.id" :value="pv.id">{{ pv.name }}</option>
            </select>
          </label>
          <label class="full"><span>商品描述（管理员手写，前台原样展示）</span>
            <NInput v-model:value="form.description" type="textarea" :rows="4" placeholder="在这里写商品介绍，换行会原样保留。留空则前台不显示描述。" />
            <small class="ua">前台只展示你写的这段文字：不会自动追加系统提取的配置卖点，也不会在留空时填充默认文案。</small>
          </label>
        </div>

        <div class="sub-head">周期价格</div>
        <div v-if="form.pay_type === 'free'" class="hint-box">免费商品：下单金额为 0，创建订单后立即开通，不走支付。</div>
        <div v-else class="form-grid">
          <label v-for="k in visibleCycles" :key="k"><span>{{ cycleLabel(k) }}价（元）</span>
            <NInputNumber v-model:value="form.prices[k]" :min="0" :precision="2" style="width:100%" placeholder="留空表示不提供该周期" />
          </label>
        </div>

        <div v-if="form.pay_type === 'trial'" class="form-grid">
          <label><span>试用天数</span><NInputNumber v-model:value="form.trial_days" :min="1" style="width:100%" /><small>到期后自动回收服务</small></label>
          <label><span>试用费（元，0 = 免费试用）</span><NInputNumber v-model:value="form.trial_price" :min="0" :precision="2" style="width:100%" /></label>
        </div>
        <div class="form-grid">
          <label><span>开通后自动删除（天，0 = 不删除）</span><NInputNumber v-model:value="form.auto_terminate_days" :min="0" style="width:100%" /></label>
        </div>

        <div class="sub-head">库存与限购</div>
        <div class="check-grid">
          <NCheckbox v-model:checked="form.stock_control">启用库存控制</NCheckbox>
          <NCheckbox v-model:checked="form.allow_qty">允许一次购买多件</NCheckbox>
          <NCheckbox v-model:checked="form.is_featured">设为推荐商品</NCheckbox>
        </div>
        <div class="form-grid">
          <label><span>库存数量</span><NInputNumber v-model:value="form.stock_qty" :min="0" :disabled="!form.stock_control" style="width:100%" /></label>
          <label><span>单客户最多购买（0 = 不限）</span><NInputNumber v-model:value="form.max_per_customer" :min="0" style="width:100%" /></label>
        </div>

        <div class="form-actions">
          <NButton type="primary" size="large" :loading="busy" @click="save">{{ editingID ? '保存修改' : '创建商品' }}</NButton>
          <NButton v-if="editingID" size="large" @click="resetForm">取消编辑</NButton>
        </div>
      </section>

      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>产品分组</h2><span>权重越大越靠前</span></div></div>
        <div class="form-grid">
          <label><span>新分组名称</span><NInput v-model:value="groupName" placeholder="例如：香港节点" @keyup.enter="createGroup" /></label>
          <label><span>排序权重</span><NInputNumber v-model:value="groupWeight" style="width:100%" /></label>
        </div>
        <NButton secondary block @click="createGroup">添加分组</NButton>
        <div class="group-list">
          <div v-for="g in groups" :key="g.id" class="group-row">
            <div><b>{{ g.name }}</b><small>{{ g.product_count }} 个在售商品 · 权重 {{ g.sort_weight }}</small></div>
            <NButton size="tiny" tertiary type="error" @click="deleteGroup(g)">删除</NButton>
          </div>
          <div v-if="!groups.length" class="empty-box" style="margin:0">还没有分组。分组用于产品中心页按区块展示商品。</div>
        </div>
      </section>
    </div>

    <section v-if="editingID" class="panel">
      <div class="panel-title-row"><div><h2>配置项（可配置选项）</h2><span>下单时买家选择，加价由服务端重算</span></div></div>
      <div class="form-grid">
        <label><span>配置项名称</span><NInput v-model:value="optForm.name" placeholder="例如：CPU" /></label>
        <label><span>类型</span><NSelect v-model:value="optForm.option_type" :options="optionTypeOptions" /></label>
        <label class="full"><span>说明（可选）</span><NInput v-model:value="optForm.description" placeholder="显示在下单页的补充说明" /></label>
        <label v-if="optForm.option_type === 4"><span>数量下限</span><NInputNumber v-model:value="optForm.qty_min" :min="1" style="width:100%" /></label>
        <label v-if="optForm.option_type === 4"><span>数量上限</span><NInputNumber v-model:value="optForm.qty_max" :min="1" style="width:100%" /></label>
      </div>
      <NCheckbox v-model:checked="optForm.required">必选</NCheckbox>
      <div class="sub-head">候选项</div>
      <div v-for="(v, i) in optForm.values" :key="i" class="value-row">
        <NInput v-model:value="v.label" :placeholder="optForm.option_type === 4 ? '计价单位，例如 每 10GB' : '选项名称，例如 2 核'" style="flex:2" />
        <NInputNumber v-model:value="v.price" :min="0" :precision="2" placeholder="加价(元)" style="flex:1" />
        <NInputNumber v-model:value="v.setup" :min="0" :precision="2" placeholder="初装费(元)" style="flex:1" />
        <NCheckbox v-model:checked="v.is_default">默认</NCheckbox>
        <NButton size="tiny" tertiary type="error" :disabled="optForm.values.length <= 1" @click="removeOptionValue(i)">删除</NButton>
      </div>
      <div class="form-actions">
        <NButton secondary @click="addOptionValue">添加候选项</NButton>
        <NButton type="primary" @click="createOption">保存配置项</NButton>
      </div>

      <div v-if="configOptions.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>配置项</span><span>类型</span><span>候选项</span><span>操作</span></div>
        <div v-for="o in configOptions" :key="o.id" class="audit-row">
          <span><b>{{ o.name }}</b><small v-if="o.required" class="ua">必选</small></span>
          <span class="muted">{{ optionTypeLabel(o.option_type) }}</span>
          <span class="muted">
            <template v-for="(v, i) in o.values" :key="v.id">{{ i ? '、' : '' }}{{ v.label }}<template v-if="v.price_cents">(+{{ money(v.price_cents) }})</template></template>
          </span>
          <span><NButton size="tiny" tertiary type="error" @click="deleteOption(o)">删除</NButton></span>
        </div>
      </div></div>
      <div v-else class="empty-box">这个商品还没有配置项。买家将只能按基础价购买。</div>
    </section>

    <section v-if="editingID" class="panel">
      <div class="panel-title-row"><div><h2>自定义字段</h2><span>下单时向买家收集，随开通请求下发给上游</span></div></div>
      <div class="form-grid">
        <label><span>字段名</span><NInput v-model:value="fieldForm.name" placeholder="例如：服务器备注" /></label>
        <label><span>字段键（英文，传给上游）</span><NInput v-model:value="fieldForm.field_key" placeholder="例如：remark" /></label>
        <label><span>类型</span><NSelect v-model:value="fieldForm.field_type" :options="fieldTypeOptions" /></label>
        <label><span>占位提示</span><NInput v-model:value="fieldForm.placeholder" /></label>
        <label v-if="fieldForm.field_type === 'dropdown'" class="full"><span>候选项（每行一个）</span><NInput v-model:value="fieldForm.options_text" type="textarea" :rows="3" placeholder="Debian 12&#10;Ubuntu 22.04" /></label>
        <label class="full"><span>校验正则（可选）</span><NInput v-model:value="fieldForm.regex" placeholder="例如 ^[a-zA-Z0-9-]{1,32}$" /></label>
        <label class="full"><span>说明（可选）</span><NInput v-model:value="fieldForm.description" /></label>
      </div>
      <div class="check-grid">
        <NCheckbox v-model:checked="fieldForm.required">必填</NCheckbox>
        <NCheckbox v-model:checked="fieldForm.show_on_order">下单页显示</NCheckbox>
        <NCheckbox v-model:checked="fieldForm.admin_only">仅管理员可见</NCheckbox>
      </div>
      <div class="form-actions"><NButton type="primary" @click="createField">保存字段</NButton></div>

      <div v-if="customFields.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>字段</span><span>键</span><span>类型</span><span>操作</span></div>
        <div v-for="f in customFields" :key="f.id" class="audit-row">
          <span><b>{{ f.name }}</b><small v-if="f.required" class="ua">必填</small></span>
          <span><code>{{ f.field_key }}</code></span>
          <span class="muted">{{ f.field_type }}</span>
          <span><NButton size="tiny" tertiary type="error" @click="deleteField(f)">删除</NButton></span>
        </div>
      </div></div>
      <div v-else class="empty-box">这个商品还没有自定义字段。</div>
    </section>

    <section class="panel">
      <div class="panel-title-row"><div><h2>全部商品</h2><span>{{ products.length }} 个（含已下架）</span></div></div>
      <div v-if="products.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>商品</span><span>分组</span><span>计费</span><span>价格</span><span>库存</span><span>状态</span><span>操作</span></div>
        <div v-for="prod in products" :key="prod.id" class="audit-row">
          <span><b>{{ prod.name }}</b><small class="ua">{{ (prod.description || '').split('\n')[0] }}</small></span>
          <span>{{ prod.group_name || '未分组' }}</span>
          <span><NTag size="small" round :type="prod.pay_type === 'free' ? 'success' : prod.pay_type === 'trial' ? 'warning' : 'default'">{{ payTypeLabel(prod.pay_type || 'recurring') }}</NTag></span>
          <span><b>{{ money(prod.price_cents, prod.currency) }}</b><small class="ua">/{{ cycleLabel(prod.billing_cycle) }}</small></span>
          <span class="muted">{{ prod.stock_control ? (prod.stock_qty - (prod.sold_count || 0)) + ' / ' + prod.stock_qty : '不限' }}</span>
          <span><NTag v-if="prod.active" size="small" type="success" round>在售</NTag><NTag v-else size="small" round>已下架</NTag></span>
          <span class="row" style="gap:6px">
            <NButton size="tiny" tertiary @click="edit(prod)">编辑</NButton>
            <NButton size="tiny" tertiary :type="prod.active ? 'warning' : 'success'" @click="toggleActive(prod)">{{ prod.active ? '下架' : '上架' }}</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有商品。</div>
    </section>
  </div>
</template>

<style scoped>
.group-list { display: flex; flex-direction: column; gap: 8px; }
.group-row { display: flex; justify-content: space-between; align-items: center; border: 1px solid var(--border, #e5e8f0); border-radius: 10px; padding: 8px 12px; }
.group-row small { display: block; color: var(--muted, #8a93a6); font-size: 12px; }
.native-select { height: 34px; border-radius: 6px; border: 1px solid var(--border, #d5d9e4); background: var(--panel, #fff); color: var(--text, #1c2333); padding: 0 8px; width: 100%; }
.sub-head { font-weight: 700; margin: 16px 0 8px; font-size: 14px; }
.hint-box { background: var(--primary-soft, #eef3ff); color: var(--primary, #2b6cb0); border-radius: 8px; padding: 8px 12px; font-size: 12px; }
.value-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; flex-wrap: wrap; }
.check-grid { display: flex; gap: 18px; flex-wrap: wrap; margin: 8px 0; }
</style>
