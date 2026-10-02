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

export interface CreateRoomResponse {
  roomId: string
  iceServers: RTCIceServer[]
  capacity: Capacity
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
