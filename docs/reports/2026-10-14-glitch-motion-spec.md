# 「月夜故障 · B（数字切片）」效果规格与降级策略

> 票：[#13 故障效果规格与降级策略](https://github.com/Drayee/ProjectionRoom/issues/13)
> 日期：2026-10-14
> 视觉来源：票 [#10](https://github.com/Drayee/ProjectionRoom/issues/10) 定稿的 B（数字切片）
> 原型（一手来源）：`docs/prototypes/glitch-direction.html` + 生成器 `_build-glitch-proto.mjs`
> 本文是 **#18 / #19 实现时的唯一依据**——所有数值从原型实测/抄录，不是估计。
> 标记说明：`[实测]` = 本次在 Chromium 内核对原型跑出来的数字；`[抄录]` = 从原型 CSS 逐字读出。

---

## 0. 结论速览

| 项 | 结论 |
| :--- | :--- |
| **精确参数** | 见 §1 效果规格表（15 个效果，逐项给出实现手段与数值） |
| **性能预算** | **常驻循环动画上限：落地页 3 / 登录窗 1 / 外壳 5 / 帖子面 1** `[实测]`；同屏 `box-shadow` 悬停元素 **≤ 8** |
| **时长与缓动** | 沿用 #6 三档（160/240/440ms）+ 统一缓动；**故障节奏另立一档**（§3） |
| **降级** | 逐效果三态（判死 / 静态化 / 保留），见 §4；**降级后仍成立**——B 的故障主要靠字体字形与直角结构 |
| **低端设备** | **只靠 media query**，不读 `hardwareConcurrency`/`deviceMemory`（理由见 §5） |
| **可访问性** | 对比度**全部达标**（实测最低 4.87:1）`[实测]`；发现一个必须修的缺陷：**标题被屏幕阅读器念 4 遍**（§6.1，修法已验证有效） |
| **硬阻塞** | **无** |

---

## 1. 效果规格表

### 1.1 常驻循环效果（会触发 WCAG 2.2.2，见 §6.3）

| # | 效果 | 触发 | 实现手段与精确值 | 时长 | 降级 |
| ---: | :--- | :--- | :--- | :--- | :--- |
| 1 | **标题三片横向撕裂 + 三色分离** | 常驻 | 三个绝对定位切片 `.sl1/.sl2/.sl3`，`clip-path` 分别 `inset(0 0 66% 0)` / `inset(33% 0 33% 0)` / `inset(66% 0 0 0)`；颜色 `#b46cff` / `#ff6ec7` / `#ff3d6e` | 抖动 2600 / 3100 / 3700ms，`steps(3,end)` / `steps(2,end)` / `steps(3,end)`，`infinite` | **保留但静态化**：切片位置与三色分离照留，只停抖动 |
| 2 | **外壳顶栏故障线** | 常驻 | `.shell-bar::after`，`linear-gradient(90deg, transparent, accent, glitch, transparent)`，高 2px，`translateX(-100% → 100%)` | 5000ms `linear` `infinite` | **判死**（纯装饰，停掉不损失信息） |
| 3 | **等待徽标呼吸环** | 常驻 | `.wait-badge::after`，`border: 1px solid accent`，`scale(1 → 1.35)` + `opacity(.5 → 0)` | 2800ms `var(--ease)` `infinite` | **判死**（三点脉冲仍在表达"等待中"） |
| 4 | **等待三点脉冲** | 常驻 | `.dot` × 3，`scale(1 → 1.5)` + `opacity(.25 → 1)`，各错开 180ms | 1400ms `var(--ease)` `infinite` | **静态化**：三点保留在 `.25` 透明度 |

**撕裂抖动关键帧**（抄录，`steps()` 阶梯——**这是刻意的手感，不是平滑**）：

```css
/* .sl1 —— 向左跳 18px，阶梯 3 档 */
@keyframes slB1{0%,80%{transform:none;opacity:.9}84%{transform:translateX(-18px);opacity:1}88%,100%{transform:none;opacity:.9}}
/* .sl2 —— 向右跳 14px，阶梯 2 档 */
@keyframes slB2{0%,86%{transform:none;opacity:.85}90%{transform:translateX(14px)}94%,100%{transform:none;opacity:.85}}
/* .sl3 —— 向左跳 9px + 斜切 -6deg，阶梯 3 档 */
@keyframes slB3{0%,90%{transform:none;opacity:.95}93%{transform:translateX(-9px) skewX(-6deg)}96%,100%{transform:none;opacity:.95}}
```

**确认 `steps()` 而非平滑**：故障美学的"位置不稳"需要**瞬时跳变**；平滑缓动会读成"优雅滑动"，与方向相反。实现时必须用 `steps()`，不得改成 `ease`。抖动幅度 9–18px 是按 `clamp(60px,14vw,132px)` 字号定的——**实现时按 `em` 重算**（约 `0.11em`–`0.14em`），否则小屏会甩出容器。

### 1.2 入场效果（一次性）

| # | 效果 | 触发 | 实现手段与精确值 | 时长 / 延迟 | 降级 |
| ---: | :--- | :--- | :--- | :--- | :--- |
| 5 | **标题三片「拼合」** | 进入落地页一次 | `asmIn1` `translateX(-70px)→none` / `asmIn2` `translateX(70px)→none` / `asmIn3` `translateY(30px)→none`，各带 `opacity 0→.85~.95` | 460ms，错开 **0 / 80 / 160ms** | **判死**（直接呈现静态三片） |
| 6 | **逐屏交错入场** | 每次切屏 | 子元素 `opacity 0→1` + `translateY(12px)→none`，逐项错开 **45ms**，起始延迟：落地页/登录窗 **40ms**、外壳 **80ms** | 240ms/项 | **判死**（直接呈现） |
| 7 | **按钮 → 登录窗 FLIP morph** | 点「开始」 | 量按钮 `getBoundingClientRect()`，对登录窗施加 `translate(dx,dy) scale(sx,sy)` + `borderRadius: 999px → 计算值` + `opacity .7→1` | **440ms** `var(--ease)` `fill:both`；窗内子元素随后错开 **140ms** 起 | **判死**（直接切到登录窗） |

**拼合的收尾**：入场播完后**必须摘掉 `assembling` 类**（原型用 `setTimeout(…, 1100)`）。否则入场动画与常驻抖动会同时作用于同一元素，`animation` 覆盖导致抖动失效。

### 1.3 悬停 / 交互效果

| # | 效果 | 触发 | 实现手段与精确值 | 时长 | 降级 |
| ---: | :--- | :--- | :--- | :--- | :--- |
| 8 | **按钮 RGB 分离外发光** | `.start:hover` | `box-shadow: 5px 0 0 #ff3d6e, -5px 0 0 #b46cff, 0 10px 30px rgba(180,108,255,.35)`；同时 `background: #c98dff`、`letter-spacing: .16em → .2em` | 240ms `var(--ease)` | **保留**（静态分离照留） |
| 9 | **按钮掠过亮带** | `.start:hover` | `::before`，`linear-gradient(100deg, transparent 30%, rgba(255,255,255,.55) 50%, transparent 70%)`，`translateX(-120% → 120%)` | **720ms** `var(--ease)` | **判死** |
| 10 | **卡片悬停分离边框 + 角标展开** | `.card:hover` | `box-shadow: 4px 0 0 rgba(61,255,168,.5), -4px 0 0 rgba(180,108,255,.5), 0 14px 34px rgba(0,0,0,.45)`；`transform: translateY(-3px)`；`::after` 三角 `opacity .25 → .7` | 240ms `var(--ease)` | **保留**（只留边框变色，去 `box-shadow` 与位移） |
| 11 | **缩略图扫描线扫过** | `.post:hover` | `.thumb::after`，`linear-gradient(180deg, transparent, rgba(180,108,255,.35), transparent)`，`translateY(-100% → 100%)` | **700ms** `var(--ease)` | **判死** |
| 12 | **标题故障抖字** | `.post:hover h5` | `translateX(0 → -2px → 2px → -1px)` + `skewX(-3deg / 2deg)` | **420ms** `steps(2,end)` | **判死** |
| 13 | **图标悬停抖动** | `.icon-btn:hover` | `translateX(0 → -2px → 2px → 0)` | **260ms** `steps(2,end)` | **判死** |
| 14 | **输入框聚焦分离描边** | `.win input:focus` | `border-color: #b46cff` + `box-shadow: 2px 0 0 #ff3d6e, -2px 0 0 #b46cff` | 160ms `var(--ease)` | **保留**（只留 `border-color`，去 `box-shadow`） |
| 15 | **FAB 呼吸扩散环 + 悬停旋转** | 常驻 + hover | `::before` `scale(1 → 1.55)` + `opacity(.6 → 0)`；hover 时 `scale(1.08) rotate(6deg)` | 环 **2600ms** `infinite`；hover 240ms | **判死**（环停掉，hover 保留 `scale`） |

### 1.4 微交互（过渡类，非动画）

| 元素 | 属性 | 时长 |
| :--- | :--- | :--- |
| `.icon-btn` | `color` / `border-color` / `transform` / `background` | **160ms** `var(--ease)` |
| `.win input` | `border-color` / `box-shadow` / `background` | **160ms** |
| `.win .primary` | `transform` / `filter` | **160ms** |
| `.win .ghost` | `color` / `border-color` / `transform` | **160ms** |
| `:active` 态（各按钮） | `transform: scale(.97)` 或 `.98` | **160ms** |

---

## 2. 性能预算

### 2.1 常驻循环动画数（`[实测]`，原型在 Chromium 内核逐屏统计）

| 屏 | 常驻循环动画数 | 属性分布 |
| :--- | ---: | :--- |
| 落地页 | **3** | `transform` 15 处 / `opacity` 13 处（含多关键帧计数） |
| 登录窗 | **1** | `transform` 5 / `opacity` 5 |
| 外壳（等待页） | **5** | `transform` 14 / `opacity` 12 |
| 帖子面 | **1** | `transform` 2 |

**硬约束**：
- **单屏常驻循环动画 ≤ 5 个**。外壳的 5 个已是上限（顶栏线 + 徽标环 + 三点脉冲 + FAB 环 + 图标抖动），**不得再加**。
- 所有常驻动画**只允许动 `transform` / `opacity`**。已在原型上核验「非合成属性 = 0」。
- **禁止**：`filter: blur` 大面积、`backdrop-filter`、动画化 `box-shadow` 的**大面积**元素、动画化 `width/height/top/left`（会触发布局）。
  - 原型已修掉两处违规写法：CRT 扫描线的 `top` → `transform`；噪点的 `background-position` → `transform`。

### 2.2 `box-shadow` 悬停的风险（票里点名的问题）

`[实测]`：卡片悬停的 `transition-property` 是 **`border-color, transform, box-shadow`**，时长 `0.24s ×3`。

**结论**：`box-shadow` **不是合成属性**，其变化每帧触发**重绘**（reflow 不会，但 paint 会）。单元素 24 张卡片同时悬停不现实（鼠标只有一个），因此：

- **同屏 `box-shadow` 悬停元素 ≤ 8**（实际只会 1 个）。
- **列表页硬约束**：帖子列表若超过 20 张卡片，**悬停分离效果降级为只改 `border-color`**（去掉 `box-shadow`）——因为 `box-shadow` 的绘制代价与元素数量线性相关，而长列表滚动时可能同时处于悬停/焦点状态。
- 若实现时发现滚动掉帧，**优先砍 `box-shadow`，保留 `transform: translateY(-3px)`**（后者是合成属性）。

### 2.3 实测边界

`[实测]` 在 headless Chromium 下 `document.visibilityState === 'hidden'`，**rAF 被完全节流，帧率（fps）与 longtask 无法取得可信数字**。因此本规格**不给出帧率承诺**，只给出可确定的**结构约束**（动画数上限、属性白名单、`box-shadow` 数量上限）。真机帧率需在实现票里用真浏览器前台测量。

---

## 3. 时长与缓动

### 3.1 沿用 #6 的三档（确认）

```css
--ease: cubic-bezier(.22,.61,.36,1);
--dur-micro: 160ms;   /* 微交互：悬停、聚焦、按压 */
--dur-comp:  240ms;   /* 组件：卡片、面板、交错入场的单项 */
--dur-morph: 440ms;   /* 大幅：按钮→登录窗的 FLIP morph */
```

### 3.2 故障节奏**另立一档**（新增）

故障效果的节奏与"交互反馈"是两回事：前者要**看起来不稳定**，后者要**看起来确定**。因此单独列出，**不并入三档**：

| 名称 | 值 | 用途 |
| :--- | :--- | :--- |
| 撕裂抖动周期 | **2600 / 3100 / 3700ms**（互质，避免同步） | 标题三片 |
| 顶栏故障线周期 | **5000ms** `linear` | 外壳顶栏 |
| 缩略图扫描 | **700ms** | 帖子卡悬停 |
| 标题抖字 | **420ms** | 帖子卡悬停 |
| 图标抖动 | **260ms** | 图标悬停 |
| 掠过亮带 | **720ms** | 按钮悬停 |
| 呼吸环 / 三点 | **2600 / 1400ms** | 等待页 |

**为什么三个撕裂周期互质**：若取相同周期，三片会同时归位，看起来像"一次整齐的闪"而非"持续的不稳"。互质（2600/3100/3700）让它们的相位持续漂移——这是该效果成立的**关键参数，不得统一**。

---

## 4. `prefers-reduced-motion` 降级形态

**总原则**：降级后**仍然成立**——B 的"月夜故障"由三件事承担：① 故障黑体的**字形本身**（破碎笔画）、② **全直角**结构、③ **三色分离**的静态配色。三者都与动效无关，因此降级后视觉方向不塌。

| 效果 | 降级形态 |
| :--- | :--- |
| 1 标题撕裂抖动 | **静态化**：切片与三色分离保留，停抖动 |
| 2 顶栏故障线 | 判死 |
| 3 呼吸环 | 判死 |
| 4 三点脉冲 | 静态化（保留 `.25` 透明度） |
| 5 拼合入场 | 判死 |
| 6 交错入场 | 判死 |
| 7 FLIP morph | 判死（直接切屏） |
| 8 按钮分离外发光 | 保留 |
| 9 掠过亮带 | 判死 |
| 10 卡片悬停 | 保留（只留 `border-color`） |
| 11 缩略图扫描 | 判死 |
| 12 标题抖字 | 判死 |
| 13 图标抖动 | 判死 |
| 14 聚焦分离描边 | 保留（只留 `border-color`） |
| 15 FAB 环 / 旋转 | 判死 |

### 4.1 实现要点：**必须显式列出伪元素**

`[实测]` 踩过的坑：通配选择器 `*` **选不中 `::before` / `::after`**，导致 FAB 呼吸环与顶栏故障线在降级下**仍在动**。正确写法：

```css
@media (prefers-reduced-motion: reduce){
  *, *::before, *::after { animation: none !important; transition: none !important; }
}
```

修正后 `[实测]` 降级下动画数 = **0**。

### 4.2 JS 侧同样要守

WAAPI（`element.animate()`）**不自动尊重** `prefers-reduced-motion`。实现时必须：

```js
const REDUCED = matchMedia('(prefers-reduced-motion: reduce)').matches;
function anim(el, keyframes, opts){ if (REDUCED) return null; return el.animate(keyframes, opts) }
```

原型的 FLIP morph、交错入场、擦除过渡全走这条路——**双侧判死**，缺一侧就会出现"CSS 停了但 JS 还在动"。

---

## 5. 省电 / 低端设备

**结论：只靠 media query，不读 `navigator.hardwareConcurrency` / `deviceMemory`。**

理由：
1. `deviceMemory` **只有 Chromium 支持**（Safari / Firefox 无），按它降级会造成浏览器间行为不一致——同一个用户换浏览器看到不同效果。
2. `hardwareConcurrency` 反映**逻辑核数**，与"能否流畅跑合成动画"相关性弱（GPU 与刷新率才是关键），容易误判：8 核低端机被当高端、2 核高配机被降级。
3. 本方向的效果**已全部约束在合成属性 + 单屏 ≤ 5 个常驻动画**（§2），这个量级在现代设备上不构成压力；真正的风险点是 `box-shadow` 重绘，已在 §2.2 用**列表长度阈值**处理，比设备探测更精确。

**若后续真机验收发现低端机掉帧**，按此优先级降级（不动结构）：
1. 砍 `box-shadow` 悬停（§2.2）
2. 停撕裂抖动（效果 1）
3. 停顶栏故障线（效果 2）

**可选增强**（不在本票范围，实现时不做）：`@media (prefers-reduced-motion: reduce)` 已覆盖"省电模式"在 iOS/Android 上的用户意图，无需额外探测。

---

## 6. 可访问性

### 6.1 ⚠️ 必须修的缺陷：标题被读 4 遍（`[实测]`）

**实测结果**：
```
.brand 的 textContent       = "月喵月喵月喵月喵"（8 字，4 遍）
.brand 的可访问文本（未修复）= "月喵月喵月喵月喵"
三个 .sl 切片的 aria-hidden 均为 null（未设置）
```

**问题**：屏幕阅读器会把主标题**念 4 遍**（父级 `.sr` + 三个装饰切片各含一份"月喵"）。

**修法**（实现时必须做，二选一）：
```html
<!-- 方案 A（推荐）：装饰切片全部 aria-hidden，只留一个可读文本 -->
<h1 class="brand">
  <span class="sr">月喵</span>                              <!-- 可访问名来源 -->
  <span class="sl sl1" aria-hidden="true">月喵</span>
  <span class="sl sl2" aria-hidden="true">月喵</span>
  <span class="sl sl3" aria-hidden="true">月喵</span>
</h1>
```
```html
<!-- 方案 B：整体给 aria-label，内容全装饰 -->
<h1 class="brand" aria-label="月喵">
  <span aria-hidden="true" class="sl sl1">月喵</span> …
</h1>
```

**修法已验证**（`[实测]`）：就地给三个切片加上 `aria-hidden="true"` 后，按"未被 `aria-hidden` 的文本"递归计算的可访问文本从 `"月喵月喵月喵月喵"` 变为 **`"月喵"`**——修复有效。

**为什么必须做**：`ui-ux-governance` 的 `Inclusive use and device conditions` 要求"使用有意义的标签/可访问名"。标题被念 4 遍是明确的可用性缺陷，属原型脚手架引入的问题，**不能带进生产**。

> **一处自我更正**：本文早期版本曾据此断言"标题对辅助技术不可见"。那是**误读**——`browser_a11y` 工具只列交互节点（其说明写明 "every interactive node"），`<h1>` 本就不在其中。实际缺陷只有"重复 4 遍"这一条。

### 6.2 对比度（`[实测]`，WCAG 2.x 公式实算）

| 组合 | 对比度 | 判定 |
| :--- | ---: | :--- |
| 正文 `#f0e6ff` / 底 `#0a0710` | **16.62** | AA ✓ |
| 正文 / 表面 `#150e22` | **15.63** | AA ✓ |
| 次要 `#8b7aa8` / 底 | **5.18** | AA ✓ |
| 次要 / 表面 | **4.87** | AA ✓（最低项，仍过线） |
| 强调 `#b46cff`（作文字） / 底 | **6.21** | AA ✓ |
| 强调 / 表面 | **5.84** | AA ✓ |
| 故障绿 `#3dffa8` / 底 | **15.29** | AA ✓ |
| 故障粉 `#ff6ec7` / 底 | **7.91** | AA ✓ |
| 故障红 `#ff3d6e` / 底 | **5.85** | AA ✓ |
| 按钮文字 `#0a0a0a` / 强调底 | **6.16** | AA ✓ |
| 按钮文字 `#12091e` / 强调底 | **6.03** | AA ✓ |
| **描边 `rgba(200,120,255,.20)` 近似** | **1.45** | ✗ |

**结论**：
- **文字与按钮全部满足 WCAG AA（≥4.5:1）**，最低项是"次要文字 / 表面"4.87:1。
- **描边 1.45:1 不达标——但它不承载语义**。因此立一条硬约束：**状态（选中/错误/成功/悬停）不得只靠描边颜色表达**，必须同时改背景、文字色或加图标。这也是 `ui-ux-governance` 要求的"反馈不能仅靠颜色"。
- 实现时若把 `--dim` 再调暗（例如为了"更暗的故障感"），**会跌破 4.5:1**——`#8b7aa8` 已是下限，不得再暗。

### 6.3 WCAG 2.2.2（暂停、停止、隐藏）`[实测]`

**常驻自动循环动画清单**（原型实测）：
| 屏 | 常驻动画 | 时长 |
| :--- | :--- | :--- |
| 落地页 | 撕裂抖动 ×3 | 2600 / 3100 / 3700ms |
| 外壳 | 顶栏故障线 / 呼吸环 / 三点 / FAB 环 | 5000 / 2800 / 1400 / 2600ms |

**判定**：WCAG 2.2.2 的豁免条件是"该动效对正在进行的活动是**必要的**"。本方向的所有常驻动画**均非必要**（纯装饰），且**单个时长都 < 5s**、无限循环。

**处置（写进实现约束）**：
1. **`prefers-reduced-motion` 是主要出口**——系统级"减少动态效果"开关即满足"用户可停止"；
2. 装饰性动画元素**必须 `aria-hidden="true"`**（§6.1）；
3. **不做**页面内的"暂停动画"按钮——本方向动画为低强度、低面积（撕裂 3 片、顶栏 2px 线），加按钮的代价（界面上多一个非产品控件）大于收益，且 `prefers-reduced-motion` 已覆盖用户意图。
4. **若后续真机验收有用户反馈眩晕**，升级为：在设置页提供"关闭故障动效"开关，写入 `localStorage` 并在 `html` 上加 `data-motion="off"`（原型已有该属性与对应 CSS，可直接复用）。

### 6.4 装饰元素 `aria-hidden` 清单

| 元素 | 是否必须 `aria-hidden` | 理由 |
| :--- | :--- | :--- |
| 标题三个撕裂切片 `.sl1/.sl2/.sl3` | **必须** | 重复文本，且已致标题缺失（§6.1） |
| 顶栏故障线 `.shell-bar::after` | 不适用 | 伪元素不进无障碍树 |
| 呼吸环 `.wait-badge::after` | 不适用 | 伪元素 |
| 等待三点 `.dot` | **必须** | 无语义的空 `span` |
| 卡片角标 `.card::after` | 不适用 | 伪元素 |
| 图标（`BrandIcon` 的 mask `span`） | **必须** | 已有 `aria-hidden="true"`（原型已做）✓ |

---

## 7. 实现清单（给 #18 / #19 的检查项）

- [ ] 三档时长 + 统一缓动写成 CSS 变量（`--dur-micro/comp/morph`、`--ease`）
- [ ] 故障节奏独立成组（§3.2），**三个撕裂周期保持互质**
- [ ] 撕裂抖动用 `steps()`，幅度按 `em` 重算（不写死 px）
- [ ] 拼合入场播完**摘类**（1100ms），避免与常驻抖动冲突
- [ ] 降级规则**显式含 `::before` / `::after`**
- [ ] JS 侧动画走 `REDUCED` 判死（FLIP morph / 交错入场）
- [ ] **标题可访问名修复**（§6.1 方案 A 或 B）
- [ ] 装饰性 `span` 加 `aria-hidden="true"`（撕裂切片、等待三点）
- [ ] 单屏常驻循环动画 **≤ 5**
- [ ] 帖子列表 > 20 张时，悬停去掉 `box-shadow`
- [ ] 状态不得只靠描边颜色表达（描边 1.45:1 不达标）
- [ ] 不得再把 `--dim` 调暗（`#8b7aa8` 是 4.5:1 下限）
- [ ] 不引入 `filter: blur` / `backdrop-filter` 大面积动画
- [ ] `npm --prefix client run build` 绿；`grep -rn "v-html\|innerHTML" client/src` 为 0

---

## 8. 边界与未做的事

- **未测真机帧率**：headless Chromium 的 `visibilityState === 'hidden'` 使 rAF 被节流，fps 与 longtask 不可信（§2.3）。规格只给结构约束，不给帧率承诺。
- **未做跨浏览器验证**：所有实测在单一 Chromium 内核完成；Safari / Firefox 的 `clip-path` + `steps()` + WAAPI 行为未测。
- **未做低端设备实测**：`§5` 的降级优先级是**推理结果**，未经真机验证。
- **未做屏幕阅读器实测**：§6.1 的缺陷由无障碍树 + `textContent` 推得，未用 NVDA / VoiceOver 实听。
- **未实现**：本票只产出规格，不含任何 `client/src` 改动（实现属 #18 / #19）。
