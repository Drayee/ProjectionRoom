# CLAUDE.md —— 月喵（ProjectionRoom）项目导引

> **这是 AI 会话进入本项目后的唯一入口。默认只读本文件**，需要动某处时再按下面的路径精确打开那几个文件，不要重新遍历整棵树。
>
> **维护约定（必须遵守）**：目录结构、关键约定、命令、部署事实一旦变化，**同步更新本文件**。过时地图比没有地图更危险——后续会话会按它行动。
> 最近一次同步：**首页的写入口回来了一票（无票号，用户报「找不到发帖和建房间的入口」）**。根因不是功能坏了，是 **`4454c63`「删除旧展示层」把建房 UI 与 FAB 一起删掉且从未重建**——全前端只剩 `stores/room.ts` 里主播断线后的自动重建那一条 `POST /api/rooms`，发帖只剩 `/posts` 页头右上角一处。现在首页已登录态是：**圆形 FAB（`＋` 展开「新建房间 / 发送帖子 / 添加番剧」）**+ 两块卡片并排（`CreateRoomCard` 建房 / `JoinRoomCard` 进房），旧占位文字「等待加入房间」已删（它描述"我什么都没有"，而现在下面有卡片，留着只会让人以为真正入口在别处）。**建房逻辑只有一份**：`components/features/room/useCreateRoom.ts`（形态对齐 `useJoinRoom.ts`）——`ensureLoggedIn()` 就地拦截 → 昵称空校验 → `authedFetch('POST /api/rooms')` →401 报「登录状态已失效」并不再重试 → 成功落 `pr:name` + `rememberJoin(roomId,{role:'host',hostToken})` → 跳 `/room/<码>`。**FAB 不自己建房**：它 `emit('create')`，由首页 `scrollIntoView` 把卡片带到眼前再调卡片的 `create()`（`defineExpose`）；发帖 → `/posts/new`（`post-editor`），加番剧 → `/forum` —— 旧版这两个只弹「能力待接入」，现在后端都有了，再弹那句话就是撒谎。**刻意不 import room store**：旧实现从 `applyIceResponse` 偷一份 ICE 进缓存，会让首屏 chunk 把 WebRTC/信令整包拉进来（实测首屏 `applyIceResponse` 出现 0 次、`RoomPage` chunk 87KB）；房间页 `enterRoom()` 本来就会 `iceConfig.start()` 主动拉 `/api/ice`，那条路径对"从分享链接进房"的人一直成立，代价只是进房后多一次与信令并行的往返。**加号是 CSS 画的两条 2px 条**（展开转 ±45° 成 ×），不是图标文件——`public/icons/` 的个数被脚本断言，为一个加号新增 SVG 得不偿失。**收起态必须 `visibility:hidden` + 子按钮 `tabindex="-1"`**（只用 `opacity:0` 时键盘仍能 Tab 进看不见的按钮）。Esc 与点击外部两条关闭路径都在（监听挂 **capture 阶段**：页面里若有别的 `pointerdown` 先 `stopPropagation`，冒泡阶段就收不到）。顺带修掉一处误导文案：`MeRoomsBlock` 空态原来说「在公开房间页开一间房」并链到 `public-rooms`，而那页历来只有列表、没有建房按钮 —— 现已改成链到首页（`name: 'home'`）。脚本 `verify-home-entries.mjs` **28 条**（不需要后端：自起静态服务器，`POST /api/rooms` 真的回一份合法创建响应，其余 `/api/*` 回 404）：首页构成、FAB 三态（收起不可见/不可 Tab、展开可见/可聚焦）、Esc 收起、点击外部收起、「发帖」真跳 `/posts/new`、「加番剧」真跳 `/forum`、「新建房间」真发出请求并跳到 `/room/<码>`、`hostToken` 落进凭据、缺 `hostToken` 的老服务端也能建房、未登录是落地页且**零请求**、reduced-motion 下 FAB 仍可用、源码级「建房请求只有一个落点」。
>
> **本段只记"最近一次"**；每票的完整决策、取舍与判据细节在 `docs/ACCOUNTS.md` 对应章节（§7.x）与 `docs/README.md` 索引里，不要在这里继续堆。

---

## 0. 这是什么

**月喵**——主播播放**本地**视频，观众通过 WebRTC DataChannel 同步观看（像 B 站放映室）。
服务端只做信令、房间状态、拓扑与账号，**不传输任何视频字节**。
作者 **drayee**，开源：<https://github.com/Drayee/ProjectionRoom>。

## 1. 四条铁律（改任何代码前先确认没违反）

| 编号 | 内容 | 违反的表现 |
| :--- | :--- | :--- |
| **I1** | 服务端不经手视频字节（分片只在 peer 之间走 DataChannel） | 出现"服务端中转媒体"的代码路径 |
| **I2** | 房间**实时状态**（成员/拓扑/播放进度/位图）只在内存；DB 只存**元数据**与周期快照 | 每次同步都写库；房间状态以库为准 |
| **I3** | **授权 100% 在服务端**；前端缓存的 `role` 只决定"显示什么" | 前端路由守卫/`v-if` 被当作授权 |
| **I4** | 任何 DB 写入都不在 WS/REST 处理路径上（事件类走异步单写协程） | 在 handler 里同步写库 |

## 2. 目录地图

| 路径 | 职责 |
| :--- | :--- |
| `internal/service/` | **服务层（核心）**：`manager.go`/`room.go`/`assign.go`/`mode.go`（房间、成员、拓扑分配；管理端的 `CloseRoomByAdmin`/`SetRoomMeta` 也在这里）、`account.go`（账号流程 + 管理端 `BanUser`/`SetRole` + 审计投递；**身份与授权事实的唯一落点**）、`account_store.go`（包内唯一 import gorm 的适配层）、`hub.go`（WS 连接与广播）、`logring.go`（管理端日志环形缓冲）、`server.go`（HTTP 服务与超时）。**没有单独的 `admin.go`**——管理端用例分散在 account.go（用户类）与 manager.go（房间类），管理端 REST 在 `handler/admin.go` |
| `internal/bootstrap/` | **装配根**（无业务逻辑）：`bootstrap.go`(导出 `ProviderSet`)· `db.go`(连接池/迁移/事件写入器 + 装配哨兵常量)· `auth.go`(签名器/票据/账号服务/适配器)· `admin.go`(日志环形缓冲 + 管理端依赖)· `engine.go`(handler 依赖包 + 路由引擎)。只被 `cmd` 引用；**provider 列表的声明顺序 = 创建顺序，cleanup 反序**（保证"关服务 → flush 写队列 → 关连接池"） |
| `internal/service/auth/` | 凭据层：`password.go`(bcrypt)、`jwt.go`(HS256 access token)、`ticket.go`(WS 一次性票据) |
| `internal/service/limiter/` | 按 key/按 IP 的令牌桶 |
| `internal/service/ice/` | STUN 探测、打分与下发选择（`registry.go`/`score.go`/`select.go`/`sticky.go`/`stun.go`） |
| `internal/service/mp4/` | MP4 解析与切片（`parse.go`/`split.go`） |
| `internal/service/segment/` | 服务端切片子系统（队列、配额、ffmpeg 流水线） |
| `internal/handler/` | HTTP：`router.go`(装配与注册)、`ws.go`(信令入口)、`auth.go`+`middleware.go`、`admin.go`、`publicroom.go`、`roommeta.go`、`segment.go`、`static.go`、`downloads.go`、`errors.go`(错误码映射) |
| `internal/store/` | **唯一持久化 owner**（GORM）：`gorm.go`(连接)、`migrate.go`+`migrations.go`(内嵌有序 DDL)、`user.go`/`session.go`/`roommeta.go`/`audit.go`、`writer.go`(事件异步写入器) |
| `internal/config/` | 全部环境变量、默认值与校验（`config_struct.go` 结构、`config_load.go` 载入与 `applyXxxEnv`） |
| `internal/model/` | 领域模型、错误码、`wire.go`(解码前预算)、`pb_convert.go`(protobuf ↔ 领域) |
| `internal/pb/` | **生成代码**（勿手改）；真源是 `proto/projection_room.proto`，重新生成用 `scripts/gen-proto.ps1` |
| `internal/utils/` | 小工具（`rand.go` 的 crypto/rand 封装等） |
| `internal/storage/` | **对象存储的唯一出口**（MinIO / S3 兼容）：`minio.go` 的 `PutImage`/`Remove`/`EnsureBucket`。只服务图片等静态附件，**不得**把视频分片塞进来（不变量 I1）。上层（handler）不认识 S3 SDK，换实现只动这个包。`Endpoint` 为空即未启用 → `/api/uploads` 不注册（404），与账号层"DSN 为空即不注册 /api/auth/*"同一条约定 |
| `cmd/` | **根包只有三个文件**：`main.go`(最薄骨架：读配置 → `InitializeApp` → 起服务 → 优雅退出，无业务代码) · `wire.go`(**只有** `//go:build wireinject` 头 + `InitializeApp`，`wire.Build(bootstrap.ProviderSet)`，无业务代码) · `wire_gen.go`(`go tool wire ./cmd` 生成)。**provider 全在 `internal/bootstrap/`**。另有独立 main 包 `cmd/segmenter/`(本地切片器 CLI，被 `scripts/build-segmenter.ps1` 交叉编译成用户下载的 exe；它的改动会影响构建脚本与 `docs/SEGMENT.md`) |
| `client/` | Vue 3 + TS + Vite（月夜故障 · B 重建后）：`src/pages/`（含 `/me` 的 `MePage.vue`）、`src/components/{ui,features}/`、`src/styles/{tokens,base,transition}.css`、`src/stores/`、`src/api/`、`src/utils/`、`src/gen/`(生成的原型代码)、`public/icons/`(**本地 SVG 图标，用多少复制多少**)、`public/fonts/`、`scripts/prune-dist.mjs`(构建后清理陈旧产物)。旧 `src/views/` 与根级 `src/components/` 已删（零残留） |
| `test/script/` | 真机验收脚本（headless Chrome，见 §6） |
| `test/resource/` | 媒体夹具（`short_video/cut`、`mkw_cut` 等） |
| `docs/` | **文档总索引是 `docs/README.md`**（五组：现行规范 / 决策记录 / 实施计划与报告 / 笔记 / 归档）。现行规范：`SPEC.md`(房间与信令协议)、`ALGORITHM.md`、`SEGMENT.md`、`ACCOUNTS.md`(账号/社交/管理端规格)、`DEPLOY-SINGLE-PORT.md`、`STORAGE-MINIO.md`、`GLOSSARY.md`(术语唯一源)；另有 `adr/`、`agents/`(技能配置)、`plans/`、`reports/`、`notes/`、`prototypes/`、`archive/`(已过时，每篇顶部有横幅) |

