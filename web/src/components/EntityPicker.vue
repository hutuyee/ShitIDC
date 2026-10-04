<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NInput, NModal, NTag, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// EntityPicker：点开输入框就能按「名字或 UID」搜索并挑选对象，不用手抄 UUID。
//
// 后台有大量地方需要引用别的对象（把用户加进客户组、给接口密钥指定归属用户…），
// 之前都是让管理员去列表页复制 UUID——既容易抄错，也没法确认选的是不是对的人。
// 这个组件把「搜索 → 看到关键信息 → 选中」收在一个弹窗里。
//
// 用法：
//   <EntityPicker v-model="form.user" kind="user" placeholder="选择用户" />
// v-model 绑定的是对象的公开 ID（UUID），选中时通过 @picked 回传整条记录。

const props = withDefaults(defineProps<{
  modelValue?: string
  // kind: user / product / service / order，留空表示搜索全部类型
  kind?: string
  placeholder?: string
  title?: string
  disabled?: boolean
  // 已选中时的展示文案；不给就只显示 ID 尾部
  selectedLabel?: string
}>(), { modelValue: '', kind: '', placeholder: '点击搜索', title: '搜索并选择', disabled: false, selectedLabel: '' })

const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  (e: 'picked', hit: any): void
}>()

const message = useMessage()
const open = ref(false)
const query = ref('')
const results = ref<any[]>([])
const loading = ref(false)
const pickedLabel = ref(props.selectedLabel)
let seq = 0

// 选中后清掉旧标签，避免外部改了 modelValue 还显示上一次的名字。
watch(() => props.modelValue, v => { if (!v) pickedLabel.value = '' })
watch(() => props.selectedLabel, v => { if (v) pickedLabel.value = v })

const kindLabel: Record<string, string> = { user: '用户', product: '商品', service: '服务', order: '订单' }
const kindTag: Record<string, any> = { user: 'info', product: 'success', service: 'warning', order: 'default' }

const display = computed(() => {
  if (pickedLabel.value) return pickedLabel.value
  const v = props.modelValue || ''
  return v ? v.slice(0, 8) + '…' : ''
})

async function search() {
  const q = query.value.trim()
  if (!q) { results.value = []; return }
  const my = ++seq
  loading.value = true
  try {
    const params: any = { q, limit: 20 }
    if (props.kind) params.kind = props.kind
    const list = dataOf<any[]>(await api.get('/admin/search', { params }))
    // 防止慢请求覆盖新结果。
    if (my === seq) results.value = list
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '搜索失败')
  } finally {
    if (my === seq) loading.value = false
  }
}

function choose(hit: any) {
  emit('update:modelValue', hit.id)
  pickedLabel.value = hit.label + (hit.sub ? ' · ' + hit.sub : '')
  emit('picked', hit)
  open.value = false
}

function clear() {
  emit('update:modelValue', '')
  pickedLabel.value = ''
  emit('picked', null)
}

function openPicker() {
  if (props.disabled) return
  open.value = true
  results.value = []
  query.value = ''
}
</script>

<template>
  <div class="entity-picker">
    <!-- 已选中：显示名字 + 清除按钮；未选中：一个可点击的占位框。 -->
    <div v-if="modelValue" class="picker-selected">
      <span class="picker-value" :title="modelValue">{{ display }}</span>
      <NButton size="tiny" quaternary :disabled="disabled" @click="clear">清除</NButton>
      <NButton size="tiny" quaternary :disabled="disabled" @click="openPicker">更换</NButton>
    </div>
    <button v-else type="button" class="picker-trigger" :disabled="disabled" @click="openPicker">
      {{ placeholder }}
    </button>

    <NModal v-model:show="open" preset="card" :title="title" style="width:min(620px,94vw)">
      <div class="stack">
        <div class="row" style="gap:8px">
          <NInput
            v-model:value="query"
            :placeholder="kind === 'user' ? '输入邮箱、UID 或 UUID' : '输入名称或 ID'"
            clearable
            autofocus
            @keyup.enter="search"
          />
          <NButton type="primary" :loading="loading" @click="search">搜索</NButton>
        </div>
        <div class="picker-hint muted">
          支持模糊匹配{{ kind === 'user' ? '：邮箱、UID（如 12）、UUID 都可以' : '：名称或 ID 都可以' }}
        </div>

        <div v-if="loading" class="empty-box" style="margin:0">搜索中…</div>
        <div v-else-if="!results.length" class="empty-box" style="margin:0">
          {{ query.trim() ? '没有匹配结果。' : '输入关键词后回车搜索。' }}
        </div>
        <div v-else class="picker-results">
          <button
            v-for="hit in results"
            :key="hit.kind + ':' + hit.id"
            type="button"
            class="picker-hit"
            @click="choose(hit)"
          >
            <div class="picker-hit-main">
              <div class="picker-hit-title">
                <NTag size="tiny" :type="kindTag[hit.kind] || 'default'" round>{{ kindLabel[hit.kind] || hit.kind }}</NTag>
                <b>{{ hit.label }}</b>
              </div>
              <small class="muted">{{ hit.sub }}</small>
            </div>
          </button>
        </div>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.entity-picker { width: 100%; }
.picker-trigger {
  width: 100%; text-align: left; cursor: pointer;
  padding: 8px 12px; border-radius: 8px;
  border: 1px dashed var(--border, #d7dbe7); background: transparent;
  color: var(--muted, #8a93a6); font-size: 13px;
}
.picker-trigger:hover:not(:disabled) { border-color: var(--primary, #4f7cff); color: var(--primary, #4f7cff); }
.picker-trigger:disabled { cursor: not-allowed; opacity: .6; }
.picker-selected {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 8px 6px 12px; border-radius: 8px;
  border: 1px solid var(--border, #d7dbe7); background: var(--panel-soft, #fbfcfe);
}
.picker-value { flex: 1; min-width: 0; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.picker-hint { font-size: 12px; }
.picker-results { display: flex; flex-direction: column; gap: 6px; max-height: 46vh; overflow-y: auto; }
.picker-hit {
  display: flex; align-items: center; text-align: left; cursor: pointer;
  padding: 9px 12px; border-radius: 8px; border: 1px solid var(--border, #e5e8f0);
  background: transparent; width: 100%;
}
.picker-hit:hover { border-color: var(--primary, #4f7cff); background: var(--panel-soft, #f7f9ff); }
.picker-hit-main { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.picker-hit-title { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.picker-hit small { font-size: 12px; }
</style>
