// 线上 protobuf 与进程内可读结构之间的唯一转换点（与 Go 侧 internal/model/pb_convert.go 对称）。
//
// 为什么不让业务代码直接用生成的结构体：pb 的 int64 在 TS 里是 bigint，
// 而同步环、聊天时间戳、索引计算都习惯普通数值；把差异收在这里，
// 业务代码就不用到处 map(Number)。新增字段必须同时改 proto 与本文件。
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import {
  EnvelopeSchema,
  PeerControlSchema,
  type Envelope as PBEnvelope,
  type PeerControl as PBPeerControl,
} from '../gen/projection_room_pb'
import type { MediaIndex, MediaSegment } from './media'
import type {
  Capacity,
  DistributorChange,
  Envelope,
  MemberInfo,
  Metrics,
  PeerControl,
  PlaybackState,
  TopologyAssignment,
} from './protocol'

const num = (v: bigint | undefined): number => Number(v ?? 0n)
const big = (v: number | undefined): bigint => BigInt(Math.round(v ?? 0))

/**
 * 字节类型统一为 `Uint8Array<ArrayBuffer>`。
 *
 * TS 5.7 起 `Uint8Array` 默认是 `Uint8Array<ArrayBufferLike>`（含 SharedArrayBuffer），
 * 而 RTCDataChannel.send / SourceBuffer.appendBuffer 要求 `ArrayBufferView<ArrayBuffer>`；
 * 明确绑定 ArrayBuffer 才能两边都过。
 */
export type Bytes = Uint8Array<ArrayBuffer>

/** 从 protobuf 拿到的 bytes 转成明确绑定 ArrayBuffer 的视图（必要时复制）。 */
export function bytesOf(view: Uint8Array): Bytes {
  if (view.buffer instanceof ArrayBuffer) {
    return new Uint8Array(view.buffer, view.byteOffset, view.byteLength)
  }
  return new Uint8Array(view)
}

/** 从一个大缓冲区里切出一段零拷贝视图。 */
export function viewOf(buffer: ArrayBuffer, offset: number, length: number): Bytes {
  return new Uint8Array(buffer, offset, length)
}

// ---------------- MediaIndex ----------------

function segmentToPB(seg: MediaSegment) {
  return {
    index: seg.index,
    file: seg.file,
    offset: big(seg.offset),
    size: big(seg.size),
    duration: seg.duration,
    startPts: seg.startPts,
    keyframe: seg.keyframe,
    sha256: seg.sha256,
  }
}

function indexToPB(index: MediaIndex) {
  return {
    version: index.version,
    initFile: index.initFile,
    mimeType: index.mimeType,
    totalDuration: index.totalDuration,
    segmentSec: index.segmentSec,
    bitrateBps: big(index.bitrateBps),
    totalBytes: big(index.totalBytes),
    segments: index.segments.map(segmentToPB),
  }
}

function indexFromPB(pb: PBEnvelope['mediaIndex']): MediaIndex | undefined {
  if (!pb) return undefined
  return {
    version: pb.version,
    initFile: pb.initFile,
    mimeType: pb.mimeType,
    totalDuration: pb.totalDuration,
    segmentSec: pb.segmentSec,
    bitrateBps: num(pb.bitrateBps),
    totalBytes: num(pb.totalBytes),
    segments: pb.segments.map((seg) => ({
      index: seg.index,
      file: seg.file,
      offset: num(seg.offset),
      size: num(seg.size),
      duration: seg.duration,
      startPts: seg.startPts,
      keyframe: seg.keyframe,
      sha256: seg.sha256,
    })),
  }
}

// ---------------- 其余结构化载荷 ----------------

function playbackToPB(p: PlaybackState) {
  return {
    paused: p.paused,
    currentTime: p.currentTime,
    hostClockMs: big(p.hostClockMs),
    rate: p.rate,
    seq: big(p.seq),
    clockEpoch: p.clockEpoch ?? '',
  }
}

function playbackFromPB(pb: PBEnvelope['playback']): PlaybackState | undefined {
  if (!pb) return undefined
  return {
    paused: pb.paused,
    currentTime: pb.currentTime,
    hostClockMs: num(pb.hostClockMs),
    rate: pb.rate,
    seq: num(pb.seq),
    clockEpoch: pb.clockEpoch,
  }
}

