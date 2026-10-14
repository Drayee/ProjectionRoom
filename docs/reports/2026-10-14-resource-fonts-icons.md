# 调研报告：`E:\Resource` 字体与图标的可入仓形态

> 票：#9「调研 E:\Resource 字体与图标的可入仓形态」
> 日期：2026-10-14
> 范围：只读取证 + 本机实测。**未修改任何源码**（`client/src/**`、`internal/**`、`test/**`、`scripts/**` 全程只读），
> **未做任何 git 操作**。所有实验都在 `$env:TEMP\pr-research\` 下进行，本报告是唯一写进仓库的文件。
> 视觉方向前提：「月夜故障（glitch / 破碎美学）」+ icon-first 交互；CSP 保持默认严格（禁外链资源）。

---

## 0. 结论速览（可直接执行）

| 项 | 结论 |
| :--- | :--- |
| **字体选型** | 主 UI 字体用 **`寒蝉端黑体` 的 `ChillDuanSans_Black.otf`**（OFL 1.1，fsType=0，GB2312 100%）；装饰/标题用 **`猫啃网故障黑.otf`**（OFL 1.1，fsType=0，GB2312 100%，本身就是故障美学）。**`破碎零号字/零ゴシック.otf` 不建议用**（详见 §1.3）。 |
| **子集化工具链** | `fontTools` 4.66.1 + `brotli` 1.2.0，用 `pyftsubset`。**已实测跑通**，确切安装命令见 §2.1（注意：清华源在本机不可达，必须走 `pypi.org`）。 |
| **字符集策略** | **不要只用 `client/src` 抽字**。项目有大量**运行期用户产生的中文**（`displayName`、聊天 `msg.text`、帖子正文、房间名），`src` 抽字必然漏字。推荐 **`src` 汉字 ∪ GB2312 一级字库(3755)**。 |
| **推荐产物体积** | `ChillDuanSans_Black` 子集 **635.9 KiB**（woff2）+ `MaokenGlitchSans` 子集 **800.9 KiB**（woff2）= **合计 1.41 MiB**，覆盖全部现有界面文案 + 3755 常用字。 |
| **图标机制** | 两套都能**原样走现有 `BrandIcon.vue` 的 CSS mask**，**无需改渲染方式、无需改 SVG**。`stroke="#ffffff"` 硬编码**不会**让 mask 失效；`filter` 也**不影响** mask（有实测数据，见 §3）。 |
| **图标选型** | 以 **`icons/`（250 个线性 24×24）为主**，只在 `icons/` 确实没有的语义上补 **`570+`（填充风格）**。共 **44 个文件、61.1 KiB**（gzip 后 29.1 KiB）。 |
| **硬阻塞** | **无**。全部方案均在本机实测通过，含 CSP 严格模式下的加载验证。 |
| **`theme/` 目录** | `E:\Resource\icon\icons\theme\` 为其他项目专用，本项目不使用。 |

---

## 1. 字体：实际格式、家族名、字形覆盖、许可

### 1.1 三个 zip 里到底是什么

三个 zip **都被塞进了同一套「TOP10免费商用字体集合」**（18 个条目，逐字节相同，sha256 一致）。

| 压缩包 | zip 体积 | 解压总体积 | 主字体 | TOP10 冗余 |
| :--- | ---: | ---: | ---: | ---: |
| `寒蝉端黑体.zip` | 261,216,744 B (249.1 MiB) | 331,905,620 B | 27 项 / 225.6 MiB | 18 项 / **91.0 MiB** |
| `猫啃网故障黑.zip` | 86,652,622 B (82.6 MiB) | 153,690,368 B | 1 项 / 55.6 MiB | 18 项 / **91.0 MiB** |
| `破碎零号字.zip` | 63,775,966 B (60.8 MiB) | 99,951,072 B | 1 项 / 4.4 MiB | 18 项 / **91.0 MiB** |

> 三个 zip 里 TOP10 捆绑包重复出现 3 次，合计解压 **286,129,680 B = 272.9 MiB**。18 个条目在三个 zip 中**内容完全相同**（实测：`三份内容不同: 0  相同: 18`）。
> **这三个 zip 里没有任何许可文件**（`*.txt` / `*.md` / `*.pdf` 全无），许可只能从字体自身的 `name` 表读。

### 1.2 逐文件实测（容器格式 / 家族名 / 覆盖）

实测命令（`fontTools`）：

```powershell
$env:PYTHONIOENCODING='utf-8'; $env:PYTHONUTF8='1'
python "$env:TEMP\pr-research\inspect_fonts.py"      # 格式 / name 表 / cmap 覆盖
python "$env:TEMP\pr-research\coverage.py"           # 对 client/src 用字与 GB2312 的覆盖
python "$env:TEMP\pr-research\zip_audit.py"          # zip 冗余量化 + fsType 审计
```

| 文件 | 体积 | 容器 | 家族名 (nameID1) | PS 名 (nameID6) | 字形数 | cmap | src 汉字覆盖 | GB2312 覆盖 |
| :--- | ---: | :--- | :--- | :--- | ---: | ---: | ---: | ---: |
| `猫啃网故障黑.otf` | 58,313,808 B (55.6 MiB) | **OTTO / CFF** | `Maoken Glitch Sans` | `MaokenGlitchSans-Regular` | 31,036 | 30,892 | **1200/1200 = 100%** | **6763/6763 = 100%** |
| `ChillDuanSans_Black.otf` | 10,520,076 B (10.03 MiB) | **OTTO / CFF** | `ChillDuanSans Black` | `ChillDuanSans_Black` | 31,037 | 30,892 | **1200/1200 = 100%** | **6763/6763 = 100%** |
| `ChillDuanSans_Light.otf` | 9,228,036 B (8.80 MiB) | **OTTO / CFF** | `ChillDuanSans ExtraLight` | `ChillDuanSans_Light` | 31,037 | 30,892 | **1200/1200 = 100%** | **6763/6763 = 100%** |
| `零ゴシック.otf` | 4,574,512 B (4.36 MiB) | **OTTO / CFF** | `ZeroGothic` | `ZeroGothic` | 9,354 | 8,262 | **795/1200 = 66.25%** | **3385/6763 = 50.05%** |

**结论：全部是 OTF（CFF/PostScript 轮廓），没有 TTF，没有 TTC。** 四个文件的 `sfntVersion` 都是 `b'OTTO'`。
`寒蝉端黑体.zip` 里 27 个文件全是 `ChillDuanSans_*` 的**同一家族多字重**（Black/Light/Bold/Heavy/Medium/Regular/SemiBold + Compact/Condensed/Expanded/Wide 四组宽度变体），每个约 8.3–10.5 MiB。
`猫啃网故障黑.zip` 里的 `猫啃网故障黑/猫啃网故障黑/猫啃网故障黑.otf` 与磁盘上独立的 `E:\Resource\font\猫啃网故障黑.otf` **逐字节相同**（sha256 `2b5400196af8e6fb…`）。

### 1.3 许可条款（读的是字体 `name` 表，不是猜的）

| 字体 | `nameID13` 许可文本 | `nameID14` | `OS/2.fsType` |
| :--- | :--- | :--- | :--- |
| `猫啃网故障黑.otf` | **SIL Open Font License, Version 1.1** | `http://scripts.sil.org/OFL` | **0 = Installable Embedding（无限制）** |
| `ChillDuanSans_Black.otf` | **SIL Open Font License, Version 1.1** | `http://scripts.sil.org/OFL` | **0 = Installable Embedding（无限制）** |
| `ChillDuanSans_Light.otf` | **SIL Open Font License, Version 1.1** | `http://scripts.sil.org/OFL` | **0 = Installable Embedding（无限制）** |
| `零ゴシック.otf` | **无 OFL 文本**，实际内容为 `Cr:OTM0971210106204146B000012 Mo:OTM0971210107140629B000012` | （空） | **8 = Editable embedding（受限期嵌入）** |

