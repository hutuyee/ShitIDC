<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NInput, NTag, useDialog, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 公告管理独立成一页：撰写公告需要横向空间与实时预览，
// 挤在后台首页的两栏小卡片里既写不长也看不清排版。

const message = useMessage()
const dialog = useDialog()
const items = ref<any[]>([])
const busy = ref(false)
const loading = ref(false)
const editingID = ref('')
const keyword = ref('')
const previewOpen = ref(true)
const form = reactive({ title: '', body: '', active: true, pinned: false })

async function load() {
  loading.value = true
  try { items.value = dataOf(await api.get('/admin/announcements')) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取公告失败') }
  finally { loading.value = false }
}

function resetForm() {
  editingID.value = ''
  Object.assign(form, { title: '', body: '', active: true, pinned: false })
}

function edit(a: any) {
  editingID.value = a.id
  Object.assign(form, { title: a.title || '', body: a.body || '', active: Boolean(a.active), pinned: Boolean(a.pinned) })
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function save() {
  if (!form.title.trim()) { message.error('请填写公告标题'); return }
  busy.value = true
  try {
    if (editingID.value) {
      await api.put(`/admin/announcements/${editingID.value}`, { ...form })
      message.success('公告已更新')
    } else {
      await api.post('/admin/announcements', { ...form })
      message.success('公告已发布，用户仪表板立即展示')
    }
    resetForm()
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally { busy.value = false }
}

function remove(a: any) {
  dialog.warning({
    title: '删除公告',
    content: `确认删除「${a.title}」？该操作不可恢复。`,
    positiveText: '确认删除',
    negativeText: '取消',
    async onPositiveClick() {
      try {
        await api.delete(`/admin/announcements/${a.id}`)
        message.success('公告已删除')
        if (editingID.value === a.id) resetForm()
        await load()
      } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
    },
  })
}

const filtered = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return items.value
  return items.value.filter(a =>
    String(a.title || '').toLowerCase().includes(k) || String(a.body || '').toLowerCase().includes(k))
})

const fmt = (v?: string) => (v ? new Date(v).toLocaleString('zh-CN') : '—')
const wordCount = computed(() => form.body.trim().length)
// 正文按换行拆段展示，避免长文挤成一坨。
const previewParas = computed(() => form.body.split(/\n+/).map(s => s.trim()).filter(Boolean))

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">内容运营</div>
        <h1>站内公告</h1>
        <p>发布后的公告展示在用户仪表板「公告」区块；置顶排最前，取消「展示」后对用户隐藏（内容保留）。</p>
      </div>
    </div>

    <!-- 撰写区：左侧编辑、右侧实时预览。公告的排版就是它的全部价值，所以要边写边看。 -->
    <section class="panel ann-compose">
      <div class="panel-title-row">
        <div>
          <h2>{{ editingID ? '编辑公告' : '发布公告' }}</h2>
          <span>{{ editingID ? '修改后保存即对用户生效' : '支持多行正文，段落之间空一行更易读' }}</span>
        </div>
        <div class="row" style="gap:8px">
          <NTag v-if="editingID" size="small" type="info" round>编辑中</NTag>
          <NButton size="small" tertiary @click="previewOpen = !previewOpen">
            {{ previewOpen ? '隐藏预览' : '显示预览' }}
          </NButton>
        </div>
      </div>

      <div class="ann-compose-grid" :class="{ 'no-preview': !previewOpen }">
        <div class="stack ann-editor">
          <label class="ann-field">
            <span>标题</span>
            <NInput v-model:value="form.title" placeholder="例如：10 月 1 日 凌晨 2:00-4:00 香港节点维护" :maxlength="120" show-count />
          </label>
          <label class="ann-field">
            <span>正文 <em class="muted">（{{ wordCount }} 字）</em></span>
            <NInput
              v-model:value="form.body"
              type="textarea"
              :rows="14"
              placeholder="把要告诉用户的事情写清楚：影响范围、开始与结束时间、需要用户做什么。&#10;&#10;段落之间空一行，阅读体验会好很多。"
            />
          </label>
          <div class="ann-options">
            <NCheckbox v-model:checked="form.active">对用户展示</NCheckbox>
            <NCheckbox v-model:checked="form.pinned">置顶</NCheckbox>
            <span class="muted">置顶公告始终排在列表最前</span>
          </div>
          <div class="row" style="gap:8px">
            <NButton type="primary" :loading="busy" @click="save">{{ editingID ? '保存修改' : '发布公告' }}</NButton>
            <NButton v-if="editingID" @click="resetForm">取消编辑</NButton>
            <NButton v-else tertiary @click="resetForm">清空</NButton>
          </div>
        </div>

        <aside v-if="previewOpen" class="ann-preview">
          <div class="ann-preview-label">用户看到的样式</div>
          <article class="ann-card">
            <header>
              <h3>{{ form.title || '（未填写标题）' }}</h3>
              <div class="ann-meta">
                <NTag v-if="form.pinned" size="tiny" type="warning" round>置顶</NTag>
                <NTag v-if="!form.active" size="tiny" round>已下线</NTag>
                <span class="muted">{{ fmt(new Date().toISOString()) }}</span>
              </div>
            </header>
            <div class="ann-body">
              <p v-for="(p, i) in previewParas" :key="i">{{ p }}</p>
              <p v-if="!previewParas.length" class="muted">（正文为空，用户只会看到标题）</p>
            </div>
          </article>
        </aside>
      </div>
    </section>

    <!-- 列表区：整宽展示，正文预览两行，长公告也能看清开头。 -->
    <section class="panel">
      <div class="panel-title-row">
        <div><h2>全部公告</h2><span>{{ items.length }} 条{{ keyword ? ` · 匹配 ${filtered.length} 条` : '' }}</span></div>
        <NInput v-model:value="keyword" placeholder="搜索标题或正文" clearable style="max-width:240px" />
      </div>

      <div v-if="loading" class="empty-box">加载中…</div>
      <div v-else-if="!filtered.length" class="empty-box">
        {{ items.length ? '没有匹配的公告。' : '还没有公告。发布第一条后，用户仪表板会立即展示。' }}
      </div>
      <div v-else class="ann-list">
        <div v-for="a in filtered" :key="a.id" class="ann-item" :class="{ editing: a.id === editingID }">
          <div class="ann-item-main">
            <div class="ann-item-title">
              <b>{{ a.title }}</b>
              <NTag v-if="a.pinned" size="tiny" type="warning" round>置顶</NTag>
              <NTag v-if="!a.active" size="tiny" round>已下线</NTag>
            </div>
            <p v-if="a.body" class="ann-item-body">{{ a.body }}</p>
            <small class="muted">发布于 {{ fmt(a.created_at) }}</small>
          </div>
          <div class="row" style="gap:6px">
            <NButton size="tiny" tertiary @click="edit(a)">编辑</NButton>
            <NButton size="tiny" tertiary type="error" @click="remove(a)">删除</NButton>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ann-compose-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 380px); gap: 18px; align-items: start; }
