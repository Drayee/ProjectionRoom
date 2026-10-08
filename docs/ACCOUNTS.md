# 账号 / 社交 / 管理端 —— 设计规格（ACCOUNTS）

> 上游：`docs/SPEC.md`（房间与信令协议，不变量 I1「服务端不传视频字节」）。
> 本文只覆盖**账号体系、公开房间、好友私聊、管理端、数据库持久化**这一层；
> 房间信令/拓扑/切片协议仍以 `docs/SPEC.md` 为准。
> 状态：**待评审**。评审通过后才进实现计划（`writing-plans`）。

---

## 1. 目标与非目标

### 目标
1. 用户名 + 密码的账号体系（注册 / 登录 / 刷新 / 登出 / 角色），邮箱**只预留字段不验证**。
2. **建房必须登录；进房（观众）不需要登录** —— 保留"发个房间码就能看"这条最低门槛。
3. 普通用户可浏览**公开房间**列表。
4. 好友关系（请求 / 同意 / 删除 / 拉黑）。
5. 好友私聊（持久化 + 离线未读）与**房间内聊天也落库**。
6. 管理端：用户 CRUD（含新建管理员）、角色授予、封禁/解封、房间列表与强制关闭、公开房审核下架、运行指标、服务端日志查看。
7. PostgreSQL 持久化；**业务代码用 GORM，禁止字符串拼接 SQL**。

### 非目标（本期明确不做）
- 邮箱验证与找回密码（只留字段）；TLS（部署决定，见 §11 风险）。
- 头像/图片上传（用内置图标集，避免引入对象存储与外链直出风险）。
- 多实例水平扩展（因此不需要 Redis / 消息队列；在线态与人数都在单实例内存里）。
- 房间历史录像、消息全文搜索、端到端加密私聊。

---

## 2. 不变量（实现时必须成立）

| 编号 | 内容 |
| --- | --- |
| **I1**（沿用） | 服务端永不承载视频字节；DB 也不存任何媒体内容 |
| **I2** | 房间的**实时状态**（成员、拓扑、播放进度、分片位图）只在内存；DB 只存**元数据**与**周期性快照** |
| **I3** | 授权 100% 在服务端；前端缓存的 `role` 只决定"显示什么"，绝不作为授权依据 |
| **I4** | 任何数据库写入都不在 WS / REST 的处理路径上（见 §9） |

---

## 3. 分层与新增模块

沿用现有分层，新增三块，方向单一（handler → usecase → store/auth）：

```
internal/handler/     auth.go  user.go  friend.go  chat.go  admin.go  publicroom.go  ws_user.go
internal/usecase/     account.go  friend.go  chat.go  admin.go  publicroom.go
internal/auth/        jwt.go（签发/校验 access token）  password.go（bcrypt）  ticket.go（WS 一次性票据）
internal/store/       gorm.go（连接与配置）  models.go  migrate.go（内嵌迁移）
                      user.go  session.go  friend.go  message.go  roommeta.go  snapshot.go  audit.go
internal/service/     presence.go（在线态）  logring.go（日志环形缓冲，供管理端）
internal/limiters/    （沿用）
```

`internal/usecase` 只依赖 `internal/store` 暴露的**接口**，单测用内存实现；`cmd/wire.go` 负责装配。

---

## 4. 数据模型（GORM）

