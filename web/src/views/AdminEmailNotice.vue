<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 值邮件通知管理员（对齐魔方 CBAP EmailNoticeAdmin 插件）。
// 每个业务动作一行：邮件接口 / 邮件模板 / 通知人员 / 启用。
// 事件发生时由后端 internal/notify 按规则给选定员工发信。

const message = useMessage()
const busy = ref(false)

type Row = {
  event: string
  name_lang: string
  email_name: string
  email_template: string
  notify_personnel: number[]
  email_enable: boolean
}

const rows = ref<Row[]>([])
const providerOptions = ref<{ label: string; value: string }[]>([])
const templateOptions = ref<{ label: string; value: string }[]>([])
const adminOptions = ref<{ label: string; value: number }[]>([])

async function load() {
  busy.value = true
  try {
    const d = dataOf<any>(await api.get('/admin/email-notice-admin'))
    rows.value = (d?.list || []).map((r: any) => ({
      event: r.event,
      name_lang: r.name_lang || r.event,
      email_name: r.email_name || '',
      email_template: r.email_template || '',
      notify_personnel: r.notify_personnel || [],
      email_enable: Boolean(r.email_enable),
    }))
    providerOptions.value = [
      { label: '默认通道（跟随系统）', value: '' },
      ...(d?.providers || []).map((p: any) => ({
        label: `${p.name}（${p.provider}）${p.is_default ? ' · 默认' : ''}`,
        value: p.id,
      })),
    ]
    templateOptions.value = [
      { label: '（不设置）', value: '' },
      ...(d?.templates || []).map((t: any) => ({ label: t.name, value: t.name })),
    ]
    adminOptions.value = (d?.admins || []).map((m: any) => ({
      label: m.roles?.length ? `${m.email}（${m.roles.join(' / ')}）` : m.email,
      value: m.uid,
    }))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取邮件通知配置失败')
  } finally {
    busy.value = false
  }
}

async function save() {
  busy.value = true
  try {
    const list = rows.value.map((r) => ({
      event: r.event,
      email_name: r.email_name,
      email_template: r.email_template,
      notify_personnel: r.notify_personnel,
      email_enable: r.email_enable,
    }))
    for (const item of rows.value) {
      if ((item.email_enable || item.email_name) && !item.email_template) {
        message.warning(`「${item.name_lang}」请先选择邮件模板`)
        return
      }
      if (item.email_enable && !item.notify_personnel.length) {
        message.warning(`「${item.name_lang}」启用前请选择通知人员`)
        return
      }
    }
    await api.put('/admin/email-notice-admin', { list })
    message.success('邮件通知配置已保存')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>邮件通知</h1>
        <p>对齐魔方「值邮件通知管理员」插件：为每个业务动作选择邮件接口、邮件模板与通知人员，事件发生时自动给选定员工发信。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary :loading="busy" @click="load">刷新</NButton>
        <NButton type="primary" :loading="busy" @click="save">保存</NButton>
      </div>
    </div>

    <section class="panel">
      <div class="panel-title-row">
        <div>
          <h2>事件规则</h2>
          <span>「邮件接口」留空时跟随系统当前默认通道；启用前必须选择邮件模板与通知人员。</span>
        </div>
      </div>
      <div v-if="rows.length" class="notice-table">
        <div class="notice-row notice-head">
          <span>动作名称</span>
          <span>邮件接口</span>
          <span>邮件模板</span>
          <span>通知人员</span>
          <span>启用</span>
        </div>
        <div v-for="r in rows" :key="r.event" class="notice-row">
          <div class="notice-name">
            <b>{{ r.name_lang }}</b>
            <small>{{ r.event }}</small>
          </div>
          <NSelect v-model:value="r.email_name" :options="providerOptions" size="small" />
          <NSelect v-model:value="r.email_template" :options="templateOptions" size="small" filterable />
          <NSelect v-model:value="r.notify_personnel" :options="adminOptions" size="small" multiple filterable max-tag-count="responsive" placeholder="选择通知人员" />
          <NSwitch v-model:value="r.email_enable" size="small" />
        </div>
      </div>
      <div v-else class="empty-box">加载中…若长时间为空，请检查是否已有邮件模板与后台账号。</div>
      <p class="muted" style="margin:12px 0 0">
        <NTag size="tiny" round :bordered="false">提示</NTag>
        站内工单回复、注册验证等自有邮件不受此配置影响；这里只增加「给管理员的事件通知邮件」。
      </p>
    </section>
  </div>
</template>

<style scoped>
.notice-table { display: flex; flex-direction: column; font-size: 12px; }
.notice-row {
  display: grid;
  grid-template-columns: minmax(150px, 1fr) 190px 210px minmax(240px, 1.5fr) 56px;
  gap: 10px;
  align-items: center;
  padding: 10px 0;
  border-bottom: 1px solid var(--border);
}
.notice-head { font-size: 10px; font-weight: 700; color: var(--muted); }
.notice-name { display: flex; flex-direction: column; min-width: 0; }
.notice-name small { color: var(--muted); font-size: 10px; margin-top: 2px; }
@media (max-width: 960px) {
  .notice-table { overflow-x: auto; }
  .notice-row { min-width: 920px; }
}
</style>
