import type { RouteMeta } from 'vue-router'

/**
 * 路由元信息的**纯**判据（票 #79）。
 *
 * 这个文件只做三件事，且都不碰 DOM、不碰 Vue：
 *   1. 拼出文档标题（「页面名 · 月喵」）；
 *   2. 从 `meta.title` 里取出页面名（静态串或按路由求值的函数）;
 *   3. 判断「这次导航算不算换页」（决定要不要播报与移焦点）。
 *
 * 为什么单独成文件：**单点写 document.title** 是纪律，不是风格。
 * 页面里散落着各自的 `document.title = ...` 时，多标签页下会看到
 * 「月喵 · 一起看」「ABCD · 月喵」「Posts · 月喵」三种口径，而用户无法知道
 * 哪一条才是当前页。判定逻辑一旦混进路由表或组件里，就再也无法被一条测试钉住；
 * 放进这个纯模块后，`test/script/verify-route-title.mjs` 能直接 import 它做表驱动断言
 * （Node 24 的类型擦除，前端不需要测试框架）。
 */

/** 品牌名：文档标题的后缀（`<页面名> · 月喵`）。 */
export const BRAND_NAME = '月喵'

/**
 * 页面名的求值上下文。
 *
 * 只暴露 `params` 与 `path`：这是路由 meta 里能拿到、且页面名真的用得上的两样东西。
 * 刻意**不**收整个路由对象 —— 那样每个页面名函数都能摸到 query/redirect，
 * 于是「页面名」会慢慢长成「页面名 + 一堆副作用」，而单点纪律也就守不住了。
 */
export interface RouteTitleContext {
  readonly params: Record<string, string | string[]>
  readonly path: string
}

/**
 * `meta.title` 的两种形态：
 *   - 静态串：绝大多数页面（「帖子」「好友」…）；
 *   - 求值函数：少数页面的名字取决于路由参数（例如放映室的标题是**房间码**）。
 *
 * 函数形态存在的理由：房间码在 `params` 里，不在静态表里。
 * 如果不给这个口子，房间页就得自己写 `document.title`（页内自写标题 = 破单点纪律）。
 */
export type RouteTitle = string | ((route: RouteTitleContext) => string)

/**
 * 给 vue-router 的 `RouteMeta` 补上 `title` 字段的类型。
 *
 * 为什么需要：vue-router 的 `RouteMeta` 默认是 `Record<PropertyKey, unknown>`，
 * 于是路由表里写 `title: (route) => ...` 时 `route` 是 `any`（vue-tsc 直接报错）。
 * 补上这一条，路由表里的页面名函数就有了类型，参数名写错也会被编译器抓住。
 */
declare module 'vue-router' {
  interface RouteMeta {
    title?: RouteTitle
  }
}

/**
 * 拼出文档标题。
 *
 * 页面名为空时**只留品牌名**（而不是「 · 月喵」这种半截串）：
 * 路由表漏写 `meta.title` 是可修的小疏漏，但标题显示成「 · 月喵」会让用户以为
 * 页面崩了。空标题退回品牌名，至少标签页里还认得出这是月喵。
 */
export function formatDocumentTitle(pageName: string): string {
  const name = (pageName ?? '').trim()
  return name === '' ? BRAND_NAME : `${name} · ${BRAND_NAME}`
}

/**
 * 从路由元信息里取出页面名。
 *
 * 缺 `meta.title`、类型不对、函数抛错 —— 一律退回空串（调用方据此得到纯品牌名）。
 * 这里**不抛**：标题是装饰性信息，它坏了不该让整次导航失败
 * （导航失败 = 用户点的页面没打开，那比标题难看严重得多）。
 */
export function resolvePageName(meta: RouteMeta | undefined, route: RouteTitleContext): string {
  const raw = (meta as { title?: unknown } | undefined)?.title
  if (typeof raw === 'string') return raw.trim()
  if (typeof raw === 'function') {
    try {
      const value = (raw as (r: RouteTitleContext) => unknown)(route)
      return typeof value === 'string' ? value.trim() : ''
    } catch {
      return ''
    }
  }
  return ''
}

/** 一步到位：路由 → 文档标题。 */
export function resolveDocumentTitle(meta: RouteMeta | undefined, route: RouteTitleContext): string {
  return formatDocumentTitle(resolvePageName(meta, route))
}

/**
 * 这次导航**算不算换页**（决定播报与移焦点）。
 *
 * 三条判据：
 *   1. 首屏（`from` 没有匹配记录）**不算**换页：没有「上一页」，用户也不需要
 *      「你到了哪里」的提示 —— 打开页面时读屏本来就会念标题；
 *   2. 路由名不同 → 算换页（去了另一个分区）；
 *   3. 路由名相同但**参数**不同 → 也算换页（/posts/1 → /posts/2 是另一篇帖子）。
 *
 * 路由名与参数都相同、只有 query 变了 → **不算**换页：
 * 那是「同一屏的状态变化」（/messages?peer=2、列表翻页），不是换页。
 * 对它播报会把读屏用户从正在读的会话里拽走。
 */
export function isPageChange(
  to: { name?: unknown; params: Record<string, string | string[]> },
  from: { name?: unknown; params: Record<string, string | string[]>; matched?: unknown[] },
): boolean {
  const matched = from.matched
  if (!matched || matched.length === 0) return false
  if (to.name !== from.name) return true
  return !sameParams(to.params, from.params)
}

/** 参数是否逐项相同（浅比较，值都是字符串或字符串数组）。 */
export function sameParams(
  a: Record<string, string | string[]>,
  b: Record<string, string | string[]>,
): boolean {
  const ka = Object.keys(a)
  const kb = Object.keys(b)
  if (ka.length !== kb.length) return false
  for (const key of ka) {
    const va = a[key]
    const vb = b[key]
    if (Array.isArray(va) || Array.isArray(vb)) {
      if (!Array.isArray(va) || !Array.isArray(vb)) return false
      if (va.length !== vb.length) return false
      if (va.some((v, i) => v !== vb[i])) return false
      continue
    }
    if (va !== vb) return false
  }
  return true
}
