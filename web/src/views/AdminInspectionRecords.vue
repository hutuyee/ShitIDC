<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NDatePicker, NImage, NInput, NModal, NSelect, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import EntityPicker from '../components/EntityPicker.vue'

// 异常巡查记录（对齐魔方 abnormal_inspection_records 插件）：
// 列表 / 新增 / 编辑 / 删除 / 导出 Excel，关联用户与产品，支持异常截图多张。

const message = useMessage()
const list = ref<any[]>([])
const count = ref(0)
const loading = ref(false)
const busy = ref(false)
const exportBusy = ref(false)

const filters = reactive({ keywords: '', range: null as [number, number] | null, page: 1, limit: 20 })

const fmtTime = (ts: any) => (ts ? new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false }) : '--')
const seconds = (ms: number) => Math.floor(ms / 1000)
const statusText = (s: string) => ({ active: '正常', suspended: '已暂停', terminated: '已终止', pending: '待开通', provisioning: '开通中', failed: '失败' } as any)[s] || s

async function load() {
  loading.value = true
  try {
    const params: any = { page: filters.page, limit: filters.limit }
    if (filters.keywords.trim()) params.keywords = filters.keywords.trim()
    if (filters.range) { params.start_time = seconds(filters.range[0]); params.end_time = seconds(filters.range[1]) }
    const res = dataOf<any>(await api.get('/admin/abnormal-inspection-records', { params }))
    list.value = res.list || []
    count.value = Number(res.count || 0)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取记录失败') }
  finally { loading.value = false }
}
function search() { filters.page = 1; load() }
function resetFilters() { filters.keywords = ''; filters.range = null; filters.page = 1; load() }
function changePage(delta: number) {
  const max = Math.max(1, Math.ceil(count.value / filters.limit))
  const next = Math.min(max, Math.max(1, filters.page + delta))
  if (next !== filters.page) { filters.page = next; load() }
}

// ---- 表单（新增 / 编辑共用） ----
const formModal = ref(false)
const form = reactive({
  id: '', client_id: '', host_id: '', hostLabel: '', ip: '', matter: '', measure: '',
  processTime: Date.now(), imgs: [] as { stored: string; name: string; url: string }[],
})
const serviceOptions = ref<any[]>([])
const userLabel = ref('')
const uploading = ref(false)

async function loadServices(clientID: string) {
  serviceOptions.value = []
  if (!clientID) return
  try {
    const detail = dataOf<any>(await api.get(`/admin/users/${clientID}/detail`))
    userLabel.value = detail.email || ''
    serviceOptions.value = (detail.services || []).map((s: any) => ({ value: s.id, label: `${s.product_name}（${statusText(s.status)}）` }))
  } catch { serviceOptions.value = [] }
}
function onUserPicked(hit: any) {
  if (hit) userLabel.value = hit.label + (hit.sub ? ' · ' + hit.sub : '')
  form.host_id = ''
  loadServices(form.client_id)
}
function openCreate() {
  form.id = ''; form.client_id = ''; form.host_id = ''; form.hostLabel = ''; form.ip = ''
  form.matter = ''; form.measure = ''; form.processTime = Date.now(); form.imgs = []
  userLabel.value = ''; serviceOptions.value = []
  formModal.value = true
}
function openEdit(row: any) {
  form.id = row.id
  form.client_id = row.client_id
  form.host_id = row.host_id
  form.hostLabel = row.product_name || ''
  form.ip = row.ip
  form.matter = row.matter
  form.measure = row.measure
  form.processTime = Number(row.process_time || 0) * 1000 || Date.now()
  form.imgs = (row.img || []).map((x: any) => ({ stored: x.stored || '', name: x.name, url: x.url }))
  userLabel.value = `${row.username || ''}${row.company ? '(' + row.company + ')' : ''}`
  serviceOptions.value = []
  formModal.value = true
}
async function uploadImage(file: File) {
  const fd = new FormData()
  fd.append('file', file)
  uploading.value = true
  try {
    const up = dataOf<any>(await api.post('/admin/abnormal-inspection-records/images', fd))
    form.imgs.push({ stored: up.stored, name: up.name, url: URL.createObjectURL(file) })
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '截图上传失败')
  } finally { uploading.value = false }
}
function onPickFiles(e: Event) {
  const input = e.target as HTMLInputElement
  const files = Array.from(input.files || [])
  for (const f of files) uploadImage(f)
  input.value = ''
}
function removeImage(i: number) { form.imgs.splice(i, 1) }

