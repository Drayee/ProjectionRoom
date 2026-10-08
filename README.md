# ProjectionRoom

像 B 站放映室一样的「一起看」房间：**主播播放本地视频，观众同步观看并聊天**。

服务器只做**信令交换、房间状态与拓扑管理**——它不传输任何视频字节。视频分片由主播通过
WebRTC DataChannel 直接分发给其他节点（P2P 树状分发 + 多父条带化）。

设计与取舍的完整依据见 [`docs/SPEC.md`](docs/SPEC.md)；实现中任何偏离 SPEC 的地方都应先改那里。

**算法说明**（同步算法 + 数据链路，含时序图与已知偏差）见 [`docs/ALGORITHM.md`](docs/ALGORITHM.md)。

---

## 当前进度

| 里程碑 | 内容 | 状态 |
| :--- | :--- | :--- |
| M1 | 房间 / 信令 / 聊天 / 成员列表 / 房主控制 | ✅ 已验证 |
| **M2** | **分片工具 + WebRTC 分发 + MediaSource 播放 + 播放同步** | ✅ **已验证（真实浏览器实测）** |
| **M3** | **多层树 + 单链分发模式 + 多父条带化 + 抗慢节点** | **实现中**（A/B 已真机验证；C/D 待验证） |
| M4 | 稳健性、监控面板、STUN 服务端探测与 TTL 下发（TURN 已彻底退役） | 部分实现（ICE 部分已落地） |

### 启动门控（加载不设超时）

新节点接入或跳转后**不立即播放**，而是进入"加载中"：缓冲达到 4 片 / 8 秒、
且时钟偏移估计已收敛（样本 ≥ 6 且最小值 0.8s 内不再下降）才开闸。
**门控不设超时上限**：上游没数据就一直等，等待超过 20s 时提示"上游带宽可能不足"。
这修掉了上一轮"起播后 0.2–1.4s 偏差尖峰"的根因（薄缓冲硬播 → 立刻触发大幅矫正）。

### M3 已落地（已验证）

- **`internal/usecase`**：拓扑分配引擎。先按深度择父（树越浅越好）、同深度再比余量/RTT/稳定性；
  单链模式按实测上行选举分发节点，并带 1.5 倍换防滞回（避免两个差不多的节点来回抢位）；
  排布容量与**准入容量**分开算 —— 未实测的节点只能参与排布，不能凭猜测开闸。
- **逐跳时钟中继**：中继转发进度时改写 `parentClockMs`（自己的时钟）与 `parentOffsetMs`（自己到主播的偏移），
  子节点把偏移做成"本跳 + 父节点"**两级相加**。修复了"多跳叶子的偏差 = 整条路径最小单向延迟"
  这个结构性偏差：实测深度 1/2/3 的偏差 max 分别为 **78 / 58 / 74 ms**，
  而修复前深度 2 稳定滞后 ~350ms。
  两条配套规则是必需的：**只有主父的进度算权威**（备用父与换防前遗留的直连会送来另一套口径的样本），
  **换父即作废本跳样本**（它们是相对旧父时钟测的，留着会每层恒定偏掉一个时钟原点差）。
- **自适应条带化**：只向"明确声明拥有该片"的父节点取数；主父积压时才分流给备用父；
  失败即转投下一个父节点。实测：把某个中继的上行压到 15 kbps 持续 100s，
  其子节点靠备用父补齐，全程没有卡顿。
- **门控与卡顿策略**（SPEC §7.3、§7.6）：起播/换父后按"主播时间戳所在分片 + 连续 n 片"开闸（n=4 / 重灌 n=2，无超时）；
  卡顿上报 `stallCount` → 服务端把当前主父标为避开并立刻重算路径（3s 限流）→ 新路径到位后重新开闸 →
  分级加速追赶（上限 +25%）→ 滞后 >12s 提示并跳到主播进度 → 目标分片 2.5s 取不到就改按主播当前进度取片。
- **真机验证**（`test/script/verify-m3.mjs`，4 个独立 Chrome，链式树 host→1→2→3）：
  - 验收 A：深度 `[0,1,2,3]`，一级节点真的在给下级供片，全员播放；
  - 验收 B：注入主播上行后进入**单链模式**，主播只有 1 个子节点（分发节点），其余全挂在它下面；
  - 验收 C：中继时钟两级相加自洽（`offset = hop + parent`），深度 ≥2 的节点确实收到带中继戳的样本；
  - 验收 D：开闸时刻的连续分片数 ≥ 阈值（27/4、22/2、31/4 片）。

