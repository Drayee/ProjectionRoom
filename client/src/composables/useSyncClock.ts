import { ref } from 'vue'
import type { PlaybackState } from '../types/protocol'

/** 时钟滤波窗口：取窗口内最小偏移，等价于"延迟最小的那次测量最接近真值"（SPEC §7.2）。 */
const FILTER_WINDOW = 30

/**
 * 追赶速率上限：落后时最多 +25%。
 *
 * 原来的 ±5% 只够抹平几十毫秒的抖动；落后 5 秒时按 5% 追要 100 秒 ——
 * 表现就是"永远慢一截，直到某次大幅 seek"。放宽到 25% 后 5 秒缺口约 20 秒追平，
 * 而且不触发清缓冲重灌，观感连续。
 */
export const MAX_CATCHUP_RATE = 0.25
/** 超前时的减速上限：只做小幅微调，避免把画面拖慢得太明显。 */
export const MAX_SLOWDOWN_RATE = 0.1
/** 超过这个偏差（秒）就不再靠速率追，改成跳转/重灌。 */
const RATE_LIMIT_SEC = 6

export interface ProgressSample {
  currentTime: number
  hostClockMs: number
  clockEpoch?: string
  paused: boolean
  rate: number
  seq: number
  /**
   * 逐跳时钟中继：父节点发出这条进度时它自己的本地时钟。
   *
   * 主播直接发的报文里它等于 hostClockMs；中继转发时会改写成中继自己的时钟。
   * 0 表示"这条报文没有经过中继"（例如服务端转发的 room-control）。
   */
  parentClockMs?: number
  /** 逐跳时钟中继：父节点相对主播的偏移估计（主播直接发时为 0）。 */
  parentOffsetMs?: number
}

export type CorrectionMode = 'idle' | 'ok' | 'rate' | 'jump' | 'seek'

export interface Correction {
  mode: CorrectionMode
  rate: number
  target: number
}

/**
 * 播放同步的时钟核心（SPEC §7.1–§7.4）。
 *
 * 不变量：
 *   - 主播是唯一时间权威，本地只负责估算"我与主播时钟的偏移"；
 *   - offset 用最小滤波估计，因为每个样本都含正向延迟，最小值最接近真值；
 *   - 每条进度都带单调 seq，乱序与重复一律丢弃；
 *   - clockEpoch 变化（主播重载页面）必须整体重置，否则全房间一起跳错位置。
 *
 * 逐跳中继（本轮新增，修 ALGORITHM P1）：
 *   偏移估计改成**两级相加** —— 本节点只测"我到父节点"这一跳，再加上父节点上报的
 *   "它到主播"的偏移。多跳时不再把路径上每一跳的排队延迟都算进自己的偏移里，
 *   否则深度 2–3 的节点会稳定偏几百毫秒（实测过 ~350ms）。
 */
