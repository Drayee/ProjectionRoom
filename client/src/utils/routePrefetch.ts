import { PAGE_MODULES, type PageModuleName } from './routePages'

/**
 * 导航悬停预热（票 #79）。
 *
 * 做什么：鼠标悬到导航项上就把该页的 chunk 取回来，等用户真的点下去时模块已在缓存里，
 * 切换没有白屏。
 *
 * 三个刻意的决定：
 *
 *   1. **只预热一次**：同一个路由名重复悬停不重复发请求。
 *      预热的目的是"提前开始下载"，不是"多下几遍"；重复请求会让弱网更糟。
 *   2. **失败静默**：预热失败（网络抖动、chunk 404）**不报错、不弹提示、不重试**。
 *      预热是锦上添花，真点进去时路由会自己再加载一次 —— 那时失败才有资格打扰用户。
 *      在这里报错的后果是：用户只是把鼠标扫过导航栏，就弹出一条红色错误，
 *      而那次点击完全正常。
 *   3. **按路由名取，不按路径**：`/posts/3` 与 `/posts` 共用同一个 chunk，
 *      按路径预热会下载同一个模块两次。
 */

/** 已发起过的预热（路由名 → 那个 Promise）。键存在即"已经预热过"。 */
const warmed = new Map<PageModuleName, Promise<void>>()

/**
 * 预热某个路由名的页面 chunk。
 *
 * 返回值只是给测试/诊断用的句柄：**调用方不要 await 它** ——
 * 预热不该挡住任何交互（悬停时等一个网络往返是不可接受的）。
 */
export function prefetchRoute(name: string): Promise<void> | null {
  const key = name as PageModuleName
  const loader = PAGE_MODULES[key]
  // 未知路由名：什么都不做（不抛错）。调用方可能传了非路由名（例如页面内的链接）。
  if (!loader) return null
  const existing = warmed.get(key)
  if (existing) return existing

  // 失败静默：catch 吞掉错误，Promise 仍然 resolve（不产生未处理的 rejection）。
  const task = loader()
    .then(() => undefined)
    .catch(() => undefined)
  warmed.set(key, task)
  return task
}

/** 某个路由名是否已经预热过（诊断用；不暴露模块缓存本身）。 */
export function isWarmed(name: string): boolean {
  return warmed.has(name as PageModuleName)
}

/** 清空预热记录（**只给测试用**：让每个用例都从"没预热过"的状态开始）。 */
export function resetPrefetch(): void {
  warmed.clear()
}
