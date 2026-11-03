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
- ~~头像/图片上传（用内置图标集，避免引入对象存储与外链直出风险）~~ —— **已落地**：对象存储（MinIO / S3 兼容）是 `internal/storage` 的唯一职责，`POST /api/uploads` 早已可用；头像见 §7.9（票 #74 覆盖层）与 §7.10（票 #75 裁剪器与审核）。
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
  deletion_requested_at timestamptz NULL         -- 票 #73：非空 = 在注销冷静期里（可空无默认值）
  avatar_url    varchar(512) NULL                 -- 票 #74：像素头像的**覆盖层**地址，NULL = 只用字母头
  avatar_changes_on    date NULL                  -- 票 #75：上一次改头像的**日期**（UTC 截日），NULL = 从没改过
  avatar_change_count  int NOT NULL DEFAULT 0     -- 票 #75：那一天改了几次（每日上限 5，见 §7.10）
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

follows                                       -- 关注边（wayfinder #56；ADR 0001）
  id          bigserial PK
  follower_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE
  followee_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE
  created_at  timestamptz NOT NULL DEFAULT now()
  UNIQUE (follower_id, followee_id)             -- 同一条边只能有一条（重复关注是幂等的）
  CHECK (follower_id <> followee_id)            -- 自关注在数据库侧就被挡住

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

favorites                                 -- 多态私人书签（wayfinder #48 / 票 #64）
  user_id     bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE
  target_type varchar(8) NOT NULL                 -- post | room | anime（带 CHECK）
  target_id   varchar(16) NOT NULL                -- 文本：放映室主键是房间码
  created_at  timestamptz NOT NULL DEFAULT now()
  PRIMARY KEY (user_id, target_type, target_id)   -- 三元组即幂等键
  INDEX (user_id, target_type, created_at DESC)   -- 列表按收藏时间倒序
  -- 刻意没有指向 posts / anime / rooms_meta 的外键：对象失效时条目要保留

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
- **官方 / 推荐区**（`GET /api/public-rooms?official=true`）：运营位，只列 `rooms_meta.is_official` 且当前仍在内存里的房间，按 `official_rank` 降序。标记由管理端 `POST /api/admin/rooms/:id/official` 设置或撤销（只 admin，普通房主不能自荐；无申请流）。**官方标记不改变房间的实时状态与进入规则** —— 密码房仍是密码房，只是它在官方区里有一个标识。
- 房间关闭时更新 `rooms_meta.closed_at`；标题与昵称统一做字符白名单 + 去零宽/Bidi（§7.3）。

---

## 7. 社交：好友与私聊

### 7.1 好友（**已退役**，见 7.1b）

> ⚠ **本节描述的是旧模型，票 #59 之后系统里不存在「好友请求」了。**
> 保留这段是为了解释存量数据长什么样（`friendships` 表里的 `pending` / `accepted` 行
> 仍然存在，迁移只做加法，不删行）。**新代码不得再产生或消费这两种状态。**

旧流程是 `POST /api/friends/requests`（发起）→ 对方 `accept`/`reject`，
存储形态「一段关系只存一行」（`requester_id`/`addressee_id` 按登记时的角色写入）。
三种状态的旧语义：`accepted` **对称**、`pending` **有向**（只有 `addressee_id` 能 accept/reject）、
`blocked` **有向**（`blocked_by_id` 记录是谁拉黑了谁）。

**现在只剩 `blocked` 这一种活状态**，且它的语义收紧了两处：

1. **拉黑 = 一个事务里的两件事**（`store.BlockUser`）：写/覆盖 blocked 行，
   **并删掉「拉黑方 → 被拉黑方」的 follow 边**，保留反向那条（票 42 §4）。
   为什么保留反向：拉黑是我对自己的约束，不是对他的惩罚 —— 解黑之后他关注我这件事
   不该被我单方面抹掉，我回关一下就能恢复互关。
2. **被拉黑方发不出关注**（不是"发不出好友请求"，那条路已经没有入口）：
   `store.CreateFollow` 对**两个方向**都返回 `ErrFollowBlocked`（`403 FOLLOW_BLOCKED`）。

拉黑仍是 `POST /api/blocks`，**服务端强制**：双方发不进私聊、双向不能新关注、
好友/关注/粉丝名单里互不可见（不是前端隐藏）。

### 7.1b 关注（wayfinder #56；ADR 0001「好友 ≡ 互关」）
关注是**单向、免同意**的有向边，存 `follows` 表（一对用户可有两条边：A→B 与 B→A 各一行）。
「互关」因此是**派生**事实（两条边同时存在的合取），不是要维护的状态 —— 免去"忘了更新互关标记"的漂移。
「好友」是「互关」的产品同义词；好友请求流程（`pending`）随 ADR 0001 **已退役**（票 #59）。
拉黑存续期**双向**禁止新关注（票 42 决议）；拉黑事务（删拉黑方的 follow、保留反向 follow）已落地在 `store.BlockUser`。
can_dm(A,B) := mutual_follow(A,B) ∧ NOT blocked_either(A,B) —— 唯一的实现落在 `store.CanDM`。
端点：`POST /api/follows`（关注，201）/ `DELETE /api/follows/:userId`（取关，204）/
`GET /api/follows/:userId`（我与 TA 的关系 + TA 的关注/粉丝数）/
**`GET /api/follows?tab=mutual|following|followers`**（名单；好友列表就是 `tab=mutual`）。

