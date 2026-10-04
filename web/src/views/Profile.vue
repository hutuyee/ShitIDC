<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NInput, NModal, NTag, useMessage } from 'naive-ui'
import { api, dataOf, setCSRF } from '../api'
import { useAuthStore } from '../stores/auth'

const message = useMessage()
const router = useRouter()
const auth = useAuthStore()
const loading = ref(true)
const saving = ref(false)
const email = ref('')
const uid = ref(0)
const form = reactive({
  nickname: '', real_name: '', company: '', phone: '', qq: '',
  country: '中国', province: '', city: '', address: '',
})

// ---- password change ----
const pwOpen = ref(false)
const pwSaving = ref(false)
const pwForm = reactive({ current_password: '', new_password: '', confirm: '' })

async function changePassword() {
  if (!pwForm.current_password) { message.error('请输入当前密码'); return }
  if (pwForm.new_password.length < 10) { message.error('新密码至少 10 个字符'); return }
  if (pwForm.new_password !== pwForm.confirm) { message.error('两次输入的新密码不一致'); return }
  pwSaving.value = true
  try {
    await api.post('/auth/password/change', { current_password: pwForm.current_password, new_password: pwForm.new_password })
    message.success('密码已修改，所有会话已失效，请重新登录')
    setCSRF('')
    auth.user = null
    router.push('/login')
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '修改失败')
  } finally {
    pwSaving.value = false
  }
}

// ---- device / session management ----
const sessions = ref<any[]>([])
const sessionBusy = ref(0)

async function loadSessions() {
  try { sessions.value = dataOf(await api.get('/sessions')) } catch { sessions.value = [] }
}

async function revokeSession(s: any) {
  sessionBusy.value = s.id
  try {
    await api.delete(`/sessions/${s.id}`)
    message.success(s.current ? '当前设备已下线，请重新登录' : '设备已下线')
    if (s.current) { setCSRF(''); auth.user = null; router.push('/login'); return }
    await loadSessions()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '下线失败')
  } finally { sessionBusy.value = 0 }
}

async function revokeOthers() {
  try {
    const d = dataOf<{ revoked: number }>(await api.post('/sessions/revoke-others'))
    message.success(`已下线其他 ${d.revoked} 台设备`)
    await loadSessions()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}

const deviceName = (ua: string) => {
  if (/mobile|iphone|android/i.test(ua)) return '📱 移动设备'
  if (/edg/i.test(ua)) return '🌐 Edge 浏览器'
  if (/chrome/i.test(ua)) return '🌐 Chrome 浏览器'
  if (/firefox/i.test(ua)) return '🌐 Firefox 浏览器'
  if (/safari/i.test(ua)) return '🌐 Safari 浏览器'
  if (!ua) return '❓ 未知设备'
  return '💻 其他客户端'
}

// ---- TOTP two factor ----
const totpEnabled = ref(false)
const totpModal = ref<'off' | 'setup' | 'disable'>('off')
const totpSaving = ref(false)
const totpPassword = ref('')
const totpCode = ref('')
const totpSecret = ref('')
const totpUri = ref('')

async function loadTotp() {
  try { totpEnabled.value = (dataOf<{ enabled: boolean }>(await api.get('/auth/2fa/status'))).enabled } catch { totpEnabled.value = false }
}

async function startSetup() {
  totpModal.value = 'setup'
  totpPassword.value = ''; totpCode.value = ''; totpSecret.value = ''; totpUri.value = ''
}

async function doSetup() {
  if (!totpPassword.value) { message.error('请输入当前密码'); return }
  totpSaving.value = true
  try {
    const d = dataOf<{ secret: string; otpauth_uri: string }>(await api.post('/auth/2fa/setup', { password: totpPassword.value }))
    totpSecret.value = d.secret
    totpUri.value = d.otpauth_uri
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '初始化失败')
  } finally { totpSaving.value = false }
}

async function doEnable() {
  if (totpCode.value.trim().length !== 6) { message.error('请输入 6 位验证码'); return }
  totpSaving.value = true
  try {
    await api.post('/auth/2fa/enable', { code: totpCode.value.trim() })
    message.success('两步验证已开启，下次登录需要输入认证器验证码')
    totpModal.value = 'off'
    await loadTotp()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '开启失败')
  } finally { totpSaving.value = false }
}

