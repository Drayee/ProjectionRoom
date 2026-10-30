/**
 * 路由过渡决策器（票 #76「过渡壳」）。
 *
 * 一手来源：原型分支 `prototype/natural-route-transition` 的 `docs/prototypes/route-transition-logic.html`
 * 里的纯模块（原型票 #46 决议：「决策器纯模块可直接移植为 `utils/routeTransition.ts`」），
 * 参数按票 #45 的决议定稿。本文件是那个纯模块的**逐条移植**，不是重写。
 *
 * # 为什么要有一个纯函数决策器
 *
 * 过渡方向**不能**由浏览器历史推断：`pushState` 与 `back()` 在 URL 上不可区分
 * （票 #45 的自制判据①）。因此方向改由**路由层级 `meta.depth`** 决定：
 *
 *   depth 0 = 落地页      depth 1 = 列表/分区      depth 2 = 详情
 *
 * 比较两个 depth 就得到三种方向，再叠加两条豁免面（放映室整页、首屏/同路由不触发），
 * 就是一个**纯函数**：吃「从哪来 / 到哪去 / 几个开关」，吐一个决策对象。
 * 纯函数意味着它能被单测直接钉住（见 `test/script/verify-transition-logic.mjs`），
 * 而不必先起浏览器、先起服务。
 *
 * # 六种 kind 的语义
 *
 *   forward  进详情：新页从 translateX(12px) 归零，旧页向左 −12px 淡出
 *   back     回列表：方向反向
 *   lateral  同级切分区：纯交叉淡入，**无位移**（分区之间没有父子关系，
 *            硬造方向会误导 —— 票 #45 决议②）
 *   none     不触发（首屏进入 / 同路由 / 仅 query 变化）
 *   exempt   放映室整页硬替换（含进出房）
 *   instant  reduced-motion 下的瞬时切换（**双侧判死**，见下）
 *
 * # reduced-motion 的双侧纪律（票 #13 §4.2 / #45 决议）
 *
 * `tokens.css` 里的全局 `animation: none !important; transition: none !important`
 * 只管 CSS 侧。**JS 侧必须自己判**：共享元素走 WAAPI（`element.animate()`），
 * 不自动尊重该偏好；缺一侧就会出现「CSS 停了 JS 还在动」。
 * 因此本模块的入参里有 `reducedMotion`，由调用方用 `matchMedia` 读出来传进来。
 *
 * # 与 #78（共享元素 FLIP）的关系
 *
 * 本票只做**壳与决策器**。共享元素的匹配与 morph 属 #78，它会复用本模块的决策
 * （「只有前进方向触发共享元素」这条规则就住在这里的 `kind === 'forward'` 里）。
 * 因此这里**刻意**不预留 `shared` 字段：先不做的字段留在这里，
 * 只会诱使后来者以为它已经接好了。
 */

/** 位移量（px）。票 #45 决议：12px —— 是「方向提示」而不是「页面搬家」。 */
export const TRANSITION_SHIFT_PX = 12

/**
 * 过渡时长（ms）。票 #45 决议：新页与旧页同 240ms（=`--dur-comp`）。
 *
 * ⚠ 这个值与 `tokens.css` 的 `--dur-comp` 是**同一条约定**：
 * CSS 侧的过渡时长由那枚 token 决定，本常量是决策器对外声明的**同一口径**。
 * 改一处必须改另一处（CSS 里由本常量派生，见 `transition.css`）。
 */
export const TRANSITION_DURATION_MS = 240

/** 决策的六种形态。 */
export type TransitionKind = 'forward' | 'back' | 'lateral' | 'none' | 'exempt' | 'instant'

/** 一次换页的决策结果。 */
export interface TransitionDecision {
  /** 过渡形态。 */
  readonly kind: TransitionKind
  /**
   * 位移量（px）：前进/后退是 `±TRANSITION_SHIFT_PX` 的**绝对值**，
   * lateral 与其它形态是 0。方向由 `kind` 决定，位移量恒为正。
   */
  readonly shiftPx: number
  /** 过渡时长（ms）：none/exempt/instant 是 0（**不占用任何时间**）。 */
  readonly durationMs: number
  /** 人话理由（调试与验收脚本用；也是「为什么没动」的唯一解释来源）。 */
  readonly reason: string
}

/**
 * 决策器只需要路由 meta 里的两个字段。
 *
 * 用结构化子类型而不是 `RouteMeta`：这样单测可以喂最小形状，
 * 而不必构造一个完整的 vue-router meta 对象（那是实现细节，不是决策契约）。
 */
export interface TransitionRouteMeta {
  /** 路由层级：0 落地 / 1 列表 / 2 详情。 */
  readonly depth?: number
  /** 整页豁免（放映室 `/room/:roomId`：WebRTC 与播放器怕 DOM 抖动）。 */
  readonly exempt?: boolean
}

/** 决策器的入参。 */
export interface TransitionOptions {
  /** `prefers-reduced-motion: reduce` 是否命中（**调用方用 matchMedia 读**）。 */
  readonly reducedMotion?: boolean
  /** 是否是同一条路由（组件复用，只是参数/query 变了）。 */
  readonly sameRoute?: boolean
}