async function save() {
  if (!form.client_id) { message.error('请选择异常用户'); return }
  if (!form.host_id) { message.error('请选择关联产品'); return }
  if (!/^(\d{1,3}\.){3}\d{1,3}$/.test(form.ip.trim())) { message.error('ip格式为xxx.xxx.xxx.xxx'); return }
  if (!form.measure.trim()) { message.error('请填写处理措施'); return }
  if (!form.matter.trim()) { message.error('请填写异常事项'); return }
  busy.value = true
  try {
    const payload = {
      client_id: form.client_id, host_id: form.host_id, ip: form.ip.trim(),
      matter: form.matter.trim(), measure: form.measure.trim(),
      process_time: seconds(form.processTime),
      img: form.imgs.map(x => ({ stored: x.stored, name: x.name })),
    }
    if (form.id) await api.put(`/admin/abnormal-inspection-records/${form.id}`, payload)
    else await api.post('/admin/abnormal-inspection-records', payload)
    message.success(form.id ? '记录已更新' : '记录已保存')
    formModal.value = false
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}

async function remove(row: any) {
  if (!window.confirm('确认删除这条异常巡查记录？')) return
  try {
    await api.delete(`/admin/abnormal-inspection-records/${row.id}`)
    message.success('已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---- 详情 ----
const detailVisible = ref(false)
const detail = ref<any>(null)
function openDetail(row: any) { detail.value = row; detailVisible.value = true }

// ---- 导出 ----
async function exportExcel() {
  exportBusy.value = true
  try {
    const params: any = {}
    if (filters.keywords.trim()) params.keywords = filters.keywords.trim()
    if (filters.range) { params.start_time = seconds(filters.range[0]); params.end_time = seconds(filters.range[1]) }
    const res = await api.get('/admin/abnormal-inspection-records/export.xlsx', { params, responseType: 'blob' })
    const dispo = String(res.headers['content-disposition'] || '')
    let filename = `异常巡查记录-${new Date().toISOString().slice(0, 10)}.xlsx`
    const star = /filename\*=UTF-8''([^;]+)/i.exec(dispo)
    const plain = /filename="([^"]+)"/i.exec(dispo)
    if (star) filename = decodeURIComponent(star[1])
    else if (plain) filename = plain[1]
    const url = URL.createObjectURL(res.data)
    const a = document.createElement('a')
    a.href = url; a.download = filename; a.style.display = 'none'
    document.body.appendChild(a); a.click(); document.body.removeChild(a)
    URL.revokeObjectURL(url)
    message.success('导出成功')
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '导出失败') }
  finally { exportBusy.value = false }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>异常巡查记录</h1>
        <p>对齐魔方「异常巡查记录」插件：在此处记录巡查中发现的异常情况——关联用户与产品、异常时 IP、异常事项、处理措施、处理时间与异常截图。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton :loading="exportBusy" secondary @click="exportExcel">导出</NButton>
        <NButton :loading="loading" secondary @click="load">刷新</NButton>
        <NButton type="primary" @click="openCreate">新增</NButton>
      </div>
    </div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>巡查记录</h2><span>共 {{ count }} 条</span></div></div>

      <div class="row" style="gap:8px;flex-wrap:wrap;margin-bottom:14px">
        <NInput v-model:value="filters.keywords" placeholder="请输入用户、公司、联系方式、IP" clearable style="width:280px" @keyup.enter="search" />
        <NDatePicker v-model:value="filters.range" type="datetimerange" clearable placeholder="处理时间" style="width:360px" />
        <NButton @click="search">查询</NButton>
        <NButton quaternary @click="resetFilters">重置</NButton>
      </div>

      <div v-if="list.length" class="table-scroll">
        <div class="insp-grid">
          <div class="insp-row insp-head">
            <span>ID</span><span>用户</span><span>联系方式</span><span>订单ID</span><span>购买时间</span>
            <span>异常时IP</span><span>异常事项</span><span>处理措施</span><span>处理时间</span><span>最新提交人</span><span>操作</span>
          </div>
          <div v-for="row in list" :key="row.id" class="insp-row">
            <span class="mono">{{ String(row.id).slice(0, 8) }}</span>
            <span><b>{{ row.username || '—' }}</b><small v-if="row.company">{{ row.company }}</small></span>
            <span>{{ row.phone || row.email || '—' }}<small v-if="row.phone && row.email">{{ row.email }}</small></span>
            <span class="mono">{{ String(row.order_id || '').slice(0, 8) || '—' }}</span>
            <span class="muted">{{ row.pay_time ? fmtTime(row.pay_time) : '--' }}</span>
            <span class="mono">{{ row.ip }}</span>
            <span class="ellipsis" :title="row.matter">{{ row.matter }}</span>
            <span class="ellipsis" :title="row.measure">{{ row.measure }}</span>
            <span class="muted">{{ fmtTime(row.process_time) }}</span>
            <span>{{ row.admin_name || '—' }}</span>
            <span class="row" style="gap:6px">
              <NButton size="tiny" tertiary @click="openDetail(row)">详情</NButton>
              <NButton size="tiny" tertiary @click="openEdit(row)">编辑</NButton>
              <NButton size="tiny" tertiary type="error" @click="remove(row)">删除</NButton>
            </span>
          </div>
        </div>
      </div>
      <div v-else class="empty-box">还没有异常巡查记录。</div>

      <div class="row" style="gap:8px;align-items:center;margin-top:12px" v-if="count > filters.limit">
        <NButton size="small" secondary :disabled="filters.page <= 1" @click="changePage(-1)">上一页</NButton>
        <span class="muted">第 {{ filters.page }} / {{ Math.max(1, Math.ceil(count / filters.limit)) }} 页</span>
        <NButton size="small" secondary :disabled="filters.page >= Math.ceil(count / filters.limit)" @click="changePage(1)">下一页</NButton>
      </div>
    </section>

    <NModal v-model:show="formModal" preset="card" :title="form.id ? '编辑异常记录' : '新增异常记录'" style="width:min(680px,94vw)">
      <div class="form-grid">
        <label class="full" v-if="!form.id">
          <span>异常用户 *</span>
          <EntityPicker v-model="form.client_id" kind="user" placeholder="点此搜索并选择用户" @picked="onUserPicked" />
        </label>
        <label class="full" v-else><span>异常用户</span><div class="panel" style="padding:9px 12px">{{ userLabel || '—' }}</div></label>

        <label class="full" v-if="!form.id">
          <span>关联产品 *</span>
          <NSelect v-model:value="form.host_id" :options="serviceOptions" filterable placeholder="先选择用户，再选择其名下的产品" />
        </label>
        <label class="full" v-else><span>关联产品</span><div class="panel" style="padding:9px 12px">{{ form.hostLabel || '—' }}</div></label>

        <label><span>异常时IP *</span><NInput v-model:value="form.ip" placeholder="xxx.xxx.xxx.xxx" /></label>
        <label><span>处理时间 *</span><NDatePicker v-model:value="form.processTime" type="datetime" style="width:100%" /></label>
        <label class="full"><span>处理措施 *</span><NInput v-model:value="form.measure" placeholder="处理措施" /></label>
        <label class="full"><span>异常事项 *</span><NInput v-model:value="form.matter" type="textarea" :rows="3" placeholder="异常事项" /></label>
        <div class="full field-block">
          <span>异常截图（可多张，png / jpg / webp / gif，单张 ≤ 5 MB）</span>
          <div class="row" style="gap:10px;flex-wrap:wrap">
            <div v-for="(img, i) in form.imgs" :key="i" class="shot-box">
              <NImage :src="img.url" :width="96" object-fit="cover" />
              <div class="row" style="justify-content:space-between;align-items:center">
                <small class="muted ellipsis" :title="img.name" style="max-width:70px">{{ img.name }}</small>
                <NButton size="tiny" quaternary type="error" @click="removeImage(i)">移除</NButton>
              </div>
            </div>
            <label class="shot-add" v-if="form.imgs.length < 9">
              <input type="file" accept="image/*" multiple style="display:none" @change="onPickFiles" />
              <span>{{ uploading ? '上传中…' : '+ 添加截图' }}</span>
            </label>
          </div>
        </div>
      </div>
      <div class="row" style="gap:8px">
        <NButton type="primary" :loading="busy" @click="save">{{ form.id ? '保存' : '添加' }}</NButton>
        <NButton secondary @click="formModal = false">取消</NButton>
      </div>
    </NModal>

    <NModal v-model:show="detailVisible" preset="card" title="异常详情" style="width:min(760px,94vw)">
      <div class="stack" v-if="detail">
        <div class="detail-line"><b>异常用户：</b><span>{{ detail.username }}{{ detail.company ? '(' + detail.company + ')' : '' }}</span></div>
        <div class="detail-line"><b>联系方式：</b><span>{{ detail.phone || '—' }}{{ detail.email ? '（' + detail.email + '）' : '' }}</span></div>
        <div class="detail-line"><b>关联产品：</b><span>{{ detail.product_name || '--' }}{{ detail.order_id ? '（订单 ' + String(detail.order_id).slice(0, 8) + '）' : '' }}</span></div>
        <div class="detail-line"><b>购买时间：</b><span>{{ detail.pay_time ? fmtTime(detail.pay_time) : '--' }}</span></div>
        <div class="detail-line"><b>异常时IP：</b><span class="mono">{{ detail.ip }}</span></div>
        <div class="detail-line"><b>处理时间：</b><span>{{ fmtTime(detail.process_time) }}</span></div>
        <div class="detail-line"><b>处理措施：</b><span>{{ detail.measure }}</span></div>
        <div class="detail-line"><b>最新提交人：</b><span>{{ detail.admin_name || '—' }}</span></div>
        <div class="detail-line"><b>异常事项：</b><span>{{ detail.matter }}</span></div>
        <div class="detail-line"><b>异常截图：</b>
          <span v-if="detail.img && detail.img.length" class="row" style="gap:8px;flex-wrap:wrap">
            <NImage v-for="(img, i) in detail.img" :key="i" :src="img.url" :width="120" object-fit="cover" />
          </span>
          <span v-else class="muted">无截图</span>
        </div>
      </div>
      <template #footer><NButton secondary @click="detailVisible = false">关闭</NButton></template>
    </NModal>
  </div>
</template>

<style scoped>
.insp-grid{display:flex;flex-direction:column;font-size:12px;min-width:1180px}
.insp-row{display:grid;grid-template-columns:90px 150px 190px 100px 140px 120px minmax(150px,1fr) minmax(150px,1fr) 140px 100px 170px;gap:10px;align-items:start;padding:10px 0;border-bottom:1px solid var(--border)}
.insp-head{font-size:10px;font-weight:700;color:var(--muted)}
.insp-row small{display:block;color:var(--muted);font-size:10px;margin-top:2px}
.ellipsis{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.field-block{display:flex;flex-direction:column;gap:7px}
.field-block>span{font-size:11px;font-weight:700}
.shot-box{border:1px solid var(--border);border-radius:10px;padding:6px;display:flex;flex-direction:column;gap:4px;background:var(--panel)}
.shot-add{display:grid;place-items:center;width:108px;height:82px;border:1px dashed var(--border);border-radius:10px;color:var(--muted);font-size:12px;cursor:pointer}
.shot-add:hover{border-color:var(--primary);color:var(--primary)}
.detail-line{display:flex;gap:6px;font-size:12px;line-height:1.7}
.detail-line>b{flex-shrink:0}
</style>
