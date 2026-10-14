<script setup lang="ts">
/**
 * 帖子列表（/posts）。票 #15 定稿形态：瀑布流 masonry + 无限滚动 +
 * 最新/热门 pill tab。卡片悬停触发故障（无常驻循环动画，遵守票 #13 ≤5）。
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppHeader from '../components/ui/AppHeader.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'
import * as postsApi from '../api/posts'
import type { Post } from '../types/posts'

const posts = ref<Post[]>([])
const sort = ref<'latest' | 'hot'>('latest')
const busy = ref(false)
const loadingMore = ref(false)
const page = ref(1)
const hasMore = ref(false)

const sentinel = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | undefined

/** 列表卡片摘要：去掉 Markdown 标记，压成单行短句。 */
function excerpt(content: string, max = 72): string {
  const plain = content
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/[#>*_`~\[\]()!|-]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
  return plain.length > max ? `${plain.slice(0, max)}…` : plain
}

async function load() {
  if (busy.value) return
  busy.value = true
  try {
    const r = await postsApi.listPosts({ sort: sort.value, page: 1 })
    posts.value = r.items
    page.value = 1
    hasMore.value = r.hasMore
  } finally {
    busy.value = false
  }
}

async function loadMore() {
  if (busy.value || loadingMore.value || !hasMore.value) return
  loadingMore.value = true
  try {
    const next = page.value + 1
    const r = await postsApi.listPosts({ sort: sort.value, page: next })
    posts.value = posts.value.concat(r.items)
    page.value = next
    hasMore.value = r.hasMore
  } finally {
    loadingMore.value = false
  }
}

function switchSort(next: 'latest' | 'hot') {
  if (sort.value === next) return
  sort.value = next
  void load()
}

onMounted(() => {
  void load()
  if (typeof IntersectionObserver !== 'undefined' && sentinel.value) {
    observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) void loadMore()
      },
      { rootMargin: '200px' },
    )
    observer.observe(sentinel.value)
  }
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = undefined
})
</script>

<template>
  <div class="page">
    <AppHeader variant="page" />
    <main class="body">
      <div class="head">
        <h1 class="title">帖子</h1>
        <RouterLink class="new" :to="{ name: 'post-editor' }">
          <BrandIcon name="upload" decorative :size="14" />
          发帖
        </RouterLink>
      </div>
      <div class="tabs" role="tablist">
        <button role="tab" :aria-selected="sort === 'latest'" :class="{ on: sort === 'latest' }" @click="switchSort('latest')">最新</button>
        <button role="tab" :aria-selected="sort === 'hot'" :class="{ on: sort === 'hot' }" @click="switchSort('hot')">热门</button>
      </div>
      <p v-if="busy" class="dim">加载中…</p>
      <p v-else-if="posts.length === 0" class="dim">还没有帖子。</p>
      <div v-else class="masonry">
        <RouterLink v-for="p in posts" :key="p.id" class="card" :to="{ name: 'post-detail', params: { id: String(p.id) } }">
          <img v-if="p.images.length" class="cover" :src="p.images[0]" :alt="`${p.title} 封面`" loading="lazy" />
          <h2>{{ p.title }}</h2>
          <p v-if="p.content" class="excerpt dim">{{ excerpt(p.content) }}</p>
          <p class="meta dim">{{ p.author.displayName }} · 赞 {{ p.likeCount }} · 评论 {{ p.commentCount }}</p>
        </RouterLink>
      </div>
      <div ref="sentinel" class="sentinel" aria-hidden="true" />
      <p v-if="loadingMore" class="dim">加载更多…</p>
      <p v-else-if="!hasMore && posts.length > 0" class="dim end">— 到底了 —</p>
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

/* 瀑布流：CSS 多列，卡片 break-inside 避免被拆列 */
.masonry { columns: 2 320px; column-gap: 14px; }
.card {
  display: block;
  break-inside: avoid;
  margin-bottom: 14px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 14px;
  color: inherit;
  transition: border-color var(--dur-micro) var(--ease), box-shadow var(--dur-micro) var(--ease);
}
.card:hover {
  border-color: var(--accent);
  text-decoration: none;
  /* 悬停故障：RGB 分离外发光（静态，不逐帧重绘） */
  box-shadow: 3px 0 0 var(--glitch-r), -3px 0 0 var(--glitch-p), 0 8px 24px rgba(180, 108, 255, 0.25);
}
.card h2 { margin: 0 0 6px; font-size: 15px; }
.cover { display: block; width: 100%; margin-bottom: 10px; border: 1px solid var(--line); border-radius: var(--radius); }
.excerpt { margin: 0 0 8px; font-size: 13px; }
.meta { margin: 0; font-size: 12px; }
.sentinel { height: 1px; }
.end { text-align: center; }
</style>
