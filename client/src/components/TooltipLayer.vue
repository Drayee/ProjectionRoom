<script setup lang="ts">
/**
 * 提示图层（全局唯一一份，挂在 `App.vue`）。
 *
 * 它只读 `tipState`（由 `v-tip` 指令写），自己不监听任何元素：
 * 这样"谁触发"与"在哪画"彻底分开，任何新加的图标按钮只要写 `v-tip` 就自动生效。
 *
 * 可访问性：`aria-hidden="true"` —— 提示是**视觉辅助**，不是信息源。
 * 每个触发控件的可访问名都在它自己的 `aria-label` 上（见 directives/tip.ts 的纪律一）。
 */
import { computed, onBeforeUnmount, onMounted } from 'vue'
import { hideTip, tipState } from '../directives/tip'

const style = computed(() => ({ left: `${tipState.value.x}px`, top: `${tipState.value.y}px` }))

/**
 * 滚动/改尺寸之后，缓存的视口坐标立刻失真（提示会飘在错误的位置），
 * 因此直接收起 —— 下一次 `mousemove` 会把它重新摆正。
 * 捕获阶段监听：内层可滚动容器（诊断抽屉、房间成员列表）滚动也要收。
 */
function onViewportChange(): void {
  hideTip()
}

onMounted(() => {
  window.addEventListener('scroll', onViewportChange, { passive: true, capture: true })
  window.addEventListener('resize', onViewportChange, { passive: true })
})

onBeforeUnmount(() => {
  window.removeEventListener('scroll', onViewportChange, { capture: true })
  window.removeEventListener('resize', onViewportChange)
})
</script>

<template>
  <div
    v-if="tipState.visible && tipState.text !== ''"
    class="tip-layer"
    data-testid="icon-tip"
    role="presentation"
    aria-hidden="true"
    :style="style"
  >
    {{ tipState.text }}
  </div>
</template>

<style scoped>
.tip-layer {
  position: fixed;
  z-index: 9999;
  max-width: 260px;
  padding: 5px 9px;
  border-radius: 8px;
  background: rgba(12, 12, 16, 0.94);
  border: 1px solid var(--border);
  color: var(--text);
  font-size: 12px;
  line-height: 1.5;
  /* 提示绝不吞事件：它常常覆盖在被提示的按钮上。 */
  pointer-events: none;
  /* 跟随指针时不需要过渡（会显得黏），但淡入可以有一点点。 */
  animation: tip-in 90ms ease-out;
  white-space: normal;
  overflow-wrap: anywhere;
}

@keyframes tip-in {
  from {
    opacity: 0;
    transform: translateY(2px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

@media (prefers-reduced-motion: reduce) {
  .tip-layer {
    animation: none;
  }
}
</style>
