/**
 * 路由过渡壳（票 #76）的**接线层**：把纯决策器（`utils/routeTransition.ts`）
 * 接到 vue-router 与 DOM 上。
 *
 * # 分层纪律
 *
 *   utils/routeTransition.ts            纯函数，**无 DOM、无 router**，可单测直接钉住
 *   composables/useRouteTransition.ts   本文件：模块级状态 + 守卫 + 供模板读的派生值
 *
 * 纯度边界很重要：方向判定是**产品规则**（票 #45 的决议），它必须能被
 * `node --test` 直接跑；一旦它摸到 `window.matchMedia` 或 router 实例，
 * 就退化成只能靠真机脚本验的东西。
 *
 * # 守卫为什么装在 router 启动之前
 *
 * vue-router 的**首次导航**由 `app.use(router)` 触发（`install()` 内部会
 * `push(currentLocation)`）。全局 `beforeEach` 是**在导航发生的那一刻**
 * 被收集的，所以守卫必须在那之前注册，否则首屏那次导航就没有决策。
 * 因此 `installRouteTransitionGuard(router)` 必须在 `app.use(router)` 之前调用
 * （见 `main.ts`）。
 *
 * # 为什么不用 `afterEach`
 *
 * `afterEach` 晚于导航确认：那时新页面已经渲染，而 Vue 的 `<Transition>`
 * 需要在**渲染新页面之前**就知道该用哪个过渡名（过渡名决定 CSS class，
 * 而 class 是在 enter/leave 的钩子里加的）。用 `afterEach` 会导致
 * 「新页已经以默认样式入场、然后才被改成另一种过渡」——闪一下。
 *
 * # 为什么决策放在**模块级**而不是组件里
 *
 * 守卫不在组件里、读者（App.vue）在组件里。两份状态各写各的必然漂移，
 * 症状是「模板读到的是上一次的决策」——位移方向随机。
 */
import { computed, shallowRef, type ComputedRef } from 'vue'
import type { Router } from 'vue-router'
import {
  planQueryOnly,
  planTransition,
  type TransitionDecision,
  type TransitionKind,
} from '../utils/routeTransition'

/**
 * 最近一次换页的决策。
 *
 * 初值取「不触发」：首屏没有"上一页"。
 */
const decision = shallowRef<TransitionDecision>(planQueryOnly())

/**
 * `prefers-reduced-motion` 的媒体查询：懒创建一次，跨导航复用同一个实例。
 *
 * 为什么每次导航都重新读 `.matches` 而不是订阅 change 事件：
 * 用户随时可能在系统里改这个偏好，而**下一次换页**就该按新偏好走。
 * 读当前值天然满足这一点，少一个订阅也就少一处忘记清理的地方。
 */
let reducedMotionQuery: MediaQueryList | null = null

/**
 * 用户是否要求减少动效（**JS 侧的判死**）。
 *
 * ⚠ 这一侧不可省：`tokens.css` 的全局 `transition: none !important` 只管 CSS，
 * 而共享元素走 WAAPI（`element.animate()`）——它**不自动尊重**这个偏好。
 * 缺 JS 侧就会出现「CSS 停了 JS 还在动」（票 #13 §4.2 已踩过）。
 */
export function prefersReducedMotion(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  if (!reducedMotionQuery) {
    reducedMotionQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
  }
  return reducedMotionQuery.matches
}

/**
 * 装上换页守卫（**必须在 `app.use(router)` 之前调用**）。
 *
 * 守卫只做一件事：把这次换页的**决策**写进模块级状态。
 * 它不拦截、不重定向、不改路由 —— 授权一律在服务端（铁律 I3），
 * 前端守卫只能决定"显示什么"。
 *
 * ⚠ 本仓库**没有其它全局守卫**（铁律 I3：前端路由守卫不作为授权）。
 * 因此不存在「后注册的守卫把这次导航 abort 掉、决策却已经写下」的错位。
 * 将来若引入 abort 型守卫，必须在这里改成"确认导航成功后才落决策"。
 */
export function installRouteTransitionGuard(router: Router): void {
  router.beforeEach((to, from) => {
    // 首屏：`from` 是 START_LOCATION（`matched` 为空）—— 没有"上一页"，不触发过渡。
    const hasPrevious = from.matched.length > 0
    /**
     * 同一条路由（只换 query 或 params）：**不是换页**。
     *
     * 票 #76 点名了"同路由仅 query 变化不触发"（`/messages?peer=`、`?offset=`）；
     * params 变化同理：`/posts/1` → `/posts/2` 是 vue-router **复用同一个组件实例**
     * （页面自己 watch 参数重新取数），页面根本没换，不该有换页动画。
     *
     * 判据是**路由记录**相同（`name` 相同），不是路径相同。
     */
    const sameRoute = hasPrevious && from.name === to.name
    decision.value = planTransition(hasPrevious ? from.meta : null, to.meta, {
      reducedMotion: prefersReducedMotion(),
      sameRoute,
    })
  })
}

/**
 * 过渡名 → CSS class 前缀。
 *
 * `page-none` **刻意不定义任何过渡样式**：none / exempt / instant 三种决策
 * 都落在这里，于是换页是**瞬时**的 ——
 * 放映室整页硬替换、reduced-motion 判死、首屏进入、同路由复用。
 *
 * ⚠ 这一点很关键：如果给 `page-none` 也写一套 240ms 的淡入，
 * 「放映室进出零动画」与「reduced-motion 瞬时」两条判据就都被打挂了。
 */
function transitionName(kind: TransitionKind): string {
  switch (kind) {
    case 'forward':
      return 'page-forward'
    case 'back':
      return 'page-back'
    case 'lateral':
      return 'page-lateral'
    default:
      return 'page-none'
  }
}

/** 供模板消费的派生值。 */
export interface RouteTransitionShell {
  /** CSS transition 名（`page-forward` / `page-back` / `page-lateral` / `page-none`）。 */
  readonly name: string
  /**
   * 位移的**大小**（px，不含符号）：方向由过渡名决定，符号写在 CSS 里。
   *
   * 写成 CSS 变量 `--route-shift` 注入，让 CSS 成为唯一读它的��方 ——
   * CSS 与 TypeScript 各写一个 12，改了一处忘了另一处，症状是
   * 「位移变成 24px」或「决策器说 12、界面动 0」，都很难一眼看出。
   */
  readonly shiftPx: number
  /** 当前决策（调试与验收脚本用）。 */
  readonly decision: TransitionDecision
}

/**
 * 取当前过渡壳的参数（**响应式**）。
 *
 * 返回 computed 而不是普通值：决策是在导航守卫里改模块级 ref 的，
 * 模板必须跟着重渲染才能拿到新过渡名。
 */
export function useRouteTransition(): ComputedRef<RouteTransitionShell> {
  return computed(() => {
    const d = decision.value
    return {
      name: transitionName(d.kind),
      shiftPx: d.shiftPx,
      decision: d,
    }
  })
}
