import { ref } from 'vue'
import { KIND_INIT, encodeControl, type Bytes, type DecodedMedia } from '../types/codec'
import type { PeerControl } from '../types/protocol'

// 帧的编解码在 types/codec.ts（控制消息与分片帧都在那里定义），
// 本文件只负责"发请求 / 等响应 / 超时转投"，外加**实测边速率的记账**。

export interface Delivery {
  index: number
  peerId: string
  kind: number
  /** 零拷贝视图：直接交给 SourceBuffer.appendBuffer。 */
  payload: Bytes
  ms: number
}

/** 请求失败的原因分类：换父策略要按它决定"立刻转投"还是"交给下一个 tick"。 */
export type ChunkErrorCode = 'timeout' | 'send' | 'remote' | 'reset'

/**
 * 分片请求错误。
 *
 * 为什么不用裸 Error + 文案判断：换父/降权策略必须能区分"超时"与"远端明确拒绝"，
 * 靠 message 里找"超时"两个字，改一次文案就静默失效。
 */
export class ChunkRequestError extends Error {
  readonly code: ChunkErrorCode

  constructor(message: string, code: ChunkErrorCode) {
    super(message)
    this.name = 'ChunkRequestError'
    this.code = code
  }
}

/** 取一条错误的分类（不是本模块抛的就当 'remote'：远端/上层原因，不参与超时换父计数）。 */
export function chunkErrorCode(err: unknown): ChunkErrorCode | '' {
  return err instanceof ChunkRequestError ? err.code : ''
}

/** 一个父节点（边）的实测吞吐记账。 */
export interface EdgeStat {
  peerId: string
  /** 交付吞吐的 EWMA（Bytes/s）；没有速率样本时为 0。 */
  rateBps: number
  /** 观测到的最高单次吞吐（Bytes/s）：EWMA 会被 RTT 占主导的小样本压低，它用来对照。 */
  peakRateBps: number
  /** 最近一次交付耗时（ms）。 */
  lastMs: number
  /** 参与速率 EWMA 的样本数（不含 init 段）。 */
  samples: number
  deliveries: number
  timeouts: number
}

interface Inflight {
  rid: string
  startedAt: number
  timer: number
  promise: Promise<Delivery>
  resolve: (d: Delivery) => void
  reject: (e: Error) => void
}

/** 改动前这里写死 3000ms；现在默认仍取它，但 room.ts 会传入"按实测 RTT 与实测边速率推导"的函数。 */
const DEFAULT_TIMEOUT_MS = 3000
/** 吞吐 EWMA 的平滑系数：0.3 ≈ 有效窗口三四个样本，够快又不被单个抖动样本带跑。 */
const EDGE_EWMA_ALPHA = 0.3

/**
 * 分片请求器：负责"发请求 / 等响应 / 超时"这一件事。
 *
 * 用 (peerId, index) 作为在途键：同一个分片可以向多个父节点并发请求，
 * 先到先用，其余结果自然作废 —— 这是 M3 多父条带化与超时转投的基础（SPEC §6.4 L2）。
 *
 * 超时不再是常量（T3-1）：`timeoutMs` 可以传函数，按 peerId 取该边实测 RTT 与实测速率推导 ——
 * 固定 3s 在"父节点已经死了"时要白等 3s；而只按 RTT 推导又会在跨运营商的慢边上误判
 * （RTT 是控制报文往返，不含分片传输时间），所以推导里还要叠上"按该边速率传完一片要多久"。
 */
