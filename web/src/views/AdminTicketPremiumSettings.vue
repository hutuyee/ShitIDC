<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NColorPicker, NInput, NInputNumber, NModal, NSelect, NSwitch, NTabPane, NTabs, NTag, useMessage } from 'naive-ui'
import { useRouter } from 'vue-router'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'

// 用户工单配置（对齐魔方 CBAP TicketPremium 的 ticket_setting）：
// 部门设置（管理人员 / 主管 / 工单类型 + 处理时限）、工单状态（含完结状态与颜色）、
// 预设回复、其他设置（接单后回复 / 仅领取人回复 / 工单须知 / 刷新时间）。

const message = useMessage()
const router = useRouter()
const tab = ref('department')
const busy = ref(false)

const staff = ref<any[]>([])
const departments = ref<any[]>([])
const statuses = ref<any[]>([])
const prereplies = ref<any[]>([])
const config = reactive({
  ticket_receive_reply: '0', ticket_follow_reply: '0',
  ticket_notice_open: '0', ticket_notice_description: '', refresh_time: '180',
})
const refreshSeconds = ref(180)

const staffOptions = computed(() => staff.value.map((s: any) => ({ label: s.name, value: s.id })))

async function loadAll() {
  busy.value = true
  try {
    const [sf, dept, st, pp, cfg] = await Promise.all([
      api.get('/admin/ticket-premium/staff'),
      api.get('/admin/ticket-premium/department'),
      api.get('/admin/ticket-premium/status'),
      api.get('/admin/ticket-premium/prereply'),
      api.get('/admin/ticket-premium/config'),
    ])
    staff.value = dataOf<any>(sf)?.list || []
    departments.value = dataOf<any>(dept)?.list || []
    statuses.value = dataOf<any>(st)?.list || []
    prereplies.value = dataOf<any>(pp)?.list || []
    Object.assign(config, dataOf<any>(cfg) || {})
    refreshSeconds.value = Number(config.refresh_time) || 180
  } catch {
    message.error('读取工单配置失败')
  } finally {
    busy.value = false
  }
}
onMounted(loadAll)

