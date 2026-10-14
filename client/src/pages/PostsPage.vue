<script setup lang="ts">
/**
 * 帖子列表（/posts）。骨架阶段最小实现：mock API + 最新/热门切换。
 * 完整形态（票 #15：masonry + 无限滚动 + 悬停故障）由 #19 填充。
 */
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppHeader from '../components/ui/AppHeader.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'
import * as postsApi from '../api/posts'
import type { Post } from '../types/posts'

const posts = ref<Post[]>([])
const sort = ref<'latest' | 'hot'>('latest')
const busy = ref(false)

async function load() {
  busy.value = true
  try {
    const r = await postsApi.listPosts({ sort: sort.value, page: 1 })
    posts.value = r.items
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <AppHeader variant="page" />
    <main class="body">
      <div class="head">
        <h1 class="title">帖子</h1>
        <RouterLink class="new" :to="{ name: 'post-editor' }">
          <BrandIcon name="menu" decorative :size="14" />
          发帖
        </RouterLink>
      </div>
      <div class="tabs" role="tablist">
        <button role="tab" :aria-selected="sort === 'latest'" :class="{ on: sort === 'latest' }" @click="sort = 'latest'; load()">最新</button>
        <button role="tab" :aria-selected="sort === 'hot'" :class="{ on: sort === 'hot' }" @click="sort = 'hot'; load()">热门</button>
      </div>
      <p v-if="busy" class="dim">加载中…</p>
      <p v-else-if="posts.length === 0" class="dim">还没有帖子。</p>
      <ul v-else class="list">
        <li v-for="p in posts" :key="p.id">
          <RouterLink class="card" :to="{ name: 'post-detail', params: { id: String(p.id) } }">
            <h2>{{ p.title }}</h2>
            <p class="meta dim">{{ p.author.displayName }} · 赞 {{ p.likeCount }} · 评论 {{ p.commentCount }}</p>
          </RouterLink>
        </li>
      </ul>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(880px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 16px; }
.head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(32px, 6vw, 48px); }
.new { display: inline-flex; align-items: center; gap: 5px; padding: 8px 14px; border: 1px solid var(--accent); color: var(--accent); font-size: 13px; }
.new:hover { background: rgba(180, 108, 255, 0.1); text-decoration: none; }
.tabs { display: flex; gap: 2px; }
.tabs button { padding: 6px 16px; color: var(--dim); border-bottom: 2px solid transparent; }
.tabs button.on { color: var(--text); border-bottom-color: var(--accent); }
.list { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 10px; }
.card { display: block; border: 1px solid var(--line); background: var(--surface); padding: 12px 14px; color: inherit; }
.card:hover { border-color: var(--accent); text-decoration: none; }
.card h2 { margin: 0 0 4px; font-size: 15px; }
.meta { margin: 0; font-size: 12px; }
</style>