版权行原文：
- 猫啃网故障黑：`© 2021 Maoken, NightFurySL2001. Maoken Glitch is redistributed under SIL Open Font License.` + `© 2014-2021 Adobe (…), with Reserved Font Name 'Source'.`，设计者 `NightFurySL2001 (modification based on Source Han Sans CN Heavy)`
- 寒蝉端黑体：`© 2023 ChillType, with Reserved Font Name 'Duan'.` + Adobe 行，厂商 `ChillType&Adobe`

**`零ゴシック.otf` 的三个硬伤**（任一都足以排除它）：
1. **覆盖不够**：只覆盖 `client/src` 用到的 1200 个汉字里的 **795 个（66.25%）**，GB2312 只覆盖 **50.05%**。缺的 405 个字里包含 **`专 业 东 为 乐 产 们 传 你 关 单 卡 发 变 图 场 处 备 头 实 对 导 层 带 帮 库 应 开 张 强 归 录 态 总 户 报 换 时 显 术 权 极 查 标 树 样 每 汉 测 满 环 现 盘 确 离 种 稳 类 线 组 经 给 绝 统 维 编 职 联 胜 脑 节 获 营 蓝 见 观 规 视 计 认 让 记 许 论 设 访 证 评 识 词 试 诚 话 该 语 说 请 读 调 贝 负 责 败 账 质 贴 费 资 赞 车 转 轮 轴 轻 载 较 输 边 达 过 运 还 进 远 连 适 选 递 遗 邮 鉴 针 钥 销 锁 键 长 门 问 间 队 际 陈 险 隐 页 顶 项 顺 领 题 颜 风 飞 饰 马 验 骗 鱼`** 等**常用字**（不是冷僻字）。
2. **`fsType=8`**：受限期嵌入，与本项目的「本地托管 + 子集化再分发」路径冲突。
3. **无 OFL**：许可字段是一串 OTM 编号，无任何可用的授权声明。

> 附带发现：`零ゴシック.otf` 的 `nameID0` 写着 `Copyright(c)2017 Adobe Systems Incorporated (…), with Reserved Font Name 'Source'.`，即它也是 Source Han Sans 的改造版，但**改造者没有像另外两个字体那样保留 OFL 声明**。

---

## 2. 子集化与 woff2 转换：工具链与实测数字

### 2.1 安装（**已实测**；本机清华源不可达，这一步会踩坑）

`python -m pip install fonttools brotli` 在本机**会失败**。实测根因：

- 本机 `pip.ini` 把 index 指向 `https://pypi.tuna.tsinghua.edu.cn/simple`；
- 该源通过本机代理 `127.0.0.1:7890`（系统代理已开启）**握手超时**：`ProxyError('Cannot connect to proxy.', TimeoutError('_ssl.c:1011: The handshake operation timed out'))`；
- 直连（`--proxy ""`）也失败：`getaddrinfo failed`；
- **`pypi.org` 与 `mirrors.aliyun.com` 经代理可达**（实测 HTTP 200）。

**可用命令（已跑通）：**

```powershell
# 前提：系统代理 127.0.0.1:7890 在监听（实测 True）
python -m pip install --proxy http://127.0.0.1:7890 `
  --index-url https://pypi.org/simple --no-cache-dir fonttools brotli
```

实测结果：

```
Collecting fonttools
  Downloading fonttools-4.66.1-cp313-cp313-win_amd64.whl (2.5 MB)
