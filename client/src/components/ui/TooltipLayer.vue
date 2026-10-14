<script setup lang="ts">
/**
 * 全局唯一的提示图层（`v-tip` 的落点）。
 *
 * 挂在 App.vue 里、位于路由之外：否则每次换页都会重建图层，
 * 正在显示的提示会被卸载掉（鼠标还停在按钮上时闪一下）。
 *
 * 图层本身 aria-hidden —— 提示不是唯一信息源，可访问名由各控件的 aria-label 提供。
 */
import { computed, onBeforeUnmount, onMounted } from 'vue'
import { hideTip, tipState } from '../../directives/tip'

const style = computed(() => ({
  left: `${tipState.value.x}px`,
  top: `${tipState.value.y}px`,
}))

// 滚动 / 改变窗口尺寸后位置就失真了，直接收起而不是追着算。
const onViewportChange = (): void => hideTip()

onMounted(() => {
  window.addEventListener('scroll', onViewportChange, true)
  window.addEventListener('resize', onViewportChange)
})

onBeforeUnmount(() => {
  window.removeEventListener('scroll', onViewportChange, true)
  window.removeEventListener('resize', onViewportChange)
})
</script>

<template>
  <div
    v-if="tipState.visible"
    class="tip-layer"
    :style="style"
    aria-hidden="true"
    data-testid="icon-tip"
  >
    {{ tipState.text }}
  </div>
</template>

<style scoped>
.tip-layer {
  position: fixed;
  z-index: 1000;
  max-width: min(280px, 70vw);
  padding: 5px 9px;
  border: 1px solid var(--line-strong);
  border-radius: 0; /* B 全直角 */
  background: var(--surface);
  color: var(--text);
  font-size: 12px;
  line-height: 1.5;
  pointer-events: none;
  transform: translate(-50%, 0);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.55);
  animation: tip-in var(--dur-micro) var(--ease);
}

@keyframes tip-in {
  from {
    opacity: 0;
    transform: translate(-50%, -4px);
  }
  to {
    opacity: 1;
    transform: translate(-50%, 0);
  }
}
</style>