### 7.1c 关注流（票 #66；决议 #52）
`GET /api/posts?sort=following` —— **sort 枚举扩展，不新建子资源路由**（关注流是"同一批帖子换一种挑法"，
不是另一种资源）。只看我关注的人发的帖：普通帖与板块主题串**都算**（关注的是人，不是帖子类型），
严格时间倒序，不做热度加权（时间线永远可预测；热度加权有空窗期，空窗期里用户以为坏了）。

过滤口径是 `internal/store/flow.go` 的**唯一实现**，刻意分成两层：

- `applyFollowingScope(db, viewerID, authorCol)` —— 「要不要只挑这一批」。
  `viewerID <= 0` 返回 `store.ErrFlowNeedsViewer`，**不返回空列表**：匿名视角下"我关注的人"
  没有意义，空列表会把"没登录"伪装成"你还没关注谁"，界面于是给出错误引导。
- `applyAuthorVisibility(db, viewerID, authorCol)` —— 「能不能看见」：作者未封禁 ∧ 双向无拉黑。
  匿名时跳过拉黑那一条（`requester_id = 0` 匹配不到任何行），封禁对匿名同样生效。

两层分开是刻意的：合成一个函数的话，他人档案页（#68）的帖列表就只能复用后半段，
注销隐藏（#51）又要再写第三遍。

**匿名请求 `sort=following` 一律 401**：handler 层先判一次（`CodeUnauthorized` +
「关注流需要登录后查看」），store 层再用 `ErrFlowNeedsViewer` 兜一次 —— 后者防的是
"别的入口绕过 handler 直接调 store"。用例 `TestFollowingFlowRejectsAnonymous` 与
`TestFollowingFlowRequiresViewer` 各钉一头。

前端：`/posts` 第三个 pill，选中态**只读 URL**（`client/src/utils/postsRoute.ts` 是键名唯一源，
`sort` 的默认值不写进 URL）。`switchSort` 只 `router.replace`，不碰内存 ref ——
两份状态一定会漂，症状是"后退键回到上一个 pill 但列表还是新的那个"。
**游客点「关注」pill 不被弹走**（决议 #52 §6「读动作被弹走最烦」）：就地渲染登录引导，
`?redirect=` 带回当前 URL，登录回来还停在关注流。0 关注空态深链 `/friends?focus=search`
（#50 已定搜索是页头常驻**状态**、不依附任何 tab，所以不带 `tab=`）。

**已退役的端点（票 #59）**：`GET /api/friends`、`GET|POST /api/friends/requests`、
`POST /api/friends/requests/:id/{accept,reject}`、`DELETE /api/friends/:id`。
它们**不在路由表里**了 —— 与 `/api/auth/forgot`、`/api/auth/reset` 同一先例，
落到 `NoRoute` 的 `/api` 前缀拦截，返回 404 + 中文错误（**明确失败，不是静默成功**）。
「好友列表」没有替代端点：它就是 `GET /api/follows?tab=mutual`。

### 7.2 私聊
- 用户通道 `/ws/user`（ticket 必填）：`friend-removed` / `blocked` / `dm` / `dm-read` / `presence`。
  （`friend-request` / `friend-accepted` 两种帧随票 #59 一起退役：产生它们的用例已经不存在，
  线上保留 `FriendEvent` 这一帧的编码只是为了不改 protobuf —— 加字段比删字段安全，
  而删字段要先确认没有旧客户端在等它。**当前前端没有任何消费者**：`createUserChannel` 全仓无调用点。）
- 投递：服务端先校验**关系 + 非拉黑**；对方在线直投，否则只落库 → 登录时按未读拉取。
- 历史：`GET /api/messages?peer=<userId>&before=<id>`，分页 50；`read_at` 由客户端上报已读位置（`POST /api/messages/read`）。
- 限速：单对 20 条/分钟；正文长度 ≤ 2000；不允许空消息。

### 7.3 内容清洗（服务端统一入口，消息/昵称/标题共用）
1. 去控制符：零宽（`U+200B-200F`）、Bidi（`U+202A-202E`、`U+2066-2069`）、其他 `Cc` 类（换行保留为单行）。
2. 长度上限按字段（昵称 32 / 标题 60 / 消息 2000）。
3. **只做纯文本**：前端一律文本插值，禁止任何 HTML 注入 API（写进仓库规则与检查项）。

### 7.4 房间内聊天落库
`kind=room` 消息随房间信令广播，同时入库；进房时拉最近 50 条；保留 30 天（可配），后台清理协程负责。

### 7.5 私人收藏（wayfinder #48 / 票 #64）
收藏是**私人书签**，不是公开表态 —— 这是它与点赞的分界线：点赞计入 `posts.like_count`（人人可见），
收藏**不计任何公开数**，也**没有**公开收藏列表；他人档案里**不渲染**收藏块。

存储是**多态单表** `favorites(user_id, target_type, target_id, created_at)`：

- 主键是**三元组** `(user_id, target_type, target_id)`，于是 `PUT` 天然幂等（双击、重试、网络重放都不漂）；
- `target_type` 有 `CHECK IN ('post','room','anime')`；
- `target_id` 是 `varchar(16)` 而**不是** bigint —— 放映室的主键是房间码（`rooms_meta.room_id varchar(12)`），
  一个多态列装不下"数字 id 或房间码"两种形态；
- **没有任何外键**指向被收藏对象。这是故意的：对象失效时条目要**保留**并显示失效态，
  外键的 `ON DELETE` 会替用户把书签删掉 —— 规格明确禁止静默删除。
  跨表完整性因此由服务层校验：收藏一个不存在的对象返回 404，**不写孤儿行**。

