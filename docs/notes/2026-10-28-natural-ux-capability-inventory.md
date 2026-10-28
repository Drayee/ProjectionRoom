# 自然体验：现有体验面与档案/互动 API 缺口盘点

> **票**：[盘点现有体验面与档案/互动 API 缺口](https://github.com/Drayee/ProjectionRoom/issues/41)  
> **父地图**：[自然体验 wayfinder #40](https://github.com/Drayee/ProjectionRoom/issues/40)  
> **日期**：2026-10-28  
> **性质**：一手源码 / 规格对照的**能力盘点**；**不**做产品最终决策，**不**改业务代码。  
> **方法**：只读票面路径清单中的源码与规格；结论均带回路径引用。

---

## 0. 一句话摘要

社交主路径（帖子读/发/详情点赞/评论删、好友含搜索、公开档案双视角、私聊）**前后端已贯通**；自然体验层仍缺的是**路由页过渡、导航当前态、统一按钮/头像/相对时间组件、列表内点赞、收藏、删/编辑帖、关注/粉丝、按作者列帖、按人列房、`GET /api/rooms/mine`**。多数「无」是 API + UI 双缺，少数是「后端有计数、前端只展示、交互未做」。

---

## 1. 对照路径（票面清单 → 实际落点）

| 票面路径 | 实际一手落点 | 备注 |
| :--- | :--- | :--- |
| `client/src/App.vue` | 同路径 | 全局外壳：顶栏在 `router-view` **外**；**无** `<Transition>` 包裹换页 |
| `AppHeader.vue` | [`client/src/components/ui/AppHeader.vue`](../../client/src/components/ui/AppHeader.vue) | 不在 `components/` 根下 |
| `router.ts` | [`client/src/router.ts`](../../client/src/router.ts) | `meta.header`；无全局守卫 |
| `pages/{Posts,PostDetail,Friends,User,Messages}Page.vue` | 均在 [`client/src/pages/`](../../client/src/pages/) | 另有 `PostEditorPage`、论坛三页等 |
| `utils/avatar.ts` | 同路径 | `avatarOf(id, displayName)` 工具，**无** `UserAvatar` 组件 |
| `styles/tokens.css` | 同路径 | `--dur-micro/comp/morph` + 故障节奏 |
| `internal/handler/posts.go` | 同路径 | 列表/详情/发帖/点赞/评论；**无** PATCH/DELETE 帖 |
| 好友与用户 handler | [`friendships.go`](../../internal/handler/friendships.go)、[`users.go`](../../internal/handler/users.go) | 搜索在好友域：`GET /api/users/search` |
| `CLAUDE.md` API 速查 | §4 | 已知缺口含 `GET /api/rooms/mine` |
| `docs/ACCOUNTS.md` 非目标 | §1.2 | 本期非目标含头像上传等；**未**写收藏/关注 |
| 动效规格 | [`docs/reports/2026-10-14-glitch-motion-spec.md`](../reports/2026-10-14-glitch-motion-spec.md) | 微交互/入场有规格；**无**「路由级叙事过渡」专章 |

---

## 2. 能力 × 现状 × 一手路径

图例：**有** = 端到端可用（或规格已落地实现）；**部分** = 有一侧/半成品/仅展示无交互；**无** = 仓内无实现且无对等 API。

| 能力 | 现状 | 一手路径与证据 |
| :--- | :---: | :--- |
| **路由过渡（换页叙事）** | **无** | [`App.vue`](../../client/src/App.vue) L59–61：`<router-view>` 直接挂载 `Component`，无 `Transition`/mode。动效规格 [`glitch-motion-spec.md`](../reports/2026-10-14-glitch-motion-spec.md) §1.2 有「逐屏交错入场 / FLIP morph」，落在落地/登录壳（[`LandingView.vue`](../../client/src/components/features/shell/LandingView.vue)、[`AuthPanel.vue`](../../client/src/components/features/auth/AuthPanel.vue)），**不是**全站路由过渡。`AuthPanel` 刻意**不**套 `<Transition>`（依赖 transitionend + 双 rAF 在后台标签页会卡住）。 |
| **按钮反馈（统一按压/busy）** | **部分** | Token：[`tokens.css`](../../client/src/styles/tokens.css) L42–46 `--dur-micro: 160ms` 等。规格 §1.4 `:active { scale(.97/.98) }`。实现分散：[`AppHeader.vue`](../../client/src/components/ui/AppHeader.vue) L293–295 `.nav-btn:active { scale(0.92) }` + logout `busy`；[`PostDetailPage.vue`](../../client/src/pages/PostDetailPage.vue) 点赞 `likingBusy` + spinner；好友页 `actionBusy` 禁用。**无**共享 Button 组件；各页自写 `.btn`/`.primary`。 |
| **导航当前态** | **无**（顶栏）/ **有**（页内 tab） | 顶栏 [`AppHeader.vue`](../../client/src/components/ui/AppHeader.vue) L97–110：`RouterLink` + `.nav-btn`，**全仓无** `router-link-active` / `aria-current` 样式（grep 顶栏为 0）。页内 tab 有本地 `.on`：[`PostsPage.vue`](../../client/src/pages/PostsPage.vue) L125–127、[`FriendsPage.vue`](../../client/src/pages/FriendsPage.vue) L350–357；消息会话行有 `aria-current`（[`MessagesPage.vue`](../../client/src/pages/MessagesPage.vue)）。 |
| **好友搜索可发现性** | **部分** | **API 有**：[`friendships.go`](../../internal/handler/friendships.go) `GET /api/users/search`（需登录）；门面 [`api/friends.ts`](../../client/src/api/friends.ts) `searchUsers`。**UI 有但发现性弱**：仅在 [`FriendsPage.vue`](../../client/src/pages/FriendsPage.vue) 第四个 tab「找朋友」（L356、L443–484）；顶栏 NAV（AppHeader L60–67）只有「好友」总入口，**无**直达搜索深链/query。空好友列表文案指向「找朋友」（L386），属页内引导而非全局可发现。 |
| **UserAvatar 复用** | **部分** | 工具 **有**：[`utils/avatar.ts`](../../client/src/utils/avatar.ts) `avatarOf` + `AVATAR_PALETTE`。real 适配层复用（posts/friends/messages）。**组件无**：glob `*Avatar*` = 0。各处内联 `<span class="avatar">`：Friends/Messages/AnimeBoard；**顶栏头像**（AppHeader L44–50、L148–155）用**另一套**「名字哈希 → hsl」取色，与 `avatarOf` 的五色板 **不一致**。 |
| **点赞 · 详情** | **有** | 后端：[`posts.go`](../../internal/handler/posts.go) L115–117、L396–418 `POST /api/posts/:postId/like` toggle + 权威回读。前端：[`PostDetailPage.vue`](../../client/src/pages/PostDetailPage.vue) L63–85 乐观更新 + 回滚；门面 `toggleLike`。 |
| **点赞 · 列表** | **部分** | 列表卡片只展示 `赞 {{ p.likeCount }}`（[`PostsPage.vue`](../../client/src/pages/PostsPage.vue) L144），**无**点赞按钮/toggle。列表 API 在登录可选识别下可带 `liked`（posts.go L11–12、L235–236），但 UI 未消费交互。 |
| **收藏** | **无** | client/internal grep favorite/bookmark/收藏（业务义）无命中。posts 表/handler 无收藏资源。`ACCOUNTS.md` 非目标未列收藏（等于规格未定义）。 |
| **相对时间** | **部分** | **无**共享 `relativeTime`/`timeAgo` 工具。多处本地 `shortTime`：今天 `HH:mm`、否则 `M/D HH:mm`——[`MessagesPage.vue`](../../client/src/pages/MessagesPage.vue) L106–117；admin 三页各一份。帖子详情直接打 RFC 串（PostDetail L124 `post.createdAt`）；UserPage 加入日 `raw.slice(0, 10)`（L96–99）。**不是**「3 分钟前」类相对文案。 |
| **按作者列帖** | **无** | `ListPostsInput` 仅 `sort|page`（[`types/posts.ts`](../../client/src/types/posts.ts) L46–49）。`GET /api/posts` query 仅 sort/limit/offset（posts.go L206–246）。store `ListPosts` 无 author 过滤形参（`internal/store/posts.go` ListPosts）。UserPage **无**「TA 的帖子」区块。 |
| **按人列房** | **无** | 公开列表按房不按人（`public-rooms`）；响应刻意不暴露 `ownerUserId`（[`publicroom.go`](../../internal/handler/publicroom.go) 注释）。UserPage 无「TA 的房间」。store 有 `owner_user_id` 列与 `ListRoomMetas` 按**房间码**批量，**无**「按 owner 列出」公共 API。 |
| **删/编辑帖** | **无**（帖本体）/ **有**（删评论） | 路由注册：POST create/like/comments + DELETE **仅** `/comments/:commentId`（posts.go L114–117）。**无** `PATCH/DELETE /posts/:id`。门面 [`api/posts.ts`](../../client/src/api/posts.ts) 无 update/deletePost。PostDetail/Editor 无删改帖 UI。删评论：前后端均有（PostDetail L99–106；posts.go L502–527）。 |
| **关注 / 粉丝** | **无** | client + internal 对 follow/follower/following/关注/粉丝 **0** 业务命中。关系模型是 **好友**（friendships pending/accepted/blocked），见 ACCOUNTS §7.1 与 friendships handler。 |
| **UserPage 他人 / 本人块** | **有** | [`UserPage.vue`](../../client/src/pages/UserPage.vue)：`isSelf`（L54–55）；他人 `getPublicProfile`；本人读 auth store + 昵称 `PATCH /api/me` + 邮箱/验证态/退出。后端 [`users.go`](../../internal/handler/users.go) `GET /api/users/:id` 免登录、`PATCH /api/me` 需登录；不存在/封禁统一 404。**缺口（信息架构）**：有头档与本人编辑，**无**帖子/房间/关注等下属内容块（与上两行一致）。 |
| **rooms/mine（我的房间）** | **无** | [`CLAUDE.md`](../../CLAUDE.md) §10.3 明文：`GET /api/rooms/mine` 未做，「我的房间」只读本标签页 sessionStorage。internal grep `/rooms/mine` = 0。client 无「我的房间」页面文案/路由。sessionStorage 现用于 access token、进房凭据（`joinSession.ts`）等，**不是**跨设备房间列表 API。 |

---

## 3. 已具备的相关体验（便于 grilling 对照，非决策）

| 区域 | 已有事实 | 路径 |
| :--- | :--- | :--- |
| 顶栏常驻与换页不闪 | 顶栏在路由外；variant 由 `meta.header` + 登录例外 | App.vue、router.ts、AppHeader.vue |
| 分区导航 | 登录后 icon-first NAV：公开房/帖子/论坛/好友/消息/帮助 | AppHeader L60–67 |
| 帖子信息流 | 瀑布流 + latest/hot + 无限滚动 + 发帖入口 | PostsPage、api/posts、posts.go |
| 论坛主题串 | 板块列表排序 latest-reply/latest；详情复用 posts | posts.go listAnimeThreads、AnimeBoardPage |
| 好友全流程 | 列表/请求/搜索/加删/拉黑/（解除） | FriendsPage、friendships.go、api/friends |
| 私聊 | `/messages?peer=` URL 真源；shortTime；未读单一写入点 | MessagesPage、dmRoute、auth store |
| 动效预算 | 常驻 ≤5；只动 transform/opacity；reduced-motion 全停 | tokens.css、glitch-motion-spec |
| 头像上传 | 规格非目标（内置字符头像） | ACCOUNTS.md §1.2；与 MinIO 图床是另一条线 |

---

## 4. 缺口聚类（仍不做产品拍板）

仅把盘点结果收成三类，供 #40 子票（grilling）引用：

1. **纯体验层（可不新增业务 API）**  
   路由过渡、顶栏 `router-link-active`/当前态、统一按钮反馈、`UserAvatar` 组件化（含顶栏与 `avatarOf` 取色对齐）、共享时间展示（shortTime vs 相对时间选型属决策票）。

2. **已有 API、UI 未用尽**  
   列表点赞（liked/likeCount 已在列表契约中）、好友搜索可发现性（能力在 tab 内，缺全局/深链入口）。

3. **API + 模型双缺（或仅文档点名）**  
   收藏；关注/粉丝；删/编辑帖；按作者列帖；按人列房；`GET /api/rooms/mine`（CLAUDE 已知缺口）。档案页下属 feed 依赖本类。

---

## 5. 明确不在本笔记范围

- 是否做收藏/关注、互关是否等于好友 → 交 #42 / #48 等 grilling。  
- 视觉原则与组件契约文案 → #43 / #44。  
- 路由叙事具体动效方案 → #45 / #46。  
- 任何业务代码修改、接口设计终稿。

---

## 6. 来源索引（只列本盘点打开过的一手文件）

- 前端：`client/src/App.vue`、`components/ui/AppHeader.vue`、`router.ts`、`pages/PostsPage.vue`、`PostDetailPage.vue`、`FriendsPage.vue`、`UserPage.vue`、`MessagesPage.vue`、`utils/avatar.ts`、`styles/tokens.css`、`api/{posts,users,friends}.ts`、`types/posts.ts`  
- 后端：`internal/handler/{posts,users,friendships,publicroom,router}.go`、`internal/store/{posts,roommeta}.go`（检索）  
- 规格/地图：`CLAUDE.md` §4/§10、`docs/ACCOUNTS.md` §1/§7/§10、`docs/reports/2026-10-14-glitch-motion-spec.md`、`docs/notes/2026-10-28-natural-ux-wayfinder-draft.md`
