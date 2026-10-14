<script setup lang="ts">
/**
 * /login —— 不是另一张表单页（YG 裁决：注册/登录共用落地页形态）。
 * 渲染与落地页同一个界面，只是登录窗直接展开、页签停在「登录」。
 * 路由保留是契约：ensureLoggedIn 的回跳目标、verify-auth 的负向判据都指着它。
 */
import LandingView from '../components/features/shell/LandingView.vue'
</script>

<template>
  <LandingView initial-stage="auth" initial-tab="login" :direct-entry="false" />
</template>
