<script setup lang="ts">
import BrandIcon from './BrandIcon.vue'
import { useRoomStore } from '../stores/room'

const store = useRoomStore()
</script>

<template>
  <div class="members card">
    <header>
      <h2>
        <BrandIcon name="users-group" decorative :size="15" />
        成员
      </h2>
      <span class="muted">{{ store.members.length }} 人</span>
    </header>

    <ul>
      <li v-for="member in store.members" :key="member.id">
        <span class="dot" :class="{ host: member.role === 'host' }"></span>
        <span class="name">{{ member.displayName }}</span>
        <span class="badge" v-if="member.role === 'host'">主播</span>
        <span class="badge" v-if="member.id === store.clientId">我</span>
        <span class="depth muted mono">深度 {{ member.depth }}</span>
      </li>
      <li v-if="store.members.length === 0" class="muted">等待成员加入…</li>
    </ul>
  </div>
</template>

<style scoped>
.members {
  padding: 12px;
  overflow-y: auto;
}

header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 8px;
}

h2 {
  margin: 0;
  font-size: 14px;
  display: flex;
  align-items: center;
  gap: 6px;
}

ul {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

li {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--ok);
  flex: none;
}

.dot.host {
  background: var(--accent-2);
}

.name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.depth {
  font-size: 11px;
}
</style>
