<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NModal, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 万云资源管理（对齐魔方 CBAP WanyunResource 插件）。
// 五个页签与插件页面一致：IP 段 / 节点 / VLAN / 光纤 / 纤芯（在光纤详情内管理）。
// 插件的 DCIM 接口同步（加密协议不可读）不落地，数据全部手工维护。

const message = useMessage()
const tab = ref<'ips' | 'nodes' | 'vlans' | 'fibers'>('ips')
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')

// ---- 自定义字段（节点 / 纤芯共用结构，scope 区分） ----
type FieldDef = { id: string; field_name: string; field_type: string; field_option: string; is_required: boolean; show_list: boolean }
const fieldDialog = ref(false)
const fieldScope = ref<'node' | 'fiber_core'>('node')
const fieldDefs = ref<FieldDef[]>([])
const fieldForm = reactive({ field_name: '', field_type: 'text', field_option: '', is_required: false, show_list: false })
const fieldEditing = ref('')

async function loadFields(scope: 'node' | 'fiber_core') {
  try {
    fieldDefs.value = dataOf<any[]>(await api.get(`/admin/wanyun/fields/${scope}`)) || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取自定义字段失败')
  }
}
function openFields(scope: 'node' | 'fiber_core') {
  fieldScope.value = scope
  loadFields(scope)
  fieldDialog.value = true
}
function resetFieldForm() {
  fieldEditing.value = ''
  fieldForm.field_name = ''
  fieldForm.field_type = 'text'
  fieldForm.field_option = ''
  fieldForm.is_required = false
  fieldForm.show_list = false
}
async function saveField() {
  if (!fieldForm.field_name.trim()) { message.error('请填写字段名称'); return }
  if (fieldForm.field_type === 'dropdown' && !fieldForm.field_option.trim()) { message.error('下拉字段请填写下拉值（英文半角逗号分隔）'); return }
  const payload = { field_name: fieldForm.field_name.trim(), field_type: fieldForm.field_type, field_option: fieldForm.field_option.trim(), is_required: fieldForm.is_required, show_list: fieldForm.show_list }
  try {
    if (fieldEditing.value) await api.put(`/admin/wanyun/fields/${fieldScope.value}/${fieldEditing.value}`, payload)
    else await api.post(`/admin/wanyun/fields/${fieldScope.value}`, payload)
    message.success('字段已保存')
    resetFieldForm()
    await loadFields(fieldScope.value)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存字段失败')
  }
}
async function toggleFieldShow(f: FieldDef) {
  try {
    await api.put(`/admin/wanyun/fields/${fieldScope.value}/${f.id}/show`, { show: !f.show_list })
    await loadFields(fieldScope.value)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}
async function removeField(f: FieldDef) {
  try {
    await api.delete(`/admin/wanyun/fields/${fieldScope.value}/${f.id}`)
    message.success('字段已删除（字段值一并删除）')
    await loadFields(fieldScope.value)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}
async function dragField(f: FieldDef, dir: -1 | 1) {
  const idx = fieldDefs.value.findIndex(x => x.id === f.id)
  // 服务端语义是「移到 prev 之后」（prev 为空表示最前）：
  // 上移 = 放到「前前一项」之后（前一项没有则放到最前）；下移 = 放到「后一项」之后。
  let prevId = ''
  if (dir === -1) {
    if (idx > 1) prevId = fieldDefs.value[idx - 2].id
  } else {
    if (idx >= 0 && idx < fieldDefs.value.length - 1) prevId = fieldDefs.value[idx + 1].id
    else return
  }
  try {
    await api.put(`/admin/wanyun/fields/${fieldScope.value}/${f.id}/drag`, { prev_id: prevId })
    await loadFields(fieldScope.value)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '排序失败')
  }
}
const fieldTypeText = (t: string) => (t === 'dropdown' ? '下拉选择' : '文本框')
const fieldOptions = (f: FieldDef) => (f.field_option || '').split(',').map(x => x.trim()).filter(Boolean)

// ---- 类型管理（节点类型 / VLAN 类型共用弹窗） ----
const typeDialog = ref(false)
const typeScope = ref<'node' | 'vlan'>('node')
const nodeTypes = ref<any[]>([])
const vlanTypes = ref<any[]>([])
const typeForm = reactive({ name: '' })
const typeEditing = ref('')
const typePath = computed(() => (typeScope.value === 'node' ? 'node-types' : 'vlan-types'))

async function loadTypes(scope: 'node' | 'vlan') {
  try {
    const list = dataOf<any[]>(await api.get(`/admin/wanyun/${scope === 'node' ? 'node-types' : 'vlan-types'}`)) || []
    if (scope === 'node') nodeTypes.value = list
    else vlanTypes.value = list
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取类型失败')
  }
}
function openTypes(scope: 'node' | 'vlan') {
  typeScope.value = scope
  loadTypes(scope)
  typeDialog.value = true
}
async function saveType() {
  if (!typeForm.name.trim()) { message.error('请输入类型名称'); return }
  try {
    if (typeEditing.value) await api.put(`/admin/wanyun/${typePath.value}/${typeEditing.value}`, { name: typeForm.name.trim() })
    else await api.post(`/admin/wanyun/${typePath.value}`, { name: typeForm.name.trim() })
    typeForm.name = ''
    typeEditing.value = ''
    await loadTypes(typeScope.value)
    message.success('类型已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存类型失败')
  }
}
async function removeType(t: any) {
  try {
    await api.delete(`/admin/wanyun/${typePath.value}/${t.id}`)
    message.success('类型已删除')
    await loadTypes(typeScope.value)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}
const typeOptions = computed(() => (typeScope.value === 'node' ? nodeTypes.value : vlanTypes.value).map(t => ({ label: `${t.name}（${t.count}）`, value: t.id })))

// ---- 节点 ----
const nodes = ref<any[]>([])
const nodeKeyword = ref('')
const nodesBusy = ref(false)
const nodeDialog = ref(false)
const nodeEditing = ref('')
const nodeForm = reactive({ name: '', type_id: '', fields: {} as Record<string, string> })
const nodeFieldDefs = ref<FieldDef[]>([])

async function loadNodes() {
  nodesBusy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/wanyun/nodes', { params: { keyword: nodeKeyword.value.trim(), limit: 100 } }))
    nodes.value = d?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取节点失败')
  } finally {
    nodesBusy.value = false
  }
}
async function openNode(n?: any) {
  nodeEditing.value = n?.id || ''
  nodeForm.name = n?.name || ''
  nodeForm.type_id = n?.type_id || ''
  nodeForm.fields = {}
  await loadFields('node')
  nodeFieldDefs.value = fieldDefs.value
  if (n) {
    try {
      const d = dataOf<any>(await api.get(`/admin/wanyun/nodes/${n.id}`))
      for (const f of d?.self_defined_field || []) nodeForm.fields[f.id] = f.value || ''
    } catch { /* 保留空值 */ }
  }
  nodeDialog.value = true
}
async function saveNode() {
  if (!nodeForm.name.trim()) { message.error('请填写节点名称'); return }
  nodesBusy.value = true
  try {
    const payload = { name: nodeForm.name.trim(), type_id: nodeForm.type_id || '', self_defined_field: nodeForm.fields }
    if (nodeEditing.value) await api.put(`/admin/wanyun/nodes/${nodeEditing.value}`, payload)
    else await api.post('/admin/wanyun/nodes', payload)
    message.success('节点已保存')
    nodeDialog.value = false
    await loadNodes()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存节点失败')
  } finally {
    nodesBusy.value = false
  }
}
async function removeNode(n: any) {
  try {
    await api.delete(`/admin/wanyun/nodes/${n.id}`)
    message.success('节点已删除')
    await loadNodes()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}
const nodeCustomShown = (n: any) => (n.self_defined_field || []).filter((f: any) => f.show_list)

// ---- VLAN ----
const vlans = ref<any[]>([])
const vlanKeyword = ref('')
const vlanStatus = ref<string | null>(null)
const vlansBusy = ref(false)
const vlanDialog = ref(false)
const vlanEditing = ref('')
const vlanForm = reactive({ vlan_id: 1, name: '', type_id: '', assignor: '', username: '', use_unit: '', node_ids: [] as string[], notes: '' })
const vlanStatusText = (v: boolean) => (v ? '启用' : '停用')

async function loadVlans() {
  vlansBusy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/wanyun/vlans', { params: { keyword: vlanKeyword.value.trim(), status: vlanStatus.value || '', limit: 100 } }))
    vlans.value = d?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取 VLAN 失败')
  } finally {
    vlansBusy.value = false
  }
}
function openVlan(v?: any) {
  vlanEditing.value = v?.id || ''
  vlanForm.vlan_id = Number(v?.vlan_id || 1)
  vlanForm.name = v?.name || ''
  vlanForm.type_id = v?.type_id || ''
  vlanForm.assignor = v?.assignor || ''
  vlanForm.username = v?.username || ''
  vlanForm.use_unit = v?.use_unit || ''
  vlanForm.node_ids = v?.node_ids || []
  vlanForm.notes = v?.notes || ''
  vlanDialog.value = true
}
async function saveVlan() {
  if (!vlanForm.name.trim()) { message.error('请填写 VLAN 名称'); return }
  if (vlanForm.vlan_id <= 0) { message.error('请填写 VLAN 编号'); return }
  vlansBusy.value = true
  try {
    const payload = { ...vlanForm, name: vlanForm.name.trim() }
    if (vlanEditing.value) await api.put(`/admin/wanyun/vlans/${vlanEditing.value}`, payload)
    else await api.post('/admin/wanyun/vlans', payload)
    message.success('VLAN 已保存')
    vlanDialog.value = false
    await loadVlans()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存 VLAN 失败')
  } finally {
    vlansBusy.value = false
  }
}
async function toggleVlan(v: any) {
  try {
    await api.put(`/admin/wanyun/vlans/${v.id}/status`, { active: !v.active })
    await loadVlans()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}
async function removeVlan(v: any) {
  try {
    await api.delete(`/admin/wanyun/vlans/${v.id}`)
    message.success('VLAN 已删除')
    await loadVlans()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- IP 段 ----
const segments = ref<any[]>([])
const ipKeyword = ref('')
const ipsBusy = ref(false)
const segmentDialog = ref(false)
const segmentEditing = ref('')
const segmentParent = ref('')
const segmentForm = reactive({ name: '', subnet: '', subnet_mask: '', gateway: '', group_name: '', notes: '' })
const addressDialog = ref(false)
const curSegment = ref<any>(null)
const addresses = ref<any[]>([])
const addrKeyword = ref('')
const addrDialog = ref(false)
const addrEditing = ref('')
const addrForm = reactive({ ip: '', assignor: '', username: '', use_unit: '', notes: '', used: false })

async function loadSegments() {
  ipsBusy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/wanyun/ip-segments', { params: { keyword: ipKeyword.value.trim() } }))
    segments.value = d?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取 IP 段失败')
  } finally {
    ipsBusy.value = false
  }
}
function openSegment(sub?: any, parent?: any) {
  segmentEditing.value = sub?.id || ''
  segmentParent.value = parent?.id || (sub ? '' : '')
  segmentForm.name = sub?.name || ''
  segmentForm.subnet = sub?.subnet || ''
  segmentForm.subnet_mask = sub?.subnet_mask || ''
  segmentForm.gateway = sub?.gateway || ''
  segmentForm.group_name = sub?.group_name || ''
  segmentForm.notes = sub?.notes || ''
  segmentDialog.value = true
}
async function saveSegment() {
  if (!segmentForm.subnet.trim()) { message.error('请填写 IP 段 / 子网'); return }
  ipsBusy.value = true
  try {
    const payload = { ...segmentForm, subnet: segmentForm.subnet.trim() }
    if (segmentEditing.value) await api.put(`/admin/wanyun/ip-segments/${segmentEditing.value}`, payload)
    else await api.post(`/admin/wanyun/ip-segments${segmentParent.value ? `?parent=${segmentParent.value}` : ''}`, payload)
    message.success('IP 段已保存')
    segmentDialog.value = false
    await loadSegments()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存 IP 段失败')
  } finally {
    ipsBusy.value = false
  }
}
async function removeSegment(s: any) {
  try {
    await api.delete(`/admin/wanyun/ip-segments/${s.id}`)
    message.success('IP 段已删除')
    await loadSegments()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}
async function openAddresses(s: any) {
  curSegment.value = s
  addrKeyword.value = ''
  await loadAddresses()
  addressDialog.value = true
}
async function loadAddresses() {
  if (!curSegment.value) return
  try {
    addresses.value = dataOf<any[]>(await api.get(`/admin/wanyun/ip-segments/${curSegment.value.id}/addresses`, { params: { keyword: addrKeyword.value.trim() } })) || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取地址明细失败')
  }
}
function openAddr(a?: any) {
  addrEditing.value = a?.id || ''
  addrForm.ip = a?.ip || ''
  addrForm.assignor = a?.assignor || ''
  addrForm.username = a?.username || ''
  addrForm.use_unit = a?.use_unit || ''
  addrForm.notes = a?.notes || ''
  addrForm.used = !!a?.used
  addrDialog.value = true
}
async function saveAddr() {
  if (!addrForm.ip.trim()) { message.error('请填写 IP'); return }
  try {
    const payload = { ...addrForm, ip: addrForm.ip.trim() }
    if (addrEditing.value) await api.put(`/admin/wanyun/ip-addresses/${addrEditing.value}`, payload)
    else await api.post(`/admin/wanyun/ip-segments/${curSegment.value.id}/addresses`, payload)
    message.success('地址已保存')
    addrDialog.value = false
    await loadAddresses()
    await loadSegments()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存地址失败')
  }
}
async function removeAddr(a: any) {
  try {
    await api.delete(`/admin/wanyun/ip-addresses/${a.id}`)
    message.success('地址已删除')
    await loadAddresses()
    await loadSegments()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 光纤 / 纤芯 ----
const fibers = ref<any[]>([])
const fiberKeyword = ref('')
const fibersBusy = ref(false)
const fiberDialog = ref(false)
const fiberEditing = ref('')
const fiberForm = reactive({ fiber_num: '', owner: '', core_num: 1, open_unit: '', construction_unit: '', contact: '', project: '', price: 0, node_ids: [] as string[], notes: '' })
const coreDialog = ref(false)
const curFiber = ref<any>(null)
const coreEditing = ref('')
const coreEditOpen = ref(false)
const coreForm = reactive({ num: '', node_ids: [] as string[], notes: '', fields: {} as Record<string, string> })
const coreFieldDefs = ref<FieldDef[]>([])

async function loadFibers() {
  fibersBusy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/wanyun/fibers', { params: { keyword: fiberKeyword.value.trim(), limit: 100 } }))
    fibers.value = d?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取光纤失败')
  } finally {
    fibersBusy.value = false
  }
}
async function openFiber(f?: any) {
  fiberEditing.value = f?.id || ''
  fiberForm.fiber_num = f?.fiber_num || ''
  fiberForm.owner = f?.owner || ''
  fiberForm.core_num = Number(f?.core_num || 1)
  fiberForm.open_unit = f?.open_unit || ''
  fiberForm.construction_unit = f?.construction_unit || ''
  fiberForm.contact = f?.contact || ''
  fiberForm.project = f?.project || ''
  fiberForm.price = Number(f?.price_cents || 0) / 100
  fiberForm.node_ids = f?.node_ids || []
  fiberForm.notes = f?.notes || ''
  fiberDialog.value = true
}
async function saveFiber() {
  if (!fiberForm.fiber_num.trim()) { message.error('请填写光纤编号'); return }
  fibersBusy.value = true
  try {
    const payload = { ...fiberForm, fiber_num: fiberForm.fiber_num.trim(), price_cents: Math.round(fiberForm.price * 100) }
    if (fiberEditing.value) await api.put(`/admin/wanyun/fibers/${fiberEditing.value}`, payload)
    else await api.post('/admin/wanyun/fibers', payload)
    message.success('光纤已保存')
    fiberDialog.value = false
    await loadFibers()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存光纤失败')
  } finally {
    fibersBusy.value = false
  }
}
async function removeFiber(f: any) {
  try {
    await api.delete(`/admin/wanyun/fibers/${f.id}`)
    message.success('光纤已删除')
    await loadFibers()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}
async function openCores(f: any) {
  curFiber.value = f
  coreEditing.value = ''
  await loadFields('fiber_core')
  coreFieldDefs.value = fieldDefs.value
  await loadCores()
  coreDialog.value = true
}
async function loadCores() {
  if (!curFiber.value) return
  try {
    const d = dataOf<any>(await api.get(`/admin/wanyun/fibers/${curFiber.value.id}`))
    curFiber.value = d
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取纤芯失败')
  }
}
function openCore(c?: any) {
  coreEditing.value = c?.id || ''
  coreForm.num = c?.num || ''
  coreForm.node_ids = c?.node_ids || []
  coreForm.notes = c?.notes || ''
  coreForm.fields = {}
  for (const f of c?.self_defined_field || []) coreForm.fields[f.id] = f.value || ''
  if (c) coreEditOpen.value = true
}
async function saveCore() {
  try {
    if (coreEditing.value) await api.put(`/admin/wanyun/fiber-cores/${coreEditing.value}`, { ...coreForm, num: coreForm.num.trim() })
    else await api.post(`/admin/wanyun/fibers/${curFiber.value.id}/cores`, {})
    message.success('纤芯已保存')
    coreEditOpen.value = false
    coreEditing.value = ''
    await loadCores()
    await loadFibers()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存纤芯失败')
  }
}
async function addCore() {
  coreEditing.value = ''
  await saveCore()
}

async function removeCore(c: any) {
  try {
    await api.delete(`/admin/wanyun/fiber-cores/${c.id}`)
    message.success('纤芯已删除')
    await loadCores()
    await loadFibers()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

const nodeOptions = computed(() => nodes.value.map(n => ({ label: n.name, value: n.id })))
const nodeName = (id: string) => nodes.value.find(n => n.id === id)?.name || id

onMounted(async () => {
  await loadSegments()
  await loadNodes()
  await loadTypes('node')
  await loadTypes('vlan')
  await loadVlans()
  await loadFibers()
})
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>万云资源管理</h1>
        <p>对齐魔方「万云资源管理」插件：IP 段（含子网与地址明细）、节点（类型 + 自定义字段）、VLAN（类型 + 途径节点）、光纤与纤芯（途径节点 + 自定义字段）的手工资源台账。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary @click="tab === 'ips' ? loadSegments() : tab === 'nodes' ? loadNodes() : tab === 'vlans' ? loadVlans() : loadFibers()">刷新</NButton>
      </div>
    </div>

    <div class="row" style="gap:8px;margin-bottom:12px">
      <NButton :type="tab === 'ips' ? 'primary' : 'default'" @click="tab = 'ips'">IP 段</NButton>
      <NButton :type="tab === 'nodes' ? 'primary' : 'default'" @click="tab = 'nodes'">节点管理</NButton>
      <NButton :type="tab === 'vlans' ? 'primary' : 'default'" @click="tab = 'vlans'">VLAN 管理</NButton>
      <NButton :type="tab === 'fibers' ? 'primary' : 'default'" @click="tab = 'fibers'">光纤 / 纤芯</NButton>
    </div>

    <!-- IP 段 -->
    <section v-if="tab === 'ips'" class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="ipKeyword" clearable placeholder="IP 段 / 名称 / 备注" style="max-width:260px" @keyup.enter="loadSegments" />
        <NButton type="primary" :loading="ipsBusy" @click="loadSegments">查询</NButton>
        <NButton secondary @click="openSegment()">＋ 新增 IP 段</NButton>
      </div>
      <div v-if="segments.length" class="table-scroll"><div class="user-table">
        <div class="user-row wy-seg-row user-head">
          <span>IP 段</span><span>分组</span><span>IP 数</span><span>可用</span><span>已用</span><span>备注</span><span>操作</span>
        </div>
        <template v-for="s in segments" :key="s.id">
          <div class="user-row wy-seg-row">
            <span><b>{{ s.subnet }}</b><small v-if="s.subnet_mask" class="muted"> /{{ s.subnet_mask }}</small><small v-if="s.name" class="muted">（{{ s.name }}）</small></span>
            <span class="muted">{{ s.group_name || '—' }}</span>
            <span>{{ s.ip_num }}</span>
            <span class="muted">{{ s.usable }}</span>
            <span class="muted">{{ s.used }}</span>
            <span class="muted">{{ s.notes || '—' }}</span>
            <span class="row" style="gap:6px">
              <NButton size="tiny" tertiary @click="openAddresses(s)">地址明细</NButton>
              <NButton size="tiny" tertiary @click="openSegment(undefined, s)">＋ 子网</NButton>
              <NButton size="tiny" tertiary @click="openSegment(s)">编辑</NButton>
              <NButton size="tiny" tertiary type="error" @click="removeSegment(s)">删除</NButton>
            </span>
          </div>
          <div v-for="sub in s.ips_sub || []" :key="sub.id" class="user-row wy-seg-row wy-sub">
            <span>{{ sub.subnet }}<small v-if="sub.subnet_mask" class="muted"> /{{ sub.subnet_mask }}</small><small class="muted"> 网关 {{ sub.gateway || '—' }}</small></span>
            <span class="muted">{{ sub.group_name || '—' }}</span>
            <span>{{ sub.ip_num }}</span>
            <span class="muted">{{ sub.usable }}</span>
            <span class="muted">{{ sub.used }}</span>
            <span class="muted">{{ sub.notes || '—' }}</span>
            <span class="row" style="gap:6px">
              <NButton size="tiny" tertiary @click="openAddresses(sub)">地址明细</NButton>
              <NButton size="tiny" tertiary @click="openSegment(sub)">编辑</NButton>
              <NButton size="tiny" tertiary type="error" @click="removeSegment(sub)">删除</NButton>
            </span>
          </div>
        </template>
      </div></div>
      <div v-else class="empty-box">还没有 IP 段，点击「新增 IP 段」创建。</div>
    </section>

    <!-- 节点 -->
    <section v-else-if="tab === 'nodes'" class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="nodeKeyword" clearable placeholder="节点名称 / 类型" style="max-width:240px" @keyup.enter="loadNodes" />
        <NButton type="primary" :loading="nodesBusy" @click="loadNodes">查询</NButton>
        <NButton secondary @click="openNode()">＋ 新增节点</NButton>
        <NButton tertiary @click="openTypes('node')">类型管理</NButton>
        <NButton tertiary @click="openFields('node')">自定义字段</NButton>
      </div>
      <div v-if="nodes.length" class="table-scroll"><div class="user-table">
        <div class="user-row wy-node-row user-head">
          <span>序号</span><span>节点名称</span><span>节点类型</span><span v-for="f in nodeCustomShown(nodes[0])" :key="f.id">{{ f.field_name }}</span><span>操作</span>
        </div>
        <div v-for="(n, i) in nodes" :key="n.id" class="user-row wy-node-row">
          <span class="muted">{{ i + 1 }}</span>
          <span><b>{{ n.name }}</b></span>
          <span class="muted">{{ n.type_name || '—' }}</span>
          <span v-for="f in nodeCustomShown(nodes[0])" :key="f.id" class="muted">{{ (n.self_defined_field || []).find((x: any) => x.id === f.id)?.value || '—' }}</span>
          <span class="row" style="gap:6px">
            <NButton size="tiny" tertiary @click="openNode(n)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeNode(n)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有节点，点击「新增节点」创建。</div>
    </section>

    <!-- VLAN -->
    <section v-else-if="tab === 'vlans'" class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="vlanKeyword" clearable placeholder="名称 / 编号 / 分配人 / 使用人" style="max-width:260px" @keyup.enter="loadVlans" />
        <NSelect v-model:value="vlanStatus" :options="[{ label: '启用', value: '1' }, { label: '停用', value: '0' }]" placeholder="状态" clearable style="width:110px" />
        <NButton type="primary" :loading="vlansBusy" @click="loadVlans">查询</NButton>
        <NButton secondary @click="openVlan()">＋ 新增 VLAN</NButton>
        <NButton tertiary @click="openTypes('vlan')">类型管理</NButton>
      </div>
      <div v-if="vlans.length" class="table-scroll"><div class="user-table">
        <div class="user-row wy-vlan-row user-head">
          <span>编号</span><span>名称</span><span>类型</span><span>分配人</span><span>使用人</span><span>使用单位</span><span>途径节点</span><span>状态</span><span>备注</span><span>操作</span>
        </div>
        <div v-for="v in vlans" :key="v.id" class="user-row wy-vlan-row">
          <span class="muted">{{ v.vlan_id }}</span>
          <span><b>{{ v.name }}</b></span>
          <span class="muted">{{ v.type_name || '—' }}</span>
          <span class="muted">{{ v.assignor || '—' }}</span>
          <span class="muted">{{ v.username || '—' }}</span>
          <span class="muted">{{ v.use_unit || '—' }}</span>
          <span class="muted">{{ (v.node_names || []).join(' / ') || '—' }}</span>
          <span><NSwitch size="small" :value="!!v.active" @update:value="toggleVlan(v)" /></span>
          <span class="muted">{{ v.notes || '—' }}</span>
          <span class="row" style="gap:6px">
            <NButton size="tiny" tertiary @click="openVlan(v)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeVlan(v)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有 VLAN，点击「新增 VLAN」创建。</div>
    </section>

    <!-- 光纤 -->
    <section v-else class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="fiberKeyword" clearable placeholder="光纤编号 / 所属 / 项目" style="max-width:240px" @keyup.enter="loadFibers" />
        <NButton type="primary" :loading="fibersBusy" @click="loadFibers">查询</NButton>
        <NButton secondary @click="openFiber()">＋ 新增光纤</NButton>
      </div>
      <div v-if="fibers.length" class="table-scroll"><div class="user-table">
        <div class="user-row wy-fiber-row user-head">
          <span>光纤编号</span><span>所属</span><span>芯数</span><span>途径节点</span><span>开通 / 施工单位</span><span>联系人</span><span>项目</span><span>价格</span><span>备注</span><span>操作</span>
        </div>
        <div v-for="f in fibers" :key="f.id" class="user-row wy-fiber-row">
          <span><b>{{ f.fiber_num }}</b></span>
          <span class="muted">{{ f.owner || '—' }}</span>
          <span>{{ f.core_num }}</span>
          <span class="muted">{{ (f.node_names || []).join(' / ') || '—' }}</span>
          <span class="muted">{{ f.open_unit || '—' }} / {{ f.construction_unit || '—' }}</span>
          <span class="muted">{{ f.contact || '—' }}</span>
          <span class="muted">{{ f.project || '—' }}</span>
          <span>{{ money(f.price_cents) }}</span>
          <span class="muted">{{ f.notes || '—' }}</span>
          <span class="row" style="gap:6px">
            <NButton size="tiny" tertiary @click="openCores(f)">纤芯管理</NButton>
            <NButton size="tiny" tertiary @click="openFiber(f)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeFiber(f)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有光纤，点击「新增光纤」创建。</div>
    </section>

    <!-- 自定义字段弹窗 -->
    <NModal v-model:show="fieldDialog" preset="card" :title="(fieldScope === 'node' ? '节点' : '纤芯') + '自定义字段'" style="width:min(720px,94vw)">
      <div class="form-grid" style="align-items:end">
        <label><span>字段名称</span><NInput v-model:value="fieldForm.field_name" :maxlength="10" placeholder="字段名称" /></label>
        <label><span>字段类型</span>
          <NSelect v-model:value="fieldForm.field_type" :options="[{ label: '文本框', value: 'text' }, { label: '下拉选择', value: 'dropdown' }]" />
        </label>
        <label v-if="fieldForm.field_type === 'dropdown'"><span>下拉值（英文半角逗号分隔）</span><NInput v-model:value="fieldForm.field_option" placeholder="值1,值2,值3" /></label>
        <label class="row" style="gap:12px;align-items:center;padding-bottom:6px">
          <NSwitch v-model:value="fieldForm.is_required" size="small" /><span style="font-size:13px">必填</span>
          <NSwitch v-model:value="fieldForm.show_list" size="small" /><span style="font-size:13px">信息展示</span>
        </label>
        <NButton type="primary" @click="saveField">{{ fieldEditing ? '保存' : '添加' }}</NButton>
      </div>
      <div v-if="fieldDefs.length" class="table-scroll" style="margin-top:10px"><div class="user-table">
        <div class="user-row wy-field-row user-head">
          <span>序号</span><span>字段名称</span><span>字段类型</span><span>必填</span><span>信息展示</span><span>操作</span>
        </div>
        <div v-for="(f, i) in fieldDefs" :key="f.id" class="user-row wy-field-row">
          <span class="muted">{{ i + 1 }}</span>
          <span><span v-if="f.is_required" style="color:#e88080">*</span>{{ f.field_name }}<small v-if="f.field_type === 'dropdown'" class="muted">（{{ f.field_option }}）</small></span>
          <span class="muted">{{ fieldTypeText(f.field_type) }}</span>
          <span class="muted">{{ f.is_required ? '是' : '否' }}</span>
          <span><NSwitch size="small" :value="!!f.show_list" @update:value="toggleFieldShow(f)" /></span>
          <span class="row" style="gap:4px">
            <NButton size="tiny" tertiary @click="() => { fieldEditing = f.id; fieldForm.field_name = f.field_name; fieldForm.field_type = f.field_type; fieldForm.field_option = f.field_option; fieldForm.is_required = !!f.is_required; fieldForm.show_list = !!f.show_list }">编辑</NButton>
            <NButton size="tiny" tertiary @click="dragField(f, -1)">上移</NButton>
            <NButton size="tiny" tertiary @click="dragField(f, 1)">下移</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeField(f)">删除</NButton>
          </span>
        </div>
      </div></div>
    </NModal>

    <!-- 类型管理弹窗 -->
    <NModal v-model:show="typeDialog" preset="card" :title="(typeScope === 'node' ? '节点' : 'VLAN') + '类型管理'" style="width:min(520px,94vw)">
      <div class="row" style="gap:8px">
        <NInput v-model:value="typeForm.name" :maxlength="10" placeholder="类型名称" style="flex:1" @keyup.enter="saveType" />
        <NButton type="primary" @click="saveType">{{ typeEditing ? '保存' : '添加' }}</NButton>
      </div>
      <div v-if="typeOptions.length" class="table-scroll" style="margin-top:10px"><div class="user-table">
        <div class="user-row wy-type-row user-head"><span>类型</span><span>使用数量</span><span>操作</span></div>
        <div v-for="t in (typeScope === 'node' ? nodeTypes : vlanTypes)" :key="t.id" class="user-row wy-type-row">
          <span>{{ t.name }}</span>
          <span class="muted">{{ t.count }}</span>
          <span class="row" style="gap:4px">
            <NButton size="tiny" tertiary @click="() => { typeEditing = t.id; typeForm.name = t.name }">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeType(t)">删除</NButton>
          </span>
        </div>
      </div></div>
    </NModal>

    <!-- 节点弹窗 -->
    <NModal v-model:show="nodeDialog" preset="card" :title="nodeEditing ? '编辑节点' : '新增节点'" style="width:min(540px,94vw)">
      <div class="form-grid">
        <label class="full"><span>节点名称</span><NInput v-model:value="nodeForm.name" :maxlength="20" placeholder="节点名称" /></label>
        <label class="full"><span>节点类型</span><NSelect v-model:value="nodeForm.type_id" :options="nodeTypes.map(t => ({ label: t.name, value: t.id }))" clearable placeholder="请选择" /></label>
        <template v-for="f in nodeFieldDefs" :key="f.id">
          <label class="full"><span>{{ f.is_required ? '* ' : '' }}{{ f.field_name }}</span>
            <NInput v-if="f.field_type === 'text'" v-model:value="nodeForm.fields[f.id]" :placeholder="f.field_name" />
            <NSelect v-else v-model:value="nodeForm.fields[f.id]" :options="fieldOptions(f).map(o => ({ label: o, value: o }))" clearable :placeholder="f.field_name" />
          </label>
        </template>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" :loading="nodesBusy" @click="saveNode">保存</NButton>
        <NButton secondary @click="nodeDialog = false">取消</NButton>
      </div>
    </NModal>

    <!-- VLAN 弹窗 -->
    <NModal v-model:show="vlanDialog" preset="card" :title="vlanEditing ? '编辑 VLAN' : '新增 VLAN'" style="width:min(600px,94vw)">
      <div class="form-grid">
        <label><span>VLAN 编号</span><NInputNumber v-model:value="vlanForm.vlan_id" :min="1" :precision="0" style="width:100%" /></label>
        <label><span>名称</span><NInput v-model:value="vlanForm.name" placeholder="名称" /></label>
        <label><span>类型</span><NSelect v-model:value="vlanForm.type_id" :options="vlanTypes.map(t => ({ label: t.name, value: t.id }))" clearable placeholder="请选择" /></label>
        <label><span>分配人</span><NInput v-model:value="vlanForm.assignor" placeholder="分配人" /></label>
        <label><span>使用人</span><NInput v-model:value="vlanForm.username" placeholder="使用人" /></label>
        <label><span>使用单位</span><NInput v-model:value="vlanForm.use_unit" placeholder="使用单位" /></label>
        <label class="full"><span>途径节点（可多选）</span><NSelect v-model:value="vlanForm.node_ids" :options="nodeOptions" filterable multiple clearable placeholder="途径节点" /></label>
        <label class="full"><span>备注</span><NInput v-model:value="vlanForm.notes" type="textarea" :rows="2" placeholder="备注" /></label>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" :loading="vlansBusy" @click="saveVlan">保存</NButton>
        <NButton secondary @click="vlanDialog = false">取消</NButton>
      </div>
    </NModal>

    <!-- IP 段弹窗 -->
    <NModal v-model:show="segmentDialog" preset="card" :title="segmentEditing ? '编辑 IP 段' : (segmentParent ? '新增子网' : '新增 IP 段')" style="width:min(560px,94vw)">
      <div class="form-grid">
        <label><span>IP 段 / 子网</span><NInput v-model:value="segmentForm.subnet" placeholder="如 103.152.33.0/24" /></label>
        <label><span>名称</span><NInput v-model:value="segmentForm.name" placeholder="名称" /></label>
        <label><span>子网掩码</span><NInput v-model:value="segmentForm.subnet_mask" placeholder="255.255.255.0" /></label>
        <label><span>网关</span><NInput v-model:value="segmentForm.gateway" placeholder="网关" /></label>
        <label><span>分组</span><NInput v-model:value="segmentForm.group_name" placeholder="分组" /></label>
        <label class="full"><span>备注</span><NInput v-model:value="segmentForm.notes" type="textarea" :rows="2" placeholder="备注" /></label>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" :loading="ipsBusy" @click="saveSegment">保存</NButton>
        <NButton secondary @click="segmentDialog = false">取消</NButton>
      </div>
    </NModal>

    <!-- 地址明细弹窗 -->
    <NModal v-model:show="addressDialog" preset="card" :title="`「${curSegment?.subnet || ''}」地址明细`" style="width:min(760px,94vw)">
      <div class="users-toolbar">
        <NInput v-model:value="addrKeyword" clearable placeholder="IP / 分配人 / 使用人 / 备注" style="max-width:240px" @keyup.enter="loadAddresses" />
        <NButton type="primary" @click="loadAddresses">查询</NButton>
        <NButton secondary @click="openAddr()">＋ 登记地址</NButton>
      </div>
      <div v-if="addresses.length" class="table-scroll" style="margin-top:10px"><div class="user-table">
        <div class="user-row wy-addr-row user-head">
          <span>IP</span><span>分配人</span><span>使用人</span><span>使用单位</span><span>状态</span><span>分配时间</span><span>备注</span><span>操作</span>
        </div>
        <div v-for="a in addresses" :key="a.id" class="user-row wy-addr-row">
          <span><b>{{ a.ip }}</b></span>
          <span class="muted">{{ a.assignor || '—' }}</span>
          <span class="muted">{{ a.username || '—' }}</span>
          <span class="muted">{{ a.use_unit || '—' }}</span>
          <span><NTag :type="a.used ? 'warning' : 'success'" size="tiny" round>{{ a.used ? '已用' : '可用' }}</NTag></span>
          <span class="muted">{{ fmt(a.used_at) }}</span>
          <span class="muted">{{ a.notes || '—' }}</span>
          <span class="row" style="gap:4px">
            <NButton size="tiny" tertiary @click="openAddr(a)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeAddr(a)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">该段还没有地址明细，点击「登记地址」添加。</div>
    </NModal>

    <!-- 地址弹窗 -->
    <NModal v-model:show="addrDialog" preset="card" :title="addrEditing ? '编辑地址' : '登记地址'" style="width:min(520px,94vw)">
      <div class="form-grid">
        <label class="full"><span>IP</span><NInput v-model:value="addrForm.ip" placeholder="IP" /></label>
        <label><span>分配人</span><NInput v-model:value="addrForm.assignor" placeholder="分配人" /></label>
        <label><span>使用人</span><NInput v-model:value="addrForm.username" placeholder="使用人" /></label>
        <label><span>使用单位</span><NInput v-model:value="addrForm.use_unit" placeholder="使用单位" /></label>
        <label class="row" style="gap:10px;align-items:center">
          <NSwitch v-model:value="addrForm.used" size="small" />
          <span style="font-size:13px">{{ addrForm.used ? '已用（保存时补记分配时间）' : '可用' }}</span>
        </label>
        <label class="full"><span>备注</span><NInput v-model:value="addrForm.notes" type="textarea" :rows="2" placeholder="备注" /></label>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" @click="saveAddr">保存</NButton>
        <NButton secondary @click="addrDialog = false">取消</NButton>
      </div>
    </NModal>

    <!-- 光纤弹窗 -->
    <NModal v-model:show="fiberDialog" preset="card" :title="fiberEditing ? '编辑光纤' : '新增光纤'" style="width:min(640px,94vw)">
      <div class="form-grid">
        <label><span>光纤编号</span><NInput v-model:value="fiberForm.fiber_num" placeholder="光纤编号" /></label>
        <label><span>光纤所属</span><NInput v-model:value="fiberForm.owner" placeholder="光纤所属" /></label>
        <label><span>光纤芯数</span><NInputNumber v-model:value="fiberForm.core_num" :min="0" :precision="0" style="width:100%" /></label>
        <label><span>价格（元）</span><NInputNumber v-model:value="fiberForm.price" :min="0" :precision="2" style="width:100%" /></label>
        <label><span>开通单位</span><NInput v-model:value="fiberForm.open_unit" placeholder="开通单位" /></label>
        <label><span>施工单位</span><NInput v-model:value="fiberForm.construction_unit" placeholder="施工单位" /></label>
        <label><span>联系人</span><NInput v-model:value="fiberForm.contact" placeholder="联系人" /></label>
        <label><span>项目</span><NInput v-model:value="fiberForm.project" placeholder="项目" /></label>
        <label class="full"><span>途径节点（可多选）</span><NSelect v-model:value="fiberForm.node_ids" :options="nodeOptions" filterable multiple clearable placeholder="途径节点" /></label>
        <label class="full"><span>备注</span><NInput v-model:value="fiberForm.notes" type="textarea" :rows="2" placeholder="备注" /></label>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" :loading="fibersBusy" @click="saveFiber">保存</NButton>
        <NButton secondary @click="fiberDialog = false">取消</NButton>
      </div>
    </NModal>

    <!-- 纤芯管理弹窗 -->
    <NModal v-model:show="coreDialog" preset="card" :title="`「${curFiber?.fiber_num || ''}」纤芯管理`" style="width:min(760px,94vw)">
      <div class="users-toolbar">
        <span class="muted" style="align-self:center">共 {{ (curFiber?.cores || []).length }} 芯</span>
        <NButton secondary @click="addCore">＋ 补一芯</NButton>
        <NButton tertiary @click="openFields('fiber_core')">自定义字段</NButton>
      </div>
      <div v-if="(curFiber?.cores || []).length" class="table-scroll" style="margin-top:10px"><div class="user-table">
        <div class="user-row wy-core-row user-head">
          <span>纤芯编号</span><span>途径节点</span><span v-for="f in coreFieldDefs.filter(x => x.show_list)" :key="f.id">{{ f.field_name }}</span><span>备注</span><span>操作</span>
        </div>
        <div v-for="c in curFiber?.cores || []" :key="c.id" class="user-row wy-core-row">
          <span><b>{{ c.num }}</b></span>
          <span class="muted">{{ (c.node_names || []).join(' / ') || '—' }}</span>
          <span v-for="f in coreFieldDefs.filter(x => x.show_list)" :key="f.id" class="muted">{{ (c.self_defined_field || []).find((x: any) => x.id === f.id)?.value || '—' }}</span>
          <span class="muted">{{ c.notes || '—' }}</span>
          <span class="row" style="gap:4px">
            <NButton size="tiny" tertiary @click="openCore(c)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeCore(c)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有纤芯，点击「补一芯」添加。</div>
    </NModal>

    <!-- 纤芯编辑弹窗（嵌在纤芯管理内） -->
    <NModal v-model:show="coreEditOpen" preset="card" title="编辑纤芯" style="width:min(540px,94vw)">
      <div class="form-grid">
        <label><span>纤芯编号</span><NInput v-model:value="coreForm.num" placeholder="纤芯编号" /></label>
        <label class="full"><span>途径节点（可多选）</span><NSelect v-model:value="coreForm.node_ids" :options="nodeOptions" filterable multiple clearable placeholder="途径节点" /></label>
        <template v-for="f in coreFieldDefs" :key="f.id">
          <label class="full"><span>{{ f.is_required ? '* ' : '' }}{{ f.field_name }}</span>
            <NInput v-if="f.field_type === 'text'" v-model:value="coreForm.fields[f.id]" :placeholder="f.field_name" />
            <NSelect v-else v-model:value="coreForm.fields[f.id]" :options="fieldOptions(f).map(o => ({ label: o, value: o }))" clearable :placeholder="f.field_name" />
          </label>
        </template>
        <label class="full"><span>备注</span><NInput v-model:value="coreForm.notes" type="textarea" :rows="2" placeholder="备注" /></label>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" @click="saveCore">保存</NButton>
        <NButton secondary @click="coreEditOpen = false">取消</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.wy-seg-row { grid-template-columns: minmax(170px, 1.2fr) 90px 60px 60px 60px minmax(120px, .8fr) 230px; }
.wy-sub { padding-left: 20px; opacity: .92; }
.wy-node-row { grid-template-columns: 50px minmax(130px, 1fr) 110px repeat(auto-fit, minmax(70px, .6fr)) 120px; }
.wy-vlan-row { grid-template-columns: 70px minmax(110px, .9fr) 90px 90px 90px minmax(100px, .8fr) minmax(140px, 1fr) 70px minmax(100px, .7fr) 110px; }
.wy-fiber-row { grid-template-columns: minmax(110px, .9fr) 90px 55px minmax(130px, 1fr) minmax(140px, 1fr) 90px 90px 90px minmax(90px, .6fr) 180px; }
.wy-field-row { grid-template-columns: 50px minmax(140px, 1.2fr) 90px 60px 90px 230px; }
.wy-type-row { grid-template-columns: minmax(120px, 1fr) 90px 130px; }
.wy-addr-row { grid-template-columns: minmax(120px, .9fr) 90px 90px minmax(100px, .8fr) 70px 150px minmax(110px, .8fr) 110px; }
</style>
