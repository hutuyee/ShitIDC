<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NModal, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const items = ref<any[]>([])
const loading = ref(false)
const busy = ref(false)
const status = ref('pending')

const rejectOpen = ref(false)
const rejectTarget = ref<any>(null)
const rejectReason = ref('')

const statusText: Record<string, string> = { pending: '待审核', approved: '已通过', rejected: '已驳回' }
const statusType = (s: string) => ({ pending: 'warning', approved: 'success', rejected: 'error' } as any)[s] || 'default'
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')

async function load() {
  loading.value = true
  try {
    items.value = dataOf(await api.get('/admin/certifications', { params: { status: status.value, limit: 200 } }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取实名记录失败')
  } finally { loading.value = false }
}

async function approve(it: any) {
  busy.value = true
  try {
    await api.post(`/admin/certifications/${it.id}/review`, { approve: true })
    message.success('已通过实名认证')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  } finally { busy.value = false }
}

function openReject(it: any) {
  rejectTarget.value = it
  rejectReason.value = ''
  rejectOpen.value = true
}

async function doReject() {
  if (!rejectReason.value.trim()) { message.error('驳回必须填写原因'); return }
  busy.value = true
  try {
    await api.post(`/admin/certifications/${rejectTarget.value.id}/review`, { approve: false, reason: rejectReason.value.trim() })
    message.success('已驳回')
    rejectOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '操作失败')
  } finally { busy.value = false }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">实名认证</div><h1>实名审核</h1><p>对应魔方 IdcsmartCertification 插件：手动通道或核验未通过时进入人工审核，通过后用户即可下单需要实名的商品；证件信息始终脱敏展示。</p></div><NButton :loading="loading" secondary @click="load">刷新</NButton></div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>实名记录</h2><span>{{ items.length }} 条</span></div>
        <div style="display:flex;gap:6px">
          <NButton v-for="s in ['pending', 'approved', 'rejected', '']" :key="s || 'all'" size="small" secondary :type="status === s ? 'primary' : 'default'" @click="status = s; load()">{{ s ? statusText[s] : '全部' }}</NButton>
        </div>
      </div>
      <div v-if="items.length" class="table-scroll"><div class="audit-table">
        <div class="audit-row audit-head"><span>用户</span><span>姓名</span><span>证件号</span><span>通道</span><span>状态</span><span>提交时间</span><span>操作</span></div>
        <div v-for="it in items" :key="it.id" class="audit-row">
          <span class="muted">{{ it.user_email }}</span>
          <span>{{ it.real_name_masked || '—' }}</span>
          <span class="mono">{{ it.id_number_masked }}</span>
          <span class="muted">{{ it.verified_by || it.provider || '人工' }}</span>
          <span><NTag :type="statusType(it.status)" size="small" round>{{ statusText[it.status] || it.status }}</NTag></span>
          <span class="muted">{{ fmt(it.submitted_at) }}</span>
          <span style="display:flex;gap:6px">
            <template v-if="it.status === 'pending'">
              <NButton size="tiny" tertiary type="primary" :loading="busy" @click="approve(it)">通过</NButton>
              <NButton size="tiny" tertiary type="error" :loading="busy" @click="openReject(it)">驳回</NButton>
            </template>
            <span v-else class="muted">—</span>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">没有符合条件的实名记录。</div>
    </section>

    <NModal v-model:show="rejectOpen" preset="card" title="驳回实名认证" style="width:min(440px,92vw)">
      <div class="stack" v-if="rejectTarget">
        <p class="muted" style="margin:0">用户 {{ rejectTarget.user_email }}（{{ rejectTarget.real_name_masked }}）；驳回原因会展示给用户，请写明需要修改的内容。</p>
        <NInput v-model:value="rejectReason" type="textarea" :rows="3" placeholder="驳回原因（必填）" />
        <NButton type="error" block :loading="busy" @click="doReject">确认驳回</NButton>
      </div>
    </NModal>
  </div>
</template>
