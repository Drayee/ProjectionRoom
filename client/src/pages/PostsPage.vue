<script setup lang="ts">
/**
 * 帖子列表（/posts）。票 #15 定稿形态：瀑布流 masonry + 无限滚动 + 最新/热门 pill tab。
 *
 * 票 #63 把卡片拆成三层（此前整张卡是一个 `<RouterLink>`，作者/时间/计数全挤在底部一行里）：
 *
 * ```
 * ┌─────────────────────────────┐
 * │ AuthorRow（头像 昵称 时间）   │  ← 谁写的
 * │ a.card-link（封面 标题 摘要） │  ← 点这里进详情（唯一的导航区）
 * │ ActionBar（赞 收藏 评论）     │  ← 能做什么 + 多少
 * └─────────────────────────────┘
 * ```
 *
 * 为什么必须拆：在 `<a>` 里放 `<button>` 是**无效 HTML**，浏览器会把按钮从可点区域里
 * 摘掉（或者点按钮时同时触发导航）。真机脚本 verify-authorrow.mjs 的 R2c 直接钉住这条。
 *
 * 三态（载/空/错/能力降级）统一走 `ui/AsyncState`，点赞走共用的 `usePostLike`
 * （乐观更新 + 失败回滚 + 单条在途锁，与详情页同一份实现）。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { RouterLink } from 'vue-router'
import ActionBar from '../components/ui/ActionBar.vue'
import AsyncState from '../components/ui/AsyncState.vue'
import AuthorRow from '../components/ui/AuthorRow.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'
import Button from '../components/ui/Button.vue'
import * as postsApi from '../api/posts'
import { useApiFailure } from '../components/features/common/useApiFailure'
import { usePostLike } from '../components/features/common/usePostLike'
import { useAuthStore } from '../stores/auth'
import type { Post } from '../types/posts'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { failure, run } = useApiFailure(() => route.fullPath)
const { toggle, isBusy, toast, say } = usePostLike()

const posts = ref<Post[]>([])
const sort = ref<'latest' | 'hot'>('latest')
const busy = ref(false)
const loadingMore = ref(false)
const page = ref(1)
const hasMore = ref(false)
/**
 * 后端该域未上线（`VITE_MOCK_POSTS` 翻成真实实现、而 `/api/posts/*` 还没注册）。
 * 与「加载失败」分开：前者是能力未上线，后者才是错误。
 */
const capabilityMissing = ref(false)

/** 失败态的一行文案（分诊结果，见 useApiFailure）。 */
const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能继续'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

/** AsyncState 的三种非 ready 态（`ready` 时直接渲染瀑布流，不多套一层空壳）。 */
type ListState = 'loading' | 'empty' | 'error' | 'capability'

/**
 * 列表区该渲染哪一态。
 *
 * 顺序刻意是「能力降级 → 错误 → 加载 → 空」：能力降级与错误都必须在**有旧数据**时
 * 也优先显示，否则用户会以为"刷新成功了，只是变少了"。
 */
const listState = computed<ListState | 'ready'>(() => {
  if (capabilityMissing.value) return 'capability'
  if (failureText.value) return 'error'
  if (busy.value) return 'loading'
  if (posts.value.length === 0) return 'empty'
  return 'ready'
})

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
  const r = await run(() => postsApi.listPosts({ sort: sort.value, page: 1 }))
  busy.value = false
  if (r === null) {
    // 整域 404 → 降级空态（把已渲染的列表清掉，否则空态与旧内容同屏很怪）。
    if (failure.value?.kind === 'capability') {
      capabilityMissing.value = true
      posts.value = []
      hasMore.value = false
    }
    return
  }
  posts.value = r.items
  page.value = 1
  hasMore.value = r.hasMore
  capabilityMissing.value = false
}

async function loadMore() {
  if (busy.value || loadingMore.value || !hasMore.value) return
  loadingMore.value = true
  const next = page.value + 1
  const r = await run(() => postsApi.listPosts({ sort: sort.value, page: next }))
  loadingMore.value = false
  if (r === null) {
    if (failure.value?.kind === 'capability') {
      capabilityMissing.value = true
      hasMore.value = false
    }
    return
  }
  posts.value = posts.value.concat(r.items)
  page.value = next
  hasMore.value = r.hasMore
}

