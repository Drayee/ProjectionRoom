<script setup lang="ts">
/**
 * 帮助页（/help）。骨架阶段最小实现。
 * 硬契约（票 #16）：7 个锚点 id 全保留 —— #why-login #source #room-code
 * #playback #diagnostics #privacy #faq；scrollBehavior 消费 to.hash。
 * 内容按 B 重写不平移；#19 填长文。
 */
import AppHeader from '../components/ui/AppHeader.vue'

const SECTIONS = [
  { id: 'why-login', title: '为什么建房要登录，看片不用？' },
  { id: 'source', title: '片源准备（本地切片）' },
  { id: 'room-code', title: '房间码与分享' },
  { id: 'playback', title: '同步播放与拓扑分发' },
  { id: 'diagnostics', title: '卡顿与排障' },
  { id: 'privacy', title: '隐私与安全边界' },
  { id: 'faq', title: '常见问题（重连 / 宽限期）' },
]
</script>

<template>
  <div class="page">
    <AppHeader variant="page" />
    <main class="body">
      <h1 class="title">帮助</h1>
      <nav class="toc" aria-label="目录">
        <a v-for="s in SECTIONS" :key="s.id" :href="`#${s.id}`">{{ s.title }}</a>
      </nav>
      <section v-for="s in SECTIONS" :id="s.id" :key="s.id" class="sec">
        <h2>{{ s.title }}</h2>
        <p class="dim">内容整理中（骨架占位；锚点 id 是深链契约，不会改）。</p>
      </section>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(760px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 16px; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(32px, 6vw, 48px); }
.toc { display: flex; flex-direction: column; gap: 6px; border: 1px solid var(--line); padding: 12px 16px; }
.toc a { color: var(--dim); font-size: 13px; }
.toc a:hover { color: var(--text); }
.sec { scroll-margin-top: 16px; }
.sec h2 { margin: 24px 0 6px; font-size: 17px; }
</style>
