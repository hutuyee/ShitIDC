import { defineStore } from 'pinia'
import { api, dataOf, setCSRF } from '../api'

export type User = { uid: number; id: string; email: string; status: string; email_verified?: boolean }
// Extra login fields: captcha challenge (§9 验证码) and the TOTP second
// factor code (§9 可选 2FA). The backend answers TOTP_REQUIRED when a code
// is needed, and the login form shows the input.
export type LoginExtra = {
  captcha_id?: string
  captcha_answer?: string
  captcha_token?: string
  captcha_randstr?: string
  totp_code?: string
}
export const useAuthStore = defineStore('auth', {
  state: () => ({ user: null as User | null, permissions: {} as Record<string, boolean>, ready: false }),
  actions: {
    async login(email: string, password: string, extra: LoginExtra = {}) {
      const r = await api.post('/auth/login', { email, password, ...extra })
      const d = dataOf<{user: User; csrf_token: string}>(r); this.user = d.user; setCSRF(d.csrf_token); await this.me()
    },
    async register(email: string, password: string, code = '', extra: { captcha_id?: string; captcha_answer?: string; captcha_token?: string; captcha_randstr?: string; referral_code?: string; custom_fields?: Record<string, string> } = {}) {
      await api.post('/auth/register', { email, password, code, ...extra }); await this.login(email, password)
    },
    async me() {
      try { const d = dataOf<any>(await api.get('/auth/me')); this.user = d.user; this.permissions = d.permissions || {}; if (d.csrf_token) setCSRF(d.csrf_token) }
      catch { this.user = null; this.permissions = {}; setCSRF('') }
      finally { this.ready = true }
    },
    async logout() { try { await api.post('/auth/logout') } finally { this.user = null; this.permissions = {}; setCSRF('') } }
  }
})