粒度：番剧收藏的是**目录项本身**（＝追番）；番剧板块里的主题串就是帖子，由帖子收藏覆盖，不另计。

失效语义：放映室关闭/未公开、番剧下架、帖子被删 → 条目保留、列表给 `valid:false`（灰显 +「已失效」+ 不可点），
由用户自行移除。

端点（全部需登录）：

```
PUT    /api/posts/:id/favorite        DELETE /api/posts/:id/favorite
PUT    /api/animes/:id/favorite       DELETE /api/animes/:id/favorite
PUT    /api/rooms/:id/favorite        DELETE /api/rooms/:id/favorite
GET    /api/favorites?type=post|room|anime&before=&limit=
```

写端点回 `{targetId, targetType, favorited, created|removed}`（幂等：重复 PUT 时 `created:false`）；
`GET` 按收藏时间倒序，`before` 传上一页最后一行的 `cursor`（`<unixMicro>:<targetId>`，行值比较分页）。

列表与详情**与 liked 同一批查询**回填 `favorited`（`store.favoritedPostIDs` 是唯一口径，禁 N+1）；
`Post.Favorited` 是只读列（`gorm:"->"`），**没有** `favorite_count` 字段 —— 想加它之前先想清楚"收藏是私人的"。

---

### 7.6 档案双路由与 `/me` 管理台（wayfinder #47 / 票 #68 / 票 #69）

**两条地址，两种视角**：

- `/user/:id` —— **公开橱窗**。对谁都一样，包括本人访问自己的 id（决议 #47：
  旧实现在同一地址里按 `isSelf` 切两套视图，症状是"同一个链接，登录前后是两个页面"，
  分享与后退都说不清）。块序固定为：名片 → 关系动作区 → TA 的帖子 → TA 的放映室 →
  关注数/粉丝数（**纯数字不可点**）→ 收藏块**不渲染**（收藏是私人书签，票 #64）→
  不存在/被封禁统一 404 空态。本人访问时额外一条提示 + 去管理台的链接。
- `/me` —— **本人的管理台**。四个分区，`?tab=overview|relations|favorites|settings` 进 URL
  （键名唯一源 `client/src/utils/meRoute.ts`，与 `dmRoute`/`friendsRoute`/`postsRoute` 同族）：
  概览（资料编辑 + 我的帖子 + 我的放映室）、关系（四份名单 + 黑名单入口）、
  收藏（票 #65，见下）、设置（票 #71，见下）。顶栏头像的落点是 `/me`（票 #69 起），
  头像位的"点亮"语义随之扩到 `/me`（`utils/navSection.ts` 的 `isSelfProfile`）——
  从 `/me` 点「查看我的公开主页」跳到 `/user/<我>` 时头像位若灭掉，
  用户会以为跳出了账号上下文。

**`/me?tab=settings` 账号设置（票 #71；决议 #51）**：

- **两条改密门并存，互不替代**。记得密码 → 这里，`PUT /api/me/password`
  收 `{currentPassword, newPassword}`，**仅当前密码**这一道门（`service.ChangeMyPassword`）；
  忘了密码 → 登录页的邮箱验证码（`/api/auth/password/otp` + `/password/otp/reset`），**一字未动**。
  已知取舍：劫持到有效会话即可改密，但找回密码那条路仍需要邮箱码，构成第二道防线 ——
  这是决议 #51 明确接受的风险，不是疏漏。
- **成功后的三步是判据本身**：`replacePassword`（`SetPasswordHash` → `RevokeAllSessions`
  → `BumpTokenVersion`）把其余会话全蹬掉，然后**重新 `UserByID` 读回用户再 `issueSession`**。
  不重读就会拿旧 `token_version` 签发一枚一出生就失效的 token，症状是
  「改密成功但当前标签页立刻掉线」。端点回 **200 + 完整新会话**（不是 204），
  并把新 refresh 写进 Cookie —— 当前设备因此保持在登录态。
- **错误映射**：错当前密码 → 400 `PASSWORD_MISMATCH`（**不是 401**：前端把 401 当会话过期
  会直接踢去登录页，而这里用户明明在线、只是打错了一栏；也不与 `LOGIN_FAILED` 共用码 ——
  登录语境要模糊、设置页语境要具体）；少填一栏 → 400；弱口令 → 400 + 策略原因；
  未登录 → 401；封禁账号 → 403。
- **走幂等键族**（scope `me.password`，与 `password.otp.reset` 分开 —— 否则同一把键
  在两条门之间互相命中）。注意幂等重放必须用**新口令重新登录拿到的** token：
  改密把 `token_version` 加一，第一枚 token 在第二次请求时已被 `RequireAuth` 判失效，
  那是正确的安全行为、与幂等无关。
- **退出所有设备**复用 `POST /api/auth/logout-all`（`token_version+1` + 撤销全部会话）。
  ⚠ 该端点挂 `RequireAuth`，**必须带 Bearer**；前端 `api/auth.ts` 的 `logoutAll()`
  曾漏传 token（调用方 catch 里照样 `clearSession()`，症状是「页面看起来退出了、
  服务端会话一个都没撤销」），它长期没暴露是因为那个函数从写下来起**没有调用方**，
  本轮接界面时才被真机判据 S9 抓到。
- **账号只读信息**六行：用户名 / 昵称 / 加入时间 / 角色 / 邮箱 / 邮箱验证态。
  加入时间用 `ui/RelativeTime.vue` 的 **`absolute` prop**（可见文案也是 `YYYY-MM-DD HH:mm`）——
  这个字段要的是"哪一天注册的"，对一个刚注册的账号显示「刚刚」恰恰最没信息量；
  之所以加开关而不是让页面自己格式化，是因为全站时间格式化只有
  `utils/relativeTime.ts` 一份实现，页面自己拼日期就等于开第二个出口。
