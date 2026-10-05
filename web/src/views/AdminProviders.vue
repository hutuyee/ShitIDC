<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NCheckbox, NInput, NInputNumber, NModal, NSelect, NSwitch, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import EntityPicker from '../components/EntityPicker.vue'

const message = useMessage()
const providers = ref<any[]>([])
const selected = ref<any | null>(null)
const upstreamProducts = ref<any[]>([])
const busy = ref(false)
const advanced = ref(false)
const editingID = ref('')
const sellPrices = ref<Record<string, number | null>>({})
const form = ref({
  name: '', provider_type: 'magiccube', base_url: '', username: '', api_key: '', config_json: '',
  auth_mode: 'legacy_login', token_prefix: 'Bearer', allow_private: false,
  paths: { login: '/v1/login_api', products: '/v1/products', test: '/v1/products', create: '', suspend: '', unsuspend: '', terminate: '', renew: '' }
})

async function loadProviders() {
  providers.value = dataOf(await api.get('/admin/providers'))
  if (selected.value) selected.value = providers.value.find(x => x.id === selected.value.id) || null
}

async function saveProvider() {
  busy.value = true
  try {
    if (editingID.value) {
      await api.put(`/admin/providers/${editingID.value}`, form.value)
      message.success('供应商配置已更新，请重新测试连接。')
    } else {
      await api.post('/admin/providers', buildPayload())
      message.success('供应商已保存。接下来先“测试连接”。')
    }
    resetForm()
    await loadProviders()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '供应商保存失败') }
  finally { busy.value = false }
}

function editProvider(p: any) {
  editingID.value = p.id
  const cfg = p.config || {}
  const paths = cfg.paths || {}
  form.value = {
    name: p.name || '', provider_type: p.provider_type || 'magiccube', base_url: p.base_url || '', username: p.username || '', api_key: '', config_json: p.provider_type && p.provider_type !== 'magiccube' ? JSON.stringify(p.config || {}, null, 2) : '',
    auth_mode: cfg.auth_mode || 'legacy_login', token_prefix: cfg.token_prefix || 'Bearer', allow_private: Boolean(cfg.allow_private),
    paths: { login: paths.login || '/v1/login_api', products: paths.products || '/v1/products', test: paths.test || '/v1/products', create: paths.create || '', suspend: paths.suspend || '', unsuspend: paths.unsuspend || '', terminate: paths.terminate || '', renew: paths.renew || '' }
  }
  advanced.value = true
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

// 供应商类型选项：魔方财务（分销上游）或基础设施 Provider
const typeOptions = [
  { label: '智简魔方财务（上游）', value: 'magiccube' },
  { label: 'Proxmox VE', value: 'proxmox' },
  { label: 'Virtualizor', value: 'virtualizor' },
]
const isInfra = computed(() => form.value.provider_type === 'proxmox' || form.value.provider_type === 'virtualizor')

// 基础设施 Provider 的默认配置模板
const infraTemplates: Record<string, string> = {
  proxmox: JSON.stringify({ node: 'pve1', api_token_id: 'root@pam!shitidc', vm_type: 'lxc', ostemplate: 'local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst', storage: 'local-lvm', disk_gb: 10, cores: 1, memory: 512, bridge: 'vmbr0', password: '', allow_private: false }, null, 2),
  virtualizor: JSON.stringify({ serverid: '0', plan: '', osid: '', space_gb: 10, ram_mb: 512, cores: 1, bandwidth_gb: 0, allow_private: false }, null, 2),
}
function onTypeChange(v: string) {
  if (v === 'proxmox' || v === 'virtualizor') form.value.config_json = infraTemplates[v] || '{}'
}

function buildPayload() {
  const f = form.value
  if (!isInfra.value) return f
  let cfg: any = {}
  try { cfg = JSON.parse(f.config_json || '{}') } catch { throw new Error('配置 JSON 格式无效') }
  // virtualizor: key+pass 打包进 secret JSON
  const secret = f.provider_type === 'virtualizor' ? JSON.stringify({ api_key: cfg.api_key || f.api_key, api_pass: cfg.api_pass || '' }) : f.api_key
  return { ...f, api_key: secret, config: cfg }
}

function resetForm() {
  editingID.value = ''
  form.value = { name: '', provider_type: 'magiccube', base_url: '', username: '', api_key: '', config_json: '', auth_mode: 'legacy_login', token_prefix: 'Bearer', allow_private: false, paths: { login: '/v1/login_api', products: '/v1/products', test: '/v1/products', create: '', suspend: '', unsuspend: '', terminate: '', renew: '' } }
}

async function testProvider(p: any) {
  try { await api.post(`/admin/providers/${p.id}/test`); message.success(`${p.name} 连接正常`); await loadProviders() }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '连接失败'); await loadProviders() }
}

