<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NModal, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 电子合同（用户端）：对已支付订单申请合同、签订（手写签名板 → 签名图）、
// 我的合同与下载。功能未开启或订单超时不可申请（后端判定）。

const message = useMessage()
const orders = ref<any[]>([])
const contracts = ref<any[]>([])
const busy = ref(false)
const money = (cents: number) => `¥${(Number(cents || 0) / 100).toFixed(2)}`
const fmt = (v?: string | null) => (v ? new Date(v).toLocaleString() : '—')
const statusText: Record<string, string> = { pending: '待签订', signed: '待审核', effective: '已生效', reject: '已驳回', cancel: '已作废' }
const statusType = (s: string) => ({ pending: 'warning', signed: 'info', effective: 'success', reject: 'error', cancel: 'default' } as any)[s] || 'default'

async function load() {
  busy.value = true
  try {
    const [o, cs] = await Promise.all([
      api.get('/e-contract/orders'),
      api.get('/e-contracts/my'),
    ])
    orders.value = dataOf<any>(o)?.list || []
    contracts.value = dataOf<any>(cs)?.list || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取合同信息失败')
  } finally {
    busy.value = false
  }
}

async function apply(o: any) {
  try {
    const d = dataOf<any>(await api.post('/e-contracts/apply', { order_id: o.id }))
    message.success(`合同已创建（编号 ${d.number}），请完成签订`)
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '申请合同失败')
  }
}

// ---- 签订（手写签名板） ----
const signFor = ref<any>(null)
// 同 AdminEContracts：v-model 的值必须是可赋值表达式，用可写 computed 包装。
const showSign = computed({ get: () => !!signFor.value, set: (v: boolean) => { if (!v) signFor.value = null } })
const canvas = ref<HTMLCanvasElement | null>(null)
let drawing = false
function openSign(c: any) {
  signFor.value = c
  setTimeout(() => initCanvas(), 50)
}
function initCanvas() {
  const cv = canvas.value
  if (!cv) return
  const ctx = cv.getContext('2d')!
  ctx.fillStyle = '#fff'
  ctx.fillRect(0, 0, cv.width, cv.height)
  ctx.lineWidth = 2.4
  ctx.lineCap = 'round'
  ctx.strokeStyle = '#111'
}
function pos(e: MouseEvent | TouchEvent): [number, number] {
  const cv = canvas.value!
  const rect = cv.getBoundingClientRect()
  const src = 'touches' in e ? e.touches[0] : e
  return [(src.clientX - rect.left) * (cv.width / rect.width), (src.clientY - rect.top) * (cv.height / rect.height)]
}
function startDraw(e: MouseEvent | TouchEvent) {
  e.preventDefault()
  drawing = true
  const ctx = canvas.value!.getContext('2d')!
  ctx.beginPath()
  ctx.moveTo(...pos(e))
}
function moveDraw(e: MouseEvent | TouchEvent) {
  if (!drawing) return
  e.preventDefault()
  const ctx = canvas.value!.getContext('2d')!
  ctx.lineTo(...pos(e))
  ctx.stroke()
}
function endDraw() { drawing = false }
function clearCanvas() { initCanvas() }
async function submitSign() {
  const dataURL = canvas.value!.toDataURL('image/png')
  try {
    await api.post(`/e-contracts/my/${signFor.value.id}/sign`, { sign_image: dataURL })
    message.success('签订完成，等待管理员审核')
    signFor.value = null
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '签订失败')
  }
}

async function download(c: any) {
  try {
    const resp = await api.get(`/e-contracts/my/${c.id}/download`, { responseType: 'blob' })
    const url = URL.createObjectURL(resp.data)
    const link = document.createElement('a')
    link.href = url
    link.download = `contract-${c.number}.html`
    link.click()
    URL.revokeObjectURL(url)
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '下载失败')
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <div class="eyebrow">合同</div>
        <h1>电子合同</h1>
        <p>对已支付的订单申请电子合同，完成签订后可下载合同文件。</p>
      </div>
      <NButton secondary :loading="busy" @click="load">刷新</NButton>
    </div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>可申请合同的订单</h2><span>已支付且未申请过合同的订单</span></div></div>
      <div v-if="orders.length" class="table-scroll"><div class="user-table">
        <div class="user-row ec-o-row user-head">
          <span>订单</span><span>商品</span><span>金额</span><span>下单时间</span><span>操作</span>
        </div>
        <div v-for="o in orders" :key="o.id" class="user-row ec-o-row">
          <span><b>{{ o.id.slice(0, 8) }}</b></span>
          <span class="muted">{{ o.product_name || '—' }}</span>
          <span>{{ money(o.total_cents) }}</span>
          <span class="muted">{{ fmt(o.created_at) }}</span>
          <span>
            <NButton v-if="!o.applied" size="tiny" type="primary" @click="apply(o)">申请合同</NButton>
            <NTag v-else size="tiny" round>已申请</NTag>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">暂时没有可申请合同的订单（需已支付、未申请过、在申请时间范围内）。</div>
    </section>

    <section class="panel">
      <div class="panel-title-row"><div><h2>我的合同</h2><span>共 {{ contracts.length }} 份</span></div></div>
      <div v-if="contracts.length" class="table-scroll"><div class="user-table">
        <div class="user-row ec-c-row user-head">
          <span>合同编号</span><span>模板</span><span>状态</span><span>申请时间</span><span>签订时间</span><span>操作</span>
        </div>
        <div v-for="c in contracts" :key="c.id" class="user-row ec-c-row">
          <span><b>{{ c.number }}</b></span>
          <span class="muted">{{ c.template_name }}</span>
          <span><NTag :type="statusType(c.status)" size="tiny" round>{{ statusText[c.status] || c.status }}</NTag></span>
          <span class="muted">{{ fmt(c.created_at) }}</span>
          <span class="muted">{{ fmt(c.signed_at) }}</span>
          <span class="row" style="gap:4px">
            <NButton v-if="c.status === 'pending'" size="tiny" type="primary" @click="openSign(c)">签订</NButton>
            <NButton v-if="c.status !== 'reject' && c.status !== 'cancel'" size="tiny" tertiary @click="download(c)">下载</NButton>
            <span v-if="c.reason" class="muted" style="font-size:12px">{{ c.reason }}</span>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有合同，从上方订单列表申请。</div>
    </section>

    <!-- 签名弹窗 -->
    <NModal v-model:show="showSign" preset="card" :title="`签订合同 ${signFor?.number || ''}`" style="width:min(640px,94vw)">
      <div class="sign-tip muted" style="font-size:13px;margin-bottom:8px">请在下方方框内手写签名，确认后提交（提交后进入管理员审核）。</div>
      <canvas
        ref="canvas" width="560" height="220"
        style="width:100%;border:1px dashed #bbb;border-radius:8px;touch-action:none;cursor:crosshair"
        @mousedown="startDraw" @mousemove="moveDraw" @mouseup="endDraw" @mouseleave="endDraw"
        @touchstart="startDraw" @touchmove="moveDraw" @touchend="endDraw"
      ></canvas>
      <div class="row" style="gap:8px;margin-top:14px">
        <NButton type="primary" @click="submitSign">确认签订</NButton>
        <NButton secondary @click="clearCanvas">清除重写</NButton>
        <NButton tertiary @click="signFor = null">取消</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.ec-o-row { grid-template-columns: minmax(110px, .9fr) minmax(140px, 1fr) 100px 160px 110px; }
.ec-c-row { grid-template-columns: minmax(110px, .9fr) minmax(120px, .8fr) 90px 150px 150px minmax(150px, 1fr); }
</style>
