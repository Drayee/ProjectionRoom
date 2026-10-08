<script setup lang="ts">
/**
 * 品牌图标（唯一图标渲染点）。
 *
 * 为什么用 **CSS mask + background-color: currentColor** 而不是 `<img>`：
 *   `icons/` 下的 SVG 里描边写死成 `#ffffff`，当 `<img>` 用就永远只有一种颜色，
 *   没法跟着文字色/主题色变（禁用态、悬停态、危险色都要跟着变）。
 *   遮罩只取 SVG 的**不透明度通道**，颜色由 `background-color` 给，于是图标天然继承 `currentColor`。
 *
 * 可访问性：图标本身没有文字，所以默认同时给出 `aria-label` 与 `title`
 *（前者给读屏，后者给鼠标悬停）。当图标旁边已经有可读文字时传 `decorative`，
 * 此时整块 `aria-hidden` —— 否则读屏会把同一个意思念两遍。
 */
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    /** `client/public/icons/<name>.svg` 里的文件名（不含扩展名）。 */
    name: string
    /** 图标的可访问名（中文可读文案）。decorative 时可省略。 */
    label?: string
    /** 边长（px），默认 16。 */
    size?: number
    /** 纯装饰：旁边已有文字时用，整块 aria-hidden。 */
    decorative?: boolean
  }>(),
  { label: '', size: 16, decorative: false },
)

/**
 * 文件名白名单：`name` 会被拼进 `url(...)`，不允许路径分隔符、点号、引号或空白。
 * 不合法就退回一个一定存在的图标（而不是让浏览器去请求一个可能不存在的 URL）。
 */
const safeName = computed(() => (/^[A-Za-z0-9_-]+$/.test(props.name) ? props.name : 'close'))

const style = computed(() => {
  const url = `url(/icons/${safeName.value}.svg)`
  return {
    width: `${props.size}px`,
    height: `${props.size}px`,
    '-webkit-mask': `${url} center / contain no-repeat`,
    mask: `${url} center / contain no-repeat`,
  }
})

/** 可访问名：decorative 时不给，避免与旁边的文字重复播报。 */
const accessibleName = computed(() => (props.decorative ? '' : props.label))
</script>

<template>
  <span
    class="brand-icon"
    :class="{ 'is-decorative': decorative }"
    :style="style"
    :role="decorative ? undefined : 'img'"
    :aria-label="decorative ? undefined : accessibleName"
    :aria-hidden="decorative ? 'true' : undefined"
    :title="decorative ? undefined : accessibleName"
  />
</template>

<style scoped>
.brand-icon {
  display: inline-block;
  flex: none;
  /* 颜色来自文字色：放在按钮/链接里就自动跟随 hover/disabled/主题。 */
  background-color: currentColor;
  vertical-align: -0.125em;
}
</style>