- 退出所有设备用**页内确认条**（`role="alertdialog"`）而不是 `window.confirm`：
  真机脚本里后者既不可读也不可点，而"退出所有设备"是不可撤销的破坏性动作，
  必须有人能验收它。

**作者内容的两个只读端点**（票 #68，均**免登录**）：

```
GET /api/users/:id/posts?limit&offset   → { items: postView[], hasMore }
GET /api/users/:id/rooms?limit&offset   → { items: { roomId, title, createdAt, lastSeenAt }[], hasMore }
```

- 选**子资源**而不是 `GET /api/posts?authorId=`：两者准入不同（一个是公开档案的一部分，
  一个是帖子分区）、前缀已存在（`/api/users/:id` 本来就有）、查询意图不同
  （"这个人写过什么" vs "帖子列表按作者筛"）。规格 #55 第 116 行允许二选一，**不双开**。
- 帖复用 `postView`（含 `favorited`），所以前端同一个 PostCard 直接吃。
- 房**刻意只回四个字段**：没有成员数、主播在线、房主名 —— 那些是内存实况，
  属于公开房列表（`GET /api/public-rooms`），不是"这个人开过哪些房"的历史事实。
  口径与公开房列表一致：`is_public = true AND closed_at IS NULL`。
- 可见性走 `store.applyAuthorVisibility`（未封禁 ∧ 双向无拉黑）—— 与关注流**同一个实现**，
  所以"被拉黑之后看不到对方的帖"在档案页与关注流上必然一致。
  **拉黑不改公开内容的可读性**（票 #42 §4）：档案页上的帖会消失，但那篇帖的详情页仍然 200。
- 不存在与被封禁的账号一律 404 同一个空态（防存在性探测）。

**`/me?tab=favorites` 收藏区块**（票 #65）：

- 三份书签按类型分 tab：`?type=post|anime|room`（键名唯一源同上；默认 `post` 不写进 URL）。
  分类型而不是混排：三种对象的主键形态不同（前两个自增 id，放映室是房间码），
  混排需要一个跨类型的时间序，而"这一页到底是哪一类"会说不清。
- 倒序 + **游标分页**（`before` = 上一页最后一行的 `cursor`），不是页码：
  收藏会在使用中增删，页码分页会因为"前面少了一条"而漏行。
- 失效条目**保留**、灰显、标题不是链接、带「已失效：<原因>」、旁边一个「移除」；
  失效原因按类型给不同文案（放映室"已关闭或不再公开" / 番剧"已下架" / 帖子"已被删除"）——
  一句笼统的"已失效"没法让人判断该不该清理。
- 空态按类型给不同的人话 + 去对应分区的引导（决议 #44 禁止裸「暂无数据」）。
- **只有这里能看到收藏**：`/user/:id` 上（本人与他人）都没有收藏块。
- `ui/ActionBar.vue` 的 `favoriteCount` prop 在票 #65 被删掉 —— 它从 #63 起就恒为 0、
  页面从没传过值，留着等于在源码层面暗示"这里本该有个数"。计数位节点仍保留（空 + 隐藏），
  那是 `verify-authorrow` 的结构契约。

### 7.7 我的内容管理（票 #70）

`/me` 的概览分区里，本人的帖子与放映室是**可管理**的，与 `/user/:id` 上的只读橱窗不同。

**删帖** `DELETE /api/posts/:postId`（需登录，204）：

- **授权在服务端**（I3）：判据是 `posts.author_id == 请求者`，落在 store 层
  （`store.DeletePost` 返回 `ErrPostForbidden`），handler 只把它翻译成 403。
- 403 与 404 **必须分开**：`DELETE ... WHERE id = ? AND author_id = ?` 一条 SQL 写不出这个语义 ——
  "帖子不存在"与"不是你的帖子"的 `RowsAffected` 都是 0，而前者该回 404、后者该回 403。
  所以先查 `id, author_id`，再判、再删。
- **硬删，不软删**：用户点"删除"之后正文还躺在库里是欺骗。真正需要软删的是账号注销
  （那里软删的是 `users` 行，见票 #73）。
- 子行靠 `ON DELETE CASCADE`（`post_likes` / `post_comments`），**不手写 DELETE** ——
  手写会漏掉将来新增的子表，症状是"帖删了、评论还在"且没有任何报错。
- **收藏条目刻意保留**：`favorites` 表没有外键指向 `posts`（票 #64 的决定），
  帖子删掉之后收藏行还在，收藏列表里显示"已失效：这篇帖子已被删除"，由用户自己移除。
  `store.TestDeletePostKeepsFavoritesEntry` 把这个例外钉在 store 层，
  防止以后有人"顺手补个外键"。
- 前端 `AuthorPostsBlock` 在 `self` 为真时给每条帖一个「删除」：**乐观移除 + 失败插回原位置**；
  404 例外地**不**插回（服务端已经没有它了，插回去等于显示一条不存在的帖）。

**我的放映室** `GET /api/rooms/mine?limit&offset`（需登录）→ `{ items, hasMore }`，
每行 `{ roomId, title, isPublic, isClosed, hasPassword, createdAt, lastSeenAt }`：

- **房主全部房**，含 `is_public = false` 与 `closed_at` 非空的。与 `GET /api/users/:id/rooms`
  （对外橱窗：只列已公开且未关闭）**口径互补**：他人页是"别人能看到的"，这一条是"我拥有的"。
  房主是唯一需要据此重新开放那间房的人，若复用公开房的过滤条件，
  症状是"我把房关掉了，它就凭空消失了"。
