<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import { NButton, NInput } from 'naive-ui'
import { api, dataOf } from '../api'

// 人机验证：按后端 /auth/captcha 返回的 provider 渲染对应组件。
// - builtin: 内置 SVG 算术题（id + 答案）
// - google_captcha: reCAPTCHA（token）
// - tencent_captcha: TCaptcha（ticket + randstr）
// 票据都是单次的，提交失败后父组件会调用 refresh() 重置。
const id = defineModel<string>('id')
const answer = defineModel<string>('answer')
const token = defineModel<string>('token')
const randstr = defineModel<string>('randstr')

const provider = ref('none')
const svg = ref('')
const siteKey = ref('')
const appID = ref('')
const verified = ref(false)
const loading = ref(false)
const loadError = ref('')
const widgetEl = ref<HTMLElement | null>(null)

const GOOGLE_SCRIPT = 'https://www.recaptcha.net/recaptcha/api.js'
const TENCENT_SCRIPT = 'https://turing.captcha.qcloud.com/TCaptcha.js'

let googlePromise: Promise<void> | null = null
let googleWidgetId: number | null = null
let tencentPromise: Promise<void> | null = null

// loadGoogleScript 注入谷歌脚本；显式渲染模式只有 onload 回调可用，
// 回调名固定挂在 window 上，脚本只会插入一次。
function loadGoogleScript(): Promise<void> {
  const w = window as any
  if (w.grecaptcha && typeof w.grecaptcha.render === 'function') return Promise.resolve()
  if (googlePromise) return googlePromise
  googlePromise = new Promise<void>((resolve, reject) => {
    w.__onRecaptchaLoad = () => resolve()
    const s = document.createElement('script')
    s.src = GOOGLE_SCRIPT + '?render=explicit&onload=__onRecaptchaLoad'
    s.async = true
    s.defer = true
    s.onerror = () => { googlePromise = null; reject(new Error('人机验证脚本加载失败，请检查网络')) }
    document.head.appendChild(s)
  })
  return googlePromise
}

// setupGoogle 渲染或重置谷歌组件：刷新后旧票据作废，必须重新出题。
async function setupGoogle() {
  await loadGoogleScript()
  await nextTick()
  const w = window as any
  if (!widgetEl.value || !w.grecaptcha) return
  if (googleWidgetId !== null) { w.grecaptcha.reset(googleWidgetId); return }
  googleWidgetId = w.grecaptcha.render(widgetEl.value, {
    sitekey: siteKey.value,
    callback: (value: string) => { token.value = value; verified.value = true },
    'expired-callback': () => { token.value = ''; verified.value = false },
  })
}

function loadTencentScript(): Promise<void> {
  const w = window as any
  if (w.TencentCaptcha) return Promise.resolve()
  if (tencentPromise) return tencentPromise
  tencentPromise = new Promise<void>((resolve, reject) => {
    const s = document.createElement('script')
    s.src = TENCENT_SCRIPT
    s.async = true
    s.onload = () => resolve()
    s.onerror = () => { tencentPromise = null; reject(new Error('人机验证脚本加载失败，请检查网络')) }
    document.head.appendChild(s)
  })
  return tencentPromise
}

// openTencent 弹出腾讯云滑块；ret===0 才拿到一次性 ticket（randstr 需一起回传）。
function openTencent() {
  const TC = (window as any).TencentCaptcha
  if (!TC) { loadError.value = '人机验证脚本未加载，请刷新重试'; return }
  new TC(appID.value, (res: any) => {
    if (res && res.ret === 0 && res.ticket) {
      token.value = res.ticket
      randstr.value = res.randstr || ''
      verified.value = true
      loadError.value = ''
    } else if (res && res.ret !== 0) {
      loadError.value = '人机验证未通过，请重试'
    }
  })
}

async function refresh() {
  loading.value = true
  loadError.value = ''
  verified.value = false
  token.value = ''
  randstr.value = ''
  try {
    const d = dataOf<Record<string, string>>(await api.get('/auth/captcha'))
    provider.value = d.provider || 'none'
    if (provider.value === 'builtin') {
      svg.value = d.svg || ''
      id.value = d.id || ''
    } else if (provider.value === 'google_captcha') {
      siteKey.value = d.site_key || ''
      await nextTick()
      try { await setupGoogle() } catch (e: any) { loadError.value = e?.message || '人机验证加载失败' }
    } else if (provider.value === 'tencent_captcha') {
      appID.value = d.captcha_app_id || ''
      try { await loadTencentScript() } catch (e: any) { loadError.value = e?.message || '人机验证加载失败' }
    }
  } catch {
    provider.value = 'none'
    svg.value = ''
  } finally {
    loading.value = false
  }
}
defineExpose({ refresh })
onMounted(refresh)
</script>

<template>
  <div class="captcha-wrap">
    <template v-if="provider === 'builtin'">
      <div class="captcha-row">
        <NInput v-model:value="answer" placeholder="计算结果" maxlength="4" />
        <div class="captcha-img" :class="{ loading }" title="点击刷新验证码" @click="refresh" v-html="svg"></div>
      </div>
    </template>
    <template v-else-if="provider === 'google_captcha'">
      <div ref="widgetEl" class="captcha-widget"></div>
    </template>
    <template v-else-if="provider === 'tencent_captcha'">
      <NButton v-if="!verified" type="primary" :loading="loading" @click="openTencent">点击完成人机验证</NButton>
    </template>
    <template v-else>
      <div class="captcha-tip">当前未开启人机验证</div>
    </template>
    <div v-if="verified && provider !== 'builtin'" class="captcha-tip ok">已完成人机验证</div>
    <div v-if="loadError" class="captcha-tip error">{{ loadError }}</div>
  </div>
</template>

<style scoped>
.captcha-wrap { display: flex; flex-direction: column; gap: 6px; }
.captcha-row { display: flex; gap: 8px; align-items: stretch; }
.captcha-row .n-input { flex: 1; }
.captcha-img { flex: 0 0 160px; height: 40px; border-radius: 6px; overflow: hidden; cursor: pointer; background: var(--panel, #f6f7fb); display: flex; align-items: center; }
.captcha-img :deep(svg) { width: 100%; height: 100%; display: block; }
.captcha-img.loading { opacity: .5; }
.captcha-widget { min-height: 40px; }
.captcha-tip { font-size: 13px; color: #6b7280; }
.captcha-tip.ok { color: #18a058; }
.captcha-tip.error { color: #d03050; }
</style>
