<script setup lang="ts">
/**
 * 房间室（/room/:roomId）。
 * 骨架阶段：最小可构建实现（进房装配走 useRoomEntry，硬契约不丢）；
 * 完整布局（票 #17 看片优先双栏）由 #19 填充。
 */
import AppHeader from '../components/ui/AppHeader.vue'
import MemberList from '../components/ui/MemberList.vue'
import TopologyBadge from '../components/ui/TopologyBadge.vue'
import { useRoomEntry } from '../components/features/room/useRoomEntry'

const props = defineProps<{ roomId: string }>()

const { store, missingCredential } = useRoomEntry(() => props.roomId)
</script>

<template>
  <div class="room">
    <AppHeader variant="page" />
    <main class="body">
      <p v-if="missingCredential" class="dim">没有进房凭据，请回首页用房间码进入。</p>
      <p v-else class="dim">
        {{ store.roomId.toUpperCase() }} · {{ store.connection }}
        <span v-if="store.hostOffline">{{ store.hostOfflineText }}</span>
      </p>
      <TopologyBadge />
      <MemberList />
    </main>
  </div>
</template>

<style scoped>
.room {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}

.body {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 20px;
}
</style>
