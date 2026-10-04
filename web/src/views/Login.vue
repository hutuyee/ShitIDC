<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NCard, NForm, NFormItem, NInput, NTabs, NTabPane, useMessage } from 'naive-ui'
import BrandMark from '../components/BrandMark.vue'
import CaptchaInput from '../components/CaptchaInput.vue'
import { useAuthStore, type LoginExtra } from '../stores/auth'
import { api } from '../api'

const email = ref('')
const password = ref('')
const code = ref('')
const verifyRequired = ref(false)
// SMTP configured: the register form then offers a real email code. It is
// mandatory only while the admin also turns on 强制邮箱验证.
const smtpEnabled = ref(false)
const captchaEnabled = ref(false)
const loading = ref(false)
const sending = ref(false)
const countdown = ref(0)
const tab = ref('login')
// 2FA: the field stays hidden until the backend answers TOTP_REQUIRED.
const totpNeeded = ref(false)
const totpCode = ref('')
// captcha per tab: each challenge is single-use, so every form keeps its own.
const loginCaptcha = ref({ id: '', answer: '' })
const regCaptcha = ref({ id: '', answer: '' })
const resetCaptcha = ref({ id: '', answer: '' })
const loginCaptchaRef = ref<InstanceType<typeof CaptchaInput> | null>(null)
const regCaptchaRef = ref<InstanceType<typeof CaptchaInput> | null>(null)
const resetCaptchaRef = ref<InstanceType<typeof CaptchaInput> | null>(null)
const auth = useAuthStore()
const router = useRouter()
const message = useMessage()
// 推广系统: prefill the invite code from ?ref= links.
const referralCode = ref(new URLSearchParams(window.location.search).get('ref') || '')

onMounted(async () => {
  try {
    const r = await api.get('/auth/config')
    verifyRequired.value = Boolean(r.data?.data?.email_verify_required)
    smtpEnabled.value = Boolean(r.data?.data?.smtp_enabled)
    captchaEnabled.value = Boolean(r.data?.data?.captcha_enabled)
  } catch { /* default off */ }
})

async function sendCode() {
  if (!email.value.includes('@')) { message.error('请先填写正确的邮箱'); return }
  sending.value = true
  try {
    await api.post('/auth/register/email-code', {
      email: email.value.trim().toLowerCase(),
      captcha_id: regCaptcha.value.id,
      captcha_answer: regCaptcha.value.answer,
    })
    message.success('验证码已发送，请查收邮箱（10 分钟内有效）')
    countdown.value = 60
    const timer = setInterval(() => {
      countdown.value -= 1
      if (countdown.value <= 0) clearInterval(timer)
    }, 1000)
  } catch (e: any) {
    regCaptchaRef.value?.refresh()
    message.error(e?.response?.data?.error?.message || '验证码发送失败')
  } finally {
    sending.value = false
  }
}

async function submit(register = false) {
  loading.value = true
  try {
    if (register) await auth.register(email.value, password.value, code.value.trim(), { captcha_id: regCaptcha.value.id, captcha_answer: regCaptcha.value.answer, referral_code: referralCode.value.trim() || undefined })
    else await auth.login(email.value, password.value, loginExtra())
    router.push('/')
  } catch (e: any) {
    const errCode = e?.response?.data?.error?.code
    message.error(e?.response?.data?.error?.message || '操作失败')
    if (errCode === 'EMAIL_NOT_VERIFIED') {
      tab.value = 'verify'
    }
    if (errCode === 'TOTP_REQUIRED' || errCode === 'TOTP_INVALID') {
      totpNeeded.value = true
    }
    if (errCode?.startsWith('CAPTCHA')) {
      loginCaptchaRef.value?.refresh()
      regCaptchaRef.value?.refresh()
    }
  } finally {
    loginCaptcha.value.answer = ''
    regCaptcha.value.answer = ''
    loading.value = false
  }
}

function loginExtra(): LoginExtra {
  const extra: LoginExtra = { captcha_id: loginCaptcha.value.id, captcha_answer: loginCaptcha.value.answer }
  if (totpNeeded.value) extra.totp_code = totpCode.value.trim()
  return extra
}

async function verify() {
  loading.value = true
  try {
    await api.post('/auth/verify-email', { email: email.value.trim().toLowerCase(), code: code.value.trim() })
    message.success('邮箱验证成功，现在可以登录了')
    tab.value = 'login'
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '验证失败')
  } finally {
    loading.value = false
  }
}

// ---- forgot password ----
const resetCode = ref('')
const resetPassword = ref('')
const resetCountdown = ref(0)
const resetSending = ref(false)

async function sendResetCode() {
  if (!email.value.includes('@')) { message.error('请先填写正确的邮箱'); return }
  resetSending.value = true
  try {
    await api.post('/auth/password/reset/request', {
      email: email.value.trim().toLowerCase(),
      captcha_id: resetCaptcha.value.id,
      captcha_answer: resetCaptcha.value.answer,
    })
    message.success('如果该邮箱已注册，重置验证码已发出（10 分钟内有效）')
    resetCountdown.value = 60
    const timer = setInterval(() => {
      resetCountdown.value -= 1
      if (resetCountdown.value <= 0) clearInterval(timer)
    }, 1000)
  } catch (e: any) {
    resetCaptchaRef.value?.refresh()
    message.error(e?.response?.data?.error?.message || '发送失败')
  } finally {
    resetSending.value = false
  }
}

