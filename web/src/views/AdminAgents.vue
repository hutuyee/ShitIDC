<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'
import EntityPicker from '../components/EntityPicker.vue'

const message = useMessage()
const groups = ref<any[]>([])
const form = reactive({ name: '', discount_percent: 0 })
const assign = reactive({ user: '', group_id: '' })
// 记录选中的用户是谁：分配前能看一眼邮箱，避免把金牌代理给错人。
const pickedUser = ref<any>(null)

async function load() {
  try { groups.value = dataOf(await api.get('/admin/user-groups')) }
  catch (e: any) { message.error(e?.response?.data?.error?.message || '读取用户组失败') }
}

async function create() {
  if (!form.name.trim()) { message.error('请填写用户组名称'); return }
  try {
    await api.post('/admin/user-groups', { name: form.name.trim(), discount_percent: form.discount_percent })
    message.success('用户组已创建')
    form.name = ''; form.discount_percent = 0
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '创建失败') }
}

async function update(g: any) {
  try {
    await api.put(`/admin/user-groups/${g.id}`, { name: g.name, discount_percent: g.discount_percent })
    message.success('已保存')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '保存失败') }
}

async function remove(g: any) {
  try {
    await api.delete(`/admin/user-groups/${g.id}`)
    message.success('已删除')
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '删除失败') }
}

async function doAssign() {
  if (!assign.user.trim()) { message.error('请先选择用户'); return }
  try {
    await api.post(`/admin/users/${assign.user.trim()}/group`, { group_id: assign.group_id })
    message.success(pickedUser.value ? `已更新 ${pickedUser.value.label} 的分组` : '已更新用户分组')
    assign.user = ''
    pickedUser.value = null
    await load()
  } catch (e: any) { message.error(e?.response?.data?.error?.message || '分配失败') }
}

onMounted(load)
</script>

<template>
  <div class="admin-page">
    <div class="admin-page-head"><div><div class="eyebrow">代理系统</div><h1>用户组折扣</h1><p>为代理 / 大客户分组设置整体折扣，组内用户下单时自动按比例减价（先组折扣、后优惠码）。</p></div></div>

    <div class="admin-two-col">
      <section class="panel admin-form-panel">
        <div class="panel-title-row"><div><h2>创建用户组</h2><span>折扣为订单总价的百分比</span></div></div>
        <div class="form-grid">
          <label><span>组名</span><NInput v-model:value="form.name" placeholder="例如：金牌代理" /></label>
          <label><span>折扣百分比 (0-100)</span><NInputNumber v-model:value="form.discount_percent" :min="0" :max="100" style="width:100%" /></label>
        </div>
        <NButton type="primary" size="large" @click="create">创建用户组</NButton>

        <div class="panel-title-row" style="margin-top:16px"><div><h2>分配用户</h2><span>搜索选择用户，无需手抄 UUID</span></div></div>
        <div class="form-grid">
          <label>
            <span>用户</span>
            <!-- 不再要求手抄 UUID：点开搜索即可按邮箱或 UID 挑人，并看到余额等关键信息。 -->
            <EntityPicker
              v-model="assign.user"
              kind="user"
              title="选择用户"
              placeholder="点击搜索用户（邮箱 / UID）"
              @picked="(hit: any) => (pickedUser = hit)"
            />
          </label>
          <label><span>分组</span>
            <select v-model="assign.group_id" class="native-select">
              <option value="">移出分组</option>
              <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}（{{ g.discount_percent }}%）</option>
            </select>
          </label>
        </div>
        <div v-if="pickedUser" class="picked-note">
          将把 <b>{{ pickedUser.label }}</b> <span class="muted">{{ pickedUser.sub }}</span> 设为所选分组；
          选择「移出分组」则恢复为无折扣。
        </div>
        <NButton secondary @click="doAssign">保存分组</NButton>
      </section>

      <section class="panel">
        <div class="panel-title-row"><div><h2>用户组列表</h2><span>{{ groups.length }} 组</span></div></div>
        <div class="stack">
          <div v-for="g in groups" :key="g.id" class="group-edit-row">
            <NInput v-model:value="g.name" style="flex:1" />
            <NInputNumber v-model:value="g.discount_percent" :min="0" :max="100" style="width:110px" />
            <small class="muted" style="width:64px">{{ g.members }} 人</small>
            <NButton size="small" tertiary type="primary" @click="update(g)">保存</NButton>
            <NButton size="small" tertiary type="error" @click="remove(g)">删除</NButton>
          </div>
          <div v-if="!groups.length" class="empty-box" style="margin:0">还没有用户组。</div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.group-edit-row { display: flex; gap: 8px; align-items: center; }
.picked-note {
  font-size: 12px; line-height: 1.6; color: var(--muted, #6b7280);
  border-left: 3px solid var(--primary, #4f7cff); padding: 6px 10px;
  background: var(--panel-soft, #fbfcfe); border-radius: 0 8px 8px 0;
}
.native-select { height: 34px; border-radius: 6px; border: 1px solid var(--border, #d5d9e4); background: var(--panel, #fff); color: var(--text, #1c2333); padding: 0 8px; width: 100%; }
</style>
