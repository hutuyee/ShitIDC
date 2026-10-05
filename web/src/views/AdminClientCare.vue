<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { NButton, NDatePicker, NInput, NInputNumber, NModal, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 客户关怀（对齐魔方 CBAP ClientCare 插件）。
// 后台列表 + 新建推送任务：按条件（指定用户 / 产品 / 接口 / 状态 / 时间）圈人，
// 通知形式为站内信或短信+邮件，支持一次性 / 每天 / 每周 / 每月，可预览推送名单。
// 短信自定义内容本站暂不投递（字段保留）。

const message = useMessage()

const jobs = ref<any[]>([])
const total = ref(0)
const busy = ref(false)
const keyword = ref('')
const statusFilter = ref<string | null>(null)
const statusOptions = [
  { label: '待执行', value: 'wait' },
  { label: '执行中', value: 'exec' },
  { label: '已暂停', value: 'suspended' },
  { label: '已完成', value: 'finished' },
  { label: '已失效', value: 'expired' },
]
const statusText: Record<string, string> = { wait: '待执行', exec: '执行中', suspended: '已暂停', finished: '已完成', expired: '已失效' }
const statusType = (s: string) => ({ wait: 'info', exec: 'success', suspended: 'warning', finished: 'default', expired: 'error' } as any)[s] || 'default'
const cycleText: Record<string, string> = { onetime: '一次性', day: '每天', week: '每周', month: '每月' }
const weekText = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const day = (v?: string) => (v ? String(v).slice(0, 10) : '—')
const excerpt = (s: string) => String(s || '').replace(/<[^>]+>/g, '').slice(0, 60) || '—'
const typeText = (t: number) => (t === 2 ? '短信+邮件' : '站内信')
const active = (j: any) => j.status === 'wait' || j.status === 'exec'
const cycleDetail = (j: any) => {
  const hm = `${String(j.hour ?? 0).padStart(2, '0')}:${String(j.minute ?? 0).padStart(2, '0')}`
  if (j.send_cycle === 'week') return `每周${weekText[j.week_day] || j.week_day} ${hm}`
  if (j.send_cycle === 'month') return `每月${j.month_day}日 ${hm}`
  if (j.send_cycle === 'day') return `每天 ${hm}`
  return '仅一次'
}

async function loadJobs() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/client-care', {
      params: { keyword: keyword.value.trim(), status: statusFilter.value || '', limit: 100 },
    }))
    jobs.value = d?.list || []
    total.value = d?.count || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取推送任务失败')
  } finally {
    busy.value = false
  }
}
async function toggleJob(j: any) {
  try {
    await api.put(`/admin/client-care/${j.public_id}/status`, { enable: !active(j) })
    message.success(active(j) ? '已暂停推送' : '已启用推送')
    await loadJobs()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '更新推送状态失败')
  }
}
async function removeJob(j: any) {
  try {
    await api.delete(`/admin/client-care/${j.public_id}`)
    message.success('已删除')
    await loadJobs()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

const options = reactive<{ products: any[]; providers: any[]; mail_providers: any[]; mail_templates: any[] }>({
  products: [], providers: [], mail_providers: [], mail_templates: [],
})
const productOptions = computed(() => options.products.map((p: any) => ({ label: p.name, value: p.id })))
const providerOptions = computed(() => options.providers.map((p: any) => ({ label: p.name, value: p.id })))
const mailProviderOptions = computed(() => options.mail_providers.map((p: any) => ({ label: p.name, value: p.id })))
const mailTemplateOptions = computed(() => options.mail_templates.map((t: any) => ({ label: `${t.name}（${t.subject}）`, value: t.name })))
const users = ref<any[]>([])
const userOptions = computed(() => users.value.map((u: any) => ({ label: u.email, value: u.id })))
const mailProviderName = (id: string) => options.mail_providers.find((p: any) => p.id === id)?.name || id || '默认通道'

async function loadOptions() {
  try {
    const d = dataOf<any>(await api.get('/admin/client-care/options'))
    options.products = d?.products || []
    options.providers = d?.providers || []
    options.mail_providers = d?.mail_providers || []
    options.mail_templates = d?.mail_templates || []
  } catch {
    message.error('读取推送选项失败（表单仍可手动填写）')
  }
}
async function searchUsers(kw: string) {
  try {
    users.value = dataOf<any>(await api.get('/admin/client-care/users', { params: { keyword: kw || '', limit: 50 } }))?.list || []
  } catch { users.value = [] }
}

const formOpen = ref(false)
const templatePick = ref<string | null>(null)
const form = reactive({
  title: '',
  type: 1,
  subject: '',
  content: '',
  email_name: '',
  sms_name: '',
  sms_template_id: 0,
  send_cycle: 'onetime',
  week_day: 1,
  month_day: 1,
  hour: 10,
  minute: 0,
  repeat_send: false,
  range: null as [number, number] | null,
  condition1: 'client',
  condition2: 'client',
  condition3: [] as any[],
  comparator: '>=',
  condition4: 0,
  condition4Date: null as number | null,
  condition5: 'day',
})
const cycleOptions = [
  { label: '一次性', value: 'onetime' },
  { label: '每天', value: 'day' },
  { label: '每周', value: 'week' },
  { label: '每月', value: 'month' },
]
const weekOptions = weekText.map((t, i) => ({ label: t, value: i }))
const comparatorOptions = [
  { label: '≥（至少 / N天前）', value: '>=' },
  { label: '<（少于 / 最近N天）', value: '<' },
]
const condition1Options = [
  { label: '指定用户', value: 'client' },
  { label: '指定产品', value: 'host' },
  { label: '指定接口', value: 'server' },
]
const condition2Options = computed(() => {
  if (form.condition1 === 'client') {
    return [
      { label: '指定用户', value: 'client' },
      { label: '注册时长', value: 'register_time' },
      { label: '上次登录', value: 'last_login_time' },
      { label: '持有产品数', value: 'host_num' },
      { label: '生效产品数', value: 'active_host_num' },
      { label: '指定产品', value: 'owner_special_product' },
    ]
  }
  if (form.condition1 === 'host') {
    return [
      { label: '产品状态', value: 'status' },
      { label: '购买时间', value: 'purchase_time' },
      { label: '删除时间', value: 'termination_time' },
    ]
  }
  return [{ label: '指定接口', value: 'product' }]
})
const hostStatusOptions = [
  { label: '未付款', value: 'Unpaid' },
  { label: '待开通', value: 'Pending' },
  { label: '生效中', value: 'Active' },
  { label: '已暂停', value: 'Suspended' },
  { label: '已删除', value: 'Deleted' },
  { label: '开通失败', value: 'Failed' },
  { label: '已取消', value: 'Cancelled' },
]
const isUserCondition = computed(() => form.condition1 === 'client' && form.condition2 === 'client')
const isProductCondition = computed(() => form.condition1 === 'client' && form.condition2 === 'owner_special_product')
const isProviderCondition = computed(() => form.condition1 === 'server' && form.condition2 === 'product')
const isEnumCondition = computed(() => form.condition1 === 'host' && form.condition2 === 'status')
const isTimeCondition = computed(() => form.condition1 === 'host' && (form.condition2 === 'purchase_time' || form.condition2 === 'termination_time'))
const isNumberCondition = computed(() => form.condition1 === 'client' && ['register_time', 'last_login_time', 'host_num', 'active_host_num'].includes(form.condition2))

watch(() => form.condition1, () => { form.condition2 = condition2Options.value[0]?.value || 'client' })
watch(() => form.condition2, () => { form.condition3 = []; form.condition4 = 0; form.condition4Date = null; form.condition5 = 'day' })

const ymd = (ts: number) => {
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
function buildPushObject(): Record<string, any> {
  const obj: Record<string, any> = { condition1: form.condition1, condition2: form.condition2 }
  if (isUserCondition.value || isProductCondition.value || isProviderCondition.value || isEnumCondition.value) {
    obj.condition3 = form.condition3
  } else if (isTimeCondition.value) {
    obj.condition3 = form.comparator
    obj.condition5 = form.condition5
    obj.condition4 = form.condition5 === 'date' ? (form.condition4Date ? ymd(form.condition4Date) : '') : form.condition4
  } else if (isNumberCondition.value) {
    obj.condition3 = form.comparator
    obj.condition4 = form.condition4
  } else {
    obj.condition3 = form.condition3
  }
  return obj
}

function resetForm() {
  form.title = ''
  form.type = 1
  form.subject = ''
  form.content = ''
  form.email_name = ''
  form.sms_name = ''
  form.sms_template_id = 0
  form.send_cycle = 'onetime'
  form.week_day = 1
  form.month_day = 1
  form.hour = 10
  form.minute = 0
  form.repeat_send = false
  form.range = null
  form.condition1 = 'client'
  form.condition2 = 'client'
  form.condition3 = []
  form.comparator = '>='
  form.condition4 = 0
  form.condition4Date = null
  form.condition5 = 'day'
  templatePick.value = null
}
function openCreate() {
  resetForm()
  formOpen.value = true
}
function onTemplatePick(name: string | null) {
  templatePick.value = name
  const t = options.mail_templates.find((x: any) => x.name === name)
  if (t?.subject) form.subject = t.subject
}

const previewOpen = ref(false)
const previewBusy = ref(false)
const previewCount = ref(0)
const previewList = ref<any[]>([])
async function previewRecipients() {
  previewBusy.value = true
  try {
    const d = dataOf<any>(await api.post('/admin/client-care/recipients', { push_object: buildPushObject(), limit: 100 }))
    previewCount.value = d?.count || 0
    previewList.value = d?.list || []
    previewOpen.value = true
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取推送名单失败')
  } finally {
    previewBusy.value = false
  }
}
async function save() {
  if (!form.title.trim()) { message.error('请填写通知标题'); return }
  if (!form.content.trim()) { message.error('请填写推送内容'); return }
  if (form.type === 2 && !form.subject.trim()) { message.error('邮件推送需要填写邮件标题'); return }
  if (!form.range) { message.error('请选择推送时间范围'); return }
  const [start, end] = form.range
  busy.value = true
  try {
    await api.post('/admin/client-care', {
      title: form.title.trim(),
      type: form.type,
      content: form.content,
      subject: form.subject.trim(),
      email_name: form.email_name,
      sms_name: form.sms_name,
      sms_template_id: form.sms_template_id,
      push_start_time: ymd(start),
      push_end_time: ymd(end),
      send_cycle: form.send_cycle,
      week_day: form.week_day,
      month_day: form.month_day,
      hour: form.hour,
      minute: form.minute,
      repeat_send: form.repeat_send,
      push_object: buildPushObject(),
    })
    message.success('推送任务已创建')
    formOpen.value = false
    await loadJobs()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '创建推送任务失败')
  } finally {
    busy.value = false
  }
}

onMounted(() => { loadJobs(); loadOptions(); searchUsers('') })
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">运营工具</div>
        <h1>客户关怀</h1>
        <p>按条件圈定用户，按周期推送站内信 / 邮件；短信自定义内容暂不投递（字段保留）。</p>
      </div>
      <NButton type="primary" @click="openCreate">＋ 新建推送</NButton>
    </div>

    <div class="users-toolbar">
      <NInput v-model:value="keyword" clearable placeholder="通知标题" style="max-width:240px" @keyup.enter="loadJobs" />
      <NSelect v-model:value="statusFilter" :options="statusOptions" placeholder="推送状态" clearable style="width:140px" />
      <NButton secondary :loading="busy" @click="loadJobs">查询</NButton>
      <span class="muted" style="font-size:12px">共 {{ total }} 条任务</span>
    </div>

    <div v-if="jobs.length" class="table-scroll"><div class="user-table">
      <div class="user-row cc-row user-head">
        <span>通知标题</span><span>推送内容</span><span>推送时间</span><span>推送周期</span><span>推送状态</span><span>操作</span>
      </div>
      <div v-for="j in jobs" :key="j.public_id" class="user-row cc-row">
        <span>
          <b>{{ j.title }}</b>
          <small class="muted"> {{ typeText(j.type) }}</small>
          <small v-if="j.type === 2" class="muted"> · {{ mailProviderName(j.email_name) }}</small>
        </span>
        <span class="muted">{{ excerpt(j.content) }}</span>
        <span class="muted">{{ day(j.push_start_time) }} ~ {{ day(j.push_end_time) }}</span>
        <span>{{ cycleText[j.send_cycle] || j.send_cycle }}<small class="muted"> · {{ cycleDetail(j) }}</small></span>
        <span><NTag :type="statusType(j.status)" size="tiny" round>{{ statusText[j.status] || j.status }}</NTag></span>
        <span class="row" style="gap:6px">
          <NSwitch size="small" :value="active(j)" @update:value="toggleJob(j)" />
          <NButton size="tiny" tertiary type="error" @click="removeJob(j)">删除</NButton>
        </span>
      </div>
    </div></div>
    <div v-else class="empty-box">{{ busy ? '加载中…' : '还没有推送任务，点击「新建推送」创建。' }}</div>

    <NModal v-model:show="formOpen" preset="card" title="新建推送任务" style="width:min(760px,96vw)">
      <div class="form-grid">
        <label class="full"><span>通知标题</span><NInput v-model:value="form.title" placeholder="客户端消息 / 邮件用的标题" /></label>
        <label><span>通知形式</span>
          <NSelect v-model:value="form.type" :options="[{ label: '站内信', value: 1 }, { label: '短信 + 邮件', value: 2 }]" style="width:100%" />
        </label>
        <label><span>推送周期</span>
          <NSelect v-model:value="form.send_cycle" :options="cycleOptions" style="width:100%" />
        </label>
        <label class="full"><span>推送时间范围</span>
          <NDatePicker v-model:value="form.range" type="daterange" clearable style="width:100%" />
        </label>
        <label v-if="form.send_cycle === 'week'"><span>星期</span><NSelect v-model:value="form.week_day" :options="weekOptions" style="width:100%" /></label>
        <label v-if="form.send_cycle === 'month'"><span>每月日期</span><NInputNumber v-model:value="form.month_day" :min="1" :max="31" :precision="0" style="width:100%" /></label>
        <label><span>推送时间点（时）</span><NInputNumber v-model:value="form.hour" :min="0" :max="23" :precision="0" style="width:100%" /></label>
        <label><span>推送时间点（分）</span><NInputNumber v-model:value="form.minute" :min="0" :max="59" :precision="0" style="width:100%" /></label>
        <label class="full"><span>推送内容（站内信正文 / 邮件内容，支持 HTML）</span>
          <NInput v-model:value="form.content" type="textarea" :rows="5" placeholder="推送内容" />
        </label>
        <label class="full"><span>同一用户重复发送（关闭时每个用户只发一次）</span>
          <NSwitch v-model:value="form.repeat_send" />
        </label>

        <template v-if="form.type === 2">
          <label class="full"><span>邮件标题</span><NInput v-model:value="form.subject" placeholder="邮件主题" /></label>
          <label><span>邮件模板（快速填充标题）</span>
            <NSelect v-model:value="templatePick" :options="mailTemplateOptions" clearable placeholder="选择模板" style="width:100%" @update:value="onTemplatePick" />
          </label>
          <label><span>邮件通道</span>
            <NSelect v-model:value="form.email_name" :options="mailProviderOptions" clearable placeholder="默认通道" style="width:100%" />
          </label>
          <label><span>短信通道（暂不投递）</span><NInput v-model:value="form.sms_name" placeholder="字段保留" /></label>
          <label><span>短信模板 ID（暂不投递）</span><NInputNumber v-model:value="form.sms_template_id" :min="0" :precision="0" style="width:100%" /></label>
        </template>
      </div>

      <div class="form-grid" style="margin-top:10px">
        <label class="full"><span>推送目标（圈人条件）</span>
          <div class="row" style="gap:8px;flex-wrap:wrap">
            <NSelect v-model:value="form.condition1" :options="condition1Options" style="width:120px" />
            <NSelect v-model:value="form.condition2" :options="condition2Options" style="width:150px" />
            <template v-if="isUserCondition">
              <NSelect v-model:value="form.condition3" :options="userOptions" multiple filterable placeholder="搜索 / 选择用户" style="flex:1;min-width:220px" />
            </template>
            <template v-else-if="isProductCondition">
              <NSelect v-model:value="form.condition3" :options="productOptions" multiple filterable placeholder="选择产品" style="flex:1;min-width:220px" />
            </template>
            <template v-else-if="isProviderCondition">
              <NSelect v-model:value="form.condition3" :options="providerOptions" multiple filterable placeholder="选择接口" style="flex:1;min-width:220px" />
            </template>
            <template v-else-if="isEnumCondition">
              <NSelect v-model:value="form.condition3" :options="hostStatusOptions" multiple placeholder="选择产品状态" style="flex:1;min-width:220px" />
            </template>
            <template v-else-if="isTimeCondition">
              <NSelect v-model:value="form.condition5" :options="[{ label: '按天', value: 'day' }, { label: '按日期', value: 'date' }]" style="width:100px" />
              <NSelect v-model:value="form.comparator" :options="comparatorOptions" style="width:180px" />
              <NDatePicker v-if="form.condition5 === 'date'" v-model:value="form.condition4Date" type="date" style="flex:1;min-width:170px" />
              <NInputNumber v-else v-model:value="form.condition4" :min="0" :precision="0" style="flex:1;min-width:120px" />
            </template>
            <template v-else>
              <NSelect v-model:value="form.comparator" :options="comparatorOptions" style="width:180px" />
              <NInputNumber v-model:value="form.condition4" :min="0" :precision="0" style="flex:1;min-width:120px" />
            </template>
          </div>
          <span class="muted" style="font-size:12px">「≥」表示至少 N 天 / 数量不少于 N；「&lt;」表示不足 N 天 / 最近 N 天。</span>
        </label>
      </div>

      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" :loading="busy" @click="save">创建推送任务</NButton>
        <NButton secondary :loading="previewBusy" @click="previewRecipients">预览推送名单</NButton>
        <NButton secondary @click="formOpen = false">取消</NButton>
      </div>
    </NModal>

    <NModal v-model:show="previewOpen" preset="card" :title="`推送名单预览（${previewCount} 人）`" style="width:min(560px,94vw)">
      <div v-if="!previewList.length" class="empty-box">没有用户命中该条件。</div>
      <div v-else class="stack" style="max-height:50vh;overflow:auto">
        <div v-for="u in previewList" :key="u.id" class="row" style="justify-content:space-between;gap:10px">
          <span>{{ u.email }}</span>
          <span class="muted" style="font-size:12px">{{ u.phone || '—' }}</span>
        </div>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.cc-row { grid-template-columns: minmax(200px, 1.1fr) minmax(180px, 1.2fr) 170px 170px 90px 120px; }
</style>
