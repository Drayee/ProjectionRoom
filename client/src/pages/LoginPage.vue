<script setup lang="ts">
/** /login 壳：AppHeader(bare) + AuthPanel。 */
import AppHeader from '../components/ui/AppHeader.vue'
import AuthPanel from '../components/features/auth/AuthPanel.vue'
</script>

<template>
  <div class="page">
    <AppHeader variant="bare" />
    <main class="body">
      <AuthPanel initial-tab="login" />
    </main>
  </div>
</template>

<style scoped>
.page {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}

.body {
  flex: 1;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 40px 20px;
}

.body > :deep(.panel) {
  width: min(440px, 100%);
}
</style>
