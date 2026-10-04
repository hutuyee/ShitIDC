<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NModal, NSelect, NSwitch, NTag, useDialog, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const dialog = useDialog()
const webhooks = ref<any[]>([])
const loading = ref(false)
const busy = ref('')

// ---- create/edit form ----
const formOpen = ref(false)
const editing = ref<any>(null)
const form = ref({ name: '', url: '', events: [] as string[], allow_private: false, secret: '' })
const saving = ref(false)

// secret shown exactly once after creation
const createdSecret = ref('')
const createdName = ref('')

// ---- deliveries ----
const deliveriesOpen = ref(false)
const deliveriesTarget = ref<any>(null)
const deliveries = ref<any[]>([])

const eventOptions = [
  'user.registered', 'user.login', 'user.password_reset',
  'order.created', 'order.paid', 'order.cancelled', 'order.refunded',
  'invoice.paid', 'wallet.recharged', 'wallet.adjusted', 'payment.failed',
  'service.created', 'service.failed', 'service.renewed',
  'service.suspended', 'service.unsuspended', 'service.terminated',
  'ticket.created', 'ticket.replied',
].map(e => ({ label: e, value: e }))

const deliveryStatusType = (s: string) => ({ delivered: 'success', pending: 'info', failed: 'error' } as any)[s] || 'default'
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')