### 服务端切片服务（M4）

没有 ffmpeg 的用户可以把源视频**上传到服务器**做一次性切片，再一次性下载回本地；
直播链路上的视频字节仍然只走 P2P，服务器不在分发路径上。

```bash
# 上传并切片（multipart 字段名固定为 file）
curl -F "file=@movie.mp4" http://127.0.0.1:8080/api/v1/segment/jobs
# → {"jobId":"...","state":"queued","queuePosition":0}

# 查状态；done 之后 result 里带分片数与交付形态
curl http://127.0.0.1:8080/api/v1/segment/jobs/<jobId>
# ≤1GiB：单次流式 zip；>1GiB：JSON manifest（含 parts 列表）
curl -o out.zip http://127.0.0.1:8080/api/v1/segment/jobs/<jobId>/result
curl -o part1.zip http://127.0.0.1:8080/api/v1/segment/jobs/<jobId>/parts/1
```

配额：并发 2、排队 8、令牌桶 3 作业/分钟（超限 429 + `Retry-After`）、单作业 ≤60 分钟、源文件 ≤16GiB、
产物保留 30 分钟（TTL 清理）。全部可用 `PR_SEGMENT_*` 覆盖，ffmpeg 路径用 `PR_FFMPEG`。
不想上传也行：`docs/SEGMENT.md` 里有本地 ffmpeg / `cmd/segmenter` 的完整教程。

主播页有对应入口：选片区下方的「**本机没有 ffmpeg？交给服务器切片**」展开后可上传视频、
看排队位次与切片进度，切完可以下载 zip，或者直接**写进一个本地目录并立即开播**
（`showDirectoryPicker()` + File System Access API；浏览器不支持时退化为"下载 zip 手动解压后再选目录"）。
服务器连不上或没装 ffmpeg 时，面板会直接说明原因，内嵌的本地切片教程始终可用。

主播页还能直接**下载切片工具（segmenter）+ 照命令行用法自己切**：面板从
`GET /api/downloads/segmenter` 拿清单，按当前平台高亮推荐那一行，给出体积、sha256
（可展开/复制）与**下载按钮**，并附校验命令（Windows 用 `Get-FileHash`，Linux / macOS 用
`shasum -a 256`）与用法：

```bash
# 三种用法完全等价（都不需要额外装什么）：
segmenter -in <视频文件> -out <输出目录>
segmenter "<视频文件>" -out <输出目录>   # 把视频拖到 exe 上就是这个形状（唯一的位置参数）
segmenter -out <输出目录>                # 双击 exe 后按提示粘贴路径也一样
```

**本机没有 ffmpeg 也没关系**：exe 会按 `-ffmpeg-dir` → `PATH` → exe 同级目录
（`./ffmpeg/bin/`、`./ffmpeg/`、`./bin/`）依次找；都没有就打印醒目警告 + **5 秒倒计时**
（按 Ctrl+C 取消，`-yes` 直接跳过），然后用 net/http 自动下载一份解压到 **exe 同级目录**
（Windows / macOS 取 `.zip`，用标准库解；Linux 取 `.tar.xz`，交给系统 `tar -xJf`），再找一次。
下载地址可用 `-ffmpeg-url` 或环境变量 `PR_FFMPEG_URL` 覆盖；已经有 ffmpeg 的用户用
`-ffmpeg-dir` 指过去即可（旧名 `-ffmpeg` 仍然可用）。**下载或解压失败不会静默**：
会打印手动安装指引。

> 注意：CLI 只在上面这几处 + 自动下载目录里找，**不会去扫系统里其它安装位置**
> （服务端切片用的 `PR_FFMPEG` 发现顺序仍会扫 Windows 常见安装目录）。
> ffmpeg 装在别处（例如 `D:\Program Files (x86)\ffmpeg-…\bin`）时，用 `-ffmpeg-dir` 指过去，
> 或把该目录加进 `PATH`。

**要不要转码它自己判断**：先用 ffprobe 探测，视频 ∈ {h264, av1, vp9} 且音频 ∈ {aac, opus, 无}
→ 只做 `-c copy` 无损重新封装；否则自动转码成 H.264/AAC（`libx264 veryfast crf 23` +
`aac 128k`）。直通 AV1/VP9 时会明确提示"只有在支持它的浏览器里能播（Safari、部分 Firefox
不行）；要最大兼容请加 `-transcode 1200k`"。

