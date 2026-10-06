<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NModal, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 电子合同（对齐魔方 CBAP EContract 插件）。
// 三个页签：合同列表（审核 / 邮寄 / 下载）、模板管理（变量 / 关联商品 / 复制）、
// 基础设置（功能开关 / 申请时间限制 / 我方信息 / 编号前缀 / 印章）。

const message = useMessage()
const tab = ref<'contracts' | 'templates' | 'settings'>('contracts')
const fmt = (v?: string | null) => (v ? new Date(v).toLocaleString() : '—')

// ---- 合同 ----
const contracts = ref<any[]>([])
const contractKeyword = ref('')
const contractStatus = ref<string | null>(null)
const busy = ref(false)
const statusText: Record<string, string> = { pending: '待签订', signed: '待审核', effective: '已生效', reject: '已驳回', cancel: '已作废' }
const statusType = (s: string) => ({ pending: 'warning', signed: 'info', effective: 'success', reject: 'error', cancel: 'default' } as any)[s] || 'default'

async function loadContracts() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/e-contracts', { params: { keyword: contractKeyword.value.trim(), status: contractStatus.value || '', limit: 100 } }))
    contracts.value = d?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取合同失败')
  } finally {
    busy.value = false
  }
}
const detail = ref<any | null>(null)
// 弹窗开关必须是可赋值的成员表达式（v-model 约束，生产模板编译器强制），
// 所以用可写 computed 包一层而不是在模板里写 v-model:show="!!detail"。
const showDetail = computed({ get: () => !!detail.value, set: (v: boolean) => { if (!v) detail.value = null } })
async function openDetail(r: any) {
  try {
    detail.value = dataOf<any>(await api.get(`/admin/e-contracts/${r.id}`))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取合同详情失败')
  }
}
async function review(r: any, action: 'complete' | 'reject' | 'cancel') {
  let reason = ''
  if (action !== 'complete') {
    reason = window.prompt(action === 'reject' ? '请填写驳回理由' : '请填写作废原因') || ''
    if (!reason.trim()) return
  }
  try {
    await api.post(`/admin/e-contracts/${r.id}/review`, { action, reason: reason.trim() })
    message.success('已处理')
    detail.value = null
    await loadContracts()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}
const mailFor = ref('')
const showMail = computed({ get: () => !!mailFor.value, set: (v: boolean) => { if (!v) mailFor.value = '' } })
const mailForm = reactive({ courier_company: '', courier_number: '' })
function openMail(r: any) {
  mailFor.value = r.id
  mailForm.courier_company = r.courier_company || ''
  mailForm.courier_number = r.courier_number || ''
}
async function saveMail() {
  try {
    await api.post(`/admin/e-contracts/${mailFor.value}/mail`, mailForm)
    message.success('邮寄登记已保存')
    mailFor.value = ''
    await loadContracts()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}
async function download(r: any) {
  try {
    const resp = await api.get(`/admin/e-contracts/${r.id}/download`, { responseType: 'blob' })
    const url = URL.createObjectURL(resp.data)
    const link = document.createElement('a')
    link.href = url
    link.download = `contract-${r.number}.html`
    link.click()
    URL.revokeObjectURL(url)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '下载失败')
  }
}

// ---- 模板 ----
const templates = ref<any[]>([])
const vars = ref<any[]>([])
const products = ref<any[]>([])
const templateDialog = ref(false)
const templateEditing = ref('')
const templateForm = reactive({ name: '', detail: '', product_ids: [] as string[], base_contract: false, force_sign: false, notes: '', active: true })
const productOptions = computed(() => products.value.map(p => ({ label: p.name, value: p.id })))

async function loadTemplates() {
  try {
    const d = dataOf<any>(await api.get('/admin/e-contract/templates'))
    templates.value = d?.list || []
    vars.value = d?.vars || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取模板失败')
  }
}
async function loadProducts() {
  try { products.value = dataOf<any[]>(await api.get('/admin/products')) || [] } catch { products.value = [] }
}
function openTemplate(t?: any) {
  templateEditing.value = t?.id || ''
  templateForm.name = t?.name || ''
  templateForm.detail = t?.detail || ''
  templateForm.product_ids = t?.product_ids || []
  templateForm.base_contract = !!t?.base_contract
  templateForm.force_sign = !!t?.force_sign
  templateForm.notes = t?.notes || ''
  templateForm.active = t ? !!t.active : true
  templateDialog.value = true
}
function insertVar(k: string) {
  templateForm.detail += `{{${k}}}`
}
// 变量标签交给脚本拼接：模板插值里不能出现字面量 }}（会被当成插值结束符，
// 生产构建的模板编译器会把它截断成未终止的表达式）。
function varTag(k: string) {
  return `{{${k}}}`
}
async function saveTemplate() {
  if (!templateForm.name.trim()) { message.error('请填写模板名称'); return }
  try {
    if (templateEditing.value) await api.put(`/admin/e-contract/templates/${templateEditing.value}`, templateForm)
    else await api.post('/admin/e-contract/templates', templateForm)
    message.success('模板已保存')
    templateDialog.value = false
    await loadTemplates()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}
async function copyTemplate(t: any) {
  try {
    await api.post(`/admin/e-contract/templates/${t.id}/copy`, {})
    message.success('已复制（副本默认停用）')
    await loadTemplates()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '复制失败')
  }
}
async function removeTemplate(t: any) {
  try {
    await api.delete(`/admin/e-contract/templates/${t.id}`)
    message.success('模板已删除')
    await loadTemplates()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '删除失败')
  }
}

