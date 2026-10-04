<script setup lang="ts">
import { NButton } from 'naive-ui'
</script>
<template>
  <div class="login-wrap" style="min-height:100vh;display:flex;align-items:center;justify-content:center">
    <div class="panel stack" style="align-items:center;text-align:center;padding:40px;max-width:420px">
      <div style="font-size:52px;font-weight:800;letter-spacing:2px">404</div>
      <p class="muted" style="margin:0">页面不存在或已被移动。</p>
      <router-link to="/"><NButton type="primary" block>返回首页</NButton></router-link>
    </div>
  </div>
</template>
