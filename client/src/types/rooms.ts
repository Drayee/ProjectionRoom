/**
 * 「我的放映室」的数据形状（票 #70）。
 *
 * 与 `api/publicRooms.ts` 的 `PublicRoom` **刻意不是同一个形状**，这不是疏漏：
 *
 * | 字段 | 公开房列表 | 我的放映室 |
 * | :--- | :--- | :--- |
 * | 成员数 / 主播在线 | 有（列表要展示热闹程度） | **没有**（那不是房主关心的事） |
 * | 是否公开 / 是否已关闭 | 没有（只列公开且未关闭的） | **有**（房主要据此重新开放） |
 * | 创建时间 | 没有 | 有 |
 *
 * 硬合成一个类型会让两边的界面各自忽略一半字段，而"这个字段到底该不该有"
 * 从此没人说得清。两种查询回答的是两个问题：**对外橱窗** vs **我的资产清单**。
 *
 * 与 `types/users.ts` 的 `AuthorRoomItem` 也不同：那个是他人档案页的对外橱窗
 * （只有公开且未关闭的房），没有公开性与关闭态两个布尔。
 */
export interface MyRoomItem {
  roomId: string
  /** 可以为空串（房主没起名）：界面负责显示「未命名房间」。 */
  title: string
  /** 是否对外公开。`false` = 私密房，只有房主能从这一页看见它。 */
  isPublic: boolean
  /** 是否已关闭。`true` = 房间已经结束，但记录还在（房主可以重新开放）。 */
  isClosed: boolean
  /** 有密码（只给标记，不下发密码本身）。 */
  hasPassword: boolean
  createdAt: string
  lastSeenAt: string
}

export interface MyRoomList {
  items: MyRoomItem[]
  hasMore: boolean
}
