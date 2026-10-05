<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NCheckbox, NInput, NSelect, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

const message = useMessage()
const providers = ref<any[]>([])
const busy = ref(false)
const editingID = ref('')

// 支付渠道来自后端 PaymentProvider 注册表（第七阶段），当前内置 epay。
const methodOptions = ref([{ label: '易支付（epay）', value: 'epay' }])
async function loadSupported() {
  try {
    const methods = dataOf<{ methods: string[] }>(await api.get('/admin/payment-methods/supported')).methods || []
    const methodLabels: Record<string, string> = {
      epay: '易支付（epay）', alipay: '支付宝官方（alipay）', stripe: 'Stripe',
      wechatpay: '微信支付官方（wechatpay）', paypal: 'PayPal', usdt: 'USDT（Epusdt）',
      xunhupay: '虎皮椒聚合支付（xunhupay）',
      goallpay: 'GoAllPay 全球聚合支付（goallpay）',
      ocgcpay: 'OCGC 酷云支付（ocgcpay）',
    }
    methodOptions.value = methods.map((m: string) => ({ label: methodLabels[m] || m, value: m }))
  } catch { /* keep default */ }
}

const payTypeOptions = [
  { label: '支付宝', value: 'alipay' },
  { label: '微信支付', value: 'wxpay' },
  { label: 'QQ 钱包', value: 'qqpay' },
  { label: '网银', value: 'bank' },
  { label: 'PayPal', value: 'paypal' },
  { label: 'USDT', value: 'usdt' },
]

const form = ref({ name: '', method: 'epay', gateway_url: '', merchant_id: '', secret: '', pay_types: ['alipay', 'wxpay'] as string[], active: true })

async function load() {
  try {
    providers.value = dataOf(await api.get('/admin/payment-providers'))
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取支付方式失败')
  }
}

function resetForm() {
  editingID.value = ''
  form.value = { name: '', method: 'epay', gateway_url: '', merchant_id: '', secret: '', pay_types: ['alipay', 'wxpay'], active: true }
}

function edit(p: any) {
  editingID.value = p.id
  const types = (p.config?.pay_types || []).map(String)
  form.value = { name: p.name || '', method: p.method || 'epay', gateway_url: p.gateway_url || '', merchant_id: p.merchant_id || '', secret: '', pay_types: types.length ? types : ['alipay', 'wxpay'], active: Boolean(p.active) }
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function save() {
  if (!form.value.name.trim()) { message.error('请填写名称'); return }
  busy.value = true
  try {
    if (editingID.value) {
      await api.put(`/admin/payment-providers/${editingID.value}`, form.value)
      message.success('支付方式已更新')
    } else {
      if (!form.value.secret.trim()) { message.error('请填写商户密钥'); busy.value = false; return }
      await api.post('/admin/payment-providers', form.value)
      message.success('支付方式已保存，用户下单时即可选择在线支付')
    }
    resetForm()
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '保存失败')
  } finally {
    busy.value = false
  }
}

onMounted(() => { load(); loadSupported() })
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">支付渠道</div><h1>在线支付方式</h1><p>支持易支付（Epay）协议的聚合支付平台。配置后用户可用余额支付或跳转在线支付，回调自动验证签名并开通服务。</p></div></div>

    <div class="admin-two-col">
      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>{{ editingID ? '编辑支付方式' : '添加易支付' }}</h2><span>适用于所有易支付协议平台</span></div></div>
        <div class="form-grid">
          <label><span>显示名称</span><NInput v-model:value="form.name" placeholder="例如：彩虹易支付" /></label>
          <label><span>支付协议</span><NSelect v-model:value="form.method" :options="methodOptions" :disabled="Boolean(editingID)" /></label>
          <label><span>商户 ID（pid）</span><NInput v-model:value="form.merchant_id" placeholder="平台分配的数字 PID" /></label>
          <label class="full"><span>网关地址</span><NInput v-model:value="form.gateway_url" placeholder="https://pay.example.com" /><small>填写易支付平台网址，系统自动使用 /submit.php 发起支付。</small></label>
          <label class="full"><span>商户密钥</span><NInput v-model:value="form.secret" type="password" show-password-on="click" :placeholder="editingID ? '留空表示继续使用原密钥' : '平台分配的商户密钥（KEY）'" /><small v-if="editingID">为安全起见不会回显已保存的密钥。</small></label>
          <label class="full"><span>支持的支付渠道</span><NSelect v-model:value="form.pay_types" multiple :options="payTypeOptions" /></label>
        </div>
        <div class="advanced-row" v-if="editingID"><div><b>启用状态</b><small>关闭后用户端不再显示该支付方式</small></div><NCheckbox v-model:checked="form.active" /></div>
        <div class="security-note">商户密钥使用 MASTER_KEY_BASE64 加密入库；下单回调地址为 <code>{站点地址}/api/v1/pay/epay/notify</code>，无需在平台手动配置。</div>
        <div class="form-actions"><NButton type="primary" size="large" :loading="busy" @click="save">{{ editingID ? '保存修改' : '保存支付方式' }}</NButton><NButton v-if="editingID" size="large" @click="resetForm">取消编辑</NButton></div>
      </section>

      <section class="panel">
        <div class="panel-title-row"><div><h2>支付流程说明</h2><span>自动签名与回调校验</span></div></div>
        <ol class="step-list">
          <li><b>用户选择在线支付</b><span>订单页 / 钱包页选择渠道，跳转易支付收银台。</span></li>
          <li><b>MD5 签名自动生成</b><span>按易支付规则对参数排序签名，无需手工配置。</span></li>
          <li><b>回调验签</b><span>网关异步回调校验签名、金额与商户 ID，防伪造防重放。</span></li>
          <li><b>自动开通 / 到账</b><span>订单支付成功自动入开通队列；充值实时入钱包余额。</span></li>
        </ol>
      </section>
    </div>

    <section class="panel">
      <div class="panel-title-row"><div><h2>已配置支付方式</h2><span>{{ providers.length }} 个</span></div></div>
      <div v-if="providers.length" class="provider-list">
        <article v-for="p in providers" :key="p.id" class="provider-row">
          <div class="provider-logo pay">¥</div>
          <div class="provider-main">
            <div><b>{{ p.name }}</b><span class="provider-kind">{{ p.method === 'epay' ? '易支付' : p.method }}</span><NTag v-if="p.active" size="tiny" type="success">启用</NTag><NTag v-else size="tiny">停用</NTag></div>
            <small>{{ p.gateway_url }}</small>
            <small>商户 PID：{{ p.merchant_id }} · 支持渠道：{{ (p.config?.pay_types || []).join(' / ') }}</small>
          </div>
          <div class="provider-state"><span class="state-pill">{{ p.has_secret ? '密钥已保存' : '未配置密钥' }}</span></div>
          <div class="provider-actions"><NButton size="small" tertiary @click="edit(p)">编辑</NButton></div>
        </article>
      </div>
      <div v-else class="empty-box">还没有在线支付方式。添加一个易支付平台后，用户即可在线支付与充值。</div>
    </section>
  </div>
</template>
