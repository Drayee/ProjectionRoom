# CLAUDE.md —— 月喵（ProjectionRoom）项目导引

> **这是 AI 会话进入本项目后的唯一入口。默认只读本文件**，需要动某处时再按下面的路径精确打开那几个文件，不要重新遍历整棵树。
>
> **维护约定（必须遵守）**：目录结构、关键约定、命令、部署事实一旦变化，**同步更新本文件**。过时地图比没有地图更危险——后续会话会按它行动。
> 最近一次同步：注册/改密码改成**邮箱验证码**（两段式注册 + 页内人机验证子窗体 + 幂等键），并把「配置不全也启动」改成 **`requireComplete` 必需配置齐全才启动**；同时补了 `client/.env.example` 与 README 的账号层章节。

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
| `client/` | Vue 3 + TS + Vite（月夜故障 · B 重建后）：`src/pages/`、`src/components/{ui,features}/`、`src/styles/{tokens,base}.css`、`src/stores/`、`src/api/`、`src/utils/`、`src/gen/`(生成的原型代码)、`public/icons/`(**本地 SVG 图标，用多少复制多少**)、`public/fonts/`、`scripts/prune-dist.mjs`(构建后清理陈旧产物)。旧 `src/views/` 与根级 `src/components/` 已删（零残留） |
| `test/script/` | 真机验收脚本（headless Chrome，见 §6） |
| `test/resource/` | 媒体夹具（`short_video/cut`、`mkw_cut` 等） |
| `docs/` | `SPEC.md`(房间与信令协议)、`ALGORITHM.md`、`SEGMENT.md`、`ACCOUNTS.md`(账号/社交/管理端规格)、`plans/`(实施计划)、`reports/`(验收报告) |

## 3. 关键协议与约定

- **protobuf 是唯一真源**：`proto/projection_room.proto` → 生成 `internal/pb/*.go` 与 `client/src/gen/*.ts`（`scripts/gen-proto.ps1`）。
- **WS 信封**：1 字节类型前缀 + protobuf 体；room 信令走 `/ws?roomId=<码>&clientId=<id>[&ticket=<票据>]`。
  - **无 `ticket` = 游客**（观众免登录进房是产品决定，别加拦截）；带票据则绑定账号；票据单次使用 + 30s 过期。
  - **Origin 白名单**：`PR_ALLOWED_ORIGINS` 是**唯一**判据（不再"同源隐式放行"；DNS rebinding 曾据此绕过）。默认值覆盖两种**本机**入口：开发态 `5173` 与单端口态 `8888`（后者是 `go run ./cmd` 的默认形态，漏了它会让默认部署开箱即坏：页面能开、`/ws` 一律 403）。**公网/隧道部署必须显式加域名**。
- **DataChannel**：negotiated id=0，1 字节 kind 前缀（`0x01` 控制/`0x02` init/`0x03` 媒体/`0x04` tail）+ 7 字节媒体头；64 KiB 分片 + `bufferedAmount` 背压。
- **房间码**：`^[A-Z0-9]{4,12}$`；建房**必须登录**且响应下发一次性 **`hostToken`**（主播断线宽限期内接回主播位必须携带）。
- **主播宽限期**：`PR_ROOM_HOST_GRACE`（默认 60s）内房间保留，宽限期内**永不回收**；客户端用同一房间码自动重建。
- **ICE**：TURN 已彻底退役；服务端周期性实测 STUN 并打分下发（最快 2 条 + 健康粘性 2 条恒选），带 `ttlSeconds`/`expiresAt`/`probe`。
- **解码前预算**：`internal/model/wire.go` 用 protowire 扫 repeated 元素数，**先于** `Unmarshal` 拒绝（曾出现"4 MiB 帧展开成 140 万个对象 → 3.9 GB"）。

## 4. 账号 / 社交 / 管理端（现状）

