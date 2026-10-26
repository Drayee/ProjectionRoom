<script setup lang="ts">
/**
 * 公开房列表（/rooms）。wayfinder #34 起卡片抽到 `features/rooms/RoomCard.vue`：
 * 官方区（/official）与这里必须长得一样、状态文案一样，差别只有"是不是运营位"。
 * 硬契约（票 #16）：空态文案用 api/publicRooms 导出的 PUBLIC_ROOMS_EMPTY_TEXT。
 */
import { onMounted } from 'vue'
import RoomCard from '../components/features/rooms/RoomCard.vue'
import { PUBLIC_ROOMS_EMPTY_TEXT } from '../api/publicRooms'
import { usePublicRoomsStore } from '../stores/publicRooms'

const store = usePublicRoomsStore()

onMounted(() => {
  void store.load()
})
</script>

<template>
  <div class="page">
    <main class="body">
      <h1 class="title">公开的房间</h1>
      <p v-if="store.loading" class="dim">加载中…</p>
      <p v-else-if="store.error" class="alert">{{ store.error }}</p>
      <p v-else-if="store.empty" class="dim">{{ PUBLIC_ROOMS_EMPTY_TEXT }}</p>
      <ul v-else class="cards">
        <RoomCard v-for="room in store.items" :key="room.roomId" :room="room" />
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
.cards { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 8px; }
.alert { color: var(--danger); }
.more { align-self: center; padding: 8px 20px; border: 1px solid var(--line); color: var(--dim); }
.more:hover { color: var(--text); border-color: var(--accent); }
</style>
