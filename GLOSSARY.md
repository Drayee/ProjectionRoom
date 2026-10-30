# Glossary

## Social graph（自然体验 map 决议）

Terms locked by [锁定关注与好友关系模型（互关=好友）](https://github.com/Drayee/ProjectionRoom/issues/42) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Follow** | 关注 | One-way edge A→B. No consent required. Stored in `follows`. |
| **Follower** | 粉丝 | Someone who follows me (reverse list of follow edges). |
| **Mutual follow** | 互关 | Both A→B and B→A exist. |
| **Friend** | 好友 | Product synonym for **mutual follow**. Not a separate request workflow. |
| **Block** | 拉黑 | DM kill-switch between two users; while active neither may create **new** follows to the other; blocker's follow to target is removed; target's follow to blocker may remain; public posts/profile still visible. |
| **can_dm** | 可私聊 | `mutual_follow(A,B) AND NOT blocked_either(A,B)`. Server is source of truth (I3). |
| **Friend request** | 好友请求 | **Retired.** No `pending`→accept path for friendship. |

### Retired / do not use for friendship

- `friendships.status = pending | accepted` as friend source of truth.
- UI: 待处理请求、加好友请求、删除好友（use 取关 / 拉黑 only).

### Related

- Parent map: [自然体验：过渡反馈、对象组件、档案双视角与社交轻互动](https://github.com/Drayee/ProjectionRoom/issues/40)
- Capability inventory: [docs/notes/2026-10-28-natural-ux-capability-inventory.md](docs/notes/2026-10-28-natural-ux-capability-inventory.md) (pre-decision facts)


## Profile（档案）

Terms locked by [他人与本人档案信息架构](https://github.com/Drayee/ProjectionRoom/issues/47) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Profile page** | 他人档案 | `/user/:id` — 公开名片 + 关系动作 + 该人公开帖与公开放映室。**不含**管理动作。 |
| **Console** | 管理台 | `/me` — 本人资料编辑、我的帖（含删除）、我的放映室（含未公开/已关闭）、好友/关注/粉丝三列表、账号设置入口。 |
| **Public view of me** | 我的公开主页 | 本人看 `/user/<自己id>` 的纯公开视角；与 Console 是两个页面，不是同一页的两种状态。 |
| **Relationship actions** | 关系动作 | 关注 / 取关 / 回关 / 发消息（仅互关）/ 拉黑。好友不可「删除」，只能取关（见 Social graph）。 |

### Visibility (profile content)

- **Posts**: 普通公开帖，倒序无限滚动；无草稿概念。
- **Rooms**: 同 `GET /api/public-rooms` —— 已公开且未关闭。
- **Counts**: 关注数 / 粉丝数为纯数字，**不可点开名单**；名单只在 Console 看自己的。
- **Favorites**: **他人档案不渲染收藏块** —— 收藏仅自己可见，决定见收藏域票（#48）。


## Favorites（收藏）

Terms locked by [收藏域：做不做与产品形状](https://github.com/Drayee/ProjectionRoom/issues/48) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Favorite** | 收藏 | 私人书签，**不计任何公开数**；与点赞（公开表态、计 `like_count`）语义不同。 |
| **Favorite target** | 收藏对象 | `post` / `room` / `anime`。`anime` 是**番剧目录项本身**（追番），不是板块里的主题串——主题串本身就是 `post`，由帖子收藏覆盖。 |
| **Stale favorite** | 失效收藏 | 对象已关闭/下架/删除的收藏条目：**保留**、灰显「已失效」、不可点进，由用户自行移除。不静默删除。 |
| **Favorite list** | 收藏列表 | 仅本人可见，落 `/me` 内独立区块，类型 tab（帖 / 房 / 番剧），每类按**收藏时间倒序**分页。 |

### Boundaries

- **不做**：公开收藏夹、他人可见的收藏、收藏数公开计数、收藏夹分类/标签/备注/导出。
- 存储为单表 `favorites(user_id, target_type, target_id, created_at)`，主键三元组保证幂等；跨表完整性由服务层校验（对象不存在 → 404，不写孤儿行）。


## Shared object components（共享对象组件）

Terms locked by [共享对象组件契约](https://github.com/Drayee/ProjectionRoom/issues/44) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **UserAvatar** | 头像 | 首字母 + `utils/avatar.ts` 五色板的圆形块；**不上传图片**。默认可点跳档案（本人 → `/me`）。嵌套链接场景传 `interactive=false`。 |
| **RelativeTime** | 相对时间 | 六档：刚刚 / N 分钟前 / N 小时前 / N 天前 / 同年 M月D日 / 跨年 YYYY年M月D日。全站**单一 30s tick**，`title` 给绝对时间。超 7 天回绝对日期。 |
| **Button / IconButton** | 按钮 | 五态：hover 提亮、active 下沉 1px、focus-visible 外环、disabled 降饱和、**loading 内联 spinner 且锁宽**。新代码禁止裸 `<button>`。 |
| **AuthorRow** | 作者行 | UserAvatar + displayName + @username + 角色标签 + RelativeTime；`size` = compact / detail / comment。私信气泡头不复用。 |
| **AsyncState** | 三态门面 | `loading`(骨架屏) / `empty`(图标+一句说明+可选行动) / `error`(人话+重试)。**禁止裸「暂无数据」**。 |

### Single-source boundaries

头像派生（`utils/avatar.ts`）、相对时间（RelativeTime + `useNowTick`）、按钮反馈（`ui/Button`）、三态（`AsyncState`）、作者行（`AuthorRow`）**各只有一个源**；页面里再写私有 `avatarOf` / `shortTime` / 裸 spinner 块视为回归。

### Layering

`ui/` = 纯展示（不 import store / router，props 进事件出）；壳与业务组合在 `features/`。顶栏旧的 `hsl(hash)` 取色已废除，统一走 `avatarOf()`。


## Post interaction（帖子互动）

Terms locked by [帖子互动面：列表赞、收藏入口与一致性](https://github.com/Drayee/ProjectionRoom/issues/49) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Post card** | 帖子卡 | 链接区（封面/标题/摘要）+ **动作条**（赞/收藏/评论数）两部分；按钮**不在** `<a>` 内。视觉仍是一整块。 |
| **Action bar** | 动作条 | 顺序固定 **赞 → 收藏 → 评论**；列表、详情、论坛主题串用法相同，只在详情页展开评论。 |
| **Count formatting** | 计数缩写 | `0` 隐藏数字只留图标；`≥1000` → `1.2k`；`≥10000` → `1.2w`。保留一位小数、去掉 `.0`。 |
| **Single-row lock** | 单条锁 | 请求在途时只锁该条按钮（loading 态 + 忽略点击），不锁整页、不做防抖。 |
| **Guest write CTA** | 游客写动作 | 未登录点赞/收藏 → **跳登录并带 `returnTo`**；不灰按钮、不弹提示框；计数照常可见。 |

### Rules

- 赞与收藏**都**乐观更新：先改本地、再发请求、失败回滚 + 一次轻提示。
- **评论点赞本期不做**（Out of scope，见地图）。
- 论坛主题串复用同一行组件与同一套动作，不另做样式。


## Natural layer（自然层）

Terms locked by [月夜故障 DNA 上的自然层视觉原则](https://github.com/Drayee/ProjectionRoom/issues/43) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Decoration vs feedback** | 装饰 / 确认 | 故障效果（撕裂、阶梯抖动、扫描线）是**装饰**，可以不安；交互反馈是**确认**，必须确定（位移 + 亮度 + 静态双色分离）。两者不许互相借。 |
| **Three-piece feedback** | 三件套 | 任何可交互面 hover 时必须**同时**：底色 → `--state-hover`、前景 `--dim`→`--text`、卡片 `translateY(-2px)`。描边只作辅助（`--line` 仅 1.45:1，不得单独表达状态）。 |
| **State tokens** | 状态令牌 | `--state-hover` / `--state-active` / `--state-veil` / `--ring` / `--elev-1|2` / `--shift-card` / `--press-btn` / `--press-icon`；组件里禁写 `rgba(180,108,255,.08)` 类手写状态色。 |
| **Size-graded press** | 分级按压 | 卡片 hover −2px（不压）、按钮 `scale(.98)`、图标按钮 `scale(.94)`；缩放幅度与尺寸反比。 |
| **Current-state pill** | 当前态药丸 | 导航当前项 = 静态 `--state-active` 底 + 图标升 `--text` + 1px 右红左紫分离 + `aria-current="page"`；**零新增常驻动画，无滑动**。 |
| **Hit target** | 命中区 | 最小 44×44（视觉 36px 用伪元素外扩）；焦点环全站统一 `--ring` + `--ring-offset`。 |
| **Content-page austerity** | 内容页素壳 | 内容页（帖子/档案/论坛/消息/好友/设置）只有顶栏 1 条故障线；噪点与扫描线只许在落地页与房间等待页。 |

### Rules

- **一次性动画同屏 ≤3**（常驻循环仍守 #13 的 ≤5），只许 `transform`/`opacity`；不许把新效果加到长列表的每一行。
- **reduced-motion 下反馈必须仍在**：三件套靠属性值切换，不依赖 `animation` 到达终态（靠 keyframes `to` 决定颜色的写法在降级下完全不反馈，属回归）。
- `--dim` 是 4.5:1 下限，**不得再调暗**；色板与故障三色本票不动。

详见 [docs/notes/2026-10-30-natural-layer-visual-principles.md](docs/notes/2026-10-30-natural-layer-visual-principles.md)。

### Open（交给 #46 / to-spec）

- 导航切换是否采用**滑动**指示器（本票只定「不滑动」为默认；若 #46 证明滑动更自然，必须 reduced-motion 下瞬时）。


## Route transition & navigation state（路由过渡与导航当前态）

Terms locked by [路由叙事过渡与导航当前态原则](https://github.com/Drayee/ProjectionRoom/issues/45) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Directional shift** | 方向性位移 | 换页过渡 = 12px 位移 + 淡入，240ms；**不是**整屏横移。顶栏常驻，整屏推会错位。 |
| **Depth** | 层级 | 路由 `meta.depth`（0 落地 / 1 列表 / 2 详情）。方向由 `to.depth - from.depth` 决定，不用 `router.back()` 推断（URL 上不可区分）。 |
| **Lateral switch** | 同级切换 | 同 depth 切分区 → 纯交叉淡入，**无位移**（分区之间没有父子关系，硬造方向会误导）。 |
| **Shared element** | 共享元素 | 三处：帖子卡封面→详情、头像→档案、房间卡→房间；契约是两端 `data-shared="<kind>:<key>"`，用 FLIP 440ms。**只在前进方向触发**，后退一律方向性位移。 |
| **Zero-wait fallback** | 零等待降级 | 共享元素任一条件不满足（元素缺失/图未解码/rect 为 0）→ 当场退化为 240ms 位移，**不许等待**。 |
| **data-driven current state** | 数据驱动当前态 | 导航当前项由 `utils/navMatch.ts` 的前缀表判定（**不用** `router-link-active` 默认类），外观按 #43 的静态 pill，必须带 `aria-current="page"`。 |
| **Transition-exempt** | 过渡豁免 | `/room/:roomId` 整页禁用过渡（WebRTC/播放器怕 DOM 抖动）；首屏、reduced-motion、同路由仅 query 变化同样不做过渡。 |
| **Lazy routes** | 路由懒加载 | 19 个页面全改 `() => import()`；首包只含外壳 + 落地页；chunk 加载用 AsyncState 骨架；导航项 `pointerenter` 预取。 |

### Rules

- **过渡不等数据**：点击即开始 240ms，新页先骨架；数据慢**不许**延长或重播过渡（否则慢网下就是「点了没反应」）。
- 每路由 `document.title = "页面名 · 月喵"`，单点设置；换页后焦点进主标题容器 + `aria-live` 播报一次。
- 过渡只动 `transform`/`opacity`；计入 #43 的「一次性动画同屏 ≤3」。
- `prefers-reduced-motion` 下换页瞬时：CSS 由 tokens 全局判死，**FLIP 走 WAAPI 必须自己判**（双侧判死，缺一侧会出现「CSS 停了 JS 还在动」）。

详见 [docs/notes/2026-10-30-route-transition-navigation-principles.md](docs/notes/2026-10-30-route-transition-navigation-principles.md)。


## Friend discovery（好友搜索可发现性）

Terms locked by [好友搜索可发现性](https://github.com/Drayee/ProjectionRoom/issues/50) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Header search** | 页头搜索 | `/friends` 页头常驻搜索框；输入即出结果面板盖住列表区。**搜索是一种状态，不是一个去处**（原「找朋友」tab 取消）。 |
| **Friends URL contract** | 好友页 URL 契约 | `/friends?tab=mutual|following|followers|blocked&q=<词>`；tab 与词都进 URL，可分享、刷新不丢、可深链；`?q=` 存在则进页自动搜一次。键名唯一源 `utils/friendsRoute.ts`（与 `dmRoute` 同族）。 |
| **Discovery deep link** | 找人深链 | 全站「找人」诉求一律指 `/friends?tab=find→(页头搜索)&q=`；**不做**顶栏全局搜索/命令面板。 |
| **Debounced search** | 防抖搜索 | 300ms 防抖、≥2 字符发请求、清空回列表；保留 Enter/按钮提交给键盘。 |
| **Result relation button** | 结果关系按钮 | 按 #42 关系态：关注 / 已关注(可取消) / **回关**(点完即互关=好友) / 互关→发消息；「…」=查看档案/拉黑；拉黑存续期不给关注按钮。 |
| **Guided empty vs not-found** | 引导态 vs 建议态 | 未搜=「输入 TA 的用户名或昵称」引导；搜了 0=「没找到「xxx」…」+旁路深链。把还没搜说成搜不到是误导。 |

### Rules

- 搜索**维持需登录**（防匿名枚举用户名，与 #33 防存在性探测同战线）；游客见登录墙 + 禁用态搜索框示意。
- 本票零新增组件：消费 AuthorRow/UserAvatar/Button/AsyncState（#44）。
