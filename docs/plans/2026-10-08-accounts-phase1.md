# 实现计划：账号层 一期（数据库 + 账号骨架）

> 依据规格：`docs/ACCOUNTS.md`（已评审通过）。本文只覆盖**一期**，二三期另立计划。
> 计划路径说明：规格与本计划都放在扁平的 `docs/` 下（本仓库既有结构如此），不使用 skill 默认的 `docs/aegis/plans/`。
> 状态：**待批准**。用户批准后才开始改代码。

---

## 0. 目标

让"账号"这件事真正成立并可用：**注册 / 登录 / 刷新 / 登出 / 当前用户**、封禁即时生效、建房必须登录并绑定房主、管理端最小骨架（用户列表只读 + 运行指标 + 服务端日志）。数据库从零接上（PostgreSQL + GORM），且**不破坏**现有"观众按房间码免登录进房"的能力。

## 1. 架构与分层

沿用现有方向 `handler → usecase → store/auth`，新增三块（均为新 owner，见 §2 Existence Check）：

```
internal/store/      gorm.go models.go migrate.go migrations.go user.go session.go roommeta.go audit.go writer.go
internal/auth/       password.go jwt.go ticket.go
internal/service/    logring.go            （管理端日志环形缓冲，不落库）
internal/handler/    auth.go admin.go middleware.go
internal/usecase/    account.go
client/src/stores/   auth.ts
client/src/views/    LoginView.vue RegisterView.vue
test/script/         verify-auth.mjs
```

**技术栈**：Go 1.26.6（`go.mod` 已 pin）+ gin + GORM（`gorm.io/gorm` + `gorm.io/driver/postgres`）+ PostgreSQL 16（已部署）+ Vue 3/TS（现有）。

## 2. 变更必要性、新面存在性与涟漪

**Change Necessity**：账号必须跨会话存在，进程重启不能丢 → 需要持久化与真实凭据校验，纯配置/文档无法替代；最小代码边界 = 上表 13 个文件（含 1 个前端 store、2 个视图、1 个验收脚本）。

**Existence Check（新面是否必须新建）**
- `internal/store`：仓库现无任何持久化 owner → **add-with-proof**（GORM + 迁移 + 仓储，职责单一）。
- `internal/auth`：现无凭据签发/校验 owner → **add-with-proof**。
- `internal/service/logring`：管理端要"看服务端日志"，现在日志只进 journald，无法按需读取 → **add-with-proof**；替代方案（日志落库）被否决：会把日志内容写进 DB 并带来日志注入与无界增长（见规格 §4）。
- 其余复用既有 owner：限速用 `internal/limiters` ✓；路由用 `internal/handler/router.go` ✓；装配用 `cmd/wire.go` ✓。

**Ripple Signal Triage（触发了）**
- 触发项：公共 API（新增认证面 / `POST /api/rooms` 变为需登录）、schema（新表与迁移）、持久化（首个 DB owner）、producer/consumer（前端与验收脚本都是 `POST /api/rooms` 的消费者）。
- canonical owner：持久化 = `internal/store`；身份 = `internal/auth`；房间元数据 = `internal/usecase/room.go`（既有）。
- **下游影响（必须处理，最大的兼容性风险）**：`test/script/lib/browser.mjs` 的 `createRoom()` 目前**匿名**调用 `POST /api/rooms`，而**所有**现有验收脚本（m2 / health / room-resume / ice / unassigned / schedule / segment-ui）都靠它建房 → 一期上线后它们会集体 401。缓解：`createRoom()` 改为"首次调用时注册并登录一个随机测试账号，随后带 `Authorization: Bearer`"（见 T11-b），并保留 `PR_TEST_USER/PR_TEST_PASS` 环境变量以复用已有账号。
- 兼容性边界：**观众侧零变化**（仍按房间码免登录）；WS 房间通道仍是"无 Origin 也接受脚本客户端"，但带凭据时绑定账号。

**Plan Pressure Test**：owner 边界清晰（新 owner 各自单一职责）、无重复 owner、无回退分支、验证可观测（HTTP 状态 + 脚本判据 + DB 断言）→ **proceed**。

## 3. TDD 路由（记录）

```
TDD Route: mode=off · decision=skipped · authority=会话默认（用户未要求 TDD）
test posture: 每个任务仍必须给出可执行的验证（Go 单测 + 真机验收脚本）；不要求 RED-first
reason: 会话未显式要求严格 TDD；但本工作触及持久化与权限（高风险），因此验证强度不降级
```