.ann-compose-grid.no-preview { grid-template-columns: minmax(0, 1fr); }
@media (max-width: 1080px) { .ann-compose-grid { grid-template-columns: minmax(0, 1fr); } }

.ann-editor { gap: 14px; }
.ann-field { display: flex; flex-direction: column; gap: 6px; }
.ann-field > span { font-size: 13px; font-weight: 600; }
.ann-field em { font-style: normal; font-weight: 400; }

.ann-options { display: flex; align-items: center; gap: 18px; flex-wrap: wrap; font-size: 13px; }

.ann-preview { position: sticky; top: 12px; display: flex; flex-direction: column; gap: 8px; }
.ann-preview-label { font-size: 12px; color: var(--muted, #8a93a6); }
.ann-card {
  border: 1px solid var(--border, #e5e8f0); border-radius: 12px;
  padding: 16px 18px; background: var(--panel-soft, #fbfcfe);
}
.ann-card header h3 { margin: 0 0 6px; font-size: 16px; line-height: 1.4; }
.ann-meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 12px; }
.ann-body { margin-top: 12px; }
/* 正文行高放宽：公告多为维护说明，紧凑排版读起来很累。 */
.ann-body p { margin: 0 0 12px; line-height: 1.75; font-size: 14px; white-space: pre-wrap; word-break: break-word; }
.ann-body p:last-child { margin-bottom: 0; }

.ann-list { display: flex; flex-direction: column; gap: 10px; }
.ann-item {
  display: flex; justify-content: space-between; align-items: flex-start; gap: 14px;
  border: 1px solid var(--border, #e5e8f0); border-radius: 10px; padding: 12px 14px;
}
.ann-item.editing { border-color: var(--primary, #4f7cff); box-shadow: 0 0 0 1px var(--primary, #4f7cff) inset; }
.ann-item-main { min-width: 0; display: flex; flex-direction: column; gap: 4px; flex: 1; }
.ann-item-title { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.ann-item-body {
  margin: 0; font-size: 13px; color: var(--muted, #6b7280); line-height: 1.6;
  /* 列表里只露两行，长公告也不会把整页撑得很高。 */
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
  white-space: pre-wrap; word-break: break-word;
}
.ann-item small { font-size: 12px; }
</style>