async function syncProvider(p: any) {
  busy.value = true
  try {
    const r = dataOf<any>(await api.post(`/admin/providers/${p.id}/sync`))
    message.success(`同步完成：${r.count ?? 0} 个上游商品`)
    await loadProviders(); await showProducts(p)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '同步失败') }
  finally { busy.value = false }
}

async function showProducts(p: any) {
  selected.value = p
  try {
    upstreamProducts.value = dataOf(await api.get(`/admin/providers/${p.id}/products`))
    sellPrices.value = Object.fromEntries(upstreamProducts.value.map((item: any) => [item.upstream_product_id, Number(((Number(item.price_cents || 0) / 100) * 1.10).toFixed(2))]))
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取上游产品失败') }
}

async function importProduct(item: any) {
  if (!selected.value) return
  try {
    await api.post(`/admin/providers/${selected.value.id}/products/${encodeURIComponent(item.upstream_product_id)}/import`, {
      name: item.name,
      description: item.description,
      billing_cycle: item.billing_cycle || 'monthly',
      currency: item.currency || 'CNY',
      amount_cents: Math.max(0, Math.round(Number(sellPrices.value[item.upstream_product_id] ?? (item.price_cents / 100)) * 100))
    })
    message.success(`已导入 ${item.name}，上游 ID 已自动保存，不需要手填。`)
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '导入失败') }
}

const money = (p: any) => `${p.currency === 'CNY' ? '¥' : (p.currency || 'CNY') + ' '}${(Number(p.price_cents || 0) / 100).toFixed(2)}`
const statusText = (s: string) => ({ online: '连接正常', error: '连接异常', unknown: '未测试' } as any)[s] || s

// ---- 魔方插件导入（zip → custom 供应商）与规格编辑 ----
const zjmfModal = ref(false)
const zjmfFile = ref<File | null>(null)
const zjmfPreview = ref<any | null>(null)
const zjmfForm = ref({ base_url: '', token: '' })
const zjmfBusy = ref(false)
const specModal = ref(false)
const specText = ref('')
const specProvider = ref<any | null>(null)

const authName: Record<string, string> = {
  md5sort_upper: 'MD5 排序大写（bthosts/nokvm）',
  md5concat: 'MD5 拼接（kangle 系）',
  unsupported: '无法转换（如 PVE ticket）',
}
const actionName: Record<string, string> = {
  test: '连接测试', create: '开通', suspend: '暂停', unsuspend: '解除暂停',
  terminate: '删除', renew: '续费', change_package: '升降级', sync: '同步',
  status: '状态', password: '改密',
}

function pickZJMFFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  zjmfFile.value = input.files?.[0] || null
  zjmfPreview.value = null
}

async function previewZJMF() {
  if (!zjmfFile.value) { message.warning('请先选择魔方插件 zip'); return }
  zjmfBusy.value = true
  try {
    const fd = new FormData()
    fd.append('file', zjmfFile.value)
    const d = dataOf<any>(await api.post('/admin/providers/zjmf-import', fd, { headers: { 'Content-Type': 'multipart/form-data' } }))
    zjmfPreview.value = d.spec
    message.success('转换完成，请检查识别结果后填写上游信息')
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '转换失败') }
  finally { zjmfBusy.value = false }
}

