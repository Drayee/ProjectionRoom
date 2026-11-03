# 月喵 · ProjectionRoom

> 主播播放**本地**视频，观众通过 WebRTC DataChannel 同步观看 —— 像 B 站放映室，但视频字节从不经过服务器。

作者 **drayee** · 开源：<https://github.com/Drayee/ProjectionRoom>

**服务器只做三件事**：信令交换、房间状态（成员 / 拓扑 / 播放进度）、账号与内容元数据。
视频分片由主播经 DataChannel 直接分发给观众（P2P 树状分发 + 多父条带化），**服务端不碰任何视频字节**。

---

## 目录

- [它长什么样](#它长什么样)
- [四条不变量](#四条不变量)
- [快速开始](#快速开始)
- [主播准备视频](#主播准备视频)
- [配置](#配置)
- [前端 mock / real 适配层](#前端-mock--real-适配层)
- [代码结构](#代码结构)
- [协议要点](#协议要点)
- [部署](#部署)
- [验证](#验证)
- [文档地图](#文档地图)
- [已知缺口](#已知缺口)
- [开发约定](#开发约定)

---

## 它长什么样

| 分区 | 路由 | 说明 |
| :--- | :--- | :--- |
| 落地页 | `/` | 全屏 hero +「开始」按钮（hero ↔ 登录窗 morph）；登录后进入等待加入房间的占位页 |
| 放映室 | `/room/:roomId` | 主播控制台 + 观众播放器 + 成员拓扑 + 房间聊天 |
| 公开房间 | `/rooms` | 免登录浏览已公开且未关闭的房间 |
| 官方放映区 | `/official` | 运营位次排序的官方 / 推荐房 |
| 帖子 | `/posts`、`/posts/new`、`/posts/:id` | 三个排序 pill（最新 / 热门 / **关注**）、发帖、详情与评论 |
| 番剧论坛 | `/forum`、`/forum/:animeId` | 番剧目录 → 板块 → 主题串（主题串复用帖子详情） |
| 好友 | `/friends` | 页头常驻搜索 + 四个名单 tab（好友=互关 / 关注 / 粉丝 / 黑名单） |
| 消息 | `/messages?peer=<id>` | 私聊（仅互关可发） |
| 用户档案 | `/user/:id` | 公开名片（用户名 / 昵称 / 加入时间 / 角色）+ 关注域关系动作；本人视角多出邮箱、邮箱状态与昵称编辑 |
| 账号 | `/login`、`/register`、`/verify`、`/help` | 两段式注册、邮箱验证码找回密码 |
| 管理端 | `/admin`、`/admin/rooms`、`/admin/ops` | 用户 / 房间 / 运维（日志、指标、审计） |

**社交模型一句话**：关注是**单向免同意**的边；**互关即好友**，没有「好友请求」这条流水线（已退役，见 [GLOSSARY](docs/GLOSSARY.md)）。
**收藏是私人书签**，除自己外全世界看不到，也不计任何公开数字（点赞才是公开表态）。

---

## 四条不变量

改任何代码前先确认没违反它们。违反的表现写在右列 —— 那是判断依据，不是比喻。

| 编号 | 内容 | 违反的表现 |
| :--- | :--- | :--- |
| **I1** | 服务端不经手视频字节（分片只在 peer 之间走 DataChannel） | 出现「服务端中转媒体」的代码路径 |
| **I2** | 房间**实时状态**（成员 / 拓扑 / 播放进度 / 位图）只在内存；DB 只存**元数据**与周期快照 | 每次同步都写库；房间状态以库为准 |
| **I3** | **授权 100% 在服务端**；前端缓存的 `role` 只决定「显示什么」 | 前端路由守卫 / `v-if` 被当作授权 |
| **I4** | 任何 DB 写入都不在 WS / REST 处理路径上（事件类走异步单写协程） | 在 handler 里同步写库 |

> I4 的例子：房间元数据投递、审计日志、关注通知、头像旧对象清理，全部**入队后立即返回**。

---

## 快速开始

**前置**：Go 1.26+（`go.mod` 写死）、Node 18+（Vite 6 的要求；本仓库按 20+ 开发）、PostgreSQL 16+。
`ffmpeg` **可选**（本地切片器会自己下载一份；只有服务端切片才必须装）。

服务端**要求配置齐全才启动**：缺 `PR_DB_DSN` / `PR_JWT_SECRET`(≥32 字节) / `PR_PUBLIC_BASE_URL` /
发信通道（SMTP 三件套或 `PR_OTP_DEV_ECHO=1`）/ 前端产物（`PR_SERVE_STATIC=true` 时）任一项，
`config.requireComplete` 会**逐条列出并拒绝启动**。只想要功能不全的窄实例（例如只跑信令）时，
必须**显式**写 `PR_REQUIRE_COMPLETE=0`。

### 推荐路径：一键起本机库 + 服务

```powershell
powershell -File scripts/setup-local-db.ps1    # 建 pr_app 角色 + 两个库，生成 .env.local（幂等）
powershell -File scripts/run-local.ps1         # 读 .env.local 起服务，默认 http://127.0.0.1:8888
powershell -File scripts/run-local.ps1 -Port 8090   # 换端口（脚本会自动补 Origin 白名单）
```

`run-local.ps1` 会在启动前做一次**预检**，把「等它编译完再报错」变成「现在就说清缺什么」。

### 前端开发

```bash
cd client
npm install
npm run dev        # http://127.0.0.1:5173，/api、/ws、/downloads 自动代理到后端
```

⚠️ **Vite 的代理默认指向 `http://127.0.0.1:8080`，而 `run-local.ps1` 默认监听 `8888`** —— 两者不一致时
页面能打开但所有接口 404。要么显式指定后端：

```powershell
$env:PR_SERVER_URL = 'http://127.0.0.1:8888'; npm --prefix client run dev
```

要么用 `run-local.ps1 -Port 8080`。这个不一致是**有意的默认值**（8080 是生产容器端口），不是 bug。

### 单端口（页面 + API + WS 同源）

```bash
npm --prefix client run build      # 产物在 client/dist（已 gitignore）
go run ./cmd                       # → http://127.0.0.1:8888 同时是页面、API 与 /ws
```

路由优先级固定为 **`/healthz` → `/api/*` → `/ws` → `/assets/*` → 其它真实文件 → SPA 回退 `index.html`**，
页面永远不会顶掉业务路由。静态目录缺失**不影响启动**：只打一条 WARN，`/api`、`/ws`、`/healthz` 照常可用。

---

## 主播准备视频

月喵只同步**本地**视频：主播先把源视频切成 fMP4 分片目录，再在主播控制台点「选择本地分片目录」。

### 本地切片器（推荐）

```bash
# 一条命令搞定：探测编码后自动决定（能直通就只重新封装，不能就自动转码）
go run ./cmd/segmenter -in movie.mp4 -out ./room-media

# 不想给 -in：把视频拖到 exe 上，或按文件名在常见目录里找
go run ./cmd/segmenter "movie.mp4" -out ./room-media
go run ./cmd/segmenter -name movie.mp4 -out ./room-media

# 上行不足时的低码率预设（重新编码，耗时随片长增长）
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -transcode 1200k
```

**要不要转码它自己判断**：ffprobe 探测后，视频 ∈ {h264, av1, vp9} 且音频 ∈ {aac, opus, 无}
→ 只做 `-c copy` 无损重新封装；否则转码成 H.264/AAC。

**本机没有 ffmpeg 也没关系**：按 `-ffmpeg-dir` → `PATH` → exe 同级目录（`./ffmpeg/bin/`、`./ffmpeg/`、`./bin/`）
依次找；都没有就打印警告 + **5 秒倒计时**（Ctrl+C 取消，`-yes` 跳过），然后自动下载解压到 exe 同级目录。
下载或解压失败不会静默 —— 会打印手动安装指引。

预编译版本从主播控制台的下载面板取：`GET /api/downloads/segmenter` 返回清单（平台 / 文件名 / 字节数 / sha256 / 下载 URL）。
URL 有两种来源 —— 配了对象存储时由 CI 把二进制与清单一起传到 MinIO 桶、URL 走 `/files/...`（见 `docs/STORAGE-MINIO.md`）；
否则回落到静态托管 `/downloads/<file>`。构建：`scripts/build-segmenter.ps1`（5 个平台目标，产物落 `client/dist/downloads/`）。

> ⚠ 已知限制：本地切片器（`internal/service/mp4`）只认 avc1/avc3/av01 + mp4a/Opus 的采样格式，
> 所以 **VP9（vp09）直通会在切片这一步被拒** —— 工具会提示「请加 `-transcode 1200k` 重跑」，不会静默产出播不了的目录。

### 交给服务器切片（没有 ffmpeg 时）

```bash
curl -F "file=@movie.mp4" http://127.0.0.1:8888/api/v1/segment/jobs
# → {"jobId":"...","state":"queued","queuePosition":0}
curl http://127.0.0.1:8888/api/v1/segment/jobs/<jobId>
curl -o out.zip http://127.0.0.1:8888/api/v1/segment/jobs/<jobId>/result
```

配额：并发 2、排队 8、3 作业/分钟（超限 429 + `Retry-After`）、单作业 ≤60 分钟、源文件 ≤16 GiB、
产物保留 30 分钟。全部可用 `PR_SEGMENT_*` 覆盖。**直播链路上的视频字节仍然只走 P2P** ——
服务端切片是一次性预处理，不在分发路径上。详见 [`docs/SEGMENT.md`](docs/SEGMENT.md)。

---

## 配置

全部定义与默认值在 [`internal/config/config_struct.go`](internal/config/config_struct.go) +
[`config_load.go`](internal/config/config_load.go)（每个字段都有中文注释）。用户可抄的模板是
[`.env.example`](.env.example)。**本机开发用 `.env.local`（已 gitignore）**，生产用 `/opt/projectionroom/.env`（root:600）。

### 必需项

| 变量 | 说明 |
| :--- | :--- |
| `PR_DB_DSN` | PostgreSQL 连接串（启动期触发幂等迁移）。缺 → 拒绝启动 |
| `PR_JWT_SECRET` | access token 的 HS256 密钥，**≥32 字节**（`openssl rand -hex 32`）。缺或过短 → 拒绝启动 |
| `PR_PUBLIC_BASE_URL` | 邮件链接的对外基地址。**没有默认值** —— 给默认值会让「漏配」静默指向另一个环境。**它的 host 必须在 `PR_ALLOWED_ORIGINS` 里**（见下），否则拒绝启动 |
| `PR_SMTP_HOST` / `_USER` / `_PASS` | 注册与改密码都靠邮箱验证码。本机没有 SMTP 时用 `PR_OTP_DEV_ECHO=1` 代替 |
| `PR_OTP_DEV_ECHO` | **仅本机开发**：`1` 时验证码明文回显在响应里。与已配 SMTP 同时开启会**拒绝启动** |

### 最常改的几项

| 变量 | 默认 | 说明 |
| :--- | :--- | :--- |
| `PR_ADDR` | `127.0.0.1:8888` | 监听地址 |
| `PR_ALLOWED_ORIGINS` | 含 `127.0.0.1:5173`、`localhost:5173`、`127.0.0.1:8888`、`localhost:8888` | 允许连 `/ws` 的来源，**唯一判据**（同源不再隐式放行 —— DNS rebinding 曾据此绕过）。**换端口 / 上域名必须跟着改**，否则「页面能开、`/ws` 一律 403」。**必须包含 `PR_PUBLIC_BASE_URL` 的 host**，否则启动期就被拒（这条闸是 2026-11-02 换域名漏配事故的沉淀） |
| `PR_SERVE_STATIC` / `PR_STATIC_DIR` | `true` / `client/dist` | 是否托管前端产物（单端口部署的总开关） |
| `PR_MAX_MEMBERS` | `16` | 房间成员硬上限（真实上限由实测上行算出的 `1+K0` 决定） |
| `PR_DEFAULT_STREAM_BPS` | `2000000` | 尚未拿到 mediaIndex 时的码率估计 |
| `PR_ROOM_HOST_GRACE` | `60s` | 主播断线宽限期：期内房间保留、**永不回收**，客户端用同码自动重建 |
| `PR_TRUSTED_PROXIES` | `127.0.0.1,::1` | 允许其 `X-Forwarded-For` 被采信的来源。**配错会让按 IP 限速退化成全局限速** |
| `PR_OTP_TTL` | `5m` | 邮箱验证码有效期（`1m`~`30m`） |
| `PR_ACCESS_TTL` / `PR_REFRESH_TTL` | `15m` / `720h` | access 只存内存 + `sessionStorage`（**不进 localStorage**）；refresh 是 HttpOnly Cookie，每次刷新轮换 + 重放检测 |
| `PR_AUTH_LOGIN_PER_MINUTE` / `_BURST` | `10` / `5` | 登录尝试的每 IP 令牌桶（失败也计入，挡撞库） |
| `PR_AUTH_REGISTER_PER_MINUTE` / `_BURST` | `5` / `3` | 注册与发验证码（同样跑 bcrypt，所以更严） |
| `PR_ICE_PROBE_INTERVAL` / `PR_ICE_TTL` | `60s` / `300s` | 服务端 STUN 探测周期与下发载荷有效期 |
| `PR_ICE_MAX_STUN` | `5` | 单次下发的 STUN 条数上限（= 恒选 4 席 + 1 个轮询席位） |
| `PR_SECURITY_CSP` | 严格 CSP | 本地跑验收矩阵时临时放行 `http://127.0.0.1:*` |
| `PR_STORAGE_ENDPOINT` 等 | 空 = 未启用 | 对象存储（MinIO）。**Endpoint 与 PublicBaseURL 是两件事**，混用会得到「上传成功但图片显示不出来」 |

> 全部 **87 个** `PR_*` 变量都在 `config_load.go` 里，非法取值**直接报错、不静默回退**。

---

## 前端 mock / real 适配层

部分分区按域分批上线，前端因此分成**门面 + 两份同形状实现**，由构建期环境变量二选一：

```
client/src/api/<域>.ts          门面：页面唯一 import 的出口，导出形状固定
        ├── api/mock/<域>.ts    本地 mock（内存数据）
        └── api/real/<域>.ts    真实 HTTP 实现
```

八个开关：`VITE_MOCK_{POSTS,FRIENDS,MESSAGES,FORUM,OFFICIAL,USERS,FOLLOWS,FAVORITES}`
（`client/src/api/mode.ts`，每个都是**静态成员访问** —— Vite 只替换静态形式）。

| 环境 | 不设变量时 | 为什么 |
| :--- | :--- | :--- |
| **本地** | mock | 前端先行施工期要的就是这个；配错也退化成「还是 mock」，不会退化成整片分区 404 |
| **CI / 生产** | **真实接口** | `.github/workflows/build-push.yml` 注入 `vars.VITE_MOCK_* \|\| '0'` |

**两侧默认值刻意相反**：2026-10-28 线上六个分区对所有人显示同一份硬编码假数据
（`/messages?peer=2` 恒为「放映员」），根因正是 CI 什么都没设、静默沿用了「默认 mock」。
生产走 mock 从来不是任何人的决定，只是没人想起来配它 —— 而「忘了配」在两种默认下后果不对称。

要在某个环境**显式**退回 mock：仓库 Settings → Variables → 把对应变量设成 `1`。构建日志会打印生效的开关快照。

> ⚠ 别拿「bundle 里还有 mock 的中文人名」当判据 —— 两份实现都会进产物。看页面实际发出的 `/api/*` 请求。

翻真后若该域后端路由未注册（404），页面渲染「该分区后端尚未上线」空态（`ui/CapabilityNotice.vue`），
**不是**裸的「请求失败（HTTP 404）」。

---

## 代码结构

```
cmd/                    入口：main.go（读配置 → InitializeApp → 起服务 → 优雅退出）
  wire.go               只有 //go:build wireinject 头 + InitializeApp，无业务代码
  wire_gen.go           `go tool wire ./cmd` 生成（提交，不手改）
  segmenter/            本地切片器 CLI（独立 main 包，交叉编译成用户下载的 exe）

internal/bootstrap/     装配根（无业务逻辑）：ProviderSet + db / auth / admin / engine / mail / storage 装配块
                        ⚠ provider 声明顺序 = 创建顺序，cleanup 反序（关服务 → flush 写队列 → 关连接池）
internal/service/       核心服务层：manager/room/assign/mode（房间、成员、拓扑分配）
                        account.go（账号流程 + 管理端用户类用例；身份与授权事实的唯一落点）
                        hub.go（WS 连接与广播）、logring.go、server.go
                        auth/（bcrypt、JWT、WS 票据）、limiter/、ice/（STUN 探测打分）、
                        mp4/（解析切片）、segment/（服务端切片队列）、mail/
internal/handler/       HTTP 适配层：router.go / ws.go / wsuser.go（信令入口）、middleware.go、
                        auth.go / register.go（账号）、admin.go / users.go / follows.go /
                        friendships.go（社交）、posts.go / anime.go / messages.go /
                        favorites.go（内容）、publicroom.go / roommeta.go / segment.go /
                        uploads.go / static.go / downloads.go、errors.go（错误码映射）
internal/store/         唯一持久化 owner（GORM）：gorm/migrate/migrations、user/session/roommeta/
                        audit/writer（异步事件写入器）、posts/anime/messages/follows/favorites、
                        otp/draft/idempotency
internal/config/        全部环境变量、默认值与校验
internal/model/         领域模型、错误码、wire.go（解码前预算）、pb_convert.go
internal/pb/            生成代码（勿手改）；真源 proto/projection_room.proto
internal/storage/       对象存储的唯一出口（MinIO / S3）：PutImage / Remove / EnsureBucket
internal/utils/         与业务无关的小工具

client/src/pages/       20 个页面（17 个主页面 + admin/ 三页），全部 () => import() 懒加载
client/src/components/ui/        纯展示组件：AppHeader / UserAvatar / AuthorRow / ActionBar /
                                 AsyncState / Button / IconButton / MarkdownView / RelativeTime /
                                 MemberList / TopologyBadge / RouteSkeleton / TooltipLayer …
client/src/components/features/  壳与业务组合：auth / common / room / rooms / shell
client/src/api/         门面 + mock / real 两套实现（见上一节）
client/src/stores/      Pinia：auth / room / roomMeta / publicRooms / admin
client/src/utils/       路由契约与共享逻辑（avatar / count / relativeTime / navSection /
                        postsRoute / friendsRoute / dmRoute / routeTransition …）
client/src/styles/      tokens / base / transition
client/src/gen/         protobuf 生成代码

proto/                  projection_room.proto（唯一真源）
test/script/            真机验收与诊断脚本（32 个 + lib/browser.mjs 公共库，见下）
test/resource/          媒体夹具（已 gitignore）
scripts/                setup-local-db / run-local / build-segmenter / gen-proto / minio / 迁移
docs/                   见「文档地图」
```

**依赖方向**：`handler → service → store`；`model` / `utils` 不反向依赖任何层。
`internal/` 下任何代码都不得读写视频数据（I1）。

---

## 协议要点

- **protobuf 是唯一真源**：`proto/projection_room.proto` → `internal/pb/*.go` + `client/src/gen/*.ts`
  （`scripts/gen-proto.ps1`）。改协议只改 proto，不手改生成物。
- **WS 信封**：1 字节类型前缀 + protobuf 体；信令走 `/ws?roomId=<码>&clientId=<id>[&ticket=<票据>]`。
  - **无 `ticket` = 游客**（观众免登录进房是产品决定，别加拦截）；带票据则绑定账号，票据**单次使用 + 30s 过期**。
  - **Origin 白名单是唯一判据**（见 `PR_ALLOWED_ORIGINS`）。
- **DataChannel**：negotiated id=0，1 字节 kind 前缀（`0x01` 控制 / `0x02` init / `0x03` 媒体 / `0x04` tail）
  + 7 字节媒体头；64 KiB 分片 + `bufferedAmount` 背压。
- **房间码**：`^[A-Z0-9]{4,12}$`；建房**必须登录**且响应下发一次性 `hostToken`
  （主播断线宽限期内接回主播位必须携带）。
- **ICE**：**TURN 已彻底退役**。服务端周期性实测 STUN 并打分下发（最快 2 条 + 健康粘性 2 条恒选），
  载荷带 `ttlSeconds` / `expiresAt` / `probe`，`probe.scores` 覆盖**全部候选**（排障时能看出某条为什么落选）。
- **解码前预算**：`internal/model/wire.go` 用 protowire 扫 repeated 元素数，**先于** `Unmarshal` 拒绝
  （曾出现「4 MiB 帧展开成 140 万个对象 → 3.9 GB」）。
- **启动门控**：新节点接入后不立即播放，缓冲达 4 片 / 8 秒且时钟偏移估计收敛才开闸，**不设超时上限**
  （上游没数据就一直等，超 20s 提示「上游带宽可能不足」）。细节见 [`docs/ALGORITHM.md`](docs/ALGORITHM.md)。

---

## 部署

**生产形态（2026-10-19 起）**：Docker Compose 双容器，对外只有 **80** 一个入口。

```
浏览器 → nginx (0.0.0.0:80) → 127.0.0.1:8080 → container「projectionroom-app」(network_mode: host)
                                                     ↓
                                              宿主机 PostgreSQL 16 (127.0.0.1:5432)
                                              容器「pr-minio」(:9000 / 控制台 :9001)
```

- **两个容器都用 `network_mode: host`**：宿主机 PG 只监听回环，bridge 网络连不上；代价是容器不能做端口映射。
- **数据库不在 compose 里**：本机已有 PG 16 且存着生产数据，再起一个 pg 容器会争抢 5432 并让「数据到底在哪」变成两个答案。
- **对象存储**：桶 `pr-files`，匿名只读（只给 `s3:GetObject`，不给 ListBucket），浏览器经 `/files/` 反代到 9000。
- **CI**：`.github/workflows/build-push.yml` 推镜像到 GHCR（Secret 名 **`REGISTRY_TOKEN`**），
  `deploy.yml` 从 GHCR 拉镜像部署。镜像标签是提交 sha，回滚 = 再跑一次旧 sha。
  ⚠ Dockerfile **不能**带 `-tags wireinject`（会选中空壳 `cmd/wire.go`，`InitializeApp` 返回 nil → 启动即 SIGSEGV）。

详见 [`docs/DEPLOY-SINGLE-PORT.md`](docs/DEPLOY-SINGLE-PORT.md) 与 [`docs/STORAGE-MINIO.md`](docs/STORAGE-MINIO.md)。

**内网穿透**：页面、`/api`、`/ws` 同源之后，一条隧道指向一个端口就够了。三条注意：

1. 隧道给 HTTPS 时页面会走 `wss://`（信令地址由 `location.protocol` 推导，不硬编码），隧道必须支持 WS 升级。
2. **隧道只解决「页面 + 信令」，媒体仍然是 P2P** —— 打洞失败时隧道帮不上忙，而 TURN 已退役。
3. 切片接口受隧道限制（源文件上限 16 GiB / 作业 60 分钟，而免费隧道常只有几十 MB / 30~100s），
   大视频远程上传大概率在隧道层先被拒。稳妥做法是在服务器本机切好再分批下载产物。

---

## 验证

### 门槛（每次改完都要全绿）

```bash
gofmt -l ./cmd ./internal                 # 必须无输出
go build ./... && go vet ./...
go vet -tags wireinject ./cmd && go tool wire check ./cmd
go test ./... -count=1 -timeout 900s      # 12 个有测试的包

npm --prefix client run typecheck
npm --prefix client run build             # 含 vue-tsc；产物必须 0 个 window.__pr
npm --prefix client test                  # 5 个前端单测（node --test）
```

前端渲染判据：`grep -rn "v-html" client/src` 命中 ≤1 且只在 `ui/MarkdownView.vue`；`grep -rn "innerHTML" client/src` 为 0。

### 真库测试

`internal/store` 的用例依赖真库，否则 Skip。**测试库与生产库是两个库**，DSN 的 `dbname` 必须含 `_test`（用例里有护栏）。

```powershell
$env:PR_TEST_DB_DSN = 'host=127.0.0.1 port=5432 user=pr_app password=<见 .env.local> dbname=projectionroom_test sslmode=disable'
go test ./internal/store/ -count=1
```

⚠ 真库用例会 **TRUNCATE 表** —— 手工 fixture（如验收用的管理员账号）跑过之后就没了，需要重建。
⚠ **测试实例收尾必须停进程**：曾有一个后台实例直连测试库、绕过用例的跨进程隔离锁，导致 store 用例随机红。

### 真机验收（`test/script/*.mjs`，headless Chrome）

共 32 个脚本 + `lib/browser.mjs` 公共库。**按需跑，不必全跑**；每条的完整前置条件见
[`CLAUDE.md` §7](CLAUDE.md)（那是脚本契约的唯一真源，改脚本先读它）。

| 分组 | 脚本 | 需要什么 |
| :--- | :--- | :--- |
| **协议连通** | `verify-connection` | 不需要 Chrome，几十毫秒出结论 —— **「页面连不上后端」先跑它** |
| **播放链路** | `verify-m2`、`verify-m3`、`verify-m4*`、`verify-room-resume`、`verify-ice`、`verify-health`、`verify-schedule`、`verify-unassigned`、`verify-segment-ui` | 媒体夹具 + 调试钩子（`VITE_DEBUG_HOOKS=1`）；`verify-room-resume` 的 `--grace` 必须与服务端一致 |
| **账号** | `verify-auth`、`verify-forgot` | 管理员凭据 / `PR_OTP_DEV_ECHO=1` |
| **社交与内容** | `verify-following`、`verify-follows`、`verify-favorites`、`verify-post-actions`、`verify-friends` | 真后端 + 多账号；部分要 `VITE_MOCK_*=0` 构建 |
| **视觉与结构** | `verify-avatar`、`verify-authorrow`、`verify-navstate`、`verify-transition`、`verify-transition-logic`、`verify-relative-time`、`verify-route-title`、`verify-routelazy` | **不需要后端**（自起静态服务器 + `/api/*` 回 404，跑 mock 域），但**须先 build** |
| **诊断** | `diag-chunks`、`diag-control`、`diag-write`、`verify-large` | 排障用，两端状态并列打印 |

需要调试钩子的脚本：`$env:VITE_DEBUG_HOOKS='1'; npm --prefix client run build`（生产构建刻意没有 `window.__pr`）。

---

## 文档地图

**根目录只留三个 md**：`README.md`（本页，用户门面）、`CLAUDE.md`（AI 会话的唯一入口地图）、
`AGENTS.md`（技能配置指针）。**其余全部在 [`docs/`](docs/README.md)** —— 那里是文档总索引，按
「现行规范 / 决策记录 / 实施计划与报告 / 笔记 / 归档」五组编排。

最常打开的四份：

| 文档 | 什么时候读 |
| :--- | :--- |
| [`docs/SPEC.md`](docs/SPEC.md) | 改协议、房间状态机、拓扑分配之前 —— 它是设计与取舍的**完整依据** |
| [`docs/ALGORITHM.md`](docs/ALGORITHM.md) | 改同步算法、分片分发、ICE 打分之前（含时序图与已知偏差） |
| [`docs/ACCOUNTS.md`](docs/ACCOUNTS.md) | 改账号 / 社交 / 管理端 / 持久化之前 |
| [`docs/GLOSSARY.md`](docs/GLOSSARY.md) | 写文档或命名新概念之前 —— 有歧义的词在这里定死，每条注明锁定它的 issue 与日期 |

`docs/archive/` 里是已过时的流程文档（旧部署、迁移指南），每篇顶部有横幅说明被什么取代。
**照做会出错**，只在追溯时读。里面的凭据已脱敏为 `<REDACTED>`。

---

## 已知缺口

按优先级：

1. **无 TLS**：当前是明文 http，因此 refresh Cookie **不带 `Secure`**（有测试断言钉住 —— 上 TLS 之前不该加，
   否则浏览器会直接丢弃它）。上 TLS 后应立即补 `Secure` 并收紧 `SameSite`。
2. **`GET /api/rooms/mine`**：「我的房间」目前只读本标签页 `sessionStorage`。
3. **`client/dist/downloads/room-media` 应移出静态根**：本机跑单端口实例时它会被静态托管（公网可下载）。
   生产镜像已通过 `.dockerignore` 排除，所以这个缺口只在本地实例上存在。
4. **房间元数据「投递失败只记日志」**：补偿（启动对账 / 重投）待公开列表扩大后一起做。
5. **`/about` 仍是内容占位**（等作者提供内容）。
6. **`scripts/` 下若干迁移脚本仍含历史明文口令**（已归档文档里的已脱敏，脚本里的待清理）。

---

## 开发约定

- **语言**：注释与提交信息用中文；代码标识符用英文；字符串一律 **ASCII 引号**。
- **提交**：`<type>(<scope>): <描述>`（feat / fix / docs / refactor / chore），一次任务一次提交；
  `git push` 仅在明确要求时执行。
- **前端渲染**：一切用户可控内容走**文本插值**（`{{ }}`）；唯一例外是 `ui/MarkdownView.vue`
  （`marked` 解析 + `DOMPurify.sanitize()` 后注入）。
- **看着像重复、但输出格式不同的工具函数不要合并**：例如 `service.humanBytes`（`"512 MiB"`）与
  `segment.HumanBytes`（`"512.00 MiB"`）—— 合并会静默改日志文案并打挂测试。合并前先证明「逐字等价」。
- **`data-testid` 与主行动按钮文案是验收脚本的契约**：改名 / 删除会打挂 `test/script/**`，改之前先 grep 一遍谁在用。
- **改完 `internal/bootstrap/ProviderSet` 的 provider 列表后**：重新 `go tool wire ./cmd` 并核对生成代码里的
  创建序列与 cleanup 反序（关服务 → flush 写队列 → 关连接池）。
- **编辑含中文的源文件用编辑器 / 编辑工具**，不要用 PowerShell 直接读写（GBK 乱码）。
- **遇到语义不清、或需要人拍板的点：停下来问**，带着选项和推荐问。
- **目录结构、关键约定、命令、部署事实一旦变化，同步更新 `CLAUDE.md`** —— 过时地图比没有地图更危险。
