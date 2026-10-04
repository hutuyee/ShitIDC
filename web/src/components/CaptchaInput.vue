<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NInput } from 'naive-ui'
import { api, dataOf } from '../api'

// SVG math captcha (server-rendered, no user content) shown when the backend
// enforces challenges on auth endpoints.
const id = defineModel<string>('id')
const answer = defineModel<string>('answer')
const svg = ref('')
const loading = ref(false)

async function refresh() {
  loading.value = true
  try {
    const d = dataOf<{ id: string; svg: string }>(await api.get('/auth/captcha'))
    svg.value = d.svg
    id.value = d.id
  } catch { svg.value = '' } finally { loading.value = false }
}
defineExpose({ refresh })
onMounted(refresh)
</script>

<template>
  <div class="captcha-row">
    <NInput v-model:value="answer" placeholder="计算结果" maxlength="4" />
    <div class="captcha-img" :class="{ loading }" title="点击刷新验证码" @click="refresh" v-html="svg"></div>
  </div>
</template>

<style scoped>
.captcha-row { display: flex; gap: 8px; align-items: stretch; }
.captcha-row .n-input { flex: 1; }
.captcha-img { flex: 0 0 160px; height: 40px; border-radius: 6px; overflow: hidden; cursor: pointer; background: var(--panel, #f6f7fb); display: flex; align-items: center; }
.captcha-img :deep(svg) { width: 100%; height: 100%; display: block; }
.captcha-img.loading { opacity: .5; }
</style>
