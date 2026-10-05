<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NModal, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const extensions = ref<any[]>([])
const themes = ref<any[]>([])
const storefront = ref<any>({})
const logs = ref<Record<string, string[]>>({})

// ---- 插件市场 ----
const marketItems = ref<any[]>([])
const marketSource = ref('')
const marketURL = ref('')
const marketLoading = ref(false)
const installing = ref('')
// zjmf-plugin 安装需要上游地址与密钥
const zjmfForm = ref<{ item: any; base_url: string; token: string } | null>(null)

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

async function loadMarketSettings() {
  try {
    const v = dataOf<any>(await api.get('/admin/marketplace/settings'))
    marketURL.value = v?.index_url || ''
  } catch { /* 未配置时保持空 */ }
}

async function loadMarket(urlOverride?: string) {
  marketLoading.value = true
  try {
    const q = (urlOverride || marketURL.value) ? `?url=${encodeURIComponent(urlOverride || marketURL.value)}` : ''
    const v = dataOf<any>(await api.get(`/admin/marketplace${q}`))
    marketItems.value = v.items || []
    marketSource.value = v.source_url || ''
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '拉取市场索引失败') }
  finally { marketLoading.value = false }
}

async function saveMarketURL() {
  try {
    await api.put('/admin/marketplace/settings', { index_url: marketURL.value })
    message.success('市场索引地址已保存')
    await loadMarket()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
}

const kindName: Record<string, string> = { 'extension': 'WASM 扩展', 'theme': '主题', 'zjmf-plugin': '魔方插件' }

async function installItem(it: any) {
  if (it.kind === 'zjmf-plugin') { zjmfForm.value = { item: it, base_url: '', token: '' }; return }
  installing.value = it.name
  try {
    await api.post('/admin/marketplace/install', { kind: it.kind, name: it.name })
    message.success(`${it.title || it.name} 安装成功${it.kind === 'extension' ? '，请在扩展列表启用' : ''}`)
    await Promise.all([load(), loadMarket()])
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '安装失败') }
  finally { installing.value = '' }
}

async function installZJMF() {
  if (!zjmfForm.value) return
  const { item, base_url, token } = zjmfForm.value
  installing.value = item.name
  try {
    await api.post('/admin/marketplace/install', { kind: item.kind, name: item.name, base_url, token })
    message.success('魔方插件已导入为 custom 供应商，请在「供应商」页测试连接')
    zjmfForm.value = null
    await loadMarket()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '导入失败') }
  finally { installing.value = '' }
}

// ---- 扩展 / 主题 ----
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

onMounted(() => { load(); loadMarketSettings() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">扩展与主题</div><h1>扩展包 / 主题包 / 插件市场</h1><p>扩展为 WASM 沙箱模块（能力权限最小化，崩溃不影响核心）；主题仅含静态资源；插件市场可一键安装扩展、主题与魔方 server 插件（自动转换为 custom 供应商）。</p></div></div>

    <section class="panel" style="margin-bottom:20px">
      <div class="panel-title-row">
        <div><h2>插件市场</h2><span>{{ marketItems.length ? `${marketItems.length} 个可安装项` : '未加载' }}</span></div>
        <div class="row" style="gap:8px;align-items:center">
          <input v-model="marketURL" placeholder="市场索引地址（index.json），留空用官方索引" style="width:320px" class="text-input" />
          <NButton size="small" tertiary @click="saveMarketURL">保存地址</NButton>
          <NButton size="small" type="primary" :loading="marketLoading" @click="loadMarket()">刷新市场</NButton>
        </div>
      </div>
      <div class="stack">
        <div v-for="it in marketItems" :key="it.kind + it.name" class="ext-row">
          <div class="ext-main">
            <b>{{ it.title || it.name }} <small class="muted">v{{ it.version }} · {{ kindName[it.kind] || it.kind }}</small></b>
            <small class="muted">{{ it.description || '—' }}</small>
            <small class="row" style="gap:4px;flex-wrap:wrap">
              <NTag v-for="pm in it.permissions || []" :key="pm" size="tiny" round>{{ pm }}</NTag>
              <NTag v-if="it.installed" size="tiny" type="success" round>已安装{{ it.upgradable ? `（可升级到 v${it.version}）` : '' }}</NTag>
            </small>
          </div>
          <NButton v-if="!it.installed || it.upgradable" size="small" type="primary" :loading="installing === it.name" @click="installItem(it)">{{ it.upgradable ? '升级' : '安装' }}</NButton>
          <NButton v-else-if="it.kind === 'zjmf-plugin'" size="small" tertiary @click="$router?.push?.('/admin/providers')">前往配置</NButton>
          <NTag v-else size="small" round>已安装</NTag>
        </div>
        <div v-if="!marketItems.length && !marketLoading" class="empty-box" style="margin:0">市场索引未加载。点「刷新市场」从索引地址拉取；也可以自托管市场（见 docs/marketplace.md）。</div>
      </div>
    </section>

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

    <NModal :show="zjmfForm !== null" preset="card" title="安装魔方插件" style="max-width:520px" @update:show="(v: boolean) => { if (!v) zjmfForm = null }">
      <div v-if="zjmfForm" class="stack" style="gap:12px">
        <p class="muted" style="margin:0">
          将把魔方 server 插件 <b>{{ zjmfForm.item.title || zjmfForm.item.name }}</b>
          转换为 custom 供应商。请填写上游接口地址（由魔方接口设置里的
          IP/端口/SSL 组成）与接口密钥（accesshash）。
        </p>
        <label class="stack" style="gap:4px"><span>上游接口地址</span>
          <input v-model="zjmfForm.base_url" placeholder="https://1.2.3.4:8888" class="text-input" />
        </label>
        <label class="stack" style="gap:4px"><span>接口密钥（accesshash）</span>
          <input v-model="zjmfForm.token" placeholder="上游接口的签名密钥" class="text-input" />
        </label>
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton size="small" @click="zjmfForm = null">取消</NButton>
          <NButton size="small" type="primary" :loading="installing === zjmfForm.item.name" @click="installZJMF">导入</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.ext-row { display: flex; gap: 8px; align-items: center; }
.ext-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.ext-main small { font-size: 12px; }
.ext-log { border: 1px solid var(--border, #e5e8f0); border-radius: 8px; padding: 8px 10px; }
.ext-log pre { margin: 4px 0 0; font-size: 11px; white-space: pre-wrap; max-height: 180px; overflow: auto; }
.text-input { border: 1px solid var(--border, #e5e8f0); border-radius: 6px; padding: 5px 8px; background: transparent; color: inherit; font-size: 13px; }
</style>
