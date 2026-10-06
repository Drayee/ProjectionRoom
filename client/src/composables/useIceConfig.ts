import { ref } from 'vue'

/**
 * ICE 配置的 TTL 缓存与刷新（打洞优化第 ①② 条）。
 *
 * 为什么不能"进房拉一次就完事"：服务端每约 60 秒按探测结果**重算**本次下发的
 * STUN 列表（Google/Cloudflare 为粘性条目，其余按 RTT 排序取前几条），
 * 所以列表内容会随窗口变化。客户端必须按服务端给的 TTL 周期重拉，
 * 并在**列表真的变了**时才动 PeerConnection —— 无脑重建连接会把正在跑的播放打断。
 *
 * 三条硬约束（都来自实测）：
 *   1. 余量必须留够：本部署的隧道 RTT 有 0.7–2.5s，余量取 max(60s, 0.2×TTL)。
 *      TTL 比余量还短的部署（验收用的 `PR_ICE_TTL=30s`）退化为 30% TTL，
 *      否则"到期前重拉"直接变成负数，等于永不刷新。
 *   2. 用**本地收到的时刻**锚定下一次刷新，而不是服务端的 `expiresAt` 绝对值：
 *      客户端时钟与服务端可能有偏差，用绝对时间会算出一个已经过去/还很远的时刻。
 *      `expiresAt` 仍然存下来（诊断与"是否已过期"判断用）。
 *   3. 刷新失败绝不能影响播放：退避重试、只记日志，不动任何连接。
 */

/** 服务端探测出来的单条 STUN 评分（契约字段，见 captain 冻结的 `/api/ice` 形状）。 */
export interface IceScore {
  url: string
  rttMs: number
  ok: boolean
  score: number
  selected?: boolean
}

export interface IceProbe {
  probedAt?: number
  intervalSeconds?: number
  /** 本次下发列表的评分依据。服务端理论上一定给，缺了就退化成空数组（不编造）。 */
  scores?: IceScore[]
}

/** `/api/ice` 与 `POST /api/rooms` 的 ICE 部分（纯加法扩展，旧的 iceServers 形状不变）。 */
export interface IcePayload {
  iceServers?: RTCIceServer[]
  ttlSeconds?: number
  expiresAt?: number
  probe?: IceProbe
}

/** 上一次刷新的原因。排障与验收靠它区分"到期续期"和"列表真的变了"。 */
export type IceRefreshReason =
  | 'initial'
  | 'unchanged'
  | 'servers-changed'
  | 'fetch-failed'
  | 'empty-response'
  | ''

/** 服务端没给 TTL 时的兜底：契约默认 300 秒。 */
export const DEFAULT_ICE_TTL_SECONDS = 300
/** 余量下限：覆盖隧道 RTT（0.7–2.5s）与调度抖动，取 60s。 */
export const ICE_REFRESH_MARGIN_SECONDS = 60
/** 余量比例：TTL 的 20%。 */
export const ICE_REFRESH_MARGIN_RATIO = 0.2
/** 余量兜底后的最小刷新间隔，避免短 TTL 部署把 /api/ice 打爆。 */
const MIN_REFRESH_DELAY_SECONDS = 5
/** 看门狗周期：后台标签页的定时器会被节流，单一 setTimeout 可能整体晚到。 */
const WATCH_INTERVAL_MS = 5000
/** 刷新失败后的退避序列（毫秒）。 */
const RETRY_BACKOFF_MS = [1000, 2000, 4000, 8000, 15000]

/** 刷新余量（秒）：`max(60s, 0.2×ttl)`。 */
export function iceRefreshMarginSeconds(ttlSeconds: number): number {
  const ttl = ttlSeconds > 0 ? ttlSeconds : DEFAULT_ICE_TTL_SECONDS
  return Math.max(ICE_REFRESH_MARGIN_SECONDS, ttl * ICE_REFRESH_MARGIN_RATIO)
}

/**
 * 下一次刷新的延迟（毫秒）。正常态 = `ttl - 余量`；
 * TTL 比余量还短时退化为 `max(5s, 0.3×ttl)` —— 仍然**早于**到期时刻。
 */
export function iceRefreshDelayMs(ttlSeconds: number): number {
  const ttl = ttlSeconds > 0 ? ttlSeconds : DEFAULT_ICE_TTL_SECONDS
  const remaining = ttl - iceRefreshMarginSeconds(ttl)
  const seconds = remaining > 0 ? remaining : Math.max(MIN_REFRESH_DELAY_SECONDS, ttl * 0.3)
  return Math.round(Math.min(seconds, ttl) * 1000)
}

/** 把 iceServers 里的 urls 归一化（trim + 小写 + 排序），用于"列表到底变没变"的比较。 */
export function normalizeIceUrls(list: readonly RTCIceServer[] | undefined): string[] {
  const out: string[] = []
  for (const entry of list ?? []) {
    const urls = entry?.urls
    if (typeof urls === 'string') {
      out.push(urls)
    } else if (Array.isArray(urls)) {
      out.push(...urls)
    }
  }
  return out
    .map((url) => (typeof url === 'string' ? url.trim().toLowerCase() : ''))
    .filter((url) => url !== '')
    .sort()
}

