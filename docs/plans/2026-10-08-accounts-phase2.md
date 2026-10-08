# 实现计划：账号层 二期（公开房间列表 + 管理端前端 + 标题/公开性 + 强关/下架）

> 依据：`docs/ACCOUNTS.md` §6/§8/§10/§12（二期）。一期已上线并通过公网验收（提交 `da28903`）。
> 状态：**已批准**（用户确认后开工）。三期另立计划。

## 0. 目标与成功判据

**目标**：普通用户能看到「公开房间」并直接进房；房主能改房间标题与公开性；管理员能在页面上管理用户/房间（含强制关闭、下架），并查运行指标、日志与审计。

**成功判据**（每条可观测）：
1. `GET /api/public-rooms` 只返回**显式公开**且**当前存在**的房间；**不含**密码、成员明细、房主邮箱/角色/状态。
2. 房主改标题/公开性 → 内存房间与 `rooms_meta` 一致；非房主 → 403。
3. 管理端：非 admin 访问任何 `/api/admin/*` → 401/403；强关**真的断掉房内连接**并留审计行；下架后从公开列表消失。
4. 三个新页面（`/rooms`、`/admin`、房主的标题/公开控件）在 1280/375 两档可用，与「月喵」品牌与图标体系一致。
5. 新增 `verify-public-rooms.mjs`、`verify-admin.mjs` 全 PASS；既有回归（m2/health/room-resume/ice/`go test ./...`）不回归。

## 1. 复用与新增边界（先复用，别造第二套）

| 能力 | 已有 owner | 二期怎么办 |
| :--- | :--- | :--- |
| 房间元数据读写 | `store.{UpsertRoomMeta,RoomMetaByID,UpdateRoomMeta,ListPublicRoomIDs}` | **复用**；补「按 ID 批量取」与「分页查询」 |
| 房间实时状态 | `usecase.Manager`（`RoomCount`/`closeRoom`/`CloseRoomIfEmpty`） | **补** `ListRooms()` 只读快照与 `CloseRoomByAdmin()`（受控导出既有 `closeRoom`） |
| 断连 | `service.Hub.CloseRoom/CloseByUser/Stats` | **复用**，强关走 `Hub.CloseRoom` |
| 元数据异步落库 | 一期 `RoomMetaSink.SubmitRoomMeta` | **复用**，房主改元数据也走它 |
| 管理端 RBAC / 审计 | `RequireAdmin` + `usecase.BanUser/SetRole`（审计唯一落点） | **复用**，新增写操作同模式写 `admin_audit` |
| 前端图标/品牌 | 品牌线 `BrandIcon.vue` + `client/public/icons/` | **复用**，不新建图标方案 |

不新增依赖；不改 `NewRouter` 语义；不动一期鉴权与限速实现。

## 2. 兼容性边界

- **纯新增**端点：`GET /api/public-rooms`、`PATCH /api/rooms/:id/meta`、`GET /api/admin/rooms`、`POST /api/admin/rooms/:id/close|unpublish`、`GET /api/admin/audit`。
- **观众路径零变化**（按码免登录进房、播放与信令不改）。
- 不改任何 `data-testid`，不改主行动按钮文案。
- **元数据缺行可降级 + 自愈**：内存有房但库里无元数据时照常列出（标题空、昵称未知），并在列出时投递一次 upsert（补掉一期"投递失败只记日志"的缺口）。

## 3. TDD 路由（记录）

```
TDD Route: mode=off · decision=skipped · authority=会话默认（用户未要求 TDD）
test posture: 每个任务仍必须给出可执行验证（Go 单测 + 真机脚本）；不要求 RED-first
reason: 触及权限、持久化与新公共接口；验证强度不降级
```

## 4. 任务

### 排期约束
品牌化线（月喵 + 图标化 + `/help`）仍在跑且会碰 `client/**`。**T1–T4（服务端）立刻并行开工**；**T5–T7（前端）等品牌线落地并复核后**再动。

### T1 服务端：房间清单与元数据能力（无新端点）
- **文件**：`internal/usecase/{manager.go,room.go}`、`internal/store/roommeta.go`（+ `_test.go`）
- **最小改动**：
  - `Manager.ListRooms() []RoomSnapshot`（`RoomSnapshot{ID, OwnerUserID, HasPassword, MemberCount, HostOnline, HostInGrace, MaxDepth, PlayingIndex}`），只在 `m.mu` 下取一次快照，**不访问 DB**；
  - `Manager.SetRoomMeta(roomID string, title *string, isPublic *bool) error`（内存侧，404 语义与既有一致）；
  - `Manager.CloseRoomByAdmin(roomID, reason string) error`（受控导出既有 `closeRoom`，仍走同一 remove/broadcast 路径）；
  - `Store.ListRoomMetas(ctx, ids []string) (map[string]RoomMeta, error)`（**批量**，避免 N 次查询）；
  - `Store.QueryRoomMetas(ctx, q RoomMetaQuery) ([]RoomMeta, int64, error)`（管理端分页/搜索/公开性过滤）。
- **验证**：`go test ./internal/usecase/ ./internal/store/ -count=1`（store 带 `PR_TEST_DB_DSN`）；`gofmt`/`go vet`。

### T2 服务端：公开房间 API
- **文件**：`internal/handler/publicroom.go`（新）、`router.go`、`internal/config/{config_struct,config_load}.go`（`PR_PUBLIC_ROOMS_PER_MINUTE/_BURST` 并入 `IPCConfig`）
- **最小改动**：`GET /api/public-rooms?limit=&offset=` → `ListRooms()` ∩ `is_public`（含主播离线宽限中的房间，标 `hostOffline:true`）+ `ListRoomMetas` 补标题与**房主昵称**（不含邮箱/角色/状态）；响应字段显式白名单；分页默认 20/上限 50；按 IP 令牌桶限速。
- **验证**：`go test ./internal/handler/ -count=1`（只含公开房、密码房不出现、缺元数据降级、越界截断、限速 429）。