```
users
  id            bigserial PK
  username      varchar(20) UNIQUE NOT NULL      -- ^[A-Za-z0-9_]{3,20}$，服务端统一小写存储
  display_name  varchar(32) NOT NULL
  password_hash varchar(72) NOT NULL             -- bcrypt cost 12
  email         varchar(254) NULL                -- 预留，本期不验证
  role          varchar(8)  NOT NULL DEFAULT 'user'   -- user | admin
  status        varchar(8)  NOT NULL DEFAULT 'active' -- active | banned
  token_version int         NOT NULL DEFAULT 1   -- +1 即让该用户全部 access token 立即失效
  banned_reason text NULL
  created_at / updated_at / last_seen_at

sessions                                  -- refresh token（不透明随机串，库里只存 sha256）
  id            bigserial PK
  user_id       bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE
  token_hash    char(64) UNIQUE NOT NULL
  user_agent    varchar(255) NULL
  ip            varchar(45)  NULL
  issued_at     timestamptz NOT NULL DEFAULT now()
  expires_at    timestamptz NOT NULL              -- 30 天
  revoked_at    timestamptz NULL

friendships
  id            bigserial PK
  requester_id  bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE
  addressee_id  bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE
  status        varchar(10) NOT NULL              -- pending | accepted | blocked
  blocked_by_id bigint NULL                       -- status=blocked 时记谁拉黑了谁
  created_at / updated_at
  UNIQUE (requester_id, addressee_id)

messages
  id           bigserial PK
  kind         varchar(4) NOT NULL                -- dm | room
  room_id      varchar(12) NULL                   -- kind=room 时必填
  sender_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE
  recipient_id bigint NULL                        -- kind=dm 时必填
  body         text NOT NULL                      -- 服务端已清洗（§7.3），长度 ≤ 2000
  created_at   timestamptz NOT NULL DEFAULT now()
  read_at      timestamptz NULL
  INDEX (kind, room_id, id DESC) / (kind, recipient_id, read_at)

rooms_meta                                -- 房间元数据；实时状态不入此表
  room_id        varchar(12) PK
  owner_user_id  bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE
  title          varchar(60) NOT NULL DEFAULT ''
  is_public      boolean NOT NULL DEFAULT false
  has_password   boolean NOT NULL DEFAULT false
  created_at     timestamptz NOT NULL DEFAULT now()
  closed_at      timestamptz NULL
  last_seen_at   timestamptz NOT NULL DEFAULT now()

room_snapshots                            -- §9 的"可丢"快照（周期性、闲时写）
  id            bigserial PK
  room_id       varchar(12) NOT NULL
  taken_at      timestamptz NOT NULL DEFAULT now()
  member_count  int NOT NULL
  max_depth     int NOT NULL
  playing_index int NOT NULL
  index_segments int NOT NULL
  payload       jsonb NOT NULL DEFAULT '{}'       -- 紧凑化后的拓扑摘要
  INDEX (room_id, taken_at DESC)

admin_audit
  id          bigserial PK
  actor_id    bigint NOT NULL
  action      varchar(32) NOT NULL                -- user.ban / user.role / room.close / ...
  target_type varchar(16) NOT NULL
  target_id   varchar(64) NOT NULL
  detail      jsonb NOT NULL DEFAULT '{}'
  ip          varchar(45) NULL
  created_at  timestamptz NOT NULL DEFAULT now()

schema_migrations
  version     varchar(32) PK
  applied_at  timestamptz NOT NULL DEFAULT now()
```

**迁移**：`internal/store/migrate.go` 用**有序、幂等的内嵌迁移步骤**（每步一个 `version` + 一段 DDL，执行记录写 `schema_migrations`），启动时自动补齐；DDL 只做加法，不做破坏性变更。
> 说明：用户要求"不手写 SQL"是针对**数据访问**；迁移的 DDL 无法用 ORM 表达，这里集中在一个文件里、不接受任何外部输入，不构成注入面。

**服务端日志**不落库：`internal/service/logring.go` 用有界环形缓冲（默认 2000 行）接住 `log` 输出，管理端按分页+字段白名单读取（避免日志注入进 DB、也避免无限增长）。

---

## 5. 认证与会话（混合式：审计要求的形态）

| 凭证 | 载体 | 有效期 | 撤销手段 |
| --- | --- | --- | --- |
| **refresh token** | `Set-Cookie: pr_refresh=…; HttpOnly; SameSite=Lax; Path=/api/auth`（不透明随机串，库中存 sha256） | 30 天 | 撤销该行 / 撤销该用户全部会话；**轮换 + 重放检测** |
| **access token** | JWT（HS256，`sub/role/tv/exp/jti`），仅存**内存 + sessionStorage** | 15 分钟 | `users.token_version` +1（改密/封禁/登出全部设备）即时失效 |