- store 层对应 `ListRoomsByOwner`（不过滤公开性与关闭态），注释明确**只该被"我的"路径调用**。
- 为什么是 `/api/rooms/mine` 而不是 `?owner=me`：身份只能来自 token，
  能传 owner 的查询串立刻引出"能不能传别人的 id"。代价是房间码不能叫 `MINE`
  （`^[A-Z0-9]{4,12}$` 技术上合法），与"`/api/users/search` 吃掉 id=search"同族，
  属已知可接受边界。
- 前端 `MeRoomsBlock` **刻意不复用 `AuthorRoomsBlock`**：那是对外橱窗，
  连 `isPublic`/`isClosed` 两个字段都没有，而这一页要给「私密」「已关闭」徽章。
- 这是第三个开关域之外的第九个：`VITE_MOCK_ROOMS`（`/api/rooms/mine` 是房主资产清单，
  不能挂在 `official` 下面 —— 官方区是运营位）。

### 7.8 账号注销（票 #73；决议 #51 §5）

**语义是「打一列标记 + 后台清扫真删」，不是 `status = 'deleted'`。**
冷静期内本人必须**能登录**才能撤销，而 `status` 同时是登录门与可见性门
（`isValidStatus` 只认 `active`/`banned`，`users` 的 CHECK 也钉着这两个值）；
把它改成第三态会连带改掉登录判据，症状是"注销之后再也登不进来，于是撤销不了"。

**数据**：`users.deletion_requested_at timestamptz NULL`（迁移 `0018_deletion_requested`，
`ADD COLUMN IF NOT EXISTS`）。这一列就是全部状态：发起时写、撤销时清空、到期据此删。
刻意**不**加 `deleted_at`/`is_deleted` 第二列 —— 两列都非空会造出"到底以哪个为准"的无语义中间态。
**可空且无默认值**是语义的一部分：`DEFAULT now()` 会让每个新账号一出生就在冷静期里。

**三个端点**（都需登录，各有独立幂等 scope）：

| 端点 | 作用 | 要点 |
| :--- | :--- | :--- |
| `GET /api/me/deletion` | 读状态给横幅 | `{ pending, requestedAt, deadline }`；`deadline` 由服务端算 |
| `POST /api/me/deletion` | 发起 | 收 `{currentPassword}`；scope `me.deletion.request` |
| `DELETE /api/me/deletion` | 撤销 | **不要密码**；scope `me.deletion.cancel` |

- **发起要当前密码 + 前端勾选确认**：密码与改密同一条理由（会话被劫持时，
  攻击者不该能一键删号）；勾选是「我已知晓数据将在 30 天后永久删除」的明确同意。
  确认走**页内确认条** `role="alertdialog"`，不是 `window.confirm`（#28 样板）。
- **撤销不要密码**：撤销是降低风险的方向，加门只会让"想反悔的人反悔不了"。
- **幂等且不延期**：已在冷静期里再发起，返回**原起点**。否则用户每刷一次页面就多 30 天，
  永远到不了期 —— 这个缺陷是静默的（页面上一切正常，只是永远不删）。
- 撤销时"没申请过"回 **409 `DELETION_NOT_PENDING`**：请求体没错，是状态不一致，
  回 400 会让人去改请求。
- **冷静期内不清会话、不动 `TokenVersion`**：蹬掉会话的直接后果是横幅上的「撤销」
  永远点不到（401），30 天后数据真删。

**可见性**（对外**立即**全隐藏，不必等到期）：

- 唯一源是 `internal/store/visibility.go`：
  `visibleAccountCondition = "status = 'active' AND deletion_requested_at IS NULL"` +
  `applyVisibleAccount(db, alias)`；署名占位是 `maskedDisplayNameSQL(alias)`
  （`CASE WHEN ... IS NULL THEN display_name ELSE '已注销用户' END`）
  与常量 `DeletedUserDisplayName = "已注销用户"`。
- 这条谓词管**列表**：搜索、三份名单（互关/关注/粉丝）、粉丝红点、作者帖流。
- **单点查询不走它**，必须各自补：`handler/follows.go` 的关注与查关系两处、
  `store.SendDM` 的收件人判断。漏掉的症状分别是"档案 404 了但拿着 id 还能关注 TA"
  和"还能往一个已注销的账号发新消息"——两者都会写出指向隐形账号的新行，
  而且界面上没有任何报错。
- 公开档案 404 与"用户不存在"**同一个响应**（防存在性探测，与封禁合流）。
- **历史保留、只读不回**：私聊历史与帖子正文都还在（另一方的对话权利不该被删号剥夺），
  只有署名脱敏。

**到期真删**：`internal/service/deletion_sweeper.go` 的后台协程，
`time.NewTicker(PR_DELETION_SWEEP_INTERVAL)` 驱动，每次按 `now - PR_DELETION_GRACE`
作 cutoff 调 `store.PurgeExpiredDeletions`（批 200，循环到返回 0）。
**只能被清扫协程调用**（I4）：它不出现在任何请求路径上，`AccountStore` 窄接口里也
**刻意不含**它 —— 账号用例不该有"删号"能力。外键 CASCADE 带走帖子/私聊/关注边/收藏。

