import { nextTick } from 'vue'

/**
 * 主标题的选取规则（票 #79）。
 *
 * 换页后焦点要落在**新页的主标题**上。找它的规则按优先级：
 *   1. `[data-page-title]`：显式声明（页面里标题不是 h1、或者有多个 h1 时用它）；
 *   2. `h1`：绝大多数页面的主标题就是 h1；
 *   3. `[role="heading"][aria-level="1"]`：语义等价但不是 h1 的写法。
 *
 * 找不到就**什么都不做**：焦点留在原处也比把焦点丢到 body 上好
 * （丢到 body 会让读屏从头开始念整页，用户失去位置感）。
 */

/** 页面主标题的选取器（按优先级排列）。 */
export const PAGE_TITLE_SELECTORS = [
  '[data-page-title]',
  'h1',
  '[role="heading"][aria-level="1"]',
] as const

/**
 * 在容器里找主标题元素。
 *
 * 取**第一个**匹配项：页面若真有多个 h1，那本身就是可访问性问题，
 * 但那是别处该修的，不该在这里猜"哪个才是主标题"。
 */
export function findPageTitle(container: ParentNode): HTMLElement | null {
  for (const selector of PAGE_TITLE_SELECTORS) {
    const el = container.querySelector<HTMLElement>(selector)
    if (el) return el
  }
  return null
}

/**
 * 把焦点移进新页主标题（不滚动页面）。
 *
 * 三个要点：
 *
 *   1. **`tabindex="-1"`**：让标题**可编程聚焦**但不进入 Tab 序列。
 *      没有它，`focus()` 对 h1 无效（h1 默认不可聚焦），焦点就永远落不到标题上。
 *      这是"焦点进主标题"能成立的**前提**，所以这里在运行时补上，
 *      而不是要求每个页面都记得写 —— 漏一个页面的症状是"那个页面焦点没动"，
 *      而这种漏写几乎必然发生。
 *
 *   2. **不滚动**：`focus({ preventScroll: true })`。
 *      不加它，浏览器会把标题滚进视口 —— 而此时页面刚换完、用户还没开始看，
 *      滚动会把"刚落地就被拽走"变成常态（尤其标题在页面顶部时）。
 *
 *   3. **只移一次**：同一页里焦点可能被页面自己抢走（例如输入框自动聚焦）。
 *      本函数只在换页时被调用一次，不做轮询式"纠正"。
 */
export async function focusPageTitle(container: ParentNode): Promise<boolean> {
  const title = findPageTitle(container)
  if (!title) return false
  if (!title.hasAttribute('tabindex')) {
    title.setAttribute('tabindex', '-1')
  }
  await nextTick()
  try {
    title.focus({ preventScroll: true })
  } catch {
    // 老浏览器不认识 focus(options)：退化为不带选项的 focus（会滚动，但不至于失败）。
    title.focus()
  }
  return true
}