async function doDisable() {
  if (!totpPassword.value) { message.error('请输入当前密码'); return }
  if (totpCode.value.trim().length !== 6) { message.error('请输入 6 位验证码'); return }
  totpSaving.value = true
  try {
    await api.post('/auth/2fa/disable', { password: totpPassword.value, code: totpCode.value.trim() })
    message.success('两步验证已关闭')
    totpModal.value = 'off'
    await loadTotp()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '关闭失败')
  } finally { totpSaving.value = false }
}

function copySecret() {
  navigator.clipboard?.writeText(totpSecret.value)
  message.success('密钥已复制')
}

async function load() {
  loading.value = true
  try {
    const d = dataOf<{ uid: number; email: string; profile: typeof form }>(await api.get('/profile'))
    email.value = d.email
    uid.value = d.uid
    Object.assign(form, d.profile)
    if (!form.country) form.country = '中国'
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取资料失败')
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    await api.put('/profile', { ...form })
    message.success('个人资料已保存')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

onMounted(() => { load(); loadSessions(); loadTotp() })
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">账户管理</div>
        <h1>个人信息</h1>
        <p>维护你的联系方式与详细资料，客服会通过这些信息联系你；邮箱是登录账号，不可在此修改。</p>
      </div>
      <router-link to="/tokens" class="soft-action">API 管理 →</router-link>
    </div>

    <div v-if="!loading" class="admin-two-col">
      <section class="panel stack admin-form-panel">
        <div class="panel-title-row"><div><h2>联系信息</h2><span>用于订单通知、工单回复与身份核对</span></div></div>
        <div class="form-grid">
          <label class="full">邮箱地址（登录账号，不可修改）
            <NInput :value="email" disabled />
          </label>
          <label>手机号
            <NInput v-model:value="form.phone" placeholder="用于紧急联系" />
          </label>
          <label>QQ 号码
            <NInput v-model:value="form.qq" placeholder="方便客服快速联系" />
          </label>
        </div>

        <div class="panel-title-row"><div><h2>详细资料</h2><span>开具账单 / 实名信息时使用</span></div></div>
        <div class="form-grid">
          <label>昵称
            <NInput v-model:value="form.nickname" placeholder="在站内展示的名称" />
          </label>
          <label>真实姓名
            <NInput v-model:value="form.real_name" placeholder="与支付账户一致便于核对" />
          </label>
          <label>公司名称
            <NInput v-model:value="form.company" placeholder="个人用户可留空" />
          </label>
          <label>国家
            <NInput v-model:value="form.country" placeholder="中国" />
          </label>
          <label>省份
            <NInput v-model:value="form.province" placeholder="如：广东省" />
          </label>
          <label>城市
            <NInput v-model:value="form.city" placeholder="如：深圳市" />
          </label>
          <label class="full">详细地址
            <NInput v-model:value="form.address" placeholder="省市区之外的详细地址" />
          </label>
        </div>
        <div class="form-actions">
          <NButton type="primary" :loading="saving" @click="save">保存资料</NButton>
        </div>
      </section>

      <section class="panel stack admin-form-panel">
        <div class="panel-title-row"><div><h2>账户概要</h2><span>只读信息</span></div></div>
        <div class="mini-row"><span class="muted">UID</span><b>{{ uid }}</b></div>
        <div class="mini-row"><span class="muted">邮箱</span><b>{{ email }}</b></div>
        <div class="mini-row"><span class="muted">API 管理</span><router-link to="/tokens">创建 / 管理 API Token →</router-link></div>
        <div class="mini-row"><span class="muted">工单支持</span><router-link to="/tickets">提交 / 查看工单 →</router-link></div>

        <div class="panel-title-row"><div><h2>安全</h2><span>密码 / 两步验证 / 登录设备</span></div></div>
        <div class="row" style="gap:8px;flex-wrap:wrap">
          <NButton secondary @click="pwOpen = true">修改密码</NButton>
          <NButton v-if="!totpEnabled" secondary type="primary" @click="startSetup">开启两步验证 (2FA)</NButton>
          <template v-else>
            <NTag type="success" round>两步验证已开启</NTag>
            <NButton secondary size="small" @click="totpModal = 'disable'; totpPassword = ''; totpCode = ''">关闭两步验证</NButton>
          </template>
        </div>
        <div class="security-note">修改密码后会吊销全部登录会话（含本机），需要重新登录；忘记密码可在登录页使用「找回密码」。</div>

        <div class="panel-title-row"><div><h2>登录设备</h2><span>近期的活跃会话</span></div><NButton size="small" tertiary @click="revokeOthers">下线其他设备</NButton></div>
        <div class="device-list">
          <div v-for="s in sessions" :key="s.id" class="device-row">
            <span class="device-icon">{{ deviceName(s.user_agent || '') }}</span>
            <div class="device-main">
              <b>{{ s.current ? '本设备' : deviceName(s.user_agent || '') }}</b>
              <small>{{ s.ip || '未知 IP' }} · 最近活跃 {{ new Date(s.last_seen_at).toLocaleString() }}</small>
              <small class="device-ua">{{ s.user_agent }}</small>
            </div>
            <NTag v-if="s.current" size="small" type="info" round>当前</NTag>
            <NButton size="small" tertiary type="error" :loading="sessionBusy === s.id" @click="revokeSession(s)">下线</NButton>
          </div>
          <div v-if="!sessions.length" class="empty-box">暂无活跃会话。</div>
        </div>
      </section>
    </div>

    <NModal v-model:show="pwOpen" preset="card" title="修改密码" style="width:min(420px,92vw)">
      <div class="stack">
        <NInput v-model:value="pwForm.current_password" type="password" show-password-on="click" placeholder="当前密码" />
        <NInput v-model:value="pwForm.new_password" type="password" show-password-on="click" placeholder="新密码（至少 10 位）" />
        <NInput v-model:value="pwForm.confirm" type="password" show-password-on="click" placeholder="确认新密码" />
        <NButton type="primary" block :loading="pwSaving" @click="changePassword">确认修改</NButton>
      </div>
    </NModal>

    <NModal :show="totpModal === 'setup'" preset="card" title="开启两步验证 (TOTP)" style="width:min(460px,92vw)" @update:show="(v: boolean) => { if (!v) totpModal = 'off' }">
      <div class="stack">
        <template v-if="!totpSecret">
          <p class="muted" style="margin:0">验证身份后生成密钥。需要一部安装了认证器（Google Authenticator、1Password 等）的手机。</p>
          <NInput v-model:value="totpPassword" type="password" show-password-on="click" placeholder="当前密码" @keyup.enter="doSetup" />
          <NButton type="primary" block :loading="totpSaving" @click="doSetup">生成密钥</NButton>
        </template>
        <template v-else>
          <p style="margin:0"><b>1.</b> 在认证器中添加账户，选择「输入设置密钥」，粘贴下方密钥：</p>
          <div class="totp-secret"><code>{{ totpSecret }}</code><NButton size="tiny" tertiary @click="copySecret">复制</NButton></div>
          <p class="muted" style="margin:0;font-size:12px;word-break:break-all">或直接把此链接导入认证器：{{ totpUri }}</p>
          <p style="margin:0"><b>2.</b> 输入认证器显示的 6 位验证码完成绑定：</p>
          <NInput v-model:value="totpCode" placeholder="6 位验证码" maxlength="6" @keyup.enter="doEnable" />
          <NButton type="primary" block :loading="totpSaving" @click="doEnable">确认开启</NButton>
        </template>
      </div>
    </NModal>

    <NModal :show="totpModal === 'disable'" preset="card" title="关闭两步验证" style="width:min(420px,92vw)" @update:show="(v: boolean) => { if (!v) totpModal = 'off' }">
      <div class="stack">
        <p class="muted" style="margin:0">关闭后登录仅需密码。请输入密码与当前认证器验证码确认。</p>
        <NInput v-model:value="totpPassword" type="password" show-password-on="click" placeholder="当前密码" />
        <NInput v-model:value="totpCode" placeholder="6 位验证码" maxlength="6" />
        <NButton type="error" block :loading="totpSaving" @click="doDisable">确认关闭</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.device-list { display: flex; flex-direction: column; gap: 8px; }
.device-row { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border: 1px solid var(--border, #e5e8f0); border-radius: 10px; }
.device-icon { font-size: 20px; }
.device-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.device-main small { color: var(--muted, #8a93a6); font-size: 12px; }
.device-main .device-ua { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.totp-secret { display: flex; align-items: center; gap: 8px; background: var(--panel, #f6f7fb); border-radius: 8px; padding: 8px 10px; }
.totp-secret code { font-size: 15px; letter-spacing: 2px; word-break: break-all; }
</style>
