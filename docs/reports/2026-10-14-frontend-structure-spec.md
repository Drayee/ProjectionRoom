# 新目录结构与逻辑下沉边界

> 票：[#11 新目录结构与逻辑下沉的边界](https://github.com/Drayee/ProjectionRoom/issues/11)
> 日期：2026-10-14
> 前置：视觉方向已定稿（票 [#10](https://github.com/Drayee/ProjectionRoom/issues/10)，B 数字切片）、动效规格已定稿（票 [#13](https://github.com/Drayee/ProjectionRoom/issues/13)）
> 本文是 **#18 骨架票 / #19 实现票** 的目录结构依据。所有分类结论都来自实测（import 扫描），不是估计。
> 标记：`[实测]` = 本次对 `client/src` 全量扫描得出。

---

## 0. 结论速览

| 问题 | 结论 |
| :--- | :--- |
| **`ui/` 准入判据** | **允许读 store 与 `useRouter` 导航；禁止发请求与 `import ../api/`、`../composables/`**（§1） |
| **`features/` 分桶** | **按域分**（`shell/` `room/` `auth/` `posts/` `rooms/` `segment/` `admin/`）（§2） |
| **`AppHeader` 归属** | **`ui/AppHeader.vue`**（它只读 `stores/auth`、不发请求，按新判据合法）（§2.2） |
| **新 composable 放哪** | **`composables/`**（现有目录，允许新增；不改动现有 9 个）（§3） |
| **提示体系** | `TooltipLayer.vue` → `ui/`；`directives/tip.ts` 留原地（§5） |
| **落地页动图槽位** | **删除**，不再保留（§6） |
| **旧文件处置** | **全删，新结构从零建**（零残留；本票不产出迁移映射表）（§7） |

**本票同时改掉了地图的一处铁律**：`ui/` 的准入判据从「不许 import store」改为「允许读 store，只禁发请求与 `import ../api/`」。地图 #8 的 Notes 已同步。

---

## 1. `ui/` 准入判据（第 1 问）

### 1.1 判据（可机械核对）

一个 `.vue` 可以放进 `ui/`，当且仅当**同时**满足：

1. **不发请求**：源码中无 `fetch(`、无 `XMLHttpRequest`、无 `EventSource`、无 `new WebSocket`；
2. **不 import `api/`**：无 `from '../api/...'` 形式的导入；
3. **不 import `composables/`**：无 `from '../composables/...'`（`ui/` 若依赖逻辑 composable，说明它已拥有逻辑，应进 `features/`）。

> **允许**：`import { useXxxStore } from '../stores/...'`、`import ... from 'vue'`、`import { RouterLink, useRouter, useRoute } from 'vue-router'`（**含 `useRouter().push()` 这类导航**）。

**为什么允许 `useRouter` 导航**：导航是"链接的编程式写法"，不产生数据副作用、不改变授权（授权 100% 在服务端）。`AppHeader` 的登出后 `router.push('/')`、`MyRoomsCard` 的 `router.push('/room/xxx')` 都属此类。**禁止**的是"发请求"——那才是 `ui/` 不该有的能力。

**为什么允许读 store**：`ui/` 的价值是"这一层不会自己发请求"，而不是"零状态依赖"。Pinia store 在本项目里是**只读快照 + 动作**的形态；强行要求"props 进 events 出"会把 `MemberList`、`TopologyBadge` 这类本质上是展示的组件逼进 `features/`，反而让 `features/` 失去"域逻辑"的含义。

> **一处自我更正**：本规格早期草稿曾把"不得用 `useRouter().push/replace`"列为第 4 条判据。那**超出了用户裁决**（用户定的是"只禁发请求与 `import api/`"），且与"`AppHeader` 归 `ui/`"的裁决自相矛盾——`AppHeader` 实测用了 `useRouter`。已删除该条。

### 1.2 实测分类（`[实测]`，全量 33 个 `.vue`）

**真的发请求的只有 2 个**：
| 文件 | 请求 |
| :--- | :--- |
| `components/JoinRoomCard.vue:60` | `fetch(\`/api/rooms/${code}\`)` |
| `components/SliceToolPanel.vue:161` | `fetch('/api/downloads/segmenter')` |

**另有 9 个通过 `api/` 层发请求**（`[实测]`）：`AuthPanel`、`AdminView`、`PostDetailView`、`PostEditorView`、`PostsView`、`PublicRoomsView`、`ResetPasswordView`、`SegmentUpload`、`VerifyEmailView`。

**`vue-router` 的实际用法**（`[实测]`）：22 个 `.vue` 都 import 了 `vue-router`，但其中多数只用 `RouterLink`（声明式）。用 `useRouter` 的：`ResetPasswordView`、`UserView`、`PostsView`、`PostDetailView`、`PostEditorView`、`RoomView`、`AuthPanel`、`AppHeader`、`CreateRoomCard`、`JoinRoomCard`、`MyRoomsCard`。

### 1.3 四个候选的归属（票里点名）

| 组件 | 实测依赖 | 归属 | 理由 |
| :--- | :--- | :--- | :--- |
| `BrandIcon.vue` | 仅 `vue` | **`ui/`** | 零依赖，唯一图标渲染点（CSS mask + `currentColor`） |
| `TooltipLayer.vue` | `vue` + `directives/tip` | **`ui/`** | 全局提示图层，读 `tipState` 状态、无请求 |
| `MemberList.vue` | `BrandIcon` + `stores/room` | **`ui/`** | 只遍历 `store.members` 渲染，无请求、无导航 |
| `TopologyBadge.vue` | `vue-router` + `BrandIcon` + `stores/room` | **`ui/`** | 5 个 `computed` 文本派生 + `RouterLink` 深链，无请求 |

**`AppHeader.vue`**（票第 2 问单独问）：实测 import `stores/auth` + `vue-router`（含 `useRouter`），无 `fetch`、无 `api/`。
→ **归 `ui/AppHeader.vue`**。它用 `useRouter().push('/')` 做登出后跳转（§1.1 允许），只读 `stores/auth`、不发请求，按判据合法。**用户裁决**：归 `ui/`。

### 1.4 机械核对结果（`[实测]`）

按 §1.1 三条判据对 15 个候选逐一核对：

| 文件 | fetch | api/ | composables/ | 判定 |
| :--- | :--- | :--- | :--- | :--- |
| `BrandIcon.vue` | ✗ | ✗ | ✗ | **PASS → ui/** |
| `TooltipLayer.vue` | ✗ | ✗ | ✗ | **PASS → ui/** |
| `MemberList.vue` | ✗ | ✗ | ✗ | **PASS → ui/** |
| `TopologyBadge.vue` | ✗ | ✗ | ✗ | **PASS → ui/** |
| `AppHeader.vue` | ✗ | ✗ | ✗ | **PASS → ui/** |
| `ChatPanel.vue` | ✗ | ✗ | ✗ | **PASS → ui/**（含 `store.sendChat` 写动作，见下注） |
| `PlayerStage.vue` | ✗ | ✗ | ✗ | **PASS → ui/**（含 `setVideoElement` 硬契约，见下注） |
| `HeroMotion.vue` | ✗ | ✗ | ✗ | 将删除（§6） |
| `HomeFab.vue` | ✗ | ✗ | ✗ | PASS（但只服务首页外壳 → 归 `features/shell/`） |
| `MyRoomsCard.vue` | ✗ | ✗ | ✗ | PASS（但只服务房间域 → 归 `features/room/`） |
| `AboutView.vue` / `ComingSoonView.vue` / `HelpView.vue` / `LoginView.vue` / `RegisterView.vue` | ✗ | ✗ | ✗ | 是**页面**，归 `pages/`（不进 `ui/`） |

> **两条需要实现时注意的**：
> - `ChatPanel.vue` 虽通过判据，但它调 `store.sendChat()`（**写动作**）→ 实现时**建议归 `features/room/`**（判据是准入下限，不是强制归属；域归属按 §2 规则优先）。
> - `PlayerStage.vue` 同理：它含 `setVideoElement` 硬契约与 `resumeAfterGesture` 交互入口 → **归 `features/room/`**。
>
> 即：**通过 `ui/` 判据 ≠ 必须放 `ui/`**。判据回答"能不能"，§2 回答"该不该"。

---

## 2. `features/` 分桶（第 2 问）

### 2.1 按域分，不按页面分

```
features/
├── shell/     标题栏、FAB、分区导航、等待加入占位页
├── room/      房间室：播放舞台、成员、聊天、房间码、主播控制
├── auth/      登录/注册/找回密码面板
├── posts/     帖子列表/详情/发帖
├── rooms/     公开房列表
├── segment/   服务端切片（本轮不做，见 Out of scope）
└── admin/     管理端（本轮不做，见 Out of scope）
```

**为什么按域而不按页面**：`AppHeader` 被 15 个页面引用、`JoinRoomCard` 被落地页与房间室共用——按页面分会导致共用组件无处安放或复制多份。按域分让"这个组件属于哪个业务能力"有唯一答案。

### 2.2 跨域组件的归属规则

| 情况 | 处置 |
| :--- | :--- |
| 被 ≥3 个域共用，且只读 store、不发请求 | → **`ui/`**（如 `AppHeader`、`BrandIcon`、`TooltipLayer`） |
| 被 ≥2 个域共用，但发请求或含域逻辑 | → **`features/<主域>/`**，并在组件注释里写明"被哪些域引用" |
| 只被一个域使用 | → 该域的目录下 |

### 2.3 实测域归属（本轮要实现的组件）

| 现文件 | 新位置 | 依据 |
| :--- | :--- | :--- |
| `AppHeader.vue` | `ui/AppHeader.vue` | 见 §1.3 |
| `BrandIcon.vue` | `ui/BrandIcon.vue` | 零依赖 |
| `TooltipLayer.vue` | `ui/TooltipLayer.vue` | 读状态、无请求 |
| `MemberList.vue` | `ui/MemberList.vue` | 只读 `store.members` |
| `TopologyBadge.vue` | `ui/TopologyBadge.vue` | 文本派生 + 深链 |
| `HomeFab.vue` | `features/shell/HomeFab.vue` | 仅首页外壳用 |
| `ChatPanel.vue` | `features/room/ChatPanel.vue` | 调 `store.sendChat`（写动作） |
| `PlayerStage.vue` | `features/room/PlayerStage.vue` | 调 `store.setVideoElement`（硬契约） |
| `HostPanel.vue` | `features/room/HostPanel.vue` | 主播控制，多写动作 |
| `JoinRoomCard.vue` | `features/room/JoinRoomCard.vue` | 发 `fetch`，落地页与房间室共用 |
| `CreateRoomCard.vue` | `features/room/CreateRoomCard.vue` | 发请求（经 store）+ 写凭据 |
| `MyRoomsCard.vue` | `features/room/MyRoomsCard.vue` | 读 `sessionStorage` + `useRouter` |
| `AuthPanel.vue` | `features/auth/AuthPanel.vue` | import `api/auth` |
| `PostsView.vue` / `PostDetailView.vue` / `PostEditorView.vue` | `features/posts/` | import `api/posts` |
| `PublicRoomsView.vue` | `features/rooms/PublicRoomsView.vue` | import `api/publicRooms` + store |
| `ComingSoonView.vue` | `features/shell/ComingSoonView.vue` | 占位页，props 驱动 |

---

## 3. composable 的粒度与命名（第 3 问，**票里点名"决定边界会不会被捅破"**）

### 3.1 放哪里：`composables/`（允许新增）

**裁决**：新建的展示层 composable **放现有 `composables/`**，**允许新增文件，但不得改动现有 9 个**。

**"不碰逻辑层"的边界因此精确化为**：
- ✅ **允许**：在 `composables/` **新增**文件；
- ❌ **禁止**：修改现有 9 个（`useChunkPlayer` / `useChunkRequester` / `useChunkStore` / `useIceConfig` / `useMediaIndex` / `useSignaling` / `useSyncClock` / `useTopology` / `useWebRTC`）的任何一行；
- ❌ **禁止**：修改 `api/`(9) / `stores/`(5) / `utils/`(11) / `types/`(4) / `gen/`(1) 的任何一行。

### 3.2 两套 composable 如何区分（第 4 问）

| 维度 | 逻辑层（现有 9 个） | 展示层（本轮新增） |
| :--- | :--- | :--- |
| **命名前缀** | `use` + **协议/运行时名词** | `use` + **页面/域名词** |
| 例子 | `useWebRTC`、`useChunkPlayer`、`useSignaling`、`useTopology`、`useSyncClock`、`useIceConfig`、`useMediaIndex`、`useChunkRequester`、`useChunkStore` | `useRoomEntry`、`useRoomCodeShare`、`useAuthForm`、`usePostList`、`usePostDraft`、`usePublicRoomsPage` |
| **判据** | 是否在描述**协议/媒体/时钟**这类与界面无关的机制 | 是否在描述**某个页面上的交互** |
| **文件头注释** | （不动） | **必须**写一行：`// 展示层 composable：<页面> 的交互逻辑；不含协议实现` |

**为什么不用目录区分**（例如 `composables/ui/`）：本项目 `composables/` 只有 9 个文件，再分一层目录收益低于成本；命名前缀 + 文件头注释已足够区分，且避免了 import 路径变更。

### 3.3 具体下沉清单（票里点名的三个）

| 现文件 | 内嵌逻辑（实测） | 下沉为 | 组件剩下的 |
| :--- | :--- | :--- | :--- |
| `SegmentUpload.vue`（954 行，script 501 行） | 六态机、`AbortController` 生命周期、本地时长探测、上传→提交→轮询编排、产物下载、zip 解包写盘开播 | **`useSegmentUpload.ts`**（本轮不做，见 Out of scope） | 只留渲染 |
| `RoomView.vue`（450 行） | 进房凭据装配（`readJoin` → `newClientId` → `enterRoom`）、离开（`leaveRoom`）、复制/分享两套状态机 | **`useRoomEntry.ts`** + **`useRoomCodeShare.ts`** | 只留渲染 |
| `SliceToolPanel.vue`（599 行） | `fetch('/api/downloads/segmenter')`、平台识别三函数、命令文本派生 | **`useSegmenterManifest.ts`**（本轮不做，见 Out of scope） | 只留渲染 |
| `AuthPanel.vue`（696 行） | 三页签状态机、本地校验、`forgot` 分诊、`submit()` 全流程 | **`useAuthForm.ts`** | 只留渲染 |
| `PostsView.vue` | 排序/分页、`IntersectionObserver` 无限滚 | **`usePostList.ts`** | 只留渲染 |
| `PostEditorView.vue` | `localStorage` 草稿读写 + 卸载自动存 | **`usePostDraft.ts`** | 只留渲染 |

### 3.4 硬契约：下沉时**必须逐字保留**的调用

这些是逻辑层与展示层的唯一接缝，下沉不等于改写：

| 契约 | 位置 | 要求 |
| :--- | :--- | :--- |
| `store.setVideoElement(el)` | `PlayerStage.vue:9`（挂载）/ `:13`（卸载） | 全仓仅此 2 处调用，是 `<video>` 与 store 的唯一通道；**逐字保留** |
| `@click`/`@keydown` → `store.resumeAfterGesture()` | `PlayerStage.vue:121-123` | 自动播放门控的**唯一**恢复路径 |
| `readJoin()` → `newClientId()` → `store.enterRoom()` | `RoomView.vue:86-102` | 全应用唯一把凭据变成房间会话的地方 |
| `store.leaveRoom()` | `RoomView.vue:109` | 唯一拆除点（否则信令/WebRTC/定时器不停） |
| `hostToken` 传递 | `RoomView.vue:100` | 主播宽限期接回主播位必须携带 |
| `store.publishMediaDirectory` | `HostPanel.vue:143` | 主播选片唯一入口 |
| `store.play()` / `seekTo()` / `setRate()` / `pause()` | `HostPanel.vue:164/153/172/323` | 播放控制的唯一界面调用点 |

---

## 4. 新目录树（逐目录一行职责）

```
client/
├── index.html                      SPA 入口（不动；favicon/title/meta 保留）
├── public/
│   ├── icons/                      本地 SVG 图标（按票 #9 清单复制 44 个，ASCII 名）
│   └── fonts/                      子集化 woff2（chillduan-sans-black / maoken-glitch-sans）
└── src/
    ├── main.ts                     入口装配：createApp + pinia + router + v-tip 注册 + debug 钩子
    ├── App.vue                     路由出口 + 全局提示图层挂载（无 style 块）
    ├── router.ts                   18 条路由全量保留；未实现页面走新占位页
    ├── debug.ts                    window.__pr 调试钩子（一行不改）
    ├── styles/
    │   ├── tokens.css              B 的 design token（色彩/圆角/间距/三档时长/故障节奏）
    │   └── base.css                元素基线（body/button/input/a）+ 重置
    ├── pages/                      路由页面（一个路由一个文件，只做编排）
    ├── components/
    │   ├── ui/                     纯展示原子（准入判据见 §1）
    │   └── features/               域组件（分桶见 §2）
    ├── composables/                交互逻辑（现有 9 个不动 + 本轮新增展示层 composable）
    ├── directives/tip.ts           v-tip 指令（留原地）
    ├── api/                        逻辑层（不碰）
    ├── stores/                     逻辑层（不碰）
    ├── utils/                      逻辑层（不碰）
    ├── types/                      逻辑层（不碰）
    └── gen/                        生成代码（不碰）
```

**样式分层**：
- `tokens.css`：`:root` 变量。B 的 token 见地图 Notes；**三档时长 + 故障节奏也在这里定义**（`--dur-micro/comp/morph`、`--ease`、以及故障节奏组）。
- `base.css`：元素级基线。**原 `styles.css` 的公共类（`.card`/`.glass`/`.badge`/`.icon-btn`/`.page-glass`/`.muted`/`.mono`/`.error-bar`/`.ok-bar`）不保留**——它们是旧视觉的具身，按"零残留"应删；各组件用 scoped 样式 + token 自持。

---

## 5. 提示体系归属（第 5 问）

| 文件 | 归属 | 理由 |
| :--- | :--- | :--- |
| `TooltipLayer.vue` | **`ui/TooltipLayer.vue`** | 全局唯一图层，读 `tipState`、无请求、无导航 → 按 §1 判据合法 |
| `directives/tip.ts` | **留原地 `directives/tip.ts`** | 它是**入口装配**（`main.ts` 里 `app.directive('tip', tipDirective)` 注册），不是展示层组件 |

**职责划分保持**：指令写状态 → 图层读状态渲染。`App.vue` 挂载图层（必须在路由之外，否则换页会重建图层、正在显示的提示被卸载）。

---

## 6. 落地页动图槽位（第 6 问）

**删除，不再保留。**

删除清单：
- `src/heroAssets.ts`（`HERO_LOOPS` + `heroLoopUrl`）
- `src/components/HeroMotion.vue`
- `public/hero/`（含 `README.md`）
- `styles.css` 里的 `--hero-motion-w/h/opacity/blur` 四个变量

**理由**：
1. B 定稿的落地页靠**故障黑体字形 + 三片撕裂 + 直角结构**立住，不依赖背景动图（票 #10 已确认）；
2. 原槽位**从未有过素材**（`HERO_LOOPS` 一直是空数组，`[实测]`），保留它是保留一层无用抽象；
3. 它是旧视觉（月夜玻璃）的具身，与"零残留"目标冲突。

---

## 7. 旧文件处置：全删，新结构从零建

**裁决**：**保留零残留**。本票**不产出"哪些现有文件移到哪"的映射表**。

- §2.3 的表**只用于说明域归属的判断依据**（新组件该放哪个域），**不是迁移计划**——旧文件不会"搬"过去。
- `#18` 骨架票执行 `git rm`：`views/`（15 个）、`components/`（18 个）、`styles.css`、`heroAssets.ts`、`public/hero/`、`public/icons/`（22 个旧 SVG）、`docs/prototypes/visual-direction.html`。
- 新结构下的组件是**新写的**；旧文件里的内嵌逻辑按 §3.3 下沉到 composable 时**重写**（可参考旧实现，但不是复制粘贴）。

> **一处与用户先前选择的偏差（已裁决）**：本次问答中曾选"迁移旧文件到新目录"，但在后续冲突裁决中改为"保留零残留：旧文件全删，新结构从零建"。**以冲突裁决为准**——因为它与地图 Destination 和 #18 票的任务一致。

---

## 8. 给 #18 / #19 的检查项

- [ ] `ui/` 下的每个 `.vue` 都通过 §1.1 的 3 条判据（无 `fetch`/`XHR`/`api/`/`composables/`；`useRouter` 允许）
- [ ] `ui/` 里**不出现**含写动作或硬契约的组件（`ChatPanel` / `PlayerStage` 应归 `features/room/`）
- [ ] `features/` 按域分桶，跨域组件按 §2.2 规则处置
- [ ] 新建 composable 全部落在 `composables/`，**文件头有"展示层 composable"注释**
- [ ] **现有 9 个 composable 一行未改**（`git diff` 应为空）
- [ ] **`api/` / `stores/` / `utils/` / `types/` / `gen/` 一行未改**（`git diff` 应为空）
- [ ] §3.4 的 7 条硬契约在下沉后**逐字保留**
- [ ] `styles/tokens.css` 含 B 的 token + 三档时长 + 故障节奏；`base.css` 只含元素基线
- [ ] 旧公共类（`.card`/`.glass`/`.badge`/`.icon-btn`/`.page-glass` 等）**不在新代码里出现**
- [ ] `heroAssets.ts` / `HeroMotion.vue` / `public/hero/` / `--hero-motion-*` 全部删除且无引用
- [ ] `TooltipLayer.vue` 在 `ui/`；`directives/tip.ts` 未移动；`App.vue` 仍挂载图层
- [ ] 旧文件已 `git rm`（`git status` 无残留）
- [ ] `npm --prefix client run build` 绿；`grep -rn "v-html\|innerHTML" client/src` 为 0

---

## 9. 边界

- 本票**只产出目录结构与边界决策**，不改任何源码（实现属 #18 / #19）。
- **未验证**：新结构下的构建通过性（骨架票才知道）。
- **未覆盖**：管理端 / 切片 / 诊断三个域（本轮不做，见地图 Out of scope），但目录已为它们留位（`features/admin/`、`features/segment/`）。
- `[实测]` 数据来自本次对 `client/src` 的全量 import 扫描（33 个 `.vue`）；未做运行期行为验证。