async function applyZJMF() {
  if (!zjmfFile.value || !zjmfPreview.value) return
  if (!zjmfForm.value.base_url || !zjmfForm.value.token) { message.warning('请填写上游接口地址与接口密钥'); return }
  zjmfBusy.value = true
  try {
    const fd = new FormData()
    fd.append('file', zjmfFile.value)
    fd.append('apply', 'true')
    fd.append('base_url', zjmfForm.value.base_url)
    fd.append('token', zjmfForm.value.token)
    await api.post('/admin/providers/zjmf-import', fd, { headers: { 'Content-Type': 'multipart/form-data' } })
    message.success('魔方插件已导入为供应商，建议先「测试连接」再绑定商品')
    zjmfModal.value = false
    zjmfFile.value = null; zjmfPreview.value = null
    zjmfForm.value = { base_url: '', token: '' }
    await loadProviders()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '导入失败') }
  finally { zjmfBusy.value = false }
}

async function openSpec(p: any) {
  specProvider.value = p
  try {
    const d = dataOf<any>(await api.get(`/admin/providers/${p.id}/spec`))
    specText.value = JSON.stringify(d, null, 2)
    specModal.value = true
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '读取规格失败') }
}

async function saveSpec() {
  if (!specProvider.value) return
  try {
    const parsed = JSON.parse(specText.value)
    await api.put(`/admin/providers/${specProvider.value.id}/spec`, { spec: parsed })
    message.success('规格已保存')
    specModal.value = false
    await loadProviders()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || (e instanceof SyntaxError ? '规格不是合法 JSON' : '保存失败'))
  }
}

// ---- 魔方（ZJMF）上游访问密钥：把 ShitIDC 当成魔方财务的上游接口 ----
const upstreamKeys = ref<any[]>([]);
const upstreamMeta = ref<any>({ base_url: '', test_path: '', host_path: '', prod_path: '' });
const keyForm = ref({ name: '魔方财务接口', user_id: '' });
const newKey = ref<any>(null);

const panelBase = computed(() => upstreamMeta.value.base_url || window.location.origin);
const testURL = computed(() => `${panelBase.value}${upstreamMeta.value.test_path || '/compat/magiccube/v1/test'}`);

async function loadUpstreamKeys() {
  try {
    const d = dataOf<any>(await api.get('/admin/upstream-keys'));
    upstreamKeys.value = d.keys || [];
    upstreamMeta.value = d;
  } catch { upstreamKeys.value = []; }
}

async function createUpstreamKey() {
  busy.value = true;
  try {
    const d = dataOf<any>(await api.post('/admin/upstream-keys', { name: keyForm.value.name, user_id: keyForm.value.user_id }));
    newKey.value = d;
    message.success('接口密钥已生成，请立刻复制到魔方后台');
    keyForm.value = { name: '魔方财务接口', user_id: '' };
    await loadUpstreamKeys();
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '生成失败'); }
  finally { busy.value = false; }
}

async function toggleUpstreamKey(k: any) {
  try {
    await api.put(`/admin/upstream-keys/${k.id}`, { active: !k.active });
    message.success(k.active ? '已停用' : '已启用');
    await loadUpstreamKeys();
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '操作失败'); }
}

