# 服务端视频切片（一次性预处理）

> 一句话：**服务器只在开播前接触视频字节**。用户没有 ffmpeg 时，把源视频上传到服务器切成
> 放映室可用的分片，再一次性下载回本地；开播之后，分片仍然只在 peer 之间经 WebRTC
> DataChannel 流动，服务端一个字节都不中转。
>
> 相关代码：`internal/service/segment/`（流水线与作业队列）、`internal/handler/segment.go`（HTTP 端点）、
> `cmd/segmenter/`（本地 CLI，与服务器共用同一份流水线）。

---

## 0. 它和「服务器不传输任何视频字节」的关系

不变量 **I1**（[`ALGORITHM.md`](ALGORITHM.md) §0）说的是**直播链路**：观众拉分片时，字节只在
peer 之间走，服务器不持有、不转发。[`SPEC.md`](SPEC.md) §1.1 目标 7 同理。

本服务处理的是**开播前的媒体准备**，两条链路完全分离：

```
准备阶段（本服务）        用户 --源视频上传--> 服务器切片 --zip/manifest 下载--> 用户本地 room-media/
直播阶段（P2P）          主播 <==WebRTC DataChannel==> 观众          （服务器只转发信令/拓扑/聊天）
```

因此：

- 服务器只在"准备媒体"阶段接触视频字节，**直播期仍然零视频流量**；
- 产物下载完就可以删，服务器上的副本 30 分钟后自动清理（`PR_SEGMENT_TTL`）；
- 如果主播本地有 ffmpeg，**优先本地切**（§5）：更快，也不占用服务器磁盘与 CPU。

---

## 1. 快速开始（curl）

### 1.1 提交作业

```bash
curl -X POST http://127.0.0.1:8080/api/v1/segment/jobs \
     -F "file=@movie.mp4"
```

```json
{"jobId":"7KQ2M9XR4T8A","state":"queued","progress":0,"queuePosition":1}
```

- 字段名固定为 `file`；`state` 可能是 `queued` / `running`；
- 服务器没装 ffmpeg 时仍返回 `202`，但 `state` 立刻是 `failed`，并带 `error` 说明（见 §4）。

### 1.2 轮询状态

```bash
curl http://127.0.0.1:8080/api/v1/segment/jobs/7KQ2M9XR4T8A
```

```json
{"jobId":"7KQ2M9XR4T8A","state":"running","progress":0.42}
{"jobId":"7KQ2M9XR4T8A","state":"done","progress":1,
 "result":{"bytes":94371840,"segments":812,"singleResponse":true}}
{"jobId":"7KQ2M9XR4T8A","state":"failed","progress":0,
 "error":"segment: 源视频超过时长上限：视频 73.4 分钟，上限 60 分钟"}
```

`progress` 是 0–1 的完成度。粗粒度是刻意的：切片本身没有可靠的细粒度回调。

### 1.3 取产物

```bash
curl -OJ http://127.0.0.1:8080/api/v1/segment/jobs/7KQ2M9XR4T8A/result
```

产物总大小 **≤ 1 GiB**（默认，可配）→ `200 application/zip`，zip 内含：

```
index.json          # 分片索引（SPEC §4.3）
init.mp4            # ftyp + moov，MSE 的首个 appendBuffer
c00001.m4s …        # 按 moof 边界切出的媒体分片
```

产物总大小 **> 1 GiB** → `200 application/json` 的 manifest：

```json
{
  "jobId": "7KQ2M9XR4T8A",
  "bytes": 1610612736,
  "segments": 14000,
  "singleResponse": false,
  "parts": [
    {"n":1,"bytes":943718400,"sha256":"3f0c…","url":"/api/v1/segment/jobs/7KQ2M9XR4T8A/parts/1"},
    {"n":2,"bytes":666894336,"sha256":"a91b…","url":"/api/v1/segment/jobs/7KQ2M9XR4T8A/parts/2"}
  ]
}
```

### 1.4 分批下载与校验

```bash
curl -o part1.zip http://127.0.0.1:8080/api/v1/segment/jobs/7KQ2M9XR4T8A/parts/1
sha256sum part1.zip          # 与 manifest 里的 sha256 逐字节比对
unzip -o -d ./room-media part1.zip
# 逐份下载、逐份解压到同一个目录即可：每份覆盖的产物文件互不重复
```