## 3. 关键协议与约定

- **protobuf 是唯一真源**：`proto/projection_room.proto` → 生成 `internal/pb/*.go` 与 `client/src/gen/*.ts`（`scripts/gen-proto.ps1`）。
- **WS 信封**：1 字节类型前缀 + protobuf 体；room 信令走 `/ws?roomId=<码>&clientId=<id>[&ticket=<票据>]`。
  - **无 `ticket` = 游客**（观众免登录进房是产品决定，别加拦截）；带票据则绑定账号；票据单次使用 + 30s 过期。
  - **Origin 白名单**：`PR_ALLOWED_ORIGINS` 是**唯一**判据（不再"同源隐式放行"；DNS rebinding 曾据此绕过）。默认值覆盖两种**本机**入口：开发态 `5173` 与单端口态 `8888`（后者是 `go run ./cmd` 的默认形态，漏了它会让默认部署开箱即坏：页面能开、`/ws` 一律 403）。**公网/隧道部署必须显式加域名**。**启动期会校验 `PR_PUBLIC_BASE_URL` 的 host 被白名单覆盖**（`internal/config/origin_check.go` 的 `publicBaseURLAllowed`，判据与 `internal/handler` 的 `checkOrigin` 同源：含 `://` 时比 `scheme://host`、否则比 `host[:port]`，支持 `path.Match` 通配，大小写不敏感）——不覆盖就拒绝启动，并把**该补的那一项原文** `scheme://host` 打进报错，用户照抄即可。为什么要在启动期拦：2026-11-02 线上换域名 `yachiyocat.top` 时漏配这一项，症状是**页面照常打开**（静态资源不判来源）但 `/ws` 与 `/ws/user` 一律 403、实时通道无限重连，浏览器控制台只有一行 403 handshake —— 症状与真因之间隔了整条 nginx + 白名单链路，而它在启动期就可判定。
- **DataChannel**：negotiated id=0，1 字节 kind 前缀（`0x01` 控制/`0x02` init/`0x03` 媒体/`0x04` tail）+ 7 字节媒体头；64 KiB 分片 + `bufferedAmount` 背压。
- **房间码**：`^[A-Z0-9]{4,12}$`；建房**必须登录**且响应下发一次性 **`hostToken`**（主播断线宽限期内接回主播位必须携带）。
- **主播宽限期**：`PR_ROOM_HOST_GRACE`（默认 60s）内房间保留，宽限期内**永不回收**；客户端用同一房间码自动重建。
- **ICE**：TURN 已彻底退役；服务端周期性实测 STUN 并打分下发（最快 2 条 + 健康粘性 2 条恒选），带 `ttlSeconds`/`expiresAt`/`probe`。
- **解码前预算**：`internal/model/wire.go` 用 protowire 扫 repeated 元素数，**先于** `Unmarshal` 拒绝（曾出现"4 MiB 帧展开成 140 万个对象 → 3.9 GB"）。

## 4. 账号 / 社交 / 管理端（现状）