// ---- 部门设置 ----
const deptOpen = ref(false)
const deptBusy = ref(false)
const deptForm = reactive({
  id: 0,
  name: '',
  admin_id: [] as number[],
  director_admin_id: null as number | null,
  types: [] as { id?: number; name: string; processing_limit: number }[],
})
function openDept(row?: any) {
  if (row) {
    Object.assign(deptForm, {
      id: row.id,
      name: row.name,
      admin_id: [...(row.admin_ids || [])],
      director_admin_id: row.director_admin_id || null,
      types: (row.type || []).map((t: any) => ({ id: t.id, name: t.name, processing_limit: t.processing_limit })),
    })
  } else {
    Object.assign(deptForm, { id: 0, name: '', admin_id: [], director_admin_id: null, types: [{ name: '', processing_limit: 24 }] })
  }
  deptOpen.value = true
}
function addTypeRow() { deptForm.types.push({ name: '', processing_limit: 24 }) }
function removeTypeRow(i: number) { deptForm.types.splice(i, 1) }
async function saveDept() {
  if (!deptForm.name.trim()) { message.error('请填写部门名称'); return }
  if (!deptForm.admin_id.length) { message.error('请选择部门管理人员'); return }
  if (!deptForm.director_admin_id) { message.error('请选择部门主管'); return }
  if (!deptForm.types.some((t) => t.name.trim())) { message.error('请至少配置一个工单类型'); return }
  deptBusy.value = true
  try {
    const payload = {
      name: deptForm.name.trim(),
      admin_id: deptForm.admin_id,
      director_admin_id: deptForm.director_admin_id,
      type: deptForm.types.filter((t) => t.name.trim()).map((t) => ({ id: t.id || 0, name: t.name.trim(), processing_limit: t.processing_limit || 24 })),
    }
    if (deptForm.id) await api.put(`/admin/ticket-premium/department/${deptForm.id}`, payload)
    else await api.post('/admin/ticket-premium/department', payload)
    message.success('保存成功')
    deptOpen.value = false
    loadAll()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    deptBusy.value = false
  }
}
async function removeDept(row: any) {
  try {
    await api.delete(`/admin/ticket-premium/department/${row.id}`)
    message.success('删除成功')
    loadAll()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 工单状态 ----
const statusEditing = ref(0)
const statusDraft = reactive({ name: '', color: '#0052D9', finished: '0' })
const statusAdding = ref(false)
const statusAddDraft = reactive({ name: '', color: '#0052D9', finished: '0' })
function startEditStatus(row: any) {
  statusEditing.value = row.id
  Object.assign(statusDraft, { name: row.name, color: row.color, finished: row.finished ? '1' : '0' })
}
async function saveStatus(row: any) {
  try {
    await api.put(`/admin/ticket-premium/status/${row.id}`, { name: statusDraft.name, color: statusDraft.color, finished: statusDraft.finished })
    message.success('保存成功')
    statusEditing.value = 0
    loadAll()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
async function saveNewStatus() {
  if (!statusAddDraft.name.trim()) { message.error('请输入状态名称'); return }
  try {
    await api.post('/admin/ticket-premium/status', { name: statusAddDraft.name, color: statusAddDraft.color, finished: statusAddDraft.finished })
    message.success('保存成功')
    statusAdding.value = false
    Object.assign(statusAddDraft, { name: '', color: '#0052D9', finished: '0' })
    loadAll()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
async function removeStatus(row: any) {
  try {
    await api.delete(`/admin/ticket-premium/status/${row.id}`)
    message.success('删除成功')
    loadAll()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 预设回复 ----
const prereplyText = ref('')
const prereplyEditID = ref(0)
async function savePrereply() {
  if (!prereplyText.value.trim()) { message.error('请输入回复内容'); return }
  try {
    if (prereplyEditID.value) await api.put(`/admin/ticket-premium/prereply/${prereplyEditID.value}`, { content: prereplyText.value })
    else await api.post('/admin/ticket-premium/prereply', { content: prereplyText.value })
    message.success('保存成功')
    prereplyText.value = ''
    prereplyEditID.value = 0
    loadAll()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
function editPrereply(row: any) { prereplyEditID.value = row.id; prereplyText.value = row.content }
async function removePrereply(row: any) {
  try {
    await api.delete(`/admin/ticket-premium/prereply/${row.id}`)
    loadAll()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 其他设置 ----
async function saveConfig() {
  try {
    await api.put('/admin/ticket-premium/config', {
      ticket_receive_reply: config.ticket_receive_reply,
      ticket_follow_reply: config.ticket_follow_reply,
      ticket_notice_open: config.ticket_notice_open,
      ticket_notice_description: config.ticket_notice_description,
      refresh_time: String(refreshSeconds.value || 180),
    })
    message.success('保存成功')
    loadAll()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">客服工具</div>
        <h1>工单配置</h1>
        <p>维护用户工单的部门、类型与处理时限，自定义工单状态，配置预设回复与工单须知。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary @click="router.push(ADMIN_PATH + '/tickets')">返回工单管理</NButton>
        <NButton secondary @click="router.push(ADMIN_PATH + '/tickets/stats')">工单统计</NButton>
      </div>
    </div>

    <NTabs v-model:value="tab" type="line" animated>
      <NTabPane name="department" tab="部门设置">
        <div class="row" style="justify-content:flex-end;margin-bottom:10px">
          <NButton type="primary" @click="openDept()">＋ 新增部门</NButton>
        </div>
        <div v-if="departments.length" class="table-scroll"><div class="user-table">
          <div class="user-row tis-row user-head"><span>部门名称</span><span>工单类型（处理时限）</span><span>管理人员</span><span>部门主管</span><span>操作</span></div>
          <div v-for="d in departments" :key="d.id" class="user-row tis-row">
            <span><b>{{ d.name }}</b></span>
            <span>
              <NTag v-for="t in d.type" :key="t.id" size="tiny" style="margin:2px 4px 2px 0">{{ t.name }}（{{ t.processing_limit }}h）</NTag>
            </span>
            <span>{{ (d.admin || []).map((a: any) => a.name).join('，') }}</span>
            <span>{{ d.director_admin_name || '--' }}</span>
            <span class="row" style="gap:6px">
              <NButton size="tiny" tertiary @click="openDept(d)">编辑</NButton>
              <NButton size="tiny" tertiary type="error" @click="removeDept(d)">删除</NButton>
            </span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有工单部门，点击「新增部门」创建。</div>
      </NTabPane>

      <NTabPane name="status" tab="工单状态">
        <div class="row" style="justify-content:flex-end;margin-bottom:10px">
          <NButton type="primary" @click="statusAdding = true">＋ 新增状态</NButton>
        </div>
        <div class="table-scroll"><div class="user-table">
          <div class="user-row tis-status user-head"><span>序号</span><span>工单状态</span><span>状态颜色</span><span>完结状态</span><span>操作</span></div>
          <div v-for="(s, i) in statuses" :key="s.id" class="user-row tis-status">
            <span>{{ i + 1 }}</span>
            <span>
              <NInput v-if="statusEditing === s.id" v-model:value="statusDraft.name" size="small" />
              <template v-else><NTag :style="{ background: s.color, color: '#fff' }" size="small">{{ s.name }}</NTag></template>
            </span>
            <span><NColorPicker v-if="statusEditing === s.id" v-model:value="statusDraft.color" size="small" :show-alpha="false" style="width:90px" /><span v-else class="muted">{{ s.color }}</span></span>
            <span>{{ s.finished ? '完结' : '未完结' }}</span>
            <span class="row" style="gap:6px">
              <template v-if="statusEditing === s.id">
                <NButton size="tiny" type="primary" @click="saveStatus(s)">保存</NButton>
                <NButton size="tiny" @click="statusEditing = 0">取消</NButton>
              </template>
              <template v-else>
                <NButton size="tiny" tertiary :disabled="s.system" @click="startEditStatus(s)">编辑</NButton>
                <NButton size="tiny" tertiary type="error" :disabled="s.system" @click="removeStatus(s)">删除</NButton>
              </template>
            </span>
          </div>
          <div v-if="statusAdding" class="user-row tis-status">
            <span>--</span>
            <span><NInput v-model:value="statusAddDraft.name" size="small" placeholder="状态名称" /></span>
            <span><NColorPicker v-model:value="statusAddDraft.color" size="small" :show-alpha="false" style="width:90px" /></span>
            <span><NSelect v-model:value="statusAddDraft.finished" size="small" :options="[{ label: '完结', value: '1' }, { label: '未完结', value: '0' }]" style="width:110px" /></span>
            <span class="row" style="gap:6px"><NButton size="tiny" type="primary" @click="saveNewStatus">保存</NButton><NButton size="tiny" @click="statusAdding = false">取消</NButton></span>
          </div>
        </div></div>
        <p class="muted" style="font-size:12px">待处理、待回复、已关闭为系统默认状态，无法修改或删除；完结状态下的工单会计入「已处理」。</p>
      </NTabPane>

      <NTabPane name="prereply" tab="预设回复">
        <div class="card" style="margin-bottom:12px">
          <NInput v-model:value="prereplyText" type="textarea" :rows="4" placeholder="预设回复内容" />
          <div class="row" style="justify-content:flex-end;gap:8px;margin-top:8px">
            <NButton v-if="prereplyEditID" @click="prereplyEditID = 0; prereplyText = ''">取消编辑</NButton>
            <NButton type="primary" @click="savePrereply">{{ prereplyEditID ? '保存修改' : '新增预设回复' }}</NButton>
          </div>
        </div>
        <div v-for="p in prereplies" :key="p.id" class="ti-message">
          <div class="row" style="justify-content:space-between">
            <div style="flex:1;white-space:pre-wrap">{{ p.content }}</div>
            <span class="row" style="gap:6px;margin-left:10px">
              <NButton size="tiny" tertiary @click="editPrereply(p)">编辑</NButton>
              <NButton size="tiny" tertiary type="error" @click="removePrereply(p)">删除</NButton>
            </span>
          </div>
        </div>
      </NTabPane>

      <NTabPane name="other" tab="其他设置">
        <div class="card" style="max-width:680px">
          <div class="col" style="gap:14px">
            <div class="row" style="justify-content:space-between"><span>接单后回复<small class="muted">（开启后必须点击接单才能回复）</small></span>
              <NSwitch v-model:value="config.ticket_receive_reply" checked-value="1" unchecked-value="0" /></div>
            <div class="row" style="justify-content:space-between"><span>仅领取人可回复<small class="muted">（开启后仅领取人可以回复，其他人需先接单）</small></span>
              <NSwitch v-model:value="config.ticket_follow_reply" checked-value="1" unchecked-value="0" /></div>
            <div class="row" style="justify-content:space-between"><span>工单须知<small class="muted">（在用户提交工单页展示）</small></span>
              <NSwitch v-model:value="config.ticket_notice_open" checked-value="1" unchecked-value="0" /></div>
            <div class="col" style="gap:6px">
              <span>须知内容</span>
              <NInput v-model:value="config.ticket_notice_description" type="textarea" :rows="4" placeholder="例如：请先描述清楚问题，紧急问题请电话联系。" />
            </div>
            <div class="row" style="justify-content:space-between"><span>列表自动刷新（秒）</span>
              <NInputNumber v-model:value="refreshSeconds" :min="30" :max="3600" style="width:160px" /></div>
            <div class="row" style="justify-content:flex-end"><NButton type="primary" @click="saveConfig">保存</NButton></div>
          </div>
        </div>
      </NTabPane>
    </NTabs>

    <NModal v-model:show="deptOpen" preset="card" :title="deptForm.id ? '编辑部门' : '新增部门'" style="width:min(860px,96vw)">
      <div class="form-grid">
        <label><span>部门名称</span><NInput v-model:value="deptForm.name" placeholder="部门名称" /></label>
        <label><span>部门主管</span><NSelect v-model:value="deptForm.director_admin_id" :options="staffOptions" filterable placeholder="部门主管" /></label>
        <label class="full"><span>管理人员</span><NSelect v-model:value="deptForm.admin_id" :options="staffOptions" multiple filterable placeholder="部门管理人员" /></label>
      </div>
      <div style="margin-top:10px">
        <div class="row" style="justify-content:space-between"><b>工单类型与处理时限</b><NButton size="small" secondary @click="addTypeRow">＋ 新增类型</NButton></div>
        <div v-for="(t, i) in deptForm.types" :key="i" class="row" style="gap:8px;margin-top:8px">
          <NInput v-model:value="t.name" placeholder="类型名称" style="flex:1" />
          <NInputNumber v-model:value="t.processing_limit" :min="1" :max="720" style="width:180px">
            <template #suffix>小时</template>
          </NInputNumber>
          <NButton tertiary type="error" size="small" @click="removeTypeRow(i)">删除</NButton>
        </div>
      </div>
      <template #footer>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton @click="deptOpen = false">取消</NButton>
          <NButton type="primary" :loading="deptBusy" @click="saveDept">保存</NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
.tis-row { grid-template-columns: 180px minmax(260px, 1.4fr) 220px 150px 160px; }
.tis-status { grid-template-columns: 70px 200px 150px 120px 180px; }
.ti-message { border: 1px solid #f0f0f0; border-radius: 8px; padding: 10px 12px; margin-bottom: 10px; background: #fafcff; }
</style>