function metricsToPB(m: Metrics) {
  return {
    rttMs: m.rttMs ?? 0,
    throughputBps: m.throughputBps ?? 0,
    uploadCapacityBps: big(m.uploadCapacityBps),
    depth: m.depth ?? 0,
    bufferHealth: m.bufferHealth ?? 0,
    p95DeliveryMs: m.p95DeliveryMs ?? 0,
    stallCount: m.stallCount ?? 0,
    degraded: m.degraded ?? false,
    primaryId: m.primaryId ?? '',
  }
}

function metricsFromPB(pb: PBEnvelope['metrics']): Metrics | undefined {
  if (!pb) return undefined
  return {
    rttMs: pb.rttMs,
    throughputBps: pb.throughputBps,
    uploadCapacityBps: num(pb.uploadCapacityBps),
    depth: pb.depth,
    bufferHealth: pb.bufferHealth,
    p95DeliveryMs: pb.p95DeliveryMs,
    stallCount: pb.stallCount,
    degraded: pb.degraded,
    primaryId: pb.primaryId,
  }
}

function memberToPB(m: MemberInfo) {
  return {
    id: m.id,
    displayName: m.displayName,
    role: m.role,
    depth: m.depth,
    primaryId: m.primaryId ?? '',
    backupIds: m.backupIds ?? [],
    joinedAt: big(m.joinedAt),
  }
}

function memberFromPB(pb: PBEnvelope['member']): MemberInfo | undefined {
  if (!pb) return undefined
  return {
    id: pb.id,
    displayName: pb.displayName,
    role: pb.role as MemberInfo['role'],
    depth: pb.depth,
    primaryId: pb.primaryId,
    backupIds: pb.backupIds,
    joinedAt: num(pb.joinedAt),
  }
}

function capacityToPB(c: Capacity) {
  return {
    mode: c.mode,
    maxMembers: c.maxMembers,
    streamBps: big(c.streamBps),
    hostChildSlots: c.hostChildSlots,
  }
}

function capacityFromPB(pb: PBEnvelope['capacity']): Capacity | undefined {
  if (!pb) return undefined
  return {
    mode: pb.mode,
    maxMembers: pb.maxMembers,
    streamBps: num(pb.streamBps),
    hostChildSlots: pb.hostChildSlots,
  }
}

function topologyToPB(t: TopologyAssignment) {
  return {
    peerId: t.peerId,
    primaryId: t.primaryId ?? '',
    backupIds: t.backupIds ?? [],
    children: t.children ?? [],
    depth: t.depth,
    mode: t.mode,
    distributorId: t.distributorId ?? '',
    reason: t.reason ?? '',
    maxDepth: t.maxDepth,
  }
}

function topologyFromPB(pb: PBEnvelope['topology']): TopologyAssignment | undefined {
  if (!pb) return undefined
  return {
    peerId: pb.peerId,
    primaryId: pb.primaryId,
    backupIds: pb.backupIds,
    children: pb.children,
    depth: pb.depth,
    mode: pb.mode,
    distributorId: pb.distributorId,
    reason: pb.reason,
    maxDepth: pb.maxDepth,
  }
}

function distributorToPB(d: DistributorChange) {
  return { fromId: d.fromId ?? '', toId: d.toId ?? '', reason: d.reason ?? '' }
}

function distributorFromPB(pb: PBEnvelope['distributor']): DistributorChange | undefined {
  if (!pb) return undefined
  return { fromId: pb.fromId, toId: pb.toId, reason: pb.reason }
}

// ---------------- 信封 ----------------