- **混合式会话**：refresh = HttpOnly Cookie 里的不透明串（库里只存 sha256，每次刷新**轮换** + **重放检测→撤销全部会话**）；access = 15 分钟 JWT，**只存内存 + `sessionStorage`**（**不进 localStorage**）。
- 前端缓存的 `pr:profile` 只用于显示，**不是授权依据**。
- **DB 表**：`users`（含 `email` + `email_verified_at`，`lower(email)` 上有**部分唯一索引**）/ `sessions` / `password_resets`（验证邮箱与验证码**共用**，靠 `purpose` 区分：`email_verify` / **`email_otp`**；只存 token 的 sha256；`attempts` 作验证码错误计数。⚠ DB 的 CHECK 里仍留着历史取值 `password_reset`，但**应用层已不再产生也不再接受它** —— 链接式改密已下线，见下）/ **`register_drafts`**（两段式注册的中间态：已算好的 bcrypt + 待验证邮箱）/ **`idempotency_keys`**（写操作重放保护，`(scope,key)` 主键）/ `rooms_meta` / `admin_audit` / `schema_migrations` / **`friendships`** / **`posts` + `post_comments` + `post_likes`** / **`anime`** / **`follows`**（关注边：单向、有向边对，互关 = 两条边的合取）/ **`messages`**（`kind=dm` 走通，`kind=room` 的列与索引已建但写入路径未做）。（`kind=room` 房间内聊天与 30 天清理属后续阶段。）
- **注册与改密码**：注册是**两段式**——`register/start`（账号+密码，返回一次性 `ticket`，**不建号**）→ `register/email`（发验证码）→ `register/verify`（建号+会话）。建号即视为**邮箱已验证**（验证码刚证明了归属），因此注册响应里的 `emailVerified` 是 true。**注册不收昵称**：默认等于用户名（`SanitizeDisplayName` 的兜底），之后在设置页改。**改密码只有验证码一条路**：`password/otp` + `password/otp/reset`。
- **链接式改密已下线**（`/api/auth/forgot` + `/api/auth/reset` 已删除，旧邮件里的 `/reset#token=…` 链接因此失效）：原因**不是**链接不安全（256 bit 比 6 位数字更抗爆破），而是它必须 gate 在 `email_verified_at` 上（否则攻击者填受害者的邮箱，受害者点开链接就把**攻击者账号**的密码改了），于是"邮箱未验证"的老账号**永远找不回密码**，而对外响应恒定 200 让用户只看到"已发送"却收不到信。验证码没有这个两难：码只发给邮箱的真实持有者，所以它**对未验证邮箱也开放**。`PurposePasswordReset` 常量随之删除；DB 的 CHECK 里仍留着该取值（迁移规约「只做加法」不允许删行/改约束），但应用层已不收也不产。
- **验证码的三道防线**：6 位数字、5 分钟 TTL、**错误次数落库**（`attempts`，5 次即作废，重启不清零）+ 发送冷却 60s + 每 IP/每邮箱限流。人机验证是**前端页内子窗体**（`CaptchaDialog.vue`），注册与找回密码**两条路各一个实例**（open 状态互不相干），定位是**摩擦而非安全边界**（题面与校验都在前端）。
- **写操作幂等**：三个注册端点与改密端点接受 `Idempotency-Key`（UUID）。同键同体只执行一次（双击不建两个号、不让首封验证码立刻作废）；**同键不同体返回 409**。只有成功才落键——失败往往正是值得重试的那一类。
- **邮箱与验证**：找回密码**不要求**邮箱已验证（见上）；`email_verified_at` 现在只表示"归属已被证明"这一事实本身，用于界面提示与管理端展示。管理员 `PATCH /api/admin/users/:id {email}` 绑定邮箱时会**同时标记已验证**（管理端 UI 已有该入口）。注册后的验证信仍走一次性链接 `<PR_PUBLIC_BASE_URL>/verify#token=…`（token 在 **fragment** 里，故不进服务端访问日志）。
- **迁移**：内嵌有序 DDL，启动自动补齐，**只做加法**；`schema_migrations` 记版本。⚠ `CREATE TABLE IF NOT EXISTS` **不会给已存在的表补列**：凡是在建表之后又加的列，必须另写一条 `ADD COLUMN IF NOT EXISTS`（本轮 `register_drafts` 踩过，被 schema 测试抓到）。
- **API 速查**：`/api/auth/{register,login,refresh,logout,logout-all,me,ws-ticket}`、**`/api/auth/register/{start,email,verify}`**（两段式注册）、**`/api/auth/password/otp`**（发码，恒定 200，不泄露账号是否存在）+ **`/api/auth/password/otp/reset`**（凭码改密，204）、**`/api/auth/verify-email`**（注意是 `verify-email` 而不是 `verify`）、`/api/rooms`(POST 建房，需登录)、`/api/rooms/:roomId`(信息)、`/api/rooms/:roomId/meta`(GET 仅房主读回 / PATCH 房主改)、**`/api/rooms/mine`（票 #70：房主全部房，含未公开与已关闭；与 `/api/users/:id/rooms` 口径互补）**、`/api/public-rooms`(免登录；**`?official=true` 是官方/推荐区**，只列已公开且未关闭的官方房、按运营位次排序)、`/api/ice`、`/api/v1/segment/*`、`/api/posts`(列表/详情/发帖/点赞/评论/**删帖 `DELETE /api/posts/:postId`（票 #70，仅作者，403/404 分开，硬删 + CASCADE likes/comments，收藏条目保留）**；**`?sort=latest|hot|following`** —— `following` 是票 #66 的关注流，**必须登录**（匿名 401），只看我关注的人发的帖、严格时间倒序，已封禁与双向拉黑的人一律不出现)、`/api/animes`(免登录目录 + `/:id` 详情)、**`/api/animes/:animeId/posts?sort=latest-reply|latest`**（板块主题串；`POST /api/posts` 带 `animeId` 即发主题串，详情与回复复用 `/api/posts`）、`/api/blocks`+`/api/users/search`(好友域；**`/api/friends` 与 `/api/friends/requests` 等五条已在票 #59 退役**，落到 `NoRoute` 的 `/api` 前缀拦截返回 404 + 中文，好友列表改用 `GET /api/follows?tab=mutual`)、**`/api/users/:id`（公开档案，免登录；不存在与被封禁都回 404，防存在性探测）+ `PATCH /api/me`（改自己昵称与头像，需登录；**头像三态**：键缺席=不动 / `null`=清除 / 有值=设置，值必须是本桶合法对象 URL 否则 400 `AVATAR_URL_INVALID`，未配 `PR_STORAGE_*` 时带值回 503）**、**`PUT /api/me/password`（票 #71：凭**当前密码**改密，收 `{currentPassword, newPassword}`，成功回 200 + 完整新会话 —— 其余会话全被撤销、当前设备换发新凭据；错当前密码 400 `PASSWORD_MISMATCH`，弱口令 400 + 策略原因，走幂等键族 scope `me.password`）**、**`POST /api/me/email/otp`（票 #72：把验证码发到**新**地址，收 `{email}`；恒定 200 但 dev echo 时回 `devCode`；新址被占 409 `EMAIL_TAKEN`、与当前址相同 400、冷却 60s 按 user 算）**、**`PUT /api/me/email`（票 #72：收 `{email, code}`，码**按收件地址**校验，成功 200 + 更新后的 `Profile`（`emailVerified: true`）；旧地址即刻失效、找回密码自动改指新地址；不动口令与会话）**、**`GET/POST/DELETE /api/me/deletion`（票 #73：读冷静期状态 / 发起 / 撤销）。发起收 `{currentPassword}`，要当前密码 + 前端勾选确认（页内确认条，不是 `window.confirm`）；撤销**不要密码**（撤销是降低风险的方向）。成功后对外**立即**全隐藏（档案 404、搜索与三份名单不再出现、帖子署名「已注销用户」），但私聊历史与帖正文**保留可读**；冷静期内**不清会话、不动 `TokenVersion`** —— 蹬掉会话会让横幅上的「撤销」永远点不到。撤销时"没申请过"回 **409 `DELETION_NOT_PENDING`**。到期由后台 `DeletionSweeper` 批量真删（I4，不在请求路径上）**、**`GET /api/users/:id/posts?limit&offset` + `GET /api/users/:id/rooms?limit&offset`（票 #68，**免登录**的作者内容子资源；帖复用 `postView`（含 `favorited`），房刻意只回 `roomId/title/createdAt/lastSeenAt` —— 没有成员数/主播在线/房主名，那些是内存实况，属于公开房列表；可见性走 `applyAuthorVisibility`，与关注流同一实现；**没有 `?authorId=` 变体**）、**`/api/messages`（`?peer=&before=` 历史 / POST 发送）+ `/api/messages/conversations` + `/api/messages/read`**（私聊）、**`/api/follows`**（`POST` 关注 / `DELETE /api/follows/:userId` 取关 / `GET /api/follows/:userId` 我与 TA 的关系 + TA 的关注/粉丝数 / **`GET /api/follows?tab=mutual|following|followers` 名单**（票 #58；每行带 following/followedBy/mutual/canDm，**刻意不含拉黑方向**——不泄露谁拉黑谁；已拉黑与已封禁的人一律不出现在名单里；**`tab=followers` 额外回 `newCount`**（票 #67，粉丝红点数；互关/关注两个 tab 回**不带**这个键——带上会让前端拿到一个与当前视图无关的数字）） + **`PUT /api/me/followers-seen`（票 #67：把粉丝水位线推到"现在"，幂等，204，挂在只读桶；DB 写走异步单写通道守 I4）**；关注是**单向免同意**的边，互关即好友）、**`PUT|DELETE /api/{posts|animes|rooms}/:id/favorite`（票 #64，幂等；回 `favorited` 与 `created`/`removed`）+ `GET /api/favorites?type=post|room|anime&before=&limit=`（收藏时间倒序，`before` 用上一页最后一行的 `cursor`；失效条目 `valid:false` 但**保留**）**、`/api/admin/{users,rooms,metrics,logs,audit}` + `/api/admin/rooms/:roomId/{close,unpublish}`。
- **未读数只有一个写入点**（wayfinder #30）：`stores/auth.ts` 的 `setUnread()`。两个来源都往它写 —— 会话列表的合计（mock 与 real 都走这条）与 `/me` 的 `unread`（**仅 real 域**，见 `applyServerUnread`）。为什么 mock 域要拒掉 `/me`：mock 的会话是前端内存里的假数据，而 `/me` 打真后端回的 `unread: 0` 与界面上的数字不同源，两个写入点互相覆盖的症状是"顶栏角标时有时无"。**`api/messages.ts` 门面刻意不导出 `unreadTotal()`**，多一个取数入口就等于多一个写入点。
- **档案双路由（票 #69）**：`/user/:id` 是公开橱窗（本人看自己的 id 也是公开视角），本人的管理动作全在 `/me`（四个分区 `?tab=overview|relations|favorites|settings`，键名唯一源 `utils/meRoute.ts`；收藏分区见票 #65）。顶栏头像落 `/me`，`isSelfProfile` 把 `/me` 也算"我"。**旧的双视角分支已删**，别在 `/user/:id` 里再加 `isSelf` 的管理入口 —— 那正是本票拆掉的东西。
- **`/messages?peer=<id>` 是跨票契约**（`utils/dmRoute.ts`）：`#28` 的「发消息」按它生成链接，`#30` 的消息页从 URL 解析会话。选中会话 = `router.replace(conversationPath(id))`，**没有只改内存的选中态** —— 刷新、新标签页、后退键因此自然成立。
- **启动完整性（本轮的策略变更）**：**配置必须齐全才启动**——缺 `PR_DB_DSN` / `PR_JWT_SECRET`(≥32字节) / `PR_PUBLIC_BASE_URL` / SMTP 三件套（或 `PR_OTP_DEV_ECHO=1`）/ 前端产物（`PR_SERVE_STATIC=true` 时）任一项，`config.requireComplete` 会**逐条列出并拒绝启动**。这取代了早先"缺 DB 就关账号、缺 SMTP 就 503"的运行期降级（那让"功能不全"要等用户点开才发现）。要跑功能不全的窄实例（只跑信令、单测）必须**显式**写 `PR_REQUIRE_COMPLETE=0`。**代码里"账号能力关闭"的分支仍在**（`PR_DB_DSN` 为空时不注册 `/api/auth/*`，返回 404），但它现在只在显式跳过校验时可达；前端保留"把这个 404 解释成游客模式"的分诊（`useAuthForm.ts`），因为那是对**降级实例**的正确解读。

