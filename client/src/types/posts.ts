/**
 * 帖子数据模型（纯文本插值，不做 Markdown/富文本）。
 *
 * 这里同时收着**门面契约**（`ListPostsInput` / `ListPostsResult`）：它们是 mock 与 real
 * 两份实现共同遵守的形状，放在类型层而不是任一份实现里，避免两份实现互相 import
 * （那会形成 `mock → 门面 → mock` 的环）。
 */
export interface Post {
  id: number
  title: string
  /** 纯文本正文，不包含任何 HTML（前端用 `{{ }}` + `white-space: pre-wrap` 渲染）。 */
  content: string
  authorId: number
  author: { id: number; displayName: string; avatarChar: string; avatarColor: string }
  /** 结构化图片列表（前端各自 `<img>` 渲染，绝不拼 HTML）。 */
  images: string[]
  likeCount: number
  commentCount: number
  liked: boolean
  /**
   * 我是否收藏了它（决议 #48 / 票 #64）。
   *
   * **注意这里没有 `favoriteCount`**：收藏是私人书签，不产生任何公开计数 ——
   * 那是点赞的分工。多一个公开计数会让"私人"立刻变成可被围观的社交信号。
   *
   * 字段与 `liked` 同批回填（服务端一次请求带回来，禁 N+1）；匿名视角恒 false。
   */
  favorited: boolean
  createdAt: string
  /**
   * 所属板块（wayfinder #38 / #36）：有值 = 这是一条**主题串**，普通帖子没有这个键。
   *
   * 服务端用指针 + omitempty 表达"可选归属"（`postView.AnimeID`），前端也保持可选：
   * 用 0 当哨兵会让每个普通帖子都声称属于 id=0 的板块。
   */
  animeId?: number
  /**
   * 最后回复时间（只有主题串有）。板块按「最新回复优先」排序时服务端已经排好，
   * 这里只是**回显**给用户看「最后活跃」。
   */
  lastReplyAt?: string
}

/** 评论（扁平，无楼中楼/引用）。 */
export interface Comment {
  id: number
  postId: number
  authorId: number
  author: { id: number; displayName: string; avatarChar: string; avatarColor: string }
  content: string
  createdAt: string
}

/** 帖子列表入参（排序 + 页码）。 */
export interface ListPostsInput {
  /**
   * 排序（票 #66 扩了第三个值）。
   *
   * `following` = 只看我关注的人，按发帖时间倒序（决议 #52 §1-§2）。
   * 它**需要登录**：服务端对匿名请求回 401，页面据此渲染分区内登录引导。
   */
  sort?: 'latest' | 'hot' | 'following'
  page?: number
}

/** 帖子列表出参（`hasMore` 驱动无限滚动，服务端不做总数查询）。 */
export interface ListPostsResult {
  items: Post[]
  hasMore: boolean
}
