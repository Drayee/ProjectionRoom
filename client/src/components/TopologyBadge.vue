<script setup lang="ts">
import { computed } from 'vue'
import { useRoomStore } from '../stores/room'

const store = useRoomStore()

function memberName(id: string): string {
  if (!id) return '—'
  return store.members.find((m) => m.id === id)?.displayName ?? id.slice(0, 6)
}

const modeText = computed(() => {
  switch (store.topologyMode) {
    case 'fanout':
      return '扇出模式'
    case 'chain':
      return '单链分发'
    default:
      return '容量待实测'
  }
})

const parentText = computed(() => {
  if (store.isHost) return '我是源节点'
  const primary = store.primaryParentId
  if (!primary) return '等待分配'
  const backups = store.backupParentIds.length
  return `上级 ${memberName(primary)}${backups > 0 ? `（+${backups} 备用）` : ''}`
})

const roleText = computed(() => {
  const children = store.childrenIds.length
  return children === 0 ? '叶子' : `转发 ${children} 个下游`
})

const tooltip = computed(() => {
  const parts = [store.topologyReason || '等待服务端分配拓扑', `深度 ${store.topologyDepth}`]
  if (store.distributorId) parts.push(`分发节点 ${memberName(store.distributorId)}`)
  if (store.lastDistributorChange) parts.push(`最近换防 ${store.lastDistributorChange}`)
  return parts.join(' · ')
})
</script>

<template>
  <span class="badge topology" :title="tooltip">
    {{ modeText }} · 深度 {{ store.topologyDepth }} · {{ parentText }} · {{ roleText }}
    <template v-if="store.distributorId"> · 分发 {{ memberName(store.distributorId) }}</template>
  </span>
</template>

<style scoped>
.topology {
  border-color: var(--accent);
  color: var(--accent);
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
