# 自然层视觉原则（月夜故障 DNA 之上）

> 票：[月夜故障 DNA 上的自然层视觉原则](https://github.com/Drayee/ProjectionRoom/issues/43)
> 地图：[自然体验：过渡反馈、对象组件、档案双视角与社交轻互动](https://github.com/Drayee/ProjectionRoom/issues/40)
> 日期：2026-10-30
> 上游依据：`client/src/styles/tokens.css`（三色故障 + 三档时长）、`docs/reports/2026-10-14-glitch-motion-spec.md`（#13 的 15 个效果与预算）
> 下游消费：[原型：路由叙事过渡与导航当前态](https://github.com/Drayee/ProjectionRoom/issues/46) 与 /to-spec

---

## 0. 一句话

**故障是「装饰」，反馈是「确认」。** 两者用两套语言，不许互相借：装饰可以不安（撕裂、阶梯抖动、扫描线），
确认必须确定（位移、亮度、静态双色分离）。用户抱怨的「点了没反应、放上面没反应」属于确认缺失，
用更花的故障去补只会更糟。

## 1. 现状判据（本票的事实底座）

| 事实 | 数值 | 出处 |
| :--- | :--- | :--- |
| 裸 `<button>` | **111 处** | `grep "<button" client/src --include=*.vue` |
| `:hover` 规则 | 66 处，分散在 26 个文件 | 深浅不一，多为 `rgba(180,108,255,.08)` 手写 |
| `active`/当前态 | **全仓 0 处** | `grep "router-link-active\|router-link-exact-active" client/src` |
| `transition` 声明 | 29 处 | 无统一 token |
| 降低了动效的组件 | 只有 `TopologyBadge.vue` + tokens 全局判死 | `prefers-reduced-motion` 3 处 |
| 常驻循环动画 | 落地页 3 / 登录窗 1 / 外壳 5（上限）/ 帖子面 1 | #13 §2.1 |
| 描边对比度 | `--line` = **1.45:1**，不达 WCAG AA | tokens.css 注释（票 #13 硬约束 2） |

**两个硬推论**：

1. 状态**不得**只靠描边颜色表达（1.45:1 不过 AA）——这条是 #13 钉死的，本票的 hover 方案必须遵守。
2. 「导航看不出当前页」不是配色问题而是**根本没有 active 样式**：需要先有，才谈好不好看。

## 2. 交互反馈的唯一配方（三件套）

任何可交互面（按钮 / 图标按钮 / 卡片 / 列表行 / 导航项）在 `hover` 时**同时**发生三件事，缺一即视为未实现：

| 件 | 属性 | 值 |
| :--- | :--- | :--- |
| ① 底 | `background-color` | `--state-hover`（见 §3） |
| ② 前景 | `color` | `--dim` → `--text`（原本已是 `--text` 的则不变） |
| ③ 位移 | `transform` | 卡片 `translateY(-2px)`；行内按钮 `translateX(1px)`；图标按钮不动 |

按压（`:active`）：底色换 `--state-active` + `transform: scale(...)` + **阴影收起**（若该元素有 hover 阴影）。

**描边只作辅助**：允许 `border-color` 从 `--line` 提到 `--line-strong`，但它**永远不是唯一变化**。

## 3. 新增 token（`--state-*` 一组，唯一状态色出口）

`--surface` 是 `#150e22`。两档提亮按「叠加固定比例白」算，**写入为绝对值**（避免 `color-mix` 让对比度随主题漂移）：

```css
/* 状态层：surface + 4.5% / 7.5% 白（换算后绝对值；换肤时按键值重算，不要改成 color-mix） */
--state-hover:    #20192c;   /* surface + 4.5% white */
--state-active:   #272033;   /* surface + 7.5% white */
/* 无 surface 底（--bg 之上）的裸链接、裸图标用面纱，避免再发明第三种底色 */
--state-veil:     rgba(255, 255, 255, 0.06);
/* 阴影：只有两级，不许页面自己写 */
--elev-1:         0 2px 8px rgba(0, 0, 0, 0.35);
--elev-2:         0 10px 28px rgba(0, 0, 0, 0.45);
/* 焦点环（把现有 :focus-visible 的两行字变成 token） */
--ring:           2px solid var(--accent);
--ring-offset:    2px;
/* 位移与按压：按尺寸分级，见 §4 */
--shift-card:     -2px;
--press-btn:      0.98;
--press-icon:     0.94;
```

**禁令**：组件里不许再出现 `rgba(180,108,255,.08)` 这类手写 hover 底，也不许用 `opacity` 伪造 disabled 以外的状态。

## 4. 位移与按压：按尺寸分级

| 目标 | hover | active |
| :--- | :--- | :--- |
| 卡片 / 列表行（≥120px） | `translateY(var(--shift-card))` | `scale(1)`（卡片不压） |
| 普通按钮（常规高度 32–40px） | 三件套，无位移 | `scale(var(--press-btn))` |
| 图标按钮（36px 及以下） | 三件套，无位移 | `scale(var(--press-icon))` |

理由：同样的 `scale(.94)` 在 36px 上是 2px、在 200px 卡片上是 12px，**视觉重量必须按尺寸反比**。
（顶栏现在写死 `.92`，与原型 `.97/.98` 不一致，统一时按上表。）

## 5. 导航当前态（外观；逻辑归 #45）

| 项 | 规则 |
| :--- | :--- |
| 形态 | **静态 pill**：`background: var(--state-active)` |
| 前景 | 图标升到 `--text`（未选中是 `--dim`） |
| 双色分离 | 1px 静态分离：右 `+1px 0 0 var(--glitch-r)`、左 `-1px 0 0 var(--accent)`（与 #13 按钮的「右红左紫」同向） |
| 动画 | **零新增常驻动画**；切换时无滑动（滑动留给 #46 评估，若采用必须 `prefers-reduced-motion` 下瞬时到位） |
| 语义 | 必须同时有 `aria-current="page"`（只靠颜色不算指示器） |
| 命中区 | 视觉 36px，命中 **44×44**（伪元素向外扩 4px），见 §6 |

## 6. 命中区与焦点

| 项 | 规则 |
| :--- | :--- |
| 最小命中 | **44×44**（WCAG 2.5.5 AAA）；顶栏 36px 图标按钮用 `::after { inset: -4px }` 外扩，视觉不变 |
| 行内文字按钮 | 命中高度 ≥32px，左右 padding ≥8px |
| 焦点环 | `outline: var(--ring); outline-offset: var(--ring-offset)`，**全站统一**，不许组件自造 |
| 键盘顺序 | 动作条内顺序 = DOM 顺序 = 视觉顺序（赞 → 收藏 → 评论） |

## 7. 动画预算（#13 之外的补充，不推翻 #13）

- **常驻循环**：沿用 #13「单屏 ≤5」且不许再加（外壳已 5）。
- **一次性（hover/按压/展开）**：不计入常驻预算，但**同屏同时处于动画中的元素 ≤3**，且只许动 `transform`/`opacity`（`box-shadow` 允许做 `transition`，但不许做成 keyframes 动画，见 #13 §2.2）。
- **落实手段**：新效果只能加在「同时最多一个可悬停实例」的位置（卡片、主按钮）；**不许**加在长列表的每一行上。
- **验收判据**：DevTools Performance/Animations 面板计数；`prefers-reduced-motion` 下必须为 0（tokens.css 的全局判死已覆盖，但 JS/WAAPI 侧要自己判，见 #13 §4.2）。

## 8. reduced-motion 下的反馈形态

全局 `animation: none !important; transition: none !important` 已经在 `tokens.css` 生效，因此：

- 反馈从「过渡」退化为「**瞬时切换**」——三件套的属性值必须**仍然变化**（这是可测的：把过渡关掉后 hover 仍要变色）。
- 禁止的实现：靠 `animation` 才能到达终态（例如「颜色由 keyframes 的 `to` 决定」）——这类在降级下**完全不反馈**，是本票要防的回归。
- 抖动类效果（icon-jit、标题抖字、扫描线）降级下判死，不承担任何语义。

## 9. 装饰层的收敛

| 页 | 允许的常驻装饰 |
| :--- | :--- |
| 落地页 | 标题三片撕裂（3 个循环） |
| 房间等待页 | 既有 5 个（上限） |
| **内容页（帖子 / 档案 / 论坛 / 消息 / 好友 / 设置）** | **只有顶栏那 1 条故障线**；无噪点、无扫描线 |

理由：内容页的主体是长文本与列表，噪点让小字变脏、扫描线让长滚动疲劳；而「月夜」在内容页由
色板 + 直角 + 故障字重承担已经足够（#13 §4 已论证降级后方向不塌，同一逻辑适用于「减少装饰」）。

## 10. 不做（本票明确排除）

- 整站换肤、换字体、改色板（--accent/--glitch-* 一律不动）
- 用故障效果承担状态语义（且 `--dim` 不得再调暗，见 tokens 注释）
- 深色/浅色双主题
- 头像上传相关的视觉（Out of scope，见地图）
