/**
 * 私聊域的数据模型（wayfinder #30）。
 *
 * 这里同时收着**门面契约**（`ListHistoryInput` / `HistoryPage` / `ConversationEntry`）：
 * 它们是 mock 与 real 两份实现共同遵守的形状，放在类型层而不是任一份实现里，
 * 避免两份实现互相 import（那会形成 `mock → 门面 → mock` 的环）。与
 * `types/posts.ts` / `types/friends.ts` 是同一条安排。
 *
 * 形状对着服务端 #31 冻结的契约做了一层**显示层化**：
 *   - 服务端 `messageView = {id, kind, senderId, senderName, recipientId?, body, createdAt, readAt}`
 *     → 前端 `DirectMessage`，把 `senderName` 收进 `sender`（`PersonRef`，含头像派生）；
 *   - 服务端 `conversationView = {peerId, peerName, lastId, lastSenderId, lastBody, lastSentAt, unreadCount}`
 *     → 前端 `ConversationEntry`，`peer` 是 `PersonRef`。
 *
 * **头像派生不进服务端契约**（#26 的结论）：`avatarChar` / `avatarColor` 由
 * `utils/avatar.ts` 在前端算，real 实现在适配层里补、mock 直接用同一个函数。
 */

/** 一个人的显示信息（与帖子/好友域同形状，头像派生共用 `utils/avatar.ts`）。 */
export interface PersonRef {
  id: number
  displayName: string
  avatarChar: string
  avatarColor: string
}

/**
 * 一条私聊消息（**只有 `kind=dm`**）。
 *
 * 房间内聊天（`kind=room`）属 `docs/ACCOUNTS.md` §7.4 的后续图，本票明确不做
 * （见 #30 的 out 段），所以这个类型里没有 `roomId`：多一个恒空的字段会让人
 * 以为它已经被支持。
 */
export interface DirectMessage {
  id: number
  senderId: number
  sender: PersonRef
  body: string
  createdAt: string
  /**
   * 已读时间；空串 = 未读。
   *
   * 它**只对收件人有意义**：发送方看到自己发出的消息永远是空串，因为
   * 已读回执的推送通道（`dm-read`）虽然已在 `/ws/user` 上落地，但本票不做实时投递
   * （#30 的 out 段），发送方要等下一次拉历史才能看到。
   */
  readAt: string
  /** 是不是我发的（渲染左右气泡用；由两份适配实现按当前用户 id 算好）。 */
  mine: boolean
}

/**
 * 会话列表的一行。
 *
 * 服务端把它**派生**出来（不建会话表，见 0012 迁移注释）：每个 peer 取"我参与的、
 * id 最大的那条 dm"，按该 id 倒序。所以这里的 `lastId` 同时是排序键与"已读位置"的
 * 天然上限。
 */
export interface ConversationEntry {
  peer: PersonRef
  lastId: number
  lastSenderId: number
  lastBody: string
  lastSentAt: string
  /** 这个对话里我未读的条数（服务端按 peer 现算，不是反范式计数）。 */
  unreadCount: number
}

/** 拉历史的入参。`peerId` 必填 —— 没有它就没有会话（服务端对缺 peer 回 400）。 */
export interface ListHistoryInput {
  peerId: number
  /** id 游标：只要 id **小于**它的（往上翻更早的）。缺省 = 第一页。 */
  before?: number
}

/**
 * 一页历史。
 *
 * `hasMore` 由服务端"多取一行"判定（不做 COUNT）；顺序是 **id DESC**（最新在前），
 * 与聊天界面的读法一致 —— 页面渲染时再倒一次即可，别在适配层偷偷 reverse
 * （那样"往上翻"要翻的边界就没人说得清了）。
 */
export interface HistoryPage {
  items: DirectMessage[]
  hasMore: boolean
}

/** 发一条私聊的入参。 */
export interface SendMessageInput {
  recipientId: number
  body: string
}

/**
 * 已读上报的入参。
 *
 * `upTo` 必填且必须是**具体的消息 id**：缺省不能当作"全部已读"（服务端会 400）。
 * 这条约束是有意的 —— 少传一个参数就静默清空全部未读，会让"未读数"变成一个
 * 无法解释的数字。
 */
export interface MarkReadInput {
  peerId: number
  upTo: number
}