async function submitReset() {
  if (resetPassword.value.length < 10) { message.error('新密码至少 10 位'); return }
  loading.value = true
  try {
    await api.post('/auth/password/reset/confirm', {
      email: email.value.trim().toLowerCase(),
      code: resetCode.value.trim(),
      new_password: resetPassword.value,
    })
    message.success('密码已重置，请使用新密码登录')
    tab.value = 'login'
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '重置失败')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <NCard class="login-card">
      <div class="login-brand"><BrandMark /></div>
      <NTabs type="segment" v-model:value="tab">
        <NTabPane name="login" tab="登录">
          <NForm>
            <NFormItem label="邮箱"><NInput v-model:value="email" /></NFormItem>
            <NFormItem label="密码"><NInput v-model:value="password" type="password" show-password-on="click" /></NFormItem>
            <NFormItem v-if="captchaEnabled" label="验证码">
              <CaptchaInput ref="loginCaptchaRef" v-model:id="loginCaptcha.id" v-model:answer="loginCaptcha.answer" />
            </NFormItem>
            <NFormItem v-if="totpNeeded" label="两步验证码">
              <NInput v-model:value="totpCode" placeholder="认证器 6 位数字" maxlength="6" />
            </NFormItem>
            <NButton block type="primary" :loading="loading" @click="submit(false)">登录</NButton>
          </NForm>
        </NTabPane>
        <NTabPane name="register" tab="注册">
          <NForm>
            <NFormItem label="邮箱"><NInput v-model:value="email" /></NFormItem>
            <NFormItem v-if="captchaEnabled" label="验证码">
              <CaptchaInput ref="regCaptchaRef" v-model:id="regCaptcha.id" v-model:answer="regCaptcha.answer" />
            </NFormItem>
            <NFormItem v-if="smtpEnabled" :label="verifyRequired ? '邮箱验证码' : '邮箱验证码（可选）'">
              <div class="code-row">
                <NInput v-model:value="code" placeholder="6 位数字" maxlength="6" />
                <NButton :disabled="countdown > 0 || sending" :loading="sending" @click="sendCode">{{ countdown > 0 ? `${countdown}s` : '获取验证码' }}</NButton>
              </div>
            </NFormItem>
            <NFormItem label="密码（至少10位）"><NInput v-model:value="password" type="password" show-password-on="click" /></NFormItem>
            <NFormItem v-if="referralCode" label="邀请码"><NInput v-model:value="referralCode" placeholder="好友邀请码" /></NFormItem>
            <NButton block type="primary" :loading="loading" @click="submit(true)">注册并登录</NButton>
          </NForm>
        </NTabPane>
        <NTabPane v-if="verifyRequired" name="verify" tab="验证邮箱">
          <NForm>
            <NFormItem label="邮箱"><NInput v-model:value="email" /></NFormItem>
            <NFormItem label="邮箱验证码">
              <div class="code-row">
                <NInput v-model:value="code" placeholder="6 位数字" maxlength="6" />
                <NButton :disabled="countdown > 0 || sending" :loading="sending" @click="sendCode">{{ countdown > 0 ? `${countdown}s` : '获取验证码' }}</NButton>
              </div>
            </NFormItem>
            <NButton block type="primary" :loading="loading" @click="verify">验证邮箱</NButton>
          </NForm>
        </NTabPane>
        <NTabPane name="reset" tab="找回密码">
          <NForm>
            <NFormItem label="邮箱"><NInput v-model:value="email" /></NFormItem>
            <NFormItem v-if="captchaEnabled" label="验证码">
              <CaptchaInput ref="resetCaptchaRef" v-model:id="resetCaptcha.id" v-model:answer="resetCaptcha.answer" />
            </NFormItem>
            <NFormItem label="重置验证码">
              <div class="code-row">
                <NInput v-model:value="resetCode" placeholder="6 位数字" maxlength="6" />
                <NButton :disabled="resetCountdown > 0 || resetSending" :loading="resetSending" @click="sendResetCode">{{ resetCountdown > 0 ? `${resetCountdown}s` : '发送验证码' }}</NButton>
              </div>
            </NFormItem>
            <NFormItem label="新密码（至少10位）"><NInput v-model:value="resetPassword" type="password" show-password-on="click" /></NFormItem>
            <NButton block type="primary" :loading="loading" @click="submitReset">重置密码</NButton>
            <p class="muted" style="font-size:12px;margin-top:10px">重置成功后所有已登录的会话都会失效；已开启两步验证的账户会同时重置两步验证。</p>
          </NForm>
        </NTabPane>
      </NTabs>
    </NCard>
  </div>
</template>
