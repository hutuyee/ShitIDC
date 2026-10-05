<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NDatePicker, NInput, NInputNumber, NModal, NSelect, NSwitch, NTreeSelect, useMessage } from 'naive-ui'
import { useRouter } from 'vue-router'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'

// 定时工单（对齐 TicketInternalPremium 的 ticket_time）：
// 按「每 N 天 / 自然月 / 年」循环，在日期范围内于触发时间自动创建内部工单；
// 循环周期留空表示一次性。

const message = useMessage()
const router = useRouter()
const rows = ref<any[]>([])
const total = ref(0)
const busy = ref(false)
const page = ref(1)
const limit = ref(20)
const departments = ref<any[]>([])
const staff = ref<any[]>([])

const typeTree = computed(() => departments.value.map((d: any) => ({
  label: d.name,
  key: `d-${d.id}`,
  value: `d-${d.id}`,
  disabled: true,
  children: (d.type || []).map((t: any) => ({ label: t.name, key: t.id, value: t.id })),
})))
const staffOptions = computed(() => staff.value.map((s: any) => ({ label: s.name, value: s.id })))

const unitText: Record<string, string> = { day: '天', month: '自然月', year: '年' }
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '--')
const day = (v?: string) => (v ? new Date(v).toLocaleDateString() : '--')
function periodText(r: any) {
  if (!r.cycle_period) return '一次性'
  return `每 ${r.cycle_period} ${unitText[r.unit] || r.unit}`
}

async function load() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/ticket-internal/cron', { params: { page: page.value, limit: limit.value } }))
    rows.value = d?.list || []
    total.value = d?.count || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取定时工单失败')
  } finally {
    busy.value = false
  }
}
async function loadBase() {
  try {
    const [dept, sf] = await Promise.all([
      api.get('/admin/ticket-internal/department'),
      api.get('/admin/ticket-internal/staff'),
    ])
    departments.value = dataOf<any>(dept)?.list || []
    staff.value = dataOf<any>(sf) || []
  } catch {
    message.error('读取部门 / 人员失败')
  }
}
onMounted(async () => { await loadBase(); load() })