> 已知限制：本机切片器（`internal/service/mp4`）只认 avc1/avc3/av01 + mp4a/Opus 的采样格式，
> 所以 **VP9（vp09）直通会在切片这一步被拒绝** —— 此时工具会明确提示"请加 `-transcode 1200k`
> 重跑"，而不是静默产出一个播不了的目录。

| 参数 | 默认 | 说明 |
| :--- | :--- | :--- |
| `-in` | 空 | 输入视频；不给时按「位置参数 → 常见目录按文件名找 → 交互式询问」的顺序找 |
| `-name` | 空 | 想找的文件名；不给时用 `-out` 的目录名（与旧一键脚本一致） |
| `-search-by-name` | `true` | 是否在 桌面/下载/视频/文档/当前目录/exe 同级目录 里按文件名找 |
| `-out` | `./room-media` | 输出（分片）目录 |
| `-transcode` | 空 | 强制转码到该视频码率（如 `1200k`）；优先于自动判定 |
| `-fragment` | `false` | 强制无损重新封装（`-c copy`）；优先于自动判定 |
| `-frag-sec` | `2` | 分片目标时长（秒） |
| `-pack` | `100` | 每 N 片合成一个 `pack-*.bin`；`1` = 不打包 |
| `-uplink-mbps` | `12` | 主播上行估计（Mbps），仅用于打印容量提示 |
| `-ffmpeg-dir` | 空 | ffmpeg/ffprobe 所在目录或可执行文件（别名 `-ffmpeg`） |
| `-ffmpeg-url` | 按平台 | ffmpeg 下载地址（也认环境变量 `PR_FFMPEG_URL`） |
| `-yes` | `false` | 跳过自动下载前的 5 秒倒计时（仍然会下载） |

切完回主播页「选择分片目录」选中输出目录即可开播。工具走的是「**单条复用 fMP4 + 按 moof
切分**」，产物格式与服务端切片完全一致，**不需要用户自己拼 index.json**，也不会再出现
"丢音轨 / 分片互相覆盖"的坏切片；结束时打印分片数/文件数/体积/码率 + K0 容量提示
（**K0 是能带几个直连子节点，不是能带几个人**）+ 下一步。二进制由
`scripts/build-segmenter.ps1` 交叉编译到 `client/dist/downloads/`，下载目录可用
`PR_DOWNLOADS_DIR` 覆盖。

> Go 版不再做"引号 / `$` 符号校验"：参数是用 exec 数组直接传给 ffmpeg 的，不经过 shell，
> 不再有旧 `.ps1` 那种"路径里有引号就会被打断"的问题。

> ⚠️ **不要把 `-out` 指到 `client/dist` 里面**（包括 `client/dist/room-media`）：`client/dist` 是
> 静态托管根，产物会被当成静态资源公开发布，`/downloads/room-media/...` 任何人可下 ——
> 直播分片就绕过了 WebRTC 直连链路。输出到仓库外的目录，或至少放在 `client/dist` 之外的路径。

```bash
# 界面验收：真实 Chrome 里展开入口、确认面板/探测徽标/教程/切片工具清单，并留一张截图
node test/script/verify-segment-ui.mjs
```

### M2 已交付

- **`cmd/segmenter`**：把本地视频切成 `init.mp4` + `pack-*.bin`（或 `c00001.m4s…`）+
  `index.json`。**只下载一个 exe 就能用**：自动找输入（`-in` / 位置参数=拖拽 / 常见目录按
  文件名找 / 交互式询问）、自动找 ffmpeg（`-ffmpeg-dir` → `PATH` → exe 同级目录 →
  5 秒倒计时后自动下载）、自动判定直通或转码（`-fragment` / `-transcode 1200k` 可强制），
  并直接打印"这个码率下主播能带几个直连子节点"。
- **切片正确性的判据**：init + 全部分片按序拼接必须与原文件**逐字节相同**，
  且每个分片都以 `moof` 开头。按固定字节数切分会产生"半个 moof"，浏览器会直接抛错。
- **前端播放链路**：`useMediaIndex`（选片 + `isTypeSupported` 硬校验）→
  `useWebRTC`（星形直连 + 预协商 DataChannel + `getStats` 上行估算）→
  `useChunkRequester`（二进制帧 + 超时）→ `useChunkPlayer`（MediaSource）→
  `useSyncClock`（NTP 最小滤波 + 三级漂移矫正）。
