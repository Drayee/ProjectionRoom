<script setup lang="ts">
/**
 * 番剧论坛 · 目录（`/forum`）。wayfinder #36：把占位页换成真页面。
 *
 * 四层里的第一层：番剧目录（搜索 + 分页）。搜索词与页码**进 URL**
 * （`?q=&offset=`），因此可分享、刷新不丢、后退键自然成立 —— 与 `/admin/rooms`
 * 的筛选分页是同一条既有约定。
 *
 * 空态分两种，文案必须不同（验收第 2 条）：
 *   - 目录本来就没有番剧（运营还没录入）；
 *   - 搜了但没匹配（换关键词或清除搜索）。
 * "没有"与"没搜到"在界面上同形时，用户会以为整个目录被清空了。
 */
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import {
  FORUM_ANIME_PAGE_SIZE,
  FORUM_EMPTY_TEXT,
  FORUM_NO_MATCH_TEXT,
  listAnimes,
  type Anime,
} from '../api/forum'
import { useApiFailure } from '../components/features/common/useApiFailure'

const route = useRoute()
const router = useRouter()
const { failure, run } = useApiFailure(() => route.fullPath)

const PAGE_SIZE = FORUM_ANIME_PAGE_SIZE

const items = ref<Anime[]>([])
const total = ref(0)
const busy = ref(false)
const capabilityMissing = ref(false)

/** 输入框是本地态，提交后才写回 URL（打字过程中不触发请求）。 */
const keyword = ref('')

/** 当前生效的搜索词（来自 URL，单一真相）。 */
const activeQuery = computed(() => String(route.query.q ?? '').trim())

/** 当前页码偏移（来自 URL）。 */
const offset = computed(() => {
  const raw = Number(route.query.offset ?? 0)
  return Number.isFinite(raw) && raw > 0 ? Math.floor(raw) : 0
})

const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能继续'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

const rangeText = computed(() => {
  if (total.value === 0) return '共 0 部'
  const from = offset.value + 1
  const to = Math.min(offset.value + items.value.length, total.value)
  return `第 ${from}–${to} 部 · 共 ${total.value} 部`
})

async function load(): Promise<void> {
  busy.value = true
  const page = await run(() =>
    listAnimes({ q: activeQuery.value, limit: PAGE_SIZE, offset: offset.value }),
  )
  busy.value = false
  if (page === null) {
    if (failure.value?.kind === 'capability') {
      capabilityMissing.value = true
      items.value = []
      total.value = 0
    }
    return
  }
  capabilityMissing.value = false
  items.value = page.items
  total.value = page.total
}

/** 提交搜索：清掉 offset，重新从第一页看。 */
function submitSearch(): void {
  const q = keyword.value.trim()
  const next = { ...(q === '' ? {} : { q }), offset: undefined }
  // 同一个词再点一次搜索：URL 没变化，router.push 不会触发 watch，所以手动重载。
  if (q === activeQuery.value && offset.value === 0) {
    void load()
    return
  }
  void router.push({ name: 'forum', query: next })
}

function clearSearch(): void {
  keyword.value = ''
  void router.push({ name: 'forum' })
}

function goOffset(next: number): void {
  const query = { ...(activeQuery.value === '' ? {} : { q: activeQuery.value }), ...(next > 0 ? { offset: String(next) } : {}) }
  void router.push({ name: 'forum', query })
}

/** 搜索词来自 URL 变化时同步输入框（分享链接 / 后退键）。 */
watch(
  activeQuery,
  (q) => {
    keyword.value = q
  },
  { immediate: true },
)

/** URL 的 q 或 offset 变了才重拉。 */
watch(
  () => `${activeQuery.value}|${offset.value}`,
  () => {
    void load()
  },
  { immediate: true },
)
</script>

