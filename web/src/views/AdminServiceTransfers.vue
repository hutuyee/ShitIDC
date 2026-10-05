<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 产品转移记录（对齐魔方 CBAP HostTransfer 插件）。
// 展示每次迁移的产品 / 双方用户 / 操作人 / 时间 / 备注；
// 发起转移在「服务管理」或「用户详情 → 机器」里执行。

const message = useMessage()
const items = ref<any[]>([])
const total = ref(0)
const keyword = ref('')
const busy = ref(false)

const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
const shortID = (id?: string) => (id ? String(id).slice(0, 8) : '—')

async function load() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/service-transfers', { params: { keyword: keyword.value.trim(), limit: 100 } }))
    items.value = d?.list || []
    total.value = d?.count || 0
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取转移记录失败')
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>产品转移</h1>
        <p>对齐魔方「产品转移」插件：此处展示产品转移记录；如需转移产品，请前往「服务管理」或「用户详情 → 机器」执行转移操作。同一订单的关联产品会一起迁移，订单信息不迁移。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary :loading="busy" @click="load">刷新</NButton>
      </div>
    </div>

    <section class="panel">
      <div class="users-toolbar">
        <NInput v-model:value="keyword" clearable placeholder="商品名称 / 产品ID / 用户邮箱 / 备注" style="max-width:320px" @keyup.enter="load" />
        <NButton type="primary" :loading="busy" @click="load">查询</NButton>
        <span class="muted" style="align-self:center">共 {{ total }} 条</span>
      </div>

      <div v-if="items.length" class="table-scroll"><div class="user-table">
        <div class="user-row user-head transfer-row">
          <span>产品ID</span><span>商品名称</span><span>商品标识</span><span>原始用户</span><span>目标用户</span><span>迁移时间</span><span>操作人</span><span>备注</span>
        </div>
        <div v-for="t in items" :key="t.id" class="user-row transfer-row">
          <span class="muted">{{ shortID(t.service_id) }}</span>
          <span><b>{{ t.product_name }}</b></span>
          <span class="muted">{{ t.product_ref || '—' }}</span>
          <span class="muted">{{ t.from_email }}<small>#{{ t.from_uid }}</small></span>
          <span class="muted">{{ t.to_email }}<small>#{{ t.to_uid }}</small></span>
          <span class="muted">{{ fmt(t.created_at) }}</span>
          <span class="muted">{{ t.operator || '—' }}</span>
          <span class="muted">{{ t.remark || '—' }}</span>
        </div>
      </div></div>
      <div v-else class="empty-box">没有匹配的转移记录。</div>
    </section>
  </div>
</template>

<style scoped>
.transfer-row { grid-template-columns: 90px minmax(160px, 1.2fr) 120px minmax(150px, .9fr) minmax(150px, .9fr) 140px minmax(140px, .8fr) minmax(120px, 1fr); }
</style>