- **容量闸门**：拿到实测上行前 `mode=pending`；之后按 `1+K0` 拒绝超额观众，且只拦新加入、不踢人。

### 实测数据（真实 Chrome，两个独立浏览器实例）

| 指标 | 实测 | SPEC 目标 |
| :--- | :--- | :--- |
| 观众起播耗时 | **0.4s** | 1–3s |
| 同步偏差 max / p95 | **190ms / 130ms** | < 500ms |
| 房主暂停 → 观众跟随 | 是 | 是 |
| 房主跳转 → 观众跟随 | 跳转后位置一致（偏差 0s） | 跟随 |
| 分片交付 | 74 次，0 超时，0 失败 | — |

复现方式见下方「验收」。

---

## 快速开始

前置：Go 1.26+、Node 20+；`ffmpeg` **可选**（本地切片工具会自己下载一份，服务端切片才需要它）。

```bash
# 终端 1：服务端（默认 127.0.0.1:8080）
go run ./cmd

# 终端 2：前端（默认 127.0.0.1:5173，/api 与 /ws 自动代理到服务端）
cd client && npm install && npm run dev
```

浏览器打开 http://127.0.0.1:5173 ，一个窗口「创建房间」当主播，另一个窗口用房间码「加入房间」。

### 主播准备视频

```bash
# 一条命令搞定：探测编码后自动决定（能直通就只重新封装，不能就自动转码）
go run ./cmd/segmenter -in movie.mp4 -out ./room-media

# 不想给 -in：把视频拖到 exe 上，或按文件名在常见目录里找
go run ./cmd/segmenter "movie.mp4" -out ./room-media
go run ./cmd/segmenter -name movie.mp4 -out ./room-media

# 上行不足时的低码率预设（重新编码，耗时随片长增长）
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -transcode 1200k
```

然后在主播控制台点「选择分片目录」，选中 `room-media/` 即可开播。

`segmenter` 会打印探测结果、处理依据、产物摘要与容量提示，例如：

```
探测: 时长 801.3 秒（13.4 分钟）  视频=h264  音频=aac
判定: 视频 h264 与音频 aac 都在直通集合内 → 直通：只做 -c copy 重新封装，不重新编码
...
容量提示（按主播上行 12.0 Mbps 估算，K0 = floor(上行 × 0.8 ÷ 码率)，上限 8）:
  K0 = 8
  注意：K0 是主播能直接带几个子节点，不是能带几个人；其余成员挂在这些直连节点下面。
  扇出模式：主播可直接服务 8 个一级节点，其余成员挂到它们下面。

接下来：在主播页点「选择分片目录」选中 ./room-media 即可开播。
```

### 配置

服务端全部配置都有面向本机开发的默认值，可用环境变量覆盖：

| 变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `PR_ADDR` | `127.0.0.1:8080` | 监听地址 |
| `PR_MAX_MEMBERS` | `16` | 房间成员硬上限（真实上限由实测上行算出的 `1+K0` 决定） |
| `PR_DEFAULT_STREAM_BPS` | `2000000` | 尚未拿到 mediaIndex 时的码率估计 |
| `PR_STUN_URLS` | 7 条（见下表） | 逗号分隔的 STUN 候选列表，整体覆盖默认值 |
| `PR_ICE_MAX_STUN` | `5` | 单次下发给浏览器的 STUN 条数上限（= 恒选 4 席 + 1 个轮询席位） |
| `PR_ICE_PROBE_INTERVAL` | `60s` | 服务端重新探测一轮 STUN 的周期（上限 `1h`） |
| `PR_ICE_TTL` | `300s` | `/api/ice` 与建房间响应里 ICE 载荷的有效期（允许范围 `(0, 1h]`） |
| `PR_STATIC_DIR` | `client/dist` | 前端构建产物目录（相对**进程工作目录**解析，请在仓库根目录启动） |
| `PR_SERVE_STATIC` | `true` | 是否由 Go 服务端托管前端页面（单端口部署的总开关） |

### 账号层（ACCOUNTS 一期）

账号能力**默认关闭**：不配 `PR_DB_DSN` 时服务端不连库、不注册 `/api/auth/*` 与 `/api/admin/*`，
观众仍可按房间码进房 —— 这是刻意的**两步部署**路径（先上代码、再开账号）。

