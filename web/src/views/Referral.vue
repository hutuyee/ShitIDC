<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const info = ref<any>(null)
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    info.value = dataOf(await api.get('/referral'))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取推广信息失败')
  } finally { loading.value = false }
}

function copy(text: string) {
  navigator.clipboard?.writeText(text)
  message.success('已复制')
}

const money = (cents: number, currency = 'CNY') => `${currency === 'CNY' ? '¥' : currency + ' '}${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const referralLink = computed(() => `${window.location.origin}/login?ref=${info.value?.code || ''}`)

onMounted(load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">推广返佣</div>
        <h1>邀请好友，赚取返佣</h1>
        <p v-if="info?.enabled !== false">把你的邀请链接发给朋友；TA 注册并支付订单后，你将按比例获得现金返佣，直接入余额钱包。</p>
        <p v-else class="muted">推广返佣当前未开启，请联系站长。</p>
      </div>
    </div>

    <div v-if="info" class="admin-two-col">
      <section class="panel stack admin-form-panel">
        <div class="panel-title-row"><div><h2>我的邀请码</h2><span>注册时填写此码即完成绑定</span></div></div>
        <div class="totp-secret"><code style="font-size:22px;letter-spacing:6px">{{ info.code }}</code><NButton size="tiny" tertiary @click="copy(info.code)">复制</NButton></div>
        <div class="panel-title-row"><div><h2>邀请链接</h2><span>自动带上邀请码</span></div></div>
        <div class="row" style="gap:8px">
          <NInput :value="referralLink" readonly />
          <NButton type="primary" @click="copy(referralLink)">复制链接</NButton>
        </div>
        <div class="account-kpis" style="margin-top:10px">
          <div><span>已邀请用户</span><strong>{{ info.invited }}</strong></div>
          <div><span>返佣比例</span><strong v-if="info.percent != null">{{ info.percent }}%</strong><strong v-else>—</strong></div>
          <div><span>累计返佣</span><strong>{{ money((info.commissions || []).reduce((s: number, x: any) => s + Number(x.amount_cents || 0), 0)) }}</strong></div>
        </div>
      </section>

      <section class="panel">
        <div class="panel-title-row"><div><h2>返佣记录</h2><span>{{ (info.commissions || []).length }} 条</span></div></div>
        <div v-if="(info.commissions || []).length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head"><span>时间</span><span>被邀请人</span><span>金额</span></div>
          <div v-for="c in info.commissions" :key="c.id" class="audit-row">
            <span class="muted">{{ fmt(c.created_at) }}</span>
            <span>{{ c.referee_email }}</span>
            <span><NTag type="success" size="small" round>+{{ money(c.amount_cents, c.currency) }}</NTag></span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有返佣记录。分享你的邀请链接，朋友消费后自动结算。</div>
      </section>
    </div>
  </div>
</template>