- **混合式会话**：refresh = HttpOnly Cookie 里的不透明串（库里只存 sha256，每次刷新**轮换** + **重放检测→撤销全部会话**）；access = 15 分钟 JWT，**只存内存 + `sessionStorage`**（**不进 localStorage**）。
- 前端缓存的 `pr:profile` 只用于显示，**不是授权依据**。
- **DB 表**：`users`（含 `email` + `email_verified_at`，`lower(email)` 上有**部分唯一索引**）/ `sessions` / `password_resets`（验证与重置**共用**，靠 `purpose` 区分：`email_verify` / `password_reset` / **`email_otp`**；只存 token 的 sha256；本轮新增 `attempts` 作验证码错误计数）/ **`register_drafts`**（两段式注册的中间态：已算好的 bcrypt + 待验证邮箱）/ **`idempotency_keys`**（写操作重放保护，`(scope,key)` 主键）/ `rooms_meta` / `admin_audit` / `schema_migrations` / **`friendships`** / **`posts` + `post_comments` + `post_likes`** / **`anime`** / **`messages`**（`kind=dm` 走通，`kind=room` 的列与索引已建但写入路径未做）。（`kind=room` 房间内聊天与 30 天清理属后续阶段。）
- **注册与改密码（本轮重做）**：注册是**两段式**——`register/start`（账号+密码，返回一次性 `ticket`，**不建号**）→ `register/email`（发验证码）→ `register/verify`（建号+会话）。建号即视为**邮箱已验证**（验证码刚证明了归属），因此注册响应里的 `emailVerified` 是 true。**注册不收昵称**：默认等于用户名（`SanitizeDisplayName` 的兜底），之后在设置页改。改密码走 `password/otp` + `password/otp/reset`（与链接令牌那条 `forgot`/`reset` 并存）。旧的链接式找回仍然可用。
- **验证码的三道防线**：6 位数字、5 分钟 TTL、**错误次数落库**（`attempts`，5 次即作废，重启不清零）+ 发送冷却 60s + 每 IP/每邮箱限流。人机验证是**前端页内子窗体**（`CaptchaDialog.vue`），定位是**摩擦而非安全边界**（题面与校验都在前端）。
- **写操作幂等**：三个注册端点与改密端点接受 `Idempotency-Key`（UUID）。同键同体只执行一次（双击不建两个号、不让首封验证码立刻作废）；**同键不同体返回 409**。只有成功才落键——失败往往正是值得重试的那一类。
- **邮箱与找回**：**只有验证过的邮箱能用于找回**；管理员 `PATCH /api/admin/users/:id {email}` 绑定邮箱时会**同时标记已验证**（管理端 UI 已有该入口）。重置邮件里是一次性链接 `<PR_PUBLIC_BASE_URL>/reset#token=…`（token 在 **fragment** 里，故不进服务端访问日志）；验证链接同理走 `/verify#token=…`。
- **迁移**：内嵌有序 DDL，启动自动补齐，**只做加法**；`schema_migrations` 记版本。⚠ `CREATE TABLE IF NOT EXISTS` **不会给已存在的表补列**：凡是在建表之后又加的列，必须另写一条 `ADD COLUMN IF NOT EXISTS`（本轮 `register_drafts` 踩过，被 schema 测试抓到）。
- **API 速查**：`/api/auth/{register,login,refresh,logout,logout-all,me,ws-ticket}`、**`/api/auth/register/{start,email,verify}`**（两段式注册）、**`/api/auth/password/otp` + `/api/auth/password/otp/reset`**（验证码改密）、**`/api/auth/forgot`**（恒定 200，不泄露账号是否存在）、**`/api/auth/reset`**、**`/api/auth/verify-email`**（注意是 `verify-email` 而不是 `verify`）、`/api/rooms`(POST 建房，需登录)、`/api/rooms/:roomId`(信息)、`/api/rooms/:roomId/meta`(GET 仅房主读回 / PATCH 房主改)、`/api/public-rooms`(免登录)、`/api/ice`、`/api/v1/segment/*`、`/api/posts`(列表/详情/发帖/点赞/评论)、`/api/animes`(免登录目录)、`/api/friends`+`/api/friends/requests`+`/api/blocks`+`/api/users/search`(好友域)、**`/api/users/:id`（公开档案，免登录；不存在与被封禁都回 404，防存在性探测）+ `PATCH /api/me`（改自己昵称，需登录）**、**`/api/messages`（`?peer=&before=` 历史 / POST 发送）+ `/api/messages/conversations` + `/api/messages/read`**（私聊）、`/api/admin/{users,rooms,metrics,logs,audit}` + `/api/admin/rooms/:roomId/{close,unpublish}`。
- **未读数只有一个写入点**（wayfinder #30）：`stores/auth.ts` 的 `setUnread()`。两个来源都往它写 —— 会话列表的合计（mock 与 real 都走这条）与 `/me` 的 `unread`（**仅 real 域**，见 `applyServerUnread`）。为什么 mock 域要拒掉 `/me`：mock 的会话是前端内存里的假数据，而 `/me` 打真后端回的 `unread: 0` 与界面上的数字不同源，两个写入点互相覆盖的症状是"顶栏角标时有时无"。**`api/messages.ts` 门面刻意不导出 `unreadTotal()`**，多一个取数入口就等于多一个写入点。
- **`/messages?peer=<id>` 是跨票契约**（`utils/dmRoute.ts`）：`#28` 的「发消息」按它生成链接，`#30` 的消息页从 URL 解析会话。选中会话 = `router.replace(conversationPath(id))`，**没有只改内存的选中态** —— 刷新、新标签页、后退键因此自然成立。
- **启动完整性（本轮的策略变更）**：**配置必须齐全才启动**——缺 `PR_DB_DSN` / `PR_JWT_SECRET`(≥32字节) / `PR_PUBLIC_BASE_URL` / SMTP 三件套（或 `PR_OTP_DEV_ECHO=1`）/ 前端产物（`PR_SERVE_STATIC=true` 时）任一项，`config.requireComplete` 会**逐条列出并拒绝启动**。这取代了早先"缺 DB 就关账号、缺 SMTP 就 503"的运行期降级（那让"功能不全"要等用户点开才发现）。要跑功能不全的窄实例（只跑信令、单测）必须**显式**写 `PR_REQUIRE_COMPLETE=0`。**代码里"账号能力关闭"的分支仍在**（`PR_DB_DSN` 为空时不注册 `/api/auth/*`，返回 404），但它现在只在显式跳过校验时可达；前端保留"把这个 404 解释成游客模式"的分诊（`useAuthForm.ts`），因为那是对**降级实例**的正确解读。

