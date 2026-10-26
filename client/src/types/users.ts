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