- **为什么不把 access token 放 localStorage**：审计判定该组合（无 TLS + 无 CSP + 存量 XSS 面）为账号单点失守；localStorage 只放**非敏感档案**（用户名/角色/邮箱/未读数）。
- 刷新：`POST /api/auth/refresh` → 校验 cookie 与 DB 行 → **旧行置 revoked + 发新行**；若收到已 revoked 的 token（重放）→ 撤销该用户**全部**会话并记审计。
- 封禁即时生效：`status=banned` + `token_version+1` + **主动断开该用户的全部 WS 连接**（房间与用户通道）。
- 登录/注册/刷新/建房/join 失败/私聊均有按 IP（+ 账号）维度的限速；登录失败文案统一，避免账号枚举。
- **WS 鉴权用一次性票据**：`POST /api/auth/ws-ticket`（需登录）返回 30 秒有效、单次使用的票据；客户端拼进 `?ticket=`。
  为什么不直接把 access token 放 WS URL：URL 会进 nginx access log、Referer 与浏览器历史。浏览器 WebSocket 无法自定义头，票据是标准解法。
  票据本身也会进 nginx access log —— 这是**有意的取舍**：它单次使用且 30 秒失效，泄漏价值远低于长期凭证；若将来要求更严，可改为"连接建立后首帧提交票据"。

---

## 6. 房间 × 账号

- `POST /api/rooms` **必须登录**（他们选的"建房必须登录"）→ 返回 `roomId` + **`hostToken`**（沿用 S-7）；房间写入 `rooms_meta`（owner/title/is_public）。
- 观众进房**不需要登录**（房间码即可）；带 ticket 时把连接绑定 `userId`，用于封禁拦截与"我的房间"。
- 服务端**不信任客户端自报的 `clientId`** 作为身份：`clientId` 只是"同一账号的多端标识"，身份以票据/账号为准（审计 must #1）。
- **公开房间列表**：实况取自内存（只有真的存在且有主播的房间），元数据（标题/公开性/房主昵称）取自 `rooms_meta`；只列 `is_public` 且**当前有主播**的房间（**含处于离线宽限期的房间**——它们仍可按码进入，列表里会标注"主播离线中"），分页 + 限速。列表**不返回**密码、成员明细、owner 的敏感字段。
- 列表上线后房间码不再是秘密：密码房仍以密码为准；主播位仍以 `hostToken` 为准（S-7）；管理端有"下架"权限。
- 房间关闭时更新 `rooms_meta.closed_at`；标题与昵称统一做字符白名单 + 去零宽/Bidi（§7.3）。

---

## 7. 社交：好友与私聊

### 7.1 好友
`POST /api/friends/requests`（发起）→ 对方 `accept`/`reject`。
**存储形态：一段关系只存一行**（`requester_id`/`addressee_id` 按登记时的角色写入）。三种状态的语义分别是：
`accepted` **对称**（任一方查询都能看到对方，不需要第二条边）；`pending` **有向**（只有 `addressee_id` 能 accept/reject）；`blocked` **有向**（`blocked_by_id` 记录是谁拉黑了谁，被拉黑方发不进消息也发不出请求）。
这样既不会出现"两条边不同步"的不一致，也能表达拉黑的单向语义。
拉黑：`POST /api/blocks` → `status=blocked`，**服务端强制**：被拉黑方发不进消息、好友请求被拒、列表互不可见（不是前端隐藏）。

### 7.2 私聊
- 用户通道 `/ws/user`（ticket 必填）：`friend-request` / `friend-accepted` / `friend-removed` / `blocked` / `dm` / `dm-read` / `presence`。
- 投递：服务端先校验**关系 + 非拉黑**；对方在线直投，否则只落库 → 登录时按未读拉取。
- 历史：`GET /api/messages?peer=<userId>&before=<id>`，分页 50；`read_at` 由客户端上报已读位置（`POST /api/messages/read`）。
- 限速：单对 20 条/分钟；正文长度 ≤ 2000；不允许空消息。