## 5. 配置（环境变量）

全部定义与默认值在 `internal/config/config_struct.go` + `config_load.go`（每个字段都有中文注释），用户可见的表在 `README.md`。本节只记**从默认值看不出**的三件事：

- **必需配置齐全才启动**（见 §4 的"启动完整性"）：缺任何一项由 `config.requireComplete` 在启动期拦下并逐条列出。**要跑窄实例必须显式写 `PR_REQUIRE_COMPLETE=0`**——这道闸默认开启，因为"漏配"与"刻意跳过"在日志里长得一样，而后果完全不同。
- **`PR_TRUSTED_PROXIES` 默认只信回环**：配错（把直连来源也标为可信）会让按 IP 限速退化成全局限速。
- **`PR_SECURITY_CSP` 是本地矩阵的开关**：默认严格 CSP 会拦住脚手架注入的跨源本机媒体服务器；本地跑验收矩阵时临时放行 `http://127.0.0.1:*`。
- **注销冷静期与清扫周期**（票 #73）：`PR_DELETION_GRACE`（默认 `720h`，即 30 天，区间 1m–8760h）同时决定 `/me` 横幅显示的到期日与后台清扫动手的时刻，**两处必须用同一个值**，所以前端只读接口回的 `deadline`、不自己算；`PR_DELETION_SWEEP_INTERVAL`（默认 `1h`，区间 100ms–24h）是清扫协程的 tick。跑真机验收判"到期真删"时要把它压到分钟级（`verify-account-deletion.mjs --purge`）。
- **邮件能力现在也是必需项**（注册与改密码都靠验证码）：配齐 `PR_SMTP_HOST/_USER/_PASS`，或显式打开 `PR_OTP_DEV_ECHO=1`（**仅本机开发**：验证码明文回显在响应里）。**两者同时开启会拒绝启动**——那种组合没有正当理由，只说明开发配置被带进了部署。两个还会在启动期被拦下的组合：`PR_SMTP_TLS=none` + 非回环 host（标准库 `PlainAuth` 本来就会拒，提前拒更好归因）；以及 `PR_SMTP_FROM` 与认证账号不一致（QQ 会在 RCPT 阶段 550）。**发信是异步入队的**（队列满只记日志、对外仍恒定 200），所以"日志里出现『已入队』不等于『已投递』"——排查时要看两态。

## 6. 构建与验证（照抄即可）

```powershell
# 门槛（每次改完都要全绿）
gofmt -l ./cmd ./internal                 # 必须无输出
go build ./... ; go vet ./... ; go vet -tags wireinject ./cmd ; go tool wire check ./cmd
go test ./... -count=1 -timeout 900s
# 竞态（需要 cgo；本机 gcc 路径）
$env:CGO_ENABLED='1'; $env:CC='D:\IT\zip-application\winlibs-mingw64\mingw64\bin\gcc.exe'
go test -race ./internal/service/ ./internal/handler/ -count=1
# 前端
npm --prefix client run build              # 含 vue-tsc；产物必须 0 个 window.__pr；grep "v-html" client/src ≤1（仅 MarkdownView）、innerHTML=0
```

**真库测试**（`internal/store` 的用例依赖它，否则 Skip）。两条路，任选一条：

```powershell
# ① 服务器上的测试库（经 SSH 隧道）
$env:PR_TEST_DB_DSN = (Get-Content "$env:TEMP\pr-deploy\test-dsn-5433.txt" -Raw).Trim()
go test ./internal/store/ -count=1

# ② 本机 PostgreSQL（非 docker；见下面的 scripts/setup-local-db.ps1）
$env:PR_TEST_DB_DSN = 'host=127.0.0.1 port=5432 user=pr_app password=<见 .env.local> dbname=projectionroom_test sslmode=disable'
go test ./internal/store/ -count=1
```

**本机非 docker PostgreSQL 的起法**（带账号能力的完整实例，不用隧道）：

```powershell
powershell -File scripts/setup-local-db.ps1    # 建 pr_app + 两个库，生成 .env.local（幂等）
powershell -File scripts/run-local.ps1         # 读 .env.local 起服务（默认 127.0.0.1:8888）
powershell -File scripts/run-local.ps1 -Port 8090   # 换端口（自动补 Origin 白名单）
```

⚠️ **`PR_ALLOWED_ORIGINS` 在换端口时必须跟着改**：`run-local.ps1` 会自动补，但手工起实例时忘了它就是"页面能开、`/ws` 一律 403"（默认白名单只含 5173 与 8888）。现在这种漏配会在**启动期**被 `publicBaseURLAllowed` 拦下并直接给出该补的那一项，不必再等到页面上发现。

⚠️ **两个坑**：① 测试库与生产库是**两个库**，DSN 的 `dbname` 必须含 `_test`（用例里有护栏）；② **`internal/store` 的真库用例会 TRUNCATE 表**，所以任何手工 fixture（如验收用的管理员账号）在跑过它们之后就不在了——需要重新注册并提权（`UPDATE users SET role='admin' WHERE username='…'`）。
⚠️ **测试实例收尾必须停进程**：曾有一个后台实例直连测试库、绕过了用例的跨进程隔离锁，导致 store 用例随机红。
⚠️ **并存会话的 fixture 会被清掉**：`public.users` 被 store 用例 TRUNCATE ⇒ 刚 seed 的管理员可能在几分钟后消失、`verify-auth` 因此假红。规避办法是**在同一个 `projectionroom_test` 里建隔离 schema**（DSN 追加 `search_path=pr_xxx`），在其中造 fixture、跑完 `DROP SCHEMA`；`public` 侧不留痕。
⚠️ **`TestWriterSubmitNeverBlocksWhenQueueFull` 在 Windows 上会假红**（既有缺陷，与本机库无关）：它断言 `LastLatency > 0`，而 `noopJob` 跑得比系统时钟分辨率还快 —— 实测 `Transaction(noop)` 500 次里有 415 次 `time.Since` 恰好为 0。修法要么把断言放宽到 `>= 0`，要么让夹具作业带一次真实往返。Linux CI 上通常不出现。
⚠️ 起实例做真机验收需要调试钩子（既有脚本依赖 `window.__pr`，而**生产构建刻意没有**）：`$env:VITE_DEBUG_HOOKS='1'; npx vite build --outDir dist-debug` + `PR_STATIC_DIR=client/dist-debug`，跑完删掉并重建干净 `dist`。

## 7. 验收脚本（`test/script/*.mjs`，真机 headless Chrome）

