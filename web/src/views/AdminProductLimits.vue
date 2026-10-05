<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInputNumber, NRadio, NRadioGroup, NSelect, NSwitch, NTabPane, NTabs, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 商品购买限制（对齐魔方 CBAP product_cert_limit / product_cycle_limit /
// product_related_limit 三个插件），三个页签共用一份商品列表。
// ProductNumLimit（单客户数量限制）与站内商品自带的「单客户最多购买」等价，
// 在「商品与分组」页配置，不在这里重复。

const message = useMessage()
const tab = ref('cert')
const products = ref<any[]>([])
const productOptions = computed(() => products.value.map((p: any) => ({ label: p.name, value: p.id })))

const certTypeList = [
  { value: 1, label: '个人/企业' },
  { value: 2, label: '个人认证' },
  { value: 3, label: '企业认证' },
]
const certTypeText = (t: number) => certTypeList.find(x => x.value === t)?.label || String(t)
const relatedTypeList = [
  { value: 0, label: '捆绑' },
  { value: 1, label: '必需' },
  { value: 2, label: '互斥' },
]
const relatedTypeText = (t: number) => relatedTypeList.find(x => x.value === t)?.label || String(t)

async function loadProducts() {
  try { products.value = dataOf<any[]>(await api.get('/admin/products')) || [] }
  catch { products.value = [] }
}

