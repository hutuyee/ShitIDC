<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NModal, NRadio, NRadioGroup, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 活动促销（对齐魔方 CBAP 插件 EventPromotion）。
//
// 与优惠券 / 代金券的区别：活动不需要用户输入任何码，满足条件的订单在下单时
// 自动享受折扣；同一订单只应用排序最靠前的一个命中活动（插件用置顶 / 置底排序）。
// 字段与插件前端一致：percent（折扣比例 %）/ reduce（满减：达标金额 + 优惠金额）、
// 参与产品、适用用户（不选 = 所有用户）、新注册用户 / 现有客户 / 单用户一次 /
// 周期限制四个开关、备注、启停与排序。

const message = useMessage()
const items = ref<any[]>([])
const total = ref(0)
const products = ref<any[]>([])
const busy = ref(false)
const page = ref(1)
const filter = reactive({ keywords: '', status: '', time: null as number | null })

const statusOptions = [
  { label: '全部状态', value: '' },
  { label: '启用中', value: 'Active' },
  { label: '待生效', value: 'Pending' },
  { label: '已失效', value: 'Expiration' },
  { label: '已停用', value: 'Suspended' },
]
const statusMeta: Record<string, { text: string; type: any }> = {
  Active: { text: '启用中', type: 'success' },
  Pending: { text: '待生效', type: 'warning' },
  Expiration: { text: '已失效', type: 'default' },
  Suspended: { text: '已停用', type: 'default' },
}
const cycleOptions = [
  { label: '月', value: 'monthly' },
  { label: '季', value: 'quarterly' },
  { label: '半年', value: 'semiannually' },
  { label: '一年', value: 'yearly' },
  { label: '两年', value: 'biennially' },
  { label: '三年', value: 'triennially' },
]
// 快速选择时长（插件同名的「快速选择时长」）。
const quickDurations = [
  { label: '1 天', value: 1 },
  { label: '3 天', value: 3 },
  { label: '7 天', value: 7 },
  { label: '15 天', value: 15 },
  { label: '1 个月', value: 30 },
  { label: '3 个月', value: 90 },
  { label: '6 个月', value: 180 },
  { label: '1 年', value: 365 },
  { label: '2 年', value: 730 },
]

const productOptions = computed(() => products.value.map(p => ({ label: p.name, value: p.id })))
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string | number | null) => (v ? new Date(v).toLocaleString() : '—')
const pages = computed(() => Math.max(1, Math.ceil(total.value / 20)))
const promoText = (v: any) => v.type === 'percent'
  ? `${v.value}% 折扣`
  : `满 ${Number(v.full || 0).toFixed(2)} 减 ${Number(v.value || 0).toFixed(2)}`
const scopeText = (v: any) => v.client_type === 'appoint' ? `指定用户（${(v.clients || []).length}）` : '所有用户'
const flagsText = (v: any) => {
  const out: string[] = []
  if (v.new_user) out.push('新注册')
  if (v.old_user) out.push('现有客户')
  if (v.single_user_once) out.push('单用户一次')
  if (v.cycle_limit) out.push('周期限制')
  return out.length ? out.join(' / ') : '不限'
}

const emptyForm = () => ({
  name: '', type: 'percent', value: 10, full: 0,
  start_at: null as string | null, end_at: null as string | null,
  products: [] as string[], clients: [] as string[],
  new_user: false, old_user: false, single_user_once: false,
  cycle_limit: false, cycle: [] as string[],
  notes: '',
})
const form = reactive<any>(emptyForm())
const editing = ref('')
const showForm = ref(false)
const quick = ref<number | null>(null)

