<script setup lang="ts">
// 拓扑徽标：一行话概括"我在树的哪里"，点开（Enter/Space 也行）看分配细节。
//
// 这些字段以前只存在于 window.__pr.snapshot().topology 里，排障时只能开控制台；
// 现在收进一个可键盘操作的 <details>：不点开只占一行，点开有备用父、深度、换道原因。
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
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

const childrenText = computed(() =>
  store.childrenIds.length === 0 ? '无（不再向下转发）' : store.childrenIds.map(memberName).join('、'),
)

const backupsText = computed(() =>
  store.backupParentIds.length === 0 ? '无' : store.backupParentIds.map(memberName).join('、'),
)

const tooltip = computed(() => {
  const parts = [store.topologyReason || '等待服务端分配拓扑', `深度 ${store.topologyDepth}`]
  if (store.distributorId) parts.push(`分发节点 ${memberName(store.distributorId)}`)
  if (store.lastDistributorChange) parts.push(`最近换防 ${store.lastDistributorChange}`)
  return parts.join(' · ')
})
</script>

<template>
  <details class="topo">
    <summary class="badge topology" :title="tooltip">
      {{ modeText }} · 深度 {{ store.topologyDepth }} · {{ parentText }} · {{ roleText }}
      <template v-if="store.distributorId"> · 分发 {{ memberName(store.distributorId) }}</template>
    </summary>

    <div class="topo-pop">
      <div class="row">
        <span class="k">拓扑</span>
        <span class="v">{{ modeText }} · 深度 {{ store.topologyDepth }}</span>
      </div>
      <div class="row">
        <span class="k">主父</span>
        <span class="v mono">{{ store.primaryParentId ? memberName(store.primaryParentId) : '—' }}</span>
      </div>
      <div class="row">
        <span class="k">备用父</span>
        <span class="v mono">{{ backupsText }}</span>
      </div>
      <div class="row">
        <span class="k">下游</span>
        <span class="v mono">{{ childrenText }}</span>
      </div>
      <div class="row">
        <span class="k">分发节点</span>
        <span class="v mono">{{ store.distributorId ? memberName(store.distributorId) : '无（未进入单链模式）' }}</span>
      </div>
      <div class="row">
        <span class="k">分配依据</span>
        <span class="v">{{ store.topologyReason || '服务端还没下发分配依据' }}</span>
      </div>
      <div class="row">
        <span class="k">最近换防</span>
        <span class="v mono">{{ store.lastDistributorChange || '无' }}</span>
      </div>
      <p class="muted tiny">
        「分配依据」= 服务端为什么把你挂在这个父节点下面。各指标的含义见
        <RouterLink :to="{ name: 'help', hash: '#diagnostics' }">
          <BrandIcon name="tips" decorative :size="12" />
          帮助
        </RouterLink>
        。
      </p>
    </div>
  </details>
</template>

<style scoped>
.topo {
  position: relative;
  min-width: 0;
  max-width: 100%;
}

summary.topology {
  cursor: pointer;
  list-style: none;
  display: inline-block;
  border: 1px solid var(--accent);
  color: var(--accent);
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
}

summary.topology::-webkit-details-marker {
  display: none;
}

.topo-pop {
  position: absolute;
  top: calc(100% + 6px);
  left: 0;
  z-index: 30;
  width: min(360px, 88vw);
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 12px;
  box-shadow: 0 10px 24px rgba(0, 0, 0, 0.45);
}

.topo-pop .row {
  display: grid;
  grid-template-columns: 72px minmax(0, 1fr);
  gap: 8px;
  font-size: 12px;
  line-height: 1.7;
}

.topo-pop .k {
  color: var(--text-dim);
}

.topo-pop .v {
  min-width: 0;
  word-break: break-word;
}

.tiny {
  font-size: 11px;
  line-height: 1.6;
  margin: 8px 0 0;
}

.tiny a {
  display: inline-flex;
  align-items: center;
  gap: 3px;
}

/* 窄屏（375 档）：徽标左边距 + 88vw 的弹层会顶出视口（实测右侧超出 19px）。
   这里改成以视口为界：左右各留 12px，宽度自适应 —— 不再依赖徽标的位置。 */
@media (max-width: 460px) {
  .topo-pop {
    position: fixed;
    left: 12px;
    right: 12px;
    top: auto;
    width: auto;
    max-height: 60vh;
    overflow: auto;
  }
}
</style>