## 4. 任务（顺序执行，每步都有验证命令）

### T1 依赖与配置
- **文件**：`go.mod`/`go.sum`、`internal/config/config_struct.go`、`internal/config/config_load.go`、`internal/config/accounts_config_test.go`（新）
- **最小改动**：引入 `gorm.io/gorm`、`gorm.io/driver/postgres`；新增配置 `PR_DB_DSN`（默认空=关闭账号能力并打印警告）、`PR_JWT_SECRET`（空则拒绝启动并给出生成命令）、`PR_ACCESS_TTL=15m`、`PR_REFRESH_TTL=720h`、`PR_BCRYPT_COST=12`、`PR_WS_TICKET_TTL=30s`、`PR_AUTH_RATE_*`、`PR_TRUSTED_PROXIES`（顺带修掉审计残留项）。
- **兼容**：全部有默认值；未配 `PR_DB_DSN` 时服务按现状启动（便于渐进上线）。
- **验证**：`go build ./... && go test ./internal/config/ -count=1`；`go vet ./...`。

### T2 store：连接、模型、迁移
- **文件**：`internal/store/gorm.go`、`models.go`、`migrate.go`、`migrations.go`、`migrate_test.go`（新）
- **最小改动**：连接池 + 启动期 ping；本阶段建表 `schema_migrations`、`users`、`sessions`、`rooms_meta`、`admin_audit`（其余表留二期/三期，**只做加法**）；迁移步骤为**有序、幂等**的 DDL 列表，执行记录进 `schema_migrations`。
- **兼容**：无破坏性 DDL；重复启动不重复执行。
- **验证**：`go test ./internal/store/ -count=1`（无 `PR_TEST_DB_DSN` 时 `t.Skip`）；有 PG 时额外断言"连续两次 Migrate：第二次 apply 0 步"。

### T3 store：仓储
- **文件**：`internal/store/user.go`、`session.go`、`roommeta.go`、`audit.go`（+ 各自 `_test.go`）
- **最小改动**：GORM 参数化查询（禁止拼接）；`users` 按 username 查/建/改角色/改状态/`BumpTokenVersion`；`sessions` 建/查 token_hash/撤销/撤销用户全部/清理过期；`rooms_meta` upsert/关闭/改标题公开性/按公开性列出；`admin_audit` 插入。
- **验证**：`go test ./internal/store/ -count=1`（有 PG 时覆盖 CRUD 与唯一约束冲突）。

### T4 auth：密码 / JWT / 一次性票据
- **文件**：`internal/auth/password.go`、`jwt.go`、`ticket.go`（+ `_test.go`）
- **最小改动**：bcrypt 哈希与校验（cost 可配）；HS256 access token（claims：`sub`、`role`、`tv`、`exp`、`jti`）签发与校验（校验签名、exp、`tv` 与当前用户一致）；票据 = 内存单次表（TTL 30s，`Consume` 即失效）。
- **验证**：`go test ./internal/auth/ -count=1`，覆盖：过期拒绝、签名篡改拒绝、`tv` 变化后旧 token 失效、票据二次使用拒绝、票据过期拒绝。

### T5 事件异步写入器
- **文件**：`internal/store/writer.go`（+ `_test.go`）
- **最小改动**：有界队列 + 单写协程 + 指数退避重试；`Submit(ctx, job)` **投递即返回**；`Close(timeout)` 优雅 flush；导出指标（队列长度、丢弃数、最近延迟）。
- **硬约束**：绝不在调用方 goroutine 里执行 DB 写。
- **验证**：单测断言"队列满时 `Submit` 不阻塞且计入丢弃"、"关闭前投递的作业在 `Close` 后可见"、用假 store 记录调用时序证明写入发生在写协程。

### T6 usecase：账号流程
- **文件**：`internal/usecase/account.go`（+ `account_test.go`）
- **最小改动**：注册（username 白名单 + 密码策略 + bcrypt）、登录（恒定时间、统一错误文案、失败限速）、刷新（轮换 + **重放检测**：命中已 revoke → 撤销该用户全部会话 + 审计）、登出、登出全部（`tv+1`）、封禁（`status=banned` + `tv+1` + 通知 hub 断连）、改角色。
- **验证**：`go test ./internal/usecase/ -count=1`，覆盖：重复用户名、弱密码、错误密码不泄漏账号是否存在、刷新轮换后旧 token 失效、重放触发全会话撤销、封禁后旧 access token 立刻失效。