| 环境变量 | 默认 | 说明 |
| :--- | :--- | :--- |
| `PR_DB_DSN` | 空（账号关闭） | PostgreSQL 连接串；非空即账号能力总开关（同时触发启动期幂等迁移） |
| `PR_JWT_SECRET` | 空 | access token 的 HS256 密钥，**至少 32 字节**（`openssl rand -hex 32`）。配了 `PR_DB_DSN` 却缺它 → **拒绝启动**：缺密钥的症状是"所有已登录用户随机掉线"，看起来像网络故障，极难归因 |
| `PR_ACCESS_TTL` | `15m` | access token 有效期（上限 `24h`）。它只存内存 + `sessionStorage`，**不进 localStorage** |
| `PR_REFRESH_TTL` | `720h` | refresh 会话有效期（`1h`~`1年`）。refresh 是 HttpOnly Cookie 里的不透明串，库里只存 sha256，每次刷新轮换且检测重放 |
| `PR_BCRYPT_COST` | `12` | 口令哈希代价（`10`~`14`）；本机实测 12 ≈ 0.57s/次 |
| `PR_AUTH_HASH_CONCURRENCY` | `4` | 并发 bcrypt 闸门容量（`1`~`64`）。拿不到闸门**立刻返回 429 而不是排队**：排队会让响应时间无上界，且队列本身成为第二个被打爆的资源 |
| `PR_WS_TICKET_TTL` | `30s` | WebSocket 一次性票据有效期（上限 `10m`）。浏览器 WebSocket 无法自定义请求头，所以账号身份走票（而不是把 token 塞进 URL） |
| `PR_TRUSTED_PROXIES` | `127.0.0.1,::1` | 允许其 `X-Forwarded-For` 被采信的来源；显式设为**空** = 谁都不信（服务端直接对外的形态）。默认只信任本机反代 —— 否则任何人加一行伪造头就能把每 IP 限速全部绕过 |
| `PR_AUTH_LOGIN_PER_MINUTE` / `_BURST` | `10` / `5` | 登录尝试的每 IP 令牌桶（失败也计入，挡撞库） |
| `PR_AUTH_REGISTER_PER_MINUTE` / `_BURST` | `5` / `3` | 注册的每 IP 令牌桶（同样跑 bcrypt，所以更严） |
| `PR_AUTH_REFRESH_PER_MINUTE` / `_BURST` | `30` / `10` | 刷新的每 IP 令牌桶（多标签页会叠加，故比登录宽松） |

**无 TLS 的已知缺口**：当前部署是明文 http，因此 refresh Cookie **不带 `Secure`**（有一条测试断言
把它钉住：上 TLS 之前不该加，否则浏览器会直接丢弃它）。上 TLS 后应立即补 `Secure` 并收紧 `SameSite`。

默认 STUN 列表（顺序即默认优先级，2026-02 本机实测 UDP Binding Request 往返）：

| STUN | 实测 RTT | 说明 |
| :--- | :--- | :--- |
| `stun:stun.douyucdn.cn:18000` | 11ms | |
| `stun:stun.hitv.com:3478` | 32ms | |
| `stun:stun.chat.bilibili.com:3478` | 46ms | |
| `stun:stun.miwifi.com:3478` | 119ms | |
| `stun:stun.cloudflare.com:3478` | 203ms | **粘性**：有 AAAA，负责 IPv6 srflx 候选 |
| `stun:stun.l.google.com:19302` | 236ms ↔ 4s 超时 | **粘性**：有 AAAA；时好时坏 |
| `stun:stun.qq.com:3478` | 4s 无响应 | 留在列表里由探测打分自然沉底/落选 |

服务端每 `PR_ICE_PROBE_INTERVAL` 探一轮，按 RTT 与跨窗口成功率打分，然后按**延迟优先**选入：
**实测最快的 2 条 + 本轮健康的粘性 2 条恒选**，剩下 `PR_ICE_MAX_STUN - 4 = 1` 个席位
在次优候选里加权轮询（所以默认上限是 5；4 会让轮询剩 0 席）。算法与公式见
[`docs/ALGORITHM.md` §1.6](docs/ALGORITHM.md)。`/api/ice` 与 `POST /api/rooms`
的响应形状如下，`iceServers` 与退役 TURN 之前完全一致（只做加法）：

```json
{
  "iceServers": [{"urls": "stun:stun.douyucdn.cn:18000"}],
  "ttlSeconds": 300,
  "expiresAt": 1760000000,
  "probe": {
    "probedAt": 1760000000,
    "intervalSeconds": 60,
    "scores": [
      {"url": "stun:stun.douyucdn.cn:18000", "rttMs": 11, "ok": true, "score": 0.982, "selected": true}
    ]
  }
}
```

