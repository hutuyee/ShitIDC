<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NSelect, useMessage } from 'naive-ui'
import { api, dataOf } from '../api'

// 商品下拉优化（对齐魔方 CBAP ProductDropDownSelect 插件）。
// 四张样式卡与插件的示例页同构：default 平铺 / first_group 一级分组 /
// second_group 二级分组 / first_second_group 一级 + 二级分组。
// 站内商品分组只有一级，三种分组样式的呈现相同（分组为一级、商品为叶子），
// 样式值仍按插件原样保存。

const message = useMessage()
const style = ref('default')
const groups = ref<{ group: string; products: string[] }[]>([])
const busy = ref(false)

const styleMeta: Record<string, { label: string; desc: string }> = {
  default: { label: '平铺', desc: '商品下拉框平铺展示全部商品，不做分组（默认样式）' },
  first_group: { label: '一级分组', desc: '按商品分组聚合：分组为一级，商品为叶子' },
  second_group: { label: '二级分组', desc: '二级分组样式；站内分组只有一级，呈现与一级分组相同' },
  first_second_group: { label: '一级 + 二级分组', desc: '两级分组样式；站内分组只有一级，呈现与一级分组相同' },
}
const styleOptions = Object.entries(styleMeta).map(([value, m]) => ({ label: m.label, value }))

const previewOptions = () =>
  groups.value.map(g => ({
    type: 'group' as const,
    label: g.group,
    key: g.group,
    children: g.products.map(p => ({ label: p, value: p, key: g.group + '|' + p })),
  }))
const flatOptions = () => groups.value.flatMap(g => g.products.map(p => ({ label: p, value: p, key: p })))

async function load() {
  try {
    const d = dataOf<any>(await api.get('/admin/product-dropdown-select'))
    style.value = d?.style || 'default'
    groups.value = d?.groups || []
  } catch (e: any) {
    message.error(e?.response?.data?.error?.message || '读取配置失败')
  }
}
async function save() {
  busy.value = true
  try {
    await api.put('/admin/product-dropdown-select', { style: style.value })
    message.success('已保存')
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
        <h1>商品下拉优化</h1>
        <p>选择服务详情页「升降级」弹窗里商品下拉框的下拉样式：平铺或按商品分组聚合。分组预览随下方数据实时变化。</p>
      </div>
      <div class="row" style="gap:8px">
        <NButton secondary :loading="busy" @click="load">刷新</NButton>
        <NButton type="primary" :loading="busy" @click="save">保存</NButton>
      </div>
    </div>

    <div class="pdd-grid">
      <div v-for="(opt, key) in styleMeta" :key="key" class="panel pdd-card" :class="{ active: style === key }" @click="style = key">
        <div class="row" style="justify-content:space-between;align-items:center">
          <b>{{ opt.label }}</b>
          <span class="muted" style="font-size:12px">{{ key }}</span>
        </div>
        <p class="muted" style="margin:6px 0 10px">{{ opt.desc }}</p>
        <NSelect
          :value="null"
          :options="key === 'default' ? flatOptions() : previewOptions()"
          :placeholder="'示例（' + opt.label + '）'"
          size="small"
          @update:value="style = key"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.pdd-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 12px; }
.pdd-card { cursor: pointer; border: 2px solid transparent; }
.pdd-card.active { border-color: var(--primary, #4f46e5); }
</style>