### T7 handler：认证路由与中间件
- **文件**：`internal/handler/auth.go`、`middleware.go`（新）、`router.go`（改）、`errors.go`（改）、+ `auth_test.go`
- **最小改动**：`POST /api/auth/{register,login,refresh,logout,logout-all}`、`GET /api/auth/me`、`POST /api/auth/ws-ticket`；`RequireAuth`（Bearer 解析 + `tv` 校验）、`RequireAdmin`；刷新用 `Set-Cookie: pr_refresh=…; HttpOnly; SameSite=Lax; Path=/api/auth`；按 IP 与账号双维度限速（复用 `internal/limiters`）。
- **验证**：`go test ./internal/handler/ -count=1`：无凭据 401、非管理员访问 `/api/admin/*` 403、过期 token 401、封禁用户 403、刷新响应带正确 Set-Cookie 属性。

### T8 建房绑定房主 + 封禁拦截
- **文件**：`internal/handler/router.go`（`POST /api/rooms` 挂 `RequireAuth`）、`internal/usecase/room.go`、`internal/usecase/manager.go`、`internal/service/hub.go`（`CloseByUser`）、+ 测试
- **最小改动**：建房必须登录 → 房间绑定 `ownerUserId` 并写 `rooms_meta`（`has_password`/`title`/`is_public` 默认 false）；WS 握手时若带 ticket 且该账号被封禁 → 拒绝；封禁时断开该账号的全部连接。
- **兼容**：观众免登录进房不变；`/ws` 无凭据仍可用（脚本客户端路径保留）。
- **验证**：`go test ./internal/{handler,usecase,service}/ -count=1`；手工：匿名 `POST /api/rooms` → 401。

### T9 管理端最小骨架（**一期只做 API 与权限边界，前端视图在二期**）
- **文件**：`internal/handler/admin.go`（新）、`internal/service/logring.go`（新）、`internal/handler/router.go`（改）、`main`/`cmd` 里把 `log` 输出 tee 进环形缓冲、+ 测试
- **最小改动**：`GET /api/admin/users`（分页 + 搜索）、`GET /api/admin/metrics`（在线用户/房间/连接 + 写队列指标）、`GET /api/admin/logs`（环形缓冲，分页 + 字段白名单）；全部 `RequireAdmin`；管理写操作（本阶段只有封禁/改角色）写 `admin_audit`。
- **一期不做**：`/admin` 前端视图、房间管理、公开房审核（属二期）；一期的验收口径是**脚本与 curl**（非浏览器），因为管理端页面尚不存在。
- **验证**：`go test ./internal/handler/ -count=1`（非管理员 403、分页边界、日志接口字段白名单不含原始 JSON 整包）；curl 三端点各一次并贴输出。

### T10 前端：认证与导航壳
- **文件**：`client/src/stores/auth.ts`（新）、`client/src/views/LoginView.vue`、`RegisterView.vue`（新）、`client/src/router.ts`、`client/src/views/HomeView.vue`、`client/src/api/auth.ts`（新）、`client/src/App.vue`/顶栏组件
- **最小改动**：access token 只存内存 + sessionStorage（token 本体**不进 localStorage**），启动时用 refresh cookie 换新；档案（用户名/角色/邮箱/未读）可进 localStorage；`/login` `/register` 路由；建房入口需登录（未登录跳转并回跳）；`me` 拉取与登出。
- **交互验收判据（实现该任务时须加载 `ui-ux-governance` 并沿用仓库既有样式约定）**：
  ① 三种状态齐全：提交中（禁用按钮 + 文案）、失败（服务端文案原样展示，不吞错）、成功（跳回原目标）；
  ② 未登录点"创建房间"→ 跳登录 → 登录后**回到发起处**（不留死路）；
  ③ 键盘可用：Enter 提交、Tab 顺序合理、输入框有 `label`/`autocomplete`；
  ④ 错误文案区分"用户名或密码错误"与"网络不可达"，且**不泄漏账号是否存在**；
  ⑤ 登出后回到首页且不残留任何可用的 access token（内存与 sessionStorage 都清）。
- **验证**：`npm --prefix client run build`（含 `vue-tsc`）exit 0；产物 `window.__pr` 0 命中（`prune-dist` 生效）；真实浏览器走一遍 ①–⑤。

