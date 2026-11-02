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
 * ## 四个 tab
 *
 *   overview   概览：资料编辑 + 我的帖子 + 我的放映室
 *   relations  关系管理：好友（互关）/ 关注 / 粉丝 三份名单 + 黑名单入口
 *   favorites  我的收藏（票 #65）：帖子 / 番剧 / 放映室三份书签
 *   settings   账号设置（票 #71 落地；本票只放占位说明）
 *
 * 收藏单独一个 tab 而不是塞进概览：它是**跨对象**的（帖子、番剧、放映室三种类型），
 * 而概览里那两块内容都是"我写的东西"。混在一起会让"我收藏的"与"我发布的"
 * 变成同一列 —— 那是两种完全不同的关系。
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

/**
 * 收藏分区的类型键名。
 *
 * 与关系分区的 `list` 同一个理由：`tab` 表达页面级分区（概览/关系/收藏/设置），
 * 类型是收藏分区**内部**的选择。合成一个键会让 `?tab=post` 与 `?tab=anime`
 * 看起来像两个页面，而它们其实是同一个页面的两个筛选。
 */
export const ME_FAV_TYPE_QUERY = 'type'

/** 收藏分区的三份书签（与 `types/favorites.ts` 的 `FavoriteType` 同值）。 */
export type MeFavoriteType = 'post' | 'anime' | 'room'

/** 默认收藏类型：帖子。它是三种里数量最多的一种。 */
export const ME_DEFAULT_FAV_TYPE: MeFavoriteType = 'post'

/**
 * 解析 `?type=`。
 *
 * 未知值回落默认 —— 与 `parseMeTab` / `parseMeList` 同一纪律：地址栏是用户可以
 * 随手改的地方，改错一个字母就给他一个错误页是没有道理的。
 */
export function parseMeFavType(raw: unknown): MeFavoriteType {
  if (raw === 'anime') return 'anime'
  if (raw === 'room') return 'room'
  return ME_DEFAULT_FAV_TYPE
}

/** 四个 tab。 */
export type MeTab = 'overview' | 'relations' | 'favorites' | 'settings'

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
  if (raw === 'favorites') return 'favorites'
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
export function mePath(input: { tab?: MeTab; list?: MeRelationList; type?: MeFavoriteType } = {}): string {
  const tab = input.tab ?? ME_DEFAULT_TAB
  const parts: string[] = []
  if (tab !== ME_DEFAULT_TAB) parts.push(ME_TAB_QUERY + '=' + encodeURIComponent(tab))
  if (tab === 'relations') {
    const list = input.list ?? ME_DEFAULT_LIST
    if (list !== ME_DEFAULT_LIST) parts.push(ME_LIST_QUERY + '=' + encodeURIComponent(list))
  }
  if (tab === 'favorites') {
    const type = input.type ?? ME_DEFAULT_FAV_TYPE
    if (type !== ME_DEFAULT_FAV_TYPE) parts.push(ME_FAV_TYPE_QUERY + '=' + encodeURIComponent(type))
  }
  return parts.length === 0 ? '/me' : '/me?' + parts.join('&')
}
