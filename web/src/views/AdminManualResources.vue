<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NDatePicker, NInput, NInputNumber, NModal, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import EntityPicker from '../components/EntityPicker.vue'

// 手动资源（对齐魔方 CBAP ManualResource 插件）。
// 供应商管理 + 资源台账（主 IP / 附加 IP / 配置 / 成本 / 系统账密 / 控制方式 /
// IPMI 或 DCIM 客户端参数 / 到期时间）+ 分配（关联客户产品）/ 空闲 + 电源操作。
// 插件的 VNC 控制台与重装 / 救援 / 破解密码依赖加密的 DCIM 客户端协议，不落地。

const message = useMessage()
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string | null) => (v ? new Date(v).toLocaleDateString() : '—')

// ---- 供应商 ----
const suppliers = ref<any[]>([])
const supplierDialog = ref(false)
const supplierEditing = ref('')
const supplierForm = reactive({ name: '', contact: '', notes: '' })

async function loadSuppliers() {
  try {
    suppliers.value = dataOf<any[]>(await api.get('/admin/manual-suppliers')) || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取供应商失败')
  }
}
function openSuppliers() {
  supplierDialog.value = true
  loadSuppliers()
}
async function saveSupplier() {
  if (!supplierForm.name.trim()) { message.error('请输入名称'); return }
  try {
    const payload = { ...supplierForm, name: supplierForm.name.trim() }
    if (supplierEditing.value) await api.put(`/admin/manual-suppliers/${supplierEditing.value}`, payload)
    else await api.post('/admin/manual-suppliers', payload)
    message.success('供应商已保存')
    supplierEditing.value = ''
    supplierForm.name = ''; supplierForm.contact = ''; supplierForm.notes = ''
    await loadSuppliers()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
function editSupplier(s: any) {
  supplierEditing.value = s.id
  supplierForm.name = s.name
  supplierForm.contact = s.contact
  supplierForm.notes = s.notes
}
async function removeSupplier(s: any) {
  try {
    await api.delete(`/admin/manual-suppliers/${s.id}`)
    message.success('供应商已删除')
    await loadSuppliers()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}
const supplierOptions = computed(() => suppliers.value.map(s => ({ label: s.name, value: s.id })))

// ---- 资源 ----
const resources = ref<any[]>([])
const keyword = ref('')
const supplierFilter = ref<string | null>(null)
const statusFilter = ref<string | null>(null)
const busy = ref(false)
const resourceDialog = ref(false)
const resourceEditing = ref('')
const form = reactive({
  dedicated_ip: '', assigned_ips: '', notes: '', configuration: '', cost: 0,
  username: '', password: '', control_mode: 'ipmi',
  ipmi_ip: '', ipmi_port: 623, ipmi_version: '',
  dcim_client_url: '', dcim_client_id: '', control_username: '', control_password: '',
  due_time: null as number | null, supplier_id: '',
})

const powerText: Record<string, string> = { on: '开机', off: '关机', error: '错误' }
const powerType = (s: string) => ({ on: 'success', off: 'default', error: 'error' } as any)[s] || 'default'

async function load() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/manual-resources', {
      params: { keyword: keyword.value.trim(), supplier: supplierFilter.value || '', status: statusFilter.value || '', limit: 100 },
    }))
    resources.value = d?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取手动资源失败')
  } finally {
    busy.value = false
  }
}

