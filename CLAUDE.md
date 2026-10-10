# CLAUDE.md —— 月喵（ProjectionRoom）项目导引

> **这是 AI 会话进入本项目后的唯一入口。默认只读本文件**，需要动某处时再按下面的路径精确打开那几个文件，不要重新遍历整棵树。
>
> **维护约定（必须遵守）**：目录结构、关键约定、命令、部署事实一旦变化，**同步更新本文件**。过时地图比没有地图更危险——后续会话会按它行动。
> 最近一次同步：结构整理（`usecase` 并入 `service`、`auth`/`limiter` 下沉、`cmd` 根包只留三文件、provider 迁入 `internal/bootstrap`）之后。

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
| `cmd/` | **根包只有三个文件**：`main.go`(最薄骨架：读配置 → `InitializeApp` → 起服务 → 优雅退出，无业务代码) · `wire.go`(**只有** `//go:build wireinject` 头 + `InitializeApp`，`wire.Build(bootstrap.ProviderSet)`，无业务代码) · `wire_gen.go`(`go tool wire ./cmd` 生成)。**provider 全在 `internal/bootstrap/`**。另有独立 main 包 `cmd/segmenter/`(本地切片器 CLI，被 `scripts/build-segmenter.ps1` 交叉编译成用户下载的 exe；它的改动会影响构建脚本与 `docs/SEGMENT.md`) |
| `client/` | Vue 3 + TS + Vite：`src/views/`、`src/components/`、`src/stores/`、`src/api/`、`src/utils/`、`src/gen/`(生成的原型代码)、`public/icons/`(**本地 SVG 图标，用多少复制多少**)、`scripts/prune-dist.mjs`(构建后清理陈旧产物) |
| `test/script/` | 真机验收脚本（headless Chrome，见 §6） |
| `test/resource/` | 媒体夹具（`short_video/cut`、`mkw_cut` 等） |
| `docs/` | `SPEC.md`(房间与信令协议)、`ALGORITHM.md`、`SEGMENT.md`、`ACCOUNTS.md`(账号/社交/管理端规格)、`plans/`(实施计划)、`reports/`(验收报告) |

## 3. 关键协议与约定

- **protobuf 是唯一真源**：`proto/projection_room.proto` → 生成 `internal/pb/*.go` 与 `client/src/gen/*.ts`（`scripts/gen-proto.ps1`）。
- **WS 信封**：1 字节类型前缀 + protobuf 体；room 信令走 `/ws?roomId=<码>&clientId=<id>[&ticket=<票据>]`。
  - **无 `ticket` = 游客**（观众免登录进房是产品决定，别加拦截）；带票据则绑定账号；票据单次使用 + 30s 过期。
  - **Origin 白名单**：`PR_ALLOWED_ORIGINS` 是**唯一**判据（不再"同源隐式放行"；DNS rebinding 曾据此绕过）。
- **DataChannel**：negotiated id=0，1 字节 kind 前缀（`0x01` 控制/`0x02` init/`0x03` 媒体/`0x04` tail）+ 7 字节媒体头；64 KiB 分片 + `bufferedAmount` 背压。
- **房间码**：`^[A-Z0-9]{4,12}$`；建房**必须登录**且响应下发一次性 **`hostToken`**（主播断线宽限期内接回主播位必须携带）。
- **主播宽限期**：`PR_ROOM_HOST_GRACE`（默认 60s）内房间保留，宽限期内**永不回收**；客户端用同一房间码自动重建。
- **ICE**：TURN 已彻底退役；服务端周期性实测 STUN 并打分下发（最快 2 条 + 健康粘性 2 条恒选），带 `ttlSeconds`/`expiresAt`/`probe`。
- **解码前预算**：`internal/model/wire.go` 用 protowire 扫 repeated 元素数，**先于** `Unmarshal` 拒绝（曾出现"4 MiB 帧展开成 140 万个对象 → 3.9 GB"）。

## 4. 账号 / 社交 / 管理端（现状）

