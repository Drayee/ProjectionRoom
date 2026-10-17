<script setup lang="ts">
/**
 * 首页：落地页（hero ↔ 登录窗 morph）↔ 已登录外壳（等待加入房间占位页）。
 * 授权 100% 在服务端：这里只按登录态换渲染，不做任何拦截。
 *
 * 顶栏由全局外壳（App.vue）常驻，本页不再自己渲染：
 *   未登录 → 路由 meta.header='none'，全屏落地页无顶栏；
 *   已登录 → 同一 meta 由全局外壳按登录态渲染 shell 态顶栏
 *   （分区图标导航 + 头像），换页不销毁重建。
 */
import { computed } from 'vue'
import LandingView from '../components/features/shell/LandingView.vue'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const booting = computed(() => auth.restoring)
</script>

<template>
  <div v-if="booting" class="boot dim">恢复登录中…</div>
  <div v-else-if="auth.isLoggedIn" class="shell" data-testid="home-shell">
    <main class="wait">
      <p class="big">等待加入房间</p>
      <p class="dim">拿到房间码就能进，看片不需要账号。</p>
    </main>
  </div>
  <LandingView v-else />
</template>

<style scoped>
.boot {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
}

.shell {
  flex: 1;
  display: flex;
  flex-direction: column;
}

.wait {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
}

.big {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: 22px;
  letter-spacing: 0.06em;
}
</style>
