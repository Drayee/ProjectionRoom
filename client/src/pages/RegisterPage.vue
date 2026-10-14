<script setup lang="ts">
/**
 * /register —— 同 /login：不是独立注册页，是落地页形态 + 登录窗展开 + 「注册」页签。
 * 路由保留是契约（AuthPanel 的注册深链、旧回跳路径都指它）。
 */
import LandingView from '../components/features/shell/LandingView.vue'
</script>

<template>
  <LandingView initial-stage="auth" initial-tab="register" :direct-entry="false" />
</template>
