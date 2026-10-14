<script setup lang="ts">
/**
 * 公开房列表（/rooms）。骨架阶段最小实现：store 已有分页状态机，页面只做渲染。
 * 硬契约（票 #16）：空态文案用 api/publicRooms 导出的 PUBLIC_ROOMS_EMPTY_TEXT。
 */
import { onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import AppHeader from '../components/ui/AppHeader.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'
import { PUBLIC_ROOMS_EMPTY_TEXT } from '../api/publicRooms'
import { usePublicRoomsStore } from '../stores/publicRooms'

const store = usePublicRoomsStore()

onMounted(() => {
  void store.load()
})
</script>

<template>
  <div class="page">
    <AppHeader variant="page" />
    <main class="body">
      <h1 class="title">公开的房间</h1>
      <p v-if="store.loading" class="dim">加载中…</p>
      <p v-else-if="store.error" class="alert">{{ store.error }}</p>
      <p v-else-if="store.empty" class="dim">{{ PUBLIC_ROOMS_EMPTY_TEXT }}</p>
      <ul v-else class="list">
        <li v-for="room in store.items" :key="room.roomId">
          <BrandIcon name="video" decorative :size="15" />
          <span class="name">{{ room.title || '未命名房间' }}</span>
          <span class="dim mono">{{ room.roomId }}</span>
          <span v-if="room.hasPassword" class="lock" aria-label="有密码">
            <BrandIcon name="shield-done" decorative :size="13" />
          </span>
          <RouterLink class="enter" :to="{ name: 'room', params: { roomId: room.roomId } }">
            进房
          </RouterLink>
        </li>
      </ul>
      <button v-if="store.hasMore && !store.loadingMore" class="more" type="button" @click="store.loadMore()">
        加载更多
      </button>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(880px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 14px; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(32px, 6vw, 48px); }
.list { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 8px; }
.list li { display: flex; align-items: center; gap: 8px; border: 1px solid var(--line); background: var(--surface); padding: 10px 12px; font-size: 13px; }
.name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lock { color: var(--dim); display: inline-flex; }
.enter { flex: none; padding: 5px 12px; border: 1px solid var(--line); color: var(--dim); font-size: 12px; }
.enter:hover { color: var(--text); border-color: var(--accent); text-decoration: none; }
.alert { color: var(--danger); }
.more { align-self: center; padding: 8px 20px; border: 1px solid var(--line); color: var(--dim); }
.more:hover { color: var(--text); border-color: var(--accent); }
</style>
