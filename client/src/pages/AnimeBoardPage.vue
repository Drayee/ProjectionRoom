<script setup lang="ts">
/**
 * 番剧板块（`/forum/:animeId`）。wayfinder #36：四层里的第二、三层入口。
 *
 * 页面做三件事：
 *   1. 板块头（番剧名 / 别名 / 简介 / 封面）—— `GET /api/animes/:id`；
 *   2. 主题串列表，**按最新回复排序**（服务端排好，前端不再排一遍）；
 *   3. 主题串链接到 `/posts/:id` —— 详情与回复**复用帖子域页面**，不造第二套。
 *
 * 三种"没有"必须分开（验收第 2 条）：
 *   - 板块不存在（番剧 id 查不到 → 404）→ 「板块不存在」整块空态；
 *   - 板块是空的（200 + 0 条）→ 「这个板块还没有主题串」；
 *   - 地址里的 id 不合法（本地校验）→ 「板块地址不合法」，一次请求都不发。
 * 把第一个说成第三个、或把"后端没上线"说成"板块不存在"，都会把排查引到错误的方向。
 */
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import {
  FORUM_THREADS_EMPTY_TEXT,
  FORUM_THREADS_PAGE_SIZE,
  getAnime,
  listAnimeThreads,
  type Anime,
  type Post,
} from '../api/forum'
import { isCapabilityUnavailable } from '../api/mode'
import { toHttpError } from '../api/http'
import { useApiFailure } from '../components/features/common/useApiFailure'
import { avatarOf } from '../utils/avatar'

const props = defineProps<{ animeId: string }>()
const route = useRoute()
const { failure, run } = useApiFailure(() => route.fullPath)

/** 路由参数是字符串：只有正整数才是合法板块 id（与服务端 `animeIDParam` 同一口径）。 */
const boardId = computed(() => {
  const raw = String(props.animeId ?? '').trim()
  return /^[1-9]\d*$/.test(raw) ? Number(raw) : 0
})

const anime = ref<Anime | null>(null)
const threads = ref<Post[]>([])
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
/** 整域未上线（路由没注册）。 */
const capabilityMissing = ref(false)
/** 板块不存在（服务端 404 + NOT_FOUND）。 */
const boardMissing = ref(false)
/** 板块头自己的失败（列表失败走 `failure` 的分诊）。 */
const headError = ref('')

const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能继续'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

/** 主题串的"最后活跃"：优先 lastReplyAt（服务端排序键），没有就退回创建时间。 */
function lastActive(post: Post): string {
  const raw = post.lastReplyAt || post.createdAt || ''
  return raw === '' ? '—' : raw.slice(0, 16).replace('T', ' ')
}

async function load(): Promise<void> {
  anime.value = null
  threads.value = []
  hasMore.value = false
  capabilityMissing.value = false
  boardMissing.value = false
  headError.value = ''
  if (boardId.value === 0) return

  loading.value = true
  try {
    anime.value = await getAnime(boardId.value)
  } catch (err) {
    loading.value = false
    if (isCapabilityUnavailable(err)) {
      capabilityMissing.value = true
      return
    }
    const e = toHttpError(err)
    if (e.status === 404) {
      boardMissing.value = true
      return
    }
    headError.value = e.message
    return
  }

  const page = await run(() =>
    listAnimeThreads({
      animeId: boardId.value,
      sort: 'latest-reply',
      limit: FORUM_THREADS_PAGE_SIZE,
      offset: 0,
    }),
  )
  loading.value = false
  if (page === null) {
    if (failure.value?.kind === 'capability') capabilityMissing.value = true
    return
  }
  threads.value = page.items
  hasMore.value = page.hasMore
}

/** 加载更多：offset 用**已加载条数**（与服务端分页一致，不会因为 limit 被夹而漂移）。 */
async function loadMore(): Promise<void> {
  if (loading.value || loadingMore.value || !hasMore.value) return
  loadingMore.value = true
  const page = await run(() =>
    listAnimeThreads({
      animeId: boardId.value,
      sort: 'latest-reply',
      limit: FORUM_THREADS_PAGE_SIZE,
      offset: threads.value.length,
    }),
  )
  loadingMore.value = false
  if (page === null) {
    if (failure.value?.kind === 'capability') {
      capabilityMissing.value = true
      hasMore.value = false
    }
    return
  }
  const seen = new Set(threads.value.map((t) => t.id))
  threads.value = [...threads.value, ...page.items.filter((t) => !seen.has(t.id))]
  hasMore.value = page.hasMore
}

watch(
  () => props.animeId,
  () => {
    void load()
  },
  { immediate: true },
)
</script>

