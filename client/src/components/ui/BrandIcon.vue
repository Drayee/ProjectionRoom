<script setup lang="ts">
/**
 * 图标渲染的唯一出口（CSS mask + currentColor）。
 *
 * 为什么用 mask 而不是内联 SVG：图标颜色跟随 currentColor，hover / 状态变色零成本；
 * 且 250 个图标不必逐个 import。mask 读的是 **alpha 通道**（票 #9 实测），
 * 所以源文件里 stroke="#ffffff" 的硬编码颜色不影响上色。
 *
 * 安全：name 走白名单正则，避免被拼成任意 URL（`url(/icons/${name}.svg)`）。
 */
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    name: string
    size?: number
    /** 纯装饰（读屏跳过）。与 label 互斥：给了 label 就是有意义的图标。 */
    decorative?: boolean
    /** 可访问名。不给且非 decorative 时退回 name。 */
    label?: string
  }>(),
  { size: 18, decorative: false, label: undefined },
)

const safeName = computed(() => {
  const n = String(props.name || '')
  return /^[A-Za-z0-9_-]+$/.test(n) ? n : ''
})

const style = computed(() => {
  if (!safeName.value) return { display: 'none' }
  const px = `${props.size}px`
  const url = `url(/icons/${safeName.value}.svg)`
  return {
    width: px,
    height: px,
    '-webkit-mask-image': url,
    'mask-image': url,
  }
})

const a11y = computed(() =>
  props.decorative
    ? { 'aria-hidden': 'true' as const }
    : { role: 'img' as const, 'aria-label': props.label || props.name },
)
</script>

<template>
  <span class="brand-icon" :style="style" v-bind="a11y" />
</template>

<style scoped>
.brand-icon {
  display: inline-block;
  flex: none;
  background: currentColor;
  -webkit-mask-repeat: no-repeat;
  mask-repeat: no-repeat;
  -webkit-mask-position: center;
  mask-position: center;
  -webkit-mask-size: contain;
  mask-size: contain;
  vertical-align: -0.18em;
}
</style>