## 5. 配置（环境变量）

全部定义与默认值在 `internal/config/config_struct.go` + `config_load.go`（每个字段都有中文注释），用户可见的表在 `README.md`。本节只记**从默认值看不出**的三件事：

- **必需配置齐全才启动**（见 §4 的"启动完整性"）：缺任何一项由 `config.requireComplete` 在启动期拦下并逐条列出。**要跑窄实例必须显式写 `PR_REQUIRE_COMPLETE=0`**——这道闸默认开启，因为"漏配"与"刻意跳过"在日志里长得一样，而后果完全不同。
- **`PR_TRUSTED_PROXIES` 默认只信回环**：配错（把直连来源也标为可信）会让按 IP 限速退化成全局限速。
- **`PR_SECURITY_CSP` 是本地矩阵的开关**：默认严格 CSP 会拦住脚手架注入的跨源本机媒体服务器；本地跑验收矩阵时临时放行 `http://127.0.0.1:*`。
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

⚠️ **`PR_ALLOWED_ORIGINS` 在换端口时必须跟着改**：`run-local.ps1` 会自动补，但手工起实例时忘了它就是"页面能开、`/ws` 一律 403"（默认白名单只含 5173 与 8888）。

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
| `verify-auth` | 账号 **34** 判据（重放撤销、封禁断连、游客进房、**`forgot` 不泄露账号存在性**） | 需管理员凭据（`--admin <文件>`），测试库里的管理员会被 store 用例清掉。**换环境时**：`forgot` 的期望值取决于实例是否配了 SMTP（未配=两条路径都 503，已配=都 200），脚本会先探测并打印它选了哪条 |
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
2. **论坛/番剧板块与官方分区**：帖子三页（`/posts`×3）、好友（`/friends`）、私聊（`/messages`）、他人公开档案与本人资料编辑（`/user/:id`）均已前后端贯通（后端 `#27`/`#29`/`#39`/`#31`/`#33`，前端 `#26`/`#28`/`#30`/`#32`）。剩余两张**前端**票：`/forum`（`#36`，后端 `#38` 已就绪）与 `/official`（`#34`，后端 `#35` 已就绪）——这两个页面现在仍是 `ComingSoonPage` 占位。
3. `GET /api/rooms/mine`（"我的房间"目前只读本标签页 `sessionStorage`）。
4. `client/dist/downloads/room-media` 移出静态根（公网可下载）。
5. 房间元数据"投递失败只记日志"：补偿（启动对账/重投）待公开列表扩大后一起做。
6. `CLAUDE.md` 自身：结构或约定变更后**同步更新**。
