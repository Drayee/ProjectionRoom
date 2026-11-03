/**
 * 好友域的数据模型（纯文本插值，无 HTML）。
 *
 * ⚠ **票 #59 之后这里只剩「搜索」与「黑名单」两件事**：
 * 「好友」不再是这张表里的一种状态，而是关注域的**派生**（互关）。
 * 曾经的 `FriendshipStatus` / `FriendEntry` / `FriendRequest(s)` / `RelationState`
 * 随好友请求流水线一起退役 —— 留着它们只会诱使新代码再去读一个没有真源的状态。
 *
 * 三个仍然活着的形状对着服务端冻结的契约：
 *   - `searchHitView`    → `SearchHit`（关注域四个布尔 + blocked）
 *   - `blockedEntryView` → `BlockedEntry`
 *   - `UserAvatar` 的 `user` prop → `PersonRef`
 */

/**
 * 在线态。
 *
 * `unknown` 是**真实实现唯一合法的取值**：presence 要等后续图的 `/ws/user` 用户级实时通道；
 * 在那之前前端没有任何可靠来源，猜一个绿点等于撒谎。mock 用假数据演示这一列长什么样，
 * 页面据 `unknown` 决定"这一行不显示在线态"，而不是显示成离线。
 */
export type PresenceState = 'online' | 'offline' | 'unknown'

/** 一个人的显示信息（与帖子域的 `author` 同形状，头像派生共用 `utils/avatar.ts`）。 */
export interface PersonRef {
  id: number
  displayName: string
  avatarChar: string
  avatarColor: string
  /**
   * 像素头像（票 #74）。缺省/空 = 回落字母头 —— **这是可选覆盖层，不是头像本身**。
   *
   * 与 `avatarChar` / `avatarColor` 的来源相反：那两个是前端派生，
   * 这个由服务端给（`users.avatar_url`），适配层只透传不加工。
   */
  avatarUrl?: string | null
}

/**
 * 搜索结果里的一行。
 *
 * **关系事实全部来自关注域**（票 #58 落地、#59 收口）：服务端一次算好四个布尔，
 * 前端据此决定给「关注 / 已关注 / 回关 / 发消息」哪一颗按钮，不自己推导方向
 * （I3：授权与关系判定都在服务端）。
 *
 * 旧版本这里还有一个 `relation: 'none'|'friend'|'pending-out'|'pending-in'|'blocked'`，
 * 那是 friendships 流水线的语言。它退役之后，「是不是好友」由 `mutual` 回答 ——
 * 同一个问题有两个字段可读，早晚会读到不一样的那个。
 */
export interface SearchHit extends PersonRef {
  /** 我关注了 TA。 */
  following: boolean
  /** TA 关注了我。 */
  followedBy: boolean
  /** 互关 = 好友（GLOSSARY：好友 ≡ 互关，没有独立的好友请求状态）。 */
  mutual: boolean
  /** 能私聊：互关且双向无拉黑（服务端 `can_dm`）。 */
  canDm: boolean
  /**
   * 我们之间存在拉黑（**不区分方向**）。
   *
   * 拉黑存续期整个不给关注按钮（GLOSSARY：拉黑存续期禁止新关注）。
   * 刻意不告诉前端"是谁拉黑了谁" —— 那个方向没有产品用途，
   * 却能让被拉黑的人知道对方拉黑了自己。
   */
  blocked: boolean
}

/** 黑名单里的一行。解除拉黑按 `userId`（`DELETE /api/blocks/:userId`）。 */
export interface BlockedEntry {
  user: PersonRef
}