async function load() {
  loading.value = true
  try {
    webhooks.value = dataOf(await api.get('/admin/webhooks'))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取 Webhook 失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  form.value = { name: '', url: '', events: [], allow_private: false, secret: '' }
  formOpen.value = true
}

function openEdit(w: any) {
  editing.value = w
  form.value = { name: w.name, url: w.url, events: [...(w.events || [])], allow_private: false, secret: '' }
  formOpen.value = true
}

async function save() {
  if (!form.value.name.trim() || !form.value.url.trim()) { message.error('名称与 URL 必填'); return }
  saving.value = true
  try {
    if (editing.value) {
      await api.put(`/admin/webhooks/${editing.value.id}`, {
        name: form.value.name, url: form.value.url, events: form.value.events,
        secret: form.value.secret || undefined, allow_private: form.value.allow_private,
      })
      message.success('Webhook 已更新')
    } else {
      const d = dataOf<any>(await api.post('/admin/webhooks', {
        name: form.value.name, url: form.value.url, events: form.value.events, allow_private: form.value.allow_private,
      }))
      createdSecret.value = d.secret
      createdName.value = d.webhook?.name || form.value.name
      message.success('Webhook 已创建')
    }
    formOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

function remove(w: any) {
  dialog.warning({
    title: '删除 Webhook',
    content: `确认删除「${w.name}」？投递历史会一并删除。`,
    positiveText: '删除',
    negativeText: '取消',
    async onPositiveClick() {
      try {
        await api.delete(`/admin/webhooks/${w.id}`)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e?.response?.data?.error?.message || '删除失败')
      }
    },
  })
}

async function openDeliveries(w: any) {
  deliveriesTarget.value = w
  deliveriesOpen.value = true
  try {
    deliveries.value = dataOf(await api.get(`/admin/webhooks/${w.id}/deliveries`, { params: { limit: 100 } }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取投递记录失败')
  }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">Webhook 推送</div>
        <h1>出站 Webhook</h1>
        <p>核心业务事件（下单/支付/服务/工单等）实时推送到你的系统；请求带 HMAC-SHA256 签名（X-ShitIDC-Signature: t=&lt;unix&gt;,v1=&lt;hex&gt;），经队列异步投递并自动重试。</p>
      </div>
      <NButton type="primary" @click="openCreate">＋ 新建 Webhook</NButton>
    </div>

    <section class="panel">
      <div v-if="webhooks.length" class="table-scroll"><div class="user-table">
        <div class="user-row user-head"><span>名称</span><span>URL</span><span>订阅事件</span><span>状态</span><span>最近投递</span><span>操作</span></div>
        <div v-for="w in webhooks" :key="w.id" class="user-row">
          <span><b>{{ w.name }}</b><small class="muted">{{ w.id.slice(0, 8) }}</small></span>
          <span class="muted user-email">{{ w.url }}</span>
          <span class="muted">{{ w.events?.length ? w.events.join('、') : '全部事件' }}</span>
          <span><NTag size="small" :type="w.active ? 'success' : 'default'">{{ w.active ? '启用' : '停用' }}</NTag></span>
          <span class="muted">{{ fmt(w.last_delivery_at) }}
            <NTag v-if="w.last_delivery_ok === true" size="tiny" type="success">成功</NTag>
            <NTag v-else-if="w.last_delivery_ok === false" size="tiny" type="error">失败</NTag>
          </span>
          <span class="row" style="gap:6px;flex-wrap:wrap">
            <NButton size="tiny" secondary @click="openDeliveries(w)">投递记录</NButton>
            <NButton size="tiny" type="primary" secondary @click="openEdit(w)">编辑</NButton>
            <NButton size="tiny" type="error" secondary @click="remove(w)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有 Webhook，点击右上角「新建 Webhook」接入你的通知系统。</div>
    </section>

    <NModal v-model:show="formOpen" preset="card" :title="editing ? '编辑 Webhook' : '新建 Webhook'" style="width:min(560px,92vw)">
      <div class="stack">
        <NInput v-model:value="form.name" placeholder="名称，如：工单机器人" />
        <NInput v-model:value="form.url" placeholder="https://your-app.example.com/hooks/shitidc" />
        <NSelect v-model:value="form.events" multiple :options="eventOptions" placeholder="订阅事件（不选 = 全部事件）" />
        <label class="row" style="gap:8px;align-items:center" v-if="!editing">
          <NSwitch v-model:value="form.allow_private" />
          <span class="muted">允许推送到内网地址（仅自建内网系统需要，默认关闭）</span>
        </label>
        <label v-if="editing">
          <span class="muted" style="font-size:12px">签名密钥（留空保持不变；填写则替换为新值）</span>
          <NInput v-model:value="form.secret" placeholder="留空保持现有密钥" />
        </label>
        <div class="security-note">URL 不允许指向内网/回环地址（除非勾选允许）；事件按订阅过滤，空订阅 = 全部事件。请求头含 X-ShitIDC-Event / X-ShitIDC-Delivery / X-ShitIDC-Signature。</div>
        <NButton type="primary" block :loading="saving" @click="save">{{ editing ? '保存修改' : '创建 Webhook' }}</NButton>
      </div>
    </NModal>

    <NModal :show="createdSecret !== ''" preset="card" title="请立即保存签名密钥" style="width:min(520px,92vw)">
      <div class="stack">
        <div class="security-note">「{{ createdName }}」的签名密钥只显示这一次，用于在你这边校验 X-ShitIDC-Signature。关闭后无法再次查看，只能重置。</div>
        <NInput :value="createdSecret" readonly type="textarea" :autosize="{ minRows: 2 }" />
        <NButton type="primary" block @click="createdSecret = ''">我已保存</NButton>
      </div>
    </NModal>

    <NModal v-model:show="deliveriesOpen" preset="card" :title="`投递记录 · ${deliveriesTarget?.name || ''}`" style="width:min(760px,94vw)">
      <div v-if="deliveries.length" class="stack">
        <div v-for="d in deliveries" :key="d.id" class="panel stack" style="padding:10px 14px">
          <div class="row" style="justify-content:space-between;align-items:center">
            <b>{{ d.event }}</b>
            <span class="row" style="gap:8px;align-items:center">
              <NTag size="small" :type="deliveryStatusType(d.status)">{{ d.status }}</NTag>
              <span class="muted">尝试 {{ d.attempts }} 次{{ d.response_code ? ` · HTTP ${d.response_code}` : '' }}</span>
            </span>
          </div>
          <div class="muted" style="font-size:12px">{{ fmt(d.created_at) }}<template v-if="d.last_error"> · {{ d.last_error }}</template></div>
        </div>
      </div>
      <div v-else class="empty-box">暂无投递记录。</div>
    </NModal>
  </div>
</template>
