/**
 * 导航当前态的唯一匹配源（票 #77）。
 *
 * ## 为什么不用 vue-router 自带的 `router-link-active`
 *
 * 路由自带的 active 判定是"按**记录**（route record）匹配"，而顶栏导航的语义单位是
 * **分区**（section），两者不等价：
 *
 * | 入口 | 分区 | 路由记录 | 自带 active 的表现 |
 * | :--- | :--- | :--- | :--- |
 * | 帖子 | `/posts` | `posts` / `post-detail` / `post-editor` | 进 `/posts/3` 时 `/posts` 的链接**不亮**（不同记录） |
 * | 番剧论坛 | `/forum` | `forum` / `anime-board` | 进 `/forum/7` 时 `/forum` 不亮 |
 * | 本人档案 | `/user/:id`（本人） | `user` | 亮的永远是 `/user/9` 那类链接，而不是"我"这个头像位 |
 *
 * 于是旧实现里 `router-link-active` 要么漏亮、要么误亮（父路径前缀把无关页面也算进去）。
 * 本文件把"哪些路径属于哪个入口"写成**一张显式表**，任何需要点亮的地方都查这张表 ——
 * 一处改，全站一致（顶栏分区、右侧入口、头像位、真机脚本的期望值）。
 *
 * ## 归属规则（从长到短，唯一优先）
 *
 * 1. **本人优先**：`/user/<自己的 id>` 归"我"（头像位），不归任何分区；
 * 2. 否则按 `PREFIXES` 的顺序逐条前缀匹配（顺序 = 明确程度，先具体后宽泛）；
 * 3. 都不命中 → `null`（落地页、登录页、帮助页这类"没有分区"的页面全灭）。
 *
 * 判据是**路径段**而不是字符串前缀：`/posts` 只匹配 `/posts` 与 `/posts/...`，
 * 不匹配 `/postsomething` —— 否则 `/post-editor` 与 `/posts` 之间会出现那种
 * "明明不是那个页面却亮了"的鬼。
 */

/**
 * 顶栏分区键。与 `AppHeader.vue` 的 `NAV` 表一一对应
 * （`section` 字段是同一批字符串），新增分区必须同时改两处，否则会漏亮。
 */
export type NavSection =
  | 'public-rooms'
  | 'posts'
  | 'forum'
  | 'friends'
  | 'messages'
  | 'help'
  | 'official'
  | 'admin'

/**
 * 前缀表：**唯一**的归属真源。
 *
 * 顺序有意义：更具体的前缀写在前面（`/admin/rooms` 先于 `/admin`，
 * `/posts/new` 先于 `/posts`）。当前这组前缀互不为对方的前缀，
 * 但顺序仍然写死，未来加 `/posts/:id/edit` 这类子分区时不必回头琢磨。
 *
 * @public-rooms 把 `/room/:id`（单间放映室）也算进来：从公开房间点进去就是同一件事，
 * 进房后导航整排熄灭会让用户以为"导航坏了"。落地页 `/` **不**归它 ——
 * 落地页是全屏 hero，点亮"公开房间"等于骗人。
 */
const PREFIXES: ReadonlyArray<readonly [prefix: string, section: NavSection]> = [
  ['/admin', 'admin'],
  ['/posts', 'posts'],
  ['/forum', 'forum'],
  ['/friends', 'friends'],
  ['/messages', 'messages'],
  ['/rooms', 'public-rooms'],
  ['/room', 'public-rooms'],
  ['/official', 'official'],
  ['/help', 'help'],
]

/** 把路径按 `/` 切段并丢掉空串（`/posts/3` → `['posts','3']`，`/` → `[]`）。 */
function segments(path: string): string[] {
  return path.split('?')[0].split('#')[0].split('/').filter(Boolean)
}

/** 路径的段是否以 `prefix` 的段开头（**段对齐**比较，不是字符串 startsWith）。 */
function hasPrefix(pathSegs: string[], prefix: string): boolean {
  const prefixSegs = segments(prefix)
  if (prefixSegs.length > pathSegs.length) return false
  for (let i = 0; i < prefixSegs.length; i += 1) {
    if (pathSegs[i] !== prefixSegs[i]) return false
  }
  return true
}

/**
 * 这个路径属于哪个导航分区？`null` = 不属于任何分区（顶栏全灭）。
 *
 * @param path          当前路径（`route.path`，不含 query/hash 也可以，函数会自己剥）
 * @param selfUserId   当前登录用户的 id；`null`/`undefined` = 未登录（此时 /user/:id 都归别人）
 */
export function navSectionOf(path: string, selfUserId?: number | null): NavSection | null {
  const segs = segments(path)

  // 规则 1：本人档案优先归"我"。
  if (
    typeof selfUserId === 'number' &&
    segs.length === 2 &&
    segs[0] === 'user' &&
    segs[1] === String(selfUserId)
  ) {
    return null
  }

  // 规则 2：显式前缀表。
  for (const [prefix, section] of PREFIXES) {
    if (hasPrefix(segs, prefix)) return section
  }

  // 规则 3：不命中任何分区。
  return null
}

/**
 * 是否"点亮本人头像位"。
 *
 * 只有 `/user/<自己的 id>` 这一种情况 —— 他人档案归"不属于任何分区"，
 * 头像位与分区图标都保持熄灭（票 #77 判据："本人路由点亮头像位、落地页全灭"）。
 */
export function isSelfProfile(path: string, selfUserId?: number | null): boolean {
  if (typeof selfUserId !== 'number') return false
  const segs = segments(path)
  return segs.length === 2 && segs[0] === 'user' && segs[1] === String(selfUserId)
}
