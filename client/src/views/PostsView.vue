<script setup lang="ts">
/**
 * 帖子列表页（月夜玻璃）。
 *
 * 形态（wayfinder #5 决议）：
 *   - 瀑布流 glass 卡片（月夜玻璃拟态）
 *   - 无限滚动 + 「加载更多」兜底
 *   - 顶栏「最新 / 热门」pill tab
 *   - 纯文本插值正文 + 结构化图片（禁 v-html）
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'
import * as postsApi from '../api/posts'
import type { Post } from '../types/posts'

type SortKey = 'latest' | 'hot'

const router = useRouter()

const items = ref<Post[]>([])
const sort = ref<SortKey>('latest')
const page = ref(1)
const hasMore = ref(true)
const loading = ref(false)
const loadError = ref('')

const sentinel = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | null = null

async function loadMore() {
  if (loading.value || !hasMore.value) return
  loading.value = true
  loadError.value = ''
  try {
    const result = await postsApi.listPosts({ sort: sort.value, page: page.value })
    items.value = page.value === 1 ? result.items : [...items.value, ...result.items]
    hasMore.value = result.hasMore
    page.value += 1
  } catch (err) {
    loadError.value = '加载失败：' + (err instanceof Error ? err.message : '未知错误')
  } finally {
    loading.value = false
  }
}

function switchSort(key: SortKey) {
  if (sort.value === key) return
  sort.value = key
  page.value = 1
  hasMore.value = true
  items.value = []
  void loadMore()
}

function goDetail(post: Post) {
  void router.push({ name: 'post-detail', params: { id: String(post.id) } })
}

onMounted(async () => {
  await loadMore()
  if (sentinel.value && 'IntersectionObserver' in window) {
    observer = new IntersectionObserver(
      (entries) => {
        if (entries[0].isIntersecting && hasMore.value && !loading.value) {
          void loadMore()
        }
      },
      { rootMargin: '200px' },
    )
    observer.observe(sentinel.value)
  }
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
})
</script>

<template>
  <div class="page-glass">
    <AppHeader variant="shell" />

    <header class="head">
      <div class="head-left">
        <BrandIcon name="tips" decorative :size="20" />
        <h1>帖子</h1>
      </div>
      <nav class="sort-tabs" aria-label="排序">
        <button type="button" class="sort-tab" :class="{ active: sort === 'latest' }" :aria-current="sort === 'latest' ? 'page' : undefined" @click="switchSort('latest')">最新</button>
        <button type="button" class="sort-tab" :class="{ active: sort === 'hot' }" :aria-current="sort === 'hot' ? 'page' : undefined" @click="switchSort('hot')">热门</button>
      </nav>
    </header>

    <div class="error-bar" v-if="loadError">
      <BrandIcon name="alert-triangle" label="错误" :size="15" />
      <span class="msg">{{ loadError }}</span>
      <button type="button" class="icon-btn" aria-label="重试" @click="loadMore"><BrandIcon name="refresh" decorative :size="14" /></button>
    </div>

    <div v-if="!loading && items.length === 0" class="empty">
      <BrandIcon name="tips" decorative :size="28" />
      <p class="muted">还没有帖子</p>
      <RouterLink class="btn-primary" :to="{ name: 'post-editor' }">写帖子</RouterLink>
    </div>

    <!-- FAB 写帖子按钮 -->
    <RouterLink class="fab-write" :to="{ name: 'post-editor' }" aria-label="写帖子" v-tip="'写帖子'">
      <svg class="ic" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" aria-hidden="true">
        <line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>
      </svg>
    </RouterLink>

    <TransitionGroup name="card" tag="div" class="feed">
      <article v-for="post in items" :key="post.id" class="post glass" @click="goDetail(post)" role="button" :tabindex="0" @keydown.enter="goDetail(post)">
        <div class="post-head">
          <span class="avatar-circle" :style="{ background: post.author.avatarColor }">{{ post.author.avatarChar }}</span>
          <div class="post-meta">
            <span class="post-author">{{ post.author.displayName }}</span>
            <span class="post-time">{{ new Date(post.createdAt).toLocaleString('zh-CN') }}</span>
          </div>
        </div>
        <h2 class="post-title">{{ post.title }}</h2>
        <p class="post-text">{{ post.content }}</p>
        <div v-if="post.images.length > 0" class="post-imgs">
          <img v-for="(img, i) in post.images" :key="i" :src="img" alt="" class="post-img" decoding="async" />
        </div>
        <div class="post-actions">
          <span class="stat" :class="{ liked: post.liked }"><BrandIcon name="star" decorative :size="14" /> {{ post.likeCount }}</span>
          <span class="stat"><BrandIcon name="chat" decorative :size="14" /> {{ post.commentCount }}</span>
        </div>
      </article>
    </TransitionGroup>

    <div v-if="hasMore" class="load-more">
      <div ref="sentinel" class="sentinel" />
      <button type="button" class="more-btn" :disabled="loading" :aria-busy="loading" @click="loadMore">{{ loading ? '加载中…' : '加载更多' }}</button>
    </div>
    <p v-if="!hasMore && items.length > 0" class="muted end">已全部加载</p>
  </div>
</template>

<style scoped>
.head { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.head-left { display: flex; align-items: center; gap: 8px; }
.head-left h1 { margin: 0; font-size: 20px; }
.sort-tabs { display: flex; gap: 6px; }
.sort-tab {
  padding: 6px 16px; border-radius: var(--radius-pill);
  background: transparent; border: 1px solid var(--border);
  color: var(--text-dim); font-size: 13px; cursor: pointer;
  transition: var(--transition-fast);
}
.sort-tab:hover { color: var(--text); border-color: var(--accent); }
.sort-tab.active { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); }

.feed { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 14px; }

.card-enter-active { transition: opacity 0.28s ease, transform 0.28s ease; }
.card-enter-from { opacity: 0; transform: translateY(8px); }

.post {
  display: flex; flex-direction: column; gap: 10px; padding: 16px;
  cursor: pointer; outline: none;
  transition: transform var(--transition-fast), border-color var(--transition-fast);
}
.post:hover, .post:focus-visible { transform: translateY(-2px); border-color: var(--accent); }

.post-head { display: flex; align-items: center; gap: 10px; }
.avatar-circle {
  display: inline-flex; align-items: center; justify-content: center;
  width: 32px; height: 32px; border-radius: 50%;
  color: #fff; font-weight: 700; font-size: 14px; flex: none;
}
.post-meta { display: flex; flex-direction: column; min-width: 0; }
.post-author { font-weight: 600; font-size: 13px; }
.post-time { font-size: 12px; color: var(--text-dim); }

.post-title { margin: 0; font-size: 16px; line-height: 1.3; }
.post-text {
  margin: 0; font-size: 13px; line-height: 1.6;
  color: var(--text-dim); white-space: pre-wrap;
  display: -webkit-box; -webkit-line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden;
}
.post-imgs { display: flex; gap: 8px; overflow-x: auto; }
.post-img { width: 80px; height: 80px; border-radius: var(--radius-sm); object-fit: cover; background: var(--panel-2); }

.post-actions { display: flex; gap: 14px; padding-top: 4px; border-top: 1px solid var(--border); }
.stat { display: inline-flex; align-items: center; gap: 5px; color: var(--text-dim); font-size: 12px; }
.stat.liked { color: var(--accent-2); }

.load-more { display: flex; flex-direction: column; align-items: center; gap: 8px; }
.sentinel { height: 1px; }
.more-btn {
  padding: 8px 24px; border-radius: var(--radius-pill);
  background: transparent; border: 1px solid var(--border);
  color: var(--text-dim); font-size: 13px; cursor: pointer;
  transition: var(--transition-fast);
}
.more-btn:hover:not(:disabled) { color: var(--text); border-color: var(--accent); }
.more-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.end { text-align: center; font-size: 12px; }

.empty { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 60px 0; }
.btn-primary {
  padding: 8px 20px; border-radius: var(--radius-pill);
  background: var(--accent); border: 1px solid var(--accent);
  color: var(--accent-ink); text-decoration: none; font-weight: 600;
}

.error-bar .msg { flex: 1; min-width: 0; }
button.icon-btn {
  display: inline-flex; align-items: center; justify-content: center;
  padding: 3px; background: transparent; border-color: transparent;
  color: inherit; line-height: 0;
}

/* FAB 写帖子按钮 */
.fab-write {
  position: fixed; right: 22px; bottom: 84px; z-index: 20;
  display: inline-flex; align-items: center; justify-content: center;
  width: 50px; height: 50px; padding: 0; border-radius: 50%;
  background: var(--accent); border: 1px solid var(--accent);
  color: var(--accent-ink); text-decoration: none; line-height: 0;
  box-shadow: 0 4px 16px rgba(0, 161, 214, 0.35);
  transition: transform 0.18s ease, box-shadow 0.18s ease;
}
.fab-write:hover { transform: translateY(-2px); box-shadow: 0 6px 24px rgba(0, 161, 214, 0.45); text-decoration: none; }
.fab-write svg { display: block; }

@media (prefers-reduced-motion: reduce) {
  .sort-tab, .post, .card-enter-active { transition: none; }
  .fab-write { box-shadow: none; }
}
@media (max-width: 600px) { .feed { grid-template-columns: minmax(0, 1fr); } }
</style>
