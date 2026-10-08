<script setup lang="ts">
/**
 * `/register` 深链与回跳入口。与 `LoginView` 共用 `AuthPanel`，
 * 区别只有 `initialTab`（先显示哪一页）与 `cross-link`（是否渲染"去登录"跳转）。
 */
import AppHeader from '../components/AppHeader.vue'
import AuthPanel from '../components/AuthPanel.vue'
</script>

<template>
  <div class="auth-page">
    <AppHeader variant="bare" />
    <AuthPanel initial-tab="register" cross-link />
  </div>
</template>

<style scoped>
.auth-page {
  max-width: 460px;
  margin: 0 auto;
  padding: 28px 20px 40px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
</style>
