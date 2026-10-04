<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NSelect, NTabs, NTabPane, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const tab = ref('audit')

// ---- audit log ----
const entries = ref<any[]>([])
const action = ref('')
const actor = ref('')
const loading = ref(false)

const actionOptions = [
  { label: '全部', value: '' },
  { label: '登录 / 注册', value: 'auth.' },
  { label: '订单', value: 'order.' },
  { label: '支付 / 充值', value: 'payment' },
  { label: '充值', value: 'recharge.' },
  { label: '钱包调账', value: 'wallet.adjust' },
  { label: '商品', value: 'product.' },
  { label: '供应商', value: 'provider.' },
  { label: '支付方式', value: 'payment_provider.' },
  { label: '公告', value: 'announcement.' },
  { label: '会话', value: 'session.' },
  { label: '系统设置', value: 'settings.' },
]

const actionText: Record<string, string> = {
  'auth.login': '用户登录', 'auth.register': '用户注册', 'auth.2fa.enable': '开启两步验证', 'auth.2fa.disable': '关闭两步验证', 'user.email.verified': '邮箱验证成功',
  'order.create': '创建订单', 'order.cancel': '取消订单', 'order.pay': '余额支付订单', 'order.pay_online': '发起在线支付',
  'payment.completed': '支付完成回调', 'recharge.create': '发起充值', 'wallet.adjust': '余额调账',
  'product.create': '创建商品', 'product.update': '更新商品', 'product_group.create': '创建分组', 'product_group.update': '更新分组', 'product_group.delete': '删除分组', 'provider.create': '创建供应商', 'provider.update': '更新供应商', 'provider.product.import': '导入上游商品',
  'payment_provider.create': '创建支付方式', 'payment_provider.update': '更新支付方式',
  'announcement.create': '发布公告', 'announcement.update': '更新公告', 'announcement.delete': '删除公告',
  'session.revoke': '下线会话', 'session.revoke_others': '下线其他会话',
  'settings.mail.update': '更新邮件设置', 'settings.mail.test': '发送测试邮件',
}