/** 领域信封 → WS 二进制帧内容（protobuf）。 */
export function encodeEnvelope(env: Envelope): Bytes {
  const msg = create(EnvelopeSchema, {
    type: env.type,
    roomId: env.roomId ?? '',
    clientId: env.clientId ?? '',
    to: env.to ?? '',
    from: env.from ?? '',
    displayName: env.displayName ?? '',
    role: env.role ?? '',
    password: env.password ?? '',
    payload: env.payload ? new TextEncoder().encode(JSON.stringify(env.payload)) : new Uint8Array(0),
    text: env.text ?? '',
    ts: big(env.ts),
    action: env.action ?? '',
    currentTime: env.currentTime ?? 0,
    hostClockMs: big(env.hostClockMs),
    clockEpoch: env.clockEpoch ?? '',
    paused: env.paused ?? false,
    rate: env.rate ?? 0,
    playback: env.playback ? playbackToPB(env.playback) : undefined,
    seq: big(env.seq),
    metrics: env.metrics ? metricsToPB(env.metrics) : undefined,
    selfId: env.selfId ?? '',
    hostId: env.hostId ?? '',
    member: env.member ? memberToPB(env.member) : undefined,
    members: (env.members ?? []).map(memberToPB),
    mediaIndex: env.mediaIndex ? indexToPB(env.mediaIndex) : undefined,
    capacity: env.capacity ? capacityToPB(env.capacity) : undefined,
    topology: env.topology ? topologyToPB(env.topology) : undefined,
    distributor: env.distributor ? distributorToPB(env.distributor) : undefined,
    have: env.have ?? new Uint8Array(0),
    complete: env.complete ?? false,
    code: env.code ?? '',
    message: env.message ?? '',
    reason: env.reason ?? '',
  })
  return toBinary(EnvelopeSchema, msg)
}

/** WS 二进制帧内容 → 领域信封。 */
export function decodeEnvelope(bytes: Uint8Array): Envelope {
  const pb = fromBinary(EnvelopeSchema, bytes)
  const env: Envelope = {
    type: pb.type,
    roomId: pb.roomId || undefined,
    clientId: pb.clientId || undefined,
    to: pb.to || undefined,
    from: pb.from || undefined,
    displayName: pb.displayName || undefined,
    role: (pb.role || undefined) as Envelope['role'],
    payload: pb.payload.length > 0 ? JSON.parse(new TextDecoder().decode(pb.payload)) : undefined,
    text: pb.text || undefined,
    ts: num(pb.ts),
    action: pb.action || undefined,
    currentTime: pb.currentTime,
    hostClockMs: num(pb.hostClockMs),
    clockEpoch: pb.clockEpoch || undefined,
    paused: pb.paused,
    rate: pb.rate,
    playback: playbackFromPB(pb.playback),
    seq: num(pb.seq),
    metrics: metricsFromPB(pb.metrics),
    selfId: pb.selfId || undefined,
    hostId: pb.hostId || undefined,
    member: memberFromPB(pb.member),
    members: pb.members.length > 0 ? pb.members.map((m) => memberFromPB(m)!) : undefined,
    mediaIndex: indexFromPB(pb.mediaIndex),
    capacity: capacityFromPB(pb.capacity),
    topology: topologyFromPB(pb.topology),
    distributor: distributorFromPB(pb.distributor),
    have: pb.have.length > 0 ? bytesOf(pb.have) : undefined,
    complete: pb.complete,
    code: pb.code || undefined,
    message: pb.message || undefined,
    reason: pb.reason || undefined,
  }
  return env
}

// ---------------- DataChannel 控制消息 ----------------

/** 一条 DataChannel 控制消息（protobuf），已带 1 字节 kind 前缀。 */
export function encodeControl(msg: PeerControl): Bytes {
  const pb = create(PeerControlSchema, {
    t: msg.t,
    rid: msg.rid ?? '',
    idx: msg.idx ?? 0,
    code: msg.code ?? '',
    chunks: msg.chunks ?? new Uint8Array(0),
    complete: msg.complete ?? false,
    ts: big(msg.ts),
    currentTime: msg.currentTime ?? 0,
    hostClockMs: big(msg.hostClockMs),
    clockEpoch: msg.clockEpoch ?? '',
    paused: msg.paused ?? false,
    rate: msg.rate ?? 0,
    seq: big(msg.seq),
    parentClockMs: big(msg.parentClockMs),
    parentOffsetMs: msg.parentOffsetMs ?? 0,
  })
  const body = toBinary(PeerControlSchema, pb)
  const out = new Uint8Array(1 + body.length)
  out[0] = KIND_CONTROL
  out.set(body, 1)
  return out
}