- 每一份都是 zip，`Content-Length` 与 `X-Segment-Sha256` 响应头与 manifest 一致；
- 同一个作业重复下载的字节流**完全一致**（zip 头时间戳固定），所以 sha256 可以放心用作校验；
- 每份都严格小于单次返回上限（默认 1 GiB）。

下载完 `./room-media/` 就是主播端要加载的分片目录（SPEC §4.4），前端零改动。

---

## 2. 端点与状态码

| 方法 | 路径 | 成功响应 |
| :--- | :--- | :--- |
| `POST` | `/api/v1/segment/jobs` | `202` + `{jobId, state, progress, queuePosition, error?}` |
| `GET` | `/api/v1/segment/jobs/{id}` | `200` + `{jobId, state, progress, error?, result?}` |
| `GET` | `/api/v1/segment/jobs/{id}/result` | `200` zip（≤ 上限）或 `200` JSON manifest（> 上限） |
| `GET` | `/api/v1/segment/jobs/{id}/parts/{n}` | `200` zip（第 n 份） |

错误体与房间接口同一形状：`{"error":"可读文案","code":"错误码"}`。

| 状态码 | 错误码 | 触发条件 |
| :--- | :--- | :--- |
| `202` | — | 请求已被接受（注意：可能立刻 `state=failed`，例如服务器没装 ffmpeg） |
| `400` | `BAD_REQUEST` | 不是合法的 multipart，或缺少 `file` 字段 |
| `404` | `SEGMENT_JOB_NOT_FOUND` | 作业不存在，或已经过了 TTL 被清理 |
| `404` | `SEGMENT_NO_PARTS` | 该作业走单次返回，没有分批下载 |
| `404` | `SEGMENT_PART_NOT_FOUND` | 分批编号不存在 |
| `409` | `SEGMENT_JOB_NOT_READY` | 作业还没完成（`queued`/`running`），或已经失败 |
| `413` | `SEGMENT_SOURCE_TOO_LARGE` | 源文件超过 `PR_SEGMENT_MAX_SOURCE_BYTES`（默认 16 GiB） |
| `422` | `SEGMENT_SOURCE_EMPTY` | 上传的文件是 0 字节 |
| `422` | `SEGMENT_DURATION_TOO_LONG` | 时长超过 `PR_SEGMENT_MAX_DURATION`（默认 60 分钟） |
| `422` | `SEGMENT_PROBE_FAILED` | ffprobe 解不开这个文件（不是视频 / 文件损坏） |
| `429` | `SEGMENT_QUEUE_FULL` | 排队队列已满（默认 8） |
| `429` | `SEGMENT_RATE_LIMITED` | 令牌桶已空（默认 3 作业/分钟） |
| `500` | `INTERNAL` | 服务端内部错误 |

`429` 一定带 `Retry-After`（整数秒）：队列满 → 5 秒；令牌桶空 → 距下一个令牌的秒数（≤ 20 秒）。

体积与时长校验都在**上传后立刻**做（ffprobe + 边写边判大小），不会等切完才发现。

---

## 3. 配额、保留期与配置项

| 配置项 | 默认值 | 环境变量 | 说明 |
| :--- | :--- | :--- | :--- |
| 并发作业数 | `2` | `PR_SEGMENT_CONCURRENCY` | 同时处于 `running` 的作业上限 |
| 排队队列长度 | `8` | `PR_SEGMENT_QUEUE_LENGTH` | 只算排队中的作业，不含运行中的；`0` = 不排队 |
| 令牌桶速率 | `3` /分钟 | `PR_SEGMENT_RATE_PER_MINUTE` | 按时间匀速补充 |
| 令牌桶容量 | `3` | `PR_SEGMENT_BURST` | 突发时最多连续接几个 |
| 产物/记录保留期 | `30m` | `PR_SEGMENT_TTL` | 到期后连临时目录一起删 |
| 清理扫描周期 | `1m` | `PR_SEGMENT_CLEANUP_INTERVAL` | 后台清理协程的节奏 |
| 单次返回上限 | `1GiB` | `PR_SEGMENT_SINGLE_MAX_BYTES` | 超过它改走 manifest，每份都小于它 |
| 源视频时长上限 | `60m` | `PR_SEGMENT_MAX_DURATION` | 超限 422 |
| 源文件大小上限 | `16GiB` | `PR_SEGMENT_MAX_SOURCE_BYTES` | 超限 413 |
| 分片目标时长 | `2` 秒 | `PR_SEGMENT_SECONDS` | 与 `cmd/segmenter -frag-sec` 同义 |
| 临时目录 | `%TEMP%\projectionroom-segment` | `PR_SEGMENT_TEMP_DIR` | 每个作业一个 `job-<id>/` 子目录 |
| ffmpeg 路径 | 自动发现 | `PR_FFMPEG` | 目录或可执行文件都可以 |

