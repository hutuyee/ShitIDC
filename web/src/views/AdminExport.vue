<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NCheckboxGroup, NInput, NSelect, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

type Column = { key: string; label: string; money?: boolean; time?: boolean }
type Dataset = { key: string; name: string; columns: Column[] }
type Config = { id: number; custom_name: string; dataset: string; columns: string[]; updated_at: string }

const message = useMessage()
const datasets = ref<Dataset[]>([])
const configs = ref<Config[]>([])
const busy = ref(false)
const editingId = ref<number | null>(null)
const exporting = ref<Config | null>(null)
const form = reactive({ custom_name: '', dataset: 'bill_pay', columns: [] as string[] })
const range = reactive({ start: '', end: '' })

const datasetOptions = computed(() => datasets.value.map(d => ({ label: d.name, value: d.key })))
const columnOptions = computed(() => datasets.value.find(d => d.key === form.dataset)?.columns || [])

function pickDataset(key: string) {
  form.dataset = key
  const d = datasets.value.find(x => x.key === key)
  form.columns = d ? d.columns.map(c => c.key) : []
}

function startCreate() {
  editingId.value = null
  form.custom_name = ''
  pickDataset(form.dataset || datasetOptions.value[0]?.value || 'bill_pay')
}

function startEdit(c: Config) {
  editingId.value = c.id
  form.custom_name = c.custom_name
  form.dataset = c.dataset
  form.columns = [...(c.columns || [])]
}

async function load() {
  try {
    const [ds, cfgs] = await Promise.all([api.get('/admin/export/datasets'), api.get('/admin/export/configs')])
    datasets.value = dataOf<Dataset[]>(ds)
    configs.value = dataOf<Config[]>(cfgs)
    if (!editingId.value && !form.custom_name && !form.columns.length) startCreate()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取导出列表失败') }
}

async function save() {
  if (!form.custom_name.trim()) { message.error('请填写自定义名称'); return }
  if (!form.columns.length) { message.error('至少选择一个导出字段'); return }
  busy.value = true
  try {
    const payload = { custom_name: form.custom_name.trim(), dataset: form.dataset, columns: form.columns }
    if (editingId.value) await api.put(`/admin/export/configs/${editingId.value}`, payload)
    else await api.post('/admin/export/configs', payload)
    message.success(editingId.value ? '导出列表已更新' : '导出列表已创建')
    startCreate()
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}

async function remove(c: Config) {
  try { await api.delete(`/admin/export/configs/${c.id}`); message.success('已删除'); await load() }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// 与参考插件一致：选好时间区间后打开下载地址（同源 cookie 直接带上）。
function startExport(c: Config) { exporting.value = c; range.start = ''; range.end = '' }

function doExport() {
  if (!exporting.value) return
  const q = new URLSearchParams({ config_id: String(exporting.value.id) })
  if (range.start) q.set('start', range.start)
  if (range.end) q.set('end', range.end)
  window.open('/api/v1/admin/export/download?' + q.toString(), '_blank')
  exporting.value = null
}

const datasetName = (k: string) => datasets.value.find(d => d.key === k)?.name || k
const columnLabels = (c: Config) => {
  const d = datasets.value.find(x => x.key === c.dataset)
  return (c.columns || []).map(k => d?.columns.find(col => col.key === k)?.label || k).join('、')
}
const fmt = (v?: string) => (v ? new Date(v).toLocaleString() : '—')
onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">附属插件</div><h1>导出中心</h1><p>对齐魔方「数据导出至 Excel」插件：维护自定义导出列表（内置已支付账单与推广业绩两个数据集），选择字段后按时间区间导出 xlsx。</p></div></div>

    <div v-if="exporting" class="panel" style="margin-bottom:16px">
      <div class="panel-title-row"><div><h2>导出「{{ exporting.custom_name }}」</h2><span>时间区间按{{ exporting.dataset === 'achievement' ? '结算时间' : '收款时间' }}过滤，留空导出全部</span></div></div>
      <div class="row" style="gap:8px;align-items:center;flex-wrap:wrap">
        <input type="date" class="native-input" v-model="range.start" />
        <span class="muted">至</span>
        <input type="date" class="native-input" v-model="range.end" />
        <NButton type="primary" @click="doExport">确认导出</NButton>
        <NButton tertiary @click="exporting = null">取消</NButton>
      </div>
    </div>

    <div class="admin-two-col">
      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>{{ editingId ? '编辑导出列表' : '新建导出列表' }}</h2><span>自定义名称 + 导出列表 + 字段</span></div></div>
        <div class="form-grid">
          <label class="full"><span>自定义名称</span><NInput v-model:value="form.custom_name" placeholder="例如 上月已支付账单" /></label>
          <label class="full"><span>导出列表</span>
            <NSelect :value="form.dataset" :options="datasetOptions" @update:value="pickDataset" />
          </label>
          <label class="full"><span>导出字段</span>
            <NCheckboxGroup v-model:value="form.columns">
              <div class="row" style="gap:12px;flex-wrap:wrap">
                <NCheckbox v-for="c in columnOptions" :key="c.key" :value="c.key" :label="c.label" />
              </div>
            </NCheckboxGroup>
          </label>
        </div>
        <div class="row" style="gap:8px">
          <NButton type="primary" size="large" :loading="busy" @click="save">{{ editingId ? '保存修改' : '创建导出列表' }}</NButton>
          <NButton v-if="editingId" tertiary size="large" @click="startCreate">取消编辑</NButton>
        </div>
      </section>

      <section class="panel">
        <div class="panel-title-row"><div><h2>导出列表</h2><span>{{ configs.length }} 个</span></div></div>
        <div v-if="configs.length" class="table-scroll"><div class="audit-table">
          <div class="audit-row audit-head"><span>名称</span><span>列表</span><span>字段</span><span>更新时间</span><span>操作</span></div>
          <div v-for="c in configs" :key="c.id" class="audit-row">
            <span><b>{{ c.custom_name }}</b></span>
            <span class="muted">{{ datasetName(c.dataset) }}</span>
            <span class="muted">{{ columnLabels(c) }}</span>
            <span class="muted">{{ fmt(c.updated_at) }}</span>
            <span class="row" style="gap:6px">
              <NButton size="tiny" tertiary type="primary" @click="startExport(c)">导出</NButton>
              <NButton size="tiny" tertiary @click="startEdit(c)">编辑</NButton>
              <NButton size="tiny" tertiary type="error" @click="remove(c)">删除</NButton>
            </span>
          </div>
        </div></div>
        <div v-else class="empty-box">还没有导出列表。</div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.native-input { height: 34px; border-radius: 6px; border: 1px solid var(--border, #d5d9e4); background: var(--panel, #fff); color: var(--text, #1c2333); padding: 0 8px; }
</style>
