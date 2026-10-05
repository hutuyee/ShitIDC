<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { NButton, NTag, useMessage } from 'naive-ui'
import { useRoute, useRouter } from 'vue-router'
import { api, dataOf } from '../api'

// 站内信（对齐魔方 CBAP ClientCare 插件的用户端消息中心）。
// /messages 列表；/messages/:id 详情（自动标记已读，支持上一篇 / 下一篇）。

const route = useRoute()
const router = useRouter()
const message = useMessage()

const list = ref<any[]>([])
const mail = ref<any | null>(null)
const busy = ref(false)
const detailID = computed(() => String(route.params.id || ''))
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')

async function loadList() {
  busy.value = true
  try {
    list.value = dataOf<any>(await api.get('/client-care/mails'))?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取消息失败')
  } finally {
    busy.value = false
  }
}
async function loadDetail() {
  busy.value = true
  mail.value = null
  try {
    mail.value = dataOf<any>(await api.get(`/client-care/mails/${detailID.value}`))
    await api.post(`/client-care/mails/${detailID.value}/read`)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '消息不存在或已被删除')
  } finally {
    busy.value = false
  }
}
function load() {
  if (detailID.value) loadDetail()
  else loadList()
}
onMounted(load)
watch(detailID, load)
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">消息中心</div>
        <h1>{{ detailID ? '消息详情' : '我的消息' }}</h1>
        <p>官方站内通知与客户关怀消息。</p>
      </div>
      <NButton v-if="detailID" secondary @click="router.push('/messages')">← 返回列表</NButton>
    </div>

    <template v-if="!detailID">
      <div v-if="!list.length" class="empty-box">{{ busy ? '加载中…' : '暂无消息。' }}</div>
      <div v-else class="stack">
        <div
          v-for="m in list"
          :key="m.public_id"
          class="card"
          style="display:flex;justify-content:space-between;gap:12px;align-items:center;cursor:pointer"
          @click="router.push('/messages/' + m.public_id)"
        >
          <div>
            <b>{{ m.title }}</b>
            <div class="muted" style="font-size:12px">{{ fmt(m.created_at) }}</div>
          </div>
          <NTag :type="m.read_at ? 'default' : 'warning'" size="small" round>{{ m.read_at ? '已读' : '未读' }}</NTag>
        </div>
      </div>
    </template>

    <template v-else>
      <div v-if="!mail" class="empty-box">{{ busy ? '加载中…' : '消息不存在或已被删除。' }}</div>
      <template v-else>
        <div class="card">
          <h2 style="margin:0 0 6px">{{ mail.title }}</h2>
          <div class="muted" style="font-size:12px;margin-bottom:12px">{{ fmt(mail.created_at) }}</div>
          <div class="mail-content" v-html="mail.content"></div>
        </div>
        <div class="row" style="gap:8px;margin-top:14px">
          <NButton secondary :disabled="!mail.prev_id" @click="router.push('/messages/' + mail.prev_id)">← 上一篇</NButton>
          <NButton secondary :disabled="!mail.next_id" @click="router.push('/messages/' + mail.next_id)">下一篇 →</NButton>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.mail-content { line-height: 1.7; word-break: break-word; }
.mail-content :deep(img) { max-width: 100%; }
</style>
