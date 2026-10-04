<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NTag } from 'naive-ui'
import { api, dataOf } from '../api'

// 公告详情页：正文按段落渲染，行高放宽——公告多是维护说明，紧凑排版读起来很累。

const route = useRoute()
const router = useRouter()
const item = ref<any>(null)
const loading = ref(true)
const notFound = ref(false)
const others = ref<any[]>([])

async function load(id: string) {
  loading.value = true
  notFound.value = false
  item.value = null
  try {
    item.value = dataOf<any>(await api.get(`/announcements/${id}`))
    // 顺带取列表，底部给出「其它公告」，看完一条能接着看。
    const list = dataOf<any[]>(await api.get('/announcements', { params: { limit: 100 } }))
    others.value = list.filter(a => a.id !== id).slice(0, 5)
  } catch (e: any) {
    if (e?.response?.status === 404) notFound.value = true
  } finally { loading.value = false }
}

const paras = computed(() => String(item.value?.body || '').split(/\n+/).map((s: string) => s.trim()).filter(Boolean))
const fmt = (v?: string) => (v ? new Date(v).toLocaleString('zh-CN', { dateStyle: 'long', timeStyle: 'short' }) : '—')
const edited = computed(() => {
  if (!item.value) return false
  return item.value.updated_at && item.value.created_at &&
    new Date(item.value.updated_at).getTime() - new Date(item.value.created_at).getTime() > 1000
})

watch(() => route.params.id, id => { if (id) load(String(id)) }, { immediate: false })
onMounted(() => load(String(route.params.id)))
</script>

<template>
  <div class="page">
    <div class="ann-detail-head">
      <NButton tertiary size="small" @click="router.back()">← 返回</NButton>
      <router-link to="/announcements"><NButton tertiary size="small">全部公告</NButton></router-link>
    </div>

    <div v-if="loading" class="panel empty-box">加载中…</div>

    <div v-else-if="notFound" class="panel empty-box">
      这条公告不存在或已下线。
      <div style="margin-top:12px"><router-link to="/announcements"><NButton size="small">查看全部公告</NButton></router-link></div>
    </div>

    <article v-else-if="item" class="panel ann-article">
      <header>
        <h1>{{ item.title }}</h1>
        <div class="ann-detail-meta">
          <NTag v-if="item.pinned" size="small" type="warning" round>置顶</NTag>
          <span class="muted">发布于 {{ fmt(item.created_at) }}</span>
          <span v-if="edited" class="muted">· 更新于 {{ fmt(item.updated_at) }}</span>
        </div>
      </header>
      <div class="ann-article-body">
        <p v-for="(p, i) in paras" :key="i">{{ p }}</p>
        <p v-if="!paras.length" class="muted">（这条公告没有正文）</p>
      </div>
    </article>

    <section v-if="others.length" class="panel">
      <div class="panel-title-row"><div><h2>其它公告</h2><span>继续看看还有哪些动态</span></div>
        <router-link to="/announcements">查看全部 →</router-link>
      </div>
      <div class="ann-others">
        <router-link v-for="a in others" :key="a.id" :to="`/announcements/${a.id}`" class="ann-other">
          <NTag v-if="a.pinned" size="tiny" type="warning" round>置顶</NTag>
          <b>{{ a.title }}</b>
          <span class="grow"></span>
          <time class="muted">{{ fmt(a.created_at) }}</time>
        </router-link>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ann-detail-head { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.ann-article header h1 { margin: 0 0 8px; font-size: 22px; line-height: 1.4; }
.ann-detail-meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 12px; }
.ann-article-body { margin-top: 18px; }
/* 行高 1.85 + 段间距：维护公告常有步骤列表，密排会很难读。 */
.ann-article-body p { margin: 0 0 14px; line-height: 1.85; font-size: 15px; white-space: pre-wrap; word-break: break-word; }
.ann-article-body p:last-child { margin-bottom: 0; }
.ann-others { display: flex; flex-direction: column; gap: 8px; }
.ann-other {
  display: flex; align-items: center; gap: 8px; text-decoration: none; color: inherit;
  border: 1px solid var(--border, #e5e8f0); border-radius: 10px; padding: 10px 12px; font-size: 14px;
}
.ann-other:hover { border-color: var(--primary, #4f7cff); }
.ann-other time { font-size: 12px; }
</style>