async function loadAudit() {
  loading.value = true
  try {
    entries.value = dataOf(await api.get('/admin/audit', { params: { action: action.value, actor: actor.value.trim(), limit: 200 } }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取日志失败')
  } finally { loading.value = false }
}

// ---- login logs ----
const loginLogs = ref<any[]>([])
const loginEmail = ref('')
const loginLoading = ref(false)
const reasonText: Record<string, string> = {
  ok: '登录成功', bad_password: '密码错误', unknown_email: '邮箱不存在', rate_limited: '触发限流',
  account_locked: '账户锁定', account_disabled: '账户被禁用', totp_missing: '缺少两步验证码', totp_failed: '两步验证码错误', password_reset: '密码已重置',
}
async function loadLoginLogs() {
  loginLoading.value = true
  try {
    loginLogs.value = dataOf(await api.get('/admin/login-logs', { params: { email: loginEmail.value.trim(), limit: 200 } }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取登录日志失败')
  } finally { loginLoading.value = false }
}

// ---- API request logs (§5.6 api_logs) ----
const apiLogs = ref<any[]>([])
const apiMethod = ref('')
const apiPath = ref('')
const apiStatus = ref(null as number | null)
const apiLoading = ref(false)
const methodOptions = ['', 'GET', 'POST', 'PUT', 'DELETE'].map(m => ({ label: m || '全部方法', value: m }))
const statusOptions = [
  { label: '全部状态', value: null }, { label: '≥ 400（错误）', value: 400 },
  { label: '≥ 500（服务端错误）', value: 500 },
] as any[]
async function loadApiLogs() {
  apiLoading.value = true
  try {
    const params: any = { method: apiMethod.value, path: apiPath.value.trim(), limit: 200 }
    if (apiStatus.value != null) params.status_min = apiStatus.value
    apiLogs.value = dataOf(await api.get('/admin/api-logs', { params }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取 API 日志失败')
  } finally { apiLoading.value = false }
}

const fmtDate = (v: string) => new Date(v).toLocaleString()
const brief = (v: any) => v == null ? '' : JSON.stringify(v)
const statusClass = (s: number) => s >= 500 ? 'st-5xx' : s >= 400 ? 'st-4xx' : 'st-2xx'
const durText = (ms: number) => ms >= 1000 ? (ms / 1000).toFixed(1) + 's' : ms + 'ms'

onMounted(loadAudit)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">审计与行为</div><h1>操作 / 安全 / 请求日志</h1><p>关键业务操作写入审计日志，登录尝试逐条留痕，每个 API 请求记录到 api_logs（含状态码与耗时）。</p></div></div>

    <NTabs type="segment" v-model:value="tab" style="margin-bottom:14px" @update:value="(v: string) => { if (v === 'login' && !loginLogs.length) loadLoginLogs(); if (v === 'api' && !apiLogs.length) loadApiLogs() }">
      <NTabPane name="audit" tab="审计日志">
        <section class="panel">
          <div class="audit-toolbar">
            <NSelect v-model:value="action" :options="actionOptions" @update:value="loadAudit" />
            <NInput v-model:value="actor" placeholder="按用户 UID / UUID 过滤" @keyup.enter="loadAudit" />
            <NButton type="primary" :loading="loading" @click="loadAudit">查询</NButton>
          </div>
          <div v-if="entries.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head"><span>时间</span><span>行为</span><span>对象</span><span>操作者</span><span>IP</span><span>详情</span></div>
            <div v-for="e in entries" :key="e.id" class="audit-row">
              <span>{{ fmtDate(e.created_at) }}<small>#{{ e.id }}</small></span>
              <span><b>{{ actionText[e.action] || e.action }}</b></span>
              <span class="muted">{{ e.object_type }}<small>{{ e.object_id }}</small></span>
              <span class="muted">{{ e.actor_email || '系统' }}<small v-if="e.actor_uid">UID {{ e.actor_uid }}</small></span>
              <span class="muted">{{ e.ip || '—' }}<small class="ua">{{ e.user_agent }}</small></span>
              <span class="audit-json">{{ brief(e.after_data) || brief(e.before_data) || '—' }}</span>
            </div>
          </div></div>
          <div v-else class="empty-box">暂无日志。</div>
        </section>
      </NTabPane>

      <NTabPane name="login" tab="登录日志">
        <section class="panel">
          <div class="audit-toolbar">
            <NInput v-model:value="loginEmail" placeholder="按邮箱过滤" @keyup.enter="loadLoginLogs" />
            <NButton type="primary" :loading="loginLoading" @click="loadLoginLogs">查询</NButton>
          </div>
          <div v-if="loginLogs.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head"><span>时间</span><span>邮箱</span><span>结果</span><span>原因</span><span>IP</span><span>User-Agent</span></div>
            <div v-for="l in loginLogs" :key="l.id" class="audit-row">
              <span>{{ fmtDate(l.created_at) }}</span>
              <span class="muted">{{ l.email }}<small v-if="l.user_uid">UID {{ l.user_uid }}</small></span>
              <span><NTag :type="l.success ? 'success' : 'error'" size="small" round>{{ l.success ? '成功' : '失败' }}</NTag></span>
              <span>{{ reasonText[l.reason] || l.reason }}</span>
              <span class="muted">{{ l.ip || '—' }}</span>
              <span class="muted ua">{{ l.user_agent || '—' }}</span>
            </div>
          </div></div>
          <div v-else class="empty-box">暂无登录日志。</div>
        </section>
      </NTabPane>

      <NTabPane name="api" tab="API 请求日志">
        <section class="panel">
          <div class="audit-toolbar">
            <NSelect v-model:value="apiMethod" :options="methodOptions" @update:value="loadApiLogs" style="max-width:150px" />
            <NInput v-model:value="apiPath" placeholder="按路径过滤，如 /orders" @keyup.enter="loadApiLogs" />
            <NSelect v-model:value="apiStatus" :options="statusOptions" @update:value="loadApiLogs" style="max-width:200px" />
            <NButton type="primary" :loading="apiLoading" @click="loadApiLogs">查询</NButton>
          </div>
          <div v-if="apiLogs.length" class="table-scroll"><div class="audit-table">
            <div class="audit-row audit-head"><span>时间</span><span>方法</span><span>路径</span><span>状态</span><span>用户</span><span>耗时</span><span>IP</span></div>
            <div v-for="l in apiLogs" :key="l.id" class="audit-row">
              <span>{{ fmtDate(l.created_at) }}</span>
              <span><b>{{ l.method }}</b></span>
              <span class="mono">{{ l.path }}</span>
              <span><span class="status-chip" :class="statusClass(l.status)">{{ l.status }}</span><small v-if="l.error_code" class="ua">{{ l.error_code }}</small></span>
              <span class="muted"><small v-if="l.user_id">UID {{ l.user_id }}</small><small v-else-if="l.api_token">API Token</small><small v-else>匿名</small></span>
              <span class="muted">{{ durText(l.duration_ms) }}</span>
              <span class="muted">{{ l.ip || '—' }}</span>
            </div>
          </div></div>
          <div v-else class="empty-box">暂无请求日志。</div>
        </section>
      </NTabPane>
    </NTabs>
  </div>
</template>

<style scoped>
.status-chip { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; font-weight: 700; }
.st-2xx { background: rgba(48, 190, 120, .14); color: #1d9e64; }
.st-4xx { background: rgba(240, 170, 30, .16); color: #b07a08; }
.st-5xx { background: rgba(235, 70, 70, .14); color: #cf3030; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
</style>
