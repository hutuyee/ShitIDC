<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NDatePicker, NRate, NSelect, useMessage } from 'naive-ui'
import { useRouter } from 'vue-router'
import { api, dataOf } from '../api'
import { ADMIN_PATH } from '../adminPath'

// 工单统计（对齐 TicketInternalPremium 的 ticket_statistics）：
// 按部门 / 个人与时间范围汇总单量、处理时长、评分与超时占比，并给出
// 平均分排名、平均处理时长排名（按部门或按个人）。

const message = useMessage()
const router = useRouter()
const departments = ref<any[]>([])
const staff = ref<any[]>([])
const scope = ref<'all' | 'department' | 'admin'>('all')
const departmentID = ref<number | null>(null)
const adminID = ref<number | null>(null)
const scoreRole = ref('')
const range = ref<[number, number] | null>(null)
const stats = ref<any>(null)
const scoreRank = ref<any[]>([])
const timeRank = ref<any[]>([])
const busy = ref(false)

const departmentOptions = computed(() => departments.value.map((d: any) => ({ label: d.name, value: d.id })))
const staffOptions = computed(() => staff.value.map((s: any) => ({ label: s.name, value: s.id })))

function params() {
  const p: Record<string, any> = { score_role: scoreRole.value || '' }
  if (scope.value === 'department' && departmentID.value) { p.type = 'department'; p.id = departmentID.value }
  else if (scope.value === 'admin' && adminID.value) { p.type = 'admin'; p.id = adminID.value }
  else p.type = 'all'
  if (range.value) {
    p.start_time = Math.floor(range.value[0] / 1000)
    p.end_time = Math.floor(range.value[1] / 1000)
  }
  return p
}

async function load() {
  busy.value = true
  try {
    const p = params()
    stats.value = dataOf<any>(await api.get('/admin/ticket-internal/statistics', { params: p }))
    const rankParams: Record<string, any> = { score_role: scoreRole.value || '' }
    if (range.value) {
      rankParams.start_time = p.start_time
      rankParams.end_time = p.end_time
    }
    if (scope.value === 'all') {
      const [s, t] = await Promise.all([
        api.get('/admin/ticket-internal/rank/department_score', { params: rankParams }),
        api.get('/admin/ticket-internal/rank/department_time', { params: rankParams }),
      ])
      scoreRank.value = dataOf<any>(s)?.rank || []
      timeRank.value = dataOf<any>(t)?.rank || []
    } else {
      if (scope.value === 'department' && departmentID.value) rankParams.department_id = departmentID.value
      const [s, t] = await Promise.all([
        api.get('/admin/ticket-internal/rank/person_score', { params: rankParams }),
        api.get('/admin/ticket-internal/rank/person_time', { params: rankParams }),
      ])
      scoreRank.value = dataOf<any>(s)?.rank || []
      timeRank.value = dataOf<any>(t)?.rank || []
    }
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取统计失败')
  } finally {
    busy.value = false
  }
}
async function loadBase() {
  try {
    const [dept, sf] = await Promise.all([
      api.get('/admin/ticket-internal/department'),
      api.get('/admin/ticket-internal/staff'),
    ])
    departments.value = dataOf<any>(dept)?.list || []
    staff.value = dataOf<any>(sf) || []
  } catch {
    message.error('读取部门 / 人员失败')
  }
}
onMounted(async () => { await loadBase(); load() })

function minutes(seconds: number) {
  if (!seconds) return '0'
  return (seconds / 60).toFixed(0)
}
function humanTime(seconds: number) {
  const s = Number(seconds) || 0
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor(((s % 86400) % 3600) / 60)
  const parts = []
  if (d) parts.push(`${d}天`)
  if (h) parts.push(`${h}小时`)
  if (m) parts.push(`${m}分钟`)
  if (!parts.length) parts.push(`${Math.round(s)}秒`)
  return parts.join('')
}
function rateDisplay(v: number) { return Math.min(5, Math.max(0, Number(v) || 0)) }
const scoreRoleLabel = computed(() => (scoreRole.value === '0' ? '发起人评分' : scoreRole.value === '1' ? '主管评分' : '全部评分'))
function maxTime() {
  return Math.max(1, ...timeRank.value.map((r: any) => Number(r.score) || 0))
}
</script>

