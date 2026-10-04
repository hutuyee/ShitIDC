<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NCheckboxGroup, NCheckbox, NAlert, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const tokens = ref<any[]>([])
const name = ref('我的应用')
const scopes = ref<string[]>(['product.read'])
const secret = ref('')
const message = useMessage()

const scopeLabels: Record<string, string> = {
  'product.read': '读取在售产品',
  'order.read': '读取 / 创建自己的订单',
  'invoice.read': '读取自己的账单',
  'wallet.read': '读取余额 / 流水 / 余额支付',
  'service.read': '读取自己的服务与到期时间',
  'ticket.read': '读取自己的工单',
  'ticket.write': '创建 / 回复工单',
  'profile.manage': '读取 / 修改个人资料',
  'api_token.manage': '管理 API Token 自身',
}
const options = () => Object.keys(auth.permissions).filter(k => auth.permissions[k])

const endpoints = [
  { method: 'GET', path: '/api/v1/products', scope: 'product.read', desc: '在售产品与价格' },
  { method: 'GET', path: '/api/v1/orders', scope: 'order.read', desc: '订单列表（含明细与支付方式）' },
  { method: 'POST', path: '/api/v1/orders', scope: 'order.read', desc: '下单 { product_id, billing_cycle, quantity }' },
  { method: 'POST', path: '/api/v1/orders/{id}/pay', scope: 'wallet.read', desc: '余额支付订单' },
  { method: 'POST', path: '/api/v1/orders/{id}/cancel', scope: 'order.read', desc: '取消未支付订单' },
  { method: 'GET', path: '/api/v1/invoices', scope: 'invoice.read', desc: '账单列表' },
  { method: 'GET', path: '/api/v1/wallet', scope: 'wallet.read', desc: '余额' },
  { method: 'GET', path: '/api/v1/wallet/transactions', scope: 'wallet.read', desc: '资金流水' },
  { method: 'GET', path: '/api/v1/services', scope: 'service.read', desc: '服务列表（含到期时间）' },
  { method: 'POST', path: '/api/v1/services/{id}/renew', scope: 'order.read', desc: '生成续费订单' },
  { method: 'GET', path: '/api/v1/tickets', scope: 'ticket.read', desc: '工单列表' },
  { method: 'POST', path: '/api/v1/tickets', scope: 'ticket.write', desc: '提交工单' },
  { method: 'GET', path: '/api/v1/tickets/{id}', scope: 'ticket.read', desc: '工单会话详情' },
  { method: 'POST', path: '/api/v1/tickets/{id}/reply', scope: 'ticket.write', desc: '回复工单' },
  { method: 'GET', path: '/api/v1/profile', scope: 'profile.manage', desc: '读取个人资料' },
  { method: 'PUT', path: '/api/v1/profile', scope: 'profile.manage', desc: '修改个人资料' },
]