### 7.3 内容清洗（服务端统一入口，消息/昵称/标题共用）
1. 去控制符：零宽（`U+200B-200F`）、Bidi（`U+202A-202E`、`U+2066-2069`）、其他 `Cc` 类（换行保留为单行）。
2. 长度上限按字段（昵称 32 / 标题 60 / 消息 2000）。
3. **只做纯文本**：前端一律文本插值，禁止任何 HTML 注入 API（写进仓库规则与检查项）。

### 7.4 房间内聊天落库
`kind=room` 消息随房间信令广播，同时入库；进房时拉最近 50 条；保留 30 天（可配），后台清理协程负责。

---

## 8. 管理端

- 前端 `/admin` 独立视图；**所有** `/api/admin/*` 由服务端 `RequireAdmin` 保护（前端 `v-if` 只控显示）。
- 能力清单：用户列表/搜索/新建（可直接建管理员）/改角色/封禁解封/删除；房间列表（含未公开）+ 强制关闭 + 公开房下架；运行指标（在线用户/房间/连接/切片队列 + 现有诊断数据 + §9 的写队列指标）；服务端日志查看（环形缓冲，分页 + 字段白名单，**一律文本插值**）。
- 所有写操作写 `admin_audit`（谁、何时、对谁、改了什么、来源 IP）；高危操作前端二次确认。
- 开发者界面（诊断抽屉/拓扑徽标/切片工具/上传面板）对普通用户隐藏，仅管理员或 `?debug=1`（且该开关**只在 DEV 构建生效**）可见。

---

## 9. 异步入库（按你的要求：闲时写、不阻塞信令）

**两类策略，性质不同，不能混为一谈：**

| 类别 | 内容 | 策略 |
| --- | --- | --- |
| **事件类**（不可丢） | 私聊/房间消息、好友关系、用户与房间元数据、封禁与审计 | **单写协程 + 有界队列**；调用方"投递即返回"，失败重试（指数退避）；进程优雅关闭时 flush 到超时上限 |
| **快照类**（可丢） | 房间成员数/最大深度/播放进度/分片索引规模 | 周期 ticker（默认 30s）+ **跳过规则**：上一轮未完成 → 跳过；WS 发送队列积压超阈值 → 跳过；队列满 → **丢最旧**（快照不是真相，内存才是） |

**硬约束**：任何写库都不出现在 WS/REST 处理路径上（I4）；`usecase` 只向接口投递事件。
**可观测**：队列长度、丢弃计数、最近写入耗时暴露到管理端指标。

---

## 10. API 与协议扩展（新增面）

```
认证
  POST   /api/auth/register        POST /api/auth/login        POST /api/auth/refresh
  POST   /api/auth/logout          POST /api/auth/logout-all   GET  /api/auth/me
  POST   /api/auth/ws-ticket
用户
  PATCH  /api/me                   GET  /api/users/:id
好友
  GET    /api/friends              POST /api/friends/requests
  POST   /api/friends/requests/:id/accept|reject
  DELETE /api/friends/:id          POST /api/blocks
消息
  GET    /api/messages?peer&before           POST /api/messages/read
公开房间
  GET    /api/public-rooms    PATCH /api/rooms/:id/meta（房主改标题/公开性）
管理端（全部 RequireAdmin）
  GET/POST/PATCH/DELETE /api/admin/users[/:id]
  POST   /api/admin/rooms/:id/close|unpublish
  GET    /api/admin/metrics        GET /api/admin/logs        GET /api/admin/audit

WebSocket
  /ws       房间信令（游客可用；带 ticket 则绑定账号）
  /ws/user  用户通道（ticket 必填）
  新增信封：friend-request / friend-accepted / friend-removed / blocked / dm / dm-read / presence / rooms-changed
```

`POST /api/rooms` 响应新增（已有）：`hostToken`；join 信封已有 `host_token = 34`。

---

## 11. 审计 must 的落点（逐条）

