<script setup lang="ts">
import { computed } from 'vue'
import { useRoomStore } from '../stores/room'

const store = useRoomStore()

// M1 的 mode 恒为 pending：容量要等实测上行（getStats）才有意义，M3 接入（SPEC §6.1、§6.2）。
const modeText = computed(() => {
  switch (store.capacity?.mode) {
    case 'fanout':
      return '扇出模式'
    case 'chain':
      return '单链分发模式'
    default:
      return '容量待实测'
  }
})

const parentText = computed(() => {
  if (store.isHost) return '我是分发根节点'
  return store.parentName ? `上级：${store.parentName}` : '上级：主播'
})
</script>

<template>
  <span class="badge topology" :title="`${modeText} · ${parentText}`">
    {{ modeText }} · 深度 {{ store.depth }}
  </span>
</template>

<style scoped>
.topology {
  border-color: var(--accent);
  color: var(--accent);
}
</style>