| 脚本 | 覆盖 | 注意 |
| :--- | :--- | :--- |
| `verify-m2` | 起播与同步偏差 | 需媒体夹具 + 调试钩子 |
| `verify-room-resume` | 主播断线/宽限期/同码重建 | **`--grace` 必须与服务端 `PR_ROOM_HOST_GRACE` 对齐**；素材用默认 `short_video/cut`；服务端自报 grace 要一致 |
| `verify-ice` | ICE 下发/刷新/重启 | 实例需 `PR_ICE_TTL=30s` |
| `verify-health` | 卡顿换父与健康度 | 素材用 `mkw_cut`（短素材会假失败） |
| `verify-auth` | 账号判据（重放撤销、封禁断连、游客进房、**`password/otp` 不泄露账号存在性**、**链接式改密端点已下线**） | 需管理员凭据（`--admin <文件>`），测试库里的管理员会被 store 用例清掉。**换环境时**：发码接口的期望值取决于实例是否配了 SMTP（未配=两条路径都 503，已配=都 200），脚本会先探测并打印它选了哪条 |
| `verify-forgot` | **找回密码（邮箱验证码）的真机 UI 流程**：`F1` 深链渲染找回密码页签 / `F2` 人机验证子窗体→第 2 段→拿到码 / `F3` 点「设置新密码」**确实发出**请求且 204 / `F4` 成功后切回登录并清空输入 / `F5` 新口令生效旧口令失效 / `F6` **未验证邮箱也能走完找回**（本轮改造的核心） | **实例必须 `PR_OTP_DEV_ECHO=1`**，否则拿不到验证码（脚本会明确报缺 dev echo 而不是假绿）。**刻意不并进 `verify-auth`**：那个脚本允许"没配 SMTP → 503"的口径，而这里必须拿得到码，两者对实例的要求互相矛盾。F3 靠**页面里的 fetch 钩子**判定 —— 本轮那个"点了没反应"的缺陷是静默的（无请求、无报错），只有钩子能区分"没发"与"发了但失败"；已用回退实验验过：把 busy 自锁的修复撤掉，F3/F4/F5 必红 |
| `verify-avatar` | **UserAvatar（票 #61）真机验收**：同一用户跨页同色、点击落点、键盘可达与无障碍名、**嵌套处不可点**（论坛主题行 / 消息会话行里的头像不是链接）、hover 反馈、reduced-motion 下仍换色不再位移、源码级「取色只有一个源」 | **不需要后端**：脚本自起静态服务器托管 `client/dist` 并把 `/api/*` 回 404，前端默认 mock 域即可跑；**须先 build**。源码级判据要先**去注释**再 grep（组件注释里会提到被删掉的 `hsl(hash)`，直接 grep 会假红） |
| `verify-avatar-overlay` | **头像覆盖层（票 #74）真机验收**：空态（无 `avatarUrl` 时不渲染 `<img>`、字母头与底色仍在、容器 `position: relative` + `overflow: hidden`）；有值态（`.avatar-pic` 出现、`alt=""` + `aria-hidden="true"`、`object-fit: cover`）；`onerror` 后摘掉图片、字母与底色回来；**会话内不重试**（切分区再回来仍无图）；`/user/:id` 本人视角也显示；四处源码级（`object-fit: cover` 只在 `ui/AvatarImage.vue`、失败记忆只在 `utils/avatarImage.ts`） | **不需要后端**：脚本自起静态服务器托管 `client/dist` 并把 `/api/*` 回 404，前端默认 mock 域即可跑；**须先 build**。坏图片地址走 `/api/broken-avatar`（回 JSON ⇒ 解码必失败），不能用不存在的静态路径（那会被 SPA 回退成 index.html，"为什么 error"说不清）。O4 判「不重试」要**切分区触发重挂**而非整页导航（`cdp.navigate` 会重载 JS、清空模块级 Set）。`startChrome` 返回的对象**没有 `kill`**，`finally` 里必须 `cdp.close()` + `chrome.close()`，写成 `chrome.kill?.()` 是静默 no-op、脚本跑完不退 |
| `verify-avatar-crop` | **头像裁剪与每日额度（票 #75）**：C1 入口与 aria-modal 子窗体 / C2 无图时「用这张」不可点 / C3 选 4000×3000 后 meta 写明源像素 3000×3000 / C4 缩放推到上下限不越界 / C5a 确认后**先弹人机验证**不是直接上传 / C5b 按题面点图形 / C5 用 `window.fetch` 钩子取真实上传体，断言 **512×512 + jpeg/webp + ≤1 MiB** / C6 Esc 取消**不发请求**且之后仍能再开 / C7 源码级 `AVATAR_EXPORT_SIZE` 与 `AVATAR_MAX_BYTES` 各只定义一次、`toBlob(` 只有 `AvatarCropDialog.vue` 一处；**S 组**（`--server`）S1 上传 201 / S2 外链 400 `AVATAR_URL_INVALID` / S3 只改昵称头像不动 / S4a 同地址重复不扣额度 / S4b 累计 5 次后第 6 次 429 / S5 额度满后清除仍 200 / S6 旁人可见 + 管理端 `{resetAvatar:true}` 后立刻回落 / S7 重置幂等 / S8（`--server-no-storage`）uploads 404 + 带值 PATCH 503 | **C 组不需要后端**（自起静态服务器）；**S 组要真后端**（`PR_OTP_DEV_ECHO=1` 才拿得到验证码，`--admin <文件>` 给管理员 token JSON）。⚠ **C 组那台静态服务器不要开反代**：假 token 一打真 `/api/auth/me` 就 401 → 会话被清 → 设置区不渲染头像块，C 组会以与裁剪器无关的原因红掉。S4b 的计数**起点是 1**（S1b 已经花掉一次额度），按"本段成功几次"算会漏掉前置消耗。mock 态 `/api/uploads` 必须回 **400 不是 404**（见 §7 上方说明） |
| `verify-shared-element` | **共享元素 FLIP（票 #78）真机验收**：点头像 `/friends`→`/user/2` 出现 `.shared-ghost`（第 ~43ms）、幽灵飞 **440ms** 而页面 **240ms**、两者同时在场（数帧 > 0）；幽灵挂在 `<body>` 上、`aria-hidden`、**不带 `data-shared`**；飞行中终点元素 `visibility:hidden`、落地后还原、幽灵已清；首末帧矩形确实在变（起点是列表里的小头像、终点是名片区大头像）；**后退不飞**（过渡名 back、零幽灵）；帖封面 `post-cover:<id>` 真的从卡片长到详情头图；**房卡→房不飞**（房页 `meta.exempt` ⇒ 过渡名 none、零幽灵）；三种零等待降级：目标缺失 / 源零尺寸（`display:none`）/ 源图未解码（`Network.setBlockedURLs` 断 `*/demo/cover.svg`）—— 全都不出现幽灵、页面照旧 240ms；reduced-motion 整条判死；四条源码级（440 与 240 各只有一个定义处、三处契约两端都在、只有接线层造幽灵、样式里不出现 `data-shared`） | **不需要后端也不需要 Vite**：脚本自起静态服务器托管 `client/dist` 并把 `/api/*` 回 404，前端默认 mock 域即可跑（`client/public/demo/cover.svg` 是帖封面夹具，`mock/posts.ts` 的 id 1/2 引用它）；**须先 build**。**踩坑四条**：① 必须先种 `pr:profile` + `pr:access` 再**整页重载**一次，否则好友页渲染登录引导、源元素根本不存在（S1 那组红成「幽灵 null」跟接线层无关）；② 判「并行」要数**同时在场的帧数**，取幽灵出现那一帧会假红（两者起跑差一两帧）；③ 模拟零尺寸源要用 `display:none` —— 写 `width/height:0px` 会被组件 `:style` 的 `boxStyle` 重渲染写回去；④ 一次换页可能**另一对**在飞，判「这一对没飞」只能看它自己的目标有没有被藏起来，看全局有没有幽灵是错的口径。单测 `client/test/shared-element.test.mjs` 15 条（纯判定层） |
| `verify-transition` / `verify-transition-logic` | **路由过渡（票 #76）**：前进/后退位移方向、同级只淡入无位移、放映室进出**零过渡**、reduced-motion 双侧判死（JS 侧过渡名必须是 `page-none`）、同路由仅 query 变化零过渡、过渡不等数据 | **不需要后端也不需要 Vite**：脚本自起静态服务器托管 `client/dist` 并把 `/api/*` 回 404，前端默认 mock 域即可跑；**须先 build**。`verify-transition-logic` 是决策器单测（Node 原生类型擦除，零新依赖） |
| `verify-navstate` | **导航当前态（票 #77）真机验收**：深链 `/posts/3`、`/forum/7`、`/posts/new` 落点高亮正确；本人 `/user/1` 分区全灭 + 头像位亮、他人 `/user/9` 全灭；`aria-current="page"` 与视觉同步；底色 == `--state-active`、前景升 `--text`、无滑动指示器；命中区 44×44；源码级「不依赖 `router-link-active`」 | **不需要后端**：脚本自起静态服务器托管 `client/dist` 并把 `/api/*` 回 404，前端默认 mock 域即可跑；**须先 build**。判据刻意不依赖数据齐全（`/forum/7`、`/user/9` 在 mock 里无实体，页面是空态，顶栏仍须正确点亮）。源码级判据要先**去注释**再 grep |
| `verify-authorrow` / `verify-post-actions` | **共享对象组件与列表互动（票 #63）**。`verify-authorrow`（结构与外观，mock 域）：作者行三尺寸（compact/detail/comment）、卡拆成「链接区 + 动作条」且**动作条不在链接里**、动作顺序 赞→收藏→评论、计数 0 隐藏、失败请求由 `ui/AsyncState` 渲染 error 态带重试、Tab 序、以及三个组件各只有一份实现。`verify-post-actions`（需真后端 + `VITE_MOCK_POSTS=0` 构建）：未登录点赞跳登录并带 `redirect`、乐观更新、**连点只发一次请求**、失败回滚可见 + 轻提示、成功后以服务端计数为准、收藏按钮占位 | `verify-authorrow` **不需要后端**（自起静态服务器 + `/api/*` 回 404），**须先 build**；它刻意**不**在列表页找 loading/empty —— mock 的 `listPosts` 同微任务就 resolve，那两个态结构上不可观察，所以改判「不存在的帖子 → error 态 + 重试」这条确定可达的路径。`verify-post-actions` 需要 `PR_OTP_DEV_ECHO=1`（要拿验证码建号）且列表**非空**（空列表时无卡可点）；**它读的是 `client/dist`，不是 `dist-real`** —— 构建到别的目录会静默跑 mock 域、点赞不发请求（踩过） |
| `verify-favorites` | **多态私人收藏（票 #64）真机验收**：两账号 + 一篇帖就绪；初始未收藏；点击后 `aria-pressed` **立刻**变 true（乐观更新）且响应落地仍是 true；**刷新后仍亮**（真落库）；详情页与列表同源；收藏按钮 `.count` 为空且隐藏、页面无任何公开收藏计数文案；**换第二个账号看同一帖收藏位是灭的**且页面上没有收藏痕迹（私人书签的核心判据）；再点取消、取消后刷新仍是未收藏 | **需要真后端**（`--server http://127.0.0.1:PORT`）+ `PR_OTP_DEV_ECHO=1`（要建两个号）+ 列表非空；构建必须 `VITE_MOCK_POSTS=0 VITE_MOCK_FAVORITES=0`（**且 `cd client` 再 build**，产物落在 `client/dist`）。它与 `verify-post-actions` 的分工：那个只用一账号判点赞律（乐观/在途锁/回滚/轻提示），"收藏是私人书签"必须有第二个视角才判得了，所以单独一个脚本 |
| `verify-profile` | **他人档案页（票 #68）真机验收**：游客看得到名片与「TA 的帖子」块（作者内容端点免登录）；块序（游客：名片→帖→房；登录：名片→关系动作→帖→房→计数）；帖块只含作者本人的帖、列表项是真链接；**没有「收藏」区块**（私人书签他人不可见）；关注/粉丝数是纯数字、区里零 `<a>`；bob 拉黑 alice 后 alice 的档案页帖块清空，但**同一篇帖的详情仍 200**（票 #42 §4）；不存在的 id 与被封禁的账号是**同一个**「用户不存在」空态；本人访问自己的 `/user/:id` 出现「这是别人看到的你的公开页」提示 | **需要真后端**（`--server`）+ `PR_OTP_DEV_ECHO=1`（要建三个号）+ 放宽注册/登录限速；构建必须 `cd client` 且 `VITE_MOCK_POSTS=0 VITE_MOCK_USERS=0 VITE_MOCK_FOLLOWS=0`。**P7b（被封禁账号）额外需要 `--admin <文件>`**（两行：用户名、密码），缺它只 SKIP 那一条并打印原因，其余照跑。它刻意把「计数不可点」判在**登录视角**：`GET /api/follows/:userId` 需要登录，游客视角下计数块结构上不出现，拿游客态判它恒红 |
| `verify-me` | **档案双路由与 `/me` 管理台（票 #69）真机验收**：游客访问 `/me` **就地**渲染登录引导（不跳转、`?redirect=/me`、地址仍是 `/me`）；概览有标题/昵称输入框/我的帖子/我的放映室/三个分区 tab；`?tab=relations` 选中关系分区且四份名单 tab 在场、互关名单真的列出人；`?list=blocked` 选中黑名单且行内**只给「解除拉黑」**；点名单 tab 写回 URL（`tab` 与 `list` 同时在场）；`?tab=settings` 槽位存在并钉住地址；`?tab=bogus` 回落概览；**顶栏头像落点 `/me`**；「查看我的公开主页」指向 `/user/1`；本人 `/user/1` 上只有公开视角提示 + 去管理台链接、**没有**昵称编辑（旧双视角已退役）；`/me` 上头像位点亮且分区图标全灭；源码级「`tab`/`list` 键名只在 `utils/meRoute.ts`」 | **不需要后端**（自起静态服务器 + `/api/*` 回 404，跑 mock 域），**须先 build**。它同时是 `verify-avatar` A2c 的姊妹判据：那条从本票起断言顶栏头像落 `/me`（**改过**，别再改回 `/user/1`） |
| `verify-follow-red-dot` | **粉丝红点（票 #67）真机验收**：有人关注后红点为 1 而**消息未读仍为 0**（两数两源）、顶栏「好友」分区角标出现、在线时底部飘一条含**对方名字**的提示、点提示落到 `/friends?tab=followers`、进 tab 后顶栏与 tab 角标**双灭**、服务端 `newCount` 也归零（水位线真落库）、互关提示说「好友」且红点**不亮**、取关一律静默（无新提示）、`ToastLayer.vue` 有 reduced-motion 分支且**只取消动画不取消提示**、源码级「红点数没有写进 `stores/auth.ts`」（决议 §3） | **需要真后端**（`--server`）+ `PR_OTP_DEV_ECHO=1` + 放宽注册/登录限速；构建必须 `cd client` 且 `VITE_MOCK_FOLLOWS=0 VITE_MOCK_FRIENDS=0 VITE_MOCK_USERS=0`。**脚本自带 `/ws/user` 的 Upgrade 反代并把 `origin` 改写成后端地址** —— 后端 Origin 白名单不含脚本的随机端口，让用户去改实例配置是错的解法。**账号名不能带连字符**（后端规则是 `[A-Za-z0-9_]`，带 `-` 会 400）。R6 是那个「GORM 不补括号」SQL 缺陷的回归哨兵：它判的是「互关之后红点不该亮」，而互关那条边恰好比水位线新 |
| `verify-email-change` | **自助换绑邮箱（票 #72）真机验收**：设置区有「换绑邮箱」块；填新址发码后出现验证码输入框且提示**点名新地址**；错误码给人话且邮箱没变；正确码确认成功并提示「邮箱已更新」；只读块的邮箱与状态都换成新的；`/api/auth/me` 一致；**旧地址立刻失效**（拿旧地址走找回密码拿不到码）而**新地址拿得到**（这就是「找回密码已指向新邮箱」的机制证明）；源码级换绑端点只在 `client/src/api/auth.ts` 出现 `/api/me/email`、没人把它拼进 `AUTH_BASE` | **需要真后端**（`--server`）+ `PR_OTP_DEV_ECHO=1` + 放宽注册/登录限速；构建必须 `cd client` 且 `VITE_MOCK_USERS=0 VITE_MOCK_POSTS=0`。**E7b 必须等满 62 秒再单次请求**：冷却 60s 按 user_id 算、每邮箱闸门（默认 5 次/小时）按收件地址算，写成轮询会自己把配额烧光变成 429。**脚本的反代必须用 `http.request` 逐字节转发**，用 `fetch(url,{body:req})` 会静默丢掉请求体 —— 症状是写操作全 502、只读页面正常 |
| `verify-account-deletion` | **注销冷静期（票 #73）真机验收**：危险区六块都在、密码空或未勾选时提交按钮 disabled；勾选+密码后可点，点它先展开**页内** `role=alertdialog` 确认条（不是 `window.confirm`）；提交后提示出现、横幅含「还有 N 天」与撤销按钮；**他视角六条全灭**（档案 404、搜索无、关注名单无、查关系 404、关注 404、发私聊 404）；帖仍 200 且署名「已注销用户」；冷静期内本人 `/api/auth/me` 仍 200；点撤销后横幅消失且**六条全部复原**；取关再关注仍可写；源码级 `/api/me/deletion` 只在 `client/src/api/auth.ts`；`--purge` 另判到期真删（**同名账号能重新注册**才是行被删的证据，只看 404 分不清"标记着"与"删了"） | **需要真后端**（`--server`）+ `PR_OTP_DEV_ECHO=1` + 放宽注册/登录限速；构建必须 `cd client` 且 `VITE_MOCK_USERS=0 VITE_MOCK_POSTS=0 VITE_MOCK_FOLLOWS=0`。`--purge` 要求实例把 `PR_DELETION_GRACE` 压到分钟级、`PR_DELETION_SWEEP_INTERVAL=1s`。**踩坑**：搜索结果的键是 `userId` 不是 `id`（写成 `id` 恒 undefined，那条判据永远不红 = 假绿）；关注端点幂等返回 **201（新建）/200（已存在）**，只认一个会把正常状态误判成失败 |
| `verify-me-settings` | **设置区改密与退出所有设备（票 #71）真机验收**：`?tab=settings` 选中设置；三块都在（改密码 / 退出所有设备 / 账号信息）；加入时间显示**精确绝对日期**而不是「刚刚」；错当前密码给人话且**不影响当前会话**；两次新密码不一致前端就地拦下且口令没变；弱口令报策略原因；改密成功后提示「其他设备已退出登录」、当前设备**仍在线**且 token 换成新的一枚；另一台设备的旧 access **401**、旧 refresh **刷不动**；旧口令登录失败、新口令成功；点「退出所有设备」先出**页内确认条**（不是 `window.confirm`），确认后变游客态但地址仍停在 `/me?tab=settings`、本机 token 清空；刚才那枚 token 也 401 | **需要真后端**（`--server`）+ `PR_OTP_DEV_ECHO=1` + 放宽注册/登录限速；构建必须 `cd client` 且关掉 mock 域（至少 `VITE_MOCK_USERS=0`）。**S1c 判的是可见文案里的 `YYYY-MM-DD`**：`ui/RelativeTime.vue` 的 `absolute` prop 把可见文案也换成绝对时间，只断言 `title` 会漏掉「文案还是『刚刚』」这种退化。**S9 是 `logoutAll` 那个缺陷的回归哨兵** —— 它曾在没有调用方时悄悄不带 token |
| `verify-me-favorites` | **`/me` 收藏区块（票 #65）真机验收**：三类型 tab（帖子/番剧/放映室）与顺序、`?type=` 进 URL；新收藏排最上；放映室**取消公开后条目仍在**、标题退化成「已失效的内容」、不是链接、带「已失效：…」提示、旁边有「移除」；点移除后列表空且提示「已从收藏里移除」，**刷新后仍不在**；空态是一句人话（不含「暂无数据」）；游标分页 `limit=1` 两页不重不漏；`/user/:id` 本人与他人**都没有**收藏块；源码里没有 `favoriteCount` | **需要真后端**（`--server`）+ `PR_OTP_DEV_ECHO=1` + 放宽注册限速；构建必须 `cd client` 且 `VITE_MOCK_POSTS=0 VITE_MOCK_FAVORITES=0`。**放映室元数据是异步落库的（I4）**，所以 `createPublicRoom` 必须轮询 `PATCH /api/rooms/:id/meta` 直到 `isPublic === true` —— 直接 PATCH 会撞上"内存有房、库里还没这一行"的窗口，title 被写成空串、isPublic 仍 false，G2/G5 必红。它自带的 `waitEval` 是本地实现：`lib/browser.mjs` 的 `waitFor(fn, opts)` 收的是**函数**不是 (cdp, 表达式) |
| `verify-following` | **关注流（票 #66）真机验收**：游客点「关注」pill **不被弹走**（仍停在 `/posts`，URL 保住 `?sort=following`）且就地渲染登录引导 + `?redirect=`；已登录 0 关注时空态是「还没有关注任何人」+ 深链 `/friends?focus=search`；关注某人后 TA 的普通帖**与板块主题串**都进流、未关注的人不进；取关后立刻从流里消失并回到空态；`latest` 的口径不被改变（无泄漏）；三 pill 顺序与 `?sort=` 同步、刷新后选中态保得住、从详情页后退回列表选中态还在 | **需要真后端**（`--server`）+ `PR_OTP_DEV_ECHO=1`（要建三个号）+ **至少一个番剧**（`--anime <id>` 或 `/api/animes` 里能查到一个，否则判不了「主题串进流」而 SKIP）；构建必须 `VITE_MOCK_POSTS=0 VITE_MOCK_FOLLOWS=0`（**且 `cd client` 再 build**）。注册限速默认 5/min·burst 3，跑多轮要调 `PR_AUTH_REGISTER_PER_MINUTE`/`PR_AUTH_REGISTER_BURST`，否则第二轮 429。**F8 刻意不判"pill 之间来回后退"** —— 切 pill 走 `router.replace` 是有意的（每点一次 tab 都塞历史会把用户困在同一页），后退键真正要判的是"离开列表再回来选中态没丢" |
| `verify-friends` | **好友页重排（票 #58）真机验收**：URL 契约（`?tab=` 进页选中、未知值回落默认、点 tab 写回 URL）、带 `?q=` 进页自动搜且分享/刷新不丢、**防抖 300ms 与 ≥2 字符门槛**（打 1 个字不触发；停手后 URL 才出现 q）、清空搜索框回列表、游客登录墙含**禁用态搜索框**、结果行按关系态给 关注/已关注/回关/发消息、「…」菜单（查看档案/拉黑）、拉黑存续不给关注按钮、**未搜与 0 结果两态文案分开**、行结构契约（`.row`/`.who strong`/`.avatar`）仍在、以及源码级「`tab`/`q` 键名只有 `utils/friendsRoute.ts` 一个源」 | **不需要后端**（自起静态服务器 + `/api/*` 回 404，跑 mock 域），**须先 build**。它**刻意不用 fetch 钩子数请求** —— mock 域一个 HTTP 请求都不发，数出来恒为 0；防抖改判两个确定可观察的事实（打字瞬间 URL 干净 + 停手后 q 才出现）。「好友为空自动聚焦搜索框」也**判不了真机**（mock 的种子关注边让四个 tab 全非空），拆成「搜索框常驻可聚焦」+ 源码级 focus 联动两条。它同时钉住 `verify-avatar` 依赖的行结构，别为了新样式把它改掉 |
| `verify-my-content` | **我的帖管理与我的放映室（票 #70）真机验收**：`/me` 帖子块的删除按钮只在本人视角出现、删了立刻消失且刷新仍不在、被删帖对别人 404、别人删我的帖 403 且帖仍在、`/me` 同时列出私密房与公开房且私密带徽章、私密房不出现在他人档案页与公开房列表、`/api/rooms/mine` 未登录 401 | **需要真后端**：`--server`，实例要 `PR_OTP_DEV_ECHO=1` 与放宽注册限速（`PR_AUTH_REGISTER_PER_MINUTE=200`/`BURST=100`）；构建必须 `cd client` 且 `VITE_MOCK_POSTS=0 VITE_MOCK_USERS=0 VITE_MOCK_ROOMS=0`；**建公开放映室要轮询** `PATCH /api/rooms/:id/meta` 直到回显匹配 —— 元数据是异步落库的（I4），直接 PATCH 会撞上"内存有房、库里还没这一行"的窗口 |
| `verify-connection` | **协议层连通性**：后端 `/healthz`·`/api/ice`·`/api/public-rooms`·静态页面、Vite 页面与 `/api` 代理（识破 SPA 回退成 HTML）、`/ws` 直连与经代理升级、**Origin 白名单四态**（白名单内放行/白名单外 403/同主机异端口 403/无 Origin 放行） | **不依赖 Chrome**，几十毫秒出结论——"页面连不上后端"应先跑它再起浏览器。环境变量 `PR_BACKEND`/`PR_VITE`（`PR_VITE=''` 跳过代理层）/`PR_ALLOWED_ORIGIN`。**Origin 必须用原始 Upgrade 握手验证**：Node 内置 WebSocket 的第二参数只收子协议，传 `{headers:{Origin}}` 会被判非法子协议 → 1006，于是"被拒"与"不支持"同形 |
| （待补）`verify-public-rooms` / `verify-admin` | 公开房列表与管理端强关/下架/审计 | **尚未在仓库里**（计划 T8 的产出，因故未落地）；需要时按 §6 起实例 + 管理员凭据自建 |
| `lib/browser.mjs` | 公共库：`startChrome/openTarget/waitFor/createRoom/seedAndEnter/ensureAccount/lastAccount/newCookieJar/findChrome/argOf/percentile` | `createRoom` 会自动取测试账号（账号能力关闭时退化为匿名）；`seedAndEnter` 可传第 7 参 account 给主播窗口播种会话 |

