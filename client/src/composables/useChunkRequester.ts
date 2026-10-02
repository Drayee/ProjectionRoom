import { ref } from 'vue'
import { KIND_INIT, encodeControl, type Bytes, type DecodedMedia } from '../types/codec'
import type { PeerControl } from '../types/protocol'

// 帧的编解码在 types/codec.ts（控制消息与分片帧都在那里定义），
// 本文件只负责"发请求 / 等响应 / 超时转投"。

export interface Delivery {
  index: number
  peerId: string
  kind: number
  /** 零拷贝视图：直接交给 SourceBuffer.appendBuffer。 */
  payload: Bytes
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
  send: (peerId: string, data: Bytes) => boolean
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
    if (!opts.send(peerId, encodeControl(control))) {
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
  function handleMedia(peerId: string, media: DecodedMedia): Delivery {
    const key = keyOf(peerId, media.chunkIndex)
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
      index: media.chunkIndex,
      peerId,
      kind: media.kind,
      payload: media.payload,
      ms,
    }
    entry?.resolve(delivery)

    return delivery
  }

  function isInit(kind: number): boolean {
    return kind === KIND_INIT
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

  /** 某个父节点当前的在途请求数：条带化按主父积压决定是否把分片分流给备用父。 */
  function pendingFor(peerId: string): number {
    let count = 0
    for (const key of inflight.keys()) {
      if (key.startsWith(`${peerId}#`)) count += 1
    }
    return count
  }

  function reset() {
    for (const entry of inflight.values()) {
      window.clearTimeout(entry.timer)
      entry.reject(new Error('连接已重置'))
    }
    inflight.clear()
    samples.length = 0
  }

  return {
    request,
    handleControl,
    handleMedia,
    isInit,
    p95DeliveryMs,
    pendingCount,
    pendingFor,
    reset,
    delivered,
    timedOut,
    failed,
  }
}
