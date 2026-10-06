# ProjectionRoom 设计文档（SPEC）

> 状态：设计已获批准；**M1、M2 已实现并通过验证**（含真实浏览器实测）。
> **M3 实现中**：多层树与单链分发已在真实浏览器验证通过（§10 M3 验收 A/B）；
> 抗慢节点（D）与真机换防（C）待验证；启动瞬态与分片缺失仍在收敛中。
> 目标产物：像 B 站放映室一样的「一起看」房间——主播放本地视频，观众同步观看并聊天。
> 本文档替代原始提案中的若干不成立设计，差异集中在 §2、§6 与 §13。
> 修订：v2 —— 按决策「上行 < 2×码率时由唯一的分发节点承担分发」新增 §6.1 模式判定、§6.3 选举/换防/失效恢复、§4.5 低码率预设。

---

## 1. 目标与非目标

### 1.1 目标（本轮 MVP 验收对象）

1. 创建/加入房间，房间号 + 可选密码。
2. 主播选择本地**预处理后的 fMP4 分片目录**，浏览器端持有全部分片。
3. 观众通过 WebRTC DataChannel 从主播/**转发节点**拉取分片，用 `MediaSource` 播放。
4. 播放进度同步：**全房间偏差 < 500ms**，暂停/继续/跳转由房主单向广播，全员跟随。
5. 房间内文字聊天 + 成员列表（在线状态、角色、深度）。
6. 分发拓扑为 **多层树 + 多父条带化**；当主播上行不足（`U < 2×码率`）时自动切换为 **单链分发模式**：主播只服务**唯一一个按实测上行选举出的分发节点**，由它承担全房间分发。任一节点上传不足只影响其份额内的分片，不拖垮全局。
7. 服务器**不传输任何视频字节**。

### 1.2 非目标（明确不做，避免范围蔓延）

- ❌ 弹幕（danmaku）渲染、礼物、点赞等 B 站业务功能（协议预留 `danmaku` 消息类型，不实现 UI）。
- ❌ 服务器中继视频 / 服务器作为 Seeder（会打破"零视频流量"）。
- ❌ 大规模（万人级）CDN 优化、分片 AES 加密、鉴权体系、水平扩展。
- ❌ 移动端适配、多音轨/字幕选择、直播源（RTMP/HLS）接入。
- ❌ 超过浏览器挂载能力的房间规模（见 §6.2 容量模型）。

### 1.3 已确认的决策（来自需求澄清）

| 维度 | 决策 |
| :--- | :--- |
| 范围 | 先做同步放映室 MVP，不分叉去做纯 P2P CDN |
| 视频源 | 主播浏览器当源，视频**不经服务器** |
| 部署 | 本机开发（`127.0.0.1`），HTTPS 仅预留配置；ICE **只用 STUN**（服务端探测 + 打分 + TTL 下发，见 `docs/ALGORITHM.md` §1.6），TURN 已彻底退役（见 README「为什么不再有 TURN」） |
| 前端 | Vue3 + TS + Vite，**前后端分离**（独立 dev server） |
| 后端 | **gin + google/wire DI**（沿用现有骨架意图） |
| 媒体准备 | 主播**本地 ffmpeg 预处理**成 fMP4 分片目录（方案 A） |
| 拓扑 | **多层树**，带多父/条带化以抗慢节点 |
| 低上行策略 | `U < 2×码率` 时进入**单链分发模式**：主播只服务 1 个分发节点，由它分发全房间（§6.1、§6.3） |

---

## 2. 关键技术约束（必须承认的事实）

这些约束推翻了原始提案中的几处设计，是本文档差异的根因。

| # | 约束 | 原始提案 | 本设计 |
| :-- | :--- | :--- | :--- |
| C1 | `MediaSource` 只接受 **fMP4 / WebM**；任意 MP4 的裸字节不能 `appendBuffer` | 假设可按 256KB 任意切 | 主播先 ffmpeg 预处理（§4） |
| C2 | 分片边界必须在 **`moof` 段边界**，否则 `appendBuffer` 抛错 | "固定大小切分" | 按 `moof` 边界切（§4.2） |
| C3 | 音频+视频分成两条 DASH stream 需要**两个 SourceBuffer** 并各自维护时间轴 | 未提及 | 采用**单条 muxed fMP4**（A/V 同一分片），只用 1 个 SourceBuffer（§4.3） |
| C4 | 跨机 `Date.now()` **不可比**（时钟漂移可达秒级） | 直接用 `Date.now()` 差值当延迟 | NTP 最小滤波估时钟偏移（§7.2） |
| C5 | 进度消息可能**乱序/丢失** | 无序号 | 每条进度带单调 `seq`，丢弃过期（§7.3） |
| C6 | 浏览器**自动播放策略**：`play()` 需用户手势 | 未提及 | 房间页必须有一次"进入房间"手势（§9.3） |
| C7 | DataChannel 单连接吞吐约 1–10 Mbps，且有背压 (`bufferedAmount`) | 未提及 | 在途窗口限制 + 背压检查（§5.4） |
| C8 | `RTCPeerConnection` 数量、上行带宽都是**主播侧硬瓶颈** | 假设可撑 20000 人 | 容量模型 + 成员上限（§6.2） |
| C9 | MSE 的 `seek` 需 `remove()` 清 buffer 后重灌，代价高 | "偏差 >500ms 直接 seek" | 三级策略：微调速率 → 跳缓冲 → seek（§7.4） |
| C10 | 纯树中慢父节点**必然**拖死整棵子树 | 声称树状分发即可 | 多父条带化 + 分片级转投（§6.4）——直接回应你的拓扑要求 |
| C11 | 浏览器**读不到**自己的上行带宽，自报值不可信 | 用理论值/自报值做容量计算 | 只用可测量值：`getStats().availableOutgoingBitrate` + 子节点上报的实测交付吞吐，取较小值（§6.2、§6.3） |
| C12 | 单链模式下分发节点是**单点故障**，它掉线即全房间断流 | 未提及 | 实测选举 + 平滑换防 + 失效接任（§6.3） |
| C13 | 主播重新加载页面后 `performance.now()` 归零，观众侧的时钟偏移滤波会整体失效 | 未提及 | 消息携带 `clockEpoch`，纪元变化即重置滤波（§7.1） |
| C14 | 空 MediaSource 上给 `currentTime` 赋值会被浏览器丢弃（readyState 为 HAVE_NOTHING 时只记录默认起始位置） | 未提及 | 跳转必须按「清空缓冲 → 补齐目标分片 → 再定位」的顺序执行（§7.4） |
| C15 | `getStats().availableOutgoingBitrate` 在部分环境（headless + 环回）不产生估计值 | 未提及 | 取不到就保持 `mode=pending`，不编造容量；M3 用子节点上报的实测交付吞吐做第二来源交叉验证（§6.3） |

---

## 3. 总体架构

```
┌──────────────────────── 主播端 seeder ────────────────────────┐
│ · 持有全部 fMP4 分片（来自本地预处理目录）                      │
│ · 权威时钟 + 房间控制权（play/pause/seek/rate）                 │
│ · 每 500ms 广播 progress{currentTime, hostClockMs, seq}        │
│ · DataChannel 服务 N 个子节点（N = K0，受上行容量约束）         │
└───────────┬───────────────────────────────────────────────────┘
            │ WebRTC DataChannel（视频分片，服务器不经手）
   ┌────────┼─────────┬──────────┐
   ▼        ▼         ▼          ▼
 relay1   relay2    relay3     leaf4          ← 深度 1（一级节点）
   │        │
   ├───┐    ├───┐
   ▼   ▼    ▼   ▼
 leaf leaf leaf leaf                            ← 深度 2（二级节点）
（每个节点: 1 primary parent + ≤2 backup parent，分片级条带化拉取）

            │ WebSocket（仅信令 / 房间状态 / 聊天 / 拓扑）
┌───────────┴───────────────────────────────────────────────────┐
│ Go 服务端（gin + wire）                                        │
│  internal/signal    SDP / ICE 定向转发，房间广播                │
│  internal/room      房间生命周期、成员表、房主控制、聊天         │
│  internal/tracker   分片拥有情况(bitset)、容量计分、父节点推荐   │
│  internal/topology  模式判定 / 分发节点选举 / 分配 / 重平衡      │
│  internal/config    端口、上限、STUN 候选列表与下发策略、超时参数   │
│  internal/service/ice  服务端 STUN 探测 + 打分 + 加权轮询 + TTL      │
│                        ⚠ 不传输任何视频数据                     │
└───────────────────────────────────────────────────────────────┘
```

> **目录分层现状（重构后）**：`internal/signal` → `internal/service`（Hub + Server），
> `internal/room` + `internal/topology` + `internal/tracker` → `internal/usecase`，
> `internal/httpapi` → `internal/handler`，`internal/media` + `internal/protocol` → `internal/model`，
> `internal/mp4` → `internal/service/mp4`。下文出现的旧路径均按此对应。

**职责边界（单一权威）**

| 关注点 | 唯一权威 | 说明 |
| :--- | :--- | :--- |
| 播放时间 | 主播 | 观众只跟随，不反向影响 |
| 房间控制权 | 主播 | 服务端只做权限校验与广播 |
| 成员/在线状态 | 服务端 `internal/usecase` | 断线由服务端判定并广播 |
| 拓扑模式与分配 | 服务端 `internal/usecase` | 客户端只提交度量，不自行挂载、不自我提升 |
| 分片拥有情况 | 各节点自报，服务端 `internal/usecase` 聚合 | 用于推荐，不用于传输 |
| 分片数据 | 各 peer 本地 | 服务器不持有、不转发 |

---

## 4. 媒体准备与分片模型

### 4.1 主播端预处理（一步 ffmpeg）

```bash
# 1) 生成单个 fragmented MP4（A/V muxed，moof 边界即关键帧）
ffmpeg -i input.mp4 -c copy \
  -movflags +frag_keyframe+empty_moov+default_base_moof \
  -frag_duration 2000000 \
  out_frag.mp4

# 2) 用本项目自带工具按 moof 边界切分，并产出索引
go run ./cmd/segmenter -in out_frag.mp4 -out ./room-media \
  -segment-duration 2
```

`cmd/segmenter` 产出：

```
room-media/
├── init.mp4            # ftyp + moov（MSE 首个 appendBuffer）
├── c00001.m4s          # 从 moof 起始，含 mdat
├── c00002.m4s
├── ...
└── index.json          # 分片索引（见 §4.3）
```

> 备选（未采用）：`ffmpeg -f dash` 会产出 `init-stream0/1` + 双流分片，可省去自研 segmenter，但需要 2 个 SourceBuffer 各自维护时间轴（C3），MVP 阶段复杂度不划算。

### 4.2 为什么必须按 `moof` 边界切

`SourceBuffer.appendBuffer()` 要求输入是合法的 fMP4 片段序列：首个片段必须是 `ftyp+moov`（初始化段），后续每个片段必须从一个完整的 `moof`（+随后的 `mdat`）开始。按固定字节数切割会产生"半个 moof"，浏览器直接抛 `MediaSource` 错误——原始提案的 256KB 固定分片在真实浏览器上不可用。

### 4.3 索引模型

```go
// internal/model/index.go
type Segment struct {
    Index    int     `json:"index"`
    File     string  `json:"file"`      // 打包后是 "pack-0001.bin"，未打包是 "c00001.m4s"
    Offset   int64   `json:"offset"`    // 该片在 File 里的字节偏移（打包时是包内偏移）
    Size     int64   `json:"size"`
    Duration float64 `json:"duration"`  // 秒
    StartPTS float64 `json:"startPts"`  // 播放时间轴起点（秒）
    Keyframe bool    `json:"keyframe"`  // 是否可作为 seek 落点
    SHA256   string  `json:"sha256"`    // 完整性校验（始终对**分片**求，不是对包）
}

// Pack 是打包产物：每 N 片合成一个 .bin（默认 N=100）。
// 为什么打包：Windows 上"每个文件一次 createWritable()"有固定开销（swap 文件 + 杀软扫描），
// 60 分钟的视频有 ~1800 个分片，逐片落盘要好几分钟；打包后只剩 ~18 个文件。
type Pack struct {
    File         string `json:"file"`         // "pack-0001.bin"
    FirstSegment int    `json:"firstSegment"`  // 该包第一片的 index
    Count        int    `json:"count"`
    Bytes        int64  `json:"bytes"`         // 包字节数，必须等于盘上实际大小
}

type Index struct {
    Version       int       `json:"version"`
    InitFile      string    `json:"initFile"`
    Packs         []Pack    `json:"packs,omitempty"` // 省略 = 逐片成文件（向后兼容）
    MimeType      string    `json:"mimeType"`
    TotalDuration float64   `json:"totalDuration"`
    SegmentSec    float64   `json:"segmentSec"`
    BitrateBps    int64     `json:"bitrateBps"`
    Segments      []Segment `json:"segments"`
}
```

约束：
- 分片时长目标 **2s**（可按码率调整到 1–4s）。太小 → 请求开销与拓扑抖动大；太大 → 同步粒度粗、P2P 延迟高。
- `MimeType` 由 `cmd/segmenter` 从 `moov` 的 `stsd` 解析得出，**必须**在开播前用 `MediaSource.isTypeSupported(mime)` 校验，不支持则直接拒绝开播并给出明确错误（避免运行期黑屏）。
- `BitrateBps = ΣSize*8 / TotalDuration`，是 §6.1 模式判定与 §6.2 容量模型的唯一输入。

### 4.4 主播端加载

主播用 `<input type="file" webkitdirectory>` 选择 `room-media/` 目录 → 前端按 `index.json` 建立 `Map<index, File>`（`File` 对象不读入内存，需要时 `File.slice()` 延迟读取并直接经 DataChannel 发送，避免大视频占满内存）。`index.json` 同时通过 WebSocket 发给服务端，作为房间的 `mediaIndex` 广播给所有观众。

### 4.5 低码率预设（`K0 = 0` 时的唯一出路）

当主播上行过弱（`U < 1.25×B`，实测 `K0 = 0`），或房间内没有任何节点满足分发条件时，唯一的自救方式是**降低 `B`**：

```bash
# 低上行预设：转码到 1.2 Mbps，使 U ≥ 2×B 重新成立
ffmpeg -i input.mp4 -c:v libx264 -preset veryfast -b:v 1200k \
  -c:a aac -b:a 96k \
  -movflags +frag_keyframe+empty_moov+default_base_moof \
  -frag_duration 2000000 out_frag.mp4
```

- 开播前，主播端用 §6.1 的 `HostChildSlots` 预判容量并明确提示：「当前码率 2.0 Mbps 下房间最多 4 人；启用 1.2 Mbps 预设可提升到 7 人」。
- 转码耗时随片长增长（长视频可能数分钟），因此属于**开播前的显式操作**，不在开播流程中隐式触发。
- 是否把该预设纳入 M2，见 §12 O6。

---

## 5. 通信协议

> **编码**：线上是 **protobuf**（唯一真源 `proto/projection_room.proto`，生成代码入库，
> 重新生成用 `scripts/gen-proto.ps1`）。WebSocket 走**二进制帧**；
> DataChannel 上控制消息与分片数据都是二进制，由 1 字节 kind 前缀区分（见 §5.2）。
> 分片数据**不包 protobuf**：那会为每个分片多一次大块内存拷贝。
> 本节的字段表描述的是消息语义，字段编号以 .proto 为准。

### 5.1 WebSocket（信令 / 房间，JSON 文本帧）

连接：`ws://host:8080/ws?roomId=<id>&clientId=<uuid>&role=host|viewer`

**客户端 → 服务端**

| type | 字段 | 说明 |
| :--- | :--- | :--- |
| `join` | `roomId, clientId, displayName, role, password?` | 加入房间（role=host 需房间未开播） |
| `signal` | `to, payload` | SDP / ICE 透传（`payload` 原样转发，服务端不解析） |
| `chunks-report` | `have`(Base64 bitset), `complete`(bool) | 分片拥有情况增量上报 |
| `metrics` | `rttMs, throughputBps, uploadCapacityBps, depth` | 供拓扑计分与选举；`uploadCapacityBps` 取自 `getStats().availableOutgoingBitrate`（C11） |
| `media-index` | `mediaIndex` | 主播发布分片索引（开播动作）。服务端用 `media.Index.Validate` 校验后广播给全房，并以其 `bitrateBps` 作为容量模型的码率输入（§6.1） |
| `topology-request` | `have, parents[]` | 请求父节点分配 / 重平衡 |
| `room-control` | `action(play\|pause\|seek\|rate), currentTime, hostClockMs, seq` | 仅 host，服务端校验权限 |
| `chat` | `text` | 房间文字消息 |
| `leave` | — | 主动离开 |

**服务端 → 客户端**

| type | 字段 | 说明 |
| :--- | :--- | :--- |
| `joined` | `selfId, hostId, mode, distributorId, members[], mediaIndex, topology` | 入房快照 |
| `member-joined` / `member-left` / `member-list` | `members[]` | 成员变化 |
| `media-index` | `mediaIndex` | 主播开播时发布 / 后进房者据此补齐分片索引 |
| `capacity` | `capacity`（含 `mode`/`hostChildSlots`/`streamBps`/`maxMembers`） | 容量随实测上行变化时广播（§6.2）；`mode=pending` 表示尚未拿到实测值 |
| `signal` | `from, payload` | 定向信令 |
| `parent-assignment` | `primaryId, backupIds[], reason, mode, maxDepth` | 拓扑分配结果（`reason` 含 `fanout` / `chain-distributor` / `rebalance` / `failover`） |
| `distributor-change` | `fromId, toId, reason, members[]` | 分发节点换防 / 接任广播（§6.3） |
| `room-control` | `currentTime, hostClockMs, seq, paused, rate` | 房主控制广播（所有端跟随） |
| `chat` | `from, displayName, text, ts` | 聊天广播 |
| `error` | `code, message` | `ROOM_FULL / BAD_PASSWORD / NOT_HOST / MEDIA_MISMATCH / UNSUPPORTED_MIME / ROOM_DEGRADED` |

### 5.2 DataChannel（peer 间，视频分片走二进制）

所有 peer 连接使用**预协商** DataChannel：`{ ordered: true, id: 0, protocol: "pr/1" }`。
控制消息用 **protobuf 二进制帧**（早期版本的 JSON 文本帧已被替换，见 §5.1），分片数据同样是**二进制帧**。

**帧格式（大端）**

```
+--------+--------+------------------+------------------+
| kind(1)| flags(2)| chunkIndex(4)    | payload…         |
+--------+--------+------------------+------------------+
  kind: 0x01 = 控制消息(protobuf)  0x02 = INIT 段
        0x03 = 媒体分片            0x04 = 结束/尾段
```

**分片与重组（必须做，不是优化）**：`flags` 低 1 位表示"后面还有分片"，
高 15 位是当前分片在一个 chunk 内的序号（0 起）。

DataChannel 单条消息有协商上限（Chrome↔Chrome 约 256KiB，规范默认 64KiB）。
高码率素材的单个分片可以到 1–2MB（一个大 I 帧就够），**超过上限时 `send()` 会直接抛异常**。
实际踩到过的现象：主播侧整片发不出去、观众一直显示"缓冲中"、
两边都**不报错**（发送端没检查返回值、接收端没收到任何东西）。
因此发送端固定按 ≤64KiB 切分并做背压（`bufferedAmount` 超阈值先等待），
接收端按 `(peerId, chunkIndex)` 重组；未分片的帧走零拷贝快路径。

**控制消息（文本 JSON）**

| t | 字段 | 方向 | 说明 |
| :--- | :--- | :--- | :--- |
| `have` | `chunks`(Base64 bitset), `complete` | 双向 | 我有哪些分片（变化时增量、每 3s 全量一次） |
| `req` | `rid, idx` | 子→父 | 请求分片（`rid` 为 UUID，用于并发区分） |
| `err` | `rid, idx, code` | 父→子 | 我不持有 / 读取失败 |
| `progress` | `currentTime, hostClockMs, clockEpoch, seq, paused, rate` | 主播→下游逐跳转发 | 播放权威信息 |
| `time-sync` | `hostClockMs, seq` | 主播→下游逐跳转发，每 5s | 时钟锚点 |

**关键原则**：`progress` 与 `time-sync` 中的 `hostClockMs` 是**主播的时钟读数，逐跳原样转发、任何中继都不得改写**（改写会让下游无法估算到主播的偏移）。中继只是转发者。

### 5.3 消息处理顺序与幂等

- 每条 `progress`/`time-sync` 带 `seq`；接收端只接受 `seq > lastSeq` 的消息，乱序与重复直接丢弃（C5）。
- `have` bitset 采用"或"合并（分片只增不减）；换片/重开播时 bitset 整体重置。
- `req` 允许对同一 `idx` 并发多次（不同父节点），先到先用，其余结果丢弃。

### 5.4 背压与在途窗口

- 每节点维护滑动预取窗口：`[playhead + 1, playhead + W]`，`W` 默认 **30** 片（2s/片 ≈ 60s 缓冲）。
- 全局在途请求上限 `maxInflight = 16`；`channel.bufferedAmount > 4MB` 时暂停该父节点的分派（C7）。
- 单分片请求超时 **3s**，超时后立即转投下一个父节点（最多 3 次），三次全败则记入缺失表并降级到窗口更靠后的位置重试。
- **换防专用约束**：分发节点换防期间（§6.3），预取窗口临时扩大到 `W = 60`，为切换留出缓冲。

---

## 6. 拓扑管理

### 6.1 术语与拓扑模式

**深度口径（全文统一）**：主播 = 深度 0；主播的直接子节点 = **一级节点**（深度 1）；其子节点 = **二级节点**（深度 2）。

拓扑模式由主播上行 `U` 与视频码率 `B` 决定：

| 模式 | 触发条件 | 形态 |
| :--- | :--- | :--- |
| **扇出模式 fanout** | `K0 ≥ 2` | 主播直连 `K0` 个一级节点，各自向下分发 |
| **单链分发模式 chain** | `K0 ≤ 1`（即 `U < 2×B`） | 主播**只服务 1 个分发节点 D**，由 D 承担全房间分发 |

低上行单链模式：

```
 主播 ──► 分发节点 D ──┬──► leaf1
 （唯一子节点）         ├──► leaf2
                       └──► leaf3
 （D 必须真的比主播上行强，否则只是把瓶颈从主播搬到 D）
```

```go
// internal/topology/mode.go
const safetyFactor = 0.8

// HostChildSlots: 主播可直接服务的子节点数
func HostChildSlots(uploadBps, streamBps int64) int {
    if uploadBps <= 0 || streamBps <= 0 {
        return 2 // 未实测 → 保守默认
    }
    k := int(float64(uploadBps) * safetyFactor / float64(streamBps))
    switch {
    case k < 1:
        return 0 // 连 1 个都带不动 → 见 §4.5
    case k > 8:
        return 8
    default:
        return k
    }
}

type Mode int

const (
    ModeFanout Mode = iota
    ModeChain
)

func SelectMode(hostSlots int) Mode {
    if hostSlots <= 1 {
        return ModeChain
    }
    return ModeFanout
}
```

模式**随实测数据变化**：预热期用保守默认值给出 `fanout`；实测后若主播上行低于 `2×B`，服务器切到 `chain` 并广播 `distributor-change`（§6.3）。

### 6.2 容量模型（房间规模的真实上限）

设主播上行 `U` bps、视频码率 `B` bps、转发节点上行 `Uᵢ`：

```
扇出模式：K0 = floor(U × 0.8 / B)，第 i 层节点容量 Ki = floor(Uᵢ × 0.8 / B)
         总容量 N ≈ K0 + Σ(各转发节点 Ki)，受 maxDepth 与 Ki ≥ 1 约束

单链模式：总容量 N ≈ floor(D.uploadCapacityBps × 0.8 / B)
         即全房间容量等于分发节点 D 的容量
```

示例：`U = 12 Mbps`、`B = 2 Mbps`、`Uᵢ = 6 Mbps` → `K0 = 4`、`Ki = 2`
→ 深度 1 服务 4 人，深度 2 再容纳 8 人，合计约 **12 人**。
低上行例：`U = 3 Mbps`、`B = 2 Mbps` → `K0 = 1` → 单链模式，容量由 D 的上行决定。

**必须承认的三条硬约束：**

1. **单链模式不创造带宽**，它只是把分发压力从主播转移到 D。若 `D.uploadCapacityBps < 2×B`，瓶颈只是从主播搬到 D，房间规模不变。
2. **物理下限**：若 `K0 = 0`（`U < 1.25×B`）或房间内没有任何节点满足 `CanDistribute`（§6.3），则无论怎么组织拓扑都带不动 1 名观众。出路只有三条：① 用 §4.5 低码率预设重新预处理（降 `B`）；② 容量降为 1（仅主播自己看）；③ 服务器中继（**当前决策明确不做**）。
3. **链式退化**：若 `K0 = 1` 且多数节点上行也仅 ≈ `B`，拓扑退化为**线性链**（主播→D→R2→R3…），容量受 `maxDepth` 限制（默认 **3**，约 3–4 名观众；`PR_MAX_DEPTH` 可在 [1,6] 内调整），且每增加一跳增加约一个分片时长（2s）的延迟。这是物理上限，不是实现缺陷。

实现要求：
- 服务端只用 `internal/tracker` 聚合的**实测值**计算 `Ki`，不信任客户端自报的"我能服务 N 个"（C11）。
- 房间满员时**拒绝**新成员并返回 `ROOM_FULL` + 当前容量（而不是让所有人一起卡）。
- 主播端 UI 显示：当前模式、上行占用 / 剩余容量、当前码率下的最大人数。

### 6.3 单链分发模式：选举、换防、失效恢复

`K0 = 1` 时主播只有 1 个槽位，**槽位不能先到先得**——否则第一个进房的人会占住唯一位，而他可能上行最差，等于把整个房间交给最弱的节点分发。槽位必须由**实测上行能力**决定。

**选举依据（只用可测量值，C11）**

```go
// internal/topology/election.go
type DistributorCand struct {
    PeerID            string
    UploadCapacityBps int64   // getStats().availableOutgoingBitrate
    MeasuredUpBps     int64   // 子节点上报的实测交付吞吐（交叉验证）
    Stability         float64 // 在线时长/重连次数归一化，0-1
}

// 能否承担全房间分发：容量需覆盖除自己以外的全部成员 × 码率
func CanDistribute(c DistributorCand, memberCount int, streamBps int64) bool {
    need := int64(float64(memberCount-1) * float64(streamBps) / safetyFactor)
    cap := min64(c.UploadCapacityBps, c.MeasuredUpBps) // 取小值（保守）
    return cap >= need && c.Stability >= 0.7
}
```

**三阶段时序**（解决"刚进房没有实测数据"）：

1. **预热期**（进房 0–10s）：尚无实测数据，主播槽位先给第一个到达者，房间照常可看，不阻塞体验。
2. **再选举**（每 5s）：服务器用实测数据重算所有成员的 `CanDistribute`。若存在显著更优的候选（`cap(cand) ≥ 1.5 × cap(D)` 且 `CanDistribute(cand)` 为真）→ 触发**换防**。
3. **换防（平滑轮换，不断流）**：
   - 新候选 `D'` 先以**普通子节点**身份挂到 D（或主播，视槽位而定），把预取窗口拉满，补齐目标窗口的分片；
   - 待 `D'` 的 `have` bitset 覆盖目标窗口（约 30–60s 数据量；预取窗口临时扩到 60）后，服务器下发 `distributor-change`：`D'` 提升为分发节点、`D` 降级为 `D'` 的子节点；
   - 全程**不强断任何连接**，子节点靠 2s 抖动缓冲 + 扩大后的预取窗口度过切换；
   - 回滚阈值：单次换防窗口内 stall > 1 视为换防失败，回滚到原 D 并进入 30s 冷却。

**失效恢复（C12）**：D 掉线时，服务器按同一判据选下一个满足 `CanDistribute` 的节点接任；若无人满足，主播先接管唯一位服务 1 个节点（容量暂时收缩），并广播 `ROOM_DEGRADED` + 具体原因与建议（降码率 / 减少观众）。

**换防与恢复都不需要任何一端重下整部片子**：主播始终持有全部分片，接任者只需补齐窗口内的分片。

### 6.4 抗慢节点的四层机制

纯树结构的缺陷是：一个慢父节点会让**其整棵子树**同时卡顿。本设计用四层机制把它降级为"局部、短暂、可恢复"：

| 层 | 机制 | 效果 |
| :--- | :--- | :--- |
| L1 | **分片级条带化**：窗口内分片按父节点可用容量加权分配给 primary + backup 并行拉取 | 慢节点只拖慢它份额内的分片，其余分片从其他父节点正常到达 |
| L2 | **超时转投**：单分片 3s 超时即转投下一个父节点，最多 3 个父 | 慢节点的份额被自动抽走，不阻塞播放 |
| L3 | **容量感知分配**：新节点只挂到 `Ki` 仍有冗余的父节点 | 不制造新的拥塞父节点，避免"越挂越慢"的正反馈 |
| L4 | **健康度重挂载**：每 5s 评估子节点的 `bufferHealth` 与 `p95 交付延迟`；若持续恶化且存在有冗余的候选父，则重新分配 primary | 长期劣化的父节点被逐步摘除，影响半径收敛 |

**健康度与重挂载判据**

```go
// internal/topology/health.go
type ChildHealth struct {
    PeerID        string
    BufferHealth  float64 // 已就绪分片数 / 窗口大小，0-1
    P95DeliveryMs float64 // 最近 20 次分片交付 p95 延迟
    StallCount    int     // 最近 60s 内 stall 次数
}

// 触发重挂载
func ShouldRebalance(h ChildHealth, candidateSpare float64) bool {
    degraded := h.BufferHealth < 0.6 || h.P95DeliveryMs > 3000 || h.StallCount >= 2
    return degraded && candidateSpare >= 1.0 // 候选父至少有 1 个子节点的冗余
}
```

**防环**：`parent-assignment` 由服务端集中计算，客户端只能接受；服务端在分配前沿 `primaryParent` 链向上回溯，若候选父在待分配节点的子树内则跳过（树内无环保证）。

### 6.5 父节点评分（延迟优先）

沿用"多维加权"，但**只用可测量、可验证的量**：

```go
// 公式口径。实现落点是 internal/usecase/assign.go 的 allocNode.score() / 权重常量
// （weightRTT / weightStability / weightSpare）；RTT 来自客户端经 DataChannel 实测的
// P2P 往返（不是经隧道的信令 RTT），因此可作为调度依据。
type PeerScore struct {
    RTTMs              float64 // DataChannel ping/pong 实测
    Stability          float64 // 在线时长 / 重连次数归一化，0-1（0 视为"未知"，不惩罚）
    SpareCapacityRatio float64 // 剩余上行冗余 / 上行总量，0-1（C10 的关键项）
    // 下面两项不在父节点评分里，它们属于客户端逐分片择父（§5.3、§6.4 L1）：
    ThroughputBps      float64 // 实测交付速率
    ChunkCoverage      float64 // 对目标窗口的覆盖率（bitset 实算），0-1
}

// 权重：延迟 0.50 / 稳定性 0.25 / 余量 0.25（和为 1）
func CalculateScore(s PeerScore) float64 {
    rtt := 1.0 / (1.0 + s.RTTMs/100.0)   // 100ms → 0.5（形状不变，只改权重）
    return rtt*0.50 + s.Stability*0.25 + s.SpareCapacityRatio*0.25
}
```

| 维度 | 旧权重 | 新权重 | 依据 |
| :--- | ---: | ---: | :--- |
| `RTT`（延迟） | 0.30 | **0.50** | 目标 = 延迟与卡顿优先；跨运营商正是 RTT 方差最大的场景，同一层里 30ms 与 180ms 的父节点决定起播快慢与追帧能否追上 |
| `Stability` | 0.20 | **0.25** | RTT 相近时"会不会掉线"比"还剩几个位"更值钱 —— 重挂载本身就是一次可感知的卡顿 |
| `SpareCapacityRatio` | **0.50** | 0.25 | 不再是最高权重，但**仍然必须存在**：见下面的硬排除 |

**余量的两条保护（不是只靠权重）**：

1. **硬排除**：`spare() <= 0` 的父节点直接出局，权重再高也不能被选中。只按 RTT 排序会把新节点持续挂到同一个最快的父节点上直到把它压垮，那时它自己的 RTT 会立刻变差，反过来把整棵子树拖成卡顿。权重负责"偏好"，硬排除负责"不许越界"。
2. **同深度才比分数**：先按深度择父（树越浅，跳数与故障半径越小），同一层内才比上面三项。

> 与原始提案的差异（2026-02 更新）：原始提案的评分**只有"这个节点多快"**，于是新节点会持续挂到同一个最快的父节点上直到把它压垮 —— 这正是"一个节点拖慢所有节点"的成因，也是 `SpareCapacityRatio` 被引入的原因。
> 但产品目标随后明确为**「延迟与卡顿优先、观众跨运营商」**，权重因此从"余量最高"改成"RTT 最高"，余量退到 0.25 并保留硬排除。前一次行为变更的证据（同一组候选在旧/新权重下的得分与选择结果对照表）见 `internal/usecase/assign_test.go` 的 `TestScoreWeightsLatencyFirstFlipsChoice`。

**深度上限**：`maxDepth` 默认 **3**（`PR_MAX_DEPTH`，允许 [1,6]，非法值启动即报错）。每跳中继实测给端到端额外加上 58–78ms（`docs/ALGORITHM.md` §2.1），4 跳最坏再叠约 300ms；深度与容量是对价关系，需要更大房间时显式抬高。

### 6.6 为什么是生成树而不是网状（已评估并否决的备选）

多父网状（每个节点同时从多个父取数、任意节点之间都可能建连）在这个产品里被**明确否决**，理由三条：

1. **带宽守恒**：观众的上行是固定预算，网状不会凭空造出带宽。把一个 2 Mbps 的分片同时从 3 个父节点拉过来，等于把同一条数据在链路上复制 3 份，房间总容量不变而总流量翻倍 —— 卡顿只会更早发生。
2. **打洞更差**：网状意味着 O(N²) 条候选边。IPv4 对称 NAT 下每条边都要一次 ICE 打洞尝试，边数一多，失败与重试的绝对数量随之上升；而真正被判为"可用"的边并没有变多，只是把更多时间花在了注定失败的握手上（TURN 已退役，没有兜底中继）。
3. **全局状态路由在隧道下必然过期**：当前部署形态下信令要经隧道往返 0.7–2.5s。基于"全局最新状态"做路由决策（谁最空闲、谁缺哪片）在 1s 级别的陈旧度下就是猜；而树只需要一次本地决策（我的父是谁），陈旧度的影响被限制在单条边上。

**结论**：继续用**树**（广度优先 + 同层延迟优先 + 深度上限 3）。
若未来要在 IPv4 对称 NAT 之外提高修复能力，方向是**「树 + 每节点 1–2 条备用边」**（就是现在 `backupIds` 的角色：主父慢/缺片时补取），**而不是全网状** —— 它保留了树的 O(N) 边数与本地决策，只在真正需要时用少量额外边换一次补救机会。

---

## 7. 播放同步

### 7.1 权威与时钟模型

- 主播是**唯一时间权威**。`progress` 与 `time-sync` 携带主播单调时钟 `hostClockMs`（`performance.now()` + 基准偏移），不带各跳本地时间。
- 观众目标播放位置：`expectedHostTime = lastProgress.currentTime + (estHostNow() - lastProgress.hostClockMs)/1000`。
- 关键点：观众需要估的是**自己与主播时钟的偏移**，而不是"网络延迟"。两者在数学上等价，但偏移可以用滤波稳定估计，单次延迟测量不能（C4）。
- `clockEpoch`（C13）：主播页面每次加载生成一个随机纪元串，随 `progress`/`time-sync`/入房快照下发。观众一旦发现纪元变化，必须**丢弃并重新初始化**最小滤波 —— 否则主播刷新页面后，`performance.now()` 归零而滤波仍保留旧偏移，全房间会一起跳到错误位置。
- **主播断线 ≠ 主播离开（房间生命周期）**：WebSocket 断一次（网络抖动 / 页面刷新 / 服务端重启 / 半开连接）
  不代表主播要走，因此主播连接断开时房间**不销毁**，而是进入**宽限期**（`PR_ROOM_HOST_GRACE`，默认 `60s`）：
  `hostId` 置空、房间标记为"主播离线"，但 `members` / 分片索引 / 最近播放状态与 `seq` 全部保留，
  `members` 快照里不再有主播（客户端据此判定"当前无主播"），只广播 `member-left`，**不**广播 `room-closed`。
  宽限期内主播凭同一房间码 + 密码重新 `join`（`role=host`）即恢复，`seq` **继续单调递增**（不回退、不重置，
  否则观众的 `seq` 过滤会把恢复后的进度全部丢掉）；宽限期到期仍无主播才广播 `room-closed` 并销毁房间
  （`message` 说明"主播离线超过 N 秒"），房间码随后可被重新创建。
  宽限期内房间可能只剩元数据（几 KB）也**不**提前回收：那点元数据正是"房间码还能用"的全部依据。
  宽限期内观众可以继续进出房间：`ROOM_NOT_READY` 只拦"主播从未进房"的房间；
  晚到的观众拿到的是保留的 `mediaIndex` 与主播断线前的播放状态，界面据此显示"等待主播重连"，
  容量闸门沿用主播断线前的实测值（宽限期内不重算拓扑，等主播回来再算）。

### 7.2 时钟偏移估计（NTP 最小滤波）

```
每次收到 progress/time-sync：
    sample = localNow() - hostClockMs          // 含正向延迟，恒 ≥ 真实偏移
    offset = min(offset, sample)               // 最小滤波：延迟最小的一次最接近真值
    offset 随窗口 30 次衰减，允许缓慢漂移跟踪
```

`expectedHostTime = currentTime + (localNow() - offset - hostClockMs)/1000`

### 7.3 抖动缓冲与目标延迟

- 观众端维持 **2.0s 目标缓冲**（可配置 1.5–3.0s）：这是"用延迟换稳定"的取舍，也是树状分发逐跳累积延迟与**换防切换**的补偿空间。
- **起播门控**：从**主播时间戳所在的分片**起，必须有 `n = 4` 片（2s/片 ≈ 8s）**连续且完整**的缓冲，
  且时钟偏移样本 ≥ 6、已稳定 0.8s，才允许开闸。
  判据必须是"连续完整分片"而不是"缓冲秒数"或"追加游标"：后两者会被零散片段与中间缺口骗过，起播后立刻卡住。
  跳转/换父后的重新开闸阈值降到 `n = 2`（避免每次跳转都长时间等待）。
- **门控不设超时上限**：上游没数据就一直显示"缓冲中 x"，等待超过 20s 时提示"上游带宽可能不足"。
- `seq` 过滤（C5）：丢弃 `seq <= lastSeq` 的进度消息。

### 7.4 漂移矫正三级策略（C9）

每 100ms 执行：

```
drift = video.currentTime - expectedHostTime

|drift| ≤ 0.10s   → playbackRate = 1.00
落后 0.10–6.00s   → playbackRate = 1 + min(0.25, |drift| × 0.08)   （分级加速追赶，上限 +25%）
超前 0.10–6.00s   → playbackRate = 1 - min(0.10, |drift| × 0.20)
6.00 < |drift| ≤ 9.00 → 跳 SourceBuffer 内的最近关键帧（不 seek，不重灌）
|drift| > 9.00s   → 清 buffer + seek 到目标位置重灌（代价最高，记录一次 sync-reset 指标）
```

**为什么速率区间从 ±5% 放宽到 +25%**：±5% 只够抹平几十毫秒的抖动。落后 5 秒时按 5% 要 100 秒才追平 ——
表现就是"永远慢一截，直到某次大幅 seek"。分级加速后 5 秒缺口约 20 秒平滑追平，且不需要清缓冲。

`seek` 与 `rate` 的房主控制语义：房主操作立即广播 `room-control`，所有端**清空缓冲并按同一目标位置重灌**，保证动作一致（而不是让每个端各自缓慢漂移过去）。

### 7.5 中继与分发节点的职责

1. 把 `progress` / `time-sync` 向下游转发：**不改** `currentTime`、`hostClockMs`、`seq`，
   但必须把 `parentClockMs`（自己的本地时钟）与 `parentOffsetMs`（自己到主播的偏移估计）改写成本节点的值。
   子节点据此把偏移估计做成"本跳 + 父节点"**两级相加**，误差从"整条路径的单向延迟"降到"各跳半 RTT 之和"。
2. 自己按 §7.4 同步本地播放（转发节点也是观众，同步标准与叶子一致）。
3. 按 §5.3/§5.4 服务子节点的分片请求（从自己已拥有的分片里取）。
4. **分发节点额外职责**：换防期间继续服务既有子节点，直到它们完成重挂；收到 `distributor-change` 后不得中断任何 DataChannel，只改变"谁服务谁"的角色分工。
5. **权威基准单一**：只有主父的进度可以进入时钟估计与播放矫正。备用父、以及换防前遗留的直连都会送来同一份进度，
   混用会让偏移估计在两套口径之间来回跳；换父时必须作废本跳样本（它们是相对旧父时钟测的）。

### 7.6 卡顿恢复策略

树结构下"一个慢父节点拖慢整棵子树"，恢复按以下顺序逐级升级：

```
1. 客户端检测到缓冲耗尽 / 卡顿（stall）
2. 上报 metrics { degraded, stallCount, bufferHealth }  → 服务端把该节点的当前主父标为"避开"并立即重算路径
   （只在 stallCount **增加**时触发，且 3s 限流：门控等待也会上报 degraded，按它换路会让整屋不停搬家）
3. 新路径到位（parent-assignment）：本跳时钟样本作废、取数状态复位；
   若当前处于不健康状态则按"连续 n = 2 片"重新开闸
4. 加速追赶：按 §7.4 的分级速率平滑追平（不再只有 ±5%）
5. 滞后超过 12s：提示用户并直接跳到主播当前进度
6. 若跳转目标分片超时（2.5s）取不到（全网都还没有）：改按**主播当前进度**重新取片，
   游标一并前移，避免盯着一个永远不来的分片无限等待
```

---

## 8. 数据模型

### 8.1 服务端（Go）

```go
// internal/room/room.go
type Member struct {
    ID          string
    DisplayName string
    Role        string    // "host" | "viewer"
    Depth       int       // 在树中的深度，host = 0
    PrimaryID   string    // 主父
    BackupIDs   []string
    JoinedAt    time.Time
    LastSeen    time.Time
}

type Room struct {
    ID            string
    Password      string // 空 = 无密码
    HostID        string
    Members       map[string]*Member
    MediaIndex    *media.Index  // 开播后固定，中途换片需重开房间
    Mode          topology.Mode
    DistributorID string        // 单链模式下的分发节点（fanout 模式为空）
    Capacity      Capacity      // §6.2 计算结果
    CreatedAt     time.Time
}
```

```go
// internal/tracker/peer.go
type PeerState struct {
    PeerID            string
    Have              *Bitset // 分片拥有情况
    Complete          bool
    RTTMs             float64
    ThroughputBps     float64
    UploadCapacityBps float64 // 来源：getStats().availableOutgoingBitrate（C11）
    MeasuredUpBps     float64 // 来源：子节点上报的实测交付吞吐（交叉验证）
    Children          []string
    UploadedBytes     int64 // 服务端累计的实际转发量
    LastSeen          time.Time
}
```

### 8.2 前端（Pinia，`stores/room.ts`）

```ts
interface RoomState {
  clientId: string; roomId: string; role: 'host' | 'viewer';
  ws: WebSocket | null;
  peers: Map<string, RTCPeerConnection>;
  channels: Map<string, RTCDataChannel>;
  mediaIndex: Index | null;
  localFiles: Map<number, File>;     // 主播：分片索引 → File
  haveBitset: Uint8Array;            // 本节点已拥有分片
  window: { start: number; end: number };
  inflight: Map<string, { idx: number; rid: string; startedAt: number }>;
  sync: { offsetMs: number; lastSeq: number; expectedHostTime: number; paused: boolean; rate: number };
  members: Member[]; chat: ChatMessage[];
  parents: { primary: string | null; backups: string[] };
  topology: { mode: 'fanout' | 'chain'; distributorId: string | null; depth: number };
  metrics: { stall: number; syncReset: number; bufferHealth: number; p95DeliveryMs: number };
}
```

---

## 9. 前端结构

### 9.1 目录

```
client/
├── src/
│   ├── components/
│   │   ├── RoomView.vue          # 房间布局（左播放器 / 右聊天+成员）
│   │   ├── PlayerStage.vue       # <video> + 缓冲/同步状态浮层
│   │   ├── HostPanel.vue         # 选片目录、开播、模式/容量/上行占用显示
│   │   ├── TopologyBadge.vue     # 当前模式（扇出/单链）、分发节点、我的深度
│   │   ├── MemberList.vue        # 成员、角色、深度、缓冲健康度
│   │   └── ChatPanel.vue         # 文字聊天
│   ├── composables/
│   │   ├── useSignaling.ts       # WS 连接、重连、消息路由（含 seq 过滤）
│   │   ├── useWebRTC.ts          # PeerConnection/DataChannel 生命周期、ICE、getStats 采集
│   │   ├── useMediaIndex.ts      # 选目录、解析 index.json、isTypeSupported 校验
│   │   ├── useChunkStore.ts      # 分片持有（File / 收到的 ArrayBuffer）+ bitset
│   │   ├── useChunkRequester.ts  # 二进制协议编解码、请求/超时/转投
│   │   ├── useChunkScheduler.ts  # 预取窗口、多父加权条带化、在途上限、背压
│   │   ├── useChunkPlayer.ts     # MediaSource/SourceBuffer、append 队列、remove+seek
│   │   └── useSyncClock.ts       # NTP 最小滤波、漂移三级矫正
│   ├── stores/room.ts
│   └── views/HomeView.vue        # 建房 / 加入房
└── vite.config.ts                # /ws 与 /api 代理到 :8080
```

### 9.2 前端与后端的契约边界

- 前端**不信任**后端给的容量数字用于自行挂载，只使用 `parent-assignment` 的结果；**不得自行提升为分发节点**，只能由 `distributor-change` 任命。
- 前端**必须**在开播前做 `MediaSource.isTypeSupported(index.mimeType)` 校验并给出可读错误。
- `uploadCapacityBps` 由 `useWebRTC` 每 5s 从 `getStats()` 采集后上报；不上报任何未测量的估计值。
- 所有分片数据留在内存/File 中，**不**通过服务端；WS 只走 JSON 文本帧。

### 9.3 自动播放策略（C6）

- 房间页首屏是一个"**进入放映室**"按钮：点击时创建 `MediaSource`、绑定 `<video>` 并调用一次 `play()` 以解锁音频播放权限。
- 若用户未交互就收到 `play` 控制，UI 显示"点击继续播放"而不是静默失败。

---

## 10. 里程碑与验收标准

每个里程碑都必须能独立演示，验收标准可测量。

### M1 基建 + 房间（无视频）

| 项 | 内容 |
| :--- | :--- |
| 交付 | `git init` + `.gitignore`；修复 `cmd/wire.go`（当前空 `wire.Build()` 编译不过）；依赖锁定 `gin`、`coder/websocket v1.8.15`、`google/wire v0.7.0`；`internal/room` + `internal/signal` + `/ws` 路由；Vue3+Vite 骨架 + 房间页 + 聊天 + 成员列表 |
| 验收 | `go vet ./...` 通过；两个浏览器窗口进同一房间，消息/成员/控制实时同步；断线后成员列表正确移除 |

### M2 单跳直连：分片 + MSE + 同步（**此时已是"能用的放映室"**）

| 项 | 内容 |
| :--- | :--- |
| 交付 | `cmd/segmenter`；`useMediaIndex` / `useChunkRequester` / `useChunkPlayer` / `useSyncClock`；观众直连主播（星形，不含树、不含选举） |
| 验收 | 主播播放预处理目录 → 观众 **1–3s 内起播**；**进度偏差 < 500ms**（连续观察 5 分钟，记录 max/p95）；房主暂停/跳转全员跟随；`isTypeSupported` 不通过时给出明确错误；**容量闸门生效**：主播上报实测上行后，服务端算出的 `K0` 与前端显示一致，且超出 `1+K0` 的观众加入时被拒（`ROOM_FULL`） |

### M3 多层树 + 单链模式 + 多父条带化

| 项 | 内容 |
| :--- | :--- |
| 交付 | `internal/topology`（模式判定、容量模型、评分、分配、防环、选举、换防、失效恢复）；`have` bitset 广播；多父加权条带化调度；超时转投；健康度重挂载 |
| 验收 A（扇出） | 主播上行充足 → 3 层拓扑（主播 → 3 个 relay → 每个 relay 2 个 leaf）全部起播 |
| 验收 B（单链） | **把主播上行压到 `1.5×B`** → 服务器自动进入单链模式，主播**只有 1 个子节点**（分发节点 D），其余成员全部挂在 D 的子树下并全部起播 |
| 验收 C（换防） | 在 B 场景中再加入一个上行更强的节点 → 30s 内完成换防（`distributor-change`），**期间 stall ≤ 1**，且全房间同步偏差仍 < 500ms |
| 验收 D（抗慢节点） | **把 1 个 relay 上行限速到 1 Mbps** → 其子节点 5s 内通过 backup 父恢复播放，且**其他分支同步偏差不受影响**（< 500ms）——§6.4 的核心验证 |
| 验收 E（失效） | 杀掉分发节点 D → 5s 内接任（或 `ROOM_DEGRADED` + 原因），其他成员播放不中断 |

### M4 稳健性与可观测

| 项 | 内容 |
| :--- | :--- |
| 交付 | 掉线清理与重连恢复、缺失分片兜底、监控面板（拓扑树、当前模式、分片覆盖率、同步偏差、stall 次数）、**STUN 服务端探测与 TTL 下发**（`internal/service/ice`；`/api/ice` 与建房间响应带 `ttlSeconds`/`expiresAt`/`probe`） |
| 已退役 | **TURN**：字段、`PR_TURN_*` 环境变量与 `turn:` 下发条目全部删除（原因与重新引入条件见 README「为什么不再有 TURN」） |
| 验收 | 中途杀掉主父节点，子节点在 5s 内重挂载并恢复；房间满员返回 `ROOM_FULL`；监控面板数值与实际一致；`/api/ice` 的 `probe.scores` 与真机实测一致且不含任何 `turn:` 项 |

---

## 11. 风险与应对

| 风险 | 影响 | 应对 |
| :--- | :--- | :--- |
| 浏览器编解码差异 | 部分浏览器不支持某 profile → 黑屏 | 开播前 `isTypeSupported` 硬校验；`cmd/segmenter` 输出 `avc1/Main` 或 `High` 基线 profile |
| 主播上行不足 | 第 1 层就卡 | 模式判定 + 单链分发（§6.3）+ 开播前容量提示；`ROOM_FULL` 拒绝超额成员 |
| **分发节点是单点故障**（C12） | D 掉线 → 全房间断流 | 实测选举 + 平滑换防 + 失效接任 + 2s 缓冲 / 60s 预取窗口（§6.3） |
| **房间内无强上行节点** | 单链模式也带不动，容量塌缩到 1–2 人 | §4.5 低码率预设重新预处理；UI 明确告知容量上限与原因 |
| 换防本身引发抖动 | 切换瞬间 stall | 预取窗口临时扩到 60；stall > 1 即回滚 + 30s 冷却（§6.3） |
| 树状逐跳延迟累积 | 深层观众落后 | `maxDepth=3`（默认，`PR_MAX_DEPTH` 可在 [1,6] 内调）+ 2s 抖动缓冲 + 每跳只转发控制消息不重编码 |
| DataChannel 背压 | 内存膨胀 / 卡死 | `bufferedAmount` 检查 + 在途上限 16 + 超时转投（C7） |
| MSE seek 抖动 | 画面跳变 | 三级矫正策略，优先速率微调（C9） |
| `google/wire` CLI 无法在受限环境运行 | M1 阻塞 | `wire` 已在本机 module cache（`v0.7.0`）；若构建缓存不可写则退化为手写 `wire_gen.go`，接口不变 |
| 分片哈希校验开销 | 主播端 CPU 占用 | 校验只在 `cmd/segmenter` 生成时做一次；传输期不做逐片哈希（WebRTC 已有完整性与加密） |

---

## 12. 待定问题

| # | 问题 | 影响 |
| :--- | :--- | :--- |
| O1 | **已决策**：`U < 2×码率` 时进入单链分发模式，由唯一的、按实测上行选举出的分发节点承担全房间分发；不使用服务器中继（§6.1、§6.3） | — |
| O6 | §4.5 低码率预设是否纳入 M2？（长视频转码耗时可达数分钟） | 决定 M2 是否包含 `cmd/segmenter -preset lowup` 与开播前容量预判 UI |
| O2 | **已决策（TURN 退役）**：私有 TURN（`turn:10.23.170.132:3478`）对公网观众不可达，且隧道不转发 UDP，实测无收益 → 彻底删除 TURN 字段/环境变量/下发条目，改为服务端探测 STUN 并下发 TTL；重新引入的条件见 README「为什么不再有 TURN」 | — |
| O3 | 弹幕是否列入下一轮？（本轮仅预留协议） | 影响前端工作量与协议扩展 |
| O4 | `cmd/segmenter` 是否接受"需要主播装 ffmpeg 并跑两条命令"作为产品前提？ | 替代方案是浏览器内 mp4box.js 自动 remux，零预处理但复杂度和不确定性更高 |
| O5 | 是否要房间密码 / 邀请码？ | 影响 M1 的 `join` 校验分支 |
| O7 | 换防评估周期（默认 5s）与换防阈值（默认 1.5×）是否需要可调？ | 影响 `internal/config` 项与 M3 调参成本 |

---

## 13. 与原始提案的差异清单（便于你复核）

| # | 原始提案 | 本文档 | 原因 |
| :-- | :--- | :--- | :--- |
| D1 | 256KB 固定大小分片 | 按 `moof` 边界切、约 2s/片 | C1、C2：固定切分无法被 MSE 接受 |
| D2 | 分片 base64 走 JSON | 二进制帧 + 4 字节头 | 省 33% 带宽与编码 CPU |
| D3 | 每跳用本地时间戳重写进度 | 逐跳原样转发主播 `hostClockMs` + `seq` | C4、C5：本地时间戳不可跨机比较 |
| D4 | 播放进度差直接 seek | 三级矫正（速率 → 跳关键帧 → seek） | C9：seek 代价高，会打断缓冲 |
| D5 | 评分只看"节点多快" | 新增 `SpareCapacityRatio` 权重 0.25 | C10：只看速度会导致持续挂载同一父节点直至压垮。**后续修订**：产品目标改为延迟优先后，权重次序变为 RTT 0.50 / 稳定性 0.25 / 余量 0.25（余量让出最高权重，但保留 `spare()<=0` 硬排除），见 §6.5 |
| D6 | 纯树分发 | 树状主父 + 多父条带化 + 超时转投 | C10：纯树无法阻止慢节点拖垮子树 |
| D7 | 服务器承担 Tracker + 拓扑 + 信令 + TURN | 相同，但**明确不中继视频**，容量硬约束；且 TURN 已退役（只保留 STUN，服务端探测打分后下发） | 与你的决策一致；TURN 退役的实测依据见 README |
| D8 | 声称可撑 20000 并发 | 容量模型按上行实算，示例约 12 人 | C8：星形/树的瓶颈在上行，不在服务器 |
| D9 | 未定义低上行场景 | **单链分发模式**：`U < 2×B` 时主播只服务 1 个分发节点，按实测上行选举、支持平滑换防与失效接任（§6.1、§6.3） | 你确认的策略；且容量模型在该区间本就给出 `K0 = 1` |
| D10 | 容量与选举依赖客户端自报 | 只用 `getStats().availableOutgoingBitrate` + 子节点实测吞吐交叉验证，取小值（C11、§6.3） | 自报值不可信，会把房间交给最弱的节点 |