function switchSort(next: 'latest' | 'hot') {
  if (sort.value === next) return
  sort.value = next
  void load()
}

/**
 * 动作条上的「评论」按钮：进详情页。
 *
 * 卡片上已经没有整块链接了，所以评论按钮**必须自己**导航 —— 否则它就是一个点了
 * 什么都不发生的按钮（比没有更糟）。带 `#comments` 让浏览器滚到评论区。
 */
function openPost(id: number) {
  void router.push({ name: 'post-detail', params: { id: String(id) }, hash: '#comments' })
}

/**
 * 收藏：本票只做占位（数据接线在票 #64）。
 *
 * 但**不能静默**：点了完全没反应与"功能坏了"在用户看来一样，所以给一条轻提示说清楚。
 */
function onFavorite() {
  say('收藏功能还在接（票 #64）')
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
    <main class="body">
      <div class="head">
        <h1 class="title">帖子</h1>
        <RouterLink class="new" :to="{ name: 'post-editor' }">
          <BrandIcon name="upload" decorative :size="14" />
          发帖
        </RouterLink>
      </div>
      <!-- 排序标签页：外壳是 ui/Button（票 #62 首批替换），选中态用组件暴露的变量接上，
           页面不重写状态色。 -->
      <div class="tabs" role="tablist">
        <Button
          variant="ghost"
          class="tab"
          role="tab"
          :aria-selected="sort === 'latest'"
          :class="{ on: sort === 'latest' }"
          @click="switchSort('latest')"
        >
          最新
        </Button>
        <Button
          variant="ghost"
          class="tab"
          role="tab"
          :aria-selected="sort === 'hot'"
          :class="{ on: sort === 'hot' }"
          @click="switchSort('hot')"
        >
          热门
        </Button>
      </div>

      <!-- 三态统一出口（票 #63）。空态带一个"去发第一帖"的行动，而不是一句干巴巴的说明。 -->
      <AsyncState
        v-if="listState !== 'ready'"
        :state="listState"
        :title="
          listState === 'capability'
            ? '帖子分区后端尚未上线'
            : listState === 'error'
              ? '没能加载出帖子'
              : listState === 'empty'
                ? '这里还没有帖子'
                : '加载中'
        "
        :hint="
          listState === 'capability'
            ? '上线后这里是瀑布流帖子列表：最新 / 热门排序、封面与评论计数。'
            : listState === 'error'
              ? failureText
              : listState === 'empty'
                ? '第一条帖子可能就是你写的 —— 说说今晚想一起看什么。'
                : ''
        "
        :retryable="listState === 'error' || listState === 'capability'"
        @retry="load"
      >
        <RouterLink v-if="listState === 'empty'" class="empty-cta" :to="{ name: 'post-editor' }">
          <Button variant="primary" size="sm">去发第一帖</Button>
        </RouterLink>
      </AsyncState>

      <div v-else class="masonry">
        <article v-for="p in posts" :key="p.id" class="post-card">
          <!-- 作者行：谁写的、什么时候写的。它**不在**链接里，所以昵称与头像可以点进档案
               （那是与"点卡片进帖子"不同的落点）。 -->
          <AuthorRow
            class="card-author"
            :author="p.author"
            :created-at="p.createdAt"
            :self="auth.profile?.id === p.authorId"
            size="compact"
          />

          <!-- 唯一的导航区：封面 + 标题 + 摘要。动作条刻意在它**外面**。 -->
          <RouterLink class="card-link" :to="{ name: 'post-detail', params: { id: String(p.id) } }">
            <img v-if="p.images.length" class="cover" :src="p.images[0]" :alt="`${p.title} 封面`" loading="lazy" />
            <h2 class="card-title">{{ p.title }}</h2>
            <p v-if="p.content" class="excerpt dim">{{ excerpt(p.content) }}</p>
          </RouterLink>

          <ActionBar
            class="card-actions"
            :liked="p.liked"
            :like-count="p.likeCount"
            :comment-count="p.commentCount"
            :like-busy="isBusy(p.id)"
            @like="toggle(p)"
            @favorite="onFavorite"
            @comment="openPost(p.id)"
          />
        </article>
      </div>

      <div ref="sentinel" class="sentinel" aria-hidden="true" />
      <p v-if="loadingMore" class="dim">加载更多…</p>
      <p v-else-if="!hasMore && posts.length > 0" class="dim end">— 到底了 —</p>
    </main>

    <!-- 轻提示（点赞失败 / 收藏占位）：写操作的失败绝不把整页换成错误态。 -->
    <p v-if="toast" class="toast" role="status" data-testid="action-toast">{{ toast }}</p>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; position: relative; }