| 审计要求 | 落点 |
| --- | --- |
| token 不进 localStorage | §5 混合式（refresh 入 HttpOnly Cookie，access 只内存） |
| 同批上 CSP + nosniff + frame-ancestors | 已实现（F-9）；管理端单独更严 CSP |
| 生产无调试钩子 / `?debug=1` 仅 DEV | 已实现（F-5） |
| 授权 100% 服务端（role 只控显示） | I3 + §8 |
| 昵称/标题/消息/日志：字符白名单 + 去零宽/Bidi | §7.3 |
| 管理端日志文本插值 + 字段白名单 + 分页 | §8 |
| 链接仅 http(s)（同源口径） | 已实现（F-1/F-2 safeLink） |
| 禁止 HTML 注入 API | 仓库规则 + **构建后 grep 门禁**（`v-html` / `innerHTML` / `outerHTML` / `insertAdjacentHTML` / `eval(` / `new Function` 任一命中即失败），并列为代码评审固定检查项 |
| WS 必须鉴权、不信客户端 clientId | §6 ticket + 身份以账号为准 |
| GORM 全参数化、禁拼接 SQL | §1 + 评审检查项 |
| 限速（注册/登录/刷新/建房/join/私聊） | §5 |
| 审计入库 + 高危二次确认 | §8 |

---

## 12. 分期与验收判据（每期独立可验收）

**一期：数据库 + 账号骨架**
- 交付：迁移可重复执行且幂等；注册/登录/刷新/登出/`me`；封禁即时生效（`token_version` + 断 WS）；建房绑定 owner；管理端最小骨架（用户列表只读 + 指标 + 日志查看）。
- 验收：新增 `test/script/verify-auth.mjs`（真实浏览器）：注册→登录→刷新→**重放旧 refresh 必须导致全会话撤销**→封禁后旧 access token 立刻失效→**游客仍能按房间码进房**（回归 I1 与"进房不必登录"）。

**二期：公开房间 + 管理端全量**
- 交付：公开列表（内存实况 ∪ 元数据）、标题/公开性、强制关闭、下架、审计、日志分页。
- 验收：`verify-public-rooms.mjs`（列表只含公开且有主播的房间；密码房不外泄信息）、`verify-admin.mjs`（非管理员访问 `/api/admin/*` 必须 403；每个写操作都有审计行）。

**三期：好友 + 私聊 + 房间聊天落库**
- 交付：`/ws/user`、好友请求/同意/删除/拉黑、离线未读、历史分页、房间聊天入库与 30 天清理。
- 验收：`verify-social.mjs`（A 发给 B 的私聊在 B 离线时落库、上线后未读正确；拉黑后服务端拒绝投递；断线重连不丢未读）。

**每期都必须重跑现有回归**：`verify-m2` / `verify-health` / `verify-room-resume` / `verify-ice` / `go test ./...`。

---

## 13. 已知风险与未决

1. **无 TLS**：refresh cookie 与 access token 仍是明文过网（你已选择暂不做 TLS）。cookie 暂不加 `Secure`；TLS 一上线即为所有 cookie 补 `Secure` 并缩短 refresh 有效期。
2. **单实例**：在线态与人数在内存，多实例需要 Redis（本期明确不做）。
3. **bcrypt cost=12** 在 1.6 GB / 2 vCPU 上登录约需 ~200ms；登录限速已按此设定，若实测过高可降到 11。
4. **房间销毁后的消息保留 30 天**为默认值，可配；删除用户时消息按其外键级联处理，好友关系同样级联（需要"仅删除个人信息、保留对方会话"时，改为软删除，本期不做）。
5. **公开列表让房间码不再保密**：已用"密码 + hostToken + 管理端下架"兜住；若未来要求更强，需要房间级"仅受邀可入"。
6. `PR_TRUSTED_PROXIES` 目前写死回环（审计残留项），多级代理需加配置项。
7. **管理员 MFA 本期不做**（审计建议项）：没有邮件/TOTP 基础设施，引入它等于新增中间件。本期替代控制是：强密码校验、所有管理操作入库审计、管理员数量可控、管理端接口严格 RBAC（非管理员一律 403，不靠前端隐藏）。