// ---- 设置 ----
const cfg = reactive<any>({ switch: false, day_limit: 0, my_unit: '', social_credit_code: '', contact: '', contact_phone: '', contact_email: '', contact_address: '', postcode: '', contract_number_prefix: 'HT-', contract_number_next: 1, logo: '', company_chop: '' })
async function loadSettings() {
  try {
    const d = dataOf<any>(await api.get('/admin/e-contract/settings'))
    Object.assign(cfg, d?.config || {})
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取设置失败')
  }
}
async function saveSettings() {
  try {
    await api.put('/admin/e-contract/settings', cfg)
    message.success('设置已保存')
    await loadSettings()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  }
}

onMounted(() => { loadContracts(); loadTemplates(); loadProducts(); loadSettings() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>电子合同</h1>
        <p>对齐魔方「电子合同」插件：合同模板（变量 / 关联商品）、用户对已支付订单申请并签订、后台审核与邮寄登记、可打印合同文件下载。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary :loading="busy" @click="tab === 'contracts' ? loadContracts() : tab === 'templates' ? loadTemplates() : loadSettings()">刷新</NButton>
      </div>
    </div>

    <div class="row" style="gap:8px;margin-bottom:12px">
      <NButton :type="tab === 'contracts' ? 'primary' : 'default'" @click="tab = 'contracts'">合同列表</NButton>
      <NButton :type="tab === 'templates' ? 'primary' : 'default'" @click="tab = 'templates'">模板管理</NButton>
      <NButton :type="tab === 'settings' ? 'primary' : 'default'" @click="tab = 'settings'">基础设置</NButton>
    </div>

    <!-- 合同列表 -->
    <section v-if="tab === 'contracts'" class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="contractKeyword" clearable placeholder="编号 / 客户 / 模板" style="max-width:220px" @keyup.enter="loadContracts" />
        <NSelect v-model:value="contractStatus" :options="Object.entries(statusText).map(([v, l]) => ({ label: l, value: v }))" placeholder="状态" clearable style="width:130px" />
        <NButton type="primary" :loading="busy" @click="loadContracts">查询</NButton>
      </div>
      <div v-if="contracts.length" class="table-scroll"><div class="user-table">
        <div class="user-row ec-row user-head">
          <span>合同编号</span><span>模板</span><span>客户</span><span>订单</span><span>状态</span><span>申请时间</span><span>邮寄</span><span>操作</span>
        </div>
        <div v-for="r in contracts" :key="r.id" class="user-row ec-row">
          <span><b>{{ r.number }}</b></span>
          <span class="muted">{{ r.template_name }}</span>
          <span class="muted">{{ r.user_email }}</span>
          <span class="muted">{{ (r.order_id || '—').slice(0, 8) }}</span>
          <span><NTag :type="statusType(r.status)" size="tiny" round>{{ statusText[r.status] || r.status }}</NTag></span>
          <span class="muted">{{ fmt(r.created_at) }}</span>
          <span class="muted">{{ r.courier_company ? `${r.courier_company} ${r.courier_number}` : '—' }}</span>
          <span class="row" style="gap:4px;flex-wrap:wrap">
            <NButton size="tiny" tertiary @click="openDetail(r)">详情</NButton>
            <NButton v-if="r.status === 'signed'" size="tiny" tertiary type="success" @click="review(r, 'complete')">通过</NButton>
            <NButton v-if="r.status === 'signed'" size="tiny" tertiary type="warning" @click="review(r, 'reject')">驳回</NButton>
            <NButton v-if="r.status === 'pending' || r.status === 'signed'" size="tiny" tertiary type="error" @click="review(r, 'cancel')">作废</NButton>
            <NButton v-if="r.status === 'effective'" size="tiny" tertiary @click="openMail(r)">邮寄</NButton>
            <NButton v-if="r.status !== 'cancel' && r.status !== 'reject'" size="tiny" tertiary @click="download(r)">下载</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有合同。</div>
    </section>

    <!-- 模板管理 -->
    <section v-else-if="tab === 'templates'" class="panel">
      <div class="users-toolbar">
        <NButton type="primary" @click="openTemplate()">＋ 新建模板</NButton>
        <span class="muted" style="align-self:center">共 {{ templates.length }} 个模板</span>
      </div>
      <div v-if="templates.length" class="table-scroll"><div class="user-table">
        <div class="user-row ec-tpl-row user-head">
          <span>名称</span><span>关联商品</span><span>基础合同</span><span>状态</span><span>备注</span><span>操作</span>
        </div>
        <div v-for="t in templates" :key="t.id" class="user-row ec-tpl-row">
          <span><b>{{ t.name }}</b></span>
          <span class="muted">{{ (t.product_names || []).join(' / ') || '—' }}</span>
          <span class="muted">{{ t.base_contract ? '是' : '否' }}</span>
          <span><NTag :type="t.active ? 'success' : 'default'" size="tiny" round>{{ t.active ? '启用' : '停用' }}</NTag></span>
          <span class="muted">{{ t.notes || '—' }}</span>
          <span class="row" style="gap:4px">
            <NButton size="tiny" tertiary @click="openTemplate(t)">编辑</NButton>
            <NButton size="tiny" tertiary @click="copyTemplate(t)">复制</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeTemplate(t)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有模板。先建一个「基础合同」模板，用户申请时无商品匹配即回退到它。</div>
    </section>

    <!-- 基础设置 -->
    <section v-else class="panel stack">
      <div class="panel-title-row"><div><h2>基础设置</h2><span>我方信息与编号规则会渲染进合同内容</span></div></div>
      <div class="form-grid">
        <label class="row" style="gap:10px;align-items:center"><NSwitch v-model:value="cfg.switch" /><span style="font-size:13px">功能开关（关闭后用户无法签订新合同）</span></label>
        <label><span>合同申请时间限制（天，0 = 不限）</span><NInput v-model:value="cfg.day_limit" placeholder="0" /></label>
        <label><span>我方单位名</span><NInput v-model:value="cfg.my_unit" placeholder="我方单位名" /></label>
        <label><span>社会信用代码</span><NInput v-model:value="cfg.social_credit_code" placeholder="社会信用代码" /></label>
        <label><span>联系人</span><NInput v-model:value="cfg.contact" placeholder="联系人" /></label>
        <label><span>联系电话</span><NInput v-model:value="cfg.contact_phone" placeholder="联系电话" /></label>
        <label><span>联系邮箱</span><NInput v-model:value="cfg.contact_email" placeholder="联系邮箱" /></label>
        <label><span>联系地址</span><NInput v-model:value="cfg.contact_address" placeholder="联系地址" /></label>
        <label><span>邮政编码</span><NInput v-model:value="cfg.postcode" placeholder="邮政编码" /></label>
        <label><span>合同编号前缀</span><NInput v-model:value="cfg.contract_number_prefix" placeholder="HT-" /></label>
        <label><span>合同起始编号</span><NInput v-model:value="cfg.contract_number_next" placeholder="1" /></label>
        <label class="full"><span>合同 Logo（图片 URL 或 data URL）</span><NInput v-model:value="cfg.logo" placeholder="https://…" /></label>
        <label class="full"><span>公司印章（图片 URL 或 data URL，渲染在签署区）</span><NInput v-model:value="cfg.company_chop" placeholder="https://…" /></label>
      </div>
      <div class="row" style="gap:8px">
        <NButton type="primary" @click="saveSettings">保存</NButton>
      </div>
    </section>

    <!-- 合同详情 -->
    <NModal v-model:show="showDetail" preset="card" :title="`合同 ${detail?.number || ''}`" style="width:min(760px,94vw)">
      <div class="muted" style="font-size:12px">客户：{{ detail?.user_email }} ｜ 订单：{{ detail?.order_id }} ｜ 状态：{{ statusText[detail?.status] || '' }}<template v-if="detail?.reason"> ｜ 理由：{{ detail.reason }}</template></div>
      <div class="ec-content" v-html="detail?.content"></div>
      <div v-if="detail?.has_sign" style="margin-top:12px"><span class="muted" style="font-size:12px">客户签名：</span><img :src="detail?.sign_image" style="height:56px" alt="签名" /></div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton v-if="detail?.status === 'signed'" type="primary" size="small" @click="review(detail, 'complete')">通过</NButton>
        <NButton v-if="detail?.status === 'signed'" type="warning" size="small" @click="review(detail, 'reject')">驳回</NButton>
        <NButton v-if="detail?.status === 'pending' || detail?.status === 'signed'" type="error" size="small" @click="review(detail, 'cancel')">作废</NButton>
        <NButton secondary size="small" @click="detail = null">关闭</NButton>
      </div>
    </NModal>

    <!-- 模板表单 -->
    <NModal v-model:show="templateDialog" preset="card" :title="templateEditing ? '编辑模板' : '新建模板'" style="width:min(760px,94vw)">
      <div class="form-grid">
        <label><span>名称</span><NInput v-model:value="templateForm.name" placeholder="模板名称" /></label>
        <label class="row" style="gap:12px;align-items:center;padding-bottom:4px">
          <NSwitch v-model:value="templateForm.base_contract" size="small" /><span style="font-size:13px">基础合同</span>
          <NSwitch v-model:value="templateForm.force_sign" size="small" /><span style="font-size:13px">强制签订</span>
          <NSwitch v-model:value="templateForm.active" size="small" /><span style="font-size:13px">启用</span>
        </label>
        <label class="full"><span>关联商品（限定可申请该模板的商品，基础合同可留空）</span>
          <NSelect v-model:value="templateForm.product_ids" :options="productOptions" filterable multiple clearable placeholder="选择商品" />
        </label>
        <label class="full"><span>内容（支持变量，点击下方变量名插入）</span>
          <NInput v-model:value="templateForm.detail" type="textarea" :rows="10" placeholder="甲方（我方）：{{my_unit}}&#10;乙方（客户）：{{client_name}}&#10;订单编号：{{order_id}} 金额：{{order_amount}}&#10;……" />
        </label>
      </div>
      <div class="row" style="gap:6px;flex-wrap:wrap;margin-top:8px">
        <NButton v-for="v in vars" :key="v.key" size="tiny" tertiary @click="insertVar(v.key)">{{ v.label }}（{{ varTag(v.key) }}）</NButton>
      </div>
      <label class="full" style="margin-top:8px"><span>备注</span><NInput v-model:value="templateForm.notes" placeholder="备注" /></label>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" @click="saveTemplate">保存</NButton>
        <NButton secondary @click="templateDialog = false">取消</NButton>
      </div>
    </NModal>

    <!-- 邮寄登记 -->
    <NModal v-model:show="showMail" preset="card" title="邮寄登记" style="width:min(440px,94vw)">
      <div class="form-grid">
        <label class="full"><span>快递公司</span><NInput v-model:value="mailForm.courier_company" placeholder="快递公司" /></label>
        <label class="full"><span>快递单号</span><NInput v-model:value="mailForm.courier_number" placeholder="快递单号" /></label>
      </div>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" @click="saveMail">保存</NButton>
        <NButton secondary @click="mailFor = ''">取消</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.ec-row { grid-template-columns: minmax(100px, .8fr) minmax(110px, .8fr) minmax(140px, 1fr) 90px 80px 150px minmax(130px, .9fr) 210px; }
.ec-tpl-row { grid-template-columns: minmax(140px, 1fr) minmax(180px, 1.2fr) 80px 70px minmax(120px, .8fr) 170px; }
.ec-content { border: 1px solid var(--border, #e5e5e5); border-radius: 8px; padding: 16px; max-height: 420px; overflow: auto; white-space: pre-wrap; line-height: 1.9; margin-top: 10px; }
</style>
