# 契约平移的落地清单与悬空项处置

> 票：[#12 契约平移的落地清单与悬空项处置](https://github.com/Drayee/ProjectionRoom/issues/12)
> 日期：2026-10-14
> 前置：目录结构已定稿（票 [#11](https://github.com/Drayee/ProjectionRoom/issues/11)）、动效规格已定稿（票 [#13](https://github.com/Drayee/ProjectionRoom/issues/13)）、视觉已定稿（票 [#10](https://github.com/Drayee/ProjectionRoom/issues/10)）
> 本文是 **#18 骨架 / #19 实现** 的契约依据；也是地图「契约整体平移」原则的落地清单。
> 标记：`[实测]` = 本次对 `test/script/**` 与 `client/src/**` 扫描得出；`[裁决]` = 用户本轮拍板。

---

## 0. 结论速览

| 问题 | 结论 |
| :--- | :--- |
| **归属表** | §1（21 个脚本消费 testid + 1 个 DOM id + 逐字文案 → 新位置） |
| **`#media-dir-input`** | **删除**；媒体注入**仍有兜底路径**（§2）——不是"脚本红"，是"少一条路径" |
| **「离开房间」** | **改为纯图标按钮**；**接受 `verify-ice.mjs` 变红**（§3）`[裁决]` |
| **房间页密码框** | **零 `input[type="password"]`**（§4）`[裁决]` |
| **`window.__pr`** | `debug.ts` 一行不改；**新展示层有 1 条配合项**：`<video>` 必须真的交给 store（§5） |
| **约 190 个无脚本 testid** | **全部丢弃，一个不留**（§6）`[裁决]` |
| **悬空契约** | 列清单、**不造桩、不改脚本**（§7）`[裁决]` |

---

## 1. 归属表

### 1.1 脚本消费的 21 个 `data-testid`（`[实测]`）

**重要更正**：字面量扫描只找到 **14 个**，因为 `diag-*` 那 7 个是通过变量拼进 `[data-testid="' + id + '"]` 的（见 `verify-health.mjs:58`、`verify-ice.mjs:355`、`verify-unassigned.mjs:61`）。**14 + 7 = 21，票里的数字是对的。**

| # | testid | 消费方 | 新位置 | 本轮 |
| ---: | :--- | :--- | :--- | :--- |
| 1 | `room-code` | `verify-room-resume.mjs:153` | `pages/RoomPage` 的房间码按钮 | ✅ 做 |
| 2 | `room-code-copied` | `verify-room-resume.mjs:173` | 同上（「已复制」提示） | ✅ 做 |
| 3 | `room-code-copy-failed` | `verify-room-resume.mjs:180` | 同上（「复制失败」提示） | ✅ 做 |
| 4 | `diagnostics-drawer` | `verify-health.mjs:55`、`verify-ice.mjs:352`、`verify-unassigned.mjs:58` | `features/room/DiagnosticsDrawer`（本轮**不做**） | ⚠️ 悬空 |
| 5 | `diag-upstream` | `verify-health.mjs:62`、`verify-unassigned.mjs:64` | 同上 | ⚠️ 悬空 |
| 6 | `diag-health` | `verify-health.mjs:63` | 同上 | ⚠️ 悬空 |
| 7 | `diag-health-edges` | `verify-health.mjs:64` | 同上 | ⚠️ 悬空 |
| 8 | `diag-health-schedule` | `verify-health.mjs:65`、`verify-unassigned.mjs:64` | 同上 | ⚠️ 悬空 |
| 9 | `diag-ice` | `verify-ice.mjs:358` | 同上 | ⚠️ 悬空 |
| 10 | `diag-ipv6` | `verify-ice.mjs:358` | 同上 | ⚠️ 悬空 |
| 11 | `diag-selected-pair` | `verify-ice.mjs:358` | 同上 | ⚠️ 悬空 |
| 12 | `slice-tool-panel` | `verify-segment-ui.mjs:118,223,286` | `features/segment/SliceToolPanel`（本轮**不做**） | ⚠️ 悬空 |
| 13 | `slice-tool-state` | `verify-segment-ui.mjs:127,229` | 同上 | ⚠️ 悬空 |
| 14 | `slice-tool-unavailable` | `verify-segment-ui.mjs:230` | 同上 | ⚠️ 悬空 |
| 15 | `slice-tool-row` | `verify-segment-ui.mjs:108,122,231,269` | 同上 | ⚠️ 悬空 |
| 16 | `slice-tool-download` | `verify-segment-ui.mjs:123` | 同上 | ⚠️ 悬空 |
| 17 | `slice-tool-sha-toggle` | `verify-segment-ui.mjs:140,177` | 同上 | ⚠️ 悬空 |
| 18 | `slice-tool-sha-full` | `verify-segment-ui.mjs:186` | 同上 | ⚠️ 悬空 |
| 19 | `slice-tool-verify` | `verify-segment-ui.mjs:126` | 同上 | ⚠️ 悬空 |
| 20 | `slice-tool-usage` | `verify-segment-ui.mjs:125,232` | 同上 | ⚠️ 悬空 |
| 21 | `slice-tool-copy-usage` | `verify-segment-ui.mjs:141,196` | 同上 | ⚠️ 悬空 |

**本轮要做的只有 3 个**（`room-code` 系列）；其余 **18 个悬空**（诊断 8 个 + 切片 10 个）。

### 1.2 DOM id 与结构选择器（`[实测]`）

| 契约 | 消费方 | 处置 |
| :--- | :--- | :--- |
| `#media-dir-input` | `verify-m2.mjs:384,389,395`、`lib/browser.mjs:261,266,271` | **删除**（§2） |
| `document.querySelector('video')` | `diag-control.mjs:127,130` | ✅ 保留：`features/room/PlayerStage` 仍渲染唯一 `<video>` |
| `details` + `summary.innerText.includes('诊断')` | `verify-m4-panel.mjs:36-45` | ⚠️ 悬空（诊断抽屉不做） |
| `summary,button` 含「交给服务器切片」 | `verify-segment-ui.mjs:35,54` | ⚠️ 悬空（切片不做） |
| `.badge` 含「服务端可用」/「服务器切片不可用」 | `verify-segment-ui.mjs:68-70,225,243` | ⚠️ 悬空 |
| `details.tutorial` 含 `segmenter` | `verify-segment-ui.mjs:96,99` | ⚠️ 悬空 |
| 行内 `.name` / `.sha` | `verify-segment-ui.mjs:134,139` | ⚠️ 悬空 |
| `.code-text`（需 `user-select:text`） | `verify-room-resume.mjs:155,207` | ✅ **保留**：房间码按钮内必须有 `.code-text` |
| `a[href*="ffmpeg.org"]` **不存在** | `verify-segment-ui.mjs:151,325` | ⚠️ 悬空（负向判据） |

### 1.3 逐字文案（`[实测]`，本轮相关的）

| 文案 | 消费方 | 处置 |
| :--- | :--- | :--- |
| 「已复制」 | `verify-room-resume.mjs:197` | ✅ 保留（`room-code-copied` 的文本） |
| 「复制失败」+「手动选中」 | `verify-room-resume.mjs:202` | ✅ 保留 |
| 「主播掉线」 | `verify-room-resume.mjs:328` | ✅ 保留（在 `store.recovery.hostOfflineText` 里，来自 store——**新展示层只需把它渲染出来**） |
| 页面文本**无**「房间不存在」 | `verify-room-resume.mjs:333` | ✅ 负向判据：新房间页在等待态不得出现该文案 |
| 页面含 `snapshot.recovery.roomClosed` 原文 | `verify-room-resume.mjs:475-476` | ✅ 保留：房间被关闭时要显示服务端给的原因 |
| 「离开房间」**精确匹配** | `verify-ice.mjs:556` | ⚠️ **本轮会红**（§3 裁决改图标） |
| 页面无 `input[type="password"]` | `verify-auth.mjs:583-584,605` | ✅ 遵守（§4） |
| `pathname === '/room/<code>'` | `verify-auth.mjs:605-606` | ✅ 遵守（不跳登录） |
| 切片与诊断的约 20 条中文断言 | `verify-segment-ui.mjs`、`verify-health.mjs`、`verify-unassigned.mjs` | ⚠️ 悬空 |

### 1.4 路由（`[实测]`，18 条）

**全部保留，path 与 name 都不改**（票 #11 已定「路由表全量保留」）。被脚本硬引用的 3 条：

| path | 消费方 | 备注 |
| :--- | :--- | :--- |
| `/room/:roomId` | `lib/browser.mjs:617`、`verify-auth.mjs:561,605-606`、`verify-m2.mjs:346` | **不可改**：深链 + 刷新 + SPA 回退都依赖 |
| `/login` | `verify-auth.mjs:604`（**负向**：游客不该被跳到这里） | **不可改** |
| `/reset` / `/verify` | `CLAUDE.md:65`（邮件链接写死 `<PR_PUBLIC_BASE_URL>/reset#token=`） | **不可改**：改了会打断找回密码链路 |

---

## 2. `#media-dir-input`（第 2 问）

### 裁决：**删除** `[裁决]`

### 但必须先纠正一个前提（`[实测]`）

我按"接受脚本红"去写规格时查了代码，发现**那个前提不成立**：`#media-dir-input` **不是**媒体注入的唯一通道。

`lib/browser.mjs:259-283` 的 `loadMedia` 是**两级兜底**：
```
① 先试 host.uploadDirectory('#media-dir-input', …)   ← 需要该 id
② 若 snapshot().media.loaded 仍为 false
   → startMediaServer() + injectMediaInPage()         ← 完全不依赖任何 DOM
```
而 `injectMediaInPage`（`browser.mjs:242-257`）直接调 `window.__pr.store.publishMediaDirectory(dt.files)`。

`verify-m2.mjs:368-405` 是同一套两级结构（`[实测]`）。

### 因此真实影响是

| 项 | 影响 |
| :--- | :--- |
| `verify-m2.mjs` | **仍能跑**，走兜底路径（会打印「注入路径（input.files=-1）：构造 FileList → publishMediaDirectory」） |
| `lib/browser.mjs` 的 `seedAndEnter` 路径 | **仍能跑**（9 个脚本用它） |
| 覆盖面 | **变窄**：失去"通过真实 file input 触发 `@change`"这条路径的验证 |

**所以这不是"脚本红"，是"少一条路径"**。删除的代价可接受，但**必须记清楚**——否则后续会话看到 `verify-m2` 打印 `input.files=-1` 会误判为回归。

> **给 #18 / #19 的提示**：删掉 `#media-dir-input` 后，媒体注入只剩调试钩子路径。若后续想恢复真实路径覆盖，需在 `features/room/` 里重新提供一个隐藏 file input（id 可自定，但要同步改 `browser.mjs:261,266,271` 与 `verify-m2.mjs:384,389,395`）——**本图不改脚本**，所以这条路留待下轮。

---

## 3. 「离开房间」（第 3 问）

### 裁决：**改为纯图标按钮**；**接受 `verify-ice.mjs` 变红** `[裁决]`

- 现契约：`verify-ice.mjs:556` 用 `browser: [...document.querySelectorAll('button')].find((b) => b.textContent.trim() === '离开房间')`，且 `:557` 找不到就 `throw new Error('找不到「离开房间」按钮')`。
- 新形态：icon-first 的圆形图标按钮（`aria-label="离开房间"` + `v-tip`），**无文字**。
- 后果：`verify-ice.mjs` 的第 7 段判据（"离开房间 → 刷新定时器必须停"）**会红**。

**这与地图的既有约定冲突，已按裁决记录**：
- 地图 Out of scope 原写「**不改造脚本**」→ 现在多了 `verify-ice.mjs` 一条长期红；
- 票 #10 的「icon-first 保留」写的是"主 CTA + **破坏性动作**保留文字"——本次裁决**推翻了它**对"离开房间"的适用。

**给 #18 / #19 的硬要求**：图标按钮**必须**满足：
1. `aria-label="离开房间"`（否则可访问名丢失）；
2. `v-tip="'离开房间'"`（hover 提示）；
3. 点击必须真的调 `store.leaveRoom()`（这是唯一拆除点，`RoomView.vue:109` 的原契约）。

---

## 4. 房间页零密码框（第 4 问）

### 裁决：**确认，房间页不得出现任何 `input[type="password"]`** `[裁决]`

`verify-auth.mjs:583-605` 的负向判据（`[实测]`）：
```
passwordFields = document.querySelectorAll('input[type="password"]').length
判据 ⑥: pathname === `/room/${roomId}` && passwordFields === 0
```

**新设计要遵守**：
1. `/room/:roomId` 页面树内**零密码框**；
2. 房间密码的输入**只放在落地页的「加入房间」卡片**（`features/room/JoinRoomCard`，在 `/` 路径下，不在判据范围内）；
3. 不得新增"进房后补填密码"的输入（会打挂判据）；
4. 不得新增任何全局守卫把游客跳去 `/login`（判据同时断言 `pathname` 未被改）。

---

## 5. `window.__pr` 与 `debug.ts`（第 5 问）

### 裁决：**确认推理，且列一条配合项** `[裁决]`

**推理确认**：`debug.ts` 的 `snapshot()` 读的是 `stores/room.ts` 的字段（`[实测]`：约 90 个路径）。本图**不碰 `stores/`**（票 #11 铁律），所以字段不变 → `debug.ts` 天然安全 → **一行不改**成立。

**但新展示层有 1 条配合项**（这是票里问"要不要配合"的答案）：

> **`<video>` 必须真的交给 store。** `debug.ts` 的 `snapshot().video.*`（`currentTime`/`paused`/`readyState`/`bufferedEnd`）与 `player.*`（`attached`/`ready`/`mediaSourceState`…）依赖 `store.setVideoElement(el)` 被调用（`PlayerStage.vue:9`/`:13` 是全仓唯一两处调用）。若新 `PlayerStage` 忘了调它，**验收脚本会看到 `player.attached=false`、`video.*` 全空**，但页面看起来正常——这类失败极难归因。

**其余配合项：无。** 新展示层不需要为 `window.__pr` 做任何其他事（钩子由 `main.ts:40` 的构建期守卫安装，生产构建摇掉）。

---

## 6. 约 190 个无脚本消费的 testid（第 6 问）

### 裁决：**全部丢弃，一个不留** `[裁决]`

**丢弃范围**（`[实测]` 抽样，含本轮要做的页面）：
- `AuthPanel` 的 `login-*` / `register-*` / `forgot-*`（19 个）
- `CreateRoomCard` 的 2 个、`JoinRoomCard` 的 4 个、`HostPanel` 的 9 个
- `AdminView` 的 44 个 `admin-*`、`PublicRoomsView` 的 9 个、`UserView` 的 13 个
- `ResetPasswordView` 的 12 个、`VerifyEmailView` 的 13 个
- `AppHeader` / `HomeFab` / `HelpView` / `AboutView` / `ComingSoonView` / `HomeView` 的各若干
- `DiagnosticsDrawer` / `SliceToolPanel` / `SegmentUpload` / `PlayerStage` / `RoomView` 的未消费 testid

**含义**：
1. 新页面**不**为这些元素打旧 testid；
2. 新页面**也不要求**为新元素补 testid（除非实现时另有验收需要）；
3. **只有 §1.1 里标「✅ 做」的 3 个 + §1.2 的 `.code-text` + `video` 是必须打的**。

> **代价（已知情）**：`CLAUDE.md:131` 写「`data-testid` 与主行动按钮文案是验收脚本的契约」，本轮之后新页面几乎无 testid，后续若要为登录/帖子/公开房补验收脚本，将无现成钩子——届时应**新增**（CLAUDE.md 允许"新增可以"），不是"恢复旧名"。

---

## 7. 悬空契约清单（第 1 问的后半）

### 裁决：**列清单、不造桩、不改脚本** `[裁决]`

**悬空定义**：契约在新结构里**没有落点**，因为承载它的页面本轮不做。

| 悬空契约 | 数量 | 归属（未来） | 受影响的脚本（会红） |
| :--- | ---: | :--- | :--- |
| `diagnostics-drawer` + 7 个 `diag-*` | 8 | `features/room/DiagnosticsDrawer` | `verify-health.mjs`、`verify-ice.mjs`、`verify-unassigned.mjs`、`verify-m4-panel.mjs` |
| 11 个 `slice-tool-*` + `.badge` 文案 + `details.tutorial` + `.name`/`.sha` | ~17 | `features/segment/SliceToolPanel` + `SegmentUpload` | `verify-segment-ui.mjs` |
| 「交给服务器切片」「服务端可用」「服务器切片不可用」「清单不可用」 | 4 | 同上 | `verify-segment-ui.mjs` |
| 44 个 `admin-*` | 44 | `features/admin/AdminView` | （无脚本消费，仅文档约束） |

**明确不做**：
- ❌ 不造空壳占位来"保住" testid —— 那会把"未实现"伪装成"已实现"；
- ❌ 不改造脚本让它们 skip —— 地图已定"不改造脚本"；
- ✅ **接受这些脚本长期红**（已在地图 Out of scope 记录）。

---

## 8. 本轮必打的契约（汇总，给 #18 / #19 的 checklist）

### 房间室（`features/room/`）

- [ ] `data-testid="room-code"` —— 房间码按钮（且内部含 `.code-text`，样式 `user-select: text`）
- [ ] `.code-text` 的文本 === `roomId.toUpperCase()`（`verify-room-resume.mjs:207` 断言 `out.roomCode === want`，`want = roomId.toUpperCase()`）
- [ ] 点击复制后，**复制进剪贴板的内容**也 === 大写房间码（`verify-room-resume.mjs:197` 断言 `out.copiedText === want`）
- [ ] 复制需在 `navigator.clipboard` 不可用时回退 `document.execCommand('copy')`（`verify-room-resume.mjs:197` 同时断言 `out.execCalls.includes('copy')`）
- [ ] `data-testid="room-code-copied"` —— 复制成功提示，文本含「已复制」
- [ ] `data-testid="room-code-copy-failed"` —— 复制失败提示，文本含「复制失败」+「手动选中」
- [ ] 等待态渲染 `store.recovery.hostOfflineText`（含「主播掉线」）
- [ ] 等待态**不得**出现「房间不存在」
- [ ] 房间被关闭时渲染 `snapshot.recovery.roomClosed` 原文
- [ ] 「离开房间」按钮：**无文字**、`aria-label="离开房间"`、`v-tip`、点击调 `store.leaveRoom()`
- [ ] 页面树内**零** `input[type="password"]`
- [ ] 唯一 `<video>` 元素，且挂载/卸载调 `store.setVideoElement(el)`
- [ ] `@click`/`@keydown` 触发 `store.resumeAfterGesture()`
- [ ] **不**打 `#media-dir-input`

### 全局

- [ ] 18 条路由 path/name **一字不改**
- [ ] 不新增任何全局路由守卫（游客进房不得被跳 `/login`）
- [ ] `debug.ts` **一行不改**
- [ ] 生产构建产物 **0 个** `window.__pr`

---

## 9. 边界与未做的事

- **本票只产出契约清单**，未改任何源码（实现属 #18 / #19）。
- **未运行验收脚本**：§2 的"两级兜底"结论来自代码阅读（`[实测]` 指"读到了那段代码"），**未真机跑过删除后的 `verify-m2`**——该验证属骨架票。
- **未核对约 190 个 testid 的完整名单**：§6 的抽样来自各文件扫描；丢弃是"全丢"，所以不需要逐条列举。
- **未处理**：`verify-ice.mjs` 因「离开房间」改图标而变红后的替代验证方式（留待下轮或实现票决定）。
