# 路由叙事过渡与导航当前态原则

> 票：[路由叙事过渡与导航当前态原则](https://github.com/Drayee/ProjectionRoom/issues/45)
> 地图：[自然体验：过渡反馈、对象组件、档案双视角与社交轻互动](https://github.com/Drayee/ProjectionRoom/issues/40)
> 日期：2026-10-30
> 上游：[自然层视觉原则](https://github.com/Drayee/ProjectionRoom/issues/43)（时长三档与状态令牌）、#13 动效预算
> 下游：[原型：路由叙事过渡与导航当前态](https://github.com/Drayee/ProjectionRoom/issues/46)、/to-spec

---

## 0. 现状判据

| 事实 | 出处 |
| :--- | :--- |
| `<router-view>` **没有任何 Transition**，页面硬替换 | `client/src/App.vue:59-61` |
| 顶栏已提到路由之外常驻（换页不重建） | `client/src/App.vue:58` —— 这是过渡可行的前提 |
| 19 个页面**全部同步 import**，全部进首包 | `client/src/router.ts:3-21` |
| `active`/当前态样式 **0 处** | `grep "router-link-active" client/src` |
| 无任何 `document.title` 设置 | 19 个路由共用 Vite 默认标题 |
| `scrollBehavior` 已有：back 恢复、hash 滚动、其余置顶 | `client/src/router.ts:122-128` |

## 1. 过渡形态：方向性位移 + 淡入（不是整屏推）

| 参数 | 值 |
| :--- | :--- |
| 位移 | **12px**（不是百分比、不是全屏宽） |
| 时长 | 新页 **--dur-comp 240ms**；旧页同 240ms |
| 缓动 | --ease |
| 属性 | 只动 transform + opacity（#13 硬约束） |
| 首屏 | **不做**过渡（首次进入直接呈现） |

**为什么不是全屏横移**：顶栏常驻且换页时不动，整屏横推会出现「导航栏不动、内容整体平移」的错位感；宽屏上位移量还会大到失控。12px 是「方向提示」而不是「页面搬家」。

## 2. 方向判定：按层级，不按历史

不能用 `router.back()` 推断（pushState 与 back() 在 URL 上不可区分）。改为**路由层级**：每个路由显式声明 `meta.depth`（缺省 1）。

| 层级 | 路由 |
| :--- | :--- |
| 0 | `/` |
| 1 | `/posts` `/forum` `/friends` `/messages` `/rooms` `/official` `/help` `/admin` `/admin/*` |
| 2 | `/posts/:id` `/forum/:animeId` `/user/:id` `/me` `/posts/new` |

| 关系 | 过渡 |
| :--- | :--- |
| `to.depth > from.depth`（进详情） | **前进**：新页从 translateX(12px) 归零，旧页向左 -12px 淡出 |
| `to.depth < from.depth`（回列表） | **后退**：反向 |
| `to.depth === from.depth`（同级切分区） | **横向无位移**：纯交叉淡入 240ms |
| 同路由仅 query 变化（`/messages?peer=`、`?offset=`） | **不触发**过渡（同一屏状态变化，不是换页） |

**已知取舍**：同级跳转（如 /posts → /forum）看起来「没方向」，这是**刻意**的——分区之间没有父子关系，硬造方向会误导。

## 3. 共享元素（3 处）

| 场景 | 契约 | 起点 | 终点 |
| :--- | :--- | :--- | :--- |
| 帖子卡 → 详情 | `data-shared="post-cover:<id>"` | `features/posts/PostCard.vue` 封面 | `PostDetailPage.vue` 头图 |
| 头像 → 档案 | `data-shared="avatar:<userId>"` | `ui/UserAvatar.vue`（#44） | `/user/:id` 与 `/me` 名片区头像 |
| 房间卡 → 房间 | `data-shared="room:<roomId>"` | `features/rooms/RoomCard.vue` | `RoomPage.vue` 标题区 |

**规则**：

1. **只有前进方向触发**共享元素。后退一律用 §2 的方向性位移——列表页回来时正在重新拉数据，目标元素经常还不存在，硬做只会降级成闪一下。
2. **失败即降级，零等待**：任一端找不到元素、图片未解码、或 rect 尺寸为 0 → 当场退化为 240ms 方向性位移，**不许等待**（过渡永不因为共享元素而卡住）。
3. 共享元素 morph 用 **--dur-morph 440ms**，与页面 240ms 位移**同时开始**：元素「飞过去」比页面更显眼，这是叙事重点。
4. 两端只暴露 `data-shared` 属性 + 一致的圆角/裁切，**不共享组件实例**（列表卡封面与详情头图尺寸差异大，实例化共享会撕）。
5. reduced-motion 下共享元素判死（退化为瞬时切换）。

## 4. 导航当前态

### 4.1 匹配表（前缀匹配，单一源 `utils/navMatch.ts`）

| 分区 | 命中前缀 | aria-current="page" |
| :--- | :--- | :--- |
| 房间 | `/room` `/rooms` `/official` | 是 |
| 帖子 | `/posts`（含 /posts/new、/posts/:id） | 是 |
| 番剧论坛 | `/forum`（含 /forum/:animeId） | 是 |
| 好友 | `/friends` | 是 |
| 消息 | `/messages` | 是 |
| 帮助 | `/help` | 是 |
| 管理端 | `/admin` | 是 |
| 头像位 | `/user/:id` `/me` | **头像位自身**显示当前态（其余分区全灭） |
| 落地页 `/` | 无 | 不点亮任何项 |

### 4.2 外观（由 #43 定，此处只落规则）

- 当前项：静态 `--state-active` 底 + 图标升 `--text` + 1px 右红左紫分离 + `aria-current="page"`。
- **不用** `router-link-active` 默认类：它按路径前缀自动加，会把「/posts 之外的同前缀路由」判错；统一走 `utils/navMatch.ts` 的显式判断。
- 切换时**无滑动**（#43 已定），只有颜色与分离的 160ms 过渡。

## 5. 时序：过渡不等数据

| 阶段 | 行为 |
| :--- | :--- |
| 点击 | 立刻开始 240ms 过渡 |
| 过渡中 | 新页已挂载，渲染 `AsyncState` 骨架（#44） |
| 数据到达 | 填内容；**不许**因为数据慢而延长或重播过渡 |
| 过渡结束 | 焦点移到页面主标题（§6） |

**反面教材（本票要防的）**：等数据再过渡 —— 慢网下点击后画面静止，正是用户抱怨的「点了没反应」。

## 6. 可访问性与文档标题

| 项 | 规则 |
| :--- | :--- |
| document.title | `「<页面名> · 月喵」`，单点设置（router.afterEach + meta.title），页面不许自己写 |
| 焦点 | 过渡结束后聚焦该页主标题容器（h1 with tabindex=-1），不滚动页面 |
| 播报 | 一个全局 `aria-live="polite"` 区域，换页时写入页面名（一次，不重复） |
| 键盘 | 换页不影响 Tab 顺序；顶栏链接必须是首个可达 |

## 7. 路由懒加载

19 个页面**全部**改 `() => import(...)`：

- 首包只含外壳 + 落地页（落地页是全屏 hero，必须首屏即到）。
- chunk 加载期间由 `AsyncState` 骨架承担（与 §5 同一套，不新增第三套加载样式）。
- **预热**：顶栏导航项 `pointerenter` 时预取对应 chunk（一次，失败静默）；命中缓存后切换无白屏。
- 管理端三屏也在内（管理员才加载，普通用户永不下载）。

## 8. 禁用面（默认全站开，只列例外）

| 例外 | 理由 |
| :--- | :--- |
| `/room/:roomId` **整页**（含进出房） | 房间有 WebRTC 与播放器，DOM 抖动会打断媒体管线；不做「播放中才禁」的细分（要多一条页面→外壳的状态通道） |
| 首屏首次进入 | 没有「上一页」，过渡无意义 |
| prefers-reduced-motion: reduce | 全局判死（tokens.css 已覆盖 CSS；FLIP 走 WAAPI，必须自己判，见 #13 §4.2） |
| 同路由仅 query 变化 | 不是换页 |

## 9. 不做

- 整屏横向滑移、手势返回式交互
- Suspense 级「旧页保留到新页 ready」
- 页面内二级 tab 的当前态（#43 已定外观，二级 tab 的接法另票）
- 骨架屏形状规范（归 #44 AsyncState 的 slot 约定）

## 10. 验收判据（交给 #46 与 /to-spec）

1. 前进/后退方向正确；同级切分区无位移。
2. 房间页进出**零过渡**。
3. 共享元素三处都成立；把目标元素删掉（模拟未渲染）时必须**立刻**降级为位移，且整体时长不变。
4. prefers-reduced-motion 下换页为瞬时（animation 与 WAAPI 双侧都判）。
5. 顶栏当前项：进 /posts/3 时「帖子」仍点亮且 aria-current="page"。
6. 每路由标题正确；换页后读屏播报一次。
7. 首包不含管理端 chunk。
