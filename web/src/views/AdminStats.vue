<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const stats = ref<any>(null)

const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`

async function load() {
  try { stats.value = dataOf(await api.get('/admin/statistics')) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取统计失败') }
}

const chartPoints = computed(() => {
  const series = stats.value?.revenue_series || []
  const values = series.map((x: any) => Number(x.cents || 0))
  const max = Math.max(...values, 1)
  return values.map((v: number, i: number) => `${(i / Math.max(values.length - 1, 1)) * 100},${88 - (v / max) * 78}`).join(' ')
})
const statusText: Record<string, string> = {
  pending: '待处理', unpaid: '未支付', paid: '已支付', processing: '开通中', completed: '已完成', cancelled: '已取消', refunded: '已退款',
}
onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">财务统计</div><h1>经营数据</h1><p>营收、用户、服务与订单概览（金额为支付币种原始数值，未做汇率换算）。</p></div></div>

    <div v-if="stats" class="admin-stat-grid">
      <div class="panel"><span class="admin-stat-icon">📈</span><b>今日营收</b><p>{{ money(stats.revenue_today, stats.base_currency) }}</p></div>
      <div class="panel"><span class="admin-stat-icon">📅</span><b>7 日营收</b><p>{{ money(stats.revenue_7d, stats.base_currency) }}</p></div>
      <div class="panel"><span class="admin-stat-icon">💰</span><b>30 日营收</b><p>{{ money(stats.revenue_30d, stats.base_currency) }}</p></div>
      <div class="panel"><span class="admin-stat-icon">↩</span><b>30 日退款</b><p>{{ money(stats.refunds_30d, stats.base_currency) }}</p></div>
      <div class="panel"><span class="admin-stat-icon">⬆</span><b>30 日充值</b><p>{{ money(stats.recharge_30d, stats.base_currency) }}</p></div>
      <div class="panel"><span class="admin-stat-icon">🔁</span><b>MRR 估算</b><p>{{ money(stats.mrr_estimate, stats.base_currency) }}</p></div>
      <div class="panel"><span class="admin-stat-icon">👤</span><b>用户总数</b><p>{{ stats.total_users }}（30 天新增 {{ stats.new_users_30d }}）</p></div>
      <div class="panel"><span class="admin-stat-icon">▣</span><b>运行中服务</b><p>{{ stats.active_services }}</p></div>
    </div>

    <div class="admin-two-col" v-if="stats">
      <section class="panel">
        <div class="panel-title-row"><div><h2>30 天营收趋势</h2><span>按日汇总</span></div></div>
        <div class="chart-block" style="height:220px"><svg viewBox="0 0 100 100" preserveAspectRatio="none" style="width:100%;height:100%"><line x1="0" y1="88" x2="100" y2="88" class="chart-axis"/><polyline :points="chartPoints" class="chart-line"/></svg></div>
      </section>
      <section class="panel">
        <div class="panel-title-row"><div><h2>热销商品</h2><span>近 30 天</span></div></div>
        <div v-if="stats.top_products.length" class="mini-list">
          <div v-for="(p, i) in stats.top_products" :key="p.product" class="mini-row"><span class="muted">#{{ i + 1 }}</span><b>{{ p.product }}</b><span class="grow"></span><small class="muted">{{ p.orders }} 单</small><strong>{{ money(p.cents, stats.base_currency) }}</strong></div>
        </div>
        <div v-else class="empty-box">暂无销售数据。</div>
        <div class="panel-title-row" style="margin-top:14px"><div><h2>订单状态分布</h2></div></div>
        <div class="row" style="gap:6px;flex-wrap:wrap">
          <NTag v-for="(n, s) in stats.orders_by_status" :key="s" round size="small">{{ statusText[String(s)] || s }}: {{ n }}</NTag>
        </div>
      </section>
    </div>
  </div>
</template>
