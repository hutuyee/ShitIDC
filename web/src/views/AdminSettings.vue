<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NInput, NInputNumber, NSelect, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const testTo = ref('')
const form = ref({
  smtp_host: '', smtp_port: 465, smtp_username: '', smtp_password: '',
  smtp_from: '', from_name: '', smtp_encryption: 'ssl', verify_required: false,
})
const hasPassword = ref(false)
const smtpEnabled = ref(false)

// ---- branding (多品牌) ----
const branding = reactive({ site_name: '', logo_url: '', primary_color: '' })
const savingBranding = ref(false)

// ---- storefront (主题 / 基础货币 / 下单风控) ----
const storefront = reactive({ active_theme: '', base_currency: 'CNY', max_orders_per_hour: 0 })
const savingStorefront = ref(false)

// ---- referral settings (推广返佣) ----
const referral = reactive({ enabled: false, percent: 5 })
const savingReferral = ref(false)

// ---- mail templates (邮件模板) ----
const templates = ref<any[]>([])
const tplForm = reactive({ name: 'email_verification', subject: '', body: '' })
const tplNames = [
  { label: '邮箱验证码', value: 'email_verification' },
  { label: '密码重置', value: 'password_reset' },
  { label: '登录提醒', value: 'login_notify' },
  { label: '工单通知（用户）', value: 'ticket_user' },
  { label: '工单通知（客服）', value: 'ticket_staff' },
]

async function load() {
  loading.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/settings/mail'))
    form.value = {
      smtp_host: d.smtp_host || '', smtp_port: d.smtp_port || 465, smtp_username: d.smtp_username || '',
      smtp_password: '', smtp_from: d.smtp_from || '', from_name: d.from_name || '',
      smtp_encryption: d.smtp_encryption || 'ssl', verify_required: Boolean(d.verify_required),
    }
    hasPassword.value = Boolean(d.has_password)
    smtpEnabled.value = Boolean(d.smtp_enabled)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取邮件设置失败')
  } finally {
    loading.value = false
  }
  try {
    Object.assign(branding, dataOf<any>(await api.get('/admin/settings/branding')))
  } catch { /* defaults */ }
  try {
    Object.assign(storefront, dataOf<any>(await api.get('/admin/settings/storefront')))
  } catch { /* defaults */ }
  try {
    Object.assign(referral, dataOf<any>(await api.get('/admin/settings/referral')))
  } catch { /* defaults */ }
  try {
    templates.value = dataOf<any[]>(await api.get('/admin/mail-templates'))
  } catch { templates.value = [] }
}