- **混合式会话**：refresh = HttpOnly Cookie 里的不透明串（库里只存 sha256，每次刷新**轮换** + **重放检测→撤销全部会话**）；access = 15 分钟 JWT，**只存内存 + `sessionStorage`**（**不进 localStorage**）。
- 前端缓存的 `pr:profile` 只用于显示，**不是授权依据**。
- **DB 表**：`users` / `sessions` / `rooms_meta` / `admin_audit` / `schema_migrations`（二期又加了房间元数据读接口；`friendships`/`messages`/`posts` 属后续阶段）。
- **迁移**：内嵌有序 DDL，启动自动补齐，**只做加法**；`schema_migrations` 记版本。
- **API 速查**：`/api/auth/{register,login,refresh,logout,logout-all,me,ws-ticket}`、`/api/rooms`(POST 建房，需登录)、`/api/rooms/:roomId`(信息)、`/api/rooms/:roomId/meta`(GET 仅房主读回 / PATCH 房主改)、`/api/public-rooms`(免登录)、`/api/ice`、`/api/v1/segment/*`、`/api/admin/{users,rooms,metrics,logs,audit}` + `/api/admin/rooms/:roomId/{close,unpublish}`。
- **账号能力开关**：`PR_DB_DSN` 为空 = 账号关闭（不连库、不注册 `/api/auth/*` 与 `/api/admin/*`，游客照常进房）。这是刻意的**两步部署**路径。

## 5. 配置（环境变量）

全部定义与默认值在 `internal/config/config_struct.go` + `config_load.go`（每个字段都有中文注释），用户可见的表在 `README.md`。本节只记**从默认值看不出**的三件事：

