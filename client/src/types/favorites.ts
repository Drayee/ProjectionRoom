/**
 * 收藏数据模型（决议 #48 / 票 #64）。
 *
 * ## 为什么收藏与点赞是**两套**类型
 *
 * 它们在界面上长得像（都占动作条一格、都能按下），但语义正交：
 *
 *   - 点赞是**公开表态**，计入 `likeCount`，全世界看得见；
 *   - 收藏是**私人书签**，不产生任何公开计数，只有我自己能读到。
 *
 * 所以 `Post` 上有 `liked` + `likeCount` 两个字段，而收藏只有一个 `favorited` 布尔 ——
 * **没有 `favoriteCount`**，这不是漏了，是决议 #48 §3 的对照表：一个公开计数
 * 会让"私人书签"立刻变成可被围观的社交信号。
 *
 * ## targetId 为什么是 string
 *
 * 三种可收藏对象的主键类型不一致：帖子/番剧是自增 bigint，**放映室是房间码**
 * （`^[A-Z0-9]{4,12}$` 的 varchar）。共用一个多态列就只能取能装下房间码的那种。
 * 前端跟着用 string，页面在需要数字时自己转（`Number(entry.targetId)`）。
 */

/** 可收藏对象的类型。与 DDL 的 CHECK 同值（三个，不多不少）。 */
export type FavoriteType = 'post' | 'anime' | 'room'

/**
 * 收藏列表里的一行。
 *
 * `valid` 是**失效态**的开关：对象被删/放映室关闭/番剧下架时条目仍然在
 * （决议 #48 §5：不静默删除），只是 `valid=false`、`title` 退回占位文案。
 * 界面据此灰显 + 不给点击 —— "你收藏过这个，但它已经不在了"比"它凭空消失"更可解释。
 */
export interface FavoriteEntry {
  targetId: string
  targetType: FavoriteType
  /** 对象当前的标题；失效时是占位文案（不是空串，空串会让列表出现一排空白）。 */
  title: string
  /** false = 对象已失效（保留条目、不可点）。 */
  valid: boolean
  /** 收藏时间（ISO 串），列表按它倒序。 */
  createdAt: string
  /** 游标：把这一行作为 `before` 传回去就取更旧的一页（不透明串，前端不解析）。 */
  cursor: string
}

/** 收藏列表一页。`hasMore` 驱动无限滚动（服务端不做总数查询）。 */
export interface FavoriteListResult {
  items: FavoriteEntry[]
  hasMore: boolean
}

/**
 * 一次收藏/取消收藏的结果。
 *
 * `favorited` 是**操作后的权威状态**（不是"变了没变"）；`created`/`removed` 只在
 * 真的改了行时出现 —— 幂等的可观察形态：重复收藏仍成功，只是 `created` 缺席。
 */
export interface FavoriteState {
  targetId: string
  targetType: FavoriteType
  favorited: boolean
  /** 仅 PUT：true = 这次真的新建了行（重复收藏时缺席）。 */
  created?: boolean
  /** 仅 DELETE：true = 这次真的删掉了行（重复取消时缺席）。 */
  removed?: boolean
}
