<script setup lang="ts">
/**
 * 首页（落地页 ↔ 登录窗 ↔ 已登录外壳）。
 * 骨架阶段：最小可构建实现；完整交互由 #19 填充（票 #10 的 B 形态）。
 */
import { computed } from 'vue'
import AppHeader from '../components/ui/AppHeader.vue'
import AuthPanel from '../components/features/auth/AuthPanel.vue'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const booting = computed(() => auth.restoring)
</script>

<template>
  <div v-if="booting" class="boot dim">恢复登录中…</div>
  <div v-else-if="auth.isLoggedIn" class="shell" data-testid="home-shell">
    <AppHeader variant="shell" />
    <main class="wait">
      <p class="big">等待加入房间</p>
      <p class="dim">拿到房间码就能进，看片不需要账号。</p>
    </main>
  </div>
  <div v-else class="landing">
    <h1 class="brand">月喵</h1>
    <AuthPanel plain direct-entry />
  </div>
</template>

<style scoped>
.boot {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
}

.landing {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 28px;
  padding: 40px 20px;
}

.brand {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: clamp(60px, 14vw, 132px);
  letter-spacing: 0.04em;
}

.wait {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  min-height: 60vh;
}

.big {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: 22px;
}
</style>