### T3 服务端：房主的标题/公开性
- **文件**：`internal/handler/roommeta.go`（新）、`router.go`、复用 T1
- **最小改动**：`PATCH /api/rooms/:id/meta`（`RequireAuth`）body `{title?, isPublic?}` → 校验 `OwnerUserID == 当前用户`（否则 403）→ 标题服务端清洗（去零宽/Bidi、≤60）→ `SetRoomMeta` + **异步** upsert；房间不存在 404。
- **验证**：`go test ./internal/handler/ -count=1`（非房主 403、超长/控制符被清洗、成功投递一次 upsert、404）。

### T4 服务端：管理端房间能力与审计
- **文件**：`internal/handler/admin.go`、`router.go`、+ `_test.go`
- **最小改动**：
  - `GET /api/admin/rooms?search=&public=&limit=&offset=`（含未公开；内存快照 ∪ DB 元数据）；
  - `POST /api/admin/rooms/:id/close`（`CloseRoomByAdmin` + `Hub.CloseRoom` + 审计 `room.force_close`）；
  - `POST /api/admin/rooms/:id/unpublish`（`is_public=false` + 审计 `room.unpublish`，幂等）；
  - `GET /api/admin/audit?limit=&offset=`（分页 + 字段白名单）；
  - 补一期缺口：`/api/admin/logs` 加限速；CORS 预检补 `PATCH` 与 `Authorization`。
- **验证**：`go test ./internal/handler/ -count=1`（非 admin 403、强关断连 + 审计一行、下架后公开列表消失、分页边界）。

### T5 前端：公开房间列表页 + 导航（等品牌线）
- **文件**：`client/src/views/PublicRoomsView.vue`、`client/src/api/publicRooms.ts`、`client/src/stores/publicRooms.ts`、`router.ts`、`components/AppHeader.vue`
- **最小改动**：`/rooms` 路由 + 顶栏入口；卡片显示标题（空则「未命名房间」）、房主昵称、在座人数、主播离线标记；点击进 `/room/:code`（免登录）；加载/空/失败三态；分页或加载更多。
- **验证**：`npm --prefix client run build` exit 0；真机渲染/进房/空态/375px。

### T6 前端：房主的标题与公开开关
- **文件**：`components/HostPanel.vue`、`stores/room.ts`（仅加一个 PATCH 与状态字段）、`api/rooms.ts`（新）
- **最小改动**：房主可见的标题输入 + 「公开到房间列表」开关 → `PATCH /api/rooms/:id/meta`；保存中禁用、失败显示服务端文案、成功后本地同步。
- **验证**：真机改标题后刷新仍在、打开公开后 `/rooms` 能搜到、非房主看不到控件。

### T7 前端：管理端页面
- **文件**：`views/AdminView.vue`、`api/admin.ts`、`stores/admin.ts`、`router.ts`
- **最小改动**：`/admin`；五区块（用户/房间/指标/日志/审计），房间区含**强制关闭**与**下架**（二次确认），日志与审计**一律文本插值**；入口只对 admin 显示，但授权由服务端判定。
- **验证**：非 admin 访问显示无权限且接口 403；admin 全流程走通；375px 可用。

### T8 验收脚本（服务端+前端都落地后）
- **文件**：`test/script/verify-public-rooms.mjs`、`test/script/verify-admin.mjs`
- **判据**：见 §0；管理端 admin 凭据走 `--admin-user/--admin-pass`（本机测试库已有 `pr_admin`，**不写进仓库**）。
- **验证**：两脚本全 PASS + 重跑既有矩阵。

### T9 部署与线上验收
- 重部署到 `60.205.212.25`（生产库只做加法，先 `pg_dump -s`）；公网跑公开列表（含密码房不泄密、观众免登录进房）、房主改标题/公开、管理端强关/下架 + 审计、S-1/S-5 复测。

### T10 管理员账号（需用户输入）
- 用户注册后给用户名 → 生产库 `UPDATE users SET role='admin' WHERE username='…'`；否则继续用 `pr_admin`。注册流程**不提供自助提权**（刻意）。

## 5. 三期衔接大纲

`/ws/user` 用户通道（ticket 必填）+ 好友（请求/同意/删除/拉黑，一行存对称语义）+ 私聊落库与离线未读 + **房间聊天也落库**（30 天清理）+ `verify-social.mjs`。届时另立计划（含 `friendships`/`messages` 两张表加法迁移）。**二期不做。**

## 6. 风险与缓解

| 风险 | 缓解 |
| :--- | :--- |
| 公开列表泄露隐私 | 响应字段白名单 + 只含公开房 + 不返回邮箱/角色/状态/密码/成员 |
| 标题注入/欺骗 | 写入端清洗（零宽/Bidi、长度上限）+ 前端纯文本插值 |
| 强关/下架副作用 | `RequireAdmin` + 走既有 `closeRoom`（含 room-closed 广播）+ 实际变更写审计 |
| 内存有房、库里无元数据 | 降级展示 + 自愈投递 upsert |
| 与品牌线冲突 | T5–T7 显式后置 |
| 脚本依赖文案/testid | 不改 testid 与主行动文案；若展示改动致红则**回退展示改动** |

## 7. 退役轨道

不退役任何既有路径；二期与一期的差异全部是新增端点与新增页面。

## 8. 执行路由

- T1–T4：一条线立即并行开工。
- T5–T7：品牌线落地并复核后一条线串行。
- T8：前后端都落地后一条线。
- T9：协调者亲自做；T10 需用户输入。