Requirement already satisfied: brotli in c:\program files\python313\lib\site-packages (1.2.0)
Successfully installed fonttools-4.66.1
```

验证：

```
fontTools 4.66.1
brotli 1.2.0
```

> 因为普通 `site-packages` 不可写，pip 自动落到用户目录，入口点在
> `C:\Users\<用户>\AppData\Roaming\Python\Python313\Scripts\pyftsubset.exe`（实测存在）。

**备选方案（无网络/权限时）**：Node 侧 `subset-font`（已实测可安装可运行，见 §2.6）。

### 2.2 字符集怎么取：两种方案都实测了

**方案 甲（从 `client/src` 抽汉字）——实测统计：**

```powershell
python "$env:TEMP\pr-research\extract_charset.py"
```

```
scanned 78 files under client/src
distinct CJK+punct chars used: 1222
total occurrences: 80228
其中：汉字 1200 个 + 标点/符号 22 个
```

**方案 乙（常用字表 / GB2312）——实测：** GB2312 共 **6763** 字（一级 3755 + 二级 3008）。

**⚠️ 关键发现：`src` 抽字不足以覆盖运行期文案。** 项目有大量**用户产生的中文**，不经过源码：

| 来源 | 证据（只读） |
| :--- | :--- |
| 聊天内容 | `client/src/components/ChatPanel.vue:49` `<div class="text">{{ msg.text }}</div>` |
| 显示名 | `ChatPanel.vue:46` `{{ msg.displayName }}`；`MemberList.vue:21` `{{ member.displayName }}` |
| 管理端用户表 | `AdminView.vue:404-405` `{{ user.username }}` / `{{ user.displayName }}` |
| 房间名 / 帖子正文 | `MyRoomsCard.vue:74`、`PostsView` / `PostDetailView` 的帖子正文 |

这些字段是**任意中文**，`src` 抽字覆盖不到。因此**推荐 `src ∪ GB2312 一级(3755)`**。

### 2.3 字符集策略对比（`MaokenGlitchSans`，woff2，实测）

```powershell
python "$env:TEMP\pr-research\subset2.py"
```

| 字符集 | 码点数 | 产物体积 | 相对原字体 | 耗时 |
| :--- | ---: | ---: | ---: | ---: |
| `E_latin_only`（拉丁+符号，无汉字） | 523 | 150,752 B (147.2 KiB) | 0.26% | 2.98 s |
| `A_src_only`（src 汉字 + 拉丁） | 1,723 | **820,092 B (800.9 KiB)** | 1.41% | 14.18 s |
| `B_src+gb1`（**推荐**） | 4,288 | **2,193,472 B (2142.1 KiB)** | 3.76% | 40.83 s |
| `C_src+gb12`（全 GB2312） | 7,286 | **3,865,128 B (3774.5 KiB)** | 6.63% | 71.50 s |
| `D_gb12_only`（纯 GB2312，不含 src 特有标点） | 7,286 | 3,865,128 B | 6.63% | 74.58 s |

> `C` 与 `D` 体积完全相同，说明 `src` 用到的汉字与标点**已被 GB2312 全覆盖**——即「`src` 抽字」的价值只在于**省掉二级字库**。

**纯高频字表（按 `src` 出现频次取前 N）实测：**

| 字符集 | MaokenGlitchSans | ChillDuanSans_Black |
| :--- | ---: | ---: |
| top200 汉字 | 259,488 B (253.4 KiB) | 60,744 B (59.3 KiB) |
| top500 汉字 | 425,160 B (415.2 KiB) | 106,688 B (104.2 KiB) |
| top1000 汉字 | 703,792 B (687.3 KiB) | 188,936 B (184.5 KiB) |

**结论**：`top1000`（687 KiB）与 `A_src_only`（801 KiB）体积接近但覆盖率更低——**频次表不如直接抽 `src`**。所以只需在 **A** 与 **B** 之间选。

### 2.4 输出格式对比（`MaokenGlitchSans`，字符集 `B`，实测）

| 格式 / 参数 | 体积 | 说明 |
| :--- | ---: | :--- |
| 原始 `.otf`（子集后，未压缩） | 6,669,940 B (6513.6 KiB) | 基线 |
| `--flavor=woff` | 3,222,716 B (3147.2 KiB) | 比 woff2 大 47% |
| `--flavor=woff2`（保留 layout features） | **2,178,352 B** | |
| `--flavor=woff2 --desubroutinize` | 2,178,352 B | **无变化**（CFF 子集已去子程序化） |
| `--flavor=woff2` + 最小 layout features | 2,164,064 B | 只省 14 KiB，**不建议**（丢掉 `kern`/`liga` 等） |

**`--desubroutinize` 实测无收益**（体积逐字节同），可以从命令里去掉。

### 2.5 分片（`unicode-range`）实测：**不划算**

```powershell
# subset2.py PHASE 2：500 字/片，共 15 片
```

| 方案 | 总体积 |
| :--- | ---: |
| 单片 `C_src+gb12` | 3,865,128 B |
| 15 片（拉丁+符号 1 片 + CJK 14 片） | **4,655,552 B** |
| **分片开销** | **+790,424 B（+20.4%）** |

**结论**：分片**增加 20.4% 体积**，只在「首屏只加载拉丁片」的场景才划算。本项目界面文案首屏就是中文，**不建议分片**。

### 2.6 woff2 是终态压缩：**不要再叠 gzip/brotli**

实测（`verify_subset.py`，对已产出的 woff2 再压）：

| 产物 | raw | gzip -9 | brotli q11 |
| :--- | ---: | ---: | ---: |
| `mk__B_src+gb1.woff2` | 2,193,472 | 2,194,160（**更大**） | 2,193,480（**更大**） |
| `ChillDuanSans_Black__C.woff2` | 1,171,004 | 1,171,382（**更大**） | 1,171,009（**更大**） |

**woff2 内部已是 brotli 压缩，再压只会变大。** 部署时**不要**对 `/fonts/*.woff2` 套 gzip/brotli。

### 2.7 三个候选字体 × 推荐字符集：最终产物体积（实测）

命令（**这是可直接执行的生产命令**，`$SRC` 指字体路径，`$CHARS` 指字符集文本文件）：

```powershell
$PYFT = "$env:APPDATA\Python\Python313\Scripts\pyftsubset.exe"
& $PYFT "E:\Resource\font\猫啃网故障黑.otf" `
  --text-file=$CHARS `
  --output-file=client\public\fonts\maoken-glitch-sans.woff2 `
  --flavor=woff2 `
  --layout-features=kern,liga,clig,calt,ccmp,locl,mark,mkmk,vert,vrt2,palt,halt,vhal,frac,subs,sups,tnum,pnum,onum,lnum,zero `
  --name-IDs=0,1,2,3,4,5,6,13,14 --name-languages=0x409,0x804 `
  --notdef-outline --recalc-bounds --no-hinting `
  --drop-tables+=DSIG,vhea,vmtx,VORG,BASE
```

**实测产物（字符集 `A_src_only` / `B_src+gb1`）：**

| 字体 | 字符集 A (1723 码点) | 字符集 B (4288 码点) | B 耗时 |
| :--- | ---: | ---: | ---: |
| `猫啃网故障黑.otf` → `MaokenGlitchSans` | 820,092 B (800.9 KiB) | 2,193,472 B (2142.1 KiB) | 35.00 s |
| `ChillDuanSans_Black.otf` | **222,888 B (217.7 KiB)** | **651,124 B (635.9 KiB)** | 6.08 s |
| `ChillDuanSans_Light.otf` | 212,904 B (207.9 KiB) | 605,368 B (591.2 KiB) | 6.11 s |
| `零ゴシック.otf`（**不推荐**） | — | 1,115,144 B (1089.0 KiB) | 9.66 s |

> **`ChillDuanSans` 每字形体积约为 `MaokenGlitchSans` 的 1/3.4**（651 KiB vs 2142 KiB，同为字符集 B）。
> 原因是 Maoken 的故障/破碎描边产生了远多得多的 CFF 轮廓点。

**推荐的落地组合（合计 1.41 MiB）：**

| 用途 | 文件 | 字符集 | 体积 |
| :--- | :--- | :--- | ---: |
| 主 UI 字体（正文/标题/表单） | `寒蝉端黑体` → `ChillDuanSans_Black.otf` | B（src ∪ GB2312 L1） | **635.9 KiB** |
| 装饰/品牌标题（故障美学） | `猫啃网故障黑.otf` | A（src） | **800.9 KiB** |
| | | **合计** | **1.41 MiB** |

若想把装饰字体也做到 UGC 安全，把 Maoken 换成字符集 B：合计 2.71 MiB（仍可接受）。

### 2.8 渲染验证（浏览器实测，非推断）

在复刻生产 CSP 的页面里加载子集 woff2 并逐字验证：

```javascript
// 缺字检测：与不存在的字体族逐像素比对，不同 = 该字体确实渲染了这个字
window.__glyphProbe()
```

实测结果（`$env:TEMP\pr-research\mask-test\fonts.html`）：

| 子集 | 自带字体渲染 | 回退系统字体 |
| :--- | :--- | :--- |
| `MK_A`（src） | `月夜故障房间播上传下载登录设置刷新提示票据温度牌星标畸溃南辕北辙钥懂轴富辨溯彼腾居瀑` | **`盾`**、`龘齉靐` |
| `MK_B`（src+gb1） | 同上 + **`盾`** | `龘齉靐` |
| `MK_C`（src+gb12） | 同上 + `盾` | `龘齉靐` |
| `CDS_C`（寒蝉端黑体） | 同上 + `盾` | `龘齉靐` |
| `ZG_C`（零ゴシック） | 只有 `月夜故障房播上下登置刷新提示票据温度盾星畸南北富辨溯彼居瀑` | **`间 传 载 录 设 标 溃 辕 辙 钥 懂 轴 腾`** 等 16 个 |

> 注意 `MK_A` 连 `盾` 都回退了——因为 `盾` 恰好不在 `client/src` 的 1200 字里。**这正是「只用 src 抽字」的风险实例**：现有 22 个图标语义里就有「盾牌」。

`document.fonts.load` 耗时实测均为亚毫秒级（`MK_A` 0.5 ms / `MK_B` 0.8 ms / `MK_C` 0.3 ms / `CDS_C` 0.2 ms），即 woff2 解析本身不是瓶颈。

### 2.9 Node 侧备选方案（已实测，但有硬限制）

```powershell
npm install subset-font@2 --no-audit --no-fund   # 实测 9s 装好，harfbuzzjs 1.6.3
```

| 字体 | `subset-font` 结果（字符集 B，4288 字） | `pyftsubset` 同字符集 |
| :--- | :--- | ---: |
| `ChillDuanSans_Black.otf` | 863,784 B，7.8 s | **651,124 B**，6.09 s |
| `ChillDuanSans_Light.otf` | 753,448 B，4.5 s | **605,368 B**，6.00 s |
| `零ゴシック.otf` | 1,281,768 B，5.3 s | **1,157,184 B**，8.34 s |
| `猫啃网故障黑.otf` | **失败**：`hb_subset_or_fail (harfbuzz) returned zero` / `memory access out of bounds` | 2,193,472 B，35.0 s |

**`subset-font` 对 `猫啃网故障黑.otf` 在字符集 B 上必然失败**（二分定位：从 `gb1[200]` 起第 **425** 字 OK、第 **450** 字失败；单独 428 字不含 `丛` 也失败；单字 `丛` 成功）——这是 harfbuzzjs 1.6.3（WASM）的**规模/内存上限**，不是字形问题。
另：`subset-font` 产物**比 `pyftsubset` 大 33%**（保留更多表）。

**结论**：`subset-font` 只适合作为「`pip` 装不上」时的兜底，且**它跑不了主装饰字体**。首选仍是 `pyftsubset`。

---

## 3. 图标两套的兼容性（**已实测**）

> **范围说明：`E:\Resource\icon\icons\theme\` 目录为其他项目专用，本项目不使用。** 本节只覆盖
> `E:\Resource\icon\icons\`（250 个）与 `E:\Resource\icon\570+图标-v1.0.3\`（574 个）两套。

### 3.1 两套的静态画像（实测）

```powershell
python "$env:TEMP\pr-research\analyze_icons2.py"
```

| 特征 | `icons/` | `570+图标-v1.0.3/` |
| :--- | ---: | ---: |
| SVG 数量 | **250** | **574**（7 个分类） |
| SVG 总体积 | 173,004 B (**168.9 KiB**) | 1,365,086 B (**1.30 MiB**) |
| 平均单文件 | 692 B（210 B – 5,121 B） | 2,378 B（900 B – 9,137 B） |
| 固有尺寸 | **24×24**（249 个）/ 25×24（1 个） | **10×10**（574 个，全部） |
| `viewBox` | `0 0 24 24`（249）/ `0 0 25 24`（1） | `0 0 10 10`（574） |
| 描边硬编码 | **249/250 是 `stroke="#ffffff"`**（1 个无 stroke） | 7/574 是 `stroke="white"`，其余 567 无 stroke |
| 填充硬编码 | 250/250 `fill="none"`；其中 **25 个额外有 `fill="#ffffff"`** | **568/574 `fill="white"`**，6 个仅 `fill="none"` |
| `<filter>` | **0** | **574/574**（全部带 `filter="url(#…)"`） |
| `feFlood` / `feGaussianBlur` / `hard-light` | 0 | **574/574 全部具备** |
| `<mask>` / 渐变 / `<style>` / `class` / `<use>` / `<image>` | **0（完全没有）** | 0（`clipPath` 4 个） |
| `currentColor` | 0 | 0 |
| 附带位图 | 无 | **PNG 1,723 个 = 3.25 MiB**（64px 374,849 B / 128px 708,565 B / 256px 1,470,141 B，实测真实尺寸确为 64/128/256） |

### 3.2 现有 `BrandIcon.vue` 的机制（只读复核）

`client/src/components/BrandIcon.vue:34-43` 的做法：

```ts
const safeName = computed(() => (/^[A-Za-z0-9_-]+$/.test(props.name) ? props.name : 'close'))
const style = computed(() => {
  const url = `url(/icons/${safeName.value}.svg)`
  return {
    '-webkit-mask': `${url} center / contain no-repeat`,
    mask: `${url} center / contain no-repeat`,
  }
})
```

配合 `.brand-icon { background-color: currentColor }`（同文件 62-69 行）。

**关键约束（读代码即知，且与实测一致）**：
- `safeName` 白名单是 `^[A-Za-z0-9_-]+$` —— **不允许中文文件名**。因此 `570+` 的 `设置_svg.svg` 这类文件名**必须先重命名成 ASCII**（如 `setting.svg`）才能走 `BrandIcon`。
- 渲染方式是 `mask` 简写，`mask-mode` **未显式声明**，所以走 `match-source` 的默认解析。

### 3.3 决定性实测：mask 用的是 **alpha 通道**，硬编码颜色**不影响**

造了一组「同形、只换颜色」的探针 SVG，在同一页里跑三种 `mask-mode`（`$env:TEMP\pr-research\mask-test\channels.html`）：

| 探针 | 默认 mask-mode | `mask-mode:alpha` | `mask-mode:luminance` | `<img>` 原样 |
| :--- | :--- | :--- | :--- | :--- |
| `stroke="#ffffff"` | **可见** | 可见 | 可见 | 白（浅底看不见） |
| `stroke="#000000"` | **可见** | **可见** | **不可见** | 黑（深底看不见） |
| `stroke="#ff0000"` | **可见** | 可见 | 淡 | 红 |
| `stroke="currentColor"` | **可见** | 可见 | 不可见 | 黑 |
| `stroke="none"` | 无墨 | 无墨 | 无墨 | 无墨 |
| `fill="#000000"` 圆 | **可见** | **可见** | **不可见** | 黑 |
| `fill="#ffffff"` 圆 | 可见 | 可见 | 可见 | 白 |

**结论（可量化）**：

1. **默认 `mask-mode` 解析为 alpha 通道**。证明：`stroke="#000000"` 的 X 在默认与 `alpha` 下**都可见**，只有显式 `luminance` 才把它抹掉。若默认走 luminance，黑色描边会直接消失。
2. **`stroke="#ffffff"` 硬编码颜色完全不影响 mask**。遮罩只取不透明度通道，颜色由 `background-color: currentColor` 提供 —— **`BrandIcon.vue` 的注释与实测一致**。
3. 反向推论：**任何**硬编码颜色（黑/白/红/`currentColor`）在 mask 下都等效，只要该图元的不透明度 > 0。像素级证据：`sumAlpha` 对白/黑/红三种描边分别为 `6778.6 / 6780.5 / 6778.6`（几乎相同），而 `sumAlpha×亮度` 为 `6778.6 / 0 / 1441.1`（差异巨大）—— 说明 mask 读的是前者。
4. **`stroke="none"` 无墨** → 这类图元（若有）在 mask 下也不可见。实测两套里**没有**这种图标。

### 3.4 `570+` 的 `<filter>` 能不能 mask？**能，但有一处必须知情**

`570+` 每个 SVG 都带一个 `filter0_i_*`：`feFlood` → `feBlend` → `feColorMatrix` → `feGaussianBlur stdDeviation="1"` → `feComposite` → `feBlend mode="hard-light"`。这是**内阴影**效果。

做了「原样 vs 剥掉 filter」的对照实验（同形、同色、只差 filter）：

| 图标 | 覆盖率 | 平均 alpha | 柔和像素 | alpha 层级数（带 filter → 剥掉） | 逐像素 alpha 差异 |
| :--- | ---: | ---: | ---: | ---: | ---: |
| `设置` | 0.1747 → 0.1747 | 43.26 → 43.19 | 705 → 705 | **228 → 239** | 697 px（1.064%） |
| `温度` | 0.1149 → 0.1149 | 28.73 → 28.69 | 418 → 418 | 164 → 167 | 415 px（0.633%） |
| `夜晚` | 0.1721 → 0.1721 | 42.55 → 42.46 | 722 → 722 | 230 → 242 | 715 px（1.091%） |
| `分享` | 0.2183 → 0.2183 | 54.25 → 54.16 | 1046 → 1046 | 220 → 224 | 1038 px（1.584%） |
| `直播` | 0.1710 → 0.1710 | 41.36 → 41.21 | 1213 → 1213 | 243 → 250 | 1189 px（1.814%） |
| `播放` | 0.1448 → 0.1448 | 36.24 → 36.20 | 380 → 380 | 179 → 181 | 375 px（0.572%） |

**结论：`filter` 对 mask 无实质影响。**
- **覆盖率、平均 alpha、柔和像素数三者完全一致**（同一形状）；
- 差异只体现在 **alpha 层级数**（`228 → 239` 等）和 **0.6%–1.8% 的像素**上有微小 alpha 扰动；
- 视觉上：`570+` 的 filter 内阴影在 mask 下表现为**边缘极淡的 alpha 层**（`feColorMatrix` 把内阴影 alpha 压到 ~0.1），远低于可见阈值。放大对照图（`pr-mask-zoom2.png`）里带 filter 与剥掉 filter 的 `设置` 图标**肉眼无法区分**。

**但有一个必须知情的事实**：`570+` 的**设计意图是「白色填充 + 内阴影」**（依赖浅色主题背景）。
- 作为 `<img>` 在**浅色底**上：白色填充 + 浅色内阴影 → **几乎不可见**（实测截图里 `<img>` 白底一列大面积空白）。
- 走 mask：**正常可见**，且能跟随 `currentColor`。

**所以 `570+` 走 mask 反而是它在本项目（深色「月夜」主题）里唯一正确的用法。**

### 3.5 最强证据：`570+` 的 filter 图标**已经在生产里跑 mask 了**

仓库现有 22 个图标的 sha256 逐一比对 `E:\Resource`：

```
client/public/icons/ 里 20 个与 E:\Resource\icon\icons\ 逐字节相同
client/public/icons/temperature.svg  sha256 = a3b25384a0f1fe0d…  ≡  E:\Resource\icon\570+图标-v1.0.3\7.自然与饮食\温度_svg.svg
client/public/icons/夜晚.svg         sha256 = 871bd53831e6d1c2…  ≡  E:\Resource\icon\570+图标-v1.0.3\7.自然与饮食\夜晚_svg.svg
```

而 `temperature` **正是通过 `BrandIcon` 用 mask 渲染的**：

- `client/src/components/DiagnosticsDrawer.vue:403` `<BrandIcon name="temperature" decorative :size="12" />`
- `client/src/views/AdminView.vue:586` `<BrandIcon name="temperature" decorative :size="15" />`

**⇒ 带 `filter` 的 `570+` 图标经 `BrandIcon.vue` 的 mask 渲染，在本项目已实际运行。** 这不是推断，是既有生产事实。

（`夜晚.svg` 走的是 `<img>`：`AppHeader.vue:58,100`、`HelpView.vue:37` —— 因为它是 30px 的 logo，用 `<img>` 直接出白色，也不需要跟主题变色。）

### 3.6 CSP 严格模式下的实测

复刻生产默认 CSP 全文（`internal/config/config_load.go:333-342`，`DefaultSecurityCSP`）后实测：

| 场景 | 结果 |
| :--- | :--- |
| `mask: url(/icons/x.svg)`（同源绝对路径，**生产形态**） | **通过**，无违规 |
| `mask: url(svg/x.svg)`（同源相对路径） | 通过 |
| `mask: url(data:image/svg+xml;…)` | 通过 |
| `mask: url(https://example.com/x.svg)`（跨源） | **被拦**：`securitypolicyviolation` → `blockedURI=https://example.com/nope.svg`, `violatedDirective="img-src"` |
| `@font-face src: url(/fonts/x.woff2)` | **通过**，`document.fonts` 状态 `loaded`，**无违规**，`decodedBodySize=2193472` |

**结论**：
1. **`mask-image` 受 `img-src` 管辖**（不是 `style-src`）。生产默认 `img-src 'self' data:` 正好放行本地图标，**无需改 CSP**。
2. **`font-src` 未显式声明时由 `default-src 'self'` 兜底**，本地 `/fonts/*.woff2` **加载成功**。**同样无需改 CSP。**
3. **反例警告**：若把图标放成 `<img>`，跨源会被 `img-src` 拦；若将来有人把 `img-src` 收紧成 `'none'`，**mask 会一起坏掉**——`BrandIcon.vue` 对 `img-src` 有隐性依赖，值得在注释里钉住。

---

## 4. 按需复制清单（icon-first 交互规范）

### 4.1 现有 22 个图标的用途 → 新图标集对应文件

`icons/` 命中即直接可用（同名、同 24×24、同 `stroke="#ffffff"`）；`570+` 命中需**重命名为 ASCII** 后才能走 `BrandIcon`。

| # | 用途 | 现用文件 | 建议新文件 | 来源 | 备注 |
| ---: | :--- | :--- | :--- | :--- | :--- |
| 1 | 导航 | （无，icon-first 新增） | `menu.svg` | `icons/` | 另有 `sidebar.svg` / `apps.svg` 备选 |
| 2 | 房间 | （无，icon-first 新增） | `home.svg` | `icons/` | 另有 `video.svg` / `broadcast.svg` 备选 |
| 3 | 播放 | （无，icon-first 新增） | **`play.svg`** ← `2.媒体与科技/播放_svg.svg` | **`570+`** | **`icons/` 里没有播放/暂停**，必须从 `570+` 取 |
| 4 | 上传 | `upload.svg` | `upload.svg` | `icons/` | 已有，无需改动 |
| 5 | 下载 | `download.svg` | `download.svg` | `icons/` | 已有 |
| 6 | 登录 | `login.svg` | `login.svg` | `icons/` | 已有 |
| 7 | 登出 | `logout.svg` | `logout.svg` | `icons/` | 已有 |
| 8 | 复制 | `copy.svg` | `copy.svg` | `icons/` | 已有 |
| 9 | 分享 | `share.svg` | `share.svg` | `icons/` | 已有 |
| 10 | 设置 | `settings.svg` | `settings.svg` | `icons/` | 已有 |
| 11 | 告警 | `alert-triangle.svg` | `alert-triangle.svg` | `icons/` | 已有；另有 `alert-circle` / `alert-romb` |
| 12 | 用户组 | `users-group.svg` | `users-group.svg` | `icons/` | 已有；`users-two` / `add-user` / `profile` 备选 |
| 13 | 聊天 | `chat.svg` | `chat.svg` | `icons/` | 已有；`message` / `send` 备选 |
| 14 | 星标 | `star.svg` | `star.svg` | `icons/` | 已有 |
| 15 | 票据 | `ticket.svg` | `ticket.svg` | `icons/` | 已有 |
| 16 | 温度 | `temperature.svg` | `temperature.svg` | **`570+`** | **已是 `570+` 拷贝**（sha256 已证），保持原样 |
| 17 | 盾牌 | `shield-done.svg` | `shield-done.svg` | `icons/` | 已有 |
| 18 | 关闭 | `close.svg` | `close.svg` | `icons/` | 已有 |
| 19 | 刷新 | `refresh.svg` | `refresh.svg` | `icons/` | 已有；`load-spinner.svg` 用于旋转态 |
| 20 | 提示 | `tips.svg` | `tips.svg` | `icons/` | 已有；`tooltip-circle` / `tooltip-square` 备选 |
| 21 | 视频 | `video.svg` | `video.svg` | `icons/` | 已有 |
| 22 | 夜晚 | `夜晚.svg` | **`night.svg`** ← `7.自然与饮食/夜晚_svg.svg` | **`570+`** | **已是 `570+` 拷贝**，只需**改名为 ASCII** |

**两套都没有对应件的语义：无**（22 项全部有落点）。

### 4.2 「月夜故障」+ icon-first 建议追加的图标（`icons/` 里没有、需从 `570+` 取）

| 用途 | 建议文件名 | 来源（`570+` 原名） |
| :--- | :--- | :--- |
| 播放 / 暂停 | `play.svg` / `pause.svg` | `2.媒体与科技/播放_svg.svg`、`暂停_svg.svg` |
| 直播（房间态） | `live.svg` | `2.媒体与科技/直播_svg.svg` |
| 消息（与「聊天」区分：私聊 vs 群聊） | `msg.svg` | `2.媒体与科技/消息_svg.svg` |
| 信息 / 问号（提示的两种强度） | `info.svg` / `help.svg` | `1.用户界面/信息_svg.svg`、`问号_svg.svg` |
| 叉号（`570+` 的实心叉，用于关闭按钮的强调态） | `cross.svg` | `1.用户界面/叉号_svg.svg` |
| 用户（单人，与「用户组」区分） | `user.svg` | `1.用户界面/用户_svg.svg` |
| 房子（房间，与 `home` 区分的实心态） | `house.svg` | `5.游戏/房子_svg.svg` |
| 流星 / 星星（月夜故障装饰） | `meteor.svg` / `star-fill.svg` | `7.自然与饮食/流星_svg.svg`、`星星_svg.svg` |
| 盾（实心盾，审计强调态） | `shield-fill.svg` | `6.物品/盾_svg.svg` |
| 上传 / 下载（实心态，与线性版配对） | `upload-fill.svg` / `download-fill.svg` | `2.媒体与科技/上传_svg.svg`、`下载_svg.svg` |
| 分享 / 复制（实心态） | `share-fill.svg` / `copy-fill.svg` | `2.媒体与科技/分享_svg.svg`、`3.编辑工具/复制_svg.svg` |
| 设置 / 刷新（实心态） | `setting-fill.svg` / `refresh-fill.svg` | `1.用户界面/设置_svg.svg`、`刷新_svg.svg` |
| 视频（实心态） | `video-fill.svg` | `2.媒体与科技/视频_svg.svg` |

### 4.3 品牌 / logo

**本项目不提供专用品牌图标。** `E:\Resource\icon\icons\theme\` 目录为其他项目专用，本项目不使用（该目录下 12 个文件一律不取用、不列入任何清单）。

现有做法是**用 `570+` 的 `夜晚.svg` 当 logo**（`AppHeader.vue:58,100`、`HelpView.vue:37`，走 `<img>`，30px/22px/34px）。**月夜故障方向下建议沿用这个做法**，只把文件名改成 ASCII（`night.svg`），继续以 `<img>` 渲染（logo 不需要跟随主题变色，且 `<img>` 能直接出白色）。

若要一个「更品牌化」的符号，两套里的可选替代（**均非品牌专用，属通用图形**）：
- `icons/prime.svg`（1,521 B，菱形/宝石状）—— 最接近「徽记」气质
- `icons/layers.svg`（960 B）
- `icons/gem.svg`（980 B）
- `570+ 6.物品/水晶球_svg.svg` → `crystal.svg`（月夜/占卜意象，与「月夜故障」调性接近）

> 实测：这 4 个候选都走 mask 正常渲染（16/20/24/32px 全部可辨识，见 `pr-final-map.png`）。

### 4.4 复制清单（可直接执行）

```powershell
# 全部为只读复制；目标目录需先创建
$DST = 'client\public\icons'
$SRC1 = 'E:\Resource\icon\icons'
$SRC2 = 'E:\Resource\icon\570+图标-v1.0.3'

# --- 组 1：icons/ 线性集（21 个功能图标，ASCII 名，可直接走 BrandIcon） ---
$linear = 'menu','home','video','upload','download','login','logout','copy','share',
          'settings','alert-triangle','users-group','chat','star','ticket','shield-done',
          'close','refresh','tips','load-spinner','github'
foreach ($n in $linear) { Copy-Item "$SRC1\$n.svg" "$DST\$n.svg" -Force }

# --- 组 1b：品牌/logo 候选（2 个，见 §4.3；不需要可跳过） ---
foreach ($n in 'prime','layers') { Copy-Item "$SRC1\$n.svg" "$DST\$n.svg" -Force }

# --- 组 2：570+ 填充集（需重命名为 ASCII；源名含中文） ---
$filled = @(
  @{ src='2.媒体与科技\播放_svg.svg';     dst='play.svg' },
  @{ src='2.媒体与科技\暂停_svg.svg';     dst='pause.svg' },
  @{ src='2.媒体与科技\直播_svg.svg';     dst='live.svg' },
  @{ src='2.媒体与科技\消息_svg.svg';     dst='msg.svg' },
  @{ src='2.媒体与科技\上传_svg.svg';     dst='upload-fill.svg' },
  @{ src='2.媒体与科技\下载_svg.svg';     dst='download-fill.svg' },
  @{ src='2.媒体与科技\分享_svg.svg';     dst='share-fill.svg' },
  @{ src='2.媒体与科技\视频_svg.svg';     dst='video-fill.svg' },
  @{ src='1.用户界面\信息_svg.svg';       dst='info.svg' },
  @{ src='1.用户界面\问号_svg.svg';       dst='help.svg' },
  @{ src='1.用户界面\叉号_svg.svg';       dst='cross.svg' },
  @{ src='1.用户界面\用户_svg.svg';       dst='user.svg' },
  @{ src='1.用户界面\设置_svg.svg';       dst='setting-fill.svg' },
  @{ src='1.用户界面\刷新_svg.svg';       dst='refresh-fill.svg' },
  @{ src='3.编辑工具\复制_svg.svg';       dst='copy-fill.svg' },
  @{ src='5.游戏\房子_svg.svg';           dst='house.svg' },
  @{ src='6.物品\盾_svg.svg';             dst='shield-fill.svg' },
  @{ src='7.自然与饮食\夜晚_svg.svg';     dst='night.svg' },
  @{ src='7.自然与饮食\流星_svg.svg';     dst='meteor.svg' },
  @{ src='7.自然与饮食\星星_svg.svg';     dst='star-fill.svg' },
  @{ src='7.自然与饮食\温度_svg.svg';     dst='temperature.svg' }
)
foreach ($m in $filled) { Copy-Item "$SRC2\$($m.src)" "$DST\$($m.dst)" -Force }
```

**注意**：`temperature.svg` 与 `night.svg` 会**覆盖**现有文件，但实测**内容等价**（现有 `temperature.svg`/`夜晚.svg` 就是这两个 `570+` 文件的逐字节拷贝），覆盖后 `git diff` 只体现「`夜晚.svg` → `night.svg`」这一次重命名。

**不要复制** `570+` 的 1,723 个 PNG（3.25 MiB）——mask 机制用不上位图，且会增加静态根体积。

---

## 5. 体积与性能

### 5.1 总体积实测

| 类别 | 文件数 | raw | gzip 后 |
| :--- | ---: | ---: | ---: |
| `icons/` 子集（21 功能图标 + 2 品牌候选） | 23 | 21,010 B (**20.5 KiB**) | 9,098 B (8.9 KiB) |
| `570+` 子集（补 `icons/` 缺口，含 logo/温度） | 21 | 41,580 B (**40.6 KiB**) | 20,699 B (20.2 KiB) |
| **图标合计** | **44** | **62,590 B (61.1 KiB)** | 29,797 B (**29.1 KiB**) |
| **字体合计**（ChillDuanSans_Black B + MaokenGlitchSans A） | 2 | **1,476,216 B (1.41 MiB)** | 同（woff2 已是终态压缩） |
| **总计** | **46** | **≈ 1.47 MiB** | |

**参照**：现有 `client/public/icons/` 22 个文件 = 21,387 B (20.9 KiB)。新方案图标部分只增长 **40.2 KiB**，换来 22 个功能的实心/线性两套状态。

> `570+` 全量 SVG 是 1.30 MiB、全量 PNG 是 3.25 MiB —— **只取 21 个 SVG（40.6 KiB）就够**，无需带入位图。

### 5.2 性能要点（实测）

1. **woff2 已是终态压缩**（§2.6）：不要套 gzip/brotli，否则产物**变大**。
2. **`document.fonts.load` 亚毫秒级**（0.1–0.8 ms）：woff2 解码不是瓶颈。
3. **mask 的代价是一次 SVG 请求**：44 个图标 = 44 次请求（或走 HTTP/2 多路复用）。若在意请求数，可把图标改成**内联 data URI**——实测 `mask: url(data:image/svg+xml;…)` **在 CSP 下通过**（`img-src 'self' data:` 已放行）。但 `BrandIcon.vue` 当前是 `url(/icons/<name>.svg)` 的**动态拼接**，改内联需要改组件（**本报告不改源码**，仅记录可行路径）。
4. **`570+` 的 `filter` 有渲染成本**（每个图标一次 `feGaussianBlur`），但 574 个全量里只取 21 个，且 mask 只取 alpha，实测无可见退化（§3.4）。
5. **不要用 `570+` 的 256px PNG**（1,470,141 B / 574 = 平均 2,561 B/个）：mask 用不上，`<img>` 场景 24px 显示也浪费。

### 5.3 CSP 严格模式下的本地托管路径约定（实测）

生产默认 CSP（`internal/config/config_load.go:333-342`）：

```
default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline';
connect-src 'self' ws: wss:; img-src 'self' data:; media-src 'self' blob:;
worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'
```

| 资源 | 落点 | 引用方式 | CSP 判据 | 实测 |
| :--- | :--- | :--- | :--- | :--- |
| 图标 | `client/public/icons/<ascii-name>.svg` | `mask: url(/icons/<name>.svg)` | **`img-src 'self'`** | ✅ 通过 |
| 字体 | `client/public/fonts/<name>.woff2` | `@font-face { src: url(/fonts/<name>.woff2) }` | **`default-src 'self'`**（`font-src` 未声明） | ✅ 通过，无违规 |

**约定**：
1. 两者都放在 **`client/public/`** 下，构建后原样进 `client/dist/`，由 Go 静态托管（`internal/handler/static.go`），**同源**，不触发 CSP。
2. **不要改 CSP**。`img-src 'self' data:` 与 `default-src 'self'` 已覆盖两种资源。
3. **隐含依赖（建议在代码注释里钉住）**：`BrandIcon.vue` 的 mask **依赖 `img-src 'self'`**。若将来把 `img-src` 收紧到 `'none'`，图标会整体消失，而**字体不受影响**（走 `default-src`）。
4. `client/scripts/prune-dist.mjs` **只清理 `dist/assets/` 下未被 `index.html` 引用的文件**（实测读源码确认：只遍历 `assets/`，不碰 `icons/`、`fonts/`、`downloads/`）。所以新目录**不会被误删**。
5. 与 `client/public/hero/README.md` 的既有约定一致：**只放本地素材，本项目不引外链资源**。

---

## 6. 可直接执行的结论

### 6.1 字体：3 步落地

```powershell
# 步骤 0：安装工具链（清华源在本机不可达，必须走 pypi.org）
python -m pip install --proxy http://127.0.0.1:7890 `
  --index-url https://pypi.org/simple --no-cache-dir fonttools brotli

# 步骤 1：生成字符集（src 汉字 ∪ GB2312 一级字库）
python "$env:TEMP\pr-research\extract_charset.py"     # 产出 src 用字 1222 个
#   再并入 GB2312 一级 3755 字 → 4288 码点（实测体积见 §2.7）

# 步骤 2：主 UI 字体（寒蝉端黑体 Black，635.9 KiB）
& "$env:APPDATA\Python\Python313\Scripts\pyftsubset.exe" `
  "E:\Resource\font\寒蝉端黑体.zip" `
  --text-file=$CHARS --output-file=client\public\fonts\chillduan-sans-black.woff2 `
  --flavor=woff2 --layout-features=kern,liga,clig,calt,ccmp,locl,mark,mkmk,vert,vrt2,palt,halt,vhal,frac,subs,sups,tnum,pnum,onum,lnum,zero `
  --name-IDs=0,1,2,3,4,5,6,13,14 --name-languages=0x409,0x804 `
  --notdef-outline --recalc-bounds --no-hinting --drop-tables+=DSIG,vhea,vmtx,VORG,BASE

# 步骤 3：装饰字体（猫啃网故障黑，800.9 KiB @ 字符集 A）
#   注意：源文件要先从 zip 解出，或用磁盘上现成的 E:\Resource\font\猫啃网故障黑.otf
```

> `pyftsubset` **不能直接吃 zip**：`寒蝉端黑体.zip` 里的 `ChillDuanSans_Black.otf` 必须先解压（实测用 `System.IO.Compression.ZipFile` 解出 10,520,076 B）。

**字体清单（最终建议）：**

| 落点 | 源 | 字符集 | 体积 |
| :--- | :--- | :--- | ---: |
| `client/public/fonts/chillduan-sans-black.woff2` | `寒蝉端黑体.zip` → `ChillDuanSans_Black.otf` | B（4288 码点） | 651,124 B |
| `client/public/fonts/maoken-glitch-sans.woff2` | `E:\Resource\font\猫啃网故障黑.otf` | A（1723 码点） | 820,092 B |
| （可选）`chillduan-sans-light.woff2` | `ChillDuanSans_Light.otf` | B | 605,368 B |

**不使用**：`零ゴシック.otf`（覆盖 66.25%、`fsType=8`、无 OFL）；`寒蝉端黑体` 的其余 25 个字重（Compact/Condensed/Expanded/Wide 变体，本项目用不到）。

### 6.2 图标：2 步落地

1. 按 §4.4 的脚本复制 **44 个 SVG**（61.1 KiB）到 `client/public/icons/`。
2. **重命名规则**：`570+` 的中文文件名 → ASCII kebab-case（因为 `BrandIcon.vue:34` 的 `safeName` 白名单是 `^[A-Za-z0-9_-]+$`）。

**渲染方式：全部走现有 `BrandIcon.vue` 的 CSS mask，无需改渲染方式、无需改 SVG、无需改 CSP。**

### 6.3 硬阻塞

**无。** 全部方案在本机实测通过：

- 字体：4 个候选全部成功子集化为 woff2（`pyftsubset` 全绿）；
- 图标：44 个目标文件全部复制成功、mask 渲染在 16/20/24/32px 全部可辨识；
- CSP：生产默认策略下 mask 与 `@font-face` **均无违规**；
- 构建路径：`prune-dist.mjs` 不碰 `icons/` 与 `fonts/`。

**唯一的已知风险（非阻塞，但需知情）**：字符集 `A`（仅 `src` 抽字）会漏掉运行期用户产生的中文。已在 §2.2 与 §2.8 用实测数据指出（`盾` 字就会回退）。**因此主 UI 字体用字符集 B。**

---

## 7. 复现清单

所有实验脚本与中间产物在 `$env:TEMP\pr-research\`：

| 脚本 / 产物 | 作用 |
| :--- | :--- |
| `inspect_fonts.py` | 字体容器格式、`name` 表、cmap 覆盖 |
| `coverage.py` | 各字体对 `src` 用字 / GB2312 的覆盖率 |
| `zip_audit.py` | zip 冗余量化、`fsType` 与 OFL 审计 |
| `top10_diff.py` | 三个 zip 里 TOP10 捆绑包的逐条 sha256 比对 |
| `extract_charset.py` | 从 `client/src` 抽汉字与标点 |
| `subset_fonts.py` / `subset2.py` / `final_recipe.py` | 三轮 `pyftsubset` 实测 |
| `verify_subset.py` | 子集产物复开验证 + gzip/brotli 对照 |
| `analyze_icons2.py` | 两套图标的静态画像（`theme/` 已排除） |
| `mapping.py` | 22 个用途 → 两套图标的候选解析 |
| `build_mask_test.py` / `build_channel_test.py` / `build_final_test.py` | 三个浏览器实测页的生成器 |
| `mask-test\channels.html` | **mask 通道定论**（alpha vs luminance） |
| `mask-test\final-color.html` | 硬编码色 vs mask 的浅底/深底对照 |
| `mask-test\csp.html` | **CSP 与 `mask-image`** 实测 |
| `mask-test\csp-font.html` | **CSP 与本地 woff2** 实测 |
| `mask-test\fonts.html` | 子集字体渲染 + 缺字回退检测 |
| `final-test\index.html` | 44 个映射图标 × 16/20/24/32px mask 实测 |
| `subset2\*.woff2`、`final\*.woff2`、`publish\*.woff2` | 全部子集产物 |

**证据截图**（在 `%USERPROFILE%\Downloads\`）：
`pr-mask-channels2.png`（mask 通道）、`pr-color-final.png`（硬编码色对照）、
`pr-final-map.png` / `pr-final-map2.png`（44 图标映射实测）、`pr-icons-small.png`（小尺寸可辨识度）、
`pr-fonts-render.png`（子集字体渲染）、`pr-csp.png`（CSP 实测）。

---

## 8. 边界与未做的事

- **未修改任何源码**：`client/src/**`、`internal/**`、`test/**`、`scripts/**` 全程只读；本文件是唯一写入仓库的文件。
- **未做任何 git 操作**（无 commit / branch / push / stash）。
- **未验证真实构建**：`npm --prefix client run build`（`vue-tsc --noEmit && vite build && node scripts/prune-dist.mjs`）**未运行**——本报告只做素材取证，落地构建属于实施票的范围。
- **未做真机浏览器兼容矩阵**：mask 实测在单一 Chromium 内核（DSH 内置浏览器）完成。`-webkit-mask` 前缀在 `BrandIcon.vue` 里已同时给出，但 Safari/Firefox 未实测。
- **`570+` 的 1723 个 PNG 未做逐张内容校验**：只实测了目录体积、分档体积与抽样真实像素尺寸（64×64 / 256×256 各一例）。
- **`寒蝉端黑体.zip` 的其余 25 个字重未逐一子集化**：只测了 `Black` 与 `Light`（本项目只用得上这两个）。
- **字体子集的排版质量未做视觉走查**：只验证了「字形存在 + 能渲染 + 不缺字」，未逐字比对子集前后的字形轮廓一致性。
