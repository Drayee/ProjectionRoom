import { nextTick, onMounted } from 'vue'
import { useRoute, type Router } from 'vue-router'
import { announcePage } from '../utils/pageAnnouncer'
import { focusPageTitle } from '../utils/pageFocus'
import { isPageChange, resolveDocumentTitle, resolvePageName } from '../utils/routeMeta'

/**
 * 换页的**副作用**总入口（票 #79）：文档标题、焦点、播报。
 *
 * 为什么不全写在 `router.afterEach` 里：afterEach 跑在导航**提交**时，
 * 那时新页组件可能还没挂载 —— 页面是懒加载的，组件要等 chunk 到货才挂载。
 * 而"焦点落在新页标题上"要求的是**渲染完成之后**。
 *
 * 所以拆成两段：
 *   - **标题**：afterEach 里立刻写（标题不依赖 DOM 何时就绪）；
 *   - **焦点与播报**：等一次渲染周期过去，确认新页已挂载，再动焦点。
 *
 * 这两段之间的空档正是懒加载的等待期 —— 期间骨架顶着（RouteSkeleton）。
 *
 * @param router 路由实例（便于测试时注入）
 */
export function useRoutePageMeta(router: Router): void {
  const route = useRoute()

  /** 路由出口容器：焦点只在**新页**里找，不去旧页里捞。 */
  function outlet(): ParentNode {
    return document.querySelector('#route-outlet') ?? document
  }

  /** 文档标题：**单点写**（GLOSSARY「Route transition」）。 */
  function applyTitle(to: { meta: unknown; params: Record<string, string | string[]>; path: string }): void {
    document.title = resolveDocumentTitle(
      to.meta as never,
      { params: to.params, path: to.path },
    )
  }

  /**
   * 换页后的焦点与播报。
   *
   * 只在**真的换了页**时做（同路由仅 query 变化不是换页 —— 那会把读屏用户
   * 从正在读的会话里拽走）。判据在 utils/routeMeta.ts 的 isPageChange。
   */
  async function afterRender(to: { meta: unknown; params: Record<string, string | string[]>; path: string }): Promise<void> {
    // 等一次渲染周期：新页组件这时才挂载上来。
    await nextTick()
    // 再等一帧：懒加载页面在 chunk 到货后还要走一次 patch，rAF 保证它已完成。
    await new Promise<void>((resolve) => {
      if (typeof requestAnimationFrame === 'function') requestAnimationFrame(() => resolve())
      else setTimeout(resolve, 0)
    })
    const moved = await focusPageTitle(outlet())
    if (moved) {
      announcePage(resolvePageName(to.meta as never, { params: to.params, path: to.path }))
    }
  }

  onMounted(() => {
    // 首屏：标题要写（标签页标题不能是 Vite 默认那个），但**不播报、不移焦点** ——
    // 打开页面时读屏本来就会念标题，重复播报等于念两遍。
    applyTitle(route)
  })

  router.afterEach((to, from, failure) => {
    if (failure) return
    applyTitle(to)
    if (!isPageChange(to, from)) return
    void afterRender(to)
  })
}