async function load() {
  tokens.value = dataOf(await api.get('/api-tokens'))
}
async function revoke(id: string) {
  try {
    await api.delete(`/api-tokens/${id}`)
    message.success('已撤销')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '撤销失败')
  }
}
async function create() {
  try {
    const d = dataOf<any>(await api.post('/api-tokens', { name: name.value, scopes: scopes.value }))
    secret.value = d.bearer
    message.success('Token 已创建，只显示一次')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '创建失败')
  }
}
function copySecret() {
  navigator.clipboard?.writeText(secret.value).then(() => message.success('已复制'))
}

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">账户管理</div>
        <h1>API 管理</h1>
        <p>用 API Token 以账号身份调用本站接口：自动下单、查余额、管工单，或把本站作为你的上游对接。</p>
      </div>
      <a href="/docs/openapi.yaml" class="soft-action" target="_blank">OpenAPI 规范 →</a>
    </div>

    <div class="card stack">
      <NInput v-model:value="name" placeholder="Token 名称（如：自动化脚本 / 下游面板）" />
      <div>
        <div class="muted" style="margin-bottom:6px">授予权限（只能授予自己拥有的权限子集）</div>
        <NCheckboxGroup v-model:value="scopes">
          <div class="grid">
            <NCheckbox v-for="s in options()" :key="s" :value="s" :label="`${s}（${scopeLabels[s] || s}）`" />
          </div>
        </NCheckboxGroup>
      </div>
      <NButton type="primary" @click="create">创建 Token</NButton>
      <NAlert v-if="secret" type="warning" title="请立即保存，Secret 不会再次显示">
        <code class="code-row">{{ secret }}</code>
        <NButton size="tiny" style="margin-top:6px" @click="copySecret">复制</NButton>
      </NAlert>
    </div>

    <h2 class="section-title">现有 Token</h2>
    <div v-if="!tokens.length" class="empty-box">还没有 Token。</div>
    <div class="stack">
      <div class="card" v-for="t in tokens" :key="t.id">
        <div class="row" style="justify-content:space-between;align-items:center">
          <div>
            <b>{{ t.name }}</b>
            <div class="muted">{{ t.key_id }} · 创建于 {{ new Date(t.created_at).toLocaleString() }}<template v-if="t.last_used_at"> · 最近使用 {{ new Date(t.last_used_at).toLocaleString() }}</template></div>
          </div>
          <NButton size="small" tertiary type="error" @click="revoke(t.id)">撤销</NButton>
        </div>
        <div class="row" style="flex-wrap:wrap;gap:4px;margin-top:8px">
          <NTag v-for="s in t.scopes" :key="s" size="small" :bordered="false">{{ s }}</NTag>
        </div>
      </div>
    </div>

    <h2 class="section-title">使用方法</h2>
    <div class="card stack">
      <p><b>1. 认证方式：</b>所有请求携带 <code>Authorization: Bearer &lt;KeyID&gt;.&lt;Secret&gt;</code>（即创建后显示的完整 Token）。写操作无需 CSRF 头，Token 通道自动豁免。</p>
      <p><b>2. 响应格式：</b><code>{ "data": ..., "error": null, "request_id": "..." }</code>；出错时 <code>error.code</code> / <code>error.message</code> 说明原因，非 2xx 状态码。</p>
      <p><b>3. 示例：</b>查看产品并下单（把 <code>$TOKEN</code> 换成完整 Token）：</p>
      <pre class="code-block">curl -H "Authorization: Bearer $TOKEN" https://你的域名/api/v1/products

curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"product_id":"<产品UUID>","billing_cycle":"monthly","quantity":1}' \
  https://你的域名/api/v1/orders

curl -X POST -H "Authorization: Bearer $TOKEN" \
  https://你的域名/api/v1/orders/<订单UUID>/pay</pre>
      <p><b>4. 完整端点清单（按权限）：</b></p>
      <div class="table-scroll">
        <div class="user-table api-table" style="min-width:640px">
          <div class="user-row api-head"><span>方法</span><span>路径</span><span>权限</span><span>说明</span></div>
          <div class="user-row" v-for="e in endpoints" :key="e.method + e.path">
            <span><NTag size="small" :bordered="false">{{ e.method }}</NTag></span>
            <span><code>{{ e.path }}</code></span>
            <span><code>{{ e.scope }}</code></span>
            <span>{{ e.desc }}</span>
          </div>
        </div>
      </div>
      <div class="security-note">API Token 与账号同等效力，请按需最小授权并定期轮换；泄露后立即在上方撤销。</div>
    </div>

    <h2 class="section-title">把本站作为上游（下游面板对接）</h2>
    <div class="card stack">
      <p>ShitIDC 不仅可以做「下游」去连接魔方财务，也可以反过来做「上游」：给下游（例如另一套魔方财务、其它售卖面板）发一个含 <code>product.read</code> 权限的 API Token，下游把接口地址指向本站即可：</p>
      <pre class="code-block">下游登录接口：POST https://你的域名/compat/magiccube/v1/login_api
  表单字段：account=任意标识  password=&lt;完整 API Token&gt;
  返回：{"status":200,"jwt":"<完整 API Token>",...}

下游商品接口：GET https://你的域名/compat/magiccube/v1/products
  请求头：Authorization: Bearer &lt;完整 API Token&gt;</pre>
      <p>下游只需把「API Key」填成这里的完整 Token（<code>shitidc_xxx.secret</code> 格式），登录后拿到的 jwt 就是同一个 Token，后续自动开通、查询都走本站标准接口。更多说明见 <a href="/docs/api.md" target="_blank">docs/api.md</a>。</p>
    </div>
  </div>
</template>