<template>
  <div class="orders-page">
    <div class="dashboard-heading">
      <div>
        <div class="eyebrow">客服工具</div>
        <h1>工单统计</h1>
        <p>按部门 / 个人与时间范围统计单量、处理时长、评分与超时占比（{{ scoreRoleLabel }}）。</p>
      </div>
      <NButton secondary @click="router.push(ADMIN_PATH + '/ticket-internal')">返回内部工单</NButton>
    </div>

    <div class="users-toolbar" style="flex-wrap:wrap;row-gap:8px">
      <NDatePicker v-model:value="range" type="daterange" clearable style="width:260px" />
      <NSelect v-model:value="scoreRole" :options="[{ label: '全部评分', value: '' }, { label: '发起人评分', value: '0' }, { label: '主管评分', value: '1' }]" style="width:150px" />
      <NSelect v-model:value="scope" :options="[{ label: '所有部门', value: 'all' }, { label: '按部门', value: 'department' }, { label: '按个人', value: 'admin' }]" style="width:130px" />
      <NSelect v-if="scope === 'department'" v-model:value="departmentID" :options="departmentOptions" placeholder="部门" style="width:170px" />
      <NSelect v-if="scope === 'admin'" v-model:value="adminID" :options="staffOptions" filterable placeholder="人员" style="width:170px" />
      <NButton type="primary" :loading="busy" @click="load">搜索</NButton>
    </div>

    <div v-if="stats" class="card" style="margin-bottom:12px">
      <div class="row" style="flex-wrap:wrap;gap:24px;align-items:center">
        <div class="col" style="align-items:center;min-width:170px">
          <b style="font-size:26px">{{ (stats.avgrage_score || 0).toFixed(1) }}</b>
          <NRate :value="rateDisplay(stats.avgrage_score)" allow-half disabled color="#FFC329" />
          <span class="muted" style="font-size:12px">处理满意度 {{ (stats.satisfaction || 0).toFixed(1) }} · 服务态度 {{ (stats.attitude || 0).toFixed(1) }} · 处理时效 {{ (stats.processing_time || 0).toFixed(1) }}</span>
        </div>
        <div class="col" style="gap:6px;font-size:13px">
          <span>总单量：<b>{{ stats.total }}</b></span>
          <span>处理中：<b>{{ stats.pending_total }}</b></span>
          <span>已处理：<b>{{ stats.processed_total }}</b></span>
        </div>
        <div class="col" style="gap:6px;font-size:13px">
          <span>平均处理时长：<b>{{ minutes(stats.average_processing_time) }}</b> 分钟</span>
          <span>已评分工单：<b>{{ stats.score_total }}</b> · 未评分：<b>{{ stats.not_score_total }}</b></span>
          <span>评分占比：<b>{{ Math.round((stats.score_rate || 0) * 100) }}%</b> · 工单超时占比：<b>{{ Math.round((stats.overtime_ratio || 0) * 100) }}%</b></span>
        </div>
      </div>
    </div>

    <div class="row" style="gap:12px;align-items:flex-start;flex-wrap:wrap">
      <div class="card" style="flex:1;min-width:340px">
        <h3 style="margin:0 0 10px">{{ scope === 'all' ? '部门平均分排名' : '个人平均分排名' }}</h3>
        <div v-for="r in scoreRank" :key="'s-' + r.name" class="row" style="justify-content:space-between;padding:6px 0;border-bottom:1px dashed #f0f0f0">
          <span style="min-width:120px;font-weight:600">{{ r.name }}</span>
          <NRate :value="rateDisplay(r.score)" allow-half disabled color="#FFC329" size="small" />
          <span class="muted" style="font-size:12px">{{ Number(r.score).toFixed(2) }}（满意 {{ Number(r.satisfaction).toFixed(1) }} / 态度 {{ Number(r.attitude).toFixed(1) }} / 时效 {{ Number(r.processing_time).toFixed(1) }}）</span>
        </div>
        <div v-if="!scoreRank.length" class="muted">暂无评分数据</div>
      </div>
      <div class="card" style="flex:1;min-width:340px">
        <h3 style="margin:0 0 10px">{{ scope === 'all' ? '部门平均处理时长排名' : '个人平均处理时长排名' }}</h3>
        <div v-for="r in timeRank" :key="'t-' + r.name" class="col" style="gap:4px;padding:6px 0;border-bottom:1px dashed #f0f0f0">
          <div class="row" style="justify-content:space-between">
            <span style="font-weight:600">{{ r.name }}</span>
            <span class="muted" style="font-size:12px">{{ humanTime(r.score) }}</span>
          </div>
          <div style="height:8px;background:#f5f5f5;border-radius:4px;overflow:hidden">
            <div :style="{ width: Math.round((Number(r.score) || 0) / maxTime() * 100) + '%', height: '8px', background: '#2080f0' }"></div>
          </div>
        </div>
        <div v-if="!timeRank.length" class="muted">暂无处理时长数据</div>
      </div>
    </div>
  </div>
</template>
