// 客户端侧的领域消息类型（可读版本）。
//
// 线上编码是 protobuf（见 types/codec.ts 与 proto/projection_room.proto）；
// 本文件是业务代码使用的形状 —— 数值就是 number，不需要到处写 bigint。
// 改协议时：先改 .proto，再改 codec.ts 的转换，最后才动这里。

import type { MediaIndex } from './media'

export type Role = 'host' | 'viewer'

export const T = {
  // 客户端 → 服务端
  Join: 'join',
  Signal: 'signal',
  MediaIndex: 'media-index',
  ChunksReport: 'chunks-report',
  Metrics: 'metrics',
  TopologyRequest: 'topology-request',
  RoomControl: 'room-control',
  Chat: 'chat',
  Leave: 'leave',
  // 服务端 → 客户端
  Joined: 'joined',
  MemberJoined: 'member-joined',
  MemberLeft: 'member-left',
  MemberList: 'member-list',
  Capacity: 'capacity',
  Topology: 'topology',
  ParentAssignment: 'parent-assignment',
  DistributorChange: 'distributor-change',
  RoomClosed: 'room-closed',
  Error: 'error',
} as const

export const Action = {
  Play: 'play',
  Pause: 'pause',
  Seek: 'seek',
  Rate: 'rate',
} as const

export interface MemberInfo {
  id: string
  displayName: string
  role: Role
  depth: number
  primaryId?: string
  backupIds?: string[]
  joinedAt: number
}

export interface PlaybackState {
  paused: boolean
  currentTime: number
  hostClockMs: number
  rate: number
  seq: number
  /** 主播页面的时钟纪元；变化即表示主播重载过页面，必须重置时钟滤波（C13）。 */
  clockEpoch?: string
}

/** 实测度量：UploadCapacityBps 取自 getStats().availableOutgoingBitrate（C11）。 */
export interface Metrics {
  rttMs?: number
  throughputBps?: number
  uploadCapacityBps?: number
  depth?: number
  /** 健康度：用于"卡顿 → 服务器重算路径"。 */
  bufferHealth?: number
  p95DeliveryMs?: number
  stallCount?: number
  degraded?: boolean
  primaryId?: string
}

export interface Capacity {
  mode: string
  maxMembers: number
  streamBps: number
  hostChildSlots: number
}

/** 一个节点在分发树中的位置（SPEC §6.1）。 */
export interface TopologyAssignment {
  peerId: string
  primaryId?: string
  backupIds?: string[]
  /** 以本节点为主父的成员：进度与时钟锚点沿这条路径逐跳转发（§7.5）。 */
  children?: string[]
  depth: number
  mode: string
  distributorId?: string
  reason?: string
  maxDepth: number
}

/** 单链模式下的分发节点换防通知（SPEC §6.3）。 */
export interface DistributorChange {
  fromId?: string
  toId?: string
  reason?: string
}

export interface Envelope {
  type: string
  roomId?: string
  clientId?: string
  to?: string
  from?: string
  displayName?: string
  role?: Role
  password?: string
  /**
   * 主播复位令牌（proto 字段 34，仅 join 上行使用）。
   *
   * 为什么需要它：主播断线后房间进入宽限期，此时 HostID 为空 —— 任何知道房间码的人
   * （而房间码是要分享给观众的）都能以 role=host 抢走主播位，把原主播挡在门外。
   * 令牌由 `POST /api/rooms` 创建响应一次性下发（64 位十六进制），宽限期内只有带上它
   * 才能接回主播位；不带 → 服务端回 HOST_TOKEN_REQUIRED（不是"密码错误"那种含糊结果）。
   * 首次进房（房间已有主播）不需要它。
   */
  hostToken?: string
  payload?: unknown
  text?: string
  ts?: number
  action?: string
  currentTime?: number
  hostClockMs?: number
  clockEpoch?: string
  paused?: boolean
  rate?: number
  playback?: PlaybackState
  seq?: number
  metrics?: Metrics
  selfId?: string
  hostId?: string
  member?: MemberInfo
  members?: MemberInfo[]
  mediaIndex?: MediaIndex
  capacity?: Capacity
  topology?: TopologyAssignment
  distributor?: DistributorChange
  /** 分片拥有位图（原始位，不再是 base64 字符串）。 */
  have?: Uint8Array<ArrayBuffer>
  complete?: boolean
  code?: string
  message?: string
  reason?: string

