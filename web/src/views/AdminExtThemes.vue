<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const extensions = ref<any[]>([])
const themes = ref<any[]>([])
const storefront = ref<any>({})
const logs = ref<Record<string, string[]>>({})

async function load() {
  try {
    const [e, t, sf] = await Promise.all([
      api.get('/admin/extensions').then(x => dataOf<any[]>(x)).catch(() => []),
      api.get('/admin/themes').then(x => dataOf<any[]>(x)).catch(() => []),
      api.get('/admin/settings/storefront').then(x => dataOf<any>(x)).catch(() => ({})),
    ])
    extensions.value = e
    themes.value = t
    storefront.value = sf || {}
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取失败') }
}

async function upload(kind: 'extensions' | 'themes', ev: Event) {
  const input = ev.target as HTMLInputElement
  if (!input.files?.length) return
  const fd = new FormData()
  fd.append('file', input.files[0])
  try {
    await api.post(`/admin/${kind}`, fd, { headers: { 'Content-Type': 'multipart/form-data' } })
    message.success(kind === 'extensions' ? '扩展包已上传，请在列表中启用' : '主题包已安装')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '上传失败') }
  input.value = ''
}

async function toggleExt(x: any) {
  try {
    await api.post(`/admin/extensions/${x.id}/toggle`, { active: !x.active })
    message.success(x.active ? '已停用' : '已启用')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}

async function removeExt(x: any) {
  try {
    await api.delete(`/admin/extensions/${x.id}`)
    message.success('已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

async function showLogs() {
  try {
    logs.value = dataOf(await api.get('/admin/extension-logs'))
    message.info('日志已刷新')
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取日志失败') }
}

async function activateTheme(id: string) {
  try {
    await api.put('/admin/settings/storefront', { ...storefront.value, active_theme: id })
    storefront.value.active_theme = id
    message.success('主题已激活')
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '激活失败') }
}

async function deleteTheme(id: string) {
  try {
    await api.delete(`/admin/themes/${id}`)
    message.success('主题已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">扩展与主题</div><h1>扩展包 / 主题包</h1><p>扩展为 WASM 沙箱模块（能力权限最小化，崩溃不影响核心）；主题仅含静态资源（CSS 变量契约），上传前自动做 Zip Slip / 文件类型校验。</p></div></div>

    <div class="admin-two-col">
      <section class="panel">
        <div class="panel-title-row">
          <div><h2>WASM 扩展</h2><span>{{ extensions.length }} 个</span></div>
          <div class="row" style="gap:8px">
            <label class="soft-action" style="cursor:pointer">上传扩展 zip<input type="file" accept=".zip" style="display:none" @change="upload('extensions', $event)" /></label>
            <NButton size="small" tertiary @click="showLogs">刷新日志</NButton>
          </div>
        </div>
        <div class="stack">
          <div v-for="x in extensions" :key="x.id" class="ext-row">
            <div class="ext-main">
              <b>{{ x.name }} <small class="muted">v{{ x.version }}</small></b>
              <small class="muted">{{ x.description || '—' }}</small>
              <small class="row" style="gap:4px;flex-wrap:wrap"><NTag v-for="pm in x.permissions" :key="pm" size="tiny" round>{{ pm }}</NTag><NTag size="tiny" :type="x.loaded ? 'success' : 'default'" round>{{ x.loaded ? '已加载' : '未加载' }}</NTag></small>
            </div>
            <NButton size="small" tertiary :type="x.active ? 'warning' : 'success'" @click="toggleExt(x)">{{ x.active ? '停用' : '启用' }}</NButton>
            <NButton size="small" tertiary type="error" @click="removeExt(x)">删除</NButton>
          </div>
          <div v-if="!extensions.length" class="empty-box" style="margin:0">还没有扩展。用 scripts/build-extension.sh 构建扩展包后上传。</div>
          <div v-for="(lines, name) in logs" :key="name" class="ext-log">
            <b>{{ name }}</b>
            <pre>{{ (lines || []).join('\n') || '（暂无日志）' }}</pre>
          </div>
        </div>
      </section>

      <section class="panel">
        <div class="panel-title-row">
          <div><h2>主题包</h2><span>{{ themes.length }} 套 · 当前：{{ storefront.active_theme || 'default' }}</span></div>
          <label class="soft-action" style="cursor:pointer">上传主题 zip<input type="file" accept=".zip" style="display:none" @change="upload('themes', $event)" /></label>
        </div>
        <div class="stack">
          <div v-for="t in themes" :key="t.id" class="ext-row">
            <div class="ext-main">
              <b>{{ t.name || t.id }} <small class="muted">v{{ t.version }} · {{ t.author || '未知作者' }}</small></b>
              <small class="muted">/themes/{{ t.id }}/variables.css</small>
            </div>
            <NTag v-if="(storefront.active_theme || 'default') === t.id" size="small" type="success" round>当前主题</NTag>
            <NButton v-else size="small" tertiary type="primary" @click="activateTheme(t.id)">激活</NButton>
            <NButton v-if="t.id !== 'default'" size="small" tertiary type="error" @click="deleteTheme(t.id)">删除</NButton>
          </div>
          <div v-if="!themes.length" class="empty-box" style="margin:0">未找到主题。themes/ 目录下应有 default 与 dark。</div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.ext-row { display: flex; gap: 8px; align-items: center; }
.ext-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.ext-main small { font-size: 12px; }
.ext-log { border: 1px solid var(--border, #e5e8f0); border-radius: 8px; padding: 8px 10px; }
.ext-log pre { margin: 4px 0 0; font-size: 11px; white-space: pre-wrap; max-height: 180px; overflow: auto; }
</style>