async function removeUpstreamKey(k: any) {
  try {
    await api.delete(`/admin/upstream-keys/${k.id}`);
    message.success('已删除');
    await loadUpstreamKeys();
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败'); }
}

async function copyText(text: string, label: string) {
  try { await navigator.clipboard.writeText(text); message.success(label + ' 已复制'); }
  catch { message.error('复制失败，请手动选择复制'); }
}

onMounted(() => { loadProviders(); loadUpstreamKeys(); })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">上下游管理</div><h1>供应商 / 魔方上游</h1><p>先保存上游网站、账号和 API Key，再测试连接、同步商品并一键导入。上游产品 ID 由系统自动维护。</p></div></div>

    <div class="admin-two-col">
      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>添加供应商</h2><span>适用于智简魔方财务上游</span></div></div>
        <div class="form-grid">
          <label><span>供应商名称</span><NInput v-model:value="form.name" placeholder="例如：香港上游 A" /></label>
          <label><span>供应商类型</span><NSelect v-model:value="form.provider_type" :options="typeOptions" @update:value="onTypeChange" /></label>
          <label class="full"><span>接口地址 / 上游网站</span><NInput v-model:value="form.base_url" placeholder="https://idc.example.com" /><small>填写上游魔方财务完整域名，不是产品 ID。</small></label>
          <label v-if="!isInfra"><span>上游账号</span><NInput v-model:value="form.username" placeholder="邮箱 / 手机号" /></label>
          <label v-if="!isInfra" class="full"><span>API Key</span><NInput v-model:value="form.api_key" type="password" show-password-on="click" :placeholder="editingID ? '留空表示继续使用原密钥' : '上游账户 API 管理中生成的密钥'" /><small v-if="editingID">为安全起见不会回显已保存的密钥。</small></label>
          <template v-if="form.provider_type === 'proxmox'">
            <label class="full"><span>API Token（Token ID 之后的密钥值）</span><NInput v-model:value="form.api_key" type="password" show-password-on="click" :placeholder="editingID ? '留空表示继续使用原密钥' : 'PVE API Token 密钥值（UUID）'" /><small>在 PVE → 权限 → API Token 中创建，配置 JSON 里填 node 与 api_token_id（形如 root@pam!shitidc）。</small></label>
            <label class="full"><span>Proxmox 配置（JSON）</span><NInput v-model:value="form.config_json" type="textarea" :rows="6" /></label>
          </template>
          <template v-if="form.provider_type === 'virtualizor'">
            <label class="full"><span>API Key / API Pass</span><NInput v-model:value="form.api_key" type="password" show-password-on="click" :placeholder="editingID ? '留空表示继续使用原密钥' : 'API Key 与 API Pass（JSON，见说明）'" /><small>Virtualizor 管理后台 → API 凭证。HMAC-SHA256 签名由系统自动生成。</small></label>
            <label class="full"><span>Virtualizor 配置（JSON）</span><NInput v-model:value="form.config_json" type="textarea" :rows="6" /></label>
          </template>
        </div>
        <div class="advanced-row"><div><b>高级兼容设置</b><small>不同魔方版本路径不一致时再修改</small></div><NSwitch v-model:value="advanced" /></div>
        <div v-if="advanced" class="advanced-box form-grid">
          <label><span>鉴权模式</span><NSelect v-model:value="form.auth_mode" :options="[{label:'魔方 v1 登录换 JWT',value:'legacy_login'},{label:'Bearer API Key',value:'bearer'},{label:'Basic Auth',value:'basic'},{label:'自定义 Header',value:'headers'}]" /></label>
          <label><span>Token 前缀</span><NInput v-model:value="form.token_prefix" placeholder="Bearer" /></label>
          <label><span>登录路径</span><NInput v-model:value="form.paths.login" /></label>
          <label><span>商品列表路径</span><NInput v-model:value="form.paths.products" /></label>
          <label><span>资源创建路径</span><NInput v-model:value="form.paths.create" placeholder="按上游版本填写；不填时仅同步商品" /></label>
          <label><span>暂停路径</span><NInput v-model:value="form.paths.suspend" /></label>
          <label><span>解除暂停路径</span><NInput v-model:value="form.paths.unsuspend" /></label>
          <label><span>终止路径</span><NInput v-model:value="form.paths.terminate" /></label>
          <label><span>续费路径</span><NInput v-model:value="form.paths.renew" /></label>
          <label class="full inline-check"><NCheckbox v-model:checked="form.allow_private" />允许访问私网地址（仅上游确实部署在内网时开启）</label>
        </div>
        <div class="security-note">API Key 会使用服务器的 <code>MASTER_KEY_BASE64</code> 加密后写入数据库，列表接口不会返回明文密钥。</div>
        <div class="form-actions"><NButton type="primary" size="large" :loading="busy" @click="saveProvider">{{ editingID ? '保存修改' : '保存供应商' }}</NButton><NButton v-if="editingID" size="large" @click="resetForm">取消编辑</NButton></div>
      </section>

      <section class="panel provider-help">
        <div class="panel-title-row"><div><h2>正确对接顺序</h2><span>不用再手动找产品 ID</span></div></div>
        <ol class="step-list"><li><b>在上游魔方开启资源 API</b><span>在上游账户 API 管理中生成 API Key。</span></li><li><b>把上游网站添加到这里</b><span>填写域名、你的上游账号、API Key。</span></li><li><b>测试连接</b><span>ShitIDC 会真实请求上游 API。</span></li><li><b>同步产品</b><span>上游商品 ID、名称、价格和原始响应写入映射表。</span></li><li><b>一键导入</b><span>本地商品自动绑定对应供应商与上游商品 ID。</span></li></ol>
      </section>
    </div>

    <section class="panel provider-list-panel">
      <div class="panel-title-row"><div><h2>已配置供应商</h2><span>{{ providers.length }} 个供应商</span></div><NButton size="small" type="primary" secondary @click="zjmfModal = true">导入魔方插件</NButton></div>
      <div v-if="providers.length" class="provider-list">
        <article v-for="p in providers" :key="p.id" class="provider-row">
          <div class="provider-logo">{{ p.provider_type === 'custom' ? 'Z' : 'M' }}</div>
          <div class="provider-main"><div><b>{{ p.name }}</b><span class="provider-kind">{{ p.provider_type === 'custom' ? '魔方插件导入' : '魔方财务' }}</span></div><small>{{ p.base_url }}</small><small>{{ p.username }}</small></div>
          <div class="provider-state"><span class="state-pill" :class="p.status">● {{ statusText(p.status) }}</span><small v-if="p.last_checked_at">检测 {{ new Date(p.last_checked_at).toLocaleString() }}</small><small v-if="p.last_error" class="error-text">{{ p.last_error }}</small></div>
          <div class="provider-actions"><NButton size="small" @click="testProvider(p)">测试连接</NButton><NButton v-if="p.provider_type === 'custom'" size="small" secondary @click="openSpec(p)">查看规格</NButton><NButton v-if="p.provider_type !== 'custom'" size="small" type="primary" :loading="busy" @click="syncProvider(p)">同步产品</NButton><NButton v-if="p.provider_type !== 'custom'" size="small" secondary @click="showProducts(p)">查看商品</NButton><NButton size="small" tertiary @click="editProvider(p)">编辑</NButton></div>
        </article>
      </div>
      <div v-else class="empty-box">还没有上游供应商。先在上面的表单添加一个魔方财务网站。</div>
    </section>

    <section v-if="selected" class="panel upstream-products-panel">
      <div class="panel-title-row"><div><h2>{{ selected.name }} · 上游商品</h2><span>同步后可直接导入到 ShitIDC 商品中心</span></div><NButton size="small" @click="syncProvider(selected)">重新同步</NButton></div>
      <div v-if="upstreamProducts.length" class="upstream-table">
        <div class="upstream-row upstream-head"><span>上游 ID</span><span>商品</span><span>周期</span><span>上游价格</span><span>销售价</span><span>操作</span></div>
        <div v-for="item in upstreamProducts" :key="item.upstream_product_id" class="upstream-row"><code>{{ item.upstream_product_id }}</code><div><b>{{ item.name }}</b><small>{{ item.description || '无描述' }}</small></div><span>{{ item.billing_cycle }}</span><strong>{{ money(item) }}</strong><NInputNumber v-model:value="sellPrices[item.upstream_product_id]" :min="0" :precision="2" size="small"><template #prefix>¥</template></NInputNumber><NButton size="small" type="primary" @click="importProduct(item)">导入商品</NButton></div>
      </div>
      <div v-else class="empty-box">还没有同步到商品。点击“重新同步”从上游读取。</div>
    </section>

    <section class="panel">
      <div class="panel-title-row">
        <div><h2>作为魔方财务的上游（对接魔方商品管理）</h2><span>在魔方后台“接口设置”里添加接口，商品管理即可选择“ShitIDC”模块</span></div>
        <NButton size="small" secondary @click="loadUpstreamKeys">刷新</NButton>
      </div>
      <div class="form-grid">
        <label><span>接口名称</span><NInput v-model:value="keyForm.name" placeholder="例如：主站魔方对接" /></label>
        <label><span>归属用户（可选）</span><EntityPicker
            v-model="keyForm.user_id"
            kind="user"
            title="选择密钥归属用户"
            placeholder="留空 = 归属当前管理员，点此搜索用户"
          /><small>魔方通过这把密钥开通的服务记在该用户名下，并从其余额扣费。</small></label>
      </div>
      <div class="form-actions"><NButton type="primary" :loading="busy" @click="createUpstreamKey">生成接口密钥</NButton></div>

      <div v-if="newKey" class="security-note" style="border-left:3px solid #d03050">
        <b>请立刻复制，密钥只显示这一次。</b>
        <div class="row" style="gap:8px;margin-top:8px;align-items:center;flex-wrap:wrap">
          <code style="word-break:break-all">{{ newKey.access_hash }}</code>
          <NButton size="tiny" @click="copyText(newKey.access_hash, 'Hash')">复制 Hash</NButton>
          <NButton size="tiny" @click="copyText(newKey.key_id, 'Key ID')">复制 Key ID</NButton>
          <NButton size="tiny" @click="copyText(newKey.secret, 'Secret')">复制 Secret</NButton>
        </div>
      </div>

      <ol class="step-list" style="margin-top:14px">
        <li><b>在魔方后台添加接口</b><span>接口设置 → 添加接口：接口地址填 <code>{{ panelBase }}</code>，Hash 填上面生成的 <b>Key ID.Secret</b>，接口类型选 ShitIDC 模块。</span></li>
        <li><b>测试连接</b><span>模块会请求 <code>{{ testURL }}</code>，签名通过即显示正常。</span></li>
        <li><b>商品管理里绑定模块</b><span>新建商品 → 接口类型选“ShitIDC”，接口分组选刚才的接口，模块配置里选择要卖的商品。</span></li>
        <li><b>前台下单自动开通</b><span>魔方付款后调用本系统开通接口，余额不足会返回明确原因并挂起任务。</span></li>
      </ol>

      <div v-if="upstreamKeys.length" class="table-scroll" style="margin-top:12px"><div class="audit-table">
        <div class="audit-row audit-head"><span>名称</span><span>Key ID</span><span>归属</span><span>最近调用</span><span>状态</span><span>操作</span></div>
        <div v-for="k in upstreamKeys" :key="k.id" class="audit-row">
          <span><b>{{ k.name }}</b></span>
          <span><code>{{ k.key_id }}</code></span>
          <span class="muted">{{ k.user_email || k.user_uid }}</span>
          <span class="muted">{{ k.last_used_at ? new Date(k.last_used_at).toLocaleString() : '从未调用' }}<small v-if="k.last_error" class="error-text">{{ k.last_error }}</small></span>
          <span><NTag :type="k.active ? 'success' : 'default'" size="small" round>{{ k.active ? '启用' : '停用' }}</NTag></span>
          <span class="row" style="gap:6px">
            <NButton size="tiny" tertiary @click="toggleUpstreamKey(k)">{{ k.active ? '停用' : '启用' }}</NButton>
            <NButton size="tiny" tertiary type="error" @click="removeUpstreamKey(k)">删除</NButton>
          </span>
        </div>
      </div></div>
      <div v-else class="empty-box">还没有生成接口密钥。</div>
    </section>

    <NModal v-model:show="zjmfModal" preset="card" title="导入魔方 server 插件" style="max-width:640px">
      <div class="stack" style="gap:12px">
        <p class="muted" style="margin:0">
          上传魔方财务的 server 模块 zip（public/plugins/servers/&lt;标识&gt;/ 打包，或应用商店下载的插件包）。
          系统会静态解析 PHP 源码并转换成声明式上游规格：识别签名算法、生命周期路径与配置项；
          识别不了的部分会列在警告里，导入后可点「查看规格」继续补充。
        </p>
        <label class="stack" style="gap:4px"><span>插件包 zip</span><input type="file" accept=".zip" @change="pickZJMFFile" /></label>
        <NButton size="small" :loading="zjmfBusy && !zjmfPreview" @click="previewZJMF">转换预览</NButton>

        <div v-if="zjmfPreview" class="security-note" style="margin:0">
          <b>{{ zjmfPreview.display_name || zjmfPreview.slug }} <span v-if="zjmfPreview.source_version" class="muted">v{{ zjmfPreview.source_version }}</span></b>
          <div class="row" style="gap:6px;flex-wrap:wrap;margin-top:6px">
            <NTag size="small" round>{{ authName[zjmfPreview.auth?.scheme] || zjmfPreview.auth?.scheme }}</NTag>
            <NTag v-for="(a, k) in zjmfPreview.actions" :key="k" size="small" :type="a.path ? 'success' : 'warning'" round>{{ actionName[k] || k }}{{ a.path ? '' : '（路径缺失）' }}</NTag>
          </div>
          <div v-if="zjmfPreview.config_options?.length" style="margin-top:6px">识别到 {{ zjmfPreview.config_options.length }} 个产品配置项（导入后可在商品配置项里用 provider_key 映射上游参数）。</div>
          <ul v-if="zjmfPreview.warnings?.length" style="margin:8px 0 0;padding-left:18px">
            <li v-for="(w, i) in zjmfPreview.warnings" :key="i" class="muted">{{ w }}</li>
          </ul>
        </div>

        <template v-if="zjmfPreview">
          <div class="form-grid">
            <label class="full"><span>上游接口地址</span><NInput v-model:value="zjmfForm.base_url" placeholder="https://1.2.3.4:8888" /><small>由魔方接口设置里的 IP/端口/SSL 组成。</small></label>
            <label class="full"><span>接口密钥</span><NInput v-model:value="zjmfForm.token" type="password" show-password-on="click" placeholder="accesshash（nokvm 类模块填服务器密码）" /></label>
          </div>
          <div class="row" style="justify-content:flex-end;gap:8px">
            <NButton size="small" @click="zjmfModal = false">取消</NButton>
            <NButton size="small" type="primary" :loading="zjmfBusy" @click="applyZJMF">导入为供应商</NButton>
          </div>
        </template>
      </div>
    </NModal>

    <NModal v-model:show="specModal" preset="card" :title="`规格 · ${specProvider?.name || ''}`" style="max-width:760px">
      <div class="stack" style="gap:12px">
        <p class="muted" style="margin:0">声明式上游规格（JSON）。转换器没识别出的路径/参数可以在这里直接补。保存后建议重新「测试连接」。</p>
        <NInput v-model:value="specText" type="textarea" :rows="20" style="font-family:monospace" />
        <div class="row" style="justify-content:flex-end;gap:8px">
          <NButton size="small" @click="specModal = false">取消</NButton>
          <NButton size="small" type="primary" @click="saveSpec">保存规格</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>
