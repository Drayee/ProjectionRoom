/**
 * 好友页的 URL 契约（票 #58）—— `utils/dmRoute.ts` 的同族兄弟。
 *
 * # 为什么要有这个文件
 *
 * 「好友页现在哪个 tab、在搜什么」是**可分享的状态**：规格的用户故事里明写
 * 「把 `/friends?tab=followers&q=...` 发给别人，对方看到同一屏」。
 * 而这件事只有一个前提：**键名只有一处定义**。一旦页面里手写 `route.query.tab`
 * 与手写 `?tab=` 各来一份，改名就会漏掉一半 —— 症状是"链接还能打开，但落到了
 * 默认 tab"，界面上没有任何报错。
 *
 * 所以：键名常量、解析、拼装，三件事全在这里。页面只 import 本文件。
 *
 * # 默认值不进 URL
 *
 * `tab=mutual` 是默认 tab，它**不写进 URL**：`/friends` 与 `/friends?tab=mutual`
 * 是同一屏，让两者共存只会制造"同一状态两个地址"（缓存、对比、分享都变麻烦）。
 * 搜索词相反：只要非空就写进 URL —— 它是**显式意图**，空串才等于"没有搜索"。
 */

/** tab 的 query 键名（跨票契约，改它等于改所有已分享出去的链接）。 */
export const FRIENDS_TAB_QUERY = 'tab'

/** 搜索词的 query 键名（同上）。 */
export const FRIENDS_Q_QUERY = 'q'

/**
 * "进页就把焦点放进搜索框"的 query 键名（票 #66 的空态深链用）。
 *
 * 决议 #52 §7 的空态引导是「还没有关注任何人 → 按钮深链找人入口」，
 * 而找人的动作就发生在好友页**页头那个常驻搜索框**里（#50 已定：搜索是状态，不是去处）。
 * 所以深链要能把焦点直接送进去 —— 否则用户点完按钮落到一屏列表上，
 * 还得自己找到输入框再点一下。
 *
 * 合法值只有 `search`；未知值等于没写（见 `wantsSearchFocus`）。
 */
export const FRIENDS_FOCUS_QUERY = 'focus'

/**
 * 四个 tab（票 #50 定稿）。
 *
 * `mutual` = 好友（互关）。它不是"另一种关系状态"，而是**派生事实**
 * （两个方向都有边，ADR 0001）—— 但作为 tab 名它需要一个稳定的词，
 * 而 `mutual` 比 `friends` 更准确地说明了它是怎么来的。
 */
export type FriendsTab = 'mutual' | 'following' | 'followers' | 'blocked'

/** 默认 tab：好友（互关）。 */
export const FRIENDS_DEFAULT_TAB: FriendsTab = 'mutual'

/** 四个合法 tab 的集合（解析用）。 */
const TABS: readonly FriendsTab[] = ['mutual', 'following', 'followers', 'blocked']

/** 输入防抖（票 #50 的数值）。 */
export const FRIENDS_DEBOUNCE_MS = 300

/**
 * 触发搜索的最少字符数（票 #50：≥2 字符才发请求）。
 *
 * 为什么不是 1：单个字符会命中几乎所有人（中文尤其），既浪费一次往返，
 * 又把"搜索结果"变成"用户目录"。而"我打了第一个字就发请求"与"我打完才发"
 * 在体验上没有差别 —— 防抖窗口里那个字通常会变成两个字。
 */
export const FRIENDS_MIN_QUERY = 2

/**
 * 解析 tab。
 *
 * 未知值一律回落默认 tab，**不报错**：URL 是别人给的，一个手改错的 `?tab=xx`
 * 不该把页面变成错误态（那不是"数据出错"，只是"链接里有个我不认识的词"）。
 */
export function parseFriendsTab(raw: unknown): FriendsTab {
  const v = typeof raw === 'string' ? raw.trim() : ''
  return (TABS as readonly string[]).includes(v) ? (v as FriendsTab) : FRIENDS_DEFAULT_TAB
}

/**
 * 解析搜索词：去首尾空白；非字符串回落空串。
 *
 * 不做长度校验（那是服务端的事，且超长关键词会被服务端 400）——
 * 这里只保证"页面拿到的一定是字符串"。
 */
export function parseFriendsQuery(raw: unknown): string {
  return typeof raw === 'string' ? raw.trim() : ''
}

/**
 * 拼好友页地址。
 *
 * 只写**非默认**项，参数顺序固定（tab 在前、q 在后）：
 * 顺序固定让同一屏只有一个地址，于是"对比两条链接是否同屏"变成字符串比较。
 */
export function friendsPath(input: { tab?: FriendsTab; q?: string } = {}): string {
  const params: string[] = []
  const tab = input.tab ?? FRIENDS_DEFAULT_TAB
  if (tab !== FRIENDS_DEFAULT_TAB) {
    params.push(FRIENDS_TAB_QUERY + '=' + encodeURIComponent(tab))
  }
  const q = (input.q ?? '').trim()
  if (q !== '') {
    params.push(FRIENDS_Q_QUERY + '=' + encodeURIComponent(q))
  }
  return params.length === 0 ? '/friends' : '/friends?' + params.join('&')
}

/**
 * 这个词该不该发搜索请求。
 *
 * 判据是**去空白后的字符数**（不是字节数）：`'猫'` 一个字符不发，`'月夜'` 发。
 * 用 `Array.from` 而不是 `.length` 来数，因为 `'🐱🐱'` 的 `.length` 是 4
 * （代理对算两个），那会让"两个表情"绕过最小长度。
 */
export function shouldSearch(raw: string): boolean {
  return Array.from(raw.trim()).length >= FRIENDS_MIN_QUERY
}

/**
 * 这个 URL 是不是要求"进页聚焦搜索框"。
 *
 * 只认 `focus=search` 一个值：URL 是别人给的，一个手改错的 `?focus=xx`
 * 不该让页面做出任何意外动作（这里最坏的结果只是"没聚焦"）。
 */
export function wantsSearchFocus(raw: unknown): boolean {
  return typeof raw === 'string' && raw.trim() === 'search'
}

/**
 * 「找人」的深链（票 #66 的空态引导用）。
 *
 * 落点刻意是 `/friends?focus=search` 而**不是**某个 tab：
 * 搜索是页头常驻状态、不依附任何 tab（#50 的勘误已把 `tab=followers` 那半截划掉）——
 * 带一个 tab 只会让人以为"搜索只在这个 tab 里有效"。
 */
export function findPeoplePath(): string {
  return '/friends?' + FRIENDS_FOCUS_QUERY + '=search'
}
