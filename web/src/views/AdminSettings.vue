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

// ---- 实名核验通道（对应魔方 public/plugins/certification/）----
const CERT_FIELD_SPECS: Record<string, FieldSpec[]> = {
  manual: [],
  alitwo: [
    { key: 'endpoint', label: '核验端点（默认阿里云云市场二要素）', optional: true },
    { key: 'app_code', label: 'AppCode（云市场授权码）', secret: true },
  ],
}
const certProviders = ref<any[]>([])
const certAvailable = ref<string[]>([])
const certForm = reactive({ name: '', provider: 'alitwo' } as { name: string; provider: string; values: Record<string, string> })
certForm.values = {}
const certSaving = ref(false)
const certChannelLabels: Record<string, string> = {
  manual: '人工审核', alitwo: '阿里云身份证二要素',
}
const certFieldSpecs = () => CERT_FIELD_SPECS[certForm.provider] || []
function pickCertProvider(p: string) {
  certForm.provider = p
  certForm.values = {}
}
async function loadCertProviders() {
  try {
    const d = dataOf<any>(await api.get('/admin/certification-providers'))
    certProviders.value = d.providers || []
    certAvailable.value = d.available || []
  } catch { /* ignore */ }
}
async function saveCertProvider() {
  if (!certForm.name.trim()) { message.warning('请填写通道名称'); return }
  const config: Record<string, string> = {}
  const secret: Record<string, string> = {}
  for (const spec of certFieldSpecs()) {
    const v = (certForm.values[spec.key] || '').trim()
    if (!v) continue
    if (spec.secret) secret[spec.key] = v
    else config[spec.key] = v
  }
  certSaving.value = true
  try {
    await api.post('/admin/certification-providers', { name: certForm.name.trim(), provider: certForm.provider, config, secret, is_default: certProviders.value.length === 0 })
    message.success('实名核验通道已保存')
    certForm.name = ''
    certForm.values = {}
    await loadCertProviders()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { certSaving.value = false }
}
async function setDefaultCert(id: string) {
  try { await api.post(`/admin/certification-providers/${id}/default`); await loadCertProviders() } catch (e: any) { message.error(e?.response?.data?.error?.message || '设置失败') }
}
async function deleteCert(id: string) {
  try { await api.delete(`/admin/certification-providers/${id}`); await loadCertProviders() } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---- 短信通道（对应魔方 public/plugins/sms/）----
interface FieldSpec { key: string; label: string; secret?: boolean; optional?: boolean; area?: boolean }
// 每个通道的表单字段由前端元数据描述：secret=true 的进凭据（加密存储），
// 其余进普通配置。与 internal/{sms,oauth} 各通道 Validate 的必填项一一对应。
const SMS_FIELD_SPECS: Record<string, FieldSpec[]> = {
  aliyun: [
    { key: 'sign_name', label: '短信签名' }, { key: 'template_code', label: '模板 ID' },
    { key: 'region_id', label: '地域（默认 cn-hangzhou）', optional: true },
    { key: 'template_param', label: '模板参数 JSON（{{code}} 占位）', optional: true, area: true },
    { key: 'access_key_id', label: 'AccessKey ID', secret: true },
    { key: 'access_key_secret', label: 'AccessKey Secret', secret: true },
  ],
  qcloudsms: [
    { key: 'sms_sdk_app_id', label: '应用 SmsSdkAppId' }, { key: 'sign_name', label: '短信签名' },
    { key: 'template_id', label: '模板 ID' }, { key: 'region', label: '地域（默认 ap-guangzhou）', optional: true },
    { key: 'secret_id', label: 'SecretId', secret: true }, { key: 'secret_key', label: 'SecretKey', secret: true },
  ],
  submail: [
    { key: 'app_id', label: '赛邮应用 ID' }, { key: 'app_sign', label: '短信签名（中文括号）' },
    { key: 'content_template', label: '短信文案模板（{code}/{ttl} 占位，留空用默认）', optional: true, area: true },
    { key: 'international_app_id', label: '国际短信应用 ID', optional: true },
    { key: 'international_app_sign', label: '国际短信签名', optional: true },
    { key: 'app_key', label: '赛邮应用秘钥', secret: true },
    { key: 'international_app_key', label: '国际短信应用秘钥', secret: true, optional: true },
  ],
  huaweicloud: [
    { key: 'sender', label: '国内签名通道号' }, { key: 'sign_name', label: '国内短信签名' },
    { key: 'template_id', label: '国内模板 ID' },
    { key: 'global_sender', label: '国际/港澳台通道号', optional: true },
    { key: 'global_template_id', label: '国际/港澳台模板 ID', optional: true },
    { key: 'app_key', label: 'APP_Key', secret: true }, { key: 'app_secret', label: 'APP_Secret', secret: true },
    { key: 'global_app_key', label: '国际 APP_Key', secret: true, optional: true },
    { key: 'global_app_secret', label: '国际 APP_Secret', secret: true, optional: true },
  ],
  smsbao: [
    { key: 'sign', label: '短信签名' },
    { key: 'content_template', label: '短信文案模板（{code}/{ttl} 占位，留空用默认）', optional: true, area: true },
    { key: 'user', label: '平台账号', secret: true }, { key: 'pass', label: '平台密码', secret: true },
  ],
  generic: [
    { key: 'endpoint', label: '请求地址' }, { key: 'content_template', label: '短信文案模板（{code}/{ttl} 占位）', area: true },
    { key: 'method', label: '请求方法 GET/POST（默认 POST）', optional: true },
    { key: 'content_type', label: '内容类型 form/json（默认 form）', optional: true },
    { key: 'body_template', label: '请求体模板（{{phone}}/{{code}}/{{content}}/{{secret:KEY}}）', optional: true, area: true },
    { key: 'success_keyword', label: '成功关键字（留空 2xx 即成功）', optional: true },
  ],
}
const smsProviders = ref<any[]>([])
const smsAvailable = ref<string[]>([])
const smsMessages = ref<any[]>([])
const smsForm = reactive({ name: '', provider: 'aliyun' } as { name: string; provider: string; values: Record<string, string> })
smsForm.values = {}
const smsSaving = ref(false)

const smsFieldSpecs = () => SMS_FIELD_SPECS[smsForm.provider] || []
const smsChannelLabels: Record<string, string> = {
  aliyun: '阿里云', qcloudsms: '腾讯云', submail: '赛邮', huaweicloud: '华为云', smsbao: '短信宝', generic: '通用 HTTP',
}

function pickSmsProvider(p: string) {
  smsForm.provider = p
  smsForm.values = {}
}
async function loadSmsProviders() {
  try {
    const d = dataOf<any>(await api.get('/admin/sms-providers'))
    smsProviders.value = d.providers || []
    smsAvailable.value = d.available || []
  } catch { /* ignore */ }
  try { smsMessages.value = dataOf<any[]>(await api.get('/admin/sms-messages?limit=20')) } catch { smsMessages.value = [] }
}
async function saveSmsProvider() {
  if (!smsForm.name.trim()) { message.warning('请填写通道名称'); return }
  const config: Record<string, string> = {}
  const secret: Record<string, string> = {}
  for (const spec of smsFieldSpecs()) {
    const v = (smsForm.values[spec.key] || '').trim()
    if (!v) continue
    if (spec.secret) secret[spec.key] = v
    else config[spec.key] = v
  }
  smsSaving.value = true
  try {
    await api.post('/admin/sms-providers', { name: smsForm.name.trim(), provider: smsForm.provider, config, secret, is_default: smsProviders.value.length === 0 })
    message.success('短信通道已保存')
    smsForm.name = ''
    smsForm.values = {}
    await loadSmsProviders()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { smsSaving.value = false }
}
async function setDefaultSms(id: string) {
  try { await api.post(`/admin/sms-providers/${id}/default`); await loadSmsProviders() } catch (e: any) { message.error(e?.response?.data?.error?.message || '设置失败') }
}
async function deleteSms(id: string) {
  try { await api.delete(`/admin/sms-providers/${id}`); await loadSmsProviders() } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---- 第三方登录（对应魔方 public/plugins/oauth/）----
const OAUTH_FIELD_SPECS: Record<string, FieldSpec[]> = {
  github: [
    { key: 'client_id', label: 'Client ID' }, { key: 'scope', label: 'Scope（默认 read:user user:email）', optional: true },
    { key: 'client_secret', label: 'Client Secret', secret: true },
  ],
  qq: [
    { key: 'client_id', label: 'AppID' }, { key: 'scope', label: 'Scope（默认 snsapi_login）', optional: true },
    { key: 'client_secret', label: 'AppKey', secret: true },
  ],
  weixin: [
    { key: 'client_id', label: 'AppID（开放平台）' },
    { key: 'client_secret', label: 'AppSecret', secret: true },
  ],
  weibo: [
    { key: 'client_id', label: 'App Key' }, { key: 'scope', label: 'Scope（留空用应用默认）', optional: true },
    { key: 'client_secret', label: 'App Secret', secret: true },
  ],
  alipay: [
    { key: 'app_id', label: 'APPID' },
    { key: 'app_private_key', label: '开发者私钥（PKCS8/base64）', secret: true, area: true },
  ],
}
const oauthProviders = ref<any[]>([])
const oauthAvailable = ref<string[]>([])
const oauthForm = reactive({ provider: 'github', allow_register: true } as { provider: string; allow_register: boolean; name: string; values: Record<string, string> })
oauthForm.values = {}
oauthForm.name = ''
const oauthSaving = ref(false)

const oauthFieldSpecs = () => OAUTH_FIELD_SPECS[oauthForm.provider] || []
const oauthChannelLabels: Record<string, string> = {
  github: 'GitHub', qq: 'QQ', weixin: '微信', weibo: '微博', alipay: '支付宝',
}
function pickOAuthProvider(p: string) {
  oauthForm.provider = p
  oauthForm.values = {}
}
async function loadOauthProviders() {
  try {
    const d = dataOf<any>(await api.get('/admin/oauth-providers'))
    oauthProviders.value = d.providers || []
    oauthAvailable.value = d.available || []
  } catch { /* ignore */ }
}
async function saveOauthProvider() {
  const config: Record<string, string> = {}
  const secret: Record<string, string> = {}
  for (const spec of oauthFieldSpecs()) {
    const v = (oauthForm.values[spec.key] || '').trim()
    if (!v) continue
    if (spec.secret) secret[spec.key] = v
    else config[spec.key] = v
  }
  oauthSaving.value = true
  try {
    await api.post('/admin/oauth-providers', { name: oauthForm.name.trim(), provider: oauthForm.provider, config, secret, allow_register: oauthForm.allow_register })
    message.success('第三方登录已保存（登录页会展示已启用的通道）')
    oauthForm.values = {}
    await loadOauthProviders()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { oauthSaving.value = false }
}
async function toggleOauth(p: any) {
  try { await api.post(`/admin/oauth-providers/${p.public_id}/toggle`, { active: !p.active }); await loadOauthProviders() } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}
async function deleteOauth(id: string) {
  try { await api.delete(`/admin/oauth-providers/${id}`); await loadOauthProviders() } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

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
  await Promise.all([loadSmsProviders(), loadOauthProviders(), loadCertProviders()])
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
    <section class="panel">
      <div class="panel-title-row"><div><h2>短信通道</h2><span>验证码发送通道（限频与风控在服务端）</span></div></div>
      <div class="admin-two-col">
        <div class="stack">
          <div class="form-grid">
            <label><span>通道名称</span><NInput v-model:value="smsForm.name" placeholder="例如：主用-腾讯云" /></label>
            <label><span>通道类型</span>
              <select class="native-select" :value="smsForm.provider" @change="pickSmsProvider(($event.target as HTMLSelectElement).value)">
                <option v-for="p in smsAvailable" :key="p" :value="p">{{ smsChannelLabels[p] || p }}（{{ p }}）</option>
              </select>
            </label>
          </div>
          <div class="form-grid">
            <label v-for="spec in smsFieldSpecs()" :key="spec.key" :class="{ full: spec.area }">
              <span>{{ spec.label }}<template v-if="spec.secret">（凭据）</template><template v-if="spec.optional">（可选）</template></span>
              <NInput v-if="spec.secret" v-model:value="smsForm.values[spec.key]" type="password" show-password-on="click" />
              <NInput v-else-if="spec.area" v-model:value="smsForm.values[spec.key]" type="textarea" :rows="2" />
              <NInput v-else v-model:value="smsForm.values[spec.key]" />
            </label>
          </div>
          <NButton type="primary" :loading="smsSaving" @click="saveSmsProvider">添加通道</NButton>
          <div v-if="smsProviders.length" class="stack">
            <div v-for="p in smsProviders" :key="p.public_id" class="advanced-row">
              <div><b>{{ p.name }}</b><small>{{ smsChannelLabels[p.provider] || p.provider }}<template v-if="p.is_default"> · 默认</template><template v-if="p.last_error"> · 最近错误：{{ p.last_error }}</template></small></div>
              <div class="row" style="gap:8px">
                <NButton v-if="!p.is_default" size="small" secondary @click="setDefaultSms(p.public_id)">设为默认</NButton>
                <NButton size="small" quaternary type="error" @click="deleteSms(p.public_id)">删除</NButton>
              </div>
            </div>
          </div>
          <div v-if="smsMessages.length" class="stack">
            <div class="panel-title-row"><div><h2>最近发送流水</h2><span>短信是花钱的，盗刷主要靠这张表排查</span></div></div>
            <div v-for="(m, i) in smsMessages" :key="i" class="advanced-row">
              <div><b>{{ m.phone }}</b><small>{{ m.purpose }} · {{ m.provider_name || m.provider || '-' }} · {{ m.success ? '成功' : '失败' }}<template v-if="m.error"> · {{ m.error }}</template></small></div>
            </div>
          </div>
        </div>
      </div>
    </section>

    <section class="panel">
      <div class="panel-title-row"><div><h2>第三方登录</h2><span>启用后登录页出现对应按钮；回调地址为 {站点}/api/v1/auth/oauth/{通道}/callback</span></div></div>
      <div class="admin-two-col">
        <div class="stack">
          <div class="form-grid">
            <label><span>通道类型</span>
              <select class="native-select" :value="oauthForm.provider" @change="pickOAuthProvider(($event.target as HTMLSelectElement).value)">
                <option v-for="p in oauthAvailable" :key="p" :value="p">{{ oauthChannelLabels[p] || p }}（{{ p }}）</option>
              </select>
            </label>
            <label><span>显示名称（可选）</span><NInput v-model:value="oauthForm.name" :placeholder="oauthChannelLabels[oauthForm.provider] || oauthForm.provider" /></label>
          </div>
          <div class="form-grid">
            <label v-for="spec in oauthFieldSpecs()" :key="spec.key" :class="{ full: spec.area }">
              <span>{{ spec.label }}<template v-if="spec.secret">（凭据）</template><template v-if="spec.optional">（可选）</template></span>
              <NInput v-if="spec.secret" v-model:value="oauthForm.values[spec.key]" type="password" show-password-on="click" :placeholder="spec.area ? 'PKCS8 PEM 或纯 base64' : ''" />
              <NInput v-else v-model:value="oauthForm.values[spec.key]" />
            </label>
          </div>
          <div class="advanced-row"><div><b>允许自动注册</b><small>未绑定用户首次登录时自动建号（邮箱冲突会要求先绑定）</small></div><NCheckbox v-model:checked="oauthForm.allow_register" /></div>
          <NButton type="primary" :loading="oauthSaving" @click="saveOauthProvider">保存通道（凭据留空表示沿用已存值）</NButton>
          <div v-if="oauthProviders.length" class="stack">
            <div v-for="p in oauthProviders" :key="p.public_id" class="advanced-row">
              <div><b>{{ p.name || p.provider }}</b><small>{{ oauthChannelLabels[p.provider] || p.provider }}<template v-if="!p.active"> · 已停用</template><template v-if="p.last_error"> · 最近错误：{{ p.last_error }}</template></small></div>
              <div class="row" style="gap:8px">
                <NButton size="small" secondary @click="toggleOauth(p)">{{ p.active ? '停用' : '启用' }}</NButton>
                <NButton size="small" quaternary type="error" @click="deleteOauth(p.public_id)">删除</NButton>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
    <section class="panel">
      <div class="panel-title-row"><div><h2>实名核验通道</h2><span>「人工审核」无需配置；自动核验按次计费，凭据加密存储</span></div></div>
      <div class="admin-two-col">
        <div class="stack">
          <div class="form-grid">
            <label><span>通道名称</span><NInput v-model:value="certForm.name" placeholder="例如：阿里云二要素" /></label>
            <label><span>通道类型</span>
              <select class="native-select" :value="certForm.provider" @change="pickCertProvider(($event.target as HTMLSelectElement).value)">
                <option v-for="p in certAvailable" :key="p" :value="p">{{ certChannelLabels[p] || p }}（{{ p }}）</option>
              </select>
            </label>
          </div>
          <div v-if="certFieldSpecs().length" class="form-grid">
            <label v-for="spec in certFieldSpecs()" :key="spec.key" :class="{ full: spec.area }">
              <span>{{ spec.label }}<template v-if="spec.secret">（凭据）</template><template v-if="spec.optional">（可选）</template></span>
              <NInput v-if="spec.secret" v-model:value="certForm.values[spec.key]" type="password" show-password-on="click" />
              <NInput v-else v-model:value="certForm.values[spec.key]" />
            </label>
          </div>
          <NButton type="primary" :loading="certSaving" @click="saveCertProvider">添加通道</NButton>
          <div v-if="certProviders.length" class="stack">
            <div v-for="p in certProviders" :key="p.public_id || p.id" class="advanced-row">
              <div><b>{{ p.name }}</b><small>{{ certChannelLabels[p.provider] || p.provider }}<template v-if="p.is_default"> · 默认</template><template v-if="p.last_error"> · 最近错误：{{ p.last_error }}</template></small></div>
              <div class="row" style="gap:8px">
                <NButton v-if="!p.is_default" size="small" secondary @click="setDefaultCert(p.public_id || p.id)">设为默认</NButton>
                <NButton size="small" quaternary type="error" @click="deleteCert(p.public_id || p.id)">删除</NButton>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.native-select { height: 34px; border-radius: 6px; border: 1px solid var(--border, #d5d9e4); background: var(--panel, #fff); color: var(--text, #1c2333); padding: 0 8px; width: 100%; }
</style>
