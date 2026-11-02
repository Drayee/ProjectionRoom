/**
 * 用户档案域的数据模型（wayfinder #32；服务端契约由 #33 冻结）。
 *
 * 与帖子/好友域同一条纪律：**服务端契约不掺显示层**。所以这里没有
 * `avatarChar` / `avatarColor` —— 头像首字母与取色由 `utils/avatar.ts` 从
 * `id` + `displayName` 派生（#33 刻意就不在 Go 里再实现一份，否则色板有两个源）。
 *
 * 另外一个**安全相关**的事实：公开档案只回白名单里的五个字段。
 * `email` / `status` / `tokenVersion` 在这里**没有对应字段** —— 不是"没填"，
 * 而是类型层面就不存在（#33 的字段白名单用例同时钉住了正向与反向）。
 */

/** 公开档案（他人视角，免登录可读）：`GET /api/users/:id`。 */
export interface PublicProfile {
  id: number
  /** 登录名（社区内本就公开：#39 的按用户名搜索就把它回给调用方）。 */
  username: string
  displayName: string
  /**
   * `user` | `admin`，**只作标签用**。
   *
   * 铁律 I3：授权 100% 在服务端。这里哪怕显示成"管理员"，也绝不能据此
   * 决定"能不能点某个按钮"—— 那必须由服务端 `RequireAdmin` 回答。
   */
  role: string
  /** 加入时间（RFC3339 字符串）。空串 = 服务端没返回，界面显示「服务端未返回」。 */
  createdAt: string
}

/**
 * 本人档案（`PATCH /api/me` 的响应体里 `user` 那一份）。
 *
 * 字段与 `/api/auth/me` 同形状（服务端 `usecase.Profile`），所以它可以被
 * `stores/auth.ts` 的档案落地入口直接吸收。`email` 在服务端是 `*string`
 * （未绑定即 `omitempty`），适配层已把它归一成空串。
 */
export interface MyProfile {
  id: number
  username: string
  displayName: string
  email: string
  emailVerified: boolean
  role: string
  /** active | banned。 */
  status: string
  createdAt: string
}

/**
 * 他人档案的内容块（票 #68）。两个只读端点，都**免登录**：
 *
 *   GET /api/users/:id/posts?limit&offset   → AuthorPostList
 *   GET /api/users/:id/rooms?limit&offset   → AuthorRoomList
 *
 * 为什么是子资源而不是 `GET /api/posts?authorId=`：决议 #47 §5 要求二选一。
 * 选子资源的理由写在 `internal/handler/profile.go` 的文件头（准入不同、前缀已存在、
 * 查询意图不同），前端这边只需要知道"档案页的两块内容各有自己的端点"。
 *
 * 分页语义与帖子列表完全一致（limit/offset + hasMore），因为档案页的无限滚动
 * 与帖子列表是同一个交互 —— 两处用不同的页大小会让"翻到第几页"有两个答案。
 */
export interface AuthorPostList {
  items: import('./posts').Post[]
  hasMore: boolean
}

/**
 * 作者房列表里的一行。
 *
 * 与 `PublicRoom`（公开房列表的一行）**不是**同一个形状，这不是疏漏：
 *
 *  - 没有 `memberCount` / `hostOnline` / `hostOffline` —— 那些是**内存实况**
 *    （"现在房里几个人、主播在不在"），而这个端点只读元数据（"这个人开过哪些房"）。
 *    把实况混进来的代价是端点要依赖房间管理器，而它对"当前开没开"并不关心。
 *  - 没有 `ownerName` —— 调用方已经知道这是谁的档案（URL 里就是那个 id），
 *    每行重复一遍昵称只是让响应变大。
 *  - 多的是 `createdAt` / `lastSeenAt`：档案页要按"最近活跃"排序并显示它。
 */
export interface AuthorRoomItem {
  roomId: string
  /** 空串 = 房主没起名（界面显示「未命名房间」，与公开房列表同一口径）。 */
  title: string
  createdAt: string
  lastSeenAt: string
}

export interface AuthorRoomList {
  items: AuthorRoomItem[]
  hasMore: boolean
}
