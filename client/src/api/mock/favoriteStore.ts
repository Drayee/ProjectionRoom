/**
 * 收藏的**内存真源**（mock 专用）。
 *
 * ## 为什么单独一个模块，而不是塞进 `mock/favorites.ts`
 *
 * 收藏状态有两个读者：收藏门面自己（列表/写），和**帖子 mock**（每一行都要带
 * `favorited`，否则动作条上的收藏位永远是灭的）。若把集合放进 `mock/favorites.ts`，
 * 帖子 mock 就得 import 它，而收藏 mock 又要 import 帖子 mock 去查标题 —— 立刻成环。
 *
 * 拆出这个只有数据、没有业务的小模块，两边都只依赖它：
 *
 * ```
 * mock/favoriteStore.ts  ←── mock/posts.ts        （读：这一行收藏了没）
 *         ↑
 *         └──────────────── mock/favorites.ts     （读写：列表与增删）
 * ```
 *
 * 真实现里这份状态在服务端（`favorites` 表），前端只读响应 —— 所以这个模块
 * **不会**出现在真实路径上，它只是让 mock 态能自洽。
 *
 * ## 种子为什么不是空的
 *
 * 空集合下 `/me` 的收藏区块只能验空态，而"有数据时的排版"才是最容易坏的那半。
 * 种三条覆盖三种类型，且刻意让其中一条**指向一个不存在的对象**：
 * 失效态（决议 #48 §5：条目保留、灰显、不可点）必须能在 mock 态被看见，
 * 否则它要等真有人删了帖子才第一次被渲染。
 */

/** 收藏键：`<type>:<targetId>`。用字符串键是因为 targetId 对放映室不是数字。 */
function keyOf(type: string, targetId: string): string {
  return `${type}:${targetId}`
}

/**
 * 已收藏的键集合。
 *
 * 种子：两篇帖子（id 1、3）、一个番剧（id 1）、**一个已失效的帖子（id 9999）**。
 * 9999 在帖子 mock 里不存在 —— 列表里它应当渲染成「已失效」且不可点。
 */
const keys = new Set<string>([
  keyOf('post', '1'),
  keyOf('post', '3'),
  keyOf('anime', '1'),
  keyOf('post', '9999'),
])

/** 收藏时间（mock 里给个稳定的递减序列，让列表倒序看起来是真排过的）。 */
const stamps = new Map<string, string>([
  [keyOf('post', '1'), new Date(Date.now() - 3 * 60 * 1000).toISOString()],
  [keyOf('post', '3'), new Date(Date.now() - 40 * 60 * 1000).toISOString()],
  [keyOf('anime', '1'), new Date(Date.now() - 5 * 60 * 60 * 1000).toISOString()],
  [keyOf('post', '9999'), new Date(Date.now() - 26 * 60 * 60 * 1000).toISOString()],
])

/** 这个对象是否已被我收藏（帖子 mock 每渲染一行都要问一次）。 */
export function isFavorited(type: string, targetId: string): boolean {
  return keys.has(keyOf(type, targetId))
}

/**
 * 写入/删除收藏，返回**是否真的改了**（幂等的可观察形态）。
 *
 * 时间戳只在**新建**时写：重复收藏不该把条目顶到列表最前面 ——
 * 那会让"我刚刚又点了一次"看起来像"我刚刚收藏了它"。
 */
export function setFavorited(type: string, targetId: string, on: boolean): boolean {
  const k = keyOf(type, targetId)
  if (on) {
    if (keys.has(k)) return false
    keys.add(k)
    stamps.set(k, new Date().toISOString())
    return true
  }
  if (!keys.has(k)) return false
  keys.delete(k)
  stamps.delete(k)
  return true
}

/** 某类型下的全部键（收藏列表用；顺序由调用方按时间戳排）。 */
export function keysOfType(type: string): string[] {
  const prefix = `${type}:`
  return [...keys].filter((k) => k.startsWith(prefix)).map((k) => k.slice(prefix.length))
}

/** 收藏时间（列表排序与游标都用它）。 */
export function stampOf(type: string, targetId: string): string {
  return stamps.get(keyOf(type, targetId)) ?? new Date(0).toISOString()
}
