/**
 * 帖子列表的 URL 契约（票 #66）—— `utils/dmRoute.ts` / `utils/friendsRoute.ts` 的同族兄弟。
 *
 * # 为什么要有这个文件
 *
 * 三个 pill（最新 / 热门 / 关注）的选中态是**可分享的状态**：决议 #52 §8 明写
 * 「三 pill 统一 `?sort=` 同步进 URL，刷新/后退/分享保住选中态」。
 * 而这件事只有一个前提：**键名只有一处定义**。页面里手写 `route.query.sort`
 * 与手写 `?sort=` 各来一份，改名就会漏掉一半 —— 症状是"链接还能打开，
 * 但落到了默认 pill"，界面上没有任何报错。
 *
 * # 默认值不进 URL
 *
 * `sort=latest` 是默认 pill，它**不写进 URL**：`/posts` 与 `/posts?sort=latest`
 * 是同一屏，让两者共存只会制造"同一状态两个地址"。
 *
 * # `?sort=following` 分享出去是什么
 *
 * 是**对方自己的**关注流（登录者各看各的）。这是 `following` 的正确含义，
 * 不是 bug —— 游客打开时按决议 §6 渲染分区内登录引导。
 */

/** 排序的 query 键名（跨票契约，改它等于改所有已分享出去的链接）。 */
export const POSTS_SORT_QUERY = 'sort'

/**
 * 三个 pill（决议 #52 §1）。
 *
 * `following` 需要登录才能读到内容（服务端 401），但**pill 本身可见可点** ——
 * 游客点进来看到的是一段就地登录引导，而不是被弹到登录页（§6）。
 */
export type PostsSort = 'latest' | 'hot' | 'following'

/** 默认 pill：最新。 */
export const POSTS_DEFAULT_SORT: PostsSort = 'latest'

/** 三个合法值的集合（解析用）。 */
const SORTS: readonly PostsSort[] = ['latest', 'hot', 'following']

/**
 * 解析排序。
 *
 * 未知值一律回落默认，**不报错**：URL 是别人给的，一个手改错的 `?sort=xx`
 * 不该把页面变成错误态（那不是"数据出错"，只是"链接里有个我不认识的词"）。
 * 服务端对未知 sort 回 400 —— 但那只在页面真的把未知值发出去时才发生，
 * 而这里已经把它挡住了。
 */
export function parsePostsSort(raw: unknown): PostsSort {
  const v = typeof raw === 'string' ? raw.trim() : ''
  return (SORTS as readonly string[]).includes(v) ? (v as PostsSort) : POSTS_DEFAULT_SORT
}

/**
 * 拼帖子列表地址。
 *
 * 默认排序不写进 URL（理由见文件头），其余原样编码。
 */
export function postsPath(input: { sort?: PostsSort } = {}): string {
  const sort = input.sort ?? POSTS_DEFAULT_SORT
  return sort === POSTS_DEFAULT_SORT ? '/posts' : '/posts?' + POSTS_SORT_QUERY + '=' + encodeURIComponent(sort)
}

/** 这个排序要不要登录才读得到内容（只有 following 要）。 */
export function sortNeedsLogin(sort: PostsSort): boolean {
  return sort === 'following'
}