CSP 会拦住"跨源本机媒体服务器"的注入路径（脚手架用法），本地矩阵可用 `PR_SECURITY_CSP` 临时放行 `http://127.0.0.1:*`，其余保持严格。

## 8. 部署事实（生产）

**2026-10-19 起改为 Docker Compose 部署**（原 systemd 单元已 `disable`，二进制仍留在 `/opt/projectionroom` 作回滚）。

- 服务器 **60.205.212.25**（阿里云 ECS，Ubuntu 24.04，2 vCPU / 1.6 GB）。对外只有 **80** 一个入口：nginx 反代到 `127.0.0.1:8080`。详见 `docs/DEPLOY-SINGLE-PORT.md`。
- **两个容器，都用 `network_mode: host`**（宿主机 PG 只监听回环，bridge 网络连不上）：
  - `projectionroom-app`：镜像 `ghcr.io/drayee/projectionroom:<sha>`，内存限额 700M。
  - `pr-minio`：`minio/minio:latest`，内存限额 256M（实测空载 71 MiB），数据在 `/opt/minio/data`。
  - ⚠ **宿主机连不上 Docker Hub**（`registry-1.docker.io` 超时）。MinIO 镜像是经 `docker save` + SFTP + `docker load` 送进去的；应用的镜像走 GHCR（可达）。**新镜像要么推 GHCR，要么同样手工 load**。