<template>
  <div class="page">
    <main class="body">
      <RouterLink class="back" :to="{ name: 'forum' }">← 番剧目录</RouterLink>

      <p v-if="loading" class="dim" role="status">加载中…</p>

      <CapabilityNotice
        v-else-if="capabilityMissing"
        domain="forum"
        title="番剧板块后端尚未上线"
        hint="上线后这里是该番剧下的主题串列表，按最新回复排序。"
        retryable
        @retry="load"
      />

      <section v-else-if="boardId === 0" class="empty" role="status">
        <h1 class="empty-head">板块地址不合法</h1>
        <p class="dim">板块 id 必须是正整数，例如 <code class="mono">/forum/1</code>。</p>
        <RouterLink class="btn" :to="{ name: 'forum' }">回到番剧目录</RouterLink>
      </section>

      <section v-else-if="boardMissing" class="empty" role="status">
        <h1 class="empty-head">板块不存在</h1>
        <p class="dim">这个番剧条目可能已被管理员删除，或者地址里的 id 写错了。</p>
        <RouterLink class="btn" :to="{ name: 'forum' }">回到番剧目录</RouterLink>
      </section>

      <p v-else-if="headError || failureText" class="alert" role="alert">
        {{ headError || failureText }}
        <button type="button" class="retry" @click="load">重试</button>
      </p>

      <template v-else-if="anime">
        <header class="board">
          <img
            v-if="anime.coverUrl"
            class="cover"
            :src="anime.coverUrl"
            :alt="`${anime.title} 封面`"
            loading="lazy"
          />
          <span v-else class="cover cover-fallback" aria-hidden="true">{{ anime.title.slice(0, 1) }}</span>
          <div class="who">
            <h1 class="title">{{ anime.title }}</h1>
            <p v-if="anime.aliases.length" class="aliases dim">{{ anime.aliases.join(' / ') }}</p>
            <p v-if="anime.summary" class="summary dim">{{ anime.summary }}</p>
          </div>
        </header>

        <section class="threads" aria-labelledby="threads-head">
          <div class="threads-head">
            <h2 id="threads-head">主题串</h2>
            <span class="dim small">按最新回复排序</span>
          </div>

          <p v-if="threads.length === 0" class="dim">{{ FORUM_THREADS_EMPTY_TEXT }}</p>

          <ul v-else class="rows">
            <li v-for="thread in threads" :key="thread.id">
              <RouterLink class="thread" :to="{ name: 'post-detail', params: { id: String(thread.id) } }">
                <span
                  class="avatar"
                  :style="{ background: avatarOf(thread.authorId, thread.author.displayName).avatarColor }"
                  aria-hidden="true"
                  >{{ avatarOf(thread.authorId, thread.author.displayName).avatarChar }}</span
                >
                <span class="thread-main">
                  <strong class="thread-title">{{ thread.title }}</strong>
                  <span class="meta dim">
                    {{ thread.author.displayName }} · 评论 {{ thread.commentCount }} · 赞 {{ thread.likeCount }}
                  </span>
                </span>
                <span class="last dim">最后回复 {{ lastActive(thread) }}</span>
              </RouterLink>
            </li>
          </ul>

          <div class="more-row">
            <button v-if="hasMore && !loadingMore" type="button" class="btn" @click="loadMore">加载更多</button>
            <p v-else-if="loadingMore" class="dim small">加载更多…</p>
            <p v-else-if="threads.length > 0" class="dim small">— 到底了 —</p>
          </div>
        </section>
      </template>

      <p v-else class="dim">加载中…</p>
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
  width: min(880px, 100%);
  margin: 0 auto;
  padding: 28px 20px 64px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.back {
  align-self: flex-start;
  font-size: 12px;
  color: var(--dim);
}
.back:hover {
  color: var(--text);
  text-decoration: none;
}

.board {
  display: grid;
  grid-template-columns: 84px minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}
.cover {
  width: 84px;
  height: 112px;
  object-fit: cover;
  border: 1px solid var(--line);
}
.cover-fallback {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: rgba(180, 108, 255, 0.12);
  color: var(--accent);
  font-size: 34px;
  font-weight: 700;
}
.who {
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 0;
}
.title {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: clamp(26px, 5vw, 40px);
  overflow-wrap: anywhere;
}
.aliases {
  margin: 0;
  font-size: 12px;
  overflow-wrap: anywhere;
}
.summary {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
}

.threads {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.threads-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  border-bottom: 1px solid var(--line);
  padding-bottom: 6px;
}
.threads-head h2 {
  margin: 0;
  font-size: 15px;
}
.small {
  font-size: 12px;
}

.rows {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.thread {
  display: grid;
  grid-template-columns: 32px minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 10px 14px;
  color: inherit;
}
.thread:hover {
  border-color: var(--accent);
  text-decoration: none;
}
.avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 13px;
  font-weight: 700;
}
.thread-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.thread-title {
  font-size: 14px;
  overflow-wrap: anywhere;
}
.meta {
  font-size: 12px;
}
.last {
  font-size: 11px;
  white-space: nowrap;
}

.more-row {
  display: flex;
  justify-content: center;
}
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 8px 20px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
}
.btn:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}
.retry {
  padding: 4px 12px;
  border: 1px solid var(--line);
  color: var(--text);
  font-size: 12px;
}

.empty {
  display: flex;
  flex-direction: column;
  gap: 10px;
  align-items: flex-start;
  border: 1px dashed var(--line);
  background: var(--surface);
  padding: 16px 18px;
}
.empty-head {
  margin: 0;
  font-size: 20px;
}
.empty p {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
}
.mono {
  font-family: var(--font-mono);
}
.alert {
  margin: 0;
  padding: 8px 10px;
  border: 1px solid var(--danger);
  background: rgba(255, 92, 92, 0.12);
  font-size: 12px;
  color: #ffdada;
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.dim {
  color: var(--dim);
}

/* 375 档：板块头封面缩小，主题串的"最后回复"换到第二行；不挤成一列超长。 */
@media (max-width: 560px) {
  .body {
    padding: 20px 14px 48px;
  }
  .board {
    grid-template-columns: 56px minmax(0, 1fr);
    gap: 12px;
  }
  .cover {
    width: 56px;
    height: 74px;
  }
  .thread {
    grid-template-columns: 30px minmax(0, 1fr);
    grid-template-areas:
      'avatar main'
      'last last';
    align-items: start;
  }
  .avatar {
    grid-area: avatar;
  }
  .thread-main {
    grid-area: main;
  }
  .last {
    grid-area: last;
    justify-self: start;
  }
}
</style>
