<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NInputNumber, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import { useRouter } from 'vue-router'

// 产品自助转移（对齐魔方主程序附属插件 product_divert）。
// 转出方按手机号 / 邮箱选人发起转出（费用 > 0 时先支付转出费用）；
// 接收方在下方列表「接收」（费用 > 0 时再支付转入费用），双方费用到账后产品立即迁移。

const message = useMessage()
const router = useRouter()

const config = ref<any>({ is_open: false, validity_period_days: 0, push_cost_cents: 0, pull_cost_cents: 0, protection_period_days: 0 })
const services = ref<any[]>([])
const records = ref<any[]>([])
const total = ref(0)
const statusFilter = ref<number | null>(null)
const busy = ref(false)

const pushOpen = ref(false)
const pushService = ref<string | null>(null)
const targetName = ref('')
const target = ref<{ id: number; account: string } | null>(null)
const pushing = ref(false)

const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string | null) => (v ? new Date(v).toLocaleString() : '—')
const statusText: Record<number, string> = { 1: '待接收', 2: '已完成', 3: '已关闭', 4: '已拒绝' }
const statusType = (s: number) => ({ 1: 'warning', 2: 'success' } as any)[s] || 'default'

const serviceOptions = computed(() =>
  services.value.filter(s => s.eligible).map(s => ({
    label: s.ip ? `${s.name} · ${s.ip}` : s.name,
    value: s.id,
  })),
)

async function load() {
  busy.value = true
  try {
    config.value = dataOf<any>(await api.get('/product-divert/config'))
  } catch { /* 未登录等场景静默 */ }
  try {
    services.value = dataOf<any>(await api.get('/product-divert/services'))?.list || []
  } catch { services.value = [] }
  await loadRecords()
  busy.value = false
}

async function loadRecords() {
  try {
    const params: any = { limit: 50 }
    if (statusFilter.value) params.status = statusFilter.value
    const d = dataOf<any>(await api.get('/product-divert', { params }))
    records.value = d?.list || []
    total.value = d?.count || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取转移记录失败')
  }
}

async function lookup() {
  target.value = null
  const name = targetName.value.trim()
  if (!name) { message.warning('请输入接收方的手机号或邮箱'); return }
  try {
    target.value = dataOf<any>(await api.post('/product-divert/lookup', { name }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '没有找到该用户')
  }
}

async function submitPush() {
  if (!pushService.value || !target.value) { message.error('请选择产品并确认接收方'); return }
  pushing.value = true
  try {
    const d = dataOf<any>(await api.post('/product-divert', { service_id: pushService.value, to_uid: target.value.id }))
    message.success(d.push_cost_cents > 0 && !d.push_paid_at
      ? '转出已发起，请到订单中心支付转出费用；支付后对方会收到转入通知'
      : '转出已发起，对方会收到转入通知')
    pushOpen.value = false
    targetName.value = ''; target.value = null; pushService.value = null
    await loadRecords()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '发起转出失败')
  } finally {
    pushing.value = false
  }
}

async function act(r: any, action: 'accept' | 'reject' | 'cancel' | 'verify', confirmText?: string) {
  if (confirmText && !window.confirm(confirmText)) return
  try {
    const d = dataOf<any>(await api.post(`/product-divert/${r.public_id}/${action}`))
    if (action === 'accept' && d.pull_cost_cents > 0 && !d.pull_paid_at) {
      message.success('已接受，请到订单中心支付转入费用；支付后产品立刻转移')
    } else {
      message.success('操作成功')
    }
    await loadRecords()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  }
}

function goPay(orderID: string) {
  if (!orderID) return
  router.push('/orders')
  message.info('请在订单中心完成该笔费用订单的支付')
}