export function decodeControl(bytes: Uint8Array): PeerControl {
  const pb: PBPeerControl = fromBinary(PeerControlSchema, bytes.subarray(1))
  return {
    t: pb.t as PeerControl['t'],
    rid: pb.rid || undefined,
    idx: pb.idx,
    code: pb.code || undefined,
    chunks: pb.chunks.length > 0 ? bytesOf(pb.chunks) : undefined,
    complete: pb.complete,
    ts: num(pb.ts),
    currentTime: pb.currentTime,
    hostClockMs: num(pb.hostClockMs),
    clockEpoch: pb.clockEpoch || undefined,
    paused: pb.paused,
    rate: pb.rate,
    seq: num(pb.seq),
    parentClockMs: num(pb.parentClockMs),
    parentOffsetMs: pb.parentOffsetMs,
  }
}

// ---------------- DataChannel 分片帧 ----------------
//
// 控制消息与分片数据在**同一条 DataChannel** 上，且两者都是二进制。
// 不靠"猜首字节"来区分：每条消息都带一个显式的 kind 前缀。
//
//   [kind(1)][protobuf PeerControl]                       control
//   [kind(1)][flags(2)][chunkIndex(4)][fMP4 片段]          media（init / 媒体 / 尾段）
//
// kind 取值见下面的常量；分片帧只多 1 字节开销，换来的是绝不歧义。
//
// flags：低 1 位 = 后面还有分片；高 15 位 = 当前分片序号（0 起）。
// DataChannel 单条消息有上限（Chrome 256KiB，规范默认 64KiB），
// 高码率素材的单个分片可能有 1–2MB，**必须切开发**；重组在 useWebRTC 里做。
export const KIND_CONTROL = 0x01
export const KIND_INIT = 0x02
export const KIND_MEDIA = 0x03
export const KIND_TAIL = 0x04

export const MEDIA_HEADER_BYTES = 7
/** flags 低 1 位：后面还有分片。 */
export const FRAG_MORE = 0x0001
/** flags 高 15 位能表达的分片数上限（7 字节头 + 64KB 分片 ≈ 2GB 单个分片）。 */
export const FRAG_MAX_INDEX = 0x7fff

export interface DecodedMedia {
  kind: number
  chunkIndex: number
  /** 当前分片在一个 chunk 内的序号（0 起）；未分片时恒为 0。 */
  fragmentIndex: number
  /** 是否还有后续分片。 */
  more: boolean
  /** 零拷贝视图（绑 ArrayBuffer）：直接交给 SourceBuffer.appendBuffer。 */
  payload: Bytes
}

export function encodeMediaFrame(
  kind: number,
  chunkIndex: number,
  payload: Uint8Array,
  fragmentIndex = 0,
  more = false,
): Bytes {
  const out = new Uint8Array(MEDIA_HEADER_BYTES + payload.length)
  const view = new DataView(out.buffer, out.byteOffset, out.byteLength)
  view.setUint8(0, kind)
  view.setUint16(1, ((fragmentIndex & FRAG_MAX_INDEX) << 1) | (more ? FRAG_MORE : 0))
  view.setUint32(3, chunkIndex)
  out.set(payload, MEDIA_HEADER_BYTES)
  return out
}

export interface DecodedFrame {
  kind: number
  control?: PeerControl
  media?: DecodedMedia
}

/** 解析一条 DataChannel 消息；kind 决定它是控制消息还是分片帧。 */
export function decodeFrame(bytes: Uint8Array): DecodedFrame | null {
  if (bytes.length < 1) return null
  const kind = bytes[0]

  if (kind === KIND_CONTROL) {
    return { kind, control: decodeControl(bytes) }
  }
  if (kind === KIND_INIT || kind === KIND_MEDIA || kind === KIND_TAIL) {
    if (bytes.length < MEDIA_HEADER_BYTES) return null
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
    const flags = view.getUint16(1)
    return {
      kind,
      media: {
        kind,
        chunkIndex: view.getUint32(3),
        fragmentIndex: flags >>> 1,
        more: (flags & FRAG_MORE) !== 0,
        // 直接在这个 ArrayBuffer 上开视图：零拷贝，且类型满足 appendBuffer 的要求。
        payload: viewOf(bytes.buffer as ArrayBuffer, bytes.byteOffset + MEDIA_HEADER_BYTES, bytes.byteLength - MEDIA_HEADER_BYTES),
      },
    }
  }
  return null
}
