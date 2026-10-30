import { ref } from 'vue'
import type { Router } from 'vue-router'

/**
 * 路由切换中的"在途"计数（票 #79）。
 *
 * 为什么需要它：页面组件是懒加载的，点下去之后有一段"chunk 还在路上"的窗口。
 * 这段时间里如果什么都不显示，用户看到的是**点了没反应**（本项目反复强调要消灭的症状）。
 * 所以这段时间由一层**骨架**顶着（`components/ui/RouteSkeleton.vue`）。
 *
 * 用计数而不是布尔：连续两次导航（快速点两个入口）时，布尔会在第一次结束时
 * 提前把骨架撤掉，而第二个页面的 chunk 其实还没到。
 */
export const routePending = ref(0)

/**
 * 监听路由导航，把"在途"计数接上。
 *
 * 首屏（`from` 没有匹配记录）**不计入**：那是首屏加载，不是"换页等待"，
 * 而且首屏有 index.html 的静态壳顶着，不需要额外的骨架层。
 */
export function installRouteLoadWatch(router: Router): void {
  router.beforeEach((_to, from) => {
    if (from.matched.length === 0) return
    routePending.value += 1
  })
  const done = () => {
    routePending.value = Math.max(0, routePending.value - 1)
  }
  router.afterEach(() => done())
  router.onError(() => done())
}
