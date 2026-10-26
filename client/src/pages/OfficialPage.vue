<script setup lang="ts">
/**
 * 官方 / 推荐放映区（`/official`）。wayfinder #34：把占位页换成真页面。
 *
 * 它和 `/rooms` 是**同一件事的两个筛选**（服务端就是同一个端点：`?official=true`），
 * 所以卡片是同一个 `RoomCard`、状态口径是同一份 docs §6；本页只多两样东西：
 *
 *  1. 页级身份（标题 + 「运营位」标识）与空态文案 —— 官方区为空的原因
 *     （运营还没标记）和公开列表为空的原因（没人开房）不是同一句话；
 *  2. `official` 回显的判据：若服务端不认识这个筛选参数，整页显示
 *     「官方分区后端尚未上线」，而不是把普通公开房当成官方推荐渲染出来。
 *
 * 免登录：运营位是给人看的，游客也该能看见（`/api/public-rooms` 本身就免登录）。
 */
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import RoomCard from '../components/features/rooms/RoomCard.vue'
import { useApiFailure } from '../components/features/common/useApiFailure'
import {
  OFFICIAL_ROOMS_EMPTY_TEXT,
  OFFICIAL_ROOMS_PAGE_SIZE,
  listOfficialRooms,
  type PublicRoom,
} from '../api/official'

const route = useRoute()
const { failure, run } = useApiFailure(() => route.fullPath)

const rooms = ref<PublicRoom[]>([])
const total = ref(0)
const loading = ref(false)
const capabilityMissing = ref(false)

/** 失败态文案（分诊结果见 `useApiFailure`：401 跳登录、429 稍后重试、其它原样）。 */
const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能查看官方区'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

/** 只取第一页：官方区是运营位，一屏之外的内容靠服务端的 total 如实说明（不静默截断）。 */
async function load(): Promise<void> {
  loading.value = true
  capabilityMissing.value = false
  const page = await run(() => listOfficialRooms({ limit: OFFICIAL_ROOMS_PAGE_SIZE }))
  loading.value = false
  if (page === null) {
    // 能力未上线：整块空态；其余失败留在 `failure` 里由模板显示。
    if (failure.value?.kind === 'capability') capabilityMissing.value = true
    return
  }
  rooms.value = page.items
  total.value = page.total
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="page">
    <main class="body">
      <header class="head">
        <div class="title-row">
          <h1 class="title">房间室 · 官方</h1>
          <span class="tag">运营位</span>
        </div>
        <p class="sub dim">
          由管理员标记的官方与推荐放映，按运营位次排列；与用户自建的公开房分开呈现。
        </p>
      </header>

      <p v-if="loading" class="dim" role="status">加载中…</p>

      <CapabilityNotice
        v-else-if="capabilityMissing"
        domain="official"
        title="官方分区后端尚未上线"
        hint="上线后这里是运营标记的官方 / 推荐放映；普通公开房不受影响。"
        retryable
        @retry="load"
      />

      <p v-else-if="failureText" class="alert" role="alert">
        {{ failureText }}
        <button type="button" class="retry" @click="load">重试</button>
      </p>

      <section v-else-if="rooms.length === 0" class="empty" role="status">
        <h2 class="empty-head">
          <BrandIcon name="shield-done" decorative :size="15" />
          {{ OFFICIAL_ROOMS_EMPTY_TEXT }}
        </h2>
        <p class="dim">
          运营还没有标记任何官方 / 推荐房间。这里只展示被标记的放映 —— 不拿公开房凑数，
          否则「官方」这个标识就不再说明任何事。
        </p>
        <RouterLink class="btn" :to="{ name: 'public-rooms' }">去看看公开的房间</RouterLink>
      </section>

      <template v-else>
        <ul class="cards">
          <RoomCard v-for="room in rooms" :key="room.roomId" :room="room" />
        </ul>
        <p v-if="total > rooms.length" class="note dim" role="status">
          官方区共有 {{ total }} 个放映，这里只显示前 {{ rooms.length }} 个（按运营位次）。
        </p>
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
  gap: 6px;
}
.title-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.title {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: clamp(32px, 6vw, 48px);
}
/* 官方区的**页级**标识：与卡片上的「官方」徽标同一套颜色与描边。 */
.tag {
  padding: 1px 8px;
  border: 1px solid var(--accent);
  color: var(--accent);
  font-size: 11px;
  line-height: 18px;
  white-space: nowrap;
}
.sub {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
}
.cards {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.note {
  margin: 0;
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
  text-decoration: none;
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
.retry:hover {
  border-color: var(--accent);
}
.dim {
  color: var(--dim);
}
</style>
