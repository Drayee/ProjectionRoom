/**
 * 番剧论坛域的数据模型（wayfinder #36）。
 *
 * 目录条目对应服务端 #37 的 `anime` 表（`animeView`）：id / title / aliases / coverUrl /
 * summary / createdAt。**没有** 主题串计数 —— 目录端点不返回它，前端不编一个。
 *
 * 主题串本身不是独立类型：它就是 `types/posts.ts` 的 `Post` 加上 `animeId` / `lastReplyAt`
 * （#38 的 `postView` 也是这么表达的）。共用同一个形状意味着详情页与回复可以完全复用
 * 帖子域，不需要第二套"主题串详情"。
 */

/** 目录里的一部番。 */
export interface Anime {
  id: number
  title: string
  /** 别名（用于搜索与展示，如 `Sousou no Frieren` / `芙莉莲`）。 */
  aliases: string[]
  /** 外链封面；空串 = 没有封面（界面降级成首字色块，不猜图片地址）。 */
  coverUrl: string
  summary: string
  /** 收录时间（RFC3339）。 */
  createdAt: string
}

/** 目录分页（与服务端 `{items,total,limit,offset}` 同形状）。 */
export interface AnimePage {
  items: Anime[]
  total: number
  limit: number
  offset: number
}
