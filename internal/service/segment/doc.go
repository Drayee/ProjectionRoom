// Package segment 实现「服务端视频切片服务」：把用户上传的源视频**一次性预处理**成
// 放映室可用的 fMP4 分片目录（init.mp4 + c*.m4s + index.json）。
//
// # 与不变量 I1 的关系（必读）
//
// ALGORITHM §0 的 I1 说的是**直播链路**上服务器不传输任何视频字节。本包处理的是
// **开播前的一次性预处理**，与直播链路完全分离：
//
//	用户 → (multipart 上传) → 服务器切片 → (一次性下载) → 用户本地分片目录
//	                                                ↓ 开播后
//	主播 ⇄ 观众：分片只在 peer 之间经 WebRTC DataChannel 流动，服务器一个字节都不经手
//
// 也就是说，服务器只在"准备媒体"阶段接触视频字节，直播期仍然零视频流量
// （SPEC §1.1 目标 7、§1.3 决策表；ALGORITHM §0 I1）。这个包里的任何代码
// 都不得被直播链路引用。
//
// # 分层
//
//   - tools.go     ffmpeg/ffprobe 的发现顺序
//   - pipeline.go  probe → 必要时重新封装/转码 → 按 moof 边界切分 → 写 index.json
//   - artifacts.go 产物收集、manifest 分批、确定性 zip 打包
//   - queue.go     作业队列、状态机、配额（并发/排队/令牌桶）、TTL 清理
//
// pipeline.go 与 artifacts.go 不依赖队列，因此 cmd/segmenter 直接复用它们，
// 保证"本地 CLI"和"服务端切片"产出完全一致的产物格式。
package segment