/** 只保留有 urls 的条目：空条目会让 RTCPeerConnection 构造直接抛。 */
function sanitizeServers(list: readonly RTCIceServer[] | undefined): RTCIceServer[] {
  return (list ?? []).filter((entry) => normalizeIceUrls([entry]).length > 0)
}

/**
 * 凭证指纹：TURN 已退役，但服务端将来若再给凭证，它也必须被推到已存在的 PC 上。
 * 单独算一份是为了"凭证变了要 setConfiguration，但**不需要**重启 ICE"。
 */
function credentialKey(list: readonly RTCIceServer[]): string {
  return list
    .map((entry) => `${entry.username ?? ''}:${entry.credential ?? ''}`)
    .sort()
    .join('|')
}

export interface IceSnapshot {
  ttlSeconds: number
  expiresAt: number
  refreshCount: number
  changeCount: number
  failedRefreshCount: number
  lastRefreshReason: IceRefreshReason
  lastRefreshAt: number
  nextRefreshAt: number
  refreshMarginSec: number
  lastError: string
  serverUrls: string[]
  scores: IceScore[]
}

export function useIceConfig(opts: {
  /** 真正去拉一次配置；失败要抛错（由本模块负责退避重试）。 */
  fetchPayload: () => Promise<IcePayload>
  /** 配置有变化（urls 或凭证）：调用方负责 setConfiguration；urlsChanged 时才该重启 ICE。 */
  onChanged: (servers: RTCIceServer[], info: { urlsChanged: boolean }) => void
  log?: (text: string) => void
}) {
  /** 当前生效的 ICE 列表（PC 构造只读它）。 */
  const servers = ref<RTCIceServer[]>([])
  const ttlSeconds = ref(0)
  const expiresAt = ref(0)
  const probe = ref<IceProbe | null>(null)

  const refreshCount = ref(0)
  const changeCount = ref(0)
  const failedRefreshCount = ref(0)
  const lastRefreshReason = ref<IceRefreshReason>('')
  const lastRefreshAt = ref(0)
  const lastError = ref('')

  /**
   * 会话代号。切房/离开时 +1，所有在途的 fetch 与看门狗都靠它作废：
   * 否则"上一个房间的定时器"会拿着旧代号去打新房间的接口。
   */
  let generation = 0
  let ticker: number | undefined
  let inFlight = false
  let retryIndex = 0
  let nextRefreshAtMs = 0
  let urlKey = ''
  let credKey = ''

  const log = (text: string) => opts.log?.(text)

  function clearTicker() {
    if (ticker !== undefined) {
      window.clearInterval(ticker)
      ticker = undefined
    }
  }

  function schedule(delayMs: number) {
    nextRefreshAtMs = Date.now() + delayMs
  }

  /**
   * 应用一份配置。返回值告诉调用方"urls 是否变了"（变没变决定要不要重启 ICE）。
   * `expiresAt` 一律以**本地收到时刻 + TTL** 重算，服务端的绝对值只在与本地一致或更晚时才采纳。
   */
  function apply(payload: IcePayload): { urlsChanged: boolean; empty: boolean } {
    const nowSec = Math.floor(Date.now() / 1000)
    const incoming = sanitizeServers(payload.iceServers)

    if (incoming.length === 0) {
      // 空列表 = 服务端这次没给（或探测全军覆没）：保住手上这份能用的配置，
      // 但仍然按 TTL 继续重拉 —— 清空等于把已经在跑的连接置于无候选可用的境地。
      lastError.value = '服务端本次未下发任何 iceServers'
      return { urlsChanged: false, empty: true }
    }

    const nextTtl = payload.ttlSeconds && payload.ttlSeconds > 0 ? payload.ttlSeconds : DEFAULT_ICE_TTL_SECONDS
    const nextUrlKey = normalizeIceUrls(incoming).join('|')
    const nextCredKey = credentialKey(incoming)
    // 第一次拿到列表（此前什么都不已知）不算"变化"：没有可比对象，
    // 也不该触发 ICE restart —— 那时候连接本来就还没建。
    const known = urlKey !== ''
    const urlsChanged = known && nextUrlKey !== urlKey
    const credsChanged = known && nextCredKey !== credKey

    servers.value = incoming
    ttlSeconds.value = nextTtl
    // 服务端给了 expiresAt 且比本地算出来的更晚，就信服务端（它的 TTL 窗口从探测时刻算起）；
    // 两个都没有时才退化成"本地收到时刻 + 默认 TTL"。
    const localExpiry = nowSec + nextTtl
    expiresAt.value = payload.expiresAt && payload.expiresAt > nowSec ? payload.expiresAt : localExpiry
    probe.value = payload.probe ? { ...payload.probe, scores: payload.probe.scores ?? [] } : null
    urlKey = nextUrlKey
    credKey = nextCredKey

    if (urlsChanged) {
      changeCount.value += 1
    }
    // 第一次拿到列表也要"应用"一次（已有 PC 可能还是空配置），但只有**真的变了**才重启 ICE。
    if (!known || urlsChanged || credsChanged) {
      opts.onChanged(incoming, { urlsChanged })
    }
    return { urlsChanged, empty: false }
  }

  /**
   * 拉一次配置。
   * `trigger` 只描述触发来源；写进 lastRefreshReason 的是**结果**
   *（initial / unchanged / servers-changed / empty-response / fetch-failed）。
   */
  async function refresh(trigger: 'initial' | 'ttl' | 'retry') {
    if (inFlight) {
      return
    }
    inFlight = true
    const gen = generation
    try {
      const payload = await opts.fetchPayload()
      if (gen !== generation) {
        return
      }
      refreshCount.value += 1
      lastRefreshAt.value = Date.now()
      const first = refreshCount.value === 1
      retryIndex = 0

      const result = apply(payload)
      if (result.empty) {
        lastRefreshReason.value = 'empty-response'
      } else if (first) {
        lastRefreshReason.value = 'initial'
      } else if (result.urlsChanged) {
        lastRefreshReason.value = 'servers-changed'
      } else {
        lastRefreshReason.value = 'unchanged'
      }

      if (result.urlsChanged) {
        log(`ICE 列表变化（第 ${changeCount.value} 次）：${normalizeIceUrls(payload.iceServers).join(', ')}`)
      }
      lastError.value = result.empty ? lastError.value : ''
      schedule(iceRefreshDelayMs(ttlSeconds.value))
    } catch (err) {
      if (gen !== generation) {
        return
      }
      failedRefreshCount.value += 1
      lastRefreshReason.value = 'fetch-failed'
      lastError.value = err instanceof Error ? err.message : String(err)
      const backoff = RETRY_BACKOFF_MS[Math.min(retryIndex, RETRY_BACKOFF_MS.length - 1)]
      retryIndex += 1
      // 失败只影响"下一次什么时候再拉"，绝不触碰任何连接：播放必须照常。
      log(`ICE 配置刷新失败（第 ${failedRefreshCount.value} 次，${backoff}ms 后重试）：${lastError.value}`)
      schedule(Math.max(backoff, trigger === 'initial' ? 1000 : 0))
    } finally {
      inFlight = false
    }
  }

  /** 看门狗：到期（或后台节流后补上）就重拉。 */
  function startTicker() {
    clearTicker()
    ticker = window.setInterval(() => {
      if (Date.now() >= nextRefreshAtMs && !inFlight) {
        void refresh('ttl')
      }
    }, WATCH_INTERVAL_MS)
  }

  /**
   * 进房时启动：立刻拉一次，并按 TTL 排下一次。
   * 计数归零（同一个房间会话内的观测是干净的），但**不清空**已有的列表 ——
   * 主播页在 POST /api/rooms 那一步已经拿到过一份，PC 用它构造是合法的，
   * 等 /api/ice 回来做一次"变没变"的比较即可。
   */
  function start() {
    generation += 1
    refreshCount.value = 0
    changeCount.value = 0
    failedRefreshCount.value = 0
    lastRefreshReason.value = ''
    lastRefreshAt.value = 0
    lastError.value = ''
    retryIndex = 0
    inFlight = false
    nextRefreshAtMs = 0
    startTicker()
    void refresh('initial')
  }

  /** 离开房间：停掉定时器并作废在途请求。 */
  function stop() {
    generation += 1
    clearTicker()
    inFlight = false
    nextRefreshAtMs = 0
  }

  /** 顺手喂一份配置（`POST /api/rooms` 的响应），让 PC 构造时就有列表可用。 */
  function seed(payload: IcePayload, label: string) {
    const result = apply(payload)
    lastRefreshReason.value = result.empty ? 'empty-response' : 'initial'
    log(`ICE 配置来自 ${label}：${urlKey || '（空）'}`)
  }

  function snapshot(): IceSnapshot {
    return {
      ttlSeconds: ttlSeconds.value,
      expiresAt: expiresAt.value,
      refreshCount: refreshCount.value,
      changeCount: changeCount.value,
      failedRefreshCount: failedRefreshCount.value,
      lastRefreshReason: lastRefreshReason.value,
      lastRefreshAt: lastRefreshAt.value,
      nextRefreshAt: nextRefreshAtMs,
      refreshMarginSec: Math.round(iceRefreshMarginSeconds(ttlSeconds.value)),
      lastError: lastError.value,
      serverUrls: normalizeIceUrls(servers.value),
      scores: probe.value?.scores ?? [],
    }
  }

  function secondsUntilExpiry(): number {
    if (expiresAt.value <= 0) {
      return 0
    }
    return Math.max(0, Math.round(expiresAt.value - Date.now() / 1000))
  }

  function secondsUntilRefresh(): number {
    if (nextRefreshAtMs <= 0) {
      return 0
    }
    return Math.max(0, Math.round((nextRefreshAtMs - Date.now()) / 1000))
  }

  return {
    servers,
    probe,
    ttlSeconds,
    expiresAt,
    refreshCount,
    changeCount,
    failedRefreshCount,
    lastRefreshReason,
    lastError,
    start,
    stop,
    seed,
    snapshot,
    secondsUntilExpiry,
    secondsUntilRefresh,
  }
}
