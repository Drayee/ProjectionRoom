<script setup lang="ts">
/**
 * 统一按钮（票 #62）。
 *
 * 为什么要有它：全仓曾有 111 处裸 `<button>`，各页各写一套 hover/active，
 * 于是「点了没反应」与「hover 颜色不统一」到处是。本组件是**唯一的按钮外观出口**：
 * 新代码一律用它，裸按钮按域逐批替换（票 #62 只做首批：帖子面 / 好友面 / 档案面 / 顶栏）。
 *
 * 五态矩阵（票 #44 定的契约，本组件逐条实现）：
 *
 *   | 状态 | 表现 |
 *   | :--- | :--- |
 *   | hover | 提亮（底色 → --state-hover / --accent-hover）+ 前景 --dim → --text |
 *   | active | 下沉 1px + 按尺寸缩放（普通按钮 --press-btn）+ 底色 → --state-active + 阴影收起 |
 *   | focus-visible | 全站统一外环（--ring / --ring-offset），只给键盘用户 |
 *   | disabled | 降饱和 + 不可聚焦（原生 disabled）+ 不可点 |
 *   | loading | 内联 spinner + **锁宽不跳版**；**不等于 disabled**（仍可聚焦，只挡重复触发） |
 *
 * 两条纪律：
 *
 *  1. **状态色只走令牌**（票 #43）：组件里不再出现 `rgba(180,108,255,.08)` 这类手写底色，
 *     也不许用 `color-mix`（换肤时按键值重算，见 tokens.css 的注释）。
 *  2. **降级只留颜色**（票 #43 §8）：`prefers-reduced-motion` 下取消位移与缩放，
 *     但底色与前景**仍然变化** —— 靠 keyframes 走到终态的实现一律不许（降级下会完全没反馈）。
 *
 * 归 ui/：纯表现件，不 import store / router；props 进、事件出（票 #44 的准入判据）。
 */
import { computed, ref, watch } from 'vue'
import BrandIcon from './BrandIcon.vue'

export type ButtonVariant = 'primary' | 'ghost' | 'danger'
export type ButtonSize = 'sm' | 'md' | 'lg'

const props = withDefaults(
  defineProps<{
    /** 视觉变体：primary=实心主行动，ghost=次级描边，danger=危险动作。 */
    variant?: ButtonVariant
    /** 尺寸：sm 行内小号、md 默认、lg 主行动。 */
    size?: ButtonSize
    /** 原生 type：表单里的提交按钮必须显式传 submit。 */
    type?: 'button' | 'submit' | 'reset'
    /** 加载中：内联 spinner + 锁宽；**不等于 disabled**。 */
    loading?: boolean
    /** 禁用：降饱和 + 不可聚焦 + 不可点。 */
    disabled?: boolean
  }>(),
  {
    variant: 'ghost',
    size: 'md',
    type: 'button',
    loading: false,
    disabled: false,
  },
)

const emit = defineEmits<{ (e: 'click', ev: MouseEvent): void }>()

const root = ref<HTMLButtonElement | null>(null)

/**
 * loading 期间锁住的宽度（px）。
 *
 * 为什么需要它：spinner 出现时若不锁宽，按钮会因为内容变化而**跳版**（一排按钮同时
 * 触发时整行都在动）。这里在 loading 变成 true 的那一刻量一次当前宽度（那一刻 DOM 还没
 * 更新，量到的正是「常态宽度」），loading 期间用这个固定宽度顶着。
 *
 * 用 `offsetWidth` 而不是 `getBoundingClientRect()`：前者是布局宽度，不受 hover/active
 * 的 transform 影响（正在缩放时量出来的宽度会偏小）。
 */
const lockedWidth = ref<number | null>(null)

watch(
  () => props.loading,
  (loading) => {
    if (loading) {
      const w = root.value?.offsetWidth ?? 0
      lockedWidth.value = w > 0 ? w : null
    } else {
      lockedWidth.value = null
    }
  },
)

/**
 * 点击闸门。
 *
 * disabled 由原生 `disabled` 属性拦下（浏览器根本不会派发 click）；
 * loading 这里单独挡 —— 它**不是 disabled**：按钮仍可聚焦、仍可被读屏读到，
 * 只是不许重复触发（票 #44 的明确要求：loading ≠ disabled）。
 */
function onClick(ev: MouseEvent): void {
  if (props.loading) {
    ev.preventDefault()
    ev.stopPropagation()
    return
  }
  emit('click', ev)
}

/** loading 期间把宽度钉死（不是 min-width：锁宽要挡的是「变宽」，不是「变窄」）。 */
const lockedStyle = computed(() =>
  lockedWidth.value === null ? undefined : { width: lockedWidth.value + 'px' },
)
</script>

<template>
  <button
    ref="root"
    class="ui-btn"
    :class="['ui-btn--' + props.variant, 'ui-btn--' + props.size, { 'is-loading': props.loading }]"
    :type="props.type"
    :disabled="props.disabled"
    :aria-busy="props.loading || undefined"
    :style="lockedStyle"
    @click="onClick"
  >
    <span class="ui-btn__label"><slot /></span>
    <span v-if="props.loading" class="ui-btn__spinner" aria-hidden="true">
      <BrandIcon name="load-spinner" decorative :size="14" />
    </span>
  </button>