async function save() {
  saving.value = true
  try {
    await api.put('/admin/settings/mail', form.value)
    message.success('邮件设置已保存')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

async function sendTest() {
  testing.value = true
  try {
    await api.post('/admin/settings/mail/test', { to: testTo.value.trim() })
    message.success(`测试邮件已发送到 ${testTo.value.trim() || '你的账号邮箱'}`)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '发送失败')
  } finally {
    testing.value = false
  }
}

async function saveBranding() {
  savingBranding.value = true
  try {
    await api.put('/admin/settings/branding', branding)
    message.success('品牌设置已保存，刷新页面生效')
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { savingBranding.value = false }
}

async function saveStorefront() {
  savingStorefront.value = true
  try {
    await api.put('/admin/settings/storefront', storefront)
    message.success('商店设置已保存')
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { savingStorefront.value = false }
}

async function saveReferral() {
  savingReferral.value = true
  try {
    await api.put('/admin/settings/referral', referral)
    message.success('推广设置已保存')
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { savingReferral.value = false }
}

async function loadTemplate(name: string) {
  const hit = templates.value.find(t => t.name === name)
  if (hit) { tplForm.subject = hit.subject; tplForm.body = hit.body }
  else { tplForm.subject = ''; tplForm.body = '' }
}

async function saveTemplate() {
  try {
    await api.post('/admin/mail-templates', { ...tplForm })
    message.success('模板已保存（留空模板将回退到内置文案）')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
}

async function removeTemplate() {
  try {
    await api.delete(`/admin/mail-templates/${tplForm.name}`)
    message.success('模板已删除，恢复内置文案')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">系统设置</div><h1>站点 / 邮件 / 运营设置</h1><p>SMTP、品牌（多品牌）、商店（主题 / 基础货币 / 下单风控）、推广返佣与邮件模板。</p></div></div>

    <div class="admin-two-col">
      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>SMTP 服务器</h2><span><span v-if="smtpEnabled" class="state-pill online">● 已配置</span><span v-else class="state-pill">● 未配置</span></span></div></div>
        <div class="form-grid">
          <label><span>SMTP 服务器</span><NInput v-model:value="form.smtp_host" placeholder="smtp.example.com" /></label>
          <label><span>端口</span><NInputNumber v-model:value="form.smtp_port" :min="1" :max="65535" style="width:100%" /></label>
          <label><span>加密方式</span><NSelect v-model:value="form.smtp_encryption" :options="[{label:'SSL（465 端口常用）',value:'ssl'},{label:'STARTTLS（587 端口常用）',value:'starttls'},{label:'不加密',value:'none'}]" /></label>
          <label><span>发件人名称（可选）</span><NInput v-model:value="form.from_name" placeholder="ShitIDC" /></label>
          <label><span>登录账号</span><NInput v-model:value="form.smtp_username" placeholder="通常为发件邮箱" /></label>
          <label><span>登录密码 / 授权码</span><NInput v-model:value="form.smtp_password" type="password" show-password-on="click" :placeholder="hasPassword ? '已保存，留空表示不修改' : 'SMTP 授权码'" /></label>
          <label class="full"><span>发件人地址</span><NInput v-model:value="form.smtp_from" placeholder="no-reply@example.com" /></label>
        </div>
        <div class="advanced-row"><div><b>强制邮箱验证</b><small>开启后注册需要邮箱验证码，未验证邮箱无法登录</small></div><NCheckbox v-model:checked="form.verify_required" /></div>
        <div class="security-note">密码使用服务器 MASTER_KEY_BASE64 加密后入库；QQ / 163 等邮箱请填写 SMTP 授权码而不是登录密码。</div>
        <div class="form-actions"><NButton type="primary" size="large" :loading="saving" @click="save">保存设置</NButton></div>
        <div class="panel-title-row" style="margin-top:16px"><div><h2>发送测试邮件</h2></div></div>
        <NInput v-model:value="testTo" placeholder="收件邮箱（留空发给自己）" />
        <NButton type="primary" secondary :loading="testing" @click="sendTest">发送测试邮件</NButton>
      </section>

      <section class="panel stack admin-form-panel">
        <div class="panel-title-row"><div><h2>品牌（多品牌）</h2><span>站点名 / Logo / 主题色</span></div></div>
        <div class="form-grid">
          <label><span>站点名称</span><NInput v-model:value="branding.site_name" placeholder="ShitIDC" /></label>
          <label><span>Logo 地址</span><NInput v-model:value="branding.logo_url" placeholder="/logo.png" /></label>
          <label><span>主题色</span><NInput v-model:value="branding.primary_color" placeholder="#4f46e5" /></label>
        </div>
        <NButton secondary :loading="savingBranding" @click="saveBranding">保存品牌设置</NButton>

        <div class="panel-title-row" style="margin-top:14px"><div><h2>商店设置</h2><span>激活主题 / 基础货币 / 下单风控</span></div></div>
        <div class="form-grid">
          <label><span>激活主题（ID）</span><NInput v-model:value="storefront.active_theme" placeholder="default" /></label>
          <label><span>基础货币</span><NInput v-model:value="storefront.base_currency" placeholder="CNY" /></label>
          <label><span>每小时下单上限（0 = 默认 10）</span><NInputNumber v-model:value="storefront.max_orders_per_hour" :min="0" style="width:100%" /></label>
        </div>
        <NButton secondary :loading="savingStorefront" @click="saveStorefront">保存商店设置</NButton>

        <div class="panel-title-row" style="margin-top:14px"><div><h2>推广返佣</h2><span>订单支付后自动给邀请人返佣</span></div></div>
        <div class="advanced-row"><div><b>启用推广返佣</b><small>关闭后不产生新返佣，已发放不受影响</small></div><NCheckbox v-model:checked="referral.enabled" /></div>
        <label><span>返佣比例 %（0-50）</span><NInputNumber v-model:value="referral.percent" :min="0" :max="50" style="width:100%" /></label>
        <NButton secondary :loading="savingReferral" @click="saveReferral">保存推广设置</NButton>
      </section>
    </div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>邮件模板</h2><span>支持占位符：code、email、ip、site、ticket_id（双大括号包裹）</span></div></div>
      <div class="admin-two-col">
        <div class="form-grid">
          <label><span>模板</span>
            <select v-model="tplForm.name" class="native-select" @change="loadTemplate(tplForm.name)">
              <option v-for="n in tplNames" :key="n.value" :value="n.value">{{ n.label }}</option>
            </select>
          </label>
        </div>
        <div class="stack">
          <NInput v-model:value="tplForm.subject" placeholder="邮件主题（留空用内置主题）" />
          <NInput v-model:value="tplForm.body" type="textarea" :rows="6" placeholder="邮件 HTML 正文，留空用内置文案。可用占位符 code、email 等（双大括号包裹）" />
          <div class="row" style="gap:8px">
            <NButton type="primary" @click="saveTemplate">保存模板</NButton>
            <NButton secondary @click="removeTemplate">删除模板（回退内置）</NButton>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.native-select { height: 34px; border-radius: 6px; border: 1px solid var(--border, #d5d9e4); background: var(--panel, #fff); color: var(--text, #1c2333); padding: 0 8px; width: 100%; }
</style>
