<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 我的代金券（对齐魔方 IdcsmartVoucher 插件的前台）。
// 公开券可在这里领取，私有券由后台按用户发放；下单 / 续费 / 升降级时在购物车
// 或订单确认处填入券码使用，抵扣不超过应付金额、不找零、订单取消不返还。

const message = useMessage()
const mine = ref<any[]>([])
const claimable = ref<any[]>([])
const loading = ref(true)
const claiming = ref('')

const statusMeta: Record<string, { text: string; type: any }> = {
  enable: { text: '可使用', type: 'success' },
  disable: { text: '已停用', type: 'default' },
  wait_effect: { text: '未生效', type: 'warning' },
  expired: { text: '已过期', type: 'error' },
  used: { text: '已使用', type: 'default' },
}

const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string | null) => (v ? new Date(v).toLocaleDateString() : '长期有效')

async function load() {
  loading.value = true
  try {
    const [a, b] = await Promise.all([api.get('/vouchers/my'), api.get('/vouchers/claimable')])
    mine.value = dataOf<any[]>(a) || []
    claimable.value = dataOf<any[]>(b) || []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取代金券失败') }
  finally { loading.value = false }
}

async function claim(v: any) {
  claiming.value = v.id
  try {
    await api.post(`/vouchers/${v.id}/claim`)
    message.success(`已领取 ${v.code}，下单时可直接使用`)
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '领取失败') }
  finally { claiming.value = '' }
}

async function copy(code: string) {
  try {
    await navigator.clipboard?.writeText(code)
    message.success('券码已复制，结算时粘贴即可')
  } catch { message.warning('复制失败，请手动记录券码') }
}

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">财务中心</div>
        <h1>我的代金券</h1>
        <p>代金券是定额抵扣券：下单 / 续费 / 升降级时在结算处填码使用，抵扣金额不超过应付金额（不找零），订单取消不返还。</p>
      </div>
      <router-link to="/cart" class="soft-action">去购物车结算</router-link>
    </div>

    <div v-if="loading" class="empty-box">正在加载代金券…</div>
    <template v-else>
      <section class="panel" style="margin-bottom:16px">
        <div class="panel-title-row"><div><h2>可领取</h2><span>{{ claimable.length }} 张</span></div></div>
        <div v-if="claimable.length" class="stack">
          <div v-for="v in claimable" :key="v.id" class="card row" style="align-items:center;gap:12px;flex-wrap:wrap">
            <div style="min-width:180px">
              <b style="font-size:16px">{{ money(v.price_cents) }}</b>
              <div class="muted" style="font-size:12px">
                满 {{ money(v.min_amount_cents) }} 可用 · {{ fmt(v.start_at) }} 至 {{ fmt(v.end_at) }}
                <template v-if="v.remaining >= 0"> · 剩余 {{ v.remaining }} 张</template>
              </div>
            </div>
            <span class="muted" style="flex:1">{{ v.notes || '—' }}</span>
            <NButton type="primary" size="small" :loading="claiming === v.id" @click="claim(v)">领取</NButton>
          </div>
        </div>
        <div v-else class="empty-box">当前没有可领取的公开代金券。</div>
      </section>

      <section class="panel">
        <div class="panel-title-row"><div><h2>我的代金券</h2><span>{{ mine.length }} 张</span></div></div>
        <div v-if="mine.length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head"><span>券码</span><span>面额</span><span>使用门槛</span><span>有效期</span><span>来源</span><span>状态 / 操作</span></div>
          <div v-for="v in mine" :key="v.id" class="audit-row">
            <span><b>{{ v.code }}</b><small>{{ v.notes || '—' }}</small></span>
            <span>{{ money(v.price_cents) }}</span>
            <span>满 {{ money(v.min_amount_cents) }} 可用</span>
            <span class="muted">{{ fmt(v.start_at) }} 至 {{ fmt(v.end_at) }}</span>
            <span>{{ v.source === 'claim' ? '领取' : '发放' }}</span>
            <span>
              <NTag :type="statusMeta[v.status]?.type || 'default'" size="tiny" round>{{ statusMeta[v.status]?.text || v.status }}</NTag>
              <small>
                <NButton v-if="v.status === 'enable' && !v.used" size="tiny" tertiary @click="copy(v.code)">复制券码</NButton>
                <template v-else-if="v.used && v.order_id">订单 {{ String(v.order_id).slice(0, 8) }}…</template>
              </small>
            </span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有代金券，去上面看看有没有可以领取的公开券。</div>
      </section>
    </template>
  </div>
</template>
