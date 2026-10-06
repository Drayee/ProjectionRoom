// 调度算术：发送队列的紧迫度排序、取数请求超时、在途上限推导。
//
// 为什么单独抽一份 **纯函数** 模块（不 import Vue / 不碰 DOM / 不读时钟）：
// 这三件事正是本轮"卡顿优化"的判据本体 ——
//   · 排序错了，紧要分片会排在后面（起播/追赶变慢）；
//   · 在途算错了，要么饿死（并发太低），要么把链路压垮；
//   · 超时算错了，跨运营商抖动下会误判换父，反而制造重复请求。
// 做成纯函数才验证得动：test/script/verify-schedule.mjs 直接 import 本文件断言，
// 不需要浏览器、不需要 MediaSource、不需要真实 RTC。

/** 入队任务的最小形状：只需要分片序号，其余字段原样透传（泛型保住调用方的类型）。 */
export interface ServeTaskLike {
  index: number
  /**
   * 入队序号（单调递增）：同紧迫度时"先到先发"。
   * 缺省时依赖 Array.prototype.sort 的稳定性（ES2019 起规范保证）保持原顺序。
   */
  seq?: number
}

/**
 * "已经播过"的那一组在排序键上的起点。
 *
 * 正向分片的键是 `index - playheadSeg`，最大也就一个预取窗口（几十），
 * 所以取一个远大于它的常量即可把"已播过"整组稳定地压到最后，不需要额外的比较分支。
 */
export const PLAYED_PRIORITY_BASE = 1_000_000

/**
 * 紧迫度：**越小越先发**。
 *
 *   1. 播放头未知（没有索引 / 时钟没就绪）→ 退化为按序号升序（`index` 本身就是键）；
 *   2. 还没播到（index ≥ 播放头）→ `index - playheadSeg`，离播放头越近越急；
 *   3. 已经播过（index < 播放头）→ 整组排最后；组内取"离播放头近的"优先
 *      —— 它们才是能立刻补上播放缺口的那几片，离得远的已经彻底没用了。
 */
export function servePriority(index: number, playheadSeg: number | null): number {
  if (playheadSeg === null || !Number.isFinite(playheadSeg)) {
    return index
  }
  if (index < playheadSeg) {
    return PLAYED_PRIORITY_BASE + (playheadSeg - index)
  }
  return index - playheadSeg
}

/** 两条入队任务的先后比较（排序与选优共用同一套判据，避免两处漂移）。 */
export function compareServeTasks(a: ServeTaskLike, b: ServeTaskLike, playheadSeg: number | null): number {
  const pa = servePriority(a.index, playheadSeg)
  const pb = servePriority(b.index, playheadSeg)
  if (pa !== pb) {
    return pa - pb
  }
  return (a.seq ?? 0) - (b.seq ?? 0)
}

/** 按紧迫度排序；不修改入参。播放头取不到时退化为按序号升序。 */
export function orderServeTasks<T extends ServeTaskLike>(tasks: readonly T[], playheadSeg: number | null): T[] {
  return [...tasks].sort((a, b) => compareServeTasks(a, b, playheadSeg))
}

/**
 * 取"最该发的那一条"在队列里的下标（不改动队列）；空队列返回 -1。
 *
 * 返回下标而不是元素：调用方（drainServeQueue）需要在自己持锁的那一刻原子地把它移除，
 * 否则并发 drain 会把同一个任务发两次。
 */
export function pickServeTaskIndex(tasks: readonly ServeTaskLike[], playheadSeg: number | null): number {
  let best = -1
  for (let i = 0; i < tasks.length; i += 1) {
    if (best < 0 || compareServeTasks(tasks[i], tasks[best], playheadSeg) < 0) {
      best = i
    }
  }
  return best
}

// ---------- 在途上限（T2-2）----------

/** 速率未被测量时的在途上限：与改动前的固定值一致，保证起播阶段行为不变。 */
export const INFLIGHT_FALLBACK = 4
export const INFLIGHT_MIN = 2
export const INFLIGHT_MAX = 8
/**
 * 希望在途窗口覆盖的秒数。
 *
 * 它决定的是"并发多少片才填得满 0.4s 的链路"：0.4s 大致是"播放头吃一口"的节拍，
 * 在途数据量低于它，链路就会在"请求—等待—到达"之间空转（这就是卡顿的微观来源）。
 */
export const INFLIGHT_HEADROOM_SEC = 0.4

/**
 * 由**实测边速率**推导在途上限：
 *
 *   inflight = clamp(2, 8, ceil(edgeRate × 0.4s / avgSegmentBytes))
 *
 * 速率或分片大小未知（≤0）时回落到 4 —— 与改动前的固定 `MAX_INFLIGHT = 4` 一致。
 */