- **`/opt/projectionroom/.env`（root:600）** 含 `PR_DB_DSN`、`PR_JWT_SECRET`、SMTP、`PR_ADDR=127.0.0.1:8080`、`PR_ALLOWED_ORIGINS`（含 `http://60.205.212.25` 与 `:80` 两种形态）、以及 `PR_STORAGE_*` 六项。
- **`/opt/minio/.env`（root:600）** 含 `MINIO_ROOT_USER/_PASSWORD`，被 compose 的 minio 服务消费，也被应用侧的 `PR_STORAGE_ACCESS_KEY/_SECRET_KEY` 引用（轮换时两处都要改）。
- **对象存储**：桶 `pr-files`，匿名只读（只给 `s3:GetObject`，不给 ListBucket）。浏览器经 `http://60.205.212.25/files/pr-files/...` 访问（nginx `location /files/` 反代到 9000）。详见 `docs/STORAGE-MINIO.md`。
- PostgreSQL 16 在宿主机（回环），库 `projectionroom`（生产）+ `projectionroom_test`（测试）；角色 `pr_app`。**`pg_hba.conf` 曾临时改过 trust 用于导入，已从 `.bak` 恢复**。
- CI：`.github/workflows/build-push.yml` 推镜像到 GHCR（Secret 名 **`REGISTRY_TOKEN`**，classic PAT 带 `write:packages`）；`deploy.yml` 从 GHCR 拉镜像部署。⚠ Dockerfile **不能**带 `-tags wireinject`（会选中空壳 `cmd/wire.go`，`InitializeApp` 返回 nil → 启动即 SIGSEGV）。
- **前端在 CI 里默认走真实接口**：build-push 的「Build frontend」给六个 `VITE_MOCK_*` 注入 `vars.VITE_MOCK_* || '0'`（**不设仓库变量 = 翻真**；想退回 mock 才去仓库 Variables 设 `1`）。这与 `api/mode.ts` 的"本地默认 mock"**刻意相反** —— 2026-10-28 线上六个分区对所有人显示同一份硬编码假数据（`/messages?peer=2` 恒为 mock 的「放映员」），根因正是 CI 什么都没设、静默沿用了"默认 mock"。判据别用"bundle 里还有 mock 人名"（两份实现都进产物），看 CI 日志打印的开关快照或页面实际发的 `/api/*` 请求。
- **已知缺口**：无 TLS（refresh Cookie 因此不带 `Secure`，token/密码明文过网）；`client/dist/downloads/room-media` 本地约 3.95 GB，但 **`.dockerignore` 已排除**，镜像内不存在（该缺口在 Docker 部署下已消解）。

