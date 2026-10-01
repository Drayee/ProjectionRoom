# ProjectionRoom

像 B 站放映室一样的「一起看」房间：**主播播放本地视频，观众同步观看并聊天**。

服务器只做**信令交换、房间状态与拓扑管理**——它不传输任何视频字节。视频分片由主播通过
WebRTC DataChannel 直接分发给其他节点（P2P 树状分发 + 多父条带化）。

设计与取舍的完整依据见 [`docs/SPEC.md`](docs/SPEC.md)；实现中任何偏离 SPEC 的地方都应在那里先改。

---

## 当前进度

| 里程碑 | 内容 | 状态 |
| :--- | :--- | :--- |
| **M1** | 基建 + 房间 / 信令 / 聊天 / 成员列表 / 房主控制 | ✅ 已实现并验证 |
| M2 | `cmd/segmenter` + WebRTC 分片传输 + MediaSource 播放 + 播放同步 | 待开始 |
| M3 | 多层树 + 单链分发模式 + 多父条带化 + 抗慢节点 | 待开始 |
| M4 | 稳健性、监控面板、STUN/TURN 接入 | 待开始 |

### M1 已交付

- **服务端**（Go 1.26 + gin + google/wire）：`/ws` 信令端点、房间生命周期、
  密码校验、成员上限、房主控制权限校验与单调 `seq`、聊天广播、同房信令透传（含跨房拦截）。
- **前端**（Vue 3 + TS + Vite + Pinia）：创建/加入房间、房间页（成员、聊天、房主控制台、
  连接状态与拓扑徽标）。
- **验证**：`go build ./...`、`go vet ./...`、`go vet -tags wireinject ./cmd`、`go test ./...`
  全绿；前端 `vue-tsc --noEmit` 与 `vite build` 通过；服务端 → Vite 代理 → 浏览器链路实测可用。

其中 `internal/httpapi/ws_test.go` 用**两条真实 WebSocket 连接**跑完整链路，覆盖 M1 的验收标准：
同房聊天双向可见、成员表实时更新、观众下发控制被拒（`NOT_HOST`）、主播控制全网广播、
迟到者拿到最新播放状态、跨房信令被拒、主播离开房间关闭。

---

## 快速开始

前置：Go 1.26+、Node 20+。M2 起还需要 `ffmpeg` 用于把本地视频预处理成 fMP4 分片。

```bash
# 终端 1：服务端（默认 127.0.0.1:8080）
go run ./cmd

# 终端 2：前端（默认 127.0.0.1:5173，/api 与 /ws 自动代理到服务端）
cd client
npm install
npm run dev
```

浏览器打开 http://127.0.0.1:5173 ，一个窗口「创建房间」当主播，另一个窗口用房间码「加入房间」。

### 配置

服务端全部配置都有面向本机开发的默认值，可用环境变量覆盖：

| 变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `PR_ADDR` | `127.0.0.1:8080` | 监听地址 |
| `PR_MAX_MEMBERS` | `16` | 房间成员硬上限（P2P 真实容量上限在 M3 由实测上行计算） |
| `PR_DEFAULT_STREAM_BPS` | `2000000` | 尚未拿到 mediaIndex 时的码率估计，用于容量预判 |
| `PR_STUN_URLS` | `stun:stun.l.google.com:19302` | 逗号分隔；M3 起用于 WebRTC |
| `PR_TURN_URLS` / `PR_TURN_USER` / `PR_TURN_PASS` | 空 | 直连失败时的中继（默认不启用） |

---

## 项目结构

```
cmd/                    服务端入口 + wire 依赖注入（wire.go 生成 wire_gen.go）
internal/config/        配置结构与加载（环境变量覆盖）
internal/protocol/      WebSocket 消息契约（与 client/src/types/protocol.ts 一一对应）
internal/signal/        WebSocket 信令层：连接注册、定向转发、房间广播
internal/room/          房间/成员/房主控制/聊天的唯一权威（不碰网络，可单测）
internal/httpapi/       gin 路由、/ws 处理与错误映射
client/                 Vue 3 + TS + Vite 前端
docs/SPEC.md            设计文档（协议、拓扑、同步算法、里程碑与验收标准）
```

**不变量**：`internal/` 下任何代码都不得读写视频数据；服务器的职责边界在 SPEC §3 有明确表格。

---

## 开发约定

```bash
gofmt -l ./cmd ./internal     # 必须为空
go vet ./... && go test ./...
go vet -tags wireinject ./cmd # 校验 wire 注入文件（默认构建不含它）

cd client && npm run typecheck && npm run build
```

消息字段在 `internal/protocol/message.go` 与 `client/src/types/protocol.ts` 各有一份，
改动必须同步——它们是同一个协议的两份投影。
