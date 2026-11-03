# Glossary

> 术语表：产品与实现里**有歧义、或曾经被误解**的词在这里定死。
> 每条都注明锁定它的 GitHub Issue 与日期，改口径前先看那条决议。
> 本文件被 `client/src/**` 与 `internal/**` 的注释按名字引用（`GLOSSARY「Social graph」`），
> 移动或改名会让那些引用失效。

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
- **Endpoints** (removed in #59, now 404 with a Chinese error — never a silent 200):
  `GET /api/friends`, `GET|POST /api/friends/requests`,
  `POST /api/friends/requests/:id/accept|reject`, `DELETE /api/friends/:id`.
  The friend list has **no replacement endpoint**: it is `GET /api/follows?tab=mutual`.
- **WS frames**: `friend-request` / `friend-accepted` have no producer any more
  (the `FriendEvent` frame encoding is kept to avoid a protobuf change).
- `friendships` still exists, but only `status = 'blocked'` is live. Blocking is now a
  transaction of **two** steps: write/override the blocked row **and** delete the
  blocker's own follow edge (`A→B`), keeping the target's edge (`B→A`).

### Related

- Parent map: [自然体验：过渡反馈、对象组件、档案双视角与社交轻互动](https://github.com/Drayee/ProjectionRoom/issues/40)
- Capability inventory: [notes/2026-10-28-natural-ux-capability-inventory.md](notes/2026-10-28-natural-ux-capability-inventory.md) (pre-decision facts)


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

Terms locked by [共享对象组件契约](https://github.com/Drayee/ProjectionRoom/issues/44) (2026-10-30); pixel-avatar overlay rules by [真头像上传](https://github.com/Drayee/ProjectionRoom/issues/54) (2026-10-30).

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

详见 [notes/2026-10-30-natural-layer-visual-principles.md](notes/2026-10-30-natural-layer-visual-principles.md)。

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
| **Zero-wait fallback** | 零等待降级 | 共享元素任一条件不满足（元素缺失/图未解码/rect 为 0）→ 当场退化为 240ms 位移，**不许等待**。判定是纯函数（`utils/sharedElement.ts` 的 `planSharedMorph`），只看已经量好的数，**刻意不等**图片解码、不等组件挂载。 |
| **Kind-keyed matching** | 按契约匹配 | 起飞前扫**离开页**里全部 `data-shared`，按 `kind:key` 建表；落地后按同一串在**进入页**里找。**不写死页对页** —— 写死就等于「新增一条共享必须同时改匹配表」，按契约发现更准（同一个 `avatar:7` 能从任意带它的列表飞向任意带它的档案）。只按**第一个**冒号切，key 里允许再有冒号。 |
| **Ghost element** | 幽灵元素 | 飞的是源节点的**克隆**（挂 `<body>`、`position:fixed`、`transform-origin:top left`、`z-index:70`、去掉 `id`/`data-testid`/`data-shared`/`tabindex`、`aria-hidden`），真元素只被 `visibility:hidden` 藏住、飞完还原。挂 `<body>` 是因为换页时新旧两页**同在**，挂在页里的动画会被祖先的 transform/opacity 一起带走。**不共享组件实例**：列表卡封面与详情头图尺寸差数倍，实例化共享会撕。**外壳里的常驻元素不算源**（顶栏头像换页时不被新实例取代，飞起来只是原地抖动）。 |
| **data-driven current state** | 数据驱动当前态 | 导航当前项由 `utils/navMatch.ts` 的前缀表判定（**不用** `router-link-active` 默认类），外观按 #43 的静态 pill，必须带 `aria-current="page"`。 |
| **Transition-exempt** | 过渡豁免 | `/room/:roomId` 整页禁用过渡（WebRTC/播放器怕 DOM 抖动）；首屏、reduced-motion、同路由仅 query 变化同样不做过渡。 |
| **Lazy routes** | 路由懒加载 | 19 个页面全改 `() => import()`；首包只含外壳 + 落地页；chunk 加载用 AsyncState 骨架；导航项 `pointerenter` 预取。 |

### Rules

- **过渡不等数据**：点击即开始 240ms，新页先骨架；数据慢**不许**延长或重播过渡（否则慢网下就是「点了没反应」）。
- 每路由 `document.title = "页面名 · 月喵"`，单点设置；换页后焦点进主标题容器 + `aria-live` 播报一次。
- 过渡只动 `transform`/`opacity`；计入 #43 的「一次性动画同屏 ≤3」。
- `prefers-reduced-motion` 下换页瞬时：CSS 由 tokens 全局判死，**FLIP 走 WAAPI 必须自己判**（双侧判死，缺一侧会出现「CSS 停了 JS 还在动」）。

详见 [notes/2026-10-30-route-transition-navigation-principles.md](notes/2026-10-30-route-transition-navigation-principles.md)。


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


## Account settings（账号设置与注销）

Terms locked by [账号设置：并入档案还是保持独立流](https://github.com/Drayee/ProjectionRoom/issues/51) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Settings partition** | 设置区 | `/me?tab=settings` 深链分区；五块：改密码 / 退出所有设备 / 邮箱 / 账号只读信息 / 危险区。 |
| **Two doors** | 两条门 | 记得密码→**仅当前密码**（`PUT /api/me/password`）；忘了密码→邮箱验证码（现有 `password/otp*` 不动）。登录态门刻意不含验证码——已接受的取舍（找回密码仍靠邮箱码兜底）。 |
| **Email rebind** | 换绑邮箱 | 发码到**新**地址→确认→置 `email_verified_at`；与管理员代办 PATCH email **并存不同路**。旧邮箱换绑后立即失效。 |
| **Deletion cooling** | 注销冷静期 | 软删标志 + **30 天**：即时不可对外、登录可撤销（/me 横幅）；到期硬删走后台清扫（I4），CASCADE 带走全部内容。 |
| **Deleted user display** | 已注销用户 | 冷静期起对外即隐藏：`/user/:id` 404、搜索/关注/粉丝/互关消失、帖子作者显示「已注销用户」；**私聊历史保留**给另一方。 |

### Not doing

- 改用户名（公开且被搜索引用）；注销立即硬删选项。
- ~~头像上传（地图 Out of scope）~~ —— **已落地且全部验收**：票 #74 覆盖层（`verify-avatar-overlay.mjs` 15 条）、票 #75 裁剪与额度（`verify-avatar-crop.mjs` C 组 11 条 + S 组 14 条）。见上节「Real avatar」与 `docs/ACCOUNTS.md` §7.9 / §7.10。


## Following feed（关注流）

Terms locked by [关注信息流形态：优先推送的消费面](https://github.com/Drayee/ProjectionRoom/issues/52) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Following pill** | 「关注」pill | `/posts?sort=following`：/posts 的第三个排序 pill（最新/热门/关注）。严格时间倒序，**仅我关注的人**的全部帖（普通帖+板块主题串）。不是独立路由、不是顶栏分区。 |
| **Feed predicate** | 流谓词 | `author ∈ 我 following ∧ ¬blocked_either ∧ 未注销 ∧ 未封禁`；store 层**共用实现**（关注流 / #47 他人帖列表 / #51 注销隐藏同一真源），前端零判断。 |
| **Guest feed CTA** | 游客流引导 | pill 对游客可见可点；进入后列表区=分区内登录引导（AsyncState empty 形），`returnTo=/posts?sort=following`，不弹走页面。 |
| **Zero-follow empty** | 零关注空态 | 「还没有关注任何人」+ 深链 `/friends?focus=search`（#50 页头搜索）。不设新鲜度门槛：关注对象只有老帖也照显示。 |
| **Posts URL contract** | 帖子页 URL 契约 | 三 pill 统一 `?sort=latest\|hot\|following` 进 URL；`utils/postsRoute.ts` 键源。分享 following 给他人=对方的关注流，语义如此。 |


## Follow notifications（关注通知）

Terms locked by [关注与回关的通知渠道与已读语义](https://github.com/Drayee/ProjectionRoom/issues/53) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Follow badge** | 粉丝红点 | 两处同数：/friends 粉丝 tab + 顶栏「好友」分区角标。计数 = 新粉丝里**我未回关**且 `followed_at > followers_seen_at` 的人数。**派生值，零新表**（users 新列 `followers_seen_at`）。 |
| **Second unread** | 第二条未读 | 与消息 `unread`（setUnread 唯一写入点，#30）**完全独立**：两数、两源、两清除点，永不互相覆盖。 |
| **Seen on open** | 进页即清 | 打开 /friends?tab=followers 即写 `PUT /api/me/followers-seen`（幂等；异步单写守 I4）。不做逐条已读。 |
| **Follow events** | 关注事件 | 只两类产生通知：**新粉丝**（follow-created）与**互关**（mutual-follow，文案「互关成为好友」）。**取关/被取关静默**。在线经 FriendPusher→底部 3~5s 自消失 toast；toast 不改变红点生命周期。 |
| **Mute predicate** | 豁免谓词 | 拉黑存续期双向静默；已注销/封禁不发。与 #52 流谓词同一个 store 层共用实现（`NOT blocked_either ∧ 未注销未封禁`）。 |
| **Followers endpoint** | 粉丝端点 | `GET /api/followers → {items, new_count}`：列表与红点一端两用；需登录。 |


## Real avatar（真头像）

Terms locked by [真头像上传：存储、审核与字母头兜底](https://github.com/Drayee/ProjectionRoom/issues/54) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Avatar overlay** | 覆盖层 | `avatarUrl` 是 UserAvatar 的可选覆盖：空 / onerror（该会话内不重试）/ 被管理端重置 —— 三态全回落 `avatarOf()` 字母头。字母头永存。 |
| **Browser crop** | 浏览器裁剪 | 票 #75 落地：canvas 裁方形 **512×512**、≤1 MiB、JPEG/WebP（GIF 保留第一帧）。取景数学在 `utils/avatarCrop.ts`（纯函数，**画布与导出共用同一组输入**，否则"看到的和传上去的不一样"），压不下就报错而不是硬传。服务端零图像依赖、不重编码；非方图由 `object-fit` 兜（与 uploads 现安全论证同族）。 |
| **Moderation four-pack** | 审核四件套 | 人机验证子窗体（裁剪确认之后、上传之前）+ **每人日 5 次**变更频控（记在 PATCH /api/me 这一层，幂等重复与清除**不扣**）+ 管理端 `reset_avatar`（**先置空→再异步删对象**，删失败只记日志：处置的目的是"没人看得见"，不是"桶里没有"）+ 人工处置。无内容识别、无第三方。 |
| **URL-as-version** | URL 即版本 | 每次上传新随机 key，PATCH 替换 `users.avatar_url`；长缓存安全；旧对象异步延迟删除（不在请求路径，I4 同族）。 |
| **Single entry** | 唯一入口 | 改头像只在 `/me?tab=settings`。头像位不加编辑态——#44 定「头像=导航」，避免进主页/改图二义。 |
| **Zero new endpoint** | 零新端点 | 复用 `POST /api/uploads` 拿 URL + 扩 `PATCH /api/me {avatarUrl}`（校验属本桶 key 形态，**外链注入 400**；null=清除）。票 #75 也**没有**为频控新开端点：额度就挂在同一个 PATCH 上，超限 **429 `RATE_LIMITED`**（不是 403 —— "今天用完了"不是"没权限"）。 |

### Doc debt

~~`docs/ACCOUNTS.md` 的非目标「头像/图片上传」一句随实现票改写（决策已翻转）。~~ 已在票 #74 / #75 改完（§7.9 覆盖层、§7.10 裁剪与额度）。