export function useSyncClock() {
  const playback = ref<PlaybackState>({ paused: true, currentTime: 0, hostClockMs: 0, rate: 1, seq: 0 })
  /** 我到主播的总偏移（两级相加的结果），expectedAt 用它。 */
  const offsetMs = ref(0)
  /** 只含"我到父节点"这一跳的滤波结果（调试与验收用）。 */
  const hopOffsetMs = ref(0)
  /** 父节点上报的"它到主播"的偏移。 */
  const parentOffsetMs = ref(0)
  const epoch = ref('')
  const ready = ref(false)
  const drift = ref(0)
  const rejected = ref(0)

  /** 本跳的偏移样本（now - parentClockMs）。 */
  const samples: number[] = []
  let lastSeq = -1
  /** 偏移最小值最后一次下降的时刻：最小值只在更小样本到来时下降，停止下降即说明已收敛。 */
  let lastMinDropAt = 0

  function acceptSample(hopSample: number, now: number, parentOffset: number) {
    const previous = samples.length > 0 ? Math.min(...samples) : Number.POSITIVE_INFINITY
    samples.push(hopSample)
    if (samples.length > FILTER_WINDOW) {
      samples.shift()
    }
    hopOffsetMs.value = Math.min(...samples)
    parentOffsetMs.value = parentOffset
    // 两级相加：本跳的滤波结果 + 父节点到主播的偏移。
    // 父节点的估计是它自己算的，随最新报文刷新即可，不需要跟本跳样本一起滤波。
    offsetMs.value = parentOffset + hopOffsetMs.value
    if (hopOffsetMs.value < previous) {
      lastMinDropAt = now
    }
  }

  /** 从一条进度里取出"这一跳"的时钟锚点与父节点偏移。 */
  function hopOf(msg: ProgressSample): { parentClock: number; parentOffset: number } {
    const parentClock = msg.parentClockMs && msg.parentClockMs > 0 ? msg.parentClockMs : msg.hostClockMs
    const parentOffset = typeof msg.parentOffsetMs === 'number' ? msg.parentOffsetMs : 0
    return { parentClock, parentOffset }
  }

  function resetEpoch(next: string) {
    epoch.value = next
    samples.length = 0
    offsetMs.value = 0
    hopOffsetMs.value = 0
    parentOffsetMs.value = 0
    ready.value = false
    lastSeq = -1
  }

  /** 收到一条权威进度。返回 false 表示被丢弃（乱序/重复）。 */
  function onProgress(msg: ProgressSample, now = performance.now()): boolean {
    const msgEpoch = msg.clockEpoch ?? ''
    if (msgEpoch !== epoch.value) {
      resetEpoch(msgEpoch)
    }
    if (msg.seq <= lastSeq) {
      rejected.value += 1
      return false
    }
    lastSeq = msg.seq

    const hop = hopOf(msg)
    acceptSample(now - hop.parentClock, now, hop.parentOffset)
    ready.value = true
    playback.value = {
      paused: msg.paused,
      currentTime: msg.currentTime,
      hostClockMs: msg.hostClockMs,
      rate: msg.rate > 0 ? msg.rate : 1,
      seq: msg.seq,
      clockEpoch: msg.clockEpoch,
    }

    return true
  }

  /**
   * 时钟锚点：来自服务端中继的离散指令或入房快照。
   *
   * 它不参与 progress 的 seq 过滤 —— progress 的 seq 由主播单独编号，
   * 而 room-control 的 seq 由服务端编号，两者不是同一个序列，混用会互相丢弃。
   * 锚点只贡献一个偏移样本并刷新播放状态。
   *
   * 服务端转发的是主播的原始报文（没有经过 P2P 中继），因此 parentClockMs 为 0 时
   * 直接退回 hostClockMs：这条路径的额外延迟只有一跳 WS，由最小滤波吸收。
   */
  function onAnchor(msg: ProgressSample, now = performance.now()): void {
    const msgEpoch = msg.clockEpoch ?? ''
    if (msgEpoch !== epoch.value) {
      resetEpoch(msgEpoch)
    }

    const hop = hopOf(msg)
    acceptSample(now - hop.parentClock, now, hop.parentOffset)
    ready.value = true
    playback.value = {
      paused: msg.paused,
      currentTime: msg.currentTime,
      hostClockMs: msg.hostClockMs,
      rate: msg.rate > 0 ? msg.rate : 1,
      seq: playback.value.seq,
      clockEpoch: msg.clockEpoch,
    }
  }

  /** 主播此刻应播到的位置（秒）；时钟未就绪时返回 null。 */
  function expectedAt(now = performance.now()): number | null {
    if (!ready.value) {
      return null
    }
    const state = playback.value
    return state.currentTime + (now - offsetMs.value - state.hostClockMs) / 1000
  }

  /**
   * 三级漂移矫正：速率微调（含分级加速追赶） → 跳关键帧 → seek（SPEC §7.4）。
   *
   * 速率不再是固定 ±5%：缺口越大加速越猛（上限 +25%），
   * 这样"落后 3–6 秒"能在十几秒内平滑追平，而不是等到超过阈值来一次大幅跳转。
   */
  function correction(videoTime: number, now = performance.now()): Correction {
    const target = expectedAt(now)
    if (target === null || playback.value.paused) {
      return { mode: 'idle', rate: 1, target: videoTime }
    }

    const diff = videoTime - target
    drift.value = diff
    const abs = Math.abs(diff)

    if (abs <= 0.1) {
      return { mode: 'ok', rate: 1, target }
    }
    if (abs <= RATE_LIMIT_SEC) {
      if (diff > 0) {
        return { mode: 'rate', rate: 1 - Math.min(MAX_SLOWDOWN_RATE, abs * 0.2), target }
      }
      return { mode: 'rate', rate: 1 + Math.min(MAX_CATCHUP_RATE, abs * 0.08), target }
    }
    if (abs <= RATE_LIMIT_SEC + 3) {
      return { mode: 'jump', rate: 1, target }
    }
    return { mode: 'seek', rate: 1, target }
  }

  function setPaused(paused: boolean) {
    playback.value = { ...playback.value, paused }
    drift.value = 0
  }

  /** 已经积累的偏移样本数：样本太少时估计不可信，门控要等它收敛。 */
  function sampleCount(): number {
    return samples.length
  }

  /** 偏移估计已经稳定了多久（秒）：最小值一段时间内没再下降 = 已收敛。 */
  function settledSeconds(now = performance.now()): number {
    if (lastMinDropAt === 0) {
      return 0
    }
    return (now - lastMinDropAt) / 1000
  }

  return {
    playback,
    offsetMs,
    hopOffsetMs,
    parentOffsetMs,
    epoch,
    ready,
    drift,
    rejected,
    sampleCount,
    settledSeconds,
    onProgress,
    onAnchor,
    expectedAt,
    correction,
    setPaused,
  }
}
