import { ref } from 'vue'
import { FrameType } from '../types/protocol'
import type { PeerControl } from '../types/protocol'

/** 二进制帧头：ver(1) type(1) flags(2) chunkIndex(4)（SPEC §5.2）。 */
export const FRAME_HEADER_BYTES = 8

export function encodeFrame(type: number, chunkIndex: number, payload: ArrayBuffer): ArrayBuffer {
  const out = new ArrayBuffer(FRAME_HEADER_BYTES + payload.byteLength)
  const view = new DataView(out)
  view.setUint8(0, 1)
  view.setUint8(1, type)
  view.setUint16(2, 0)
  view.setUint32(4, chunkIndex)
  new Uint8Array(out, FRAME_HEADER_BYTES).set(new Uint8Array(payload))
  return out
}

export interface DecodedFrame {
  type: number
  chunkIndex: number
  payload: ArrayBuffer
}

export function decodeFrame(data: ArrayBuffer): DecodedFrame | null {
  if (data.byteLength < FRAME_HEADER_BYTES) {
    return null
  }
  const view = new DataView(data)
  if (view.getUint8(0) !== 1) {
    return null
  }
  return {
    type: view.getUint8(1),
    chunkIndex: view.getUint32(4),
    payload: data.slice(FRAME_HEADER_BYTES),
  }
}

export interface Delivery {
  index: number
  peerId: string
  type: number
  payload: ArrayBuffer
  ms: number
}

interface Inflight {
  rid: string
  startedAt: number
  timer: number
  promise: Promise<Delivery>
  resolve: (d: Delivery) => void
  reject: (e: Error) => void
}

const DEFAULT_TIMEOUT_MS = 3000

/**
 * 分片请求器：负责"发请求 / 等响应 / 超时"这一件事。
 *
 * 用 (peerId, index) 作为在途键：同一个分片可以向多个父节点并发请求，
 * 先到先用，其余结果自然作废 —— 这是 M3 多父条带化与超时转投的基础（SPEC §6.4 L2）。
 */
export function useChunkRequester(opts: {
  send: (peerId: string, data: string | ArrayBuffer) => boolean
  timeoutMs?: number
}) {
  const inflight = new Map<string, Inflight>()
  const delivered = ref(0)
  const timedOut = ref(0)
  const failed = ref(0)

  const samples: number[] = []
  let ridSeq = 0

  const keyOf = (peerId: string, index: number) => `${peerId}#${index}`

  function request(peerId: string, index: number): Promise<Delivery> {
    const key = keyOf(peerId, index)
    const running = inflight.get(key)
    if (running) {
      return running.promise
    }

    const rid = `${index}-${++ridSeq}`
    let resolveFn!: (d: Delivery) => void
    let rejectFn!: (e: Error) => void
    const promise = new Promise<Delivery>((resolve, reject) => {
      resolveFn = resolve
      rejectFn = reject
    })

    const timer = window.setTimeout(() => {
      inflight.delete(key)
      timedOut.value += 1
      rejectFn(new Error(`分片 ${index} 从 ${peerId} 请求超时`))
    }, opts.timeoutMs ?? DEFAULT_TIMEOUT_MS)

    const entry: Inflight = { rid, startedAt: performance.now(), timer, promise, resolve: resolveFn, reject: rejectFn }
    inflight.set(key, entry)

    const control: PeerControl = { t: 'req', rid, idx: index }
    if (!opts.send(peerId, JSON.stringify(control))) {
      window.clearTimeout(timer)
      inflight.delete(key)
      failed.value += 1
      rejectFn(new Error(`到 ${peerId} 的通道不可用`))
    }

    return promise
  }

  /** 处理 peer 的文本控制消息；返回 true 表示已消费。 */
  function handleControl(peerId: string, msg: PeerControl): boolean {
    if (msg.t !== 'err' || msg.idx === undefined) {
      return false
    }
    const key = keyOf(peerId, msg.idx)
    const entry = inflight.get(key)
    if (!entry) {
      return true
    }
    window.clearTimeout(entry.timer)
    inflight.delete(key)
    failed.value += 1
    entry.reject(new Error(`分片 ${msg.idx} 请求失败：${msg.code ?? '未知原因'}`))
    return true
  }

  /**
   * 处理二进制帧。
   * 命中在途请求就完成它；未命中（M3 的推送式分发或迟到响应）也照样返回，由调用方决定怎么用。
   */
  function handleBinary(peerId: string, data: ArrayBuffer): Delivery | null {
    const frame = decodeFrame(data)
    if (!frame) {
      return null
    }

    const key = keyOf(peerId, frame.chunkIndex)
    const entry = inflight.get(key)
    const ms = entry ? performance.now() - entry.startedAt : 0

    if (entry) {
      window.clearTimeout(entry.timer)
      inflight.delete(key)
      samples.push(ms)
      if (samples.length > 20) {
        samples.shift()
      }
      delivered.value += 1
    }

    const delivery: Delivery = {
      index: frame.chunkIndex,
      peerId,
      type: frame.type,
      payload: frame.payload,
      ms,
    }
    entry?.resolve(delivery)

    return delivery
  }

  function isInit(frameType: number): boolean {
    return frameType === FrameType.Init
  }

  /** 最近 20 次交付时延的 p95（没有样本时返回 0）。 */
  function p95DeliveryMs(): number {
    if (samples.length === 0) {
      return 0
    }
    const sorted = [...samples].sort((a, b) => a - b)
    const idx = Math.min(sorted.length - 1, Math.ceil(sorted.length * 0.95) - 1)
    return Math.round(sorted[idx])
  }

  function pendingCount(): number {
    return inflight.size
  }

  function reset() {
    for (const entry of inflight.values()) {
      window.clearTimeout(entry.timer)
      entry.reject(new Error('连接已重置'))
    }
    inflight.clear()
    samples.length = 0
  }

  return { request, handleControl, handleBinary, isInit, p95DeliveryMs, pendingCount, reset, delivered, timedOut, failed }
}
