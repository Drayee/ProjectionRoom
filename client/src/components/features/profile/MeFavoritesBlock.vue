<script setup lang="ts">
/**
 * 「我的收藏」区块（票 #65）。住在 `/me?tab=favorites`，`?type=post|anime|room`。
 *
 * ## 为什么它只出现在 /me
 *
 * 收藏是**私人书签**（决议 #48 §3）：不产生任何公开计数，除本人外全世界看不到。
 * 所以 `/user/:id` 上**没有**这一块，也不该有 —— 连"这里本来有个收藏块"的痕迹都不留。
 * 票 #68 的 `verify-profile.mjs` 有 P4 判据钉住这一点，本票不重开那个口子。
 *
 * ## 三份书签是三个 tab，不是三块并排
 *
 * 收藏是**跨对象**的：帖子、番剧、放映室三种类型的主键形态都不同
 * （前两个是自增 id，放映室是房间码）。混排需要一个跨类型的时间序，
 * 而"这一页到底是哪一类"会变得说不清。所以按类型分 tab，`?type=` 进 URL
 * （键名唯一源 `utils/meRoute.ts`，与 `?tab=` / `?list=` 同族）。
 *
 * ## 失效态：保留 + 灰显 + 不可点（决议 #48 §5）
 *
 * 对象失效（帖子被删 / 番剧下架 / 放映室关闭或取消公开）时**条目仍然在**，
 * 只是 `valid=false`：灰显、不给跳转、旁边一个「移除」。
 * 静默删掉会让"我的收藏莫名变少了"变成一个没人能解释的现象 ——
 * 而用户其实什么都没做。
 *
 * ## 分页
 *
 * 游标式（`before` = 上一页最后一行的 `cursor`），不是页码：收藏会在使用中
 * 增删，页码分页会因为"前面少了一条"而漏行。`IntersectionObserver` 到底自动加载，
 * 观察器不可用时退化成「加载更多」按钮。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import AsyncState from '../../ui/AsyncState.vue'
import Button from '../../ui/Button.vue'
import RelativeTime from '../../ui/RelativeTime.vue'
import * as favoritesApi from '../../../api/favorites'
import type { FavoriteEntry, FavoriteType } from '../../../types/favorites'
import { isCapabilityUnavailable } from '../../../api/mode'
import { toHttpError } from '../../../api/http'
import { ME_FAV_TYPE_QUERY, ME_TAB_QUERY, mePath, parseMeFavType, type MeFavoriteType } from '../../../utils/meRoute'

const route = useRoute()
const router = useRouter()

/** 三份书签（顺序即界面顺序：帖子最多、放映室最特殊放最后）。 */
const TYPES: { key: MeFavoriteType; label: string; icon: string }[] = [
  { key: 'post', label: '帖子', icon: 'chat' },
  { key: 'anime', label: '番剧', icon: 'star-fill' },
  { key: 'room', label: '放映室', icon: 'video' },
]

/** 当前类型：**唯一真源是 URL**（与关系分区的 `list` 同一条纪律）。 */
const type = computed<FavoriteType>(() => parseMeFavType(route.query[ME_FAV_TYPE_QUERY]))

function selectType(next: MeFavoriteType): void {
  notice.value = ''
  errorText.value = ''
  void router.replace(mePath({ tab: 'favorites', type: next }))
}

const items = ref<FavoriteEntry[]>([])
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
const errorText = ref('')
const capabilityMissing = ref(false)
const notice = ref('')
/** 正在移除的那一行（按 `<type>:<targetId>` 记，避免整块禁用）。 */
const removing = ref<string[]>([])

/** 三态判定：能力降级 > 错误 > 载/空。 */
const state = computed<'loading' | 'empty' | 'error' | 'capability' | 'ready'>(() => {
  if (capabilityMissing.value) return 'capability'
  if (errorText.value !== '') return 'error'
  if (loading.value) return 'loading'
  if (items.value.length === 0) return 'empty'
  return 'ready'
})

/** 空态文案按类型给 —— 三份书签的"本来会有什么"不一样。 */
const emptyCopy = computed(() => {
  if (type.value === 'anime') {
    return { title: '还没有收藏番剧', hint: '在番剧目录里看到想追的，点一下收藏就会出现在这里。' }
  }
  if (type.value === 'room') {
    return { title: '还没有收藏放映室', hint: '在公开房间或官方推荐里看到想再来的房间，收藏它。' }
  }
  return { title: '还没有收藏帖子', hint: '在帖子列表或详情页点一下收藏，它就会留在这里 —— 只有你看得见。' }
})

