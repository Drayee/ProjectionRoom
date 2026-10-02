import { ref } from 'vue'
import type { PlaybackState } from '../types/protocol'

/** 时钟滤波窗口：取窗口内最小偏移，等价于"延迟最小的那次测量最接近真值"（SPEC §7.2）。 */
const FILTER_WINDOW = 30

export interface ProgressSample {
  currentTime: number
  hostClockMs: number
  clockEpoch?: string
  paused: boolean
  rate: number
  seq: number
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
 */
export function useSyncClock() {
  const playback = ref<PlaybackState>({ paused: true, currentTime: 0, hostClockMs: 0, rate: 1, seq: 0 })
  const offsetMs = ref(0)
  const epoch = ref('')
  const ready = ref(false)
  const drift = ref(0)
  const rejected = ref(0)

  const samples: number[] = []
  let lastSeq = -1
  /** 偏移最小值最后一次下降的时刻：最小值只在更小样本到来时下降，停止下降即说明已收敛。 */
  let lastMinDropAt = 0

  function acceptSample(sample: number, now: number) {
    const previous = samples.length > 0 ? Math.min(...samples) : Number.POSITIVE_INFINITY
    samples.push(sample)
    if (samples.length > FILTER_WINDOW) {
      samples.shift()
    }
    offsetMs.value = Math.min(...samples)
    if (offsetMs.value < previous) {
      lastMinDropAt = now
    }
  }

  function resetEpoch(next: string) {
    epoch.value = next
    samples.length = 0
    offsetMs.value = 0
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

    acceptSample(now - msg.hostClockMs, now)
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
   */
  function onAnchor(msg: ProgressSample, now = performance.now()): void {
    const msgEpoch = msg.clockEpoch ?? ''
    if (msgEpoch !== epoch.value) {
      resetEpoch(msgEpoch)
    }

    acceptSample(now - msg.hostClockMs, now)
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

  /** 三级漂移矫正：速率微调 → 跳关键帧 → seek（SPEC §7.4）。 */
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
    if (abs <= 0.5) {
      return { mode: 'rate', rate: diff > 0 ? 0.95 : 1.05, target }
    }
    if (abs <= 1.5) {
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