function resetForm() {
  resourceEditing.value = ''
  form.dedicated_ip = ''; form.assigned_ips = ''; form.notes = ''; form.configuration = ''
  form.cost = 0; form.username = ''; form.password = ''; form.control_mode = 'ipmi'
  form.ipmi_ip = ''; form.ipmi_port = 623; form.ipmi_version = ''
  form.dcim_client_url = ''; form.dcim_client_id = ''; form.control_username = ''; form.control_password = ''
  form.due_time = null; form.supplier_id = ''
}
function openCreate() { resetForm(); resourceDialog.value = true }
function openEdit(r: any) {
  resetForm()
  resourceEditing.value = r.id
  form.dedicated_ip = r.dedicated_ip
  form.assigned_ips = (r.assigned_ips || []).join('\n')
  form.notes = r.notes; form.configuration = r.configuration
  form.cost = Number(r.cost_cents || 0) / 100
  form.username = r.username; form.password = r.password
  form.control_mode = r.control_mode
  form.ipmi_ip = r.ipmi_ip; form.ipmi_port = Number(r.ipmi_port || 623); form.ipmi_version = r.ipmi_version
  form.dcim_client_url = r.dcim_client_url; form.dcim_client_id = r.dcim_client_id
  form.control_username = r.control_username; form.control_password = r.control_password
  form.due_time = r.due_time ? new Date(r.due_time).getTime() : null
  form.supplier_id = r.supplier_id
  resourceDialog.value = true
}
async function save() {
  if (!form.dedicated_ip.trim()) { message.error('请填写主 IP'); return }
  busy.value = true
  try {
    const payload: any = {
      ...form,
      dedicated_ip: form.dedicated_ip.trim(),
      cost: form.cost,
      due_time: form.due_time ? new Date(form.due_time).toISOString() : '',
    }
    if (resourceEditing.value) await api.put(`/admin/manual-resources/${resourceEditing.value}`, payload)
    else await api.post('/admin/manual-resources', payload)
    message.success('资源已保存')
    resourceDialog.value = false
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    busy.value = false
  }
}
async function removeResource(r: any) {
  try {
    await api.delete(`/admin/manual-resources/${r.id}`)
    message.success('资源已删除')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 分配 / 空闲 ----
const assignDialog = ref(false)
const assignResource = ref<any>(null)
const assignService = ref('')
const assignDue = ref<number | null>(null)
function openAssign(r: any) {
  assignResource.value = r
  assignService.value = ''
  assignDue.value = r.due_time ? new Date(r.due_time).getTime() : null
  assignDialog.value = true
}
async function doAssign() {
  if (!assignService.value) { message.error('请选择要分配到的产品（服务）'); return }
  try {
    await api.post(`/admin/manual-resources/${assignResource.value.id}/assign`, {
      service_id: assignService.value,
      due_time: assignDue.value ? new Date(assignDue.value).toISOString() : '',
    })
    message.success('已分配')
    assignDialog.value = false
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '分配失败')
  }
}
async function idle(r: any) {
  try {
    await api.post(`/admin/manual-resources/${r.id}/idle`, {})
    message.success('资源已置为空闲')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}

// ---- 电源操作 ----
const powerBusy = ref('')
async function power(r: any, action: 'status' | 'on' | 'off' | 'reboot') {
  powerBusy.value = r.id + action
  try {
    const d = dataOf<any>(await api.post(`/admin/manual-resources/${r.id}/power/${action}`, {}))
    message.success(action === 'status' ? (d.power_on ? '电源状态：开机' : '电源状态：关机') : '操作成功')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '电源操作失败')
  } finally {
    powerBusy.value = ''
  }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>手动资源</h1>
        <p>对齐魔方「手动资源」插件：独立服务器台账与供应商管理；IPMI 模式支持开机 / 关机 / 重启 / 电源状态，资源可分配到客户名下产品。控制台与重装 / 救援依赖 DCIM 客户端（协议加密不可读），未落地。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary :loading="busy" @click="load">刷新</NButton>
        <NButton tertiary @click="openSuppliers">管理供应商</NButton>
        <NButton type="primary" @click="openCreate">＋ 添加资源</NButton>
      </div>
    </div>

    <section class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="keyword" clearable placeholder="搜索 IP、配置、备注" style="max-width:240px" @keyup.enter="load" />
        <NSelect v-model:value="supplierFilter" :options="supplierOptions" placeholder="选择供应商" clearable style="width:160px" />
        <NSelect v-model:value="statusFilter" :options="[{ label: '空闲', value: 'idle' }, { label: '已分配', value: 'assigned' }]" placeholder="状态" clearable style="width:120px" />
        <NButton type="primary" :loading="busy" @click="load">查询</NButton>
        <span class="muted" style="align-self:center">共 {{ resources.length }} 台</span>
      </div>

      <div v-if="resources.length" class="table-scroll"><div class="user-table">
        <div class="user-row mr-row user-head">
          <span>IP</span><span>电源状态</span><span>机器操作</span><span>配置</span><span>用户名/密码</span><span>供应商/成本</span><span>关联客户（产品）</span><span>到期时间</span><span>备注</span><span>操作</span>
        </div>
        <div v-for="r in resources" :key="r.id" class="user-row mr-row">
          <span><b>{{ r.dedicated_ip }}</b><small v-if="(r.assigned_ips || []).length" class="muted"><br />+{{ r.assigned_ips.length }} 附加</small></span>
          <span><NTag v-if="r.power_status" :type="powerType(r.power_status)" size="tiny" round>{{ powerText[r.power_status] || r.power_status }}</NTag><span v-else class="muted">—</span></span>
          <span class="row" style="gap:4px;flex-wrap:wrap">
            <NButton size="tiny" tertiary :loading="powerBusy === r.id + 'on'" @click="power(r, 'on')">开机</NButton>
            <NButton size="tiny" tertiary :loading="powerBusy === r.id + 'off'" @click="power(r, 'off')">关机</NButton>
            <NButton size="tiny" tertiary :loading="powerBusy === r.id + 'reboot'" @click="power(r, 'reboot')">重启</NButton>
            <NButton size="tiny" tertiary :loading="powerBusy === r.id + 'status'" @click="power(r, 'status')">电源状态</NButton>
          </span>
          <span class="muted">{{ r.configuration || '—' }}</span>
          <span class="muted">{{ r.username || '—' }}<template v-if="r.password"> / {{ r.password }}</template></span>
          <span class="muted">{{ r.supplier_name || '—' }} / {{ money(r.cost_cents) }}</span>
          <span v-if="r.status === 'assigned'">{{ r.user_email }}<small class="muted">（{{ r.service_name }}）</small></span>
          <span v-else class="muted">空闲</span>
          <span class="muted">{{ fmt(r.due_time) }}</span>
          <span class="muted">{{ r.notes || '—' }}</span>
          <span class="row" style="gap:4px">
            <NButton size="tiny" tertiary @click="openAssign(r)">分配</NButton>
            <NButton v-if="r.status === 'assigned'" size="tiny" tertiary @click="idle(r)">空闲</NButton>
            <NButton size="tiny" tertiary @click="openEdit(r)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeResource(r)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有手动资源，点击「添加资源」创建。</div>
    </section>

    <!-- 资源表单 -->
    <NModal v-model:show="resourceDialog" preset="card" :title="resourceEditing ? '编辑资源' : '添加资源'" style="width:min(680px,94vw)">
      <div class="form-grid">
        <label><span>主 IP</span><NInput v-model:value="form.dedicated_ip" placeholder="主 IP" /></label>
        <label><span>供应商</span><NSelect v-model:value="form.supplier_id" :options="supplierOptions" clearable placeholder="请选择" /></label>
        <label><span>成本（元）</span><NInputNumber v-model:value="form.cost" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>到期时间</span><NDatePicker v-model:value="form.due_time" type="datetime" clearable style="width:100%" /></label>
        <label class="full"><span>附加 IP（换行添加多个）</span><NInput v-model:value="form.assigned_ips" type="textarea" :rows="2" placeholder="附加 IP" /></label>
        <label><span>系统用户名</span><NInput v-model:value="form.username" placeholder="系统用户名" /></label>
        <label><span>系统密码</span><NInput v-model:value="form.password" placeholder="系统密码" /></label>
        <label><span>配置</span><NInput v-model:value="form.configuration" placeholder="如 2C4G/500G" /></label>
        <label><span>控制方式</span>
          <NSelect v-model:value="form.control_mode" :options="[{ label: 'ipmi', value: 'ipmi' }, { label: '客户端（DCIM）', value: 'client' }]" />
        </label>
        <template v-if="form.control_mode === 'ipmi'">
          <label><span>IPMI IP</span><NInput v-model:value="form.ipmi_ip" placeholder="IPMI IP" /></label>
          <label><span>端口</span><NInputNumber v-model:value="form.ipmi_port" :min="1" :max="65535" style="width:100%" /></label>
          <label><span>IPMI 版本</span><NInput v-model:value="form.ipmi_version" placeholder="如 2.0" /></label>
          <label><span>控制用户名</span><NInput v-model:value="form.control_username" placeholder="控制用户名" /></label>
          <label><span>控制密码</span><NInput v-model:value="form.control_password" placeholder="控制密码" /></label>
        </template>
        <template v-else>
          <label class="full"><span>DCIM 客户端地址</span><NInput v-model:value="form.dcim_client_url" placeholder="http://…" /></label>
          <label><span>服务器 ID</span><NInput v-model:value="form.dcim_client_id" placeholder="服务器 ID" /></label>
        </template>
        <label class="full"><span>备注</span><NInput v-model:value="form.notes" type="textarea" :rows="2" placeholder="备注" /></label>
      </div>
      <div v-if="form.control_mode === 'client'" class="security-note">「客户端（DCIM）」控制协议在魔方参考源中加密不可读，电源操作与控制台暂不支持，仅作台账记录。</div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" :loading="busy" @click="save">保存</NButton>
        <NButton secondary @click="resourceDialog = false">取消</NButton>
      </div>
    </NModal>

    <!-- 供应商 -->
    <NModal v-model:show="supplierDialog" preset="card" title="管理供应商" style="width:min(620px,94vw)">
      <div class="form-grid" style="align-items:end">
        <label><span>名称</span><NInput v-model:value="supplierForm.name" placeholder="名称" /></label>
        <label><span>联系方式</span><NInput v-model:value="supplierForm.contact" placeholder="联系方式" /></label>
        <label><span>备注</span><NInput v-model:value="supplierForm.notes" placeholder="备注" /></label>
        <NButton type="primary" @click="saveSupplier">{{ supplierEditing ? '保存' : '添加' }}</NButton>
      </div>
      <div v-if="suppliers.length" class="table-scroll" style="margin-top:10px"><div class="user-table">
        <div class="user-row mr-sup-row user-head"><span>名称</span><span>联系方式</span><span>资源数</span><span>备注</span><span>操作</span></div>
        <div v-for="s in suppliers" :key="s.id" class="user-row mr-sup-row">
          <span><b>{{ s.name }}</b></span>
          <span class="muted">{{ s.contact || '—' }}</span>
          <span class="muted">{{ s.count }}</span>
          <span class="muted">{{ s.notes || '—' }}</span>
          <span class="row" style="gap:4px">
            <NButton size="tiny" tertiary @click="editSupplier(s)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeSupplier(s)">删除</NButton>
          </span>
        </div>
      </div></div>
    </NModal>

    <!-- 分配 -->
    <NModal v-model:show="assignDialog" preset="card" :title="`分配「${assignResource?.dedicated_ip || ''}」`" style="width:min(520px,94vw)">
      <div class="form-grid">
        <label class="full"><span>选择要分配到的产品（服务，客户随服务带出）</span>
          <EntityPicker v-model="assignService" kind="service" placeholder="点击搜索服务" title="搜索服务" />
        </label>
        <label class="full"><span>到期时间（留空沿用原值）</span>
          <NDatePicker v-model:value="assignDue" type="datetime" clearable style="width:100%" />
        </label>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" @click="doAssign">确认分配该资源</NButton>
        <NButton secondary @click="assignDialog = false">取消</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.mr-row { grid-template-columns: minmax(120px, .8fr) 70px minmax(210px, 1.3fr) minmax(110px, .9fr) minmax(120px, .8fr) minmax(120px, .8fr) minmax(150px, 1fr) 90px minmax(100px, .7fr) 190px; }
.mr-sup-row { grid-template-columns: minmax(120px, 1fr) minmax(120px, .9fr) 70px minmax(120px, .9fr) 110px; }
</style>