`probe.scores` **覆盖全部候选**（不只下发的那几条），所以排障时能直接看出"某条为什么落选"。

---

## 单端口部署 / 内网穿透

开发态是两个端口（Vite 5173 + Go 8080）。要在局域网或公网给外部用，就把前端构建产物交给 Go 托管：
**API、`/ws`、页面全部在同一个端口上**，于是一条隧道指向这一个端口就够了。

前端只使用相对路径 `/api` 与 `/ws`，WebSocket 地址按页面协议推导
（`client/src/stores/room.ts`：`location.protocol === 'https:' ? 'wss' : 'ws'`），
所以同源托管之后不需要任何额外配置。

```bash
# 1. 构建前端（产物在 client/dist，已 gitignore）
npm --prefix client install     # 首次
npm --prefix client run build

# 2. 起服务：默认就会托管 client/dist
go run ./cmd                    # → http://127.0.0.1:8080 同时是页面、API 与 /ws

# 3. 需要局域网/公网可访问时，监听所有网卡
PR_ADDR=0.0.0.0:8080 go run ./cmd
```

Windows PowerShell 里等价写法：

```powershell
$env:PR_ADDR = '0.0.0.0:8080'; go run ./cmd
```

然后用任意内网穿透工具把隧道指向这个端口，例如：

```bash
# frp（服务端 frps + 本地 frpc）
frpc tcp --server_addr <frps 地址> --server_port 7000 --local_ip 127.0.0.1 --local_port 8080 --remote_port 8080

# ngrok / cloudflared（直接指本地端口）
ngrok http 8080
cloudflared tunnel --url http://127.0.0.1:8080
```

隧道把外部请求转给本机 `8080`，页面、`/api`、`/ws` 一起可达。路由优先级固定为
**`/healthz` → `/api/*` → `/ws` → `/assets/*` → 其它真实文件 → SPA 回退 `index.html`**，
所以页面永远不会顶掉业务路由。

**三条必须知道的注意：**

1. **隧道给 HTTPS 时，页面会走 `wss://`。** 信令地址由 `location.protocol` 推导（见上），
   https 页面连的是 `wss://<域名>/ws`；隧道必须支持 WebSocket 升级（frp 的 http 类型、
   ngrok、cloudflared 都支持，但某些只做 HTTP 转发的反代会在升级时失败）。
   页面本身如果是 https，混用 `ws://` 会被浏览器按混合内容拦掉——这也是必须推导而不是硬编码的原因。
2. **隧道只解决「页面 + 信令」，媒体仍然是 P2P。** 视频分片走 WebRTC DataChannel，
   不经过隧道，也不经过服务器（不变量 I1）。因此双向打洞失败时（对称 NAT、严格公司网络），
   就是连不上——隧道对 P2P 打洞没有任何帮助。**TURN 已彻底退役**（原因见下），
   所以现在的 ICE 配置里只有 STUN；打洞成功与否完全取决于双方的 NAT 类型。

### 为什么不再有 TURN

**已移除**：`PR_TURN_URLS` / `PR_TURN_USER` / `PR_TURN_PASS` 三个环境变量、
`config.ICEConfig` 里的 TURN 字段，以及 `/api/ice`（与建房间响应）里的 `turn:` 条目。
设置这三个环境变量现在是无声无息的空操作，`/api/ice` 里不会再出现任何 `turn:` 项。

移除的原因（实测结论）：

- 私有 TURN（`turn:10.23.170.132:3478`）对**公网观众不可达**：它是私网地址，且隧道不转发 UDP。
  对公网观众而言它的存在只会让候选收集多等一轮超时，成本是负的。
- 一台"只对主播一侧可达"的中继没有任何意义：中继必须双方都能连上才能转发媒体，
  而 ICE 一旦把不可达的 TURN 收进候选，浏览器仍会在它上面浪费收集时间甚至把
  `relay` 候选当成可用路径。
- 没有可用 TURN 时，公开 STUN 列表的质量就成了打洞成功率的全部——所以本次同时把
  "谁可用"的判断从浏览器搬到服务端（服务端探测 + 打分 + TTL 下发，见上表与
  [`docs/ALGORITHM.md` §1.6](docs/ALGORITHM.md)）。

**什么条件下才该重新引入 TURN**：同时满足下面三条才值得做，缺一条都会退化成
"多一轮超时、零收益"：