配额语义：

- **同时最多 2 个作业在切，最多 8 个在排队**；第 11 个请求直接 `429`（不会无限堆积）；
- 令牌桶是第二道闸门：即使队列空着，平均也只有 **3 个作业/分钟** 能被接受；
- **源文件**在上传后立刻落盘，作业一结束（无论成功还是失败）**立即删除**；
- **产物**保留到 TTL 到期；后台每分钟扫一次，同时清理上次进程被强杀留下的孤儿目录；
- 作业记录与产物同寿命：过期的作业查询返回 `404`（下载请趁早）。

---

## 4. 服务器侧：ffmpeg 的发现顺序与处理策略

发现顺序（`internal/service/segment/tools.go`）：

1. `PR_FFMPEG` —— 可以是可执行文件，也可以是包含 ffmpeg/ffprobe 的**目录**；
2. `exec.LookPath`（即 `PATH`）；
3. Windows 常见安装目录，包括本机实测的
   `D:\Program Files (x86)\ffmpeg-8.0.1-essentials_build\bin\`，以及
   `C:\ffmpeg\bin`、winget（`Gyan.FFmpeg`）、scoop、chocolatey 的常见位置。

找不到时：**进程启动时打一条 WARN 日志**，此后每个作业被接受（`202`）后**立即 failed**，`error` 就是那句：

```
服务器未安装 ffmpeg，你也可以在本地用 segmenter/ffmpeg 自行切片，见 docs/SEGMENT.md
```

处理策略（服务端自动判断，无需用户选）：

| 源编码 | 处理 | 耗时 |
| :--- | :--- | :--- |
| H.264 + AAC（或无音轨） | `-c copy` 无损重新封装成 fMP4，再按 moof 切 | 与文件大小成正比，通常几十秒内 |
| 其它（HEVC / AV1 / VP9 / 其它音频） | 转码成 `libx264`（`-preset veryfast -crf 23`）+ `aac 128k` 后再切 | 可能数分钟，因此时长上限 60 分钟 |

> 为什么必须这样：MSE 只认 H.264/AAC 的 `mimeType`（SPEC §4.3），其它编码切出来浏览器
> `appendBuffer` 会直接报错。

---

## 5. 本地自行切片（有 ffmpeg 时的推荐路径）

### 5.1 一步 ffmpeg 生成 fragmented MP4

```bash
# 无损重新封装（秒级，不重新编码）
ffmpeg -i input.mp4 -c copy \
  -movflags +frag_keyframe+empty_moov+default_base_moof \
  -frag_duration 2000000 \
  out_frag.mp4
```

参数含义：

| 参数 | 作用 |
| :--- | :--- |
| `-c copy` | 不重新编码（改容器不改码流） |
| `+frag_keyframe` | 每个分片都从关键帧开始 —— 这正是"能按 moof 边界切"的前提 |
| `+empty_moov` | 初始化段只放 `moov`，媒体数据全在分片里 |
| `+default_base_moof` | 用 `moof` 作为数据基准偏移，切分后仍能正确解码 |
| `-frag_duration 2000000` | 分片目标 2 秒（微秒）。若要 4 秒改成 `4000000` |

低上行预设（重新编码降码率，SPEC §4.5）：

```bash
ffmpeg -i input.mp4 -c:v libx264 -preset veryfast -b:v 1200k \
  -c:a aac -b:a 96k \
  -movflags +frag_keyframe+empty_moov+default_base_moof \
  -frag_duration 2000000 \
  out_frag.mp4
```

> 源视频不是 H.264/AAC 时先转码：`-c:v libx264 -c:a aac`。HEVC / AV1 / VP9 不能直接进 MSE。

### 5.2 `cmd/segmenter` 的三种用法

```bash
# 1) 已经是 fragmented MP4：直接按 moof 边界切（这一步不需要 ffmpeg）
go run ./cmd/segmenter -in out_frag.mp4 -out ./room-media

