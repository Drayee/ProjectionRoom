<script setup lang="ts">
/**
 * 房间成员列表。
 *
 * 归 ui/：只读 store.members 渲染，不发请求、不含写动作。
 */
import BrandIcon from './BrandIcon.vue'
import { useRoomStore } from '../../stores/room'

const store = useRoomStore()
</script>

<template>
  <section class="members">
    <header>
      <h2>
        <BrandIcon name="users-group" decorative :size="15" />
        成员
      </h2>
      <span class="dim">{{ store.members.length }} 人</span>
    </header>

    <ul>
      <li v-for="member in store.members" :key="member.id">
        <span class="dot" :class="{ host: member.role === 'host' }" aria-hidden="true" />
        <span class="name">{{ member.displayName }}</span>
        <span v-if="member.role === 'host'" class="badge">主播</span>
        <span v-if="member.id === store.clientId" class="badge">我</span>
        <span class="depth dim mono">深度 {{ member.depth }}</span>
      </li>
      <li v-if="store.members.length === 0" class="dim">等待成员加入…</li>
    </ul>
  </section>
</template>

<style scoped>
.members {
  border: 1px solid var(--line);
  background: var(--surface);
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

/* 在线圆点：直角语言下仍保留圆形（表示"点"而非"块"） */
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--ok);
  flex: none;
}

.dot.host {
  background: var(--glitch-p);
}

.name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.badge {
  flex: none;
  padding: 1px 6px;
  border: 1px solid var(--line);
  font-size: 11px;
  color: var(--dim);
}

.depth {
  font-size: 11px;
  flex: none;
}
</style>