function toLocalInput(v?: string | null | number) {
  if (!v) return null
  const d = new Date(typeof v === 'number' ? v * 1000 : v)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function applyQuick(days: number | null) {
  if (!days) return
  const start = form.start_at ? new Date(form.start_at) : new Date()
  const end = new Date(start.getTime() + days * 24 * 3600 * 1000)
  form.start_at = toLocalInput(start.toISOString())
  form.end_at = toLocalInput(end.toISOString())
}

async function load() {
  busy.value = true
  try {
    const params: any = { page: page.value, limit: 20 }
    if (filter.keywords.trim()) params.keywords = filter.keywords.trim()
    if (filter.status) params.status = filter.status
    if (filter.time) params.time = Math.floor(filter.time / 1000)
    const res = dataOf<any>(await api.get('/admin/promotions', { params }))
    items.value = res?.list || []
    total.value = res?.count || 0
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取活动失败') }
  finally { busy.value = false }
}

async function loadProducts() {
  try { products.value = dataOf<any[]>(await api.get('/admin/products')) || [] }
  catch { products.value = [] }
}

function resetForm() {
  editing.value = ''
  Object.assign(form, emptyForm())
  quick.value = null
}

function openCreate() {
  resetForm()
  showForm.value = true
}

async function openEdit(v: any) {
  resetForm()
  editing.value = v.id
  try {
    const res = dataOf<any>(await api.get(`/admin/promotions/${v.id}`))
    const d = res?.event_promotion || {}
    Object.assign(form, {
      name: d.name, type: d.type, value: d.value, full: d.full,
      start_at: toLocalInput(d.start_time), end_at: toLocalInput(d.end_time),
      products: [...(d.products || [])], clients: [...(d.clients || [])],
      new_user: !!d.new_user, old_user: !!d.old_user, single_user_once: !!d.single_user_once,
      cycle_limit: !!d.cycle_limit, cycle: [...(d.cycle || [])],
      notes: d.notes || '',
    })
    showForm.value = true
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取活动失败') }
}

async function save() {
  if (!form.name.trim()) { message.error('请填写活动名称'); return }
  if (!form.start_at) { message.error('请选择生效时间'); return }
  if (form.type === 'percent' && (!form.value || form.value <= 0 || form.value > 100)) {
    message.error('折扣比例须在 0~100 之间'); return
  }
  if (form.type === 'reduce' && (!form.value || form.value <= 0)) {
    message.error('优惠金额必须大于 0'); return
  }
  if (form.cycle_limit && !form.cycle.length) { message.error('请选择周期限制的计费周期'); return }
  const payload = {
    name: form.name.trim(),
    type: form.type,
    value: Number(form.value) || 0,
    full: form.type === 'reduce' ? Number(form.full) || 0 : 0,
    start_time: Math.floor(new Date(form.start_at).getTime() / 1000),
    end_time: form.end_at ? Math.floor(new Date(form.end_at).getTime() / 1000) : 0,
    products: form.products,
    clients: form.clients,
    client_type: form.clients.length ? 'appoint' : 'all',
    new_user: form.new_user,
    old_user: form.old_user,
    single_user_once: form.single_user_once,
    cycle_limit: form.cycle_limit,
    cycle: form.cycle,
    notes: form.notes.trim(),
  }
  busy.value = true
  try {
    if (editing.value) await api.put(`/admin/promotions/${editing.value}`, payload)
    else await api.post('/admin/promotions', payload)
    message.success(editing.value ? '活动已更新' : '活动已创建')
    showForm.value = false
    resetForm()
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}

async function toggle(v: any) {
  try {
    await api.put(`/admin/promotions/${v.id}/status`, { status: v.status === 'Active' ? 0 : 1 })
    message.success(v.status === 'Active' ? '已停用' : '已启用')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}

async function remove(v: any) {
  if (!window.confirm(`确认删除活动「${v.name}」？`)) return
  try {
    await api.delete(`/admin/promotions/${v.id}`)
    message.success('已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---------- 适用用户（指定用户） ----------
const userKeyword = ref('')
const userResults = ref<any[]>([])
const pickedUsers = ref<any[]>([])

async function searchUsers() {
  const q = userKeyword.value.trim()
  if (!q) { userResults.value = []; return }
  try {
    userResults.value = dataOf<any[]>(await api.get('/admin/search', { params: { q, kind: 'user', limit: 20 } })) || []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '搜索失败') }
}

function addUser(u: any) {
  if (form.clients.includes(u.id)) return
  form.clients.push(u.id)
  if (!pickedUsers.value.some(x => x.id === u.id)) pickedUsers.value.push(u)
}

function removeUser(id: string) {
  form.clients = form.clients.filter((x: string) => x !== id)
  pickedUsers.value = pickedUsers.value.filter(x => x.id !== id)
}

// ---------- 排序（置顶 / 置底） ----------
const showSort = ref(false)
const activeList = ref<any[]>([])
const doesNotParticipate = ref(false)

async function openSort() {
  try {
    const res = dataOf<any>(await api.get('/admin/promotions/active'))
    activeList.value = res?.list || []
    doesNotParticipate.value = !!res?.addon_event_promotion_does_not_participate
    showSort.value = true
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取排序数据失败') }
}

function move(index: number, delta: number) {
  const next = index + delta
  if (next < 0 || next >= activeList.value.length) return
  const list = [...activeList.value]
  const [row] = list.splice(index, 1)
  list.splice(next, 0, row)
  activeList.value = list
}

function moveTop(index: number) { move(index, -index) }
function moveBottom(index: number) { move(index, activeList.value.length - 1 - index) }

async function saveSort() {
  busy.value = true
  try {
    await api.put('/admin/promotions/order', { id: activeList.value.map(v => v.id) })
    await api.put('/admin/promotions/config', { addon_event_promotion_does_not_participate: doesNotParticipate.value })
    message.success('排序已保存')
    showSort.value = false
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}

onMounted(() => { load(); loadProducts() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>活动促销</h1>
        <p>对齐魔方「活动促销」插件：满足条件的订单在下单时自动打折（按比例）或满减，无需用户填码；同一订单只应用排序最靠前的一个活动。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary @click="openSort">排序 / 配置</NButton>
        <NButton type="primary" @click="openCreate">新建活动</NButton>
      </div>
    </div>

    <div class="audit-toolbar" style="grid-template-columns:minmax(180px,260px) minmax(140px,180px) minmax(180px,240px) auto">
      <NInput v-model:value="filter.keywords" clearable placeholder="活动名称 / 备注" @keyup.enter="() => { page = 1; load() }" />
      <NSelect v-model:value="filter.status" :options="statusOptions" @update:value="() => { page = 1; load() }" />
      <input type="datetime-local" class="native-input" v-model="filter.time" @change="() => { page = 1; load() }" />
      <NButton secondary :loading="busy" @click="() => { page = 1; load() }">查询</NButton>
    </div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>活动列表</h2><span>共 {{ total }} 个</span></div></div>
      <div v-if="items.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>活动名称</span><span>优惠</span><span>活动时间</span><span>活动范围</span><span>状态</span><span>操作</span></div>
        <div v-for="v in items" :key="v.id" class="audit-row">
          <span><b>{{ v.name }}</b><small>{{ flagsText(v) }}</small></span>
          <span>{{ promoText(v) }}<small>{{ (v.products || []).length ? `限 ${v.products.length} 个商品` : '全部商品' }}</small></span>
          <span class="muted">{{ fmt(v.start_time ? v.start_time * 1000 : null) }}<small>至 {{ v.end_time ? fmt(v.end_time * 1000) : '长期' }}</small></span>
          <span>{{ scopeText(v) }}<small>{{ v.notes || '—' }}</small></span>
          <span><NTag :type="statusMeta[v.status]?.type || 'default'" size="tiny" round>{{ statusMeta[v.status]?.text || v.status }}</NTag></span>
          <span class="row" style="gap:6px;flex-wrap:wrap">
            <NButton size="tiny" tertiary @click="openEdit(v)">编辑</NButton>
            <NButton size="tiny" tertiary @click="toggle(v)">{{ v.status === 'Active' ? '停用' : '启用' }}</NButton>
            <NButton size="tiny" tertiary type="error" @click="remove(v)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有活动。点击右上角「新建活动」创建第一个促销。</div>
      <div class="row" style="justify-content:flex-end;gap:8px;margin-top:10px">
        <NButton size="small" secondary :disabled="page <= 1" @click="() => { page--; load() }">上一页</NButton>
        <span class="muted" style="align-self:center">{{ page }} / {{ pages }}</span>
        <NButton size="small" secondary :disabled="page >= pages" @click="() => { page++; load() }">下一页</NButton>
      </div>
    </section>

    <NModal v-model:show="showForm" preset="card" :title="editing ? '编辑活动' : '新建活动'" style="width:min(680px,94vw)">
      <div class="form-grid">
        <label class="full"><span>活动名称</span><NInput v-model:value="form.name" placeholder="例如：双十一全场 9 折" /></label>
        <label><span>生效时间</span><input type="datetime-local" class="native-input" v-model="form.start_at" /></label>
        <label><span>截止时间（留空=长期）</span><input type="datetime-local" class="native-input" v-model="form.end_at" /></label>
        <label><span>快速选择时长</span>
          <NSelect v-model:value="quick" :options="quickDurations.map(d => ({ label: d.label, value: d.value }))" clearable placeholder="从生效时间起算" @update:value="applyQuick" />
        </label>
        <label class="full"><span>活动类型</span>
          <NRadioGroup v-model:value="form.type">
            <NRadio value="percent">百分比（折扣比例）</NRadio>
            <NRadio value="reduce">满减</NRadio>
          </NRadioGroup>
        </label>
        <label v-if="form.type === 'percent'"><span>折扣比例 %</span><NInputNumber v-model:value="form.value" :min="0" :max="100" :precision="2" style="width:100%" /></label>
        <template v-else>
          <label><span>达标金额（元）</span><NInputNumber v-model:value="form.full" :min="0" :precision="2" style="width:100%" /></label>
          <label><span>优惠金额（元）</span><NInputNumber v-model:value="form.value" :min="0" :precision="2" style="width:100%" /></label>
        </template>
        <label class="full"><span>参与产品（不选＝全部）</span>
          <NSelect v-model:value="form.products" multiple filterable clearable :options="productOptions" placeholder="留空表示所有商品参与" />
        </label>
        <label class="full"><span>适用用户（不选＝所有用户）</span>
          <div class="row" style="gap:8px">
            <NInput v-model:value="userKeyword" placeholder="按邮箱 / 昵称 / UID 搜索用户" @keyup.enter="searchUsers" />
            <NButton size="small" secondary @click="searchUsers">搜索</NButton>
          </div>
          <NTag v-for="u in pickedUsers" :key="u.id" closable style="margin:6px 6px 0 0" @close="removeUser(u.id)">{{ u.label }}</NTag>
          <div v-if="userResults.length" class="table-scroll" style="max-height:140px;overflow-y:auto;margin-top:6px">
            <div class="audit-table">
              <div v-for="u in userResults" :key="u.id" class="audit-row" style="grid-template-columns:minmax(140px,1fr) minmax(120px,.8fr) 70px">
                <span>{{ u.label }}</span>
                <span class="muted">{{ u.sub || '—' }}</span>
                <span><NButton size="tiny" tertiary @click="addUser(u)">添加</NButton></span>
              </div>
            </div>
          </div>
        </label>
        <label class="full"><span>限制开关</span>
          <div class="row" style="gap:16px;flex-wrap:wrap">
            <span class="row" style="gap:6px"><NSwitch v-model:value="form.new_user" size="small" />仅新注册用户</span>
            <span class="row" style="gap:6px"><NSwitch v-model:value="form.old_user" size="small" />仅现有客户（需有已支付订单）</span>
            <span class="row" style="gap:6px"><NSwitch v-model:value="form.single_user_once" size="small" />单用户一次</span>
            <span class="row" style="gap:6px"><NSwitch v-model:value="form.cycle_limit" size="small" />周期限制</span>
          </div>
        </label>
        <label v-if="form.cycle_limit" class="full"><span>适用计费周期</span>
          <NSelect v-model:value="form.cycle" multiple filterable :options="cycleOptions" placeholder="只有选择这些周期的订单可参与" />
        </label>
        <label class="full"><span>备注</span><NInput v-model:value="form.notes" type="textarea" :rows="2" placeholder="仅后台可见" /></label>
      </div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton secondary @click="showForm = false">取消</NButton>
          <NButton type="primary" :loading="busy" @click="save">{{ editing ? '保存修改' : '创建活动' }}</NButton>
        </div>
      </template>
    </NModal>

    <NModal v-model:show="showSort" preset="card" title="活动排序 / 配置" style="width:min(620px,94vw)">
      <p class="muted" style="margin-top:0">越靠上的活动优先级越高：同一订单只会应用排序最靠前的一个命中活动。</p>
      <div v-if="activeList.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head" style="grid-template-columns:minmax(160px,1fr) minmax(120px,.8fr) 200px"><span>活动名称</span><span>优惠</span><span>操作</span></div>
        <div v-for="(v, i) in activeList" :key="v.id" class="audit-row" style="grid-template-columns:minmax(160px,1fr) minmax(120px,.8fr) 200px">
          <span>{{ i + 1 }}. <b>{{ v.name }}</b></span>
          <span>{{ promoText(v) }}</span>
          <span class="row" style="gap:6px">
            <NButton size="tiny" tertiary :disabled="i === 0" @click="moveTop(i)">置顶</NButton>
            <NButton size="tiny" tertiary :disabled="i === 0" @click="move(i, -1)">上移</NButton>
            <NButton size="tiny" tertiary :disabled="i === activeList.length - 1" @click="move(i, 1)">下移</NButton>
            <NButton size="tiny" tertiary :disabled="i === activeList.length - 1" @click="moveBottom(i)">置底</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">没有启用中的活动。</div>
      <label class="row" style="gap:8px;margin-top:12px">
        <NSwitch v-model:value="doesNotParticipate" size="small" />
        <span>选项「不参与活动」</span>
      </label>
      <p class="muted" style="margin:6px 0 0">该配置项与插件同名保存；站内商品配置暂未提供「不参与活动」选项的消费入口。</p>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton secondary @click="showSort = false">取消</NButton>
          <NButton type="primary" :loading="busy" @click="saveSort">保存</NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>