**前端**：`/me` 顶部横幅（分区条之上、所有分区之外 —— 横幅说的是"整个账号要没了"，
与当前分区无关）+ 设置区「注销账号」危险区。
横幅与设置区都读接口回的 `deadline`，**不在前端算冷静期长度**（那是服务端配置，
复制一份就是第二份真源）。`stores/auth.ts` 的 `syncDeletionFlag()` 是
`profile.deletionRequestedAt` 的**唯一写入点**（与 `setUnread` 同一条纪律）；
`MePage.vue` 靠 watch 这个标志位去拉 `GET /api/me/deletion`，
而不是靠组件 emit —— 标志位变了就说明状态真变了，不管是谁改的、在哪个标签页改的。

**验收**：`test/script/verify-account-deletion.mjs`（真后端；14 条判据 + 可选 `--purge`）。
`--purge` 要求实例把 `PR_DELETION_GRACE` 压到分钟级、`PR_DELETION_SWEEP_INTERVAL=1s`，
且"行真被删了"的证据是**同名账号能重新注册**，只看 404 分不清"标记着"与"删了"。

### 7.9 头像覆盖层（票 #74；决议 #54）

**头像只是覆盖层，不是身份的另一份真源。** `utils/avatar.ts` 的 `avatarOf()`
（首字母 + 名字哈希取色）始终是本体，`avatar_url` 有值才盖上去 —— 所以"没有头像"
与"头像加载失败"在界面上是同一件事：回到那个有辨识度的色块 + 字母。

**降级三态**（缺一条就会看到破图或白圈）：

| 情形 | 行为 |
| :--- | :--- |
| `avatarUrl` 为空 / `null` | 不渲染 `<img>`，只有字母头 |
| 图片解码失败（`onerror`） | 把该 URL 记进 `utils/avatarImage.ts` 的模块级 `failedAvatarUrls`，组件改回字母头 |
| 本次会话里已经失败过 | 直接不渲染 `<img>`，**不再重试** |

那个 Set **必须是 `reactive()`**：`has()` 是在 computed 里被读的，普通 `Set`
加进去不触发重渲染 —— 症状是"失败后永远空白"，且只在真机上才看得见。
刷新页面清空（新会话值得再试一次）。

**数据**：`users.avatar_url varchar(512) NULL`（迁移 `0019_avatar_url`，
`ADD COLUMN IF NOT EXISTS`）。**可空且无默认值**是语义的一部分：NULL 同时表示
"从没设置过 / 被管理端重置 / 注销清零"。**没有新表、没有新开关域。**

**零新端点**：上传走现成的 `POST /api/uploads`（拿 `{url}`，201），再
`PATCH /api/me {avatarUrl}`。那个 `PATCH` 的**三态**是这张票最容易写错的地方：

| 请求体 | 含义 |
| :--- | :--- |
| 没有 `avatarUrl` 这个键 | 不改头像（只改昵称） |
| `"avatarUrl": null` | 清除 |
| `"avatarUrl": "<url>"` | 设置为该地址 |

实现必须**先 `c.GetRawData()` 解成 `map[string]json.RawMessage` 判键**、再解结构体：
`ShouldBindJSON` 会把 body 读走，反过来的顺序会让"键缺席"与"null"不可分辨。
两个字段都没给 → 400（不是静默成功）。**头像先落库、昵称后落库**：两者都可能失败，
更可能失败的那个先做，就不会留下"昵称改了头像没改"的半成功。返回体是 `{"user": …}`。

**URL 即版本**：每次上传都是新随机 key，换头像就是换 URL，旧值直接替换；
**同 key 覆盖明确否决**（浏览器缓存会把旧图当新图显示）。旧对象走
`AsyncJobSink.SubmitJob` 异步删（I4），**删失败只记日志**——事实上传已经成功，
因为一次存储抖动就让用户重传是错的。**同 URL 再设一次是幂等的**，不会去删它自己。

**只验不重编码**（go.mod 零图像依赖，决议 #54 §1）。安全因此全落在
"这个地址是不是本桶的合法对象"上，而不是"图片内容是否安全"：

- `storage.OwnsURL(u)`：前缀必须**恰好**是 `publicURL("")`（**不是**"包含 `/bucket/`"——
  否则 `https://evil.com/x/pr-files/...` 会通过），余下交给
- `isObjectKey(key)`：3 段、年 4 位数字、月 2 位数字、stem **恰好** 32 位小写 hex、
  扩展名命中 `allowedImageTypes`（JPEG/PNG/GIF/WebP，**显式禁 SVG** —— SVG 是 XML，
  可内嵌 `<script>`，同源托管即存储型 XSS）。
- 判的是**权限**不是**状态**：查对象是否存在要一次网络往返，而且"先置空再删对象"
  的顺序会让它必然失败。
- `PublicBaseURL` 带不带尾斜杠都要通过（运维很容易写成 `https://cdn.example.com/`，
  不 TrimSuffix 会导致"自己发的 URL 全被判非法"）。

**未配 `PR_STORAGE_*`** 时带值 `PATCH` 回 **503**（`CodeAuthUnavailable`，
不是 400 —— 请求本身没错，是部署没开）；设置区据此改用 `CapabilityNotice`，
而不是让用户点了没反应。探测走 `api/avatar.ts` 的 `probeAvatarUpload()`，
**判据是状态码不是响应体**：那条路由只注册了 POST，空请求在已注册时会于解析
multipart 之前返回 400（不碰存储、不写任何东西）。

**显示层**：`ui/AvatarImage.vue` 一层 `<img>`，`object-fit: cover` 是"服务端不重编码"
的执行点（非方图不会拉伸变形）；`alt=""` + `aria-hidden="true"`（装饰性，无障碍名
由外层容器给）。**容器必须自带 `position: relative; overflow: hidden`** ——
覆盖层是 `position: absolute; inset: 0`，漏了圆角裁剪会让它溢出圆外。