# 2) 普通 MP4：先无损重新封装成 fMP4，再切
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -fragment

# 3) 低上行预设：转码降码率后再切（长视频耗时数分钟）
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -transcode 1200k -uplink-mbps 3
```

| 参数 | 默认 | 说明 |
| :--- | :--- | :--- |
| `-in` | 必填 | 输入视频 |
| `-out` | `./room-media` | 输出目录 |
| `-fragment` | `false` | 输入是普通 MP4 时先无损重新封装 |
| `-transcode` | 空 | 低上行预设：转码到指定码率（如 `1200k`） |
| `-frag-sec` | `2` | 分片目标时长（秒） |
| `-uplink-mbps` | `12` | 仅用于打印容量提示（SPEC §6.1） |
| `-ffmpeg` | 空 | 指定 ffmpeg 路径（目录或可执行文件），优先于 `PR_FFMPEG` |

产物：

```
room-media/
├── index.json     # 分片索引
├── init.mp4       # ftyp + moov
├── c00001.m4s     # 从 moof 起始，含 mdat
└── …
```

本地 CLI 与服务端切片**共用同一份流水线实现**（`internal/service/segment`），
所以两条路径的产物格式完全一致，主播端"选择分片目录"的行为不需要任何改动。

### 5.3 常见报错与处理

| 报错 | 原因 | 处理 |
| :--- | :--- | :--- |
| `mp4: … 不是 fragmented MP4（没有 moof box），无法按分片边界切分` | 输入是普通 MP4 | 加 `-fragment`，或先跑 §5.1 的一步 ffmpeg |
| `mp4: 暂不支持 hvc1/hev1/vp09/av01（M2 只处理 H.264/AAC）` | 视频编码不是 H.264 | `ffmpeg -c:v libx264 -c:a aac` 转码后再切（服务端切片会自动转） |
| `未找到 ffmpeg，请先安装并加入 PATH（或用 -in 直接传 fragmented MP4）` | `PATH` 里没有 ffmpeg | 安装 ffmpeg，或用 `-ffmpeg` / `PR_FFMPEG` 指定路径 |
| `ffmpeg 执行失败: … Invalid data found when processing input` | 文件损坏，或根本不是视频 | 先用 `ffprobe -v error <文件>` 确认 |
| `segment: 源视频超过时长上限` | 视频超过 60 分钟 | 本地切（§5.2），或调大 `PR_SEGMENT_MAX_DURATION` |
| `服务器未安装 ffmpeg …` | 服务器没有 ffmpeg | 走 §5.1 / §5.2 本地切片 |

---

## 6. 前端集成提示

1. `POST /api/v1/segment/jobs`（`FormData` 字段名 `file`）→ 记下 `jobId`；
2. 轮询 `GET /jobs/{id}`（建议 1s 一次）：`state` 为 `done` 后看 `result`；
3. `result.singleResponse === true` → `GET /jobs/{id}/result` 存成 zip，解压到 `room-media/`；
4. `result.singleResponse === false` → 遍历 `result.parts`，逐个 `GET part.url`
   （并发 2 即可），校验 `sha256`，再解压到**同一个**目录；
5. 把 `room-media/` 交给主播端"选择分片目录"，行为与本地切片完全一致；
6. `state === 'failed'` 时把 `error` 原文展示给用户（文案已经写成可读的中文）。

---

## 7. 已知限制

- 本服务是**一次性预处理**，不属于直播链路；直播期服务器仍然不接触任何视频字节（I1 未被破坏）。
- 服务端转码只用于"源编码不是 H.264/AAC"的情况，固定 `veryfast + crf 23`，没有码率预设；
  需要精确控制码率时请用本地 `cmd/segmenter -transcode`。
- 产物保存在临时目录，TTL 到期即删；下载要趁早，重复提交会重新消耗配额与磁盘。
- 单个产物文件若本身超过单份上限（默认 1 GiB；2 秒分片几乎不可能出现）会直接失败：
  宁可明确报错，也不产出超限的一份。
- 服务器端不提供断点续传：分批下载的每一份都可以单独重下（内容一致），但没有 Range 支持。
