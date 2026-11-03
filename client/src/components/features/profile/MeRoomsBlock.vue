<script setup lang="ts">
/**
 * 「我的放映室」内容块（票 #70）。只出现在 `/me`，需要登录。
 *
 * # 为什么不复用 `AuthorRoomsBlock`
 *
 * 那一个是**他人档案页的对外橱窗**：只列公开且未关闭的房，字段里连
 * `isPublic`/`isClosed` 都没有 —— 因为对访客而言"这间房曾经关过"不是信息。
 * 这一块恰恰相反：**全部房**（含私密与已关闭），而那两个布尔正是它存在的理由
 * （房主要据此把一间房重新开放）。硬塞进一个组件会让每个字段都要问
 * "现在这个视角该不该显示它"，而那正是两份实现开始漂移的起点。
 *
 * # 为什么每行要画状态标记
 *
 * 只列房间码与标题的话，"私密"与"已关闭"两间房在界面上长得一模一样 ——
 * 用户会以为自己记错了。标记不是装饰，是这一页唯一能解释"为什么我在公开房列表里
 * 看不到它"的东西。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AsyncState from '../../ui/AsyncState.vue'
import BrandIcon from '../../ui/BrandIcon.vue'
import RelativeTime from '../../ui/RelativeTime.vue'
import * as myRoomsApi from '../../../api/myRooms'
import { isCapabilityUnavailable } from '../../../api/mode'
import { toHttpError } from '../../../api/http'
import type { MyRoomItem } from '../../../types/rooms'

const items = ref<MyRoomItem[]>([])
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
const errorText = ref('')
/** 整域未上线（真实实现撞上 NoRoute 的纯文本 404）。 */
const capabilityMissing = ref(false)

/** 三态优先级与其它内容块一致：能力降级 > 错误 > 载/空。 */
const state = computed<'loading' | 'empty' | 'error' | 'capability' | 'ready'>(() => {
  if (capabilityMissing.value) return 'capability'
  if (errorText.value !== '') return 'error'
  if (loading.value) return 'loading'
  if (items.value.length === 0) return 'empty'
  return 'ready'
})

const sentinel = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | undefined

/** 请求序号：翻页与重试交错时，迟到的响应不许覆盖最新那份。 */
let seq = 0

async function load(reset: boolean): Promise<void> {
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
    const result = await myRoomsApi.listMyRooms({ page: page.value })
    if (mine !== seq) return
    items.value = reset ? result.items : [...items.value, ...result.items]
    hasMore.value = result.hasMore
  } catch (err) {
    if (mine !== seq) return
    if (isCapabilityUnavailable(err)) {
      capabilityMissing.value = true
    } else {
      errorText.value = toHttpError(err).message
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

/** 状态标记：私密 / 已关闭 是两个独立事实，可能同时成立。 */
function badgesOf(room: MyRoomItem): string[] {
  const out: string[] = []
  if (!room.isPublic) out.push('私密')
  if (room.isClosed) out.push('已关闭')
  return out
}

onMounted(() => {
  void load(true)
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
  <section class="block" aria-labelledby="me-rooms-head">
    <div class="block-head">
      <h2 id="me-rooms-head" class="block-title">我的放映室</h2>
      <span v-if="state === 'ready'" class="dim small">{{ items.length }} 间</span>
    </div>

    <AsyncState
      v-if="state !== 'ready'"
      :state="state === 'capability' ? 'capability' : state"
      :title="
        state === 'empty'
          ? '你还没开过放映室'
          : state === 'error'
            ? '放映室没读出来'
            : state === 'capability'
              ? '放映室分区尚未上线'
              : undefined
      "
      :hint="
        state === 'empty'
          ? '在首页的「作为主播创建房间」卡片里开一间房，它会出现在这里 —— 私密与已关闭的房也都在这一页。'
          : state === 'error'
            ? errorText
            : undefined
      "
      :retryable="state === 'error'"
      icon="video"
      @retry="retry"
    >
      <!-- 落点是**首页**而不是 public-rooms：公开房间页只有列表，历来没有建房按钮
           （这条链接曾指向那里，点过去找不到入口 —— 用户报「无法建房间」的一部分）。 -->
      <RouterLink v-if="state === 'empty'" class="cta" :to="{ name: 'home' }">
        去开一间房
      </RouterLink>
    </AsyncState>

    <template v-else>
      <ul class="rows">
        <li v-for="room in items" :key="room.roomId" class="row">
          <div class="row-main">
            <span class="row-title">
              <BrandIcon name="video" decorative :size="14" />
              {{ room.title || '未命名房间' }}
              <span v-for="badge in badgesOf(room)" :key="badge" class="badge">{{ badge }}</span>
              <span v-if="room.hasPassword" class="lock" role="img" aria-label="有密码">
                <BrandIcon name="shield-done" decorative :size="13" />
              </span>
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
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  font-size: 14px;
  overflow-wrap: anywhere;
}
.row-meta {
  display: flex;
  align-items: baseline;
  gap: 6px;
  flex-wrap: wrap;
}
.badge {
  padding: 1px 6px;
  border: 1px solid var(--line-strong);
  color: var(--dim);
  font-size: 11px;
  font-weight: 400;
}
.lock {
  display: inline-flex;
  color: var(--dim);
}
.enter {
  padding: 4px 12px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
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
.cta {
  display: inline-flex;
  align-items: center;
  padding: 6px 16px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
}
.cta:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}
.dim {
  color: var(--dim);
}
.small {
  font-size: 12px;
}
@media (max-width: 560px) {
  .row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
