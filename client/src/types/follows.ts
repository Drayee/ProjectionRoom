/**
 * 关注域的数据模型（wayfinder #56）。
 *
 * 与帖子/好友域同一条纪律：**服务端契约不掺显示层**。
 * 所以这里没有 avatarChar / avatarColor —— 头像首字母与取色由
 * `utils/avatar.ts` 从 `id` + `displayName` 派生（色板只有一个源）。
 *
 * 形状对着服务端 #56 冻结的 `followView` 逐字对应：
 * `userId / displayName / following / followedBy / mutual / canDm /
 * `followerCount / followingCount`。
 */

/**
 * 我与某个人的关系（服务端算好的派生视图）。
 *
 * 三个布尔的关系语义（GLOSSARY「Social graph」）：
 *   - `following`  = 我关注了 TA；
 *   - `followedBy` = TA 关注了我（我是 TA 的粉丝）；
 *   - `mutual`     = 互关（= 好友，ADR 0001）。
 *
 * ⚠ `mutual` 是**派生**的（两个方向都有边），不是服务端存的状态 ——
 * 所以前端绝不能自己拿 `following && followedBy` 去算它：
 * 那个式子在服务端加上"拉黑"这一维之后就不再等价（互关但被拉黑 → 不能私聊）。
 * 服务端算好的那份才是真源（铁律 I3）。
 */
export interface FollowRelation {
  /** 对方（被关注的人）的 id。 */
  userId: number
  /** 对方的显示名。 */
  displayName: string
  /** 我关注了对方。 */
  following: boolean
  /** 对方关注了我。 */
  followedBy: boolean
  /** 互关（= 好友）。 */
  mutual: boolean
  /** 我能给对方发私聊（= mutual ∧ ¬blocked_either）。 */
  canDm: boolean
  /** 对方的粉丝数（关注对方的人数）。 */
  followerCount: number
  /** 对方的关注数（对方关注的人数）。 */
  followingCount: number
}

/**
 * 关系动作的**语义**（界面按钮该显示什么）。
 *
 * 与服务端的 `following / followedBy / mutual` 三元组一一对应，
 * 但**多出两个**服务端不返回的状态：
 *   - `blocked`：拉黑存续期 —— 关注按钮整个消失（GLOSSARY：拉黑存续期不给关注按钮）；
 *   - `self`：本人 —— 没有"关注自己"这个动作。
 *
 * 为什么 blocked 由**前端**判断而不是服务端回一个字段：
 * 服务端对"对方拉黑了我"与"我拉黑了对方"是同一个口径（都不告诉你谁拉黑了谁，
 * 见 #39 的搜索结果语义），而前端需要区分"我不能关注"（我拉的）与
 * "我也关注不了"（对方拉的）—— 这两者的界面文案不同。
 * 所以这一项由页面按自己拉黑名单（`friendsApi.listBlocked`）判断，
 * 而**能不能关注**的最终判据始终在服务端（403）。
 */
export type FollowAction = 'none' | 'following' | 'follow-back' | 'mutual' | 'blocked' | 'self'

/**
 * 名单方向（票 #58）。
 *
 * 三个值与服务端的 `?tab=` 逐字一致，且**只有三个**：黑名单不是关注名单的一种，
 * 它走 `/api/blocks`（拉黑是另一张表的另一件事）。界面上四个 tab 里那个
 * `blocked` 由 `FriendsTab` 表达，两者的差别见 `utils/friendsRoute.ts`。
 */
export type FollowListTab = 'mutual' | 'following' | 'followers'

/**
 * 名单里的一行（服务端 `followListEntryView`）。
 *
 * 与 `FollowRelation` 的区别：没有计数（名单页不显示计数），但**带 username**
 * —— 两个同昵称的人在同一列里只能靠账号名区分。
 *
 * 拉黑方向**不在**这里：名单本身已经被服务端滤掉了被拉黑的人，
 * 所以"是否拉黑"在这个形状里没有消费者。
 */
export interface FollowListEntry {
  userId: number
  username: string
  displayName: string
  following: boolean
  followedBy: boolean
  mutual: boolean
  canDm: boolean
}

/**
 * 名单查询的结果（票 #67）。
 *
 * 为什么不是直接返回 `FollowListEntry[]`：粉丝 tab 除了列表还要带回**红点数**
 * （决议 #53 §2：`new_count` = 我未回关的粉丝中、关注时间晚于水位线的人数）。
 * 两者来自**同一次查询**是刻意的 —— 拆成两个端点会让"进粉丝 tab"变成两次往返，
 * 而两次往返之间刚好有人关注你，界面就会显示"列表里有 TA、红点却说 0"。
 *
 * `newCount` 只有粉丝 tab 有值（服务端只在那一个分支回这个字段）：
 *   - `null` = 这个 tab 不提供红点（互关/关注）；
 *   - `0`    = 有红点这个概念，但已经清干净了。
 * 用 `null` 而不是 `0` 兜底：两者在界面上都不显示角标，但在判据里是不同的事实，
 * 混成一个值之后"服务端忘了回这个字段"就再也测不出来了。
 */
export interface FollowListResult {
  items: FollowListEntry[]
  newCount: number | null
}