/**
 * 读路由的层级，缺省 1。
 *
 * 缺省 1 而不是 0：票 #45 的层级表里**绝大多数**路由是列表/分区层，
 * 缺省成 0 会让「忘了声明 depth 的新路由」被当成落地页，
 * 于是「从详情页进它」会判成后退（方向反了）。缺省 1 让它至少是个中性起点。
 */
export function routeDepth(meta: TransitionRouteMeta | undefined): number {
  const depth = meta?.depth
  return typeof depth === 'number' && Number.isFinite(depth) ? depth : 1
}

/**
 * 同路由仅 query 变化 → 不触发过渡。
 *
 * 单独一个入口而不是 `planTransition` 的一个分支：`/messages?peer=<id>`
 * 的会话切换**不是换页**，它与「同路由」是两条不同的不触发理由，
 * 而后者由调用方在组件复用时判出来（vue-router 会复用同一个组件实例）。
 */
export function planQueryOnly(): TransitionDecision {
  return {
    kind: 'none',
    shiftPx: 0,
    durationMs: 0,
    reason: '同路由仅 query 变化：同一屏的状态变化，不是换页',
  }
}

/**
 * 一次换页的过渡决策。
 *
 * @param from 离开的路由 meta；`null` 表示**首屏进入**（没有上一页）。
 * @param to   进入的路由 meta。
 * @param opts 开关（reduced-motion、同路由）。
 */
export function planTransition(
  from: TransitionRouteMeta | null | undefined,
  to: TransitionRouteMeta | null | undefined,
  opts: TransitionOptions = {},
): TransitionDecision {
  // 判死优先于一切：reduced-motion 下换页必须**瞬时**，
  // 连「哪一类换页」都不该有位移残留。
  if (opts.reducedMotion) {
    return {
      kind: 'instant',
      shiftPx: 0,
      durationMs: 0,
      reason: 'prefers-reduced-motion：CSS 与 WAAPI 双侧判死，换页瞬时到位',
    }
  }

  // 没有目标路由：交给调用方兜底（未知路由不该有位移，更不该有动画）。
  if (!to) {
    return { kind: 'none', shiftPx: 0, durationMs: 0, reason: '目标路由未知：不触发过渡' }
  }

  // 放映室整页豁免（**双向**：进房与出房都是硬替换）。
  // 优先级高于方向判定：房页有 WebRTC 与播放器，DOM 抖动会打断媒体管线，
  // 而「播放中才禁」要额外一条页面→外壳的状态通道（票 #45 决议③）。
  if (to.exempt) {
    return {
      kind: 'exempt',
      shiftPx: 0,
      durationMs: 0,
      reason: '进放映室：WebRTC/播放器怕 DOM 抖动，整页硬替换',
    }
  }
  if (from?.exempt) {
    return {
      kind: 'exempt',
      shiftPx: 0,
      durationMs: 0,
      reason: '离开放映室：整页豁免（连后退也豁免）',
    }
  }

  // 首屏进入：没有「上一页」，过渡无意义。
  if (!from) {
    return { kind: 'none', shiftPx: 0, durationMs: 0, reason: '首屏进入：没有上一页' }
  }

  // 同路由（组件被复用）：不是换页。
  if (opts.sameRoute) {
    return { kind: 'none', shiftPx: 0, durationMs: 0, reason: '同路由：不是换页' }
  }

  const fromDepth = routeDepth(from)
  const toDepth = routeDepth(to)

  // 方向只看层级差，不看历史（票 #45 决议①）。
  if (toDepth > fromDepth) {
    return {
      kind: 'forward',
      shiftPx: TRANSITION_SHIFT_PX,
      durationMs: TRANSITION_DURATION_MS,
      reason: '进详情：新页右移 12px 归零，旧页左移淡出',
    }
  }
  if (toDepth < fromDepth) {
    return {
      kind: 'back',
      shiftPx: TRANSITION_SHIFT_PX,
      durationMs: TRANSITION_DURATION_MS,
      reason: '回列表：方向反向',
    }
  }
  return {
    kind: 'lateral',
    shiftPx: 0,
    durationMs: TRANSITION_DURATION_MS,
    reason: '同级切分区：纯交叉淡入，无位移',
  }
}

/**
 * 共享元素是否该触发（票 #78 消费）。
 *
 * #78 的规则原文：「**只有前进方向触发**共享元素。后退一律用方向性位移 ——
 * 列表页回来时正在重新拉数据，目标元素经常还不存在，硬做只会降级成闪一下」。
 *
 * 这条规则住在**决策器**里而不是 #78 里，是因为它是"方向"的一部分：
 * 同一个 `planTransition` 的结果既决定页面怎么动，也决定共享元素要不要动。
 * 两处各写一遍必然漂移（改了一处忘了另一处 → 后退时也做 FLIP，
 * 症状是"列表回来时封面闪一下"）。
 *
 * #78 自己负责的是**匹配**（按 `kind:key` 在离开页找任意同值元素）与
 * **降级**（缺失/未解码/零尺寸 → 当场退化为 240ms 位移，时长不变）。
 * 那些都要摸 DOM，因此不放在这个纯模块里。
 */
export function shouldMorphSharedElement(decision: TransitionDecision): boolean {
  return decision.kind === 'forward'
}
