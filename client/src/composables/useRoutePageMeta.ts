import { nextTick, onMounted, watch } from 'vue'
import { useRoute, type Router } from 'vue-router'
import { announcePage } from '../utils/pageAnnouncer'
import { focusPageTitle } from '../utils/pageFocus'
import { isPageChange, resolveDocumentTitle, resolvePageName } from '../utils/routeMeta'

/**
 * 换页的**副作用**总入口（票 #79）：文档标题、焦点、播报。
 *
 * 为什么不写在 `router.afterEach` 里：afterEach 跑在导航**提交**时，
 * 那时新页组件还没挂载 —— 此刻去找主标题必然找不到（页面是懒加载的，
 * 组件要等 chunk 到货才挂载）。而"焦点落在新页标题上"要求的是**渲染完成之后**。
 *
 * 所以拆成两段：
 *   - `afterEach`：写 `document.title`（标题不依赖 DOM 何时就绪）；
 *   - `watch(route)` + `nextTick`：等新页挂载后再移焦点与播报。
 *
 * 这两段之间的空档正是懒加载的等待期 —— 期间骨架顶着（RouteSkeleton）。
 *
 * @param router 路由实例（便于测试时注入）
 */
export function useRoutePageMeta(router: Router): void {
  const route = useRoute()

  /** 页面名（不含品牌后缀）：播报与焦点提示都用它。 */
  function pageName(): string {
    return resolvePageName(route.meta, { params: route.params, path: route.path })
  }

  /** 文档标题：**单点写**（GLOSSARY「Route transition」）。 */
  function applyTitle(): void {
    document.title = resolveDocumentTitle(route.meta, { params: route.params, path: route.path })
  }

  /**
   * 换页后的焦点与播报。
   *
   * 只在**真的换了页**时做（同路由仅 query 变化不是换页 —— 那会把读屏用户
   * 从正在读的会话里拽走）。判据在 utils/routeMeta.ts 的 isPageChange。
   */
  async function afterRender(): Promise<void> {
    await nextTick()
    // 等一帧：懒加载页面此刻可能还没挂载（chunk 还在路上）。
    await new Promise<void>((resolve) => {
      if (typeof requestAnimationFrame === 'function') requestAnimationFrame(() => resolve())
      else setTimeout(resolve, 0)
    })
    const moved = await focusPageTitle(document)
    if (moved) {
      announcePage(pageName())
    }
  }

  onMounted(() => {
    // 首屏：标题要写（标签页标题不能是 Vite 默认那个），但**不播报、不移焦点** ——
    // 打开页面时读屏本来就会念标题，重复播报等于念两遍。
    applyTitle()
  })

  watch(
    () => route.fullPath,
    () => {
      applyTitle()
    },
  )
}
