<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const items = ref<any[]>([])
const query = ref('')
const busy = ref(false)

async function load() {
  busy.value = true
  try { items.value = dataOf(await api.get('/admin/expired-ip-logs', { params: { query: query.value.trim() } })) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取到期IP记录失败') }
  finally { busy.value = false }
}

const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">附属插件</div><h1>到期IP记录</h1><p>服务终止成功后留档当次 IP 与开通时间，对齐魔方「到期产品删除IP记录」插件；记录只增不改，可按服务 / 邮箱 / IP / 产品搜索。</p></div></div>

    <section class="panel">
      <div class="panel-title-row">
        <div><h2>IP 留档</h2><span>{{ items.length }} 条</span></div>
        <div class="row" style="gap:8px">
          <NInput v-model:value="query" placeholder="搜索服务 / 邮箱 / IP / 产品" style="width:280px" clearable @keyup.enter="load" />
          <NButton tertiary :loading="busy" @click="load">查询</NButton>
        </div>
      </div>
      <div v-if="items.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>记录时间</span><span>服务</span><span>用户</span><span>产品</span><span>主IP</span><span>分配IP</span><span>开通时间</span></div>
        <div v-for="x in items" :key="x.id" class="audit-row">
          <span class="muted">{{ fmt(x.created_at) }}</span>
          <span><b>{{ (x.service_id || '').slice(0, 8) }}</b></span>
          <span>{{ x.user_uid }}<small class="ua">{{ x.user_email }}</small></span>
          <span>{{ x.product_name || '—' }}</span>
          <span>{{ x.dedicated_ip || '—' }}</span>
          <span class="muted">{{ x.assigned_ips || '—' }}</span>
          <span class="muted">{{ fmt(x.service_created_at) }}</span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有记录：服务终止成功后会自动留档。</div>
    </section>
  </div>
</template>
