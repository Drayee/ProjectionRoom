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
| M4 | 稳健性、监控面板、STUN/TURN 接入 | 待开始 |

### 启动门控（加载不设超时）

新节点接入或跳转后**不立即播放**，而是进入"加载中"：缓冲达到 4 片 / 8 秒、
且时钟偏移估计已收敛（样本 ≥ 6 且最小值 0.8s 内不再下降）才开闸。
**门控不设超时上限**：上游没数据就一直等，等待超过 20s 时提示"上游带宽可能不足"。
这修掉了上一轮"起播后 0.2–1.4s 偏差尖峰"的根因（薄缓冲硬播 → 立刻触发大幅矫正）。

### M3 已落地（部分验证）

- **`internal/usecase`**：拓扑分配引擎。先按深度择父（树越浅越好）、同深度再比余量/RTT/稳定性；
  单链模式按实测上行选举分发节点，并带 1.5 倍换防滞回（避免两个差不多的节点来回抢位）；
  排布容量与**准入容量**分开算 —— 未实测的节点只能参与排布，不能凭猜测开闸。
- **客户端中继**：转发节点从自己的分片仓库应答子节点；进度与时钟锚点沿主父路径逐跳转发
  （不改 `currentTime`/`hostClockMs`/`seq`）；`have` 位图让取数有依据。
- **自适应条带化**：只向"明确声明拥有该片"的父节点取数；主父积压时才分流给备用父；
  失败即转投下一个父节点。
- **真机验证**（`tools/verify-m3.mjs`，4 个独立 Chrome）：
  - 验收 A：深度 `[0,1,1,2]`，一级节点真的在给二级节点供片，全员播放；
  - 验收 B：注入主播上行后进入**单链模式**，主播只有 1 个子节点（分发节点），其余全挂在它下面。

### M2 已交付

- **`cmd/segmenter`**：把本地视频切成 `init.mp4` + `c00001.m4s…` + `index.json`。
  支持 `-fragment`（无损重新封装）与 `-transcode 1200k`（低上行预设），并直接打印
  "这个码率下主播能带几个人"。
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

前置：Go 1.26+、Node 20+、`ffmpeg`（用于把本地视频预处理成 fMP4 分片）。

```bash
# 终端 1：服务端（默认 127.0.0.1:8080）
go run ./cmd

# 终端 2：前端（默认 127.0.0.1:5173，/api 与 /ws 自动代理到服务端）
cd client && npm install && npm run dev
```

浏览器打开 http://127.0.0.1:5173 ，一个窗口「创建房间」当主播，另一个窗口用房间码「加入房间」。

### 主播准备视频

```bash
# 普通 MP4 → 分片目录（无损重新封装，不重新编码）
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -fragment -frag-sec 2

# 上行不足时的低码率预设（重新编码，耗时随片长增长）
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -transcode 1200k
```

然后在主播控制台点「选择分片目录」，选中 `room-media/` 即可开播。

`segmenter` 会打印容量提示，例如：

```
容量提示（按主播上行 12.0 Mbps 估计）:
  K0 = 4 → 扇出模式：主播可直接服务 4 个一级节点，其余成员挂到它们下面。
```

### 配置

服务端全部配置都有面向本机开发的默认值，可用环境变量覆盖：

| 变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `PR_ADDR` | `127.0.0.1:8080` | 监听地址 |
| `PR_MAX_MEMBERS` | `16` | 房间成员硬上限（真实上限由实测上行算出的 `1+K0` 决定） |
| `PR_DEFAULT_STREAM_BPS` | `2000000` | 尚未拿到 mediaIndex 时的码率估计 |
| `PR_STUN_URLS` | `stun:stun.l.google.com:19302` | 逗号分隔 |
| `PR_TURN_URLS` / `PR_TURN_USER` / `PR_TURN_PASS` | 空 | 直连失败时的中继（默认不启用） |

---

## 验收

### 自动化（服务端 + 前端单测）

```bash
gofmt -l ./cmd ./internal          # 必须为空
go vet ./... && go vet -tags wireinject ./cmd
go test ./...                      # 8 个包

cd client && npm run typecheck && npm run build
```

### 真实浏览器实测（M2 验收脚本）

需要 Go 服务端与 Vite dev server 都在运行，且准备一个分片目录：

```bash
node tools/verify-m2.mjs --media ./room-media --duration 300

# M3：多层树 / 单链分发（可注入实测上行，因为 headless 的 getStats 不产生估计值）
node tools/verify-m3.mjs --media ./room-media --nodes 4 --duration 30
node tools/verify-m3.mjs --media ./room-media --nodes 4 --host-uplink 800000 --uplink 1=6000000
```

脚本会启动**两个独立的 Chrome 实例**（不能是同一实例的两个标签页：后台标签会被冻结/节流，
既会让 CDP 调用挂死，也会把 200ms 的同步循环拖成 1s，那测的就不是同步精度了），
分别扮演主播与观众，然后测量起播耗时、全程偏差分布、暂停/跳转/继续的跟随情况，
并输出 `PASS` / `FAIL`。

页面通过 `window.__pr.snapshot()` 暴露结构化状态快照（`client/src/debug.ts`），
脚本只读快照、不抠 DOM 文本。

---

## 项目结构

```
cmd/                    入口：main.go 只有 加载配置 → InitializeApp → Run
cmd/init.go             Init 聚合根与 Run()（wire 图与 main 的唯一交点）
cmd/wire.go             wire.Build 依赖声明（inject 侧，勿手写 wire_gen.go）
cmd/wire_gen.go         `go tool wire ./cmd` 生成的装配代码（提交，不手改）
cmd/segmenter/          视频分片工具（moof 边界切片 + index.json）
internal/config/        配置结构与加载（环境变量覆盖）
internal/handler/       gin 路由、/ws 处理与错误映射（HTTP/协议适配层）
internal/usecase/       业务用例：房间生命周期、拓扑分配、模式判定、换防（唯一状态权威）
internal/model/         数据契约：消息模型、分片索引、protobuf 转换
internal/service/       WebSocket 信令层（Hub）与 HTTP 服务器生命周期
internal/service/mp4/   fragmented MP4 解析与按 moof 边界切分
internal/utils/         与业务无关的小工具（随机码、切片比较）
internal/pb/            protoc 生成的 protobuf 代码
client/                 Vue 3 + TS + Vite 前端（播放链路在 src/composables/）
tools/verify-m2.mjs     真实浏览器验收脚本
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