export function deriveInflightLimit(
  edgeRateBps: number,
  avgSegmentBytes: number,
  bounds: { fallback?: number; min?: number; max?: number; headroomSec?: number } = {},
): number {
  const fallback = bounds.fallback ?? INFLIGHT_FALLBACK
  const min = bounds.min ?? INFLIGHT_MIN
  const max = bounds.max ?? INFLIGHT_MAX
  const headroom = bounds.headroomSec ?? INFLIGHT_HEADROOM_SEC
  if (!(edgeRateBps > 0) || !(avgSegmentBytes > 0)) {
    return fallback
  }
  const raw = Math.ceil((edgeRateBps * headroom) / avgSegmentBytes)
  return Math.min(max, Math.max(min, raw))
}

// ---------- 取数请求超时（T3-1）----------

/**
 * RTT 未测得时的兜底超时（毫秒）。
 *
 * 数据通道的 ping 每 3s 才刷一次 RTT（useWebRTC PING_INTERVAL_MS），
 * 起播最初几秒 peer.rttMs 就是 0 —— 这时必须有兜底值，否则超时会退到 0。
 */
export const REQUEST_TIMEOUT_FALLBACK_MS = 800
export const REQUEST_TIMEOUT_FLOOR_MS = 500
export const REQUEST_TIMEOUT_RTT_FACTOR = 3
/** "按该边峰值速率传完一个平均分片"所需时间的倍数：超时必须容得下传输本身。 */
export const REQUEST_TIMEOUT_DELIVERY_FACTOR = 2
/**
 * 超时上限：保持改动前那个固定值 3000ms —— 这样新公式**任何情况下都不会比以前等得更久**
 * （高 RTT 链路若将来需要更宽，抬这个常量即可）。
 */
export const REQUEST_TIMEOUT_CAP_MS = 3000

/**
 * 取数请求超时 = `clamp(500ms, 3000ms, max(3 × 该父实测 RTT, 2 × 预计传输时间))`；
 * 两个输入都没有时用 800ms 兜底。
 *
 * 改动前是**固定 3000ms**（useChunkRequester 的 DEFAULT_TIMEOUT_MS，room.ts 也没传覆盖值）：
 * 父节点已经死了也要白等 3s 才换父，这就是"卡在一个坏父上"的主要来源。
 *
 * 为什么 RTT 之外还要看"预计传输时间"：RTT 来自数据通道上每 3s 一次的**控制报文** ping，
 * 它不含分片自身的传输耗时。跨运营商时 200 KB 分片跑在 2 Mbps 上要约 800ms，而 3×200ms=600ms
 * —— 只按 RTT 推导会**必然误判超时**，代价是同一个分片被重复请求（浪费上行、加剧卡顿）。
 *
 * 关键：预计传输时间必须用该边的**峰值速率**（健康时测到的最好成绩）而不是当前速率。
 * 用当前速率会得到相反的效果 —— 一个正在退化的父节点（峰值 20 Mbps、此刻 1.5 Mbps）
 * 会被算成"传一片要 1000 秒"，超时反而被拖到上限，坏父从此换不掉。
 * 峰值口径下：稳定但中等的边拿到更宽的超时（少误判），退化的边仍按它的历史最好成绩估，
 * 于是超时停在 500ms 下限、照旧快速换父。
 */
export function deriveRequestTimeoutMs(
  rttMs: number,
  opts: {
    fallbackMs?: number
    floorMs?: number
    factor?: number
    /** 按该边**峰值速率**传完一个平均分片预计要多久（ms）；无法估计时传 0/省略。 */
    expectedDeliveryMs?: number
    deliveryFactor?: number
    capMs?: number
  } = {},
): number {
  const fallbackMs = opts.fallbackMs ?? REQUEST_TIMEOUT_FALLBACK_MS
  const floorMs = opts.floorMs ?? REQUEST_TIMEOUT_FLOOR_MS
  const factor = opts.factor ?? REQUEST_TIMEOUT_RTT_FACTOR
  const expected = opts.expectedDeliveryMs ?? 0
  const deliveryFactor = opts.deliveryFactor ?? REQUEST_TIMEOUT_DELIVERY_FACTOR
  const capMs = opts.capMs ?? REQUEST_TIMEOUT_CAP_MS
  if (!(rttMs > 0) && !(expected > 0)) {
    return fallbackMs
  }
  const byRtt = rttMs > 0 ? factor * rttMs : 0
  const byDelivery = expected > 0 ? deliveryFactor * expected : 0
  return Math.min(capMs, Math.max(floorMs, Math.round(Math.max(byRtt, byDelivery))))
}

/**
 * 同一个分片对**同一个父节点**的连续失败上限。
 *
 * 到顶就把这个父节点暂时降权（useTopology 的 avoid 集合）：
 * 不禁用（避免"无父可问"），但不再优先选它 —— 换父要快，也要能回来。
 */
export const MAX_FAILURES_PER_EDGE = 2

/** 父节点降权的持续时长（毫秒）：到点自动解禁，避免一次抖动把它永久拉黑。 */
export const PARENT_AVOID_TTL_MS = 20_000

/** 单次 fetchChunk 内部最多换几次父（主父 → 备用父）。超过就交给下一个调度 tick。 */
export const MAX_FETCH_FAILOVER_HOPS = 2