// 行操作按角色与状态展开（对齐插件 pushpulllist 模板的分支）。
const actions = (r: any) => {
  const list: { label: string; type?: 'primary' | 'error'; run: () => void }[] = []
  if (r.status === 1 && r.role === 'push') {
    if (r.push_order_id && !r.push_paid_at) list.push({ label: '支付转出费用', type: 'primary', run: () => goPay(r.push_order_id) })
    list.push({ label: '取消', run: () => act(r, 'cancel', '取消转移将导致对方无法接收，已支付的费用不自动退还。确认取消？') })
  } else if (r.status === 1 && r.role === 'pull') {
    if (r.push_paid_at && r.pull_cost_cents > 0 && !r.pull_order_id) list.push({ label: '接收', type: 'primary', run: () => act(r, 'accept') })
    if (r.pull_order_id && !r.pull_paid_at) {
      list.push({ label: '支付转入费用', type: 'primary', run: () => goPay(r.pull_order_id) })
      list.push({ label: '拒绝', run: () => act(r, 'reject', '拒绝后将无法接收对方转移的产品。确认拒绝？') })
    } else if (r.push_paid_at && r.pull_cost_cents === 0 && !r.pull_paid_at) {
      list.push({ label: '接收', type: 'primary', run: () => act(r, 'accept') })
    }
    if (r.push_paid_at && r.pull_paid_at) list.push({ label: '手动检测', run: () => act(r, 'verify') })
  }
  return list
}

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">增值服务</div>
        <h1>产品转移</h1>
        <p>把名下产品转移给其他用户：转出方发起并支付转出费用，接收方确认并支付转入费用后，产品立即迁移到对方账户。订单与账单记录不随产品迁移。</p>
      </div>
      <NButton type="primary" :disabled="!config.is_open" @click="pushOpen = true">发起转出</NButton>
    </div>

    <div v-if="!config.is_open" class="empty-box">产品自助转移暂未开放，如需转移请联系客服。</div>

    <div class="stack">
      <div class="card" style="display:flex;gap:18px;flex-wrap:wrap;font-size:13px" v-if="config.is_open">
        <span class="muted">转出费用：<b>{{ money(config.push_cost_cents) }}</b></span>
        <span class="muted">转入费用：<b>{{ money(config.pull_cost_cents) }}</b></span>
        <span class="muted" v-if="config.validity_period_days > 0">转出有效期：{{ config.validity_period_days }} 天（超时未接收自动关闭）</span>
        <span class="muted" v-if="config.protection_period_days > 0">订购保护期：{{ config.protection_period_days }} 天</span>
      </div>
    </div>

    <h2 style="margin-top:26px">转移记录 <span class="muted" style="font-size:13px">共 {{ total }} 条</span></h2>
    <div class="table-scroll"><div class="order-table">
      <div class="order-row user-head">
        <span>产品</span><span>对方</span><span>费用（我方）</span><span>发起时间</span><span>完成时间</span><span>类型</span><span>状态</span><span>操作</span>
      </div>
      <div v-for="r in records" :key="r.public_id" class="order-row">
        <span><b>{{ r.product_name }}</b><small class="muted"> {{ String(r.service_id || '').slice(0, 8) }}</small></span>
        <span class="muted">{{ r.role === 'push' ? r.pull_email : r.push_email }}</span>
        <span>{{ money(r.role === 'push' ? r.push_cost_cents : r.pull_cost_cents) }}</span>
        <span class="muted">{{ fmt(r.created_at) }}</span>
        <span class="muted">{{ fmt(r.end_at) }}</span>
        <span><NTag size="tiny" round>{{ r.role === 'push' ? '转出' : '转入' }}</NTag></span>
        <span><NTag :type="statusType(r.status)" size="tiny" round>{{ statusText[r.status] || r.status }}</NTag></span>
        <span class="row" style="gap:6px;flex-wrap:wrap">
          <NButton v-for="a2 in actions(r)" :key="a2.label" size="tiny" :type="a2.type || 'default'" @click="a2.run()">{{ a2.label }}</NButton>
          <span v-if="!actions(r).length" class="muted">—</span>
        </span>
      </div>
      <div v-if="!records.length" class="empty-box">还没有转移记录。</div>
    </div></div>

    <NModal v-model:show="pushOpen" preset="card" title="产品转出" style="width:min(560px,94vw)">
      <div class="stack">
        <div>
          <b class="muted" style="font-size:12px">选择要转出的产品</b>
          <NSelect v-model:value="pushService" :options="serviceOptions" placeholder="选择产品" style="margin-top:6px" />
        </div>
        <div>
          <b class="muted" style="font-size:12px">接收方（手机号或邮箱）</b>
          <div class="row" style="gap:8px;margin-top:6px">
            <NInput v-model:value="targetName" placeholder="接收方需在平台填写过手机号或邮箱" @keyup.enter="lookup" />
            <NButton secondary @click="lookup">查找</NButton>
          </div>
          <p v-if="target" style="margin:8px 0 0;color:#d97706">接收方账号：{{ target.account }}（已脱敏，请核对）</p>
        </div>
        <p class="muted" style="font-size:12px;margin:0">
          转出费用 {{ money(config.push_cost_cents) }}，接收方需支付转入费用 {{ money(config.pull_cost_cents) }}。
          支付后，产品会立刻转移到对方账户中；已支付的费用在对方拒绝或超时关闭时不自动退还。
        </p>
        <div class="row" style="gap:8px">
          <NButton type="primary" :loading="pushing" :disabled="!target" @click="submitPush">确认转出</NButton>
          <NButton secondary @click="pushOpen = false">取消</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>
