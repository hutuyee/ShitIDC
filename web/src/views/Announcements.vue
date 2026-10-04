<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NTag } from 'naive-ui'
import { api, dataOf } from '../api'

// 公告列表独立成页：仪表板那块小面板只放最新几条做入口，
// 正文全部在这里展开，长公告也不会把首页撑变形。

const items = ref<any[]>([])
const loading = ref(false)
const keyword = ref('')
const onlyPinned = ref(false)

async function load() {
  loading.value = true
  try { items.value = dataOf(await api.get('/announcements', { params: { limit: 100 } })) }
  catch { items.value = [] }
  finally { loading.value = false }
}

const filtered = computed(() => {
  let list = items.value
  if (onlyPinned.value) list = list.filter(a => a.pinned)
  const k = keyword.value.trim().toLowerCase()
  if (k) list = list.filter(a => String(a.title || '').toLowerCase().includes(k) || String(a.body || '').toLowerCase().includes(k))
  return list
})

const pinnedCount = computed(() => items.value.filter(a => a.pinned).length)

// 列表里给一段摘要：太长的话整页都在滚动。
function summary(a: any) {
  const body = String(a.body || '').replace(/\s+/g, ' ').trim()
  return body.length > 160 ? body.slice(0, 160) + '…' : body
}

const fmt = (v?: string) => (v ? new Date(v).toLocaleString('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }) : '—')

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <div class="eyebrow">平台动态</div>
        <h1>公告</h1>
        <p>维护通知、功能更新与平台动态。点击任意一条查看完整内容。</p>
      </div>
      <router-link to="/"><NButton tertiary>返回仪表板</NButton></router-link>
    </div>

    <section class="panel">
      <div class="ann-toolbar">
        <NInput v-model:value="keyword" placeholder="搜索公告标题或内容" clearable style="max-width:320px" />
        <NButton :type="onlyPinned ? 'primary' : 'default'" tertiary @click="onlyPinned = !onlyPinned">
          只看置顶{{ pinnedCount ? `（${pinnedCount}）` : '' }}
        </NButton>
        <span class="grow"></span>
        <span class="muted">共 {{ filtered.length }} 条</span>
      </div>

      <div v-if="loading" class="empty-box">加载中…</div>
      <div v-else-if="!filtered.length" class="empty-box">
        {{ items.length ? '没有匹配的公告。' : '暂时还没有公告。' }}
      </div>
      <div v-else class="ann-feed">
        <!-- 整卡可点：进详情页看全文 -->
        <router-link
          v-for="a in filtered"
          :key="a.id"
          :to="`/announcements/${a.id}`"
          class="ann-card"
        >
          <div class="ann-card-head">
            <h2>{{ a.title }}</h2>
            <div class="ann-card-tags">
              <NTag v-if="a.pinned" size="small" type="warning" round>置顶</NTag>
            </div>
          </div>
          <p v-if="summary(a)" class="ann-card-body">{{ summary(a) }}</p>
          <div class="ann-card-foot">
            <time class="muted">{{ fmt(a.created_at) }}</time>
            <span class="ann-read">查看全文 →</span>
          </div>
        </router-link>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ann-toolbar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 14px; }
.ann-feed { display: flex; flex-direction: column; gap: 12px; }
.ann-card {
  display: block; text-decoration: none; color: inherit;
  border: 1px solid var(--border, #e5e8f0); border-radius: 12px;
  padding: 16px 18px; transition: border-color .15s, box-shadow .15s, transform .15s;
}
.ann-card:hover {
  border-color: var(--primary, #4f7cff);
  box-shadow: 0 2px 12px rgba(79, 124, 255, .10);
  transform: translateY(-1px);
}
.ann-card-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.ann-card-head h2 { margin: 0; font-size: 16px; line-height: 1.45; }
.ann-card-body { margin: 8px 0 0; font-size: 13px; line-height: 1.7; color: var(--muted, #6b7280); }
.ann-card-foot { display: flex; align-items: center; justify-content: space-between; margin-top: 10px; font-size: 12px; }
.ann-read { color: var(--primary, #4f7cff); }
</style>