const sentinel = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | undefined

/**
 * 请求序号：切类型之后，上一个类型的迟到响应必须被丢弃。
 *
 * 症状是"点了番剧，列表先空一下、然后帖子的数据盖上来"——两次请求都正常，
 * 只靠看响应内容是查不出来的（与 `AuthorPostsBlock` 同一条教训）。
 */
let seq = 0

function messageOf(err: unknown): string {
  return toHttpError(err).message
}

function keyOf(entry: FavoriteEntry): string {
  return entry.targetType + ':' + entry.targetId
}

/** 取一页。`reset` 时从头开始（切类型 / 重试 / 移除后重取）。 */
async function load(reset: boolean, before?: string): Promise<void> {
  const mine = ++seq
  if (reset) {
    loading.value = true
    errorText.value = ''
    capabilityMissing.value = false
    items.value = []
    hasMore.value = false
  } else {
    loadingMore.value = true
  }
  try {
    const result = await favoritesApi.listFavorites({ type: type.value, before })
    if (mine !== seq) return
    items.value = reset ? result.items : [...items.value, ...result.items]
    hasMore.value = result.hasMore
  } catch (err) {
    if (mine !== seq) return
    if (isCapabilityUnavailable(err)) capabilityMissing.value = true
    else errorText.value = messageOf(err)
  } finally {
    if (mine === seq) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

async function loadMore(): Promise<void> {
  if (!hasMore.value || loading.value || loadingMore.value) return
  const last = items.value[items.value.length - 1]
  if (!last) return
  await load(false, last.cursor)
}

function retry(): void {
  void load(true)
}

/**
 * 移除一条收藏。
 *
 * 失效条目也走这条路 —— 这正是它存在的意义：对象没了，书签还在，
 * 用户需要一个地方把它清掉。`setFavorite(..., false)` 是幂等的（DELETE），
 * 所以双击/重试都不会出问题。
 */
async function remove(entry: FavoriteEntry): Promise<void> {
  const key = keyOf(entry)
  if (removing.value.includes(key)) return
  removing.value = [...removing.value, key]
  notice.value = ''
  errorText.value = ''
  try {
    await favoritesApi.setFavorite(entry.targetType, entry.targetId, false)
    // 本地移除而不是整块重取：重取会把已经翻过的几页收回去，
    // 用户刚才滚到的位置就没了。幂等 DELETE 保证这里不会留下幽灵行。
    items.value = items.value.filter((row) => keyOf(row) !== key)
    notice.value = '已从收藏里移除'
    // 列表被清空时补一次 —— 否则空态永远等不到（`state` 依赖 `items.length`，
    // 这一条只是为了让 hasMore 与空态文案对齐，不重取数据）。
    if (items.value.length === 0) hasMore.value = false
  } catch (err) {
    errorText.value = messageOf(err)
  } finally {
    removing.value = removing.value.filter((k) => k !== key)
  }
}

/** 失效条目的说明：不同类型失效原因不同，别用一句"已失效"糊过去。 */
function invalidHint(row: FavoriteEntry): string {
  if (row.targetType === 'room') return '已失效：这间放映室已关闭或不再公开'
  if (row.targetType === 'anime') return '已失效：这部番剧已下架'
  return '已失效：这篇帖子已被删除'
}

/** 有效条目的落点（失效时调用方不渲染链接）。 */
function targetOf(row: FavoriteEntry) {
  if (row.targetType === 'room') return { name: 'room', params: { roomId: row.targetId } }
  if (row.targetType === 'anime') return { name: 'anime-board', params: { animeId: row.targetId } }
  return { name: 'post-detail', params: { id: row.targetId } }
}

watch(
  () => type.value,
  () => {
    notice.value = ''
    void load(true)
  },
  { immediate: true },
)

onMounted(() => {
  if (typeof IntersectionObserver === 'undefined' || !sentinel.value) return
  observer = new IntersectionObserver(
    (entries) => {
      if (entries.some((e) => e.isIntersecting)) void loadMore()
    },
    { rootMargin: '200px' },
  )
  observer.observe(sentinel.value)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = undefined
})
</script>

<template>
  <section class="fav" aria-labelledby="me-favorites-head">
    <div class="fav-head">
      <h2 id="me-favorites-head" class="sect-title">我的收藏</h2>
      <p class="dim small">
        收藏是私人书签：只有你看得见，也不计入任何公开数字。
        在 <RouterLink :to="{ name: 'posts' }">帖子</RouterLink> /
        <RouterLink :to="{ name: 'forum' }">番剧</RouterLink> /
        <RouterLink :to="{ name: 'public-rooms' }">放映室</RouterLink> 里点收藏，就会出现在这里。
      </p>
    </div>

    <nav class="types" role="tablist" aria-label="收藏类型">
      <Button
        v-for="t in TYPES"
        :key="t.key"
        variant="ghost"
        class="type-tab"
        role="tab"
        :aria-selected="type === t.key"
        :class="{ on: type === t.key }"
        @click="selectType(t.key)"
      >
        {{ t.label }}
      </Button>
    </nav>

    <p v-if="notice" class="ok" role="status">{{ notice }}</p>
    <p v-if="errorText" class="alert" role="alert">{{ errorText }}</p>

    <AsyncState
      v-if="state === 'capability'"
      state="capability"
      icon="star-fill"
      title="收藏分区尚未上线"
      hint="服务端还没提供收藏接口，这里暂时读不到内容。"
    />

    <AsyncState v-else-if="state === 'loading'" state="loading" title="收藏加载中…" />

    <AsyncState
      v-else-if="state === 'error'"
      state="error"
      title="收藏没读出来"
      :hint="errorText"
      @retry="retry"
    />

    <AsyncState
      v-else-if="state === 'empty'"
      state="empty"
      icon="star-fill"
      :title="emptyCopy.title"
      :hint="emptyCopy.hint"
    >
      <RouterLink
        v-if="type === 'anime'"
        class="btn"
        :to="{ name: 'forum' }"
      >去番剧目录</RouterLink>
      <RouterLink
        v-else-if="type === 'room'"
        class="btn"
        :to="{ name: 'public-rooms' }"
      >去公开房间</RouterLink>
      <RouterLink v-else class="btn" :to="{ name: 'posts' }">去帖子广场</RouterLink>
    </AsyncState>

    <template v-else>
      <ul class="rows" data-testid="me-favorite-rows">
        <li v-for="row in items" :key="keyOf(row)" class="row" :class="{ invalid: !row.valid }">
          <div class="row-main">
            <component
              :is="row.valid ? RouterLink : 'span'"
              v-bind="row.valid ? { to: targetOf(row), class: 'row-title' } : { class: 'row-title plain' }"
            >{{ row.title }}</component>
            <p class="row-meta">
              <span class="kind">{{ row.targetType === 'post' ? '帖子' : row.targetType === 'anime' ? '番剧' : '放映室' }}</span>
              <span aria-hidden="true">·</span>
              <RelativeTime :value="row.createdAt" />
              <template v-if="!row.valid">
                <span aria-hidden="true">·</span>
                <span class="invalid-hint">{{ invalidHint(row) }}</span>
              </template>
            </p>
          </div>
          <Button
            variant="ghost"
            size="sm"
            :disabled="removing.includes(keyOf(row))"
            :loading="removing.includes(keyOf(row))"
            @click="remove(row)"
          >移除</Button>
        </li>
      </ul>

      <div ref="sentinel" class="sentinel" aria-hidden="true"></div>
      <div class="more-row">
        <Button v-if="hasMore" variant="ghost" :loading="loadingMore" @click="loadMore">加载更多</Button>
        <span v-else class="dim small">到底了</span>
      </div>
    </template>
  </section>
</template>

<style scoped>
.fav {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.fav-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.types {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.type-tab {
  --ui-btn-bg-hover: var(--state-hover);
  --ui-btn-bg-active: var(--state-active);
  --ui-btn-fg-hover: var(--text);
  --ui-btn-fg-active: var(--text);
}

.type-tab.on {
  color: var(--text);
  background: var(--state-active);
  border-color: var(--accent);
}

.rows {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 10px;
  align-items: center;
  padding: 10px 12px;
  border: 1px solid var(--line);
  background: var(--surface);
}

.row.invalid {
  opacity: 0.62;
}

.row-main {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.row-title {
  font-size: 14px;
  color: var(--text);
  text-decoration: none;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

a.row-title:hover {
  color: var(--accent);
  text-decoration: underline;
}

/* 失效条目不是链接：光标与颜色都要说清"这不能点"。 */
.row-title.plain {
  color: var(--dim);
  cursor: default;
}

.row-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 6px;
  margin: 0;
  font-size: 12px;
  color: var(--dim);
}

.invalid-hint {
  color: var(--danger);
}

.sentinel {
  height: 1px;
}

.more-row {
  display: flex;
  justify-content: center;
  padding: 4px 0 8px;
}

@media (max-width: 560px) {
  .row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