- **账号能力总开关**：`PR_DB_DSN` 为空 ⇒ 账号整体关闭（不连库、不注册 `/api/auth/*` 与 `/api/admin/*`，游客照常进房）。这是刻意的**两步部署**路径。配了 DSN 而 `PR_JWT_SECRET` 少于 32 字节 ⇒ **拒绝启动**（缺密钥的症状是"所有人随机掉线"，极难归因）。
- **`PR_TRUSTED_PROXIES` 默认只信回环**：配错（把直连来源也标为可信）会让按 IP 限速退化成全局限速。
- **`PR_SECURITY_CSP` 是本地矩阵的开关**：默认严格 CSP 会拦住脚手架注入的跨源本机媒体服务器；本地跑验收矩阵时临时放行 `http://127.0.0.1:*`。

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
npm --prefix client run build              # 含 vue-tsc；产物必须 0 个 window.__pr
```

**真库测试**（`internal/store` 的用例依赖它，否则 Skip）：
```powershell
$env:PR_TEST_DB_DSN = (Get-Content "$env:TEMP\pr-deploy\test-dsn-5433.txt" -Raw).Trim()   # 经 SSH 隧道到服务器上的测试库
go test ./internal/store/ -count=1
```
⚠️ **两个坑**：① 测试库与生产库是**两个库**，DSN 的 `dbname` 必须含 `_test`（用例里有护栏）；② **`internal/store` 的真库用例会 TRUNCATE 表**，所以任何手工 fixture（如验收用的管理员账号）在跑过它们之后就不在了——需要重新注册并提权（`UPDATE users SET role='admin' WHERE username='…'`）。
⚠️ **测试实例收尾必须停进程**：曾有一个后台实例直连测试库、绕过了用例的跨进程隔离锁，导致 store 用例随机红。
⚠️ **并存会话的 fixture 会被清掉**：`public.users` 被 store 用例 TRUNCATE ⇒ 刚 seed 的管理员可能在几分钟后消失、`verify-auth` 因此假红。规避办法是**在同一个 `projectionroom_test` 里建隔离 schema**（DSN 追加 `search_path=pr_xxx`），在其中造 fixture、跑完 `DROP SCHEMA`；`public` 侧不留痕。
⚠️ 起实例做真机验收需要调试钩子（既有脚本依赖 `window.__pr`，而**生产构建刻意没有**）：`$env:VITE_DEBUG_HOOKS='1'; npx vite build --outDir dist-debug` + `PR_STATIC_DIR=client/dist-debug`，跑完删掉并重建干净 `dist`。

## 7. 验收脚本（`test/script/*.mjs`，真机 headless Chrome）

| 脚本 | 覆盖 | 注意 |
| :--- | :--- | :--- |
| `verify-m2` | 起播与同步偏差 | 需媒体夹具 + 调试钩子 |
| `verify-room-resume` | 主播断线/宽限期/同码重建 | **`--grace` 必须与服务端 `PR_ROOM_HOST_GRACE` 对齐**；素材用默认 `short_video/cut`；服务端自报 grace 要一致 |
| `verify-ice` | ICE 下发/刷新/重启 | 实例需 `PR_ICE_TTL=30s` |
| `verify-health` | 卡顿换父与健康度 | 素材用 `mkw_cut`（短素材会假失败） |
| `verify-auth` | 账号 30 判据（含重放撤销、封禁断连、游客进房） | 需管理员凭据（`--admin <文件>`），测试库里的管理员会被 store 用例清掉 |
| `verify-auth` | 账号 34 判据（含重放撤销、封禁断连、游客进房、**`forgot` 不泄露账号存在性**） | 需管理员凭据（`--admin <文件>`），测试库里的管理员会被 store 用例清掉。**换环境时**：`forgot` 的期望值取决于实例是否配了 SMTP（未配=两条路径都 503，已配=都 200），脚本会先探测并打印它选了哪条 |
| （待补）`verify-public-rooms` / `verify-admin` | 公开房列表与管理端强关/下架/审计 | **尚未在仓库里**（计划 T8 的产出，因故未落地）；需要时按 §6 起实例 + 管理员凭据自建 |
| `lib/browser.mjs` | 公共库：`startChrome/openTarget/waitFor/createRoom/seedAndEnter/ensureAccount/lastAccount/newCookieJar/findChrome/argOf/percentile` | `createRoom` 会自动取测试账号（账号能力关闭时退化为匿名）；`seedAndEnter` 可传第 7 参 account 给主播窗口播种会话 |

CSP 会拦住"跨源本机媒体服务器"的注入路径（脚手架用法），本地矩阵可用 `PR_SECURITY_CSP` 临时放行 `http://127.0.0.1:*`，其余保持严格。

## 8. 部署事实（生产）

- 服务器 **60.205.212.25**（阿里云 ECS，Ubuntu 24.04，2 vCPU / 1.6 GB）。访问入口：nginx 80 反代到 `127.0.0.1:8080`。
- `systemd` 单元 `projectionroom.service`：**非 root**（`User=projectionroom`）、沙箱（`ProtectSystem=strict`/`PrivateTmp`/`NoNewPrivileges`/`RestrictAddressFamilies`）、cgroup 限额（`MemoryMax=700M`/`CPUQuota=150%`）、`EnvironmentFile=/opt/projectionroom/.env`（**root:600**，含 `PR_DB_DSN` 与 `PR_JWT_SECRET`）。
- 应用**只监听回环**（公网只能经 nginx）；8080 曾有安全组规则，现已无害。
- PostgreSQL 16 在本机（回环），库 `projectionroom`（生产）+ `projectionroom_test`（测试）；角色 `pr_app`（最小权限）。
- 回滚点：`/root/pr-backup-20261007-161941/`（nginx 站点配置、旧 unit、schema 备份、旧二进制与 dist）。
- **已知缺口**：无 TLS（refresh Cookie 因此不带 `Secure`，token/密码明文过网）；`client/dist/downloads/room-media`（约 3.95 GB）仍在静态根里，**对公网可下载**。

## 9. 工作约定（人与 AI 都适用）

- **语言**：注释与提交信息用中文；代码标识符用英文；字符串一律 **ASCII 引号**。
- **改文件**：用编辑工具（apply_patch / 编辑器）改源码——PowerShell 直接读写含中文的源文件会 GBK 乱码。
- **前端渲染**：一切用户可控内容走**文本插值**（`{{ }}`）。可测试的判据：`grep -rn "v-html\|innerHTML" client/src` 应为 0。
- **`data-testid` 与主行动按钮文案是验收脚本的契约**：新增可以，改名/删除会打挂 `test/script/**`（改之前先 `grep` 一遍谁在用）。
- **提交**：`<type>(<scope>): <描述>`，一次任务一次提交；`git push` 仅在明确要求时执行。
- **遇到语义不清、或需要人拍板的点**：**停下来问**（弹选项/提问窗等待），带着选项和你的推荐问。
- **AI 会话**：先读本文件，再按 §2 精确打开指到的那几个文件；需要全树认知时读本文件而不是遍历代码树。
- **看着像重复、但输出格式不同的工具函数不要合并**：例如 `service.humanBytes`（`"512 MiB"`）与 `segment.HumanBytes`（`"512.00 MiB"`）——合并会静默改日志文案并打挂测试。合并前先证明"逐字等价"。
- **改完 `internal/bootstrap/ProviderSet` 的 provider 列表后**：重新 `go tool wire ./cmd` 并核对生成代码里的创建序列与 cleanup 反序（`关服务 → flush 写队列 → 关连接池`）。

## 10. 待办与已知缺口（按优先级）

1. **无 TLS**：上 TLS 后立刻给 cookie 补 `Secure` 并收紧 `SameSite`（有测试断言钉住当前行为，改它要先改断言）。
2. **论坛/帖子、好友、私聊、消息**：尚未实现（数据模型与协议另立计划）。
3. `GET /api/rooms/mine`（"我的房间"目前只读本标签页 `sessionStorage`）。
4. `client/dist/downloads/room-media` 移出静态根（公网可下载）。
5. 房间元数据"投递失败只记日志"：补偿（启动对账/重投）待公开列表扩大后一起做。
6. `CLAUDE.md` 自身：结构或约定变更后**同步更新**。
