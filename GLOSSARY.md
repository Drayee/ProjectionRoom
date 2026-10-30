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
