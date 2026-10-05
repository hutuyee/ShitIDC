<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { NButton, NCheckbox, NInput, NModal, NSelect, NSwitch, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 客户自定义字段（对齐魔方 client_custom_field 插件）：后台定义用户详情可输入的信息。
// 类型：文本框 / 下拉 / 链接 / 密码 / 勾选框 / 文本区 / 下拉文本框。

const message = useMessage()
const list = ref<any[]>([])
const loading = ref(false)
const busy = ref(false)

const typeList = [
  { value: 'text', label: '文本框' },
  { value: 'dropdown', label: '下拉' },
  { value: 'link', label: '链接' },
  { value: 'password', label: '密码' },
  { value: 'tickbox', label: '勾选框' },
  { value: 'textarea', label: '文本区' },
  { value: 'dropdown_text', label: '下拉文本框' },
]
const typeText = (t: string) => typeList.find(x => x.value === t)?.label || t

async function load() {
  loading.value = true
  try {
    const res = dataOf<any>(await api.get('/admin/client-custom-fields'))
    list.value = res.list || []
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取自定义字段失败') }
  finally { loading.value = false }
}

const modal = ref(false)
const editing = ref('')
const form = reactive({ name: '', type: 'text', options: '', description: '', regexpr: '', admin_only: true, required: false, before_settle: false, show_register: true })

function openCreate() {
  editing.value = ''
  Object.assign(form, { name: '', type: 'text', options: '', description: '', regexpr: '', admin_only: true, required: false, before_settle: false, show_register: true })
  modal.value = true
}
function openEdit(row: any) {
  editing.value = row.id
  Object.assign(form, {
    name: row.name, type: row.type, options: row.options || '', description: row.description || '', regexpr: row.regexpr || '',
    admin_only: !!row.admin_only, required: !!row.required, before_settle: !!row.before_settle, show_register: !!row.show_register,
  })
  modal.value = true
}
async function save() {
  if (!form.name.trim()) { message.error('请填写字段名称'); return }
  if ((form.type === 'dropdown' || form.type === 'dropdown_text') && !form.options.trim()) { message.error('下拉类型必须填写下拉值（英文半角逗号分隔）'); return }
  busy.value = true
  try {
    const payload: any = {
      name: form.name.trim(), type: form.type, options: form.options.trim(), description: form.description.trim(), regexpr: form.regexpr.trim(),
      admin_only: form.admin_only, required: form.required, before_settle: form.before_settle, show_register: form.show_register,
    }
    if (editing.value) await api.put(`/admin/client-custom-fields/${editing.value}`, payload)
    else await api.post('/admin/client-custom-fields', payload)
    message.success(editing.value ? '字段已更新' : '字段已新增')
    modal.value = false
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
  finally { busy.value = false }
}
async function toggleStatus(row: any) {
  try {
    await api.put(`/admin/client-custom-fields/${row.id}/status`, { status: row.status ? 0 : 1 })
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}
async function remove(row: any) {
  const tip = row.value_count > 0 ? `当前字段可能存在数据（${row.value_count} 条），是否确认删除？` : '确认删除该字段？'
  if (!window.confirm(tip)) return
  try {
    await api.delete(`/admin/client-custom-fields/${row.id}`)
    message.success('已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}
async function move(index: number, dir: -1 | 1) {
  const cur = list.value[index]
  const prevID = dir === -1 ? (index >= 2 ? list.value[index - 2].id : '0') : list.value[index + 1]?.id
  if (!cur || !prevID) return
  try {
    await api.put(`/admin/client-custom-fields/${cur.id}/drag`, { prev_id: prevID })
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '排序失败') }
}
onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>用户详情自定义字段</h1>
        <p>对齐魔方「客户自定义字段」插件：请在此自定义用户详情中可以输入的信息。用户在个人中心填写，注册时可一并提交（注册时显示的字段），管理员在用户详情查看。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton :loading="loading" secondary @click="load">刷新</NButton>
        <NButton type="primary" @click="openCreate">新增字段</NButton>
      </div>
    </div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>字段列表</h2><span>共 {{ list.length }} 个</span></div></div>
      <div v-if="list.length" class="table-scroll">
        <div class="cf-grid">
          <div class="cf-row cf-head"><span>排序</span><span>字段名称</span><span>字段类型</span><span>字段描述</span><span>可见位置</span><span>订购前必填</span><span>显示状态</span><span>操作</span></div>
          <div v-for="(row, i) in list" :key="row.id" class="cf-row">
            <span class="row" style="gap:2px">
              <NButton size="tiny" quaternary :disabled="i === 0" @click="move(i, -1)">↑</NButton>
              <NButton size="tiny" quaternary :disabled="i === list.length - 1" @click="move(i, 1)">↓</NButton>
            </span>
            <span><span v-if="row.required" class="red-text">*</span>{{ row.name }}<small v-if="row.options && (row.type === 'dropdown' || row.type === 'dropdown_text')">{{ row.options }}</small></span>
            <span>{{ typeText(row.type) }}</span>
            <span class="muted">{{ row.description || '--' }}<small v-if="row.regexpr">{{ row.regexpr }}</small></span>
            <span class="muted">{{ [row.admin_only ? '管理员可见' : '', row.show_register ? '注册时显示' : ''].filter(Boolean).join(' / ') || '--' }}</span>
            <span>{{ row.before_settle ? '是' : '否' }}</span>
            <span><NSwitch :value="!!row.status" size="small" @update:value="toggleStatus(row)" /></span>
            <span class="row" style="gap:6px">
              <NButton size="tiny" tertiary @click="openEdit(row)">编辑</NButton>
              <NButton size="tiny" tertiary type="error" @click="remove(row)">删除</NButton>
            </span>
          </div>
        </div>
      </div>
      <div v-else class="empty-box">还没有自定义字段。</div>
    </section>

    <NModal v-model:show="modal" preset="card" :title="editing ? '编辑字段' : '新增字段'" style="width:min(660px,94vw)">
      <div class="form-grid">
        <label class="full">
          <span>字段名称 *</span>
          <div class="row" style="gap:12px;align-items:center">
            <NInput v-model:value="form.name" placeholder="字段名称" />
            <NCheckbox v-if="form.type !== 'tickbox'" v-model:checked="form.required">字段必填</NCheckbox>
            <NCheckbox v-if="form.type !== 'tickbox'" v-model:checked="form.before_settle">订购前必填</NCheckbox>
          </div>
        </label>
        <label><span>字段类型 *</span><NSelect v-model:value="form.type" :disabled="!!editing" :options="typeList" /></label>
        <label><span>字段描述</span><NInput v-model:value="form.description" placeholder="字段描述" /></label>
        <label v-if="form.type === 'dropdown' || form.type === 'dropdown_text'" class="full"><span>下拉值 *（值之间使用英文半角「,」间隔）</span><NInput v-model:value="form.options" placeholder="值1,值2,值3" /></label>
        <label class="full"><span>验证规则（正则表达式）</span><NInput v-model:value="form.regexpr" placeholder="可选，如 ^1[3-9]\d{9}$" /></label>
        <label class="full">
          <span>显示设置</span>
          <div class="row" style="gap:16px">
            <NCheckbox v-model:checked="form.admin_only">管理员可见</NCheckbox>
            <NCheckbox v-model:checked="form.show_register">注册时显示</NCheckbox>
          </div>
        </label>
      </div>
      <div class="row" style="gap:8px">
        <NButton type="primary" :loading="busy" @click="save">保存</NButton>
        <NButton secondary @click="modal = false">取消</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.cf-grid{display:flex;flex-direction:column;font-size:12px;min-width:1000px}
.cf-row{display:grid;grid-template-columns:80px minmax(160px,1fr) 110px minmax(160px,1fr) 190px 90px 90px 140px;gap:10px;align-items:start;padding:10px 0;border-bottom:1px solid var(--border)}
.cf-head{font-size:10px;font-weight:700;color:var(--muted)}
.cf-row small{display:block;color:var(--muted);font-size:10px;margin-top:2px}
</style>