const modalOpen = ref(false)
const modalBusy = ref(false)
const editingID = ref(0)
const form = reactive({
  title: '',
  cycle_period: null as number | null,
  unit: 'day',
  type_id: null as number | null,
  range: null as [number, number] | null,
  trigger_time: '09:00',
  admin_id: null as number | null,
  content: '',
})
function openCreate() {
  editingID.value = 0
  Object.assign(form, { title: '', cycle_period: null, unit: 'day', type_id: null, range: null, trigger_time: '09:00', admin_id: null, content: '' })
  modalOpen.value = true
}
async function openEdit(row: any) {
  try {
    const d = dataOf<any>(await api.get(`/admin/ticket-internal/cron/${row.id}`))
    editingID.value = row.id
    Object.assign(form, {
      title: d.title,
      cycle_period: d.cycle_period || null,
      unit: d.unit || 'day',
      type_id: d.type_id,
      range: [new Date(d.start_time).getTime(), d.end_time ? new Date(d.end_time).getTime() : new Date(d.start_time).getTime() + 7 * 86400000] as [number, number],
      trigger_time: d.trigger_time || '09:00',
      admin_id: d.admin_id || null,
      content: d.content || '',
    })
    modalOpen.value = true
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取定时工单失败')
  }
}
function departmentOfType(typeID: number | null) {
  if (!typeID) return null
  for (const d of departments.value) {
    if ((d.type || []).some((t: any) => t.id === typeID)) return d.id
  }
  return null
}
async function submit() {
  if (!form.title.trim()) { message.error('请填写工单标题'); return }
  if (!form.type_id) { message.error('请选择指定部门-类型'); return }
  if (!form.range) { message.error('请选择日期范围'); return }
  if (!/^\d{1,2}:\d{2}$/.test(form.trigger_time.trim())) { message.error('触发时间格式应为 HH:mm'); return }
  modalBusy.value = true
  try {
    const payload = {
      title: form.title.trim(),
      content: form.content,
      cycle_period: form.cycle_period || 0,
      unit: form.unit,
      start_time: Math.floor(form.range[0] / 1000),
      end_time: Math.floor(form.range[1] / 1000),
      trigger_time: form.trigger_time.trim(),
      department_id: departmentOfType(form.type_id),
      type_id: form.type_id,
      admin_id: form.admin_id || 0,
      status: 1,
    }
    if (editingID.value) await api.put(`/admin/ticket-internal/cron/${editingID.value}`, payload)
    else await api.post('/admin/ticket-internal/cron', payload)
    message.success('保存成功')
    modalOpen.value = false
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    modalBusy.value = false
  }
}
async function toggleStatus(row: any) {
  try {
    await api.put(`/admin/ticket-internal/cron/${row.id}/status`, { status: row.status ? 0 : 1 })
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}
async function remove(row: any) {
  try {
    await api.delete(`/admin/ticket-internal/cron/${row.id}`)
    message.success('删除成功')
    load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">客服工具</div>
        <h1>定时工单</h1>
        <p>按周期在日期范围内自动创建内部工单；循环周期留空为一次性。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary @click="router.push(ADMIN_PATH + '/ticket-internal')">返回内部工单</NButton>
        <NButton type="primary" @click="openCreate">＋ 新建定时工单</NButton>
      </div>
    </div>

    <div v-if="rows.length" class="table-scroll"><div class="user-table">
      <div class="user-row tic-row user-head">
        <span>ID</span><span>工单标题</span><span>循环周期</span><span>日期范围</span><span>下个周期日期</span><span>指定部门-类型</span><span>指定人员</span><span>创建人</span><span>状态</span><span>操作</span>
      </div>
      <div v-for="r in rows" :key="r.id" class="user-row tic-row">
        <span>{{ r.id }}</span>
        <span>{{ r.title }}</span>
        <span>{{ periodText(r) }}</span>
        <span class="muted">{{ day(r.start_time) }} ~ {{ day(r.end_time) }}</span>
        <span class="muted">{{ r.next_create_time ? fmt(r.next_create_time) : '--' }}</span>
        <span>{{ r.department_name }} - {{ r.type_name }}</span>
        <span>{{ r.admin_name || '--' }}</span>
        <span>{{ r.create_admin_name || '--' }}</span>
        <span><NSwitch size="small" :value="r.status === 1" @update:value="toggleStatus(r)" /></span>
        <span class="row" style="gap:6px">
          <NButton size="tiny" tertiary @click="openEdit(r)">编辑</NButton>
          <NButton size="tiny" tertiary type="error" @click="remove(r)">删除</NButton>
        </span>
      </div>
    </div></div>
    <div v-else class="empty-box">{{ busy ? '加载中…' : '还没有定时工单。' }}</div>

    <div v-if="total > limit" class="row" style="justify-content:flex-end;gap:8px;margin-top:10px">
      <NButton size="small" :disabled="page <= 1" @click="page--; load()">上一页</NButton>
      <span class="muted">第 {{ page }} / {{ Math.ceil(total / limit) }} 页</span>
      <NButton size="small" :disabled="page >= Math.ceil(total / limit)" @click="page++; load()">下一页</NButton>
    </div>

    <NModal v-model:show="modalOpen" preset="card" :title="editingID ? '编辑定时工单' : '新建定时工单'" style="width:min(820px,96vw)">
      <div class="form-grid">
        <label><span>工单标题</span><NInput v-model:value="form.title" placeholder="工单标题" /></label>
        <label><span>循环周期（留空 = 一次性）</span><NInputNumber v-model:value="form.cycle_period" :min="0" style="width:100%" placeholder="N" /></label>
        <label><span>周期类型</span>
          <NSelect v-model:value="form.unit" :options="[{ label: '天', value: 'day' }, { label: '自然月', value: 'month' }, { label: '年', value: 'year' }]" />
        </label>
        <label><span>指定部门-类型</span>
          <NTreeSelect v-model:value="form.type_id" :options="typeTree" checkable cascade placeholder="部门 - 类型" />
        </label>
        <label><span>日期范围</span>
          <NDatePicker v-model:value="form.range" type="daterange" clearable style="width:100%" />
        </label>
        <label><span>触发时间（HH:mm）</span><NInput v-model:value="form.trigger_time" placeholder="09:00" /></label>
        <label><span>指定人员（可留空）</span><NSelect v-model:value="form.admin_id" :options="staffOptions" filterable clearable /></label>
        <label class="full"><span>工单内容</span><NInput v-model:value="form.content" type="textarea" :rows="5" placeholder="工单内容（支持 HTML）" /></label>
      </div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="modalOpen = false">取消</NButton>
          <NButton type="primary" :loading="modalBusy" @click="submit">保存</NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
.tic-row { grid-template-columns: 60px minmax(160px, 1.2fr) 110px 170px 160px 180px 110px 110px 70px 130px; }
.tic-row span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