1. 有一台**公网可达**的中继（要有公网 IP 或域名的 VPS，且 UDP 3478 真的通得过；
   纯 HTTP 隧道与只转 TCP 的反代都不行——TURN 靠 UDP 转发媒体）；
2. 房间成员真的会落在对称 NAT / 严格公司网络下面（否则直连成功率本来就很高）；
3. 接受中继的带宽成本（中继转发的是**视频字节**，这是唯一一处会让成本模型从 P2P
   退回 CDN 的地方，与不变量 I1 直接冲突，必须是有意识的决策而不是顺手打开）。

重新引入时不要恢复"字段留着不用"的半退役状态：要么按上面的条件实配并做真机验证，
要么就保持现在的纯 STUN 形态。
3. **分片上传/下载接口受隧道限制。** `/api/v1/segment/*` 的源文件上限是 **16 GiB**、
   作业时长上限 **60 分钟**（`PR_SEGMENT_MAX_SOURCE_BYTES` / `PR_SEGMENT_MAX_DURATION`），
   而隧道通常有自己的最大请求体与超时（很多免费隧道只有几十 MB / 30~100s），
   大视频远程上传大概率会在隧道层先被拒或超时。稳妥做法：在服务器本机（或局域网内）先切好分片，
   再把产物通过隧道分批下载（≥1 GiB 时接口会自动返回 JSON manifest + 分批链接）。

静态目录缺失（还没构建前端 / `PR_STATIC_DIR` 指错）**不影响服务启动**：
服务端只打一条 WARN 并在页面路径上返回 404 提示，`/api`、`/ws`、`/healthz` 照常工作
（所以 `go run ./cmd` 不装前端也能当纯信令服务器跑）。

---

## 验收

### 自动化（服务端 + 前端单测）

```bash
gofmt -l ./cmd ./internal          # 必须为空
go vet ./... && go vet -tags wireinject ./cmd
go test ./...                      # 7 个有测试的包（含 cmd/segmenter 的一键流程）

cd client && npm run typecheck && npm run build
```

`cmd/segmenter` 的测试里有两条不需要真 ffmpeg 的关键用例：用 `httptest` 提供一个小 zip
验证"下载→解压→找到工具"，以及用 `testdata/stubtools`（假 ffmpeg/ffprobe）把整条
一键流程跑到产出 `index.json`（假 ffmpeg 会把收到的参数写进 `PR_STUB_LOG`，用来证明
"找到了工具并把它作为 ffmpeg 路径传下去"）。

### 竞态检测（-race）

Windows 上 Go 的 `-race` 需要 gcc 兼容驱动（mingw-w64），MSVC/clang 都不行；
本机没有 mingw 时用容器跑（Docker Desktop 即可，不动主机）：

```bash
docker run --rm -v "D:/IT/program/go-program/ProjectionRoom:/app" -v pr-gomod:/go/pkg/mod \
  -w /app -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off -e GOTOOLCHAIN=local \
  golang:1.26 sh -c "go test -race ./..."
```

`GOPROXY` 必须指到能通的源（容器里直连 `proxy.golang.org` 会被拒）。
最后一次全绿：`config / handler / model / service/mp4 / service/segment / usecase` 全部 ok。
容器里没有 ffmpeg，正好顺带覆盖"服务器缺 ffmpeg"这条分支。

### 真实浏览器实测（M2 验收脚本）

需要 Go 服务端与 Vite dev server 都在运行，且准备一个分片目录：

```bash
# 默认就用仓库自带的测试素材 test/resource/short_video/cut（25s），无需额外准备
node test/script/verify-m2.mjs

# 也可以指向自己切出来的分片目录
node test/script/verify-m2.mjs --media ./room-media --duration 120

# M3：多层树 / 单链分发（可注入实测上行，因为 headless 的 getStats 不产生估计值）
node test/script/verify-m3.mjs --nodes 4 --host-uplink 12000000 --uplink 1=12000000
node test/script/verify-m3.mjs --media ./room-media --nodes 4 --host-uplink 800000 --uplink 1=6000000
```

### 诊断脚本（两端取证）

验收脚本只给 PASS/FAIL；排障时用这两个把两端状态并列打出来：