</template>

<style scoped>
/*
 * 状态色一律走 `--ui-btn-*` 变量：
 *   页面要改某个按钮的样子时覆盖这些变量即可，不必与本组件的选择器抢优先级
 *   （例如标签页把下划线选中态接上去，见 PostsPage 的 .tabs）。
 */
.ui-btn {
  --ui-btn-bg: transparent;
  --ui-btn-bg-hover: var(--state-hover);
  --ui-btn-bg-active: var(--state-active);
  --ui-btn-fg: var(--dim);
  --ui-btn-fg-hover: var(--text);
  --ui-btn-fg-active: var(--text);
  --ui-btn-border-color: var(--line);
  --ui-btn-shadow: none;

  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  /* 命中区：行内文字按钮高度 ≥32px、左右 padding ≥8px（票 #43 §6）。 */
  min-height: 32px;
  padding: 6px 14px;
  border: 1px solid var(--ui-btn-border-color);
  border-radius: var(--radius);
  background: var(--ui-btn-bg);
  color: var(--ui-btn-fg);
  box-shadow: var(--ui-btn-shadow);
  font-size: 13px;
  line-height: 1.5;
  cursor: pointer;
  overflow: hidden;
  transition:
    background-color var(--dur-micro) var(--ease),
    color var(--dur-micro) var(--ease),
    border-color var(--dur-micro) var(--ease),
    box-shadow var(--dur-micro) var(--ease),
    transform var(--dur-micro) var(--ease);
}

/* ---- 变体 ---- */

/* 主行动：实心紫。hover 提亮一级（绝对色，不用 filter），active 回落并下沉。 */
.ui-btn--primary {
  --ui-btn-bg: var(--accent);
  --ui-btn-bg-hover: var(--accent-hover);
  --ui-btn-bg-active: var(--accent);
  --ui-btn-fg: var(--accent-ink);
  --ui-btn-fg-hover: var(--accent-ink);
  --ui-btn-fg-active: var(--accent-ink);
  --ui-btn-border-color: var(--accent);
  --ui-btn-shadow: var(--elev-1);
}

/* 危险动作：常态保持低调（与旧实现一致），hover 才显红 —— 但 hover 时底色与前景
   同时变，不靠描边单独承担（票 #43 §2：描边只有 1.45:1，不过 AA）。 */
.ui-btn--danger {
  --ui-btn-fg-hover: var(--danger);
}

/* ---- 尺寸 ---- */

.ui-btn--sm {
  min-height: 32px;
  padding: 5px 10px;
  font-size: 12px;
}

.ui-btn--md {
  min-height: 32px;
  padding: 6px 14px;
  font-size: 13px;
}

.ui-btn--lg {
  min-height: 38px;
  padding: 9px 18px;
  font-size: 14px;
}

/* ---- 三态反馈 ---- */

/* hover：底色提亮 + 前景变亮（两件都在，缺一即视为未实现）。 */
.ui-btn:not(:disabled):hover {
  background-color: var(--ui-btn-bg-hover);
  color: var(--ui-btn-fg-hover);
}

/* active：下沉 1px + 按尺寸缩放 + 阴影收起（票 #43 §2/§4）。 */
.ui-btn:not(:disabled):active {
  background-color: var(--ui-btn-bg-active);
  color: var(--ui-btn-fg-active);
  transform: translateY(1px) scale(var(--press-btn));
  box-shadow: none;
}

/* 焦点环：全站统一，不许组件自造（票 #43 §6）。 */
.ui-btn:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}

/* 禁用：降饱和 + 变淡；原生 disabled 让它不可聚焦、不可点。 */
.ui-btn:disabled {
  cursor: not-allowed;
  filter: saturate(0.35);
  opacity: 0.55;
}

/* ---- loading ---- */

/* 文字用 opacity 隐去而不是 visibility：visibility:hidden 会把文字从无障碍树里摘掉，
   读屏就听不到「保存」这个按钮名了。opacity 保留占位（宽度不变）也保留可访问名。 */
.ui-btn.is-loading .ui-btn__label {
  opacity: 0;
}

.ui-btn__label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
}

/* spinner 绝对定位居中：按钮的几何尺寸与常态完全一致（不跳版）。 */
.ui-btn__spinner {
  position: absolute;
  inset: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  animation: ui-btn-spin 900ms linear infinite;
}

@keyframes ui-btn-spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

/*
 * 降级（票 #43 §8）：取消位移与缩放，**只保留颜色变化**。
 *
 * 关键：底色与前景在降级下**仍然要变** —— 所以这里只把 transform 归零，
 * 绝不能把颜色改成「靠 keyframes 走到终态」（那样降级下会完全没有反馈）。
 * tokens.css 的全局判死已经把 transition 关掉，这里再把 transform 清掉。
 */
@media (prefers-reduced-motion: reduce) {
  .ui-btn:not(:disabled):active {
    transform: none;
  }
}
</style>
