<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NRadioButton, NRadioGroup, NSelect, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 到期账单处理（对齐魔方主程序附属插件 expired_auto_delete_bill）。
// 上半是插件设置页的唯一字段：产品到期（终止）后未支付续费账单的处理方式
// （无 / 直接删除 / 标记取消）；下半是插件的「账单处理记录」。
// 站内账目不物理删除：删除与取消的最终账面状态都是 void（作废），动作记入日志。

const message = useMessage()
const action = ref<string>('')
const saving = ref(false)
const records = ref<any[]>([])
const total = ref(0)
const keyword = ref('')
const busy = ref(false)

const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const actionText: Record<string, string> = { delete: '直接删除', cancel: '标记取消' }

async function loadConfig() {
  try { action.value = dataOf<any>(await api.get('/admin/expired-bill-action'))?.expired_bill_action || '' }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取配置失败') }
}
async function saveConfig() {
  saving.value = true
  try {
    await api.put('/admin/expired-bill-action', { expired_bill_action: action.value })
    message.success('配置已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally { saving.value = false }
}

async function load() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/expired-bill-logs', { params: { keyword: keyword.value.trim(), limit: 100 } }))
    records.value = d?.list || []
    total.value = d?.count || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取处理记录失败')
  } finally { busy.value = false }
}

onMounted(() => { loadConfig(); load() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>到期账单处理</h1>
        <p>对齐魔方「到期账单自动删除」插件：产品被终止时按下方配置处理其未支付的续费订单与账单并留档。站内账目不物理删除，删除与取消的最终账面状态都是「已作废」，配置的动作记入处理记录。</p>
      </div>
      <NButton secondary :loading="busy" @click="load">刷新</NButton>
    </div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>处理方式</h2><span>未配置（无）时不做任何处理，保持既有行为</span></div></div>
      <div class="stack" style="gap:14px;max-width:640px">
        <NRadioGroup v-model:value="action">
          <NRadioButton value="">无</NRadioButton>
          <NRadioButton value="delete">直接删除</NRadioButton>
          <NRadioButton value="cancel">标记取消</NRadioButton>
        </NRadioGroup>
        <p class="muted" style="font-size:12px;margin:0">
          「直接删除」与「标记取消」在站内同为作废该服务名下未支付的续费订单 / 账单（插件为物理删除，本站为保留账目痕迹的等价实现）；已支付的账单不受影响。
        </p>
        <div><NButton type="primary" :loading="saving" @click="saveConfig">保存更改</NButton></div>
      </div>
    </section>

    <section class="panel">
      <div class="panel-title-row"><div><h2>账单处理记录</h2><span>关键词匹配账单号 / 产品 / IP / 用户邮箱</span></div></div>
      <div class="users-toolbar">
        <NInput v-model:value="keyword" clearable placeholder="账单号 / 产品 / IP / 邮箱" style="max-width:280px" @keyup.enter="load" />
        <NButton type="primary" :loading="busy" @click="load">查询</NButton>
        <span class="muted" style="align-self:center">共 {{ total }} 条</span>
      </div>
      <div v-if="records.length" class="table-scroll"><div class="user-table">
        <div class="user-row expired-bill-row">
          <span>账单号</span><span>状态</span><span>处理方式</span><span>处理时间</span><span>关联产品</span><span>用户</span>
        </div>
        <div v-for="r in records" :key="r.id" class="user-row expired-bill-row">
          <span class="muted">#{{ String(r.invoice_id).slice(0, 8) }}</span>
          <span><NTag type="default" size="tiny" round>已作废</NTag></span>
          <span class="muted">{{ actionText[r.action] || r.action }}</span>
          <span class="muted">{{ fmt(r.created_at) }}</span>
          <span><b>{{ r.product_name }}</b><small class="muted" v-if="r.dedicated_ip"> · {{ r.dedicated_ip }}</small><small class="muted"> {{ String(r.service_id || '').slice(0, 8) }}</small></span>
          <span class="muted">{{ r.user_email }}<small>#{{ r.user_uid }}</small></span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有处理记录。配置处理方式后，产品终止时会自动了结其未支付的续费账单并在这里留档。</div>
    </section>
  </div>
</template>

<style scoped>
.expired-bill-row { grid-template-columns: 120px 90px 100px 160px minmax(180px, 1.3fr) minmax(160px, .9fr); }
</style>