```bash
# 观众拿不到分片：连接/请求/应答/门控/播放器 + 取数失败原因与主播应答日志
node test/script/diag-chunks.mjs

# 房主控制的时序（200ms 采样）：control=按钮暂停 / native=原生控件暂停 / join-order=观众先入房后开播
node test/script/diag-control.mjs --scenario native

# 写入本地目录分阶段计时（zip 解析 vs 逐文件写入、重复写快路径）
node test/script/diag-write.mjs
```

### 大文件 / 真实场景

`test/resource/` 下的素材（大体积二进制，已 gitignore）：

| 素材 | 规格 | 说明 |
| :--- | :--- | :--- |
| `short_video/cut` | 25s / 26 片 / 4.5Mbps | 验收脚本默认素材（已切好，含 index.json） |
| `middle_mkv_video.mkv` | 2.9min H.264+AAC | 中等长度 |
| `middle_mp4_video.mkv` | 13.4min H.264+AAC / 111MB | **真实大文件**：实测服务端 2.0s 切出 578 片、单次 zip 111.7MiB、下载 2.9s |
| `big_mp4_video.mp4` | 142min AV1+Opus / 3.9GB | 超过 60min 配额，应被拒（前端会先本地判时长，不白传） |

```bash
# 大文件端到端：上传 → 服务端切片 → 取产物，打印每阶段耗时与产物形态
node test/script/verify-large.mjs
node test/script/verify-large.mjs --source test/resource/big_mp4_video.mp4   # 预期被配额拒绝
```


脚本会启动**两个独立的 Chrome 实例**（不能是同一实例的两个标签页：后台标签会被冻结/节流，
既会让 CDP 调用挂死，也会把 200ms 的同步循环拖成 1s，那测的就不是同步精度了），
分别扮演主播与观众，然后测量起播耗时、全程偏差分布、暂停/跳转/继续的跟随情况，
并输出 `PASS` / `FAIL`。

页面通过 `window.__pr.snapshot()` 暴露结构化状态快照（`client/src/debug.ts`），脚本只读快照、不抠 DOM 文本。

---

## 项目结构

```
cmd/                    入口：main.go 只有 加载配置 → InitializeApp → Run
cmd/init.go             Init 聚合根与 Run()（wire 图与 main 的唯一交点）
cmd/wire.go             wire.Build 依赖声明（inject 侧，勿手写 wire_gen.go）
cmd/wire_gen.go         `go tool wire ./cmd` 生成的装配代码（提交，不手改）
cmd/segmenter/          视频分片工具（一键流程：找输入/找或下载 ffmpeg/探测判定 + moof 边界切片 + index.json）
internal/config/        配置结构与加载（环境变量覆盖）
internal/handler/       gin 路由、/ws 处理、错误映射与前端静态资源托管（HTTP/协议适配层）
internal/usecase/       业务用例：房间生命周期、拓扑分配、模式判定、换防（唯一状态权威）
internal/model/         数据契约：消息模型、分片索引、protobuf 转换
internal/service/       WebSocket 信令层（Hub）与 HTTP 服务器生命周期
internal/service/mp4/   fragmented MP4 解析与按 moof 边界切分
internal/utils/         与业务无关的小工具（随机码、切片比较）
internal/pb/            protoc 生成的 protobuf 代码
client/                 Vue 3 + TS + Vite 前端（播放链路在 src/composables/）
test/script/          真实浏览器验收与诊断脚本（verify-m2 / verify-m3 / verify-segment-ui / diag-*）
test/resource/        测试素材（大体积二进制，已 gitignore；脚本默认用 short_video/cut）
docs/SPEC.md            设计文档（协议、拓扑、同步算法、里程碑与验收标准）
```

**依赖方向**：`handler → usecase → model`；基础设施（`service*`）只通过接口被用例引用
（`wire.Bind(new(usecase.Broadcaster), new(*service.Hub))`），`utils`/`model` 不反向依赖任何层。

**不变量**：`internal/` 下任何代码都不得读写视频数据；服务器的职责边界在 SPEC §3 有明确表格。

---

## 开发约定

- 消息字段在 `internal/model/message.go` 与 `client/src/types/protocol.ts` 各有一份，
  改动必须同步 —— 它们是同一个协议的两份投影；线上编码为 protobuf
  （`proto/projection_room.proto` 是唯一源，转换点见 `internal/model/pb_convert.go` 与 `client/src/types/codec.ts`）。
- 分片索引在 `internal/model/index.go` 与 `client/src/types/media.ts` 同理。
- 前端改动期间建议先停掉 Vite：Windows 上文件写入的原子替换会与它的 watcher 抢锁（EBUSY）。