**入口只在 `/me?tab=settings`**（`MeAvatarBlock.vue`）；顶栏与别人看到的档案页
**不加**编辑态 —— 顶栏头像的落点是 `/me`，管理动作集中在一处（票 #69 的纪律）。

**验收**：`test/script/verify-avatar-overlay.mjs`（15 条，**不需要后端**，
mock 域 + 自起静态服务器即可：空态 / 有值态 / onerror 回落 / 会话内不重试 /
切分区再回来 / 本人视角 / 四处源码级）。裁剪器与频控、人机验证、管理端重置见 §7.10。

### 7.10 头像裁剪、每日额度与管理端重置（票 #75；决议 #54 §2/§3）

§7.9 说的是"地址怎么落地"，这一节说的是"那张图是谁裁的、能改几次、违规了怎么下架"。

**裁剪在浏览器里做，服务端不碰像素。** `client/src/utils/avatarCrop.ts` 是纯函数
（无 DOM，Node 原生类型擦除就能测）：`coverScale` 算"短边铺满"的比例（**cover 不是
contain** —— contain 会留白边），`drawnSize` 是画布上图片的实际尺寸，`clampOffset`
把取景位移夹回"图片必须盖满裁剪框"，`cropRect` 算出要画哪一块**源像素**，
`pickAcceptableBlob` 在若干档压缩结果里挑第一个 ≤1 MiB 的。`AvatarCropDialog.vue`
的画布预览与导出**共用同一组输入** —— 两处各算一遍取景，迟早会漂成"看到的和传上去的
不一样"。导出固定 **512×512**、JPEG 或 WebP（`toDataURL('image/webp')` 探一次
WebP 支持，不支持退回 JPEG）；JPEG 无 alpha，导出前先铺白底，否则透明区会变黑。
按 `QUALITY_STEPS` 逐档压，压到最低质量仍超标就报「压到最低质量仍有 X MiB」——
**不硬传超限文件**，那只会让用户等到一个 413。

单测 `client/test/avatar-crop.test.mjs`（14 条）里最值钱的是那 **81 组
zoom×offset**：断言裁剪框永远落在源图内。这个性质靠读代码看不出来，但越界一像素
就是"导出的图右侧有一条透明带"。

**频控落在 `users` 上**（迁移 `0020_avatar_change_quota`），每人每日 **5 次**：

| 列 | 语义 |
| :--- | :--- |
| `avatar_changes_on date NULL` | 上一次变更的**日期**，NULL = 从没改过 |
| `avatar_change_count integer NOT NULL DEFAULT 0` | 那一天已经改了几次 |

为什么不单开计数表：单开就要"先查后写"两次往返，中间有并发窗口。为什么不放内存
令牌桶：重启清零、多实例各算各的。日期 + 计数两列还顺手省掉了定时任务 ——
下次写入发现日期不是今天就归零。**代价是按 UTC 截日**，也就是北京时间 08:00 才重置；
这是刻意选的（部署时区不该改变产品口径），改它要先改 `store.dateOnly`。

判并扣是**一条 UPDATE**：

```sql
UPDATE users
   SET avatar_changes_on = ?,
       avatar_change_count = CASE WHEN avatar_changes_on IS DISTINCT FROM ? THEN 1
                                  ELSE avatar_change_count + 1 END
 WHERE id = ?
   AND (avatar_changes_on IS DISTINCT FROM ? OR avatar_change_count < ?)
```

`IS DISTINCT FROM` 不能换成 `<>`：NULL 上 `<>` 求值为 NULL，条件恒假 ⇒ **新账号
第一次改头像就 429**。这条有个专门的哨兵（`...FirstEverIsNotBlocked`）。

**口径四条**：

| 情形 | 扣不扣 | 为什么 |
| :--- | :--- | :--- |
| 同一个 URL 再设一次 | **不扣** | 幂等短路在扣额度之前，用户刷新/双击不该吃额度 |
| `{"avatarUrl": null}` 清除 | **不扣** | 回落到字母头是**降风险**方向，与"撤销注销不要密码"同一条判断 |
| 跨日 | 计数自动归零 | 见上（UTC 截日） |
| 写库失败 | 已经扣了 | 反过来会"两边都成功、两边都去扣"；白吃一次额度的代价小于超额 |

超限回 **429 `RATE_LIMITED`**（不是 403）："今天用完了"不是"没权限"，而且 429
天然承载"稍后重试"。文案里带次数与「明天再试」，前端不需要再解释一遍。

**人机验证是链路上的第二道门**：裁剪确认之后、上传之前弹 `CaptchaDialog`
（与注册/找回密码同一个组件，**题面与校验都在前端**，定位是摩擦不是安全边界）。
这一步的存在理由不是防机器刷 —— 每日 5 次已经压住了量 —— 而是让"换头像"这件事
不至于像改个昵称那样一键完成。

**管理端重置**：`PATCH /api/admin/users/:id {resetAvatar: true}`。收**布尔**不收
`avatarUrl` —— 管理员不该能把任意地址塞进别人的头像列。实现是**先置空、再异步删对象**：

```
UpdateAvatarURL(target, nil)      ← 置空。这一步让"所有人立刻看到字母头"成立
  ↓ AsyncJobSink.SubmitJob
Storage.Remove(旧 URL)            ← 删失败只记日志，不回滚
```