## 9. 工作约定（人与 AI 都适用）

- **语言**：注释与提交信息用中文；代码标识符用英文；字符串一律 **ASCII 引号**。
- **改文件**：用编辑工具（apply_patch / 编辑器）改源码——PowerShell 直接读写含中文的源文件会 GBK 乱码。
- **前端渲染**：一切用户可控内容走**文本插值**（`{{ }}`）；唯一例外是 Markdown 消毒出口 `ui/MarkdownView.vue`（`marked` 解析 + `DOMPurify.sanitize()` 后注入）。判据：`grep -rn "v-html" client/src` 命中 ≤1 且处在该文件；`grep -rn "innerHTML" client/src` 为 0。
- **`data-testid` 与主行动按钮文案是验收脚本的契约**：本轮前端重写只保留 3 个脚本消费 testid（`room-code`/`room-code-copied`/`room-code-copy-failed`），其余旧 testid 全部丢弃（票 #12）。新增可以，改名/删除会打挂 `test/script/**`（改之前先 `grep` 一遍谁在用）。
- **提交**：`<type>(<scope>): <描述>`，一次任务一次提交；`git push` 仅在明确要求时执行。
- **遇到语义不清、或需要人拍板的点**：**停下来问**（弹选项/提问窗等待），带着选项和你的推荐问。
- **AI 会话**：先读本文件，再按 §2 精确打开指到的那几个文件；需要全树认知时读本文件而不是遍历代码树。
- **看着像重复、但输出格式不同的工具函数不要合并**：例如 `service.humanBytes`（`"512 MiB"`）与 `segment.HumanBytes`（`"512.00 MiB"`）——合并会静默改日志文案并打挂测试。合并前先证明"逐字等价"。
- **改完 `internal/bootstrap/ProviderSet` 的 provider 列表后**：重新 `go tool wire ./cmd` 并核对生成代码里的创建序列与 cleanup 反序（`关服务 → flush 写队列 → 关连接池`）。

## 10. 待办与已知缺口（按优先级）

1. **无 TLS**：上 TLS 后立刻给 cookie 补 `Secure` 并收紧 `SameSite`（有测试断言钉住当前行为，改它要先改断言）。
2. **论坛/番剧板块**：帖子三页（`/posts`×3）、好友（`/friends`）、私聊（`/messages`）、他人公开档案与本人资料编辑（`/user/:id`）、官方/推荐放映区（`/official`）、番剧论坛（`/forum` 目录 → `/forum/:animeId` 板块 → `/posts/:id` 主题串）**均已前后端贯通**（后端 `#27`/`#29`/`#39`/`#31`/`#33`/`#35`/`#37`/`#38`，前端 `#26`/`#28`/`#30`/`#32`/`#34`/`#36`）。地图 #21 的「留空界面」至此全部落地；只剩 `/about` 是内容占位（旧图 Out of scope，等作者提供内容）。
3. ~~`GET /api/rooms/mine`（"我的房间"目前只读本标签页 `sessionStorage`）~~ **已在票 #70 落地**（房主全部房，含未公开与已关闭）。仍缺的是「房间页里的"我的房间"入口」——现在只有 `/me` 的「我的放映室」块，房间页/公开房页还没有回链。
4. `client/dist/downloads/room-media` 移出静态根（公网可下载）。
5. 房间元数据"投递失败只记日志"：补偿（启动对账/重投）待公开列表扩大后一起做。
6. `CLAUDE.md` 自身：结构或约定变更后**同步更新**。
