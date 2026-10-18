/**
 * 好友域的数据模型（纯文本插值，无 HTML）。
 *
 * 门面契约（`FriendEntry` / `FriendRequests` / `SearchHit` / `BlockedEntry`）也收在这里：
 * mock 与 real 两份实现共同遵守，放类型层避免 `mock → 门面 → mock` 的环
 * （与 `types/posts.ts` 收 `ListPostsInput` 是同一条理由）。
 *
 * 形状对着服务端 #29 冻结的 `friendshipView`（`relationshipId` / `userId` / `displayName` /
 * `status`）做了一层**显示层化**：把 `userId + displayName` 收进 `user`，并补上前端才需要的
 * 两个派生字段（头像首字母/颜色）与 `presence`。派生只在 `api/real/friends.ts` 里做，
 * 服务端契约不掺显示层（同 #26 对帖子域的处理）。
 */

/** 关系状态：与服务端 `friendships.status` 逐字一致（docs/ACCOUNTS.md §7.1）。 */
export type FriendshipStatus = 'pending' | 'accepted' | 'blocked'

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
}

/** 好友列表里的一行：`accepted` 关系 + 对方。 */
export interface FriendEntry {
  /**
   * 关系行 id。
   *
   * **它是删除好友 / 接受请求时唯一正确的身份**，不是对方的 `userId`：
   * `DELETE /api/friends/:friendshipId` 收的是关系 id，传成 userId 只会 404
   * （服务端对越权与不存在统一回 404，见 #29）。
   */
  relationshipId: number
  /** 关系里的"对方"（相对当前登录用户）。 */
  user: PersonRef
  status: FriendshipStatus
  presence: PresenceState
}

/** 待处理请求的一行（收件箱 / 发件箱共用一个形状）。 */
export interface FriendRequest {
  relationshipId: number
  user: PersonRef
  status: FriendshipStatus
}

/** 待处理请求的两摞：别人发给我的（可接受/拒绝）与我发出去的（只能等）。 */
export interface FriendRequests {
  incoming: FriendRequest[]
  outgoing: FriendRequest[]
}

/**
 * 搜索结果里"我和这个人现在是什么关系" —— 决定按钮该显示什么、能不能点。
 *
 * 与服务端 #39 的 `relation` 逐字对应（store.RelationState 那一组常量）：
 * `none|friend|pending-out|pending-in|blocked`。`pending-*` 的**方向**由谁是
 * 请求方决定（服务端按 requester 判），前端不重新推导。
 */
export type RelationState = 'none' | 'friend' | 'pending-out' | 'pending-in' | 'blocked'

export interface SearchHit extends PersonRef {
  relation: RelationState
}

/** 黑名单里的一行。解除拉黑按 `userId`（`DELETE /api/blocks/:userId`，#39）。 */
export interface BlockedEntry {
  user: PersonRef
}