// ---------- 实名要求 ----------
const certs = ref<any[]>([])
const certForm = reactive({ product_id: '', type: 1 as number })
const certEditing = ref('')
async function loadCerts() {
  try { certs.value = dataOf<any[]>(await api.get('/admin/product-cert-limits')) || [] }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取实名要求失败') }
}
function certReset() { certEditing.value = ''; certForm.product_id = ''; certForm.type = 1 }
function certEdit(r: any) { certEditing.value = r.id; certForm.product_id = r.product_id; certForm.type = r.type }
async function certSave() {
  if (!certForm.product_id && !certEditing.value) { message.error('请选择商品'); return }
  try {
    if (certEditing.value) await api.put(`/admin/product-cert-limits/${certEditing.value}`, { type: certForm.type })
    else await api.post('/admin/product-cert-limits', { product_id: certForm.product_id, type: certForm.type })
    message.success(certEditing.value ? '实名要求已更新' : '实名要求已新增')
    certReset(); await loadCerts()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
}
async function certToggle(r: any) {
  try { await api.put(`/admin/product-cert-limits/${r.id}/status`, { status: !r.status }); await loadCerts() }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}
async function certRemove(r: any) {
  if (!window.confirm('确认删除该商品的实名要求？')) return
  try { await api.delete(`/admin/product-cert-limits/${r.id}`); message.success('已删除'); await loadCerts() }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---------- 周期性限购 ----------
const cycles = ref<any[]>([])
const cycleForm = reactive({ product_id: '', num: 1, cycle: 0 })
const cycleEditing = ref('')
async function loadCycles() {
  try { cycles.value = dataOf<any[]>(await api.get('/admin/product-cycle-limits')) || [] }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取周期性限购失败') }
}
function cycleReset() { cycleEditing.value = ''; cycleForm.product_id = ''; cycleForm.num = 1; cycleForm.cycle = 0 }
function cycleEdit(r: any) { cycleEditing.value = r.id; cycleForm.product_id = r.product_id; cycleForm.num = r.num; cycleForm.cycle = r.cycle }
async function cycleSave() {
  if (!cycleForm.product_id && !cycleEditing.value) { message.error('请选择商品'); return }
  if (!cycleForm.num || cycleForm.num < 1) { message.error('限制数量至少为 1'); return }
  try {
    const payload = { num: Math.round(cycleForm.num), cycle: Math.max(0, Math.round(cycleForm.cycle || 0)) }
    if (cycleEditing.value) await api.put(`/admin/product-cycle-limits/${cycleEditing.value}`, payload)
    else await api.post('/admin/product-cycle-limits', { product_id: cycleForm.product_id, ...payload })
    message.success(cycleEditing.value ? '周期性限购已更新' : '周期性限购已新增')
    cycleReset(); await loadCycles()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
}
async function cycleToggle(r: any) {
  try { await api.put(`/admin/product-cycle-limits/${r.id}/status`, { status: !r.status }); await loadCycles() }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}
async function cycleRemove(r: any) {
  if (!window.confirm('确认删除该商品的周期性限购？')) return
  try { await api.delete(`/admin/product-cycle-limits/${r.id}`); message.success('已删除'); await loadCycles() }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

// ---------- 关联限购 ----------
const relateds = ref<any[]>([])
const relatedForm = reactive({ product_id: '', related_product_id: [] as string[], type: 0 as number })
const relatedEditing = ref('')
async function loadRelateds() {
  try { relateds.value = dataOf<any[]>(await api.get('/admin/product-related-limits')) || [] }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取关联限购失败') }
}
function relatedReset() { relatedEditing.value = ''; relatedForm.product_id = ''; relatedForm.related_product_id = []; relatedForm.type = 0 }
function relatedEdit(r: any) { relatedEditing.value = r.id; relatedForm.product_id = r.product_id; relatedForm.related_product_id = [...(r.related_product_ids || [])]; relatedForm.type = r.type }
async function relatedSave() {
  if (!relatedForm.product_id && !relatedEditing.value) { message.error('请选择被限制商品'); return }
  if (!relatedForm.related_product_id.length) { message.error('请至少选择一个关联商品'); return }
  try {
    if (relatedEditing.value) await api.put(`/admin/product-related-limits/${relatedEditing.value}`, { related_product_id: relatedForm.related_product_id, type: relatedForm.type })
    else await api.post('/admin/product-related-limits', { product_id: relatedForm.product_id, related_product_id: relatedForm.related_product_id, type: relatedForm.type })
    message.success(relatedEditing.value ? '关联限购已更新' : '关联限购已新增')
    relatedReset(); await loadRelateds()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
}
async function relatedToggle(r: any) {
  try { await api.put(`/admin/product-related-limits/${r.id}/status`, { status: !r.status }); await loadRelateds() }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败') }
}
async function relatedRemove(r: any) {
  if (!window.confirm('确认删除该商品的关联限购？')) return
  try { await api.delete(`/admin/product-related-limits/${r.id}`); message.success('已删除'); await loadRelateds() }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

onMounted(() => { loadProducts(); loadCerts(); loadCycles(); loadRelateds() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head">
      <div>
        <div class="eyebrow">附属插件</div>
        <h1>商品购买限制</h1>
        <p>对齐魔方「商品实名要求 / 商品周期性限购 / 商品关联限购」插件：下单时由服务端强制校验。计数口径与插件一致——账户中的服务，已终止与开通失败的不计数，其余状态均计数。</p>
      </div>
    </div>

    <NTabs v-model:value="tab" type="line">
      <NTabPane name="cert" tab="实名要求">
        <div class="admin-two-col">
          <section class="panel admin-form-panel">
            <div class="panel-title-row"><div><h2>{{ certEditing ? '编辑实名要求' : '新增实名要求' }}</h2><span>指定商品只允许已实名用户购买</span></div></div>
            <div class="form-grid">
              <label class="full"><span>商品</span>
                <NSelect v-model:value="certForm.product_id" filterable :disabled="!!certEditing" :options="productOptions" placeholder="选择商品" />
              </label>
              <label class="full"><span>类型要求</span>
                <NSelect v-model:value="certForm.type" :options="certTypeList" />
              </label>
            </div>
            <p class="muted" style="margin:0">本站实名为统一类型（不区分个人 / 企业），三种要求都按「已通过实名」校验。</p>
            <div class="row" style="gap:8px">
              <NButton type="primary" @click="certSave">{{ certEditing ? '保存修改' : '添加' }}</NButton>
              <NButton v-if="certEditing" secondary @click="certReset">取消编辑</NButton>
            </div>
          </section>
          <section class="panel">
            <div class="panel-title-row"><div><h2>实名要求</h2><span>{{ certs.length }} 条</span></div></div>
            <div v-if="certs.length" class="table-scroll"><div class="audit-table">
              <div class="audit-row audit-head"><span>商品名称</span><span>类型要求</span><span>状态</span><span>操作</span></div>
              <div v-for="r in certs" :key="r.id" class="audit-row">
                <span><b>{{ r.product_name }}</b></span>
                <span>{{ certTypeText(r.type) }}</span>
                <span><NSwitch :value="!!r.status" size="small" @update:value="certToggle(r)" /></span>
                <span class="row" style="gap:6px">
                  <NButton size="tiny" tertiary @click="certEdit(r)">编辑</NButton>
                  <NButton size="tiny" tertiary type="error" @click="certRemove(r)">删除</NButton>
                </span>
              </div>
            </div></div>
            <div v-else class="empty-box">还没有商品实名要求。</div>
          </section>
        </div>
      </NTabPane>

      <NTabPane name="cycle" tab="周期性限购">
        <div class="admin-two-col">
          <section class="panel admin-form-panel">
            <div class="panel-title-row"><div><h2>{{ cycleEditing ? '编辑周期性限购' : '新增周期性限购' }}</h2><span>设定商品在周期内可拥有的最大数量</span></div></div>
            <div class="form-grid">
              <label class="full"><span>商品</span>
                <NSelect v-model:value="cycleForm.product_id" filterable :disabled="!!cycleEditing" :options="productOptions" placeholder="选择商品" />
              </label>
              <label><span>限制数量</span><NInputNumber v-model:value="cycleForm.num" :min="1" :step="1" style="width:100%" /></label>
              <label><span>限制周期（天，0=永久）</span><NInputNumber v-model:value="cycleForm.cycle" :min="0" :step="1" style="width:100%" /></label>
            </div>
            <p class="muted" style="margin:0">周期开始时间以用户未在限制内下的第一单时间为准开始计算；修改周期会影响正在限制中的周期。</p>
            <div class="row" style="gap:8px">
              <NButton type="primary" @click="cycleSave">{{ cycleEditing ? '保存修改' : '添加' }}</NButton>
              <NButton v-if="cycleEditing" secondary @click="cycleReset">取消编辑</NButton>
            </div>
          </section>
          <section class="panel">
            <div class="panel-title-row"><div><h2>周期性限购</h2><span>{{ cycles.length }} 条</span></div></div>
            <div v-if="cycles.length" class="table-scroll"><div class="audit-table">
              <div class="audit-row audit-head"><span>商品名称</span><span>限制数量</span><span>限制周期</span><span>状态</span><span>操作</span></div>
              <div v-for="r in cycles" :key="r.id" class="audit-row">
                <span><b>{{ r.product_name }}</b></span>
                <span>{{ r.num }} 件</span>
                <span class="muted">{{ r.cycle > 0 ? `${r.cycle} 天` : '永久' }}</span>
                <span><NSwitch :value="!!r.status" size="small" @update:value="cycleToggle(r)" /></span>
                <span class="row" style="gap:6px">
                  <NButton size="tiny" tertiary @click="cycleEdit(r)">编辑</NButton>
                  <NButton size="tiny" tertiary type="error" @click="cycleRemove(r)">删除</NButton>
                </span>
              </div>
            </div></div>
            <div v-else class="empty-box">还没有周期性限购。</div>
          </section>
        </div>
      </NTabPane>

      <NTabPane name="related" tab="关联限购">
        <div class="admin-two-col">
          <section class="panel admin-form-panel">
            <div class="panel-title-row"><div><h2>{{ relatedEditing ? '编辑关联限购' : '新增关联限购' }}</h2><span>配置商品购买 / 续费时的关联要求</span></div></div>
            <div class="form-grid">
              <label class="full"><span>被限制商品</span>
                <NSelect v-model:value="relatedForm.product_id" filterable :disabled="!!relatedEditing" :options="productOptions" placeholder="选择被限制商品" />
              </label>
              <label class="full"><span>限制商品（可多选）</span>
                <NSelect v-model:value="relatedForm.related_product_id" multiple filterable :options="productOptions" placeholder="选择关联商品" />
              </label>
              <label class="full"><span>限制类型</span>
                <NRadioGroup v-model:value="relatedForm.type">
                  <NRadio v-for="t in relatedTypeList" :key="t.value" :value="t.value">{{ t.label }}</NRadio>
                </NRadioGroup>
              </label>
            </div>
            <p class="muted" style="margin:0">捆绑：须与关联商品同时购买（一起结算）；必需：需先拥有激活中的关联商品（任一）；互斥：不得拥有激活中的关联商品。</p>
            <div class="row" style="gap:8px">
              <NButton type="primary" @click="relatedSave">{{ relatedEditing ? '保存修改' : '添加' }}</NButton>
              <NButton v-if="relatedEditing" secondary @click="relatedReset">取消编辑</NButton>
            </div>
          </section>
          <section class="panel">
            <div class="panel-title-row"><div><h2>关联限购</h2><span>{{ relateds.length }} 条</span></div></div>
            <div v-if="relateds.length" class="table-scroll"><div class="audit-table">
              <div class="audit-row audit-head"><span>被限制商品</span><span>限制商品</span><span>限制类型</span><span>状态</span><span>操作</span></div>
              <div v-for="r in relateds" :key="r.id" class="audit-row">
                <span><b>{{ r.product_name }}</b></span>
                <span class="muted">{{ r.related_product_names || '—' }}</span>
                <span>{{ relatedTypeText(r.type) }}</span>
                <span><NSwitch :value="!!r.status" size="small" @update:value="relatedToggle(r)" /></span>
                <span class="row" style="gap:6px">
                  <NButton size="tiny" tertiary @click="relatedEdit(r)">编辑</NButton>
                  <NButton size="tiny" tertiary type="error" @click="relatedRemove(r)">删除</NButton>
                </span>
              </div>
            </div></div>
            <div v-else class="empty-box">还没有关联限购。</div>
          </section>
        </div>
      </NTabPane>
    </NTabs>
  </div>
</template>