export function useChunkRequester(opts: {
  send: (peerId: string, data: Bytes) => boolean
  /** 固定超时，或按 peerId 推导（见 utils/serveSchedule.ts 的 deriveRequestTimeoutMs）。 */
  timeoutMs?: number | ((peerId: string) => number)
}) {
  const inflight = new Map<string, Inflight>()
  const delivered = ref(0)
  const timedOut = ref(0)
  const failed = ref(0)
  /** 每个父节点的实测吞吐记账（诊断抽屉的"每边速率"与在途推导都读它）。 */
  const edges = ref(new Map<string, EdgeStat>())

  const samples: number[] = []
  let ridSeq = 0

  const keyOf = (peerId: string, index: number) => `${peerId}#${index}`

  function timeoutOf(peerId: string): number {
    const value = opts.timeoutMs
    if (typeof value === 'function') {
      const ms = value(peerId)
      return ms > 0 ? ms : DEFAULT_TIMEOUT_MS
    }
    return value ?? DEFAULT_TIMEOUT_MS
  }

  /** 空账本：只读字段都从 0 起，避免 undefined 漏进诊断文本。 */
  function blankEdge(peerId: string): EdgeStat {
    return { peerId, rateBps: 0, peakRateBps: 0, lastMs: 0, samples: 0, deliveries: 0, timeouts: 0 }
  }

  /**
   * 记一次交付的吞吐。
   *
   * init 段（kind=INIT / index 0）**不参与速率样本**：它只有几百字节，
   * "字节数 / 耗时"算出来是几 KB/s，一条就能把 EWMA 从 MB/s 拉到 KB/s 级，
   * 于是自动在途上限被压到下限 2（实测过）。但它的交付次数照样计。
   */
  function noteEdgeDelivery(peerId: string, bytes: number, ms: number, countRate: boolean) {
    const prev = edges.value.get(peerId) ?? blankEdge(peerId)
    let rateBps = prev.rateBps
    let peakRateBps = prev.peakRateBps
    let samplesCount = prev.samples
    if (countRate && bytes > 0 && ms > 0) {
      const sample = bytes / (ms / 1000)
      rateBps = samplesCount === 0 ? sample : prev.rateBps * (1 - EDGE_EWMA_ALPHA) + sample * EDGE_EWMA_ALPHA
      peakRateBps = Math.max(prev.peakRateBps, sample)
      samplesCount += 1
    }
    // 每次都换一个新对象：Map 里的就地修改不会触发浅层依赖，换对象最稳。
    edges.value.set(peerId, {
      peerId,
      rateBps,
      peakRateBps,
      lastMs: ms,
      samples: samplesCount,
      deliveries: prev.deliveries + 1,
      timeouts: prev.timeouts,
    })
  }

  function noteEdgeTimeout(peerId: string) {
    const prev = edges.value.get(peerId) ?? blankEdge(peerId)
    edges.value.set(peerId, { ...prev, peerId, timeouts: prev.timeouts + 1 })
  }

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
      noteEdgeTimeout(peerId)
      rejectFn(new ChunkRequestError(`分片 ${index} 从 ${peerId} 请求超时`, 'timeout'))
    }, timeoutOf(peerId))

    const entry: Inflight = { rid, startedAt: performance.now(), timer, promise, resolve: resolveFn, reject: rejectFn }
    inflight.set(key, entry)

    const control: PeerControl = { t: 'req', rid, idx: index }
    if (!opts.send(peerId, encodeControl(control))) {
      window.clearTimeout(timer)
      inflight.delete(key)
      failed.value += 1
      rejectFn(new ChunkRequestError(`到 ${peerId} 的通道不可用`, 'send'))
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
    entry.reject(new ChunkRequestError(`分片 ${msg.idx} 请求失败：${msg.code ?? '未知原因'}`, 'remote'))
    return true
  }

  /**
   * 处理二进制帧。
   * 命中在途请求就完成它；未命中（M3 的推送式分发或迟到响应）也照样返回，由调用方决定怎么用。
   *
   * 迟到的响应**不是丢弃**：上层照样把它写进分片仓库，所以"超时换父"最坏只是多一次重复传输，
   * 不会丢数据（这一点决定了 T3 把超时收紧到 max(500ms, 3×RTT) 的代价上限）。
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
    // 只有命中在途请求的才算"这一条边的交付速率"：迟到/推送的帧没有配对的耗时，
    // 拿它算速率会得到一个假的巨大值（分母≈0）。
    if (entry) {
      noteEdgeDelivery(peerId, media.payload.length, ms, media.kind !== KIND_INIT && media.chunkIndex !== 0)
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

  /** 某个父节点实测吞吐（Bytes/s）；没测到返回 0。 */
  function edgeRateBps(peerId: string): number {
    return edges.value.get(peerId)?.rateBps ?? 0
  }

  /** 所有父节点实测吞吐之和：多父条带化时聚合吞吐才是"我这条下游能吃多快"。 */
  function totalEdgeRateBps(): number {
    let sum = 0
    for (const stat of edges.value.values()) {
      sum += stat.rateBps
    }
    return sum
  }

  /** 诊断快照：按吞吐从高到低（界面上最关心的排前面）。 */
  function edgeStatList(): EdgeStat[] {
    return [...edges.value.values()].map((stat) => ({ ...stat })).sort((a, b) => b.rateBps - a.rateBps)
  }

  function reset() {
    for (const entry of inflight.values()) {
      window.clearTimeout(entry.timer)
      entry.reject(new ChunkRequestError('连接已重置', 'reset'))
    }
    inflight.clear()
    samples.length = 0
  }

  /** 换父/离开房间时连边账本一起作废：留着旧父节点的速率会误导在途推导。 */
  function resetEdges() {
    edges.value.clear()
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
    resetEdges,
    edgeRateBps,
    totalEdgeRateBps,
    edgeStatList,
    edges,
    delivered,
    timedOut,
    failed,
  }
}