两步分开的意义就在"删失败"那一支：桶里那张图还在，但没人看得见它了。
**刻意不前置判"有没有配对象存储"** —— 部署没开存储时，管理员恰恰更需要能下架
违规头像（那时图在别处，置空仍然是唯一的处置手段）。本来没头像也回 200，
审计里记 `unchanged: true`（幂等，重复点不会写两条不同的审计）。

**前端入口**只在 `/me?tab=settings` 的 `MeAvatarBlock.vue`，链路是
「更换头像 → 裁剪 → 人机验证 → 上传 → PATCH」。两次往返之间**不留半状态**：
上传成功而 PATCH 失败时提示「图片已经传上去了，但没能保存到账号」并给「重试保存」，
**重试不会再传一遍**（`pendingUrl` 记着那个地址）。取消人机验证不报错，只说
「已取消，头像没有改动」。

**验收**：`test/script/verify-avatar-crop.mjs`。C 组 11 条判裁剪器本身（mock 域即可，
**不需要后端**），S 组 14 条打真后端（`--server`）：外链 400、只改昵称不动头像、
同地址重复不扣额度、累计 5 次后第 6 次 429、额度满后清除仍 200、旁人可见、
管理端重置后立刻回落、重置幂等；`--server-no-storage` 再判未配存储的 404/503。

⚠ 三个踩过的坑：① **C 组那台静态服务器不能开反代** —— 假 token 一打真
`/api/auth/me` 就 401，前端按约定清会话，设置区根本不渲染头像块，C 组会以
"与裁剪器无关的原因"红掉；② **mock 态 `/api/uploads` 要回 400 不是 404** ——
`probeAvatarUpload()` 的判据是状态码，404 会被读成"没开存储"、按钮不渲染；
③ S4b 的计数**起点是 1**（前面 S1b 已经成功改过一次、真花掉一个额度），
按"本段成功几次"算会漏掉前置消耗。

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
  PUT    /api/me/password          凭当前密码改密（票 #71；成功 200 + 新会话，其余会话全撤销）
  POST   /api/me/email/otp         发换绑码到**新**地址（票 #72；恒定 200，dev echo 回 devCode）
  PUT    /api/me/email             凭码换绑（票 #72；码按收件地址校验；旧址即刻失效）
  GET    /api/me/deletion          读注销冷静期状态（票 #73；{pending,requestedAt,deadline}）
  POST   /api/me/deletion          发起注销（票 #73；收 currentPassword，要密码+勾选）
  DELETE /api/me/deletion          撤销注销（票 #73；**不要密码**；没申请过 → 409 DELETION_NOT_PENDING）
  GET    /api/users/:id/posts?limit&offset    GET /api/users/:id/rooms?limit&offset
  （票 #68：作者内容子资源，**免登录**；刻意没有 ?authorId= 变体，规格 #55 允许二选一不双开）
  GET    /api/rooms/mine?limit&offset        房主全部房，含未公开与已关闭（票 #70）
  DELETE /api/posts/:postId                  仅作者；403/404 分开；硬删 + CASCADE（票 #70）
好友 / 关注（票 #59 之后）
  POST   /api/follows                       DELETE /api/follows/:userId
  GET    /api/follows/:userId               GET    /api/follows?tab=mutual|following|followers
  GET    /api/posts?sort=following          关注流（票 #66；需登录，匿名 401）
  POST   /api/blocks                        GET    /api/blocks
  DELETE /api/blocks/:userId                GET    /api/users/search?q=
  （已退役：GET /api/friends、GET|POST /api/friends/requests、
    POST /api/friends/requests/:id/accept|reject、DELETE /api/friends/:id → 一律 404）
收藏（票 #64；全部需登录，均为私人数据）
  PUT    /api/posts/:id/favorite             DELETE /api/posts/:id/favorite
  PUT    /api/animes/:id/favorite            DELETE /api/animes/:id/favorite
  PUT    /api/rooms/:id/favorite             DELETE /api/rooms/:id/favorite
  GET    /api/favorites?type=post|room|anime&before=&limit=
消息
  GET    /api/messages?peer&before           POST /api/messages/read
我的内容（票 #70；需登录）
  DELETE /api/posts/:postId                仅作者；403/404 分开；硬删 + CASCADE
  GET    /api/rooms/mine?limit&offset      房主全部房（含未公开与已关闭）
公开房间
  GET    /api/public-rooms[?official=true|false]    PATCH /api/rooms/:id/meta（房主改标题/公开性）
管理端（全部 RequireAdmin）
  GET/POST/PATCH/DELETE /api/admin/users[/:id]
  POST   /api/admin/rooms/:id/close|unpublish|official
  GET    /api/admin/rooms[?public=&official=&search=]
  GET    /api/admin/metrics        GET /api/admin/logs        GET /api/admin/audit

WebSocket
  /ws       房间信令（游客可用；带 ticket 则绑定账号）
  /ws/user  用户通道（ticket 必填）
  新增信封：friend-removed / blocked / dm / dm-read / presence / rooms-changed
  （friend-request / friend-accepted 的**用例**已随票 #59 退役，帧编码保留未删）
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
| 禁止 HTML 注入 API | 仓库规则 + **构建后 grep 门禁**（`innerHTML` / `outerHTML` / `insertAdjacentHTML` / `eval(` / `new Function` 任一命中即失败；`v-html` 仅允许 1 处——`ui/MarkdownView.vue` 经 DOMPurify 消毒的 Markdown 出口），并列为代码评审固定检查项 |
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
- 交付：`/ws/user`、关注/取关/拉黑（好友请求流水线已在票 #59 退役）、离线未读、历史分页、房间聊天入库与 30 天清理。
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