### T11 验收脚本
- **文件**：`test/script/verify-auth.mjs`（新）、`test/script/lib/browser.mjs`（改）
- **最小改动**：
  - `browser.mjs`：新增 `loginAs()`/`ensureTestUser()`；`createRoom()` 自动带 Bearer；保留 `--user/--pass` 复用账号。
  - `verify-auth.mjs` 判据：①注册返回 201 且不返回密码哈希 ②登录下发 Set-Cookie（HttpOnly/SameSite=Lax/Path）③access token 可访问 `/api/auth/me` ④刷新轮换后**旧 refresh 重放 → 该用户全部会话被撤销** ⑤封禁后旧 access token 立即 401/403 且已连 WS 被断开 ⑥**游客仍能按房间码进入**（不破坏现状）⑦匿名 `POST /api/rooms` 401 ⑧建房后再查 `/api/rooms/:id` 显示归属一致。
- **验证**：`node test/script/verify-auth.mjs --server … --client …` 全 PASS；随后重跑 `verify-m2`、`verify-room-resume`、`verify-ice`、`verify-health` 确认无回归。

### T12 部署与线上验收
- **改动**：服务器 `/etc/systemd/system/projectionroom.service` 追加 `Environment=PR_JWT_SECRET=<随机 32B>`（或写入 `.env`，注意 `.env` 是 root:600 由 systemd 读取）、`PR_DB_DSN` 已在 `.env`；重启；确认迁移在启动时自动执行（只做加法）。
- **验证**：`journalctl` 确认迁移日志与"账号能力已启用"；公网：注册/登录/刷新/`me` 全通；匿名建房 401；**游客按房间码进房仍然成功**；`verify-auth.mjs` 对公网实例再跑一遍；`go test ./...` 与本地矩阵复跑。

## 5. 验证与回归（一期总体）

1. `gofmt -l ./cmd ./internal` 无输出；`go build ./...`、`go vet ./...`、`go vet -tags wireinject ./cmd`、`go test ./... -count=1` 全绿。
2. 新脚本 `verify-auth.mjs` 全 PASS。
3. 既有回归全 PASS：`verify-m2` / `verify-room-resume` / `verify-ice` / `verify-health`（脚本改造后）。
4. 线上：匿名建房 401、游客进房成功、封禁断连生效。
5. 检查项：全仓 grep 不得出现 `v-html|innerHTML|eval(|new Function`（规格 §11 门禁）；业务代码不得出现字符串拼接 SQL。

## 6. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 无 TLS：refresh cookie 明文过网 | 已接受（规格 §13.1）；`Secure` 留待 TLS |
| bcrypt cost 12 在 2 vCPU 上约 200ms | 登录限速按此设定；过高降到 11 |
| 迁移在运行中的服务器执行 | 只做加法的 DDL；迁移前自动备份 schema（`pg_dump -s`）到 `/root/pr-backup-*/` |
| 现有验收脚本因需登录集体 401 | T11 的 `browser.mjs` 改造是一次性修复点；若遗漏，症状是脚本 401，定位明确 |
| 仓储测试需要 PG | 用 `PR_TEST_DB_DSN`，未设置则 `t.Skip`（不假绿） |
| 单实例内存态 | 本期明确不做多实例（规格 §13.2） |

## 7. 退役轨道（Retirement Track）

一期**不退役任何既有路径**：观众免登录进房、`/ws` 无凭据接受脚本客户端、`internal/limiters` 继续复用、`hostToken` 机制不变。唯一的"收缩"是 `POST /api/rooms` 从匿名变为需登录（这是产品决策，已在规格中批准）。

## 8. 执行路由

- 主路径：**inline**（后端 T1→T9 是强顺序链，同一组文件耦合，分派收益低）。
- 可委派：T10（前端）与 T11（脚本）在 API 面冻结后文件不重叠、边界清晰 → 可各派一个子代理并行；若子代理不可用则内联执行。
- 每个任务在其**自己的**验证通过后才算完成；按可独立交付的边界分批提交（建议：T1–T4「store+auth 底座」、T5–T9「账号与权限面」、T10「前端」、T11「验收脚本」、T12「部署」各一次），每次提交前重跑受影响的门槛命令；**整期收口时必须重跑 §5 全量**，提交信息用 `feat(accounts): …` / `test(auth): …`。

**User confirmation required: yes — 本计划批准后再开工**（依你的全局约定：≥3 文件变更须先给方案并等待批准）。
