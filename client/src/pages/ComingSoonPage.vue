<script setup lang="ts">
/**
 * 建设中占位页（props 驱动，零请求、零副作用）。
 * 一份渲染逻辑，多份说明（票 #16）；内容写在 router.ts 的路由 props 里。
 */
import AppHeader from '../components/ui/AppHeader.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'

defineProps<{
  title: string
  icon: string
  summary: string
  bullets: string[]
  depends: string[]
}>()
</script>

<template>
  <div class="page">
    <AppHeader variant="page" />
    <main class="body">
      <h1 class="title">{{ title }}</h1>
      <p class="summary">{{ summary }}</p>
      <ul class="bullets">
        <li v-for="(b, i) in bullets" :key="i">{{ b }}</li>
      </ul>
      <section class="depends">
        <h2>依赖</h2>
        <ul>
          <li v-for="(d, i) in depends" :key="i">
            <BrandIcon name="alert-triangle" decorative :size="13" />
            {{ d }}
          </li>
        </ul>
      </section>
      <p class="dim">该分区尚未实现，不发任何请求。</p>
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
  width: min(720px, 100%);
  margin: 0 auto;
  padding: 40px 20px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.title {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: clamp(32px, 6vw, 48px);
  letter-spacing: 0.04em;
}

.summary {
  margin: 0;
  font-size: 15px;
}

.bullets {
  margin: 0;
  padding-left: 20px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  color: var(--dim);
}

.depends {
  border: 1px solid var(--line);
  padding: 12px 16px;
}

.depends h2 {
  margin: 0 0 8px;
  font-size: 13px;
  color: var(--dim);
}

.depends ul {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 13px;
}

.depends li {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}
</style>
