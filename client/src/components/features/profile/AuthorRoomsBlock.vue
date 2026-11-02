<script setup lang="ts">
/**
 * 他人档案的「TA 的放映室」内容块（票 #68）。免登录可读。
 *
 * # 为什么和「TA 的帖子」是两个组件而不是一个带 kind 的组件
 *
 * 两块的行形状完全不同：帖子是"标题 + 摘要 + 计数"，放映室是"房间码 + 标题 +
 * 最近活跃 + 进房"。硬合成一个组件要么让模板里长出一堆 `v-if="kind === 'room'"`，
 * 要么抽出一层"行内容 slot"—— 后者的代价是每块的分页/三态逻辑还得再抄一遍。
 * 两份各自 150 行的组件比一个 300 行、每条判据都要读两遍的组件更好维护。
 *
 * # 与公开房列表的口径
 *
 * 服务端 `ListPublicRoomsByOwner` 的 WHERE 是 `owner_user_id = ? AND is_public
 * AND closed_at IS NULL` —— 与 `GET /api/public-rooms` **完全同口径**。
 * 前端不再筛一遍：两边不一致时"以谁为准"会变成一个没有答案的问题。
 *
 * # 为什么行里没有"多少人在看 / 主播在不在"
 *
 * 那些是**内存实况**，属于公开房列表（它要跟房间管理器求交）。档案页要回答的是
 * "这个人开过哪些房"，一个历史事实。混进实况的代价是这个块要为"房间当前
 * 有没有人"显示加载中，而那个信息对"我要不要去看看 TA 的房"并不重要。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import AsyncState from '../../ui/AsyncState.vue'
import BrandIcon from '../../ui/BrandIcon.vue'
import RelativeTime from '../../ui/RelativeTime.vue'
import * as usersApi from '../../../api/users'
import { isCapabilityUnavailable } from '../../../api/mode'
import { toHttpError } from '../../../api/http'
import type { AuthorRoomItem } from '../../../types/users'

const props = withDefaults(
  defineProps<{
    /** 房主 id。`/me` 传自己的 id，档案页传路由里的 id。 */
    userId: number
    /** 块标题：他人档案页「TA 的放映室」，`/me` 说「我的放映室」（票 #69）。 */
    title?: string
    /** 主语：他人视角用「TA」，本人视角用「你」。只影响文案，不影响行为。 */
    self?: boolean
  }>(),
  { title: 'TA 的放映室', self: false },
)

const items = ref<AuthorRoomItem[]>([])
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
const errorText = ref('')
const capabilityMissing = ref(false)

const state = computed<'loading' | 'empty' | 'error' | 'capability' | 'ready'>(() => {
  if (capabilityMissing.value) return 'capability'
  if (errorText.value !== '') return 'error'
  if (loading.value) return 'loading'
  if (items.value.length === 0) return 'empty'
  return 'ready'
})

const sentinel = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | undefined
/** 请求序号：换人之后上一个 id 的迟到响应必须丢弃（与帖块同一理由）。 */
let seq = 0

function messageOf(err: unknown): string {
  return toHttpError(err).message
}

async function load(reset: boolean): Promise<void> {
  if (props.userId <= 0) return
  const mine = ++seq
  if (reset) {
    loading.value = true
    errorText.value = ''
    capabilityMissing.value = false
    page.value = 1
    items.value = []
  } else {
    loadingMore.value = true
  }
  try {
    const result = await usersApi.listAuthorRooms(props.userId, { page: page.value })
    if (mine !== seq) return
    items.value = reset ? result.items : [...items.value, ...result.items]
    hasMore.value = result.hasMore
  } catch (err) {
    if (mine !== seq) return
    if (isCapabilityUnavailable(err)) {
      capabilityMissing.value = true
    } else {
      errorText.value = messageOf(err)
    }
  } finally {
    if (mine === seq) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

async function loadMore(): Promise<void> {
  if (!hasMore.value || loading.value || loadingMore.value) return
  page.value += 1
  await load(false)
}

function retry(): void {
  void load(true)
}

watch(
  () => props.userId,
  () => {
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
  <section class="block" aria-labelledby="author-rooms-head">
    <div class="block-head">
      <h2 id="author-rooms-head" class="block-title">{{ props.title }}</h2>
      <span v-if="state === 'ready'" class="dim small">{{ items.length }} 间</span>
    </div>

    <AsyncState
      v-if="state !== 'ready'"
      :state="state === 'capability' ? 'capability' : state"
      :title="
        state === 'empty'
          ? props.self
            ? '你还没开过公开放映室'
            : 'TA 还没开过公开放映室'
          : state === 'error'
            ? '放映室没读出来'
            : state === 'capability'
              ? '放映室分区尚未上线'
              : undefined
      "
      :hint="
        state === 'empty'
          ? props.self
            ? '在公开房间页开一间房，它会出现在这里。'
            : '只列已公开且未关闭的房间 —— 私密房与已关闭的房不在这里出现。'
          : state === 'error'
            ? errorText
            : undefined
      "
      :retryable="state === 'error'"
      icon="video"
      @retry="retry"
    />

    <template v-else>
      <ul class="rows">
        <li v-for="room in items" :key="room.roomId" class="row">
          <div class="row-main">
            <span class="row-title">
              <BrandIcon name="video" decorative :size="14" />
              {{ room.title || '未命名房间' }}
            </span>
            <span class="row-meta dim small">
              <span class="mono">{{ room.roomId }}</span>
              <template v-if="room.lastSeenAt">
                <span aria-hidden="true">·</span>
                <span>最近活跃 <RelativeTime :value="room.lastSeenAt" /></span>
              </template>
            </span>
          </div>
          <RouterLink class="enter" :to="{ name: 'room', params: { roomId: room.roomId } }">进房</RouterLink>
        </li>
      </ul>

      <div ref="sentinel" class="sentinel" aria-hidden="true" />
      <div class="more-row">
        <button v-if="hasMore && !loadingMore" type="button" class="more-btn" @click="loadMore">
          加载更多
        </button>
        <p v-else-if="loadingMore" class="dim small">加载更多…</p>
        <p v-else class="dim small">— 到底了 —</p>
      </div>
    </template>
  </section>
</template>

<style scoped>
.block {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 10px;
  border-top: 1px solid var(--line);
  padding-top: 16px;
}
.block-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
}
.block-title {
  margin: 0;
  font-size: 15px;
}
.rows {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 10px 14px;
}
.row-main {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.row-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  overflow-wrap: anywhere;
}
.row-meta {
  display: flex;
  align-items: baseline;
  gap: 6px;
  flex-wrap: wrap;
}
.enter {
  padding: 6px 14px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
  white-space: nowrap;
}
.enter:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}
.sentinel {
  height: 1px;
}
.more-row {
  display: flex;
  justify-content: center;
  padding: 4px 0;
}
.more-btn {
  padding: 6px 16px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
  background: transparent;
}
.more-btn:hover {
  color: var(--text);
  border-color: var(--accent);
}
.mono {
  font-family: var(--font-mono);
}
.dim {
  color: var(--dim);
}
.small {
  font-size: 12px;
}
</style>
