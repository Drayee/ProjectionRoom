<script setup lang="ts">
/**
 * AsyncState —— 「空 / 载 / 错」三态的唯一出口（票 #63，契约见决议 #44）。
 *
 * ## 为什么必须有它
 *
 * 全站每个列表页都自己写这三态，于是同一件事有五种长相：
 *
 * ```
 * <p class="dim">加载中…</p>
 * <p class="dim">还没有帖子。</p>
 * <p class="alert">加载失败</p>
 * <p class="dim">暂无数据</p>          ← 最糟的一种：用户不知道是没数据还是坏了
 * <p class="dim">暂无数据</p>（另一页，字号还不一样）
 * ```
 *
 * 决议 #44 明确禁止裸「暂无数据」：空态必须是一句**人话**（说清"这里本来会有什么"），
 * 错误态必须**能重试**，加载态必须**有形状**（骨架，而不是一行"加载中…"把布局顶飞）。
 *
 * ## 四态（`data-state` 的取值与真机脚本 R5 对齐）
 *
 * | state | 渲染 | 什么时候用 |
 * | :--- | :--- | :--- |
 * | `loading` | 骨架屏（页面可用 `#skeleton` 覆盖形状） | 首次加载 / 换筛选条件 |
 * | `empty` | 图标 + 一句人话 + 可选行动 | 请求成功但零条 |
 * | `error` | 人话 + 重试按钮 | 请求失败（**不是**空） |
 * | `capability` | 该域后端未上线 | `CapabilityUnavailable` |
 *
 * `ready` 是**调用方的状态**，不是本组件的渲染态：数据到手时调用方直接渲染内容，
 * 不挂本组件（多渲染一层空壳只会带来无谓的 DOM）。所以这里没有 `ready` 分支。
 *
 * ## 原始错误只进 console
 *
 * `error` 是给**人**看的（"网络似乎断了，请稍后重试"），不是给日志看的。
 * 把 `TypeError: Failed to fetch` 印在页面上既吓人又无用 —— 详细错误由调用方
 * `console.error` 出去（本组件不 console，它不知道上下文）。
 *
 * ## 归 ui/
 *
 * 纯展示：不读 store、不发请求。`@retry` 只是把点击原样抛给调用方。
 */
import BrandIcon from './BrandIcon.vue'

/** 本组件渲染的三态 + 能力降级（`ready` 由调用方直接渲染内容，不经过本组件）。 */
export type AsyncStateKind = 'loading' | 'empty' | 'error' | 'capability'

const props = withDefaults(
  defineProps<{
    /** 当前状态。 */
    state: AsyncStateKind
    /** 空态/错误态的标题（一句人话，不是「暂无数据」）。 */
    title?: string
    /** 补充说明：空态说"这里本来会有什么"，错误态说"接下来能做什么"。 */
    hint?: string
    /** 图标名（走 BrandIcon 的白名单）。 */
    icon?: string
    /** 是否显示重试按钮（错误态默认显示，其余默认不显示）。 */
    retryable?: boolean
  }>(),
  {
    title: '',
    hint: '',
    icon: 'info',
    retryable: undefined,
  },
)

defineEmits<{ retry: [] }>()

/** 各态默认的图标与标题 —— 调用方不传时也要有像样的兜底。 */
const DEFAULTS: Record<AsyncStateKind, { icon: string; title: string }> = {
  loading: { icon: 'load-spinner', title: '加载中' },
  empty: { icon: 'info', title: '这里还没有内容' },
  error: { icon: 'alert-triangle', title: '没能加载出来' },
  capability: { icon: 'alert-triangle', title: '该分区后端尚未上线' },
}

const shownTitle = () => props.title || DEFAULTS[props.state].title
const shownIcon = () => props.icon === 'info' ? DEFAULTS[props.state].icon : props.icon
/** 错误态默认给重试（"失败了却不能重试"是本组件要消灭的形态之一）。 */
const canRetry = () => (props.retryable === undefined ? props.state === 'error' : props.retryable)
</script>

<template>
  <div class="async-state" :class="'async-state--' + props.state" data-testid="async-state" :data-state="props.state" role="status">
    <!-- 加载态：骨架屏。页面用 `#skeleton` slot 覆盖形状（卡片骨架 vs 行骨架）。 -->
    <template v-if="props.state === 'loading'">
      <div class="skeleton" aria-hidden="true">
        <slot name="skeleton">
          <span class="bar" />
          <span class="bar short" />
        </slot>
      </div>
      <p class="sr-only">{{ shownTitle() }}</p>
    </template>

    <template v-else>
      <h2 class="head">
        <BrandIcon :name="shownIcon()" decorative :size="16" />
        {{ shownTitle() }}
      </h2>
      <p v-if="props.hint" class="hint dim">{{ props.hint }}</p>
      <div class="foot">
        <!-- 空态的行动（去发帖 / 去别处看看）：调用方塞按钮进来。 -->
        <slot />
        <button v-if="canRetry()" type="button" class="retry" @click="$emit('retry')">重试</button>
      </div>
    </template>
  </div>
</template>

<style scoped>
/*
 * 三态共用外壳：与页面内容同宽、左对齐。
 *
 * 为什么不居中：本仓的列表都是左对齐的密排（masonry / 行表），
 * 中间突然出现一块居中空态会让"这块是内容还是提示"更难分辨。
 */
.async-state {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 20px 0;
  min-width: 0;
}

.head {
  margin: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 15px;
  /* 中性字色：状态不靠颜色单独表达（tokens.css 硬约束 2）。 */
  color: var(--text);
}

.hint {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
}

.foot {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.retry {
  padding: 6px 14px;
  border: 1px solid var(--accent);
  background: transparent;
  color: var(--accent);
  font-size: 13px;
  cursor: pointer;
  transition:
    background-color var(--dur-micro) var(--ease),
    color var(--dur-micro) var(--ease);
}

.retry:hover {
  background: var(--state-hover);
  color: var(--text);
}

.retry:active {
  background: var(--state-active);
}

.retry:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}

/* ---- 骨架 ---- */

.skeleton {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.bar {
  display: block;
  height: 14px;
  background: var(--state-hover);
  /* 呼吸而不是滑动：滑动骨架在 reduced-motion 下必须停，而"停住的滑动"看起来像卡死。
     呼吸（透明度）是降级后仍能表达"在等"的最小动效。 */
  animation: async-breathe 1200ms ease-in-out infinite;
}

.bar.short {
  width: 60%;
}

@keyframes async-breathe {
  0%,
  100% {
    opacity: 0.45;
  }
  50% {
    opacity: 1;
  }
}

/* 降级：停掉呼吸，但**保留骨架的形状**（形状本身就在说"内容马上来"）。 */
@media (prefers-reduced-motion: reduce) {
  .bar {
    animation: none;
    opacity: 0.7;
  }
}

/* 读屏专用：加载态没有可见标题，但状态必须播报一次。 */
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  padding: 0;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
</style>