  // —— 用户级实时通道（/ws/user）的载荷。wayfinder #31。
  //
  // 四个字段只在 `/ws/user` 上有值；房间信令里它们恒为 undefined。
  dm?: DirectMessage
  /** 通道就绪确认（服务端在连接注册完成后立刻下发，type = 'user-ready'）。 */
  userChannelReady?: boolean
  friend?: FriendEvent
  presence?: PresenceState
}

/**
 * 一条私聊（`dm` 与 `dm-read` 共用）。
 *
 * `dm-read` 里 `body` 为空、`readAt` 有值 —— 客户端按 **type** 决定渲染什么，
 * 而不是按字段有没有值去猜（见 proto 里 DirectMessage 的注释）。
 */
export interface DirectMessage {
  id: number
  senderId: number
  recipientId: number
  senderName?: string
  /** dm-read 事件里为空。 */
  body?: string
  /** RFC3339；空 = 未读。 */
  createdAt?: string
  /** RFC3339；空 = 未读。 */
  readAt?: string
}

/** 好友域的一次变化（friend-request / friend-accepted / friend-removed / blocked）。 */
export interface FriendEvent {
  relationshipId: number
  /** "对方"是谁（相对收件人）。 */
  userId: number
  displayName?: string
  /** pending | accepted | blocked | removed */
  status: string
}

/** 好友上线/下线。 */
export interface PresenceState {
  userId: number
  online: boolean
}

/** WebRTC 信令载荷：服务端只做原样转发，不解析（SPEC §5.1）。 */
export interface SignalPayload {
  kind: 'offer' | 'answer' | 'candidate'
  sdp?: string
  candidate?: RTCIceCandidateInit
}

/**
 * Peer 之间的 DataChannel 控制消息。
 * 帧格式（1 字节 kind 前缀 + protobuf）见 types/codec.ts；分片数据不走这里。
 */
export interface PeerControl {
  t: 'req' | 'chunk' | 'err' | 'have' | 'ping' | 'pong' | 'progress' | 'time-sync'
  rid?: string
  idx?: number
  code?: string
  /** have 消息的位图。 */
  chunks?: Uint8Array<ArrayBuffer>
  complete?: boolean
  ts?: number
  currentTime?: number
  hostClockMs?: number
  clockEpoch?: string
  paused?: boolean
  rate?: number
  seq?: number
  /** 逐跳时钟中继：转发节点自己的本地时钟读数。 */
  parentClockMs?: number
  /** 逐跳时钟中继：转发节点自己到主播的偏移估计（毫秒）。 */
  parentOffsetMs?: number
}

/**
 * ICE 部分（`POST /api/rooms` 与 `GET /api/ice` 同构，纯加法扩展）。
 *
 * 服务端每约 60 秒按探测结果重算下发的 STUN 列表（最多 4 条，Google/Cloudflare 为粘性条目），
 * 所以客户端必须按 ttlSeconds 周期重拉；`probe.scores` 是这次下发的依据（可观测、可复现）。
 */
export interface IceResponsePart {
  iceServers?: RTCIceServer[]
  ttlSeconds?: number
  expiresAt?: number
  probe?: {
    probedAt?: number
    intervalSeconds?: number
    scores?: Array<{ url: string; rttMs: number; ok: boolean; score: number; selected?: boolean }>
  }
}

export interface CreateRoomResponse extends IceResponsePart {
  roomId: string
  iceServers: RTCIceServer[]
  capacity: Capacity
  /**
   * 主播复位令牌：只在创建响应里出现这一次（服务端只存它的哈希），必须持久化并随 join 上行。
   * 老服务端没有这个字段 → undefined（不带照常能进房，只是宽限期内接不回主播位）。
   */
  hostToken?: string
}

export interface RoomInfoResponse {
  exists: boolean
  roomId?: string
  hasHost?: boolean
  hasMedia?: boolean
  memberCount?: number
  capacity?: Capacity
  error?: string
}
