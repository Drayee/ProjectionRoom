<script setup lang="ts">
/**
 * `/login` 深链与回跳入口。
 *
 * 表单本体在 `AuthPanel`（落地页未登录态用的是同一个组件），本文件只负责页面外壳：
 * 顶栏（月喵品牌 + 帮助/GitHub/账号区）+ 居中容器。`cross-link` 让登录页保留
 * "注册一个"的跳转（落地页有标签页，不需要第二套切换入口）。
 */
import AppHeader from '../components/AppHeader.vue'
import AuthPanel from '../components/AuthPanel.vue'
</script>

<template>
  <div class="auth-page">
    <AppHeader variant="bare" />
    <AuthPanel initial-tab="login" cross-link />
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