.body { flex: 1; width: min(880px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 16px; }
.head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(32px, 6vw, 48px); }
.new { display: inline-flex; align-items: center; gap: 5px; padding: 8px 14px; border: 1px solid var(--accent); color: var(--accent); font-size: 13px; }
.new:hover { background: var(--state-hover); text-decoration: none; }
.tabs { display: flex; gap: 2px; }
.tab { --ui-btn-border-color: transparent; padding: 6px 16px; border-bottom: 2px solid transparent; border-radius: 0; }
.tab.on { --ui-btn-fg: var(--text); --ui-btn-fg-hover: var(--text); border-bottom-color: var(--accent); }

/* 瀑布流：CSS 多列，卡片 break-inside 避免被拆列 */
.masonry { columns: 2 320px; column-gap: 14px; }

/*
 * 卡片：三个块竖排（作者行 / 链接区 / 动作条）。
 *
 * 边框与背景留在 article 上（而不是链接上）：整张卡看起来是一块，
 * 但只有中间的链接区可点 —— 这是"卡不再是一整块链接"的视觉落点。
 */
.post-card {
  display: flex;
  flex-direction: column;
  gap: 10px;
  break-inside: avoid;
  margin-bottom: 14px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 14px;
  transition: border-color var(--dur-micro) var(--ease), box-shadow var(--dur-micro) var(--ease);
}

/* hover 故障：RGB 分离外发光（静态，不逐帧重绘）。整卡一起亮，
   因为作者行与动作条都属于这张卡；链接区自己另有焦点环。 */
.post-card:hover {
  border-color: var(--accent);
  box-shadow: 3px 0 0 var(--glitch-r), -3px 0 0 var(--glitch-p), 0 8px 24px rgba(180, 108, 255, 0.25);
}

.card-link {
  display: block;
  color: inherit;
  text-decoration: none;
  min-width: 0;
}

.card-link:hover { text-decoration: none; }

.card-link:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}

.card-title { margin: 0 0 6px; font-size: 15px; }
.cover { display: block; width: 100%; margin-bottom: 10px; border: 1px solid var(--line); border-radius: var(--radius); }
.excerpt { margin: 0; font-size: 13px; }

/* 动作条贴底：卡片高度不一（瀑布流），让动作条始终在最下方。 */
.card-actions { margin-top: auto; }

.sentinel { height: 1px; }
.end { text-align: center; }

.empty-cta { align-self: flex-start; text-decoration: none; }

/* 轻提示：固定在视口底部居中，不推动布局（推动布局会让"回滚"看起来像页面抖了一下）。 */
.toast {
  position: fixed;
  left: 50%;
  bottom: 24px;
  transform: translateX(-50%);
  z-index: 20;
  margin: 0;
  padding: 10px 16px;
  border: 1px solid var(--line-strong);
  background: var(--bg-mid);
  box-shadow: var(--elev-2);
  color: var(--text);
  font-size: 13px;
  max-width: min(520px, calc(100vw - 40px));
}

/* 降级：只留颜色变化，位移类动效全部取消。 */
@media (prefers-reduced-motion: reduce) {
  .post-card { transition: border-color var(--dur-micro) var(--ease); }
}
</style>
