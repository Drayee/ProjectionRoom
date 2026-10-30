<script setup lang="ts">
/**
 * 图标按钮（票 #62）。
 *
 * 与 `ui/Button.vue` 的差别只有两处，其余契约完全一致（五态矩阵见 Button 的文件头）：
 *
 *  1. **命中区**：视觉 36px，用一个 `::after` 把命中区撑到 44×44（WCAG 2.5.5）。
 *     撑出来的伪元素不吃事件（`pointer-events: none`），只是把点击热区扩大。
 *  2. **按压分级**：图标按钮按 `--press-icon`（0.94）缩放，不移动（票 #43 §4）。
 *
 * 归 ui/：纯表现件，不 import store / router；props 进、事件出（票 #44 的准入判据）。
 */
import { computed, ref, watch } from 'vue'
import BrandIcon from './BrandIcon.vue'

export type IconButtonVariant = 'ghost' | 'primary' | 'danger'

const props = withDefaults(
  defineProps<{
    /** 图标名（对应 public/icons/NAME.svg）。 */
    icon: string
    /** 无障碍名：图标按钮没有文字，必须显式给。 */
    label: string
    /** 视觉变体。 */
    variant?: IconButtonVariant
    /** 原生 type。 */
    type?: 'button' | 'submit' | 'reset'
    /** 加载中：内联 spinner + 锁宽；**不等于 disabled**。 */
    loading?: boolean
    /** 禁用：降饱和 + 不可聚焦 + 不可点。 */
    disabled?: boolean
    /** 图标尺寸（px）。 */
    iconSize?: number
  }>(),
  {
    variant: 'ghost',
    type: 'button',
    loading: false,
    disabled: false,
    iconSize: 17,
  },
)

const emit = defineEmits<{ (e: 'click', ev: MouseEvent): void }>()

const root = ref<HTMLButtonElement | null>(null)

/**
 * loading 期间锁住的宽度（px）：图标按钮常态就是定宽 36px，但仍然量一次，
 * 保证"锁宽"是**实测值**而不是硬编码（将来改了视觉尺寸也不会跳版）。
 */
const lockedWidth = ref<number | null>(null)

watch(
  () => props.loading,
  (loading) => {
    if (loading) {
      const w = root.value?.offsetWidth ?? 0
      lockedWidth.value = w > 0 ? Math.round(w * 100) / 100 : null
    } else {
      lockedWidth.value = null
    }
  },
)

/**
 * 点击闸门：disabled 交给原生属性（浏览器不派发 click）；
 * loading 这里单独挡 —— 它仍可聚焦、仍可被读屏读到，只是不许重复触发。
 */
function onClick(ev: MouseEvent): void {
  if (props.loading) {
    ev.preventDefault()
    ev.stopPropagation()
    return
  }
  emit('click', ev)
}

const lockedStyle = computed(() =>
  lockedWidth.value === null ? undefined : { width: lockedWidth.value + 'px' },
)
</script>

<template>
  <button
    ref="root"
    class="ui-icon-btn"
    :class="['ui-icon-btn--' + props.variant, { 'is-loading': props.loading }]"
    :type="props.type"
    :disabled="props.disabled"
    :aria-label="props.label"
    :aria-busy="props.loading || undefined"
    :style="lockedStyle"
    @click="onClick"
  >
    <BrandIcon v-if="!props.loading" :name="props.icon" decorative :size="props.iconSize" />
    <span v-else class="ui-icon-btn__spinner" aria-hidden="true">
      <BrandIcon name="load-spinner" decorative :size="props.iconSize" />
    </span>
  </button>
</template>

<style scoped>
/*
 * 状态色一律走 --ui-btn-* 变量：页面要改样式时覆盖变量即可，
 * 不必与本组件的选择器抢优先级。
 */
.ui-icon-btn {
  --ui-btn-bg: transparent;
  --ui-btn-bg-hover: var(--state-hover);
  --ui-btn-bg-active: var(--state-active);
  --ui-btn-fg: var(--dim);
  --ui-btn-fg-hover: var(--text);
  --ui-btn-fg-active: var(--text);
  --ui-btn-border-color: transparent;

  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  /* 视觉 36px；命中区靠 ::after 撑到 44×44（票 #43 §6）。 */
  width: 36px;
  height: 36px;
  border: 1px solid var(--ui-btn-border-color);
  border-radius: var(--radius-pill);
  background: var(--ui-btn-bg);
  color: var(--ui-btn-fg);
  cursor: pointer;
  transition:
    background-color var(--dur-micro) var(--ease),
    color var(--dur-micro) var(--ease),
    border-color var(--dur-micro) var(--ease),
    transform var(--dur-micro) var(--ease);
}

/* 命中区：视觉 36px → 命中 44×44（WCAG 2.5.5）。伪元素不吃事件。 */
.ui-icon-btn::after {
  content: '';
  position: absolute;
  inset: -4px;
  pointer-events: none;
}

/* ---- 变体 ---- */

.ui-icon-btn--primary {
  --ui-btn-bg: var(--accent);
  --ui-btn-bg-hover: var(--accent-hover);
  --ui-btn-bg-active: var(--accent);
  --ui-btn-fg: var(--accent-ink);
  --ui-btn-fg-hover: var(--accent-ink);
  --ui-btn-fg-active: var(--accent-ink);
  --ui-btn-border-color: var(--accent);
}

.ui-icon-btn--danger {
  --ui-btn-fg-hover: var(--danger);
}

/* ---- 三态反馈 ---- */

/* hover：底色提亮 + 前景变亮（两件都在，缺一即视为未实现）。 */
.ui-icon-btn:not(:disabled):hover {
  background-color: var(--ui-btn-bg-hover);
  color: var(--ui-btn-fg-hover);
}

/* active：按图标按钮分级缩放（--press-icon），不位移（票 #43 §4）。 */
.ui-icon-btn:not(:disabled):active {
  background-color: var(--ui-btn-bg-active);
  color: var(--ui-btn-fg-active);
  transform: scale(var(--press-icon));
}

/* 焦点环：全站统一，不许组件自造（票 #43 §6）。 */
.ui-icon-btn:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}

/* 禁用：降饱和 + 变淡；原生 disabled 让它不可聚焦、不可点。 */
.ui-icon-btn:disabled {
  cursor: not-allowed;
  filter: saturate(0.35);
  opacity: 0.55;
}

/* ---- loading ---- */

.ui-icon-btn__spinner {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  animation: ui-icon-btn-spin 900ms linear infinite;
}

@keyframes ui-icon-btn-spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

/*
 * 降级（票 #43 §8）：取消缩放，**只保留颜色变化**。
 *
 * 关键：底色与前景在降级下**仍然要变** —— 所以这里只把 transform 归零，
 * 绝不能把颜色改成"靠 keyframes 走到终态"（那样降级下会完全没有反馈）。
 */
@media (prefers-reduced-motion: reduce) {
  .ui-icon-btn:not(:disabled):active {
    transform: none;
  }
}
</style>