<template>
  <div class="page">
    <main class="body">
      <header class="head">
        <h1 class="title">番剧论坛</h1>
        <p class="sub dim">一部番一个板块：板块里是按最新回复排序的主题串，主题串的回复复用帖子评论。</p>
      </header>

      <form class="search" role="search" @submit.prevent="submitSearch">
        <label class="sr-only" for="anime-search">搜索番剧</label>
        <input
          id="anime-search"
          v-model="keyword"
          type="search"
          placeholder="按番剧名或别名搜索，如 芙莉莲 / Frieren"
          autocomplete="off"
        />
        <button type="submit" class="primary">搜索</button>
        <button v-if="activeQuery" type="button" class="btn" @click="clearSearch">清除</button>
      </form>

      <p v-if="busy" class="dim" role="status">加载中…</p>

      <CapabilityNotice
        v-else-if="capabilityMissing"
        domain="forum"
        title="番剧论坛后端尚未上线"
        hint="上线后这里是番剧目录 → 板块 → 主题串 → 回复；帖子与评论不受影响。"
        retryable
        @retry="load"
      />

      <p v-else-if="failureText" class="alert" role="alert">
        {{ failureText }}
        <button type="button" class="retry" @click="load">重试</button>
      </p>

      <!-- 空目录 vs 搜不到：两种"没有"不是同一件事。 -->
      <section v-else-if="items.length === 0 && activeQuery === ''" class="empty" role="status">
        <h2 class="empty-head">
          <BrandIcon name="star" decorative :size="15" />
          {{ FORUM_EMPTY_TEXT }}
        </h2>
        <p class="dim">管理员可以在管理端录入番剧条目；录入后这里会出现对应的板块。</p>
      </section>

      <section v-else-if="items.length === 0" class="empty" role="status">
        <h2 class="empty-head">
          <BrandIcon name="tips" decorative :size="15" />
          {{ FORUM_NO_MATCH_TEXT }}
        </h2>
        <p class="dim">没有番剧匹配「{{ activeQuery }}」。换个关键词，或清除搜索看全部。</p>
        <button type="button" class="btn" @click="clearSearch">清除搜索</button>
      </section>

      <template v-else>
        <ul class="catalog">
          <li v-for="anime in items" :key="anime.id">
            <RouterLink class="anime" :to="{ name: 'anime-board', params: { animeId: String(anime.id) } }">
              <img v-if="anime.coverUrl" class="cover" :src="anime.coverUrl" :alt="`${anime.title} 封面`" loading="lazy" />
              <span v-else class="cover cover-fallback" aria-hidden="true">{{ anime.title.slice(0, 1) }}</span>
              <span class="who">
                <strong class="name">{{ anime.title }}</strong>
                <span v-if="anime.aliases.length" class="aliases dim">{{ anime.aliases.join(' / ') }}</span>
                <span v-if="anime.summary" class="summary dim">{{ anime.summary }}</span>
              </span>
              <span class="enter" aria-hidden="true">进入板块 →</span>
            </RouterLink>
          </li>
        </ul>

        <!-- 分页条的条件不能只看 total > PAGE_SIZE：手改 `?offset=` 或最后一页时
             也需要「上一页」，否则用户会被困在一页里回不去。 -->
        <nav v-if="total > PAGE_SIZE || offset > 0" class="pager" aria-label="目录分页">
          <button type="button" class="btn" :disabled="offset === 0" @click="goOffset(Math.max(0, offset - PAGE_SIZE))">
            上一页
          </button>
          <span class="range dim">{{ rangeText }}</span>
          <button
            type="button"
            class="btn"
            :disabled="offset + items.length >= total"
            @click="goOffset(offset + PAGE_SIZE)"
          >
            下一页
          </button>
        </nav>
      </template>
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
  padding: 32px 20px 64px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.title {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: clamp(32px, 6vw, 48px);
}
.sub {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
}

.search {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.search input {
  flex: 1;
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--line);
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
}

.catalog {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.anime {
  display: grid;
  grid-template-columns: 56px minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 10px 14px;
  color: inherit;
}
.anime:hover {
  border-color: var(--accent);
  text-decoration: none;
}
.cover {
  width: 56px;
  height: 74px;
  object-fit: cover;
  border: 1px solid var(--line);
}
.cover-fallback {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: var(--surface-2, rgba(180, 108, 255, 0.12));
  color: var(--accent);
  font-size: 22px;
  font-weight: 700;
}
.who {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.name {
  font-size: 15px;
  overflow-wrap: anywhere;
}
.aliases {
  font-size: 12px;
  overflow-wrap: anywhere;
}
.summary {
  font-size: 12px;
  line-height: 1.6;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.enter {
  font-size: 12px;
  color: var(--dim);
  white-space: nowrap;
}

.pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.range {
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
  font-size: 15px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.empty p {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
}

.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 6px 14px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
}
.btn:hover {
  color: var(--text);
  border-color: var(--accent);
}
.btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.primary {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 8px 18px;
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
  font-size: 13px;
  white-space: nowrap;
}
.primary:hover {
  filter: brightness(1.08);
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
.retry {
  padding: 4px 12px;
  border: 1px solid var(--line);
  color: var(--text);
  font-size: 12px;
}
.dim {
  color: var(--dim);
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

/* 375 档：封面缩小、进板块提示换行，摘要保持两行截断。 */
@media (max-width: 560px) {
  .body {
    padding: 20px 14px 48px;
  }
  .anime {
    grid-template-columns: 44px minmax(0, 1fr);
    grid-template-areas:
      'cover who'
      'enter enter';
    align-items: start;
  }
  .cover {
    grid-area: cover;
    width: 44px;
    height: 58px;
  }
  .who {
    grid-area: who;
  }
  .enter {
    grid-area: enter;
    justify-self: start;
  }
}
</style>
