<script setup lang="ts">
/**
 * 拓扑徽标：一行摘要 + 可展开详情。
 *
 * 归 ui/：5 个 computed 文本派生 + RouterLink 深链，不发请求、无写动作。
 */
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { useRoomStore } from '../../stores/room'

const store = useRoomStore()
const open = ref(false)

const roleText = computed(() => (store.isHost ? '主播' : '观众'))

/** 一行摘要：模式 + 深度 + 父/子数量。 */
const summary = computed(() => {
  const parts: string[] = [roleText.value]
  if (store.topologyMode) parts.push(store.topologyMode)
  parts.push(`深度 ${store.topologyDepth}`)
  if (!store.isHost) {
    parts.push(store.primaryId ? '已安置' : '未安置')
  }
  const children = store.childrenIds?.length ?? 0
  if (children > 0) parts.push(`下挂 ${children}`)
  return parts.join(' · ')
})

const modeText = computed(() => {
  switch (store.topologyMode) {
    case 'host':
      return '主播直发'
    case 'relay':
      return '经中继分发'
    case 'direct':
      return '直连父节点'
    default:
      return store.topologyMode || '未知'
  }
})

const parentsText = computed(() => {
  const ids = store.parentIds ?? []
  if (ids.length === 0) return '没有可用父节点'
  return ids.join('、')
})

const childrenText = computed(() => {
  const ids = store.childrenIds ?? []
  return ids.length === 0 ? '无' : ids.join('、')
})

const reasonText = computed(() => store.topologyReason || '（服务端未给出原因）')
</script>

<template>
  <section class="topo">
    <button
      type="button"
      class="summary"
      :aria-expanded="open"
      @click="open = !open"
    >
      <BrandIcon name="layers" decorative :size="14" />
      <span class="text">{{ summary }}</span>
      <span class="chev" :class="{ open }" aria-hidden="true">▾</span>
    </button>

    <dl v-if="open" class="detail">
      <dt>模式</dt>
      <dd>{{ modeText }}</dd>
      <dt>父节点</dt>
      <dd class="mono">{{ parentsText }}</dd>
      <dt>子节点</dt>
      <dd class="mono">{{ childrenText }}</dd>
      <dt>分配原因</dt>
      <dd>{{ reasonText }}</dd>
    </dl>

    <RouterLink class="more" :to="{ name: 'help', hash: '#playback' }">
      这是什么？
    </RouterLink>
  </section>
</template>

<style scoped>
.topo {
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 10px 12px;
  font-size: 12px;
}

.summary {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  color: var(--dim);
  text-align: left;
  transition: color var(--dur-micro) var(--ease);
}

.summary:hover {
  color: var(--text);
}

.text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chev {
  flex: none;
  transition: transform var(--dur-micro) var(--ease);
}

.chev.open {
  transform: rotate(180deg);
}

.detail {
  margin: 10px 0 0;
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 4px 10px;
  font-size: 12px;
}

.detail dt {
  color: var(--dim);
}

.detail dd {
  margin: 0;
  min-width: 0;
  overflow-wrap: anywhere;
}

.more {
  display: inline-block;
  margin-top: 8px;
  font-size: 11px;
}

@media (prefers-reduced-motion: reduce) {
  .chev {
    transition: none;
  }
}
</style>
