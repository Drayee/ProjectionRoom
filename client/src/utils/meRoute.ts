/**
 * 「我的管理台」URL 契约（票 #69）。
 *
 * 与 `utils/dmRoute.ts`（`?peer=`）、`utils/friendsRoute.ts`（`?tab=`/`?q=`）、
 * `utils/postsRoute.ts`（`?sort=`）同族：**键名只有这一个源**。
 * 别处再写一次 `route.query['tab']` 就等于开了第二个源 —— 一旦有人把键名
 * 从 `tab` 改成 `section`，分享出去的链接会静默退回默认视图，而界面上没有任何报错。
 *
 * ## 为什么 tab 要进 URL
 *
 * 决议 #51 §1：账号设置住在 `/me?tab=settings` 深链分区，与 #50 的 tab 进 URL
 * 同族契约（分享 / 刷新 / 后退都不丢）。设置区本身在票 #71 落地，
 * 本票先把**槽位**与键名钉下来 —— 那时只往里填内容，不改地址形态。
 *
 * ## 三个 tab
 *
 *   overview   概览：资料编辑 + 我的帖子 + 我的放映室 + 关系入口
 *   relations  关系管理：好友（互关）/ 关注 / 粉丝 三份名单 + 黑名单入口
 *   settings   账号设置（票 #71 落地；本票只放占位说明）
 *
 * 概览与关系分成两个 tab 而不是堆在一页：三份名单每份都可能很长，
 * 堆在资料下面会让"改个昵称"要滚过几十行名单。
 */

/** tab 的 query 键名（跨票契约，**不要改**）。 */
export const ME_TAB_QUERY = 'tab'

/**
 * 关系管理里"正在看哪份名单"的 query 键名。
 *
 * 为什么需要第二个键而不是把名单塞进 `tab`：`tab` 表达的是**页面级分区**
 * （概览 / 关系 / 设置），名单是关系分区**内部**的选择。混成一个键的话
 * `?tab=followers` 会与 `/friends` 的键名撞车，而且用户从关系分区切到设置
 * 再切回来就丢了他正在看的那份名单。
 */
export const ME_LIST_QUERY = 'list'

/** 关系分区的四份名单（前三份是关注边的三个方向，第四份是另一张表）。 */
export type MeRelationList = 'mutual' | 'following' | 'followers' | 'blocked'

/** 默认名单：好友（互关）。 */
export const ME_DEFAULT_LIST: MeRelationList = 'mutual'

/** 解析 `?list=`：未知值回落默认，不报错（与 `parseMeTab` 同一纪律）。 */
export function parseMeList(raw: unknown): MeRelationList {
  if (raw === 'following') return 'following'
  if (raw === 'followers') return 'followers'
  if (raw === 'blocked') return 'blocked'
  return ME_DEFAULT_LIST
}

/** 三个 tab。 */
export type MeTab = 'overview' | 'relations' | 'settings'

/** 默认 tab：地址里不写 `?tab=` 时就是它。 */
export const ME_DEFAULT_TAB: MeTab = 'overview'

/**
 * 解析 `?tab=`。
 *
 * 未知值 / 缺省 / 非字符串一律**回落默认**，不报错 —— 地址栏是用户可以随手改的地方，
 * 改错一个字母就给他一个错误页是没有道理的。
 */
export function parseMeTab(raw: unknown): MeTab {
  if (raw === 'relations') return 'relations'
  if (raw === 'settings') return 'settings'
  return ME_DEFAULT_TAB
}

/**
 * 生成 `/me` 的地址。
 *
 * 默认值**不写进 URL**：`/me` 与 `/me?tab=overview` 是同一个地方，
 * 两个地址会让「分享出去的链接」与「自己地址栏里的」长得不一样，
 * 而用户无从判断哪个是"对"的。
 *
 * `list` 只在 `tab=relations` 下有意义 —— 概览/设置里带着 `?list=followers`
 * 会让人以为"切个 tab 名单还在"，实际上它被忽略了。所以非关系分区**一律不写**。
 */
export function mePath(input: { tab?: MeTab; list?: MeRelationList } = {}): string {
  const tab = input.tab ?? ME_DEFAULT_TAB
  const parts: string[] = []
  if (tab !== ME_DEFAULT_TAB) parts.push(ME_TAB_QUERY + '=' + encodeURIComponent(tab))
  if (tab === 'relations') {
    const list = input.list ?? ME_DEFAULT_LIST
    if (list !== ME_DEFAULT_LIST) parts.push(ME_LIST_QUERY + '=' + encodeURIComponent(list))
  }
  return parts.length === 0 ? '/me' : '/me?' + parts.join('&')
}
