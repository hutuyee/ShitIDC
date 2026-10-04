<script setup lang="ts">
import { ref } from 'vue'
import { siteInitial, siteLogoUrl, siteName } from '../branding'

withDefaults(defineProps<{ compact?: boolean }>(), { compact: false })
const imageFailed = ref(false)
</script>

<template>
  <div class="brand-mark" :class="{ 'brand-mark--compact': compact }">
    <img v-if="siteLogoUrl && !imageFailed" class="brand-avatar" :src="siteLogoUrl" :alt="`${siteName} Logo`" @error="imageFailed = true" />
    <div v-else class="brand-avatar brand-avatar--fallback" aria-hidden="true">{{ siteInitial() }}</div>
    <span class="brand-name">{{ siteName }}</span>
  </div>
</template>
