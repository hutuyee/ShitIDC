<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const invoices = ref<any[]>([])
const detail = ref<any | null>(null)
const loadingDetail = ref(false)

const statusType = (s: string) => ({ paid: 'success', unpaid: 'warning', void: 'default', refunded: 'info' } as any)[s] || 'default'
const statusText = (s: string) => ({ paid: '已支付', unpaid: '未支付', void: '已作废', refunded: '已退款' } as any)[s] || s
const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')

async function load() { invoices.value = dataOf(await api.get('/invoices')) }

async function open(i: any) {
  loadingDetail.value = true
  detail.value = null
  try {
    detail.value = dataOf(await api.get(`/invoices/${i.id}`))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取账单详情失败')
  } finally { loadingDetail.value = false }
}

onMounted(load)
</script>
<template>
  <h1 class="page-title">账单</h1>
  <div class="stack">
    <div class="card row" v-for="i in invoices" :key="i.id" style="justify-content:space-between;cursor:pointer" @click="open(i)">
      <div><b>{{ i.id.slice(0, 8) }}</b><div class="muted">订单 {{ i.order_id.slice(0, 8) }} · 开具 {{ fmt(i.created_at) }} · 到期 {{ fmt(i.due_at) }}</div></div>
      <div class="row" style="gap:10px;align-items:center">
        <span class="money">{{ money(i.total_cents, i.currency) }}</span>
        <NTag :type="statusType(i.status)" round>{{ statusText(i.status) }}</NTag>
      </div>
    </div>
    <div v-if="!invoices.length" class="empty-box">暂无账单。</div>
  </div>

  <Teleport to="body">
    <div v-if="detail || loadingDetail" class="inv-mask" @click.self="detail = null; loadingDetail = false">
      <div class="inv-card">
        <template v-if="loadingDetail"><div class="empty-box">加载中…</div></template>
        <template v-else-if="detail">
          <div class="inv-head">
            <div><div class="eyebrow">账单详情</div><h2>#{{ detail.id.slice(0, 8) }}</h2></div>
            <NTag :type="statusType(detail.status)" round>{{ statusText(detail.status) }}</NTag>
          </div>
          <div class="inv-grid">
            <div><span>关联订单</span><b>{{ detail.order_id.slice(0, 8) }}</b></div>
            <div><span>订单状态</span><b>{{ detail.order_status }}</b></div>
            <div><span>开具时间</span><b>{{ fmt(detail.created_at) }}</b></div>
            <div><span>支付期限</span><b>{{ fmt(detail.due_at) }}</b></div>
            <div v-if="detail.paid_at"><span>支付时间</span><b>{{ fmt(detail.paid_at) }}</b></div>
          </div>
          <div class="inv-items">
            <div class="inv-item inv-head-row"><span>项目</span><span>金额</span></div>
            <div class="inv-item" v-for="(it, idx) in detail.items" :key="idx"><span>{{ it.description }}</span><span>{{ money(it.amount_cents, detail.currency) }}</span></div>
            <div class="inv-item inv-total"><span>合计</span><span>{{ money(detail.total_cents, detail.currency) }}</span></div>
          </div>
          <div class="inv-actions">
            <router-link v-if="detail.status === 'unpaid'" :to="`/orders`"><NButton type="primary">去订单页支付</NButton></router-link>
            <NButton v-else tertiary @click="detail = null">关闭</NButton>
          </div>
        </template>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.inv-mask { position: fixed; inset: 0; background: rgba(10, 12, 20, .5); display: flex; align-items: center; justify-content: center; z-index: 1000; padding: 16px; }
.inv-card { background: var(--panel, #fff); border-radius: 16px; padding: 22px; width: min(480px, 94vw); max-height: 86vh; overflow: auto; }
.inv-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px; }
.inv-head h2 { margin: 2px 0 0; }
.inv-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-bottom: 16px; }
.inv-grid span { display: block; color: var(--muted, #8a93a6); font-size: 12px; }
.inv-grid b { font-size: 14px; }
.inv-items { border-top: 1px solid var(--border, #e5e8f0); padding-top: 10px; }
.inv-item { display: flex; justify-content: space-between; padding: 8px 4px; border-bottom: 1px dashed var(--border, #e5e8f0); }
.inv-head-row { color: var(--muted, #8a93a6); font-size: 12px; }
.inv-total { font-weight: 700; border-bottom: none; }
.inv-actions { margin-top: 16px; display: flex; justify-content: flex-end; gap: 8px; }
</style>
