import { computed, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import { useAuthStore } from './auth'
import { useSignaling } from '../composables/useSignaling'
import { useMediaIndex } from '../composables/useMediaIndex'
import { useChunkStore } from '../composables/useChunkStore'
import { chunkErrorCode, useChunkRequester } from '../composables/useChunkRequester'
import { useChunkPlayer } from '../composables/useChunkPlayer'
import { useSyncClock } from '../composables/useSyncClock'
import { useWebRTC } from '../composables/useWebRTC'
import { useIceConfig, type IcePayload, type IceSnapshot } from '../composables/useIceConfig'
import { useTopology } from '../composables/useTopology'
import { segmentIndexAt } from '../types/media'
import { KIND_INIT, KIND_MEDIA, encodeControl, type DecodedMedia } from '../types/codec'
import { Action, T } from '../types/protocol'
import {
  INFLIGHT_FALLBACK,
  MAX_FETCH_FAILOVER_HOPS,
  PARENT_AVOID_TTL_MS,
  deriveInflightLimit,
  deriveRequestTimeoutMs,
  pickServeTaskIndex,
} from '../utils/serveSchedule'
import { sha256Hex } from '../utils/sha256'
import { rememberHostToken } from '../utils/joinSession'
import type { MediaIndex } from '../types/media'
import type {
  Capacity,
  CreateRoomResponse,
  Envelope,
  MemberInfo,
  PeerControl,
  PlaybackState,
  Role,
  SignalPayload,
} from '../types/protocol'

export interface ChatMessage {
  key: number
  from: string
  displayName: string
  text: string
  ts: number
  mine: boolean
}

export interface JoinCredentials {
  roomId: string
  clientId: string
  displayName: string
  role: Role
  password: string
  /**
   * 主播复位令牌（S-7）：服务端只在创建响应里下发一次，宽限期内接回主播位必须带上。
   * 首次进房不需要；缺了它 → 服务端回 HOST_TOKEN_REQUIRED，主播就只能等到宽限期结束重建房间。
   */
  hostToken?: string
}

const ERROR_TEXT: Record<string, string> = {
  BAD_REQUEST: '请求不合法',
  ROOM_NOT_FOUND: '房间不存在',
  BAD_PASSWORD: '房间密码错误',
  ROOM_FULL: '房间已满（受主播上行限制）',
  NOT_HOST: '只有主播可以控制播放',
  NOT_JOINED: '尚未加入房间',
  ALREADY_JOINED: '该连接已在房间中',
  HOST_TAKEN: '房间已有主播',
  ROOM_NOT_READY: '主播尚未进房，请稍候',
  ROOM_CLOSED: '房间已关闭',
  CROSS_ROOM_SIGNAL: '目标不在同一房间',
  BAD_MEDIA_INDEX: '分片索引不合法',
  MEDIA_LOCKED: '分片索引已锁定，换片需重开房间',
  INTERNAL: '服务端内部错误',
  // 宽限期内接回主播位少了复位令牌：说清原因与后果，而不是含糊的"密码错误"。
  HOST_TOKEN_REQUIRED: '缺少主播复位令牌：无法接回主播位。请用创建该房间的那个标签页重连掉线的主播',
}

/** 观众预取窗口（分片数）。2s/片 ≈ 60s 缓冲，是抖动的缓冲池。 */
const PREFETCH_WINDOW = 30
/** 主播本地读取，窗口不需要那么大。 */
const HOST_WINDOW = 8
/**
 * 在途上限的**兜底值**（T2-2 改动前它是固定值）。
 *
 * 现在真正的上限由实测边速率推导（`inflightLimit`，见 utils/serveSchedule.deriveInflightLimit），
 * 这个 4 只在"速率或分片大小还没测出来"时生效 —— 起播阶段行为因此与改动前完全一致。
 */
const MAX_INFLIGHT = INFLIGHT_FALLBACK
/** 与 SPEC §7.3 一致的抖动缓冲目标。 */
const BUFFER_TARGET_SEC = 2
/**
 * 启动门控（SPEC §7.3）：从**主播时间戳所在分片**起，必须有 n 片连续且完整的缓冲才允许起播。
 *
 * n = 4（2s/片 ≈ 8s），与原来的"≥8s"同量级，但判据换成"连续分片"：
 * 只看秒数会被零散的小片段骗过，播放到中间缺口照样卡住。
 * 门控不设超时上限 —— 上游没数据就一直显示"加载中"，而不是拿薄缓冲硬播（那会带来同步振荡）。
 */
const STARTUP_GATE_SEGMENTS = 4
/** 跳转/换父后重建缓冲的门控阈值（比首次起播低，避免每次跳转都长时间等待）。 */
const RESEEK_GATE_SEGMENTS = 2
/**
 * 滞后过久阈值（秒）：落后主播这么多就认为"靠速率追不回来了"，
 * 提示用户并直接跳到主播当前进度（SPEC §7.5 卡顿策略的最后一步）。
 */
const LAG_JUMP_SEC = 12
/** 追回到这个范围内就撤掉滞后提示。 */
const LAG_CLEAR_SEC = 2
/**
 * 目标分片迟迟取不到多久（毫秒）之后，改按主播**当前**进度重新取片。
 * 场景：跳转目标或残留缺口全网都还没有，死等它只会一直停在"加载中"。
 */
const REQUIRED_STALE_MS = 2500
/** 开闸前至少要有的时钟偏移样本数（最小平滤波靠样本收敛）。 */
const MIN_CLOCK_SAMPLES = 6
/**
 * 开闸前偏移估计必须已稳定这么久。
 * 最小值滤波只在遇到更小样本时下降，停止下降即说明收敛 ——
 * 否则开闸第一帧会带着 -400ms 级的偏差（实测过），再靠速率修正慢慢拉回来。
 */
const CLOCK_SETTLE_SEC = 0.8

/**
 * 主播断线后服务端的宽限期（秒）。
 *
 * 服务端契约（已冻结，客户端不改它）：主播的 WS 断掉后，房间、成员表、MediaIndex、
 * 播放状态都保留这么久，期间只广播 member-left（离开者是主播），**不**广播 room-closed；
 * 主播在宽限期内重新 join(role=host) 即恢复。这里只用来做本地倒计时文案 ——
 * **权威始终是服务端的 room-closed**，收到它就以它给的原因收场。
 */
export const HOST_GRACE_SECONDS = 60
/** 观众等主播重建房间：每 2s 重试一次，最多 60s（房间没了就只能等主播重建）。 */
const RESUME_RETRY_INTERVAL_MS = 2000
const RESUME_RETRY_MAX_MS = HOST_GRACE_SECONDS * 1000
/**
 * 主播自动重建房间的退避序列（毫秒）。
 *
 * 收到 ROOM_NOT_FOUND 说明房间真的没了（宽限期过后，或服务端重启过）：
 * 用**同一个房间码** POST /api/rooms 重建，200 与 409（ErrRoomExists）都算"房间已就绪"。
 * 打服务端必须有限度：用完这几次仍失败就停下来给明确文案，不允许无限重试。
 */
const REBUILD_BACKOFF_MS = [0, 1000, 2000, 4000, 8000]
/** 同一个页面会话内最多连续换几次 clientId（CLIENT_ID_TAKEN 兜底，防止互相顶号打成死循环）。 */
const MAX_CLIENT_ID_ROTATIONS = 4
/** 服务端会先下发这个错误码再关闭连接：旧连接可能是半开，客户端不能干等它被收尸。 */
const CODE_CLIENT_ID_TAKEN = 'CLIENT_ID_TAKEN'
const CODE_ROOM_NOT_FOUND = 'ROOM_NOT_FOUND'
const CODE_ROOM_NOT_READY = 'ROOM_NOT_READY'
const CODE_HOST_TAKEN = 'HOST_TAKEN'
/**
 * HOST_TAKEN 的退避序列（毫秒）。
 *
 * 场景：上一连接是**半开**的 —— 服务端此刻仍认为它是成员且 HostID 非空，
 * 要等 ping/写超时（20s + 10s ≈ ≤30s）才会收尸并进入宽限期。
 * 所以"主播身份被占"是**预期内的中间态**，不是错误：必须退避重试到旧连接被回收。
 * 8s 封顶 × 覆盖 ~90s，足够跨过那 30s 的收尸窗口。
 */
const HOST_REJOIN_BACKOFF_MS = [1000, 2000, 4000, 8000]
const HOST_REJOIN_MAX_MS = 90_000

export function newClientId(): string {
  const rand = crypto.randomUUID?.() ?? Math.random().toString(36).slice(2)
  return `c_${rand.replace(/-/g, '').slice(0, 16)}`
}

function newClockEpoch(): string {
  return `e_${Math.random().toString(36).slice(2, 10)}`
}

export const useRoomStore = defineStore('room', () => {
  /**
   * 账号会话。**只**用于给"建房 / 重建房间"那一条 REST 请求带上 access token
   *（T8 起 `POST /api/rooms` 挂了 RequireAuth）；房间的实时链路（WS / WebRTC / 分片）
   * 与账号无关 —— 观众本来就不需要登录。
   */
  const auth = useAuthStore()

  // ---------- 房间状态 ----------
  const credentials = ref<JoinCredentials | null>(null)
  const joined = ref(false)
  const members = ref<MemberInfo[]>([])
  const chat = ref<ChatMessage[]>([])
  const capacity = ref<Capacity | null>(null)
  const hostId = ref('')
  const lastError = ref('')
  const roomClosed = ref('')
  const needsGesture = ref(false)

  /**
   * ICE 配置的 TTL 缓存与刷新（打洞优化 ①②）。
   *
   * 三件事必须一起成立，少一件都是"看起来优化了、实际没生效"：
   *   - 按服务端给的 TTL 周期重拉（列表每约 60s 会随探测结果变）；
   *   - 列表没变只续期，**不碰**正在跑的连接；
   *   - 列表变了先 setConfiguration，再**只让发起方**（下游子节点）ICE restart。
   * 父节点侧的"用最新列表"由 applyIceConfiguration（推到所有 PC）+ 应答前的
   * setConfiguration 保证，它绝不主动 restart。
   */
  const iceConfig = useIceConfig({
    fetchPayload: fetchIcePayload,
    onChanged: (servers, info) => {
      const applied = rtc.applyIceConfiguration(servers)
      noteLifecycle(
        `ICE 列表变化（第 ${iceConfig.changeCount.value} 次）：已更新 ${applied} 个 PeerConnection` +
          (info.urlsChanged ? '，随后由发起方重启 ICE' : '（仅凭证变化，不重启 ICE）'),
      )
      // 拿到列表就顺手采一次本机候选：真实连接可能靠 mDNS host 候选秒连、
      // 导致 Chrome 提前结束收集，"本机有没有可跨网的全局 IPv6"就再也看不出来。
      void rtc.sampleLocalCandidates(servers)
      if (info.urlsChanged) {
        safe('ICE restart', restartIceForNewList())
      }
    },
    log: (text) => noteLifecycle(text),
  })
  const iceServers = iceConfig.servers

  // ---------- 断线恢复（缺陷 1：主播断线不再等于房间永久销毁）----------
  /** 主播离线等待态（观众侧）：已进房、且成员表里没有 role==='host' 的成员。 */
  const hostOffline = ref(false)
  /** 本地倒计时（秒），以宽限期为基准；到 0 之后仍等服务端的权威 room-closed。 */
  const hostOfflineSecondsLeft = ref(0)
  /** 观众侧：等主播重建房间的有界重试提示（ROOM_NOT_FOUND 后出现）。 */
  const resumeNotice = ref('')
  /** 不可自动恢复时的明确文案（媒体已不在内存 / 重建失败 / 等待主播超时）。 */
  const roomUnrecoverable = ref('')
  /** 主播自动重建房间的进度。 */
  const rebuildState = ref<'idle' | 'rebuilding' | 'failed'>('idle')
  /** 本页面会话已经换过几次 clientId（给排障与验收观察）。 */
  const clientIdRotations = ref(0)

  let hostOfflineTimer: number | undefined
  let hostOfflineUntil = 0
  let resumeWaitTimer: number | undefined
  let resumeWaitDeadline = 0
  let rebuildInFlight = false
  let rebuildAttempt = 0
  let hostRejoinTimer: number | undefined
  let hostRejoinStartedAt = 0
  let hostRejoinAttempt = 0
  /** 连续换号次数（进房成功后清零）：用于给"换号"设置上限，避免互相顶号打成死循环。 */
  let clientIdRotationStreak = 0
  /** 本页面会话已经成功进过房（用来区分"首次进房"与"恢复进房"）。 */
  let hostJoinedOnce = false

  let chatKey = 0
  /** 房间生命周期日志（调试与验收用）。 */
  const storeLifecycle = ref<string[]>([])

  function noteLifecycle(event: string) {
    storeLifecycle.value = [...storeLifecycle.value.slice(-19), `${Math.round(performance.now())}:${event}`]
  }

  // ---------- 媒体与播放 ----------
  const media = useMediaIndex()
  const chunkStore = useChunkStore()
  const player = useChunkPlayer()
  const clock = useSyncClock()

  const mediaIndex = ref<MediaIndex | null>(null)
  const mediaError = ref('')
  const syncMode = ref('idle')
  const syncResets = ref(0)
  const chunkErrors = ref(0)

  // ---------- 入站分片的内容校验（F-11）----------
  //
  // 索引里的 `sha256` 是主播发布时签出去的"内容指纹"，但客户端此前**从不校验**：
  // 同一个房间里的对端可以把任意字节塞进 media 帧，坏数据会一路进 chunkStore 并
  // append 进 MSE（画面花屏、被替换成任意内容，且无人察觉）。
  /** 校验不通过（内容被替换）的分片数：这是"有对端在发坏数据"的唯一直接证据。 */
  const hashMismatches = ref(0)
  /** 校验通过的分片数。 */
  const hashVerified = ref(0)
  /** 环境不支持 `crypto.subtle`（明文 http + 非 localhost）导致跳过校验的分片数。 */
  const hashSkipped = ref(0)

  /**
   * 媒体代数：每次"缓冲/房间被复位"就 +1。
   *
   * 校验是异步的（`crypto.subtle.digest` 返回 Promise），复位之后才回来的校验结果
   * 必须作废 —— 否则跳转/换房/重新挂载播放器之后，旧会话的分片会被写进新缓冲。
   */
  let mediaEpoch = 0

  /** 复位分片仓库（并作废所有在途校验）。**所有** chunkStore.reset() 都必须走这里。 */
  function resetChunkStore() {
    mediaEpoch += 1
    chunkStore.reset()
  }

  /** 索引里声明的分片摘要（小写十六进制）；索引没给或格式不对时返回空串（= 跳过校验）。 */
  function expectedSha256(segmentIndex: number): string {
    const meta = mediaIndex.value?.segments[segmentIndex - 1]
    const value = typeof meta?.sha256 === 'string' ? meta.sha256.trim().toLowerCase() : ''
    if (value === '' || !/^[0-9a-f]{64}$/.test(value)) {
      return ''
    }
    return value
  }
  /** 取数失败与应答情况的环形日志：排障时先看这两个（以前失败是静默的）。 */
  const fetchFailures = ref<string[]>([])
  const serveLog = ref<string[]>([])
  const bufferedAhead = ref(0)
  const videoEl = shallowRef<HTMLVideoElement | null>(null)

  // ---------- 播放健康度（T4）----------
  /** 按时到达的分片数（进入可播放缓冲时播放头还没走到它的起点）。 */
  const onTimeChunks = ref(0)
  /** 迟到分片数（进入缓冲时播放头已经到它了）。 */
  const lateChunks = ref(0)
  /** 因**超时**换父的次数（T3）：卡顿优化的直接观测量。 */
  const timeoutFailovers = ref(0)

  /**
   * 按时交付率统计。
   *
   * 判据 = "这一片进入可播放缓冲时，播放头还没走到它的起点"（T4 的定义）：
   *   · 参照点取**权威锚点**（clock.expectedAt()）而不是 video.currentTime ——
   *     门控期间播放头还钉在 0，用本机位置会把"按设计先攒后播"的那一批冤枉成迟到；
   *   · 跳转重建缓冲期间不统计：seekPipeline 是"先 append 目标分片、再 seekTo"，
   *     这期间 currentTime 还停在旧位置，用它判会凭空多出一堆假迟到。
   *
   * 这不是"卡顿次数"本身 —— 卡顿用的是 `<video>` 的 waiting 事件（player.stalls）：
   * 播放头到了却一点可播数据都没有时浏览器才会发它，而"迟到的分片"里有一部分
   * 仍然赶在播放头前面进了缓冲（只是裕度很小）。
   */
  const SEEK_TIMING_MUTE_MS = 3000

  function noteDeliveryTiming(segment: number) {
    const index = mediaIndex.value
    if (!index || isHost.value) return
    if (performance.now() - lastHardSeekAt < SEEK_TIMING_MUTE_MS) return
    const meta = index.segments[segment - 1]
    if (!meta) return
    const refTime = gated.value ? (clock.expectedAt() ?? 0) : (player.video.value?.currentTime ?? 0)
    if (refTime < meta.startPts) {
      onTimeChunks.value += 1
    } else {
      lateChunks.value += 1
    }
  }

  /** 启动门控状态（观众侧）：true = 正在加载，不播放、不响应播放控制。 */
  const gated = ref(false)
  const gateReason = ref('')
  const gateBufferedSec = ref(0)
  /** 门控要求：从主播时间戳所在分片起，需要连续多少片完整缓冲。 */
  const gateThresholdSegments = ref(STARTUP_GATE_SEGMENTS)
  /** 当前从锚点分片起连续完整的缓冲分片数（门控判据，供 UI 与验收观察）。 */
  const gateBufferedSegments = ref(0)
  const gateWaitedSec = ref(0)
  let gateStartedAt = 0

  /** 落后主播的秒数（正数 = 落后）与"滞后跳转"的提示文案。 */
  const lagSec = ref(0)
  const lagNotice = ref('')
  let lastLagJumpAt = 0
  /** 最近一次下发的控制状态：用于抑制"按钮 + 播放器事件"重复广播。 */
  let lastControl: { paused: boolean | null; rate: number; at: number } = { paused: null, rate: 1, at: 0 }

  /**
   * 逐跳中继的取证计数（调试与验收用）。
   *
   * forwarded：我作为中继转发出去的进度条数；
   * relayed：我收到的进度里带中继戳（parentClockMs > 0）的条数；
   * direct：不带中继戳的条数（主播直发或服务端转发）；
   * nonPrimary：来自非主父而被丢弃的条数。
   * 这几个数能一眼区分"中继没转发"和"转发了但字段没生效"。
   */
  const relayStats = { forwarded: 0, relayed: 0, direct: 0, nonPrimary: 0 }

  /** 计时纪元：主播页面每次加载生成一个，随进度下发（C13）。 */
  const clockEpoch = ref(newClockEpoch())

  let progressSeq = 0
  let nextAppend = 1
  let initRequested = false
  /** 跳转后必须先取到的分片序号；取到之前调度器会一直优先补取它。 */
  let requiredSegment: number | null = null
  /** requiredSegment 是什么时候设成当前值的：超时未满足就改按主播当前进度取片。 */
  let requiredSegmentSince = 0
  /** 主播设定的播放速率（同步环的微调是它之上的临时缩放）。 */
  let authoritativeRate = 1
  /** 正在进行的播放器挂载，用于挡住并发 attach。 */
  let playerAttachInFlight: Promise<void> | null = null
  let lastHardSeekAt = 0
  let lastHardSeekTarget = -1
  let lastConnectAttemptAt = 0
  /** 跳转后的稳定期截止时间（性能时钟毫秒）。 */
  let correctionSettleUntil = 0

  let schedulerTimer: number | undefined
  let syncTimer: number | undefined
  let progressTimer: number | undefined
  let metricsTimer: number | undefined
  let haveTimer: number | undefined

  // ---------- 信令 ----------
  const signaling = useSignaling({
    onMessage: handleMessage,
    onOpen: () => {
      joined.value = false
      sendJoin()
    },
    onClose: (reason) => {
      joined.value = false
      if (!roomClosed.value) lastError.value = reason
      // 观众：自己的信令一断，本机时钟就失去了外部锚点，再"按外推时间播"就是假播放
      //（画面会继续走几秒，然后被一次大跳转拽回来）。立刻停住并进门控，
      // 等重新进房、主播进度恢复后再由 evaluateGate 自动开闸。
      if (!isHost.value && mediaIndex.value) {
        freezePlayback('与服务端的信令断开，等待重连')
      }
    },
  })

  // ---------- WebRTC ----------
  const rtc = useWebRTC({
    iceServers: () => iceServers.value,
    sendSignal: (to, payload) => signaling.send({ type: T.Signal, to, payload }),
    onControl: handlePeerControl,
    onMedia: handlePeerMedia,
    onOpen: (peerId) => {
      // 主播给新连上的观众补齐当前播放状态与时钟锚点。
      if (isHost.value) {
        sendProgressBurst(peerId)
        clockStoreSendTimeSync(peerId)
        broadcastHaveState()
        return
      }
      // 观众连上主播后立刻开始拉分片。
      broadcastHaveState()
      void pumpPrefetch()
    },
  })

  // ---------- 派生状态 ----------
  const connection = signaling.status
  const roomId = computed(() => credentials.value?.roomId ?? '')
  const clientId = computed(() => credentials.value?.clientId ?? '')
  const displayName = computed(() => credentials.value?.displayName ?? '')
  const role = computed<Role>(() => credentials.value?.role ?? 'viewer')
  const isHost = computed(() => role.value === 'host')
  const self = computed(() => members.value.find((m) => m.id === clientId.value) ?? null)
  const depth = computed(() => self.value?.depth ?? 0)
  const primaryId = computed(() => self.value?.primaryId ?? '')
  const parentName = computed(() => members.value.find((m) => m.id === primaryId.value)?.displayName ?? '')
  const playback = clock.playback
  const drift = clock.drift
  const peerCount = computed(() => rtc.peers.value.size)
  const videoReady = computed(() => player.ready.value)
  const uploadCapacityBps = rtc.uploadCapacityBps

  // ---------- 拓扑与多父调度（SPEC §6.1–§6.4）----------
  const lastDistributorChange = ref('')

  const topology = useTopology({
    selfId: () => clientId.value,
    isHost: () => isHost.value,
    connectToPeer: connectTo,
    sendToPeer: (peerId, msg) => rtc.send(peerId, encodeControl(msg)),
    connectedPeers: () => rtc.openChannels(),
    segmentCount: () => mediaIndex.value?.segments.length ?? 0,
    // 主播是 Seeder：它能按需从本地文件读出任意分片，位图必须如实广告"全都有"。
    // 只广告自己预取窗口的话，启用 owner-first 取数后所有人都不会再向它要窗口外的分片。
    ownedSegments: () => {
      if (isHost.value) {
        const total = mediaIndex.value?.segments.length ?? 0
        const all = new Array<number>(total + 1)
        for (let i = 0; i <= total; i += 1) all[i] = i
        return all
      }
      return [0, ...chunkStore.indices()]
    },
    pendingFor: (peerId) => requester.pendingFor(peerId),
    onPrimaryChanged: () => {
      resetFetchState()
      // 换父之后本跳时钟样本作废：它们是相对旧父节点时钟测的（见 useSyncClock.resetHop）。
      clock.resetHop()
      broadcastHaveState()
      // 卡顿/换路之后（SPEC §7.5）：新父节点上的缓冲可能已经断档，
      // 因此只要当前处于不健康状态，就按"连续 n 片"重新开闸，而不是拿残缓冲继续播。
      // 主动重平衡（缓冲健康）时不重新开闸 —— 那会让用户白白多看一次加载。
      if (!isHost.value && (gated.value || bufferedAhead.value < BUFFER_TARGET_SEC)) {
        noteLifecycle(
          `换路后重新开闸 primary=${topology.primaryId.value} buffer=${bufferedAhead.value.toFixed(2)}`,
        )
        enterGate('父节点变更，重新建立缓冲', RESEEK_GATE_SEGMENTS)
      }
      void pumpPrefetch()
    },
  })

  // ---------- 信令消息 ----------
  function handleMessage(env: Envelope) {
    switch (env.type) {
      case T.Joined:
        joined.value = true
        roomClosed.value = ''
        lastError.value = ''
        // 进房成功即撤销所有"恢复中"的状态：换 id、重建房间都算过关。
        // 注意 clientIdRotations 是**累计计数**（给排障/验收看"到底换过号没有"），
        // 只在这里清零 streak（连续换号上限用），累计值留到离开房间/重新进房。
        clientIdRotationStreak = 0
        rebuildAttempt = 0
        roomUnrecoverable.value = ''
        if (rebuildState.value === 'rebuilding' || rebuildState.value === 'failed') rebuildState.value = 'idle'
        stopResumeWait()
        cancelHostRejoin()
        // 重新进房后循环可能已经被停掉（换 id 会拆掉整条链路），这里补上。
        if (player.attached.value) startLoops()
        hostId.value = env.hostId ?? ''
        members.value = env.members ?? []
        capacity.value = env.capacity ?? null
        clearHostOfflineIfPresent()
        if (env.playback) {
          clock.onAnchor(toSample(env.playback))
        }
        // 主播重新进房（宽限期内回到同一房间 / 重建之后）：分片索引必须重新发布，
        // 否则服务端手里没有索引，后来的观众会一直"等待主播开播"。
        // 重复发布同一份是幂等的（服务端 SameIndex 才允许；内容不同会被 MEDIA_LOCKED 拒）。
        if (isHost.value) {
          // C13：**恢复进房**时换一个时钟纪元。服务端只原样透传 ClockEpoch，
          // 复用旧纪元会让观众端保留旧的偏移滤波结果 → 恢复瞬间全房跳位。
          // 首次进房不需要（还没有任何观众拿到过这个纪元）。
          if (hostJoinedOnce) {
            clockEpoch.value = newClockEpoch()
            noteLifecycle(`主播重新进房，重置时钟纪元 ${clockEpoch.value}`)
          }
          hostJoinedOnce = true
          if (mediaIndex.value) {
            signaling.send({ type: T.MediaIndex, mediaIndex: mediaIndex.value })
          }
        } else if (env.mediaIndex) {
          safe('应用媒体索引', applyRemoteMediaIndex(env.mediaIndex))
        } else if (hostId.value) {
          safe('连接主播', connectToHost())
        }
        if (env.topology) {
          safe('应用拓扑', topology.apply(env.topology))
        }
        break

      case T.MemberJoined:
      case T.MemberLeft:
      case T.MemberList:
        if (env.members) members.value = env.members
        // 契约 2：joined 且成员表里没有 host ⇒ 主播离线（掉线等待态）。
        // 主播回来（member-joined 带 host）时自动消失。
        if (env.type === T.MemberJoined && containsHost(env.members)) {
          clearHostOffline()
        } else {
          noteHostOfflineIfMissing()
        }
        break

      case T.MediaIndex:
        if (env.mediaIndex) safe('应用媒体索引', applyRemoteMediaIndex(env.mediaIndex))
        break

      case T.Signal:
        // SDP / ICE 由服务端定向转发，这里交给 WebRTC 层处理。
        if (env.from && env.payload) {
          void rtc.handleSignal(env.from, env.payload as SignalPayload)
        }
        break

      case T.Capacity:
        if (env.capacity) capacity.value = env.capacity
        break

      case T.ParentAssignment:
      case T.Topology:
        if (env.topology) safe('应用拓扑', topology.apply(env.topology))
        break

      case T.DistributorChange:
        topology.noteDistributorChange()
        if (env.distributor) {
          lastDistributorChange.value = `${env.distributor.fromId || '无'} → ${env.distributor.toId || '无'}`
        }
        break

      case T.Chat:
        chat.value.push({
          key: ++chatKey,
          from: env.from ?? '',
          displayName: env.displayName ?? '匿名',
          text: env.text ?? '',
          ts: env.ts ?? Date.now(),
          mine: env.from === clientId.value,
        })
        break

      case T.RoomControl:
        // 房主的离散指令：立即应用，并作为一次时钟锚点（不参与 progress 的 seq 过滤）。
        if (env.playback) {
          clock.onAnchor(toSample(env.playback))
          safe('应用房主控制', applyPlayback(env.playback, env.action))
        }
        break

      case T.RoomClosed:
        // 服务端是唯一权威：收到它就以它给的原因收场，本地的倒计时/重试全部作废。
        joined.value = false
        roomClosed.value = env.message ?? '房间已关闭'
        clearHostOffline()
        stopResumeWait()
        rebuildState.value = 'idle'
        freezePlayback('房间已关闭')
        stopLoops()
        break

      case T.Error:
        handleErrorEnvelope(env)
        break

      default:
        break
    }
  }

  /**
   * 错误信封的分诊。
   *
   * 以前所有错误都只是往 lastError 里塞一行文案，于是 ROOM_NOT_FOUND 表现为
   * "房间不存在"然后永远停在那里；CLIENT_ID_TAKEN / HOST_TAKEN / ROOM_NOT_READY
   * 这三个"恢复路径上的中间态"完全没有处理。
   */
  function handleErrorEnvelope(env: Envelope) {
    const code = env.code ?? ''
    if (code === CODE_CLIENT_ID_TAKEN) {
      rotateClientId()
      return
    }
    if (code === CODE_ROOM_NOT_FOUND) {
      handleRoomNotFound()
      return
    }
    if (code === CODE_HOST_TAKEN) {
      // 预期内的中间态：旧连接可能还是半开，服务端仍认为它是主播（HostID 非空），
      // 要等 ping/写超时收尸（≤30s）才会空出主播位。这里退避重试，绝不报错给用户。
      if (isHost.value) {
        scheduleHostRejoin()
        return
      }
    }
    if (code === CODE_ROOM_NOT_READY) {
      // 观众在同一房间里等主播进房：同样是有界重试的中间态，不弹错误。
      if (!isHost.value) {
        startResumeWait('not-ready')
        return
      }
    }
    lastError.value = ERROR_TEXT[code] ?? env.message ?? '未知错误'
  }

  function toSample(state: PlaybackState) {
    return {
      currentTime: state.currentTime,
      hostClockMs: state.hostClockMs,
      clockEpoch: state.clockEpoch,
      paused: state.paused,
      rate: state.rate,
      seq: state.seq,
    }
  }

  function sendJoin() {
    const creds = credentials.value
    if (!creds) return
    signaling.send({
      type: T.Join,
      roomId: creds.roomId,
      clientId: creds.clientId,
      displayName: creds.displayName,
      role: creds.role,
      password: creds.password,
      // 主播复位令牌（S-7）：服务端只在"房间处于主播离线宽限期"时才看它。
      // 带上它，主播断线重连才能在同一房间码下直接接回主播位（否则只能等房间被回收后重建）。
      hostToken: creds.hostToken,
    })
  }

  // ---------- 断线恢复（缺陷 1）----------
  /**
   * 信令 WS 的地址。
   *
   * clientId 是 URL 上的查询参数（服务端用连接上的 clientId 认身份，**不看** join 报文里的），
   * 所以换 clientId 必须重建 URL 重连，只改 join 信封是不够的。
   */
  function signalUrl(creds: JoinCredentials): string {
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    return (
      `${proto}://${window.location.host}/ws?roomId=${encodeURIComponent(creds.roomId)}` +
      `&clientId=${encodeURIComponent(creds.clientId)}`
    )
  }

  /** 立刻重新进房：能发就发 join；发不出去就**不等退避**直接重连（用于错误恢复路径）。 */
  function rejoinNow() {
    const creds = credentials.value
    if (!creds) return
    if (signaling.status.value === 'open') {
      sendJoin()
      return
    }
    // 正在连（上一次 connect 还没出结果）时不要另开一条：onopen 里本来就会 sendJoin。
    if (signaling.status.value === 'connecting') return
    // 退避最长 8s：恢复路径上（重建房间 / 换 id）干等 8s 是纯粹的浪费。
    signaling.connect(signalUrl(creds))
  }

  function containsHost(list?: MemberInfo[]): boolean {
    return (list ?? []).some((m) => m.role === 'host')
  }

  /** 观众侧：成员表里没有 host ⇒ 主播离线（契约 2），进入等待态并停住画面。 */
  function noteHostOfflineIfMissing() {
    if (isHost.value || roomClosed.value || !joined.value) return
    if (members.value.length === 0) return
    if (containsHost(members.value)) {
      clearHostOffline()
      return
    }
    noteHostOffline()
  }

  /** T.Joined 快照里带回 host 就说明主播已经回来了。 */
  function clearHostOfflineIfPresent() {
    if (containsHost(members.value)) clearHostOffline()
  }

  function noteHostOffline() {
    if (hostOffline.value) return
    hostOffline.value = true
    hostOfflineUntil = performance.now() + HOST_GRACE_SECONDS * 1000
    hostOfflineSecondsLeft.value = HOST_GRACE_SECONDS
    if (hostOfflineTimer !== undefined) window.clearInterval(hostOfflineTimer)
    hostOfflineTimer = window.setInterval(() => {
      hostOfflineSecondsLeft.value = Math.max(
        0,
        Math.ceil((hostOfflineUntil - performance.now()) / 1000),
      )
    }, 1000)
    noteLifecycle('检测到主播离线（成员表里没有 host），进入等待态')
    // 主播不在，进度就不再来：继续播只是拿外推时间"假播放"，停住并进门控，
    // 等主播回来 + 新锚点到达后由 evaluateGate 自动开闸。
    freezePlayback('主播离线，等待重连')
  }

  function clearHostOffline() {
    if (!hostOffline.value) return
    hostOffline.value = false
    hostOfflineSecondsLeft.value = 0
    if (hostOfflineTimer !== undefined) {
      window.clearInterval(hostOfflineTimer)
      hostOfflineTimer = undefined
    }
    noteLifecycle('主播已回到房间，退出等待态')
  }

  /** 停住画面但保留缓冲：门控 + pause。房间关闭、主播离线、信令断开都走它。 */
  function freezePlayback(reason: string) {
    player.video.value?.pause()
    enterGate(reason)
  }

  /**
   * 是否处于"不该继续播"的等待态。
   *
   * 断线等待期间**必须压住门控**：主播的进度不再来，但本地缓冲与时钟样本还在，
   * evaluateGate 会因为"连续 n 片 + 时钟已收敛"直接把闸门打开 —— 那就是拿外推时钟假播放
   *（实测只掐线 0.75s，画面自己就走了 0.77s，等于把等待说成了正常播放）。
   * 恢复进房 / 主播回来后这些条件自然消失，门控随即按正常判据开闸。
   */
  function holdPlayback(): boolean {
    return (
      hostOffline.value ||
      resumeNotice.value !== '' ||
      roomClosed.value !== '' ||
      signaling.status.value !== 'open'
    )
  }

  /** 观众倒计时文案（UI 与验收脚本共用同一份措辞，避免两边各写一套词）。 */
  const hostOfflineText = computed(() => {
    if (!hostOffline.value) return ''
    if (hostOfflineSecondsLeft.value > 0) {
      return `主播掉线，等待重连（剩余约 ${hostOfflineSecondsLeft.value} 秒）`
    }
    return '主播掉线，等待服务端确认（本地宽限期已到，以服务端关闭通知为准）'
  })

  /**
   * 收到 ROOM_NOT_FOUND（房间真的没了）。
   *
   * 主播：媒体还在内存里就自动重建（同一个房间码）→ 重新 join → 重新发布分片索引；
   * 观众：不重建房间，改为有界重试等主播把房间建回来。
   *
   * 已经收到过 room-closed 也照样走这条路：那是服务端给的**权威原因**（保留在 roomClosed 里
   * 继续展示），但宽限期过后主播仍可用同一个房间码重建（服务端契约 4），
   * 所以观众继续有界重试、主播继续重建，成功进房后 roomClosed 会被清掉。
   */
  function handleRoomNotFound() {
    if (isHost.value) {
      // 刷新过页面 / 已经换过片子：本地没有分片句柄，重建出来的房间也喂不了数据。
      // 这时必须说清楚要做什么，而不是停在"房间不存在"上让用户干等。
      if (!mediaIndex.value || media.segmentCount() === 0) {
        roomUnrecoverable.value = '房间已失效：请重新创建房间并重新选择分片目录'
        noteLifecycle('收到 ROOM_NOT_FOUND，但本地已无分片数据，无法自动重建')
        return
      }
      // 重连时服务端可能连着回几条 ROOM_NOT_FOUND（同一次重连的多条尝试/残留队列），
      // 重建只做一次：这里挡掉重复进入，日志也只记一条。
      if (rebuildInFlight) return
      noteLifecycle('收到 ROOM_NOT_FOUND，开始自动重建房间')
      void rebuildRoom()
      return
    }
    startResumeWait()
  }

  /**
   * 主播重建房间：用同一个房间码 POST /api/rooms。
   *
   * 200（新建成功）与 409（ErrRoomExists：房间其实还在）都视为"房间已就绪"，
   * 随后立刻重新 join 并由 T.Joined 分支重新发布分片索引。
   * 失败按 REBUILD_BACKOFF_MS 退避重试，用完次数就停下来给明确文案（绝不无限打服务端）。
   */
  async function rebuildRoom(): Promise<void> {
    if (rebuildInFlight) return
    rebuildInFlight = true
    rebuildState.value = 'rebuilding'
    try {
      while (rebuildAttempt < REBUILD_BACKOFF_MS.length) {
        const delay = REBUILD_BACKOFF_MS[rebuildAttempt]
        rebuildAttempt += 1
        if (delay > 0) await new Promise((resolve) => window.setTimeout(resolve, delay))

        const creds = credentials.value
        if (!creds) return
        if (!isHost.value) return

        try {
          // 建房要登录：带 access token，401 时由 authedFetch 自动刷新一次再重试。
          // `redirectOnAuthFailure: false` 是刻意的 —— 把主播从房间里拽到登录页等于
          // **顺手掐掉整个房间**（他自己是唯一的源），比"这次重建失败"严重得多；
          // 失败就走下面的重试与"房间重建失败"文案。
          const resp = await auth.authedFetch(
            '/api/rooms',
            {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({
                roomId: creds.roomId,
                password: creds.password,
                // 码率估计交给已知的分片索引：省略也行，服务端会退回默认值。
                streamBps: mediaIndex.value?.bitrateBps ?? 0,
              }),
            },
            { redirectOnAuthFailure: false },
          )
          if (resp.ok || resp.status === 409) {
            // 409 = 房间已存在（ErrRoomExists），与 200 等价地视为"房间已就绪"。
            noteLifecycle(`房间 ${creds.roomId} 已就绪（HTTP ${resp.status}），重新进房`)
            rebuildState.value = 'idle'
            const data = (await resp.json().catch(() => null)) as CreateRoomResponse | null
            // 新房间的响应里有 ICE 配置（409 时没有）：拿到就更新，拿不到沿用旧的。
            if (data?.iceServers?.length) applyIceResponse(data, 'POST /api/rooms（重建）')
            // 重建 = 服务端**新建**了房间对象 → 会下发一枚新的复位令牌，旧令牌对新房间无效。
            // 只在响应真的带令牌时覆盖（409 时没有），否则会把还能用的旧令牌冲掉。
            if (data?.hostToken) {
              credentials.value = { ...creds, hostToken: data.hostToken }
              rememberHostToken(creds.roomId, data.hostToken)
              noteLifecycle('已保存重建房间下发的主播复位令牌')
            }
            rejoinNow()
            return
          }
          noteLifecycle(`重建房间失败：HTTP ${resp.status}`)
        } catch (err) {
          noteLifecycle(`重建房间失败：${(err as Error).message}`)
        }
      }
      rebuildState.value = 'failed'
      roomUnrecoverable.value = '房间重建失败：无法在同一个房间码下恢复，请重新创建房间'
      noteLifecycle('重建房间重试次数用尽')
    } finally {
      rebuildInFlight = false
    }
  }

  /** 观众：有界重试等主播重建房间 / 等主播进房（每 2s 一次，最多 60s）。 */
  function startResumeWait(kind: 'not-found' | 'not-ready' = 'not-found') {
    if (isHost.value) return
    roomUnrecoverable.value = ''
    if (kind === 'not-ready') {
      // 房间还在、只是没有主播：不喊"房间不存在"，也不催用户做任何事。
      resumeNotice.value = '等待主播进房…'
    } else {
      // 已经收到过权威的 room-closed 时把原因一并说清楚，不要看起来像"什么都没发生"。
      resumeNotice.value = roomClosed.value
        ? '房间已被服务端关闭，等待主播重建房间…'
        : '等待主播重建房间…'
    }
    // 主播不在，先停住画面（避免拿外推时间假播放）。
    freezePlayback(kind === 'not-ready' ? '等待主播进房' : '等待主播重建房间')
    // 已经在等待里：只刷新文案，不重复开定时器、也不重复记日志（重试是每 2s 一次的）。
    if (resumeWaitTimer !== undefined) return
    noteLifecycle(
      kind === 'not-ready'
        ? '收到 ROOM_NOT_READY：等待主播进房（每 2s 重试，最多 60s）'
        : '收到 ROOM_NOT_FOUND：等待主播重建（每 2s 重试，最多 60s）',
    )
    resumeWaitDeadline = performance.now() + RESUME_RETRY_MAX_MS
    resumeWaitTimer = window.setInterval(() => {
      if (performance.now() >= resumeWaitDeadline) {
        stopResumeWait()
        roomUnrecoverable.value =
          kind === 'not-ready'
            ? '房间已失效：主播长时间未进房，请返回首页重新加入'
            : '房间已失效：等待主播重建超时，请返回首页重新加入'
        noteLifecycle('等待主播超时（60s）')
        return
      }
      rejoinNow()
    }, RESUME_RETRY_INTERVAL_MS)
    rejoinNow()
  }

  function stopResumeWait() {
    resumeNotice.value = ''
    if (resumeWaitTimer === undefined) return
    window.clearInterval(resumeWaitTimer)
    resumeWaitTimer = undefined
  }

  /**
   * HOST_TAKEN → 退避重试主播 join。
   *
   * 旧连接是半开时，服务端仍把它当成员（HostID 非空），要等 ping/写超时才会收尸；
   * 这段时间里 join(role=host) 必然被拒。这是预期内的中间态：
   * 退避重试到旧连接被回收（1s→2s→4s→8s…，8s 封顶，总时长覆盖 ~90s），
   * 期间只显示一句进度，不把错误抛给用户。旧连接被收尸后服务端会进入宽限期
   *（HostID 清空），下一次重试就能进房。
   */
  function scheduleHostRejoin() {
    if (!isHost.value || roomClosed.value) return
    if (hostRejoinStartedAt === 0) hostRejoinStartedAt = performance.now()
    if (performance.now() - hostRejoinStartedAt > HOST_REJOIN_MAX_MS) {
      roomUnrecoverable.value = '无法回到房间：旧连接仍占用主播身份，请刷新页面重试'
      noteLifecycle('HOST_TAKEN 重试超时（90s）')
      return
    }
    const delay = HOST_REJOIN_BACKOFF_MS[Math.min(hostRejoinAttempt, HOST_REJOIN_BACKOFF_MS.length - 1)]
    hostRejoinAttempt += 1
    resumeNotice.value = '上一连接尚未被服务端回收，正在重试进入房间…'
    noteLifecycle(`HOST_TAKEN：第 ${hostRejoinAttempt} 次退避重试（${delay}ms）`)
    if (hostRejoinTimer !== undefined) window.clearTimeout(hostRejoinTimer)
    hostRejoinTimer = window.setTimeout(() => {
      hostRejoinTimer = undefined
      rejoinNow()
    }, delay)
  }

  function cancelHostRejoin() {
    if (hostRejoinTimer !== undefined) {
      window.clearTimeout(hostRejoinTimer)
      hostRejoinTimer = undefined
    }
    hostRejoinAttempt = 0
    hostRejoinStartedAt = 0
  }

  /**
   * CLIENT_ID_TAKEN：服务端会先下发这个错误码再关闭连接。
   *
   * 旧连接可能是半开（要等约 30s 才被服务端收尸），所以不能干等它断开：
   * 立刻换一个新 clientId（它同时就是成员身份）→ 拆掉旧 peer → 用新 URL 立刻重连，
   * 不走 8s 的退避。换 id 等于重新进房，成员表/拓扑/请求队列都必须按 leaveRoom 的语义清掉。
   */
  function rotateClientId() {
    if (roomClosed.value) return
    if (clientIdRotationStreak >= MAX_CLIENT_ID_ROTATIONS) {
      roomUnrecoverable.value = '无法进入房间：本机标识被反复占用，请刷新页面重试'
      noteLifecycle('clientId 连续被占用，放弃自动换号')
      return
    }
    const creds = credentials.value
    if (!creds) return

    clientIdRotationStreak += 1
    clientIdRotations.value += 1
    const next = newClientId()
    noteLifecycle(`clientId ${creds.clientId} 被占用，换新身份 ${next}`)
    credentials.value = { ...creds, clientId: next }

    // 旧身份的一切都作废：成员表、播放器连接、在途请求、拓扑分配。
    joined.value = false
    hostId.value = ''
    members.value = []
    requester.reset()
    requester.resetEdges()
    rtc.closeAll()
    topology.reset()
    if (!isHost.value && mediaIndex.value) {
      // 观众：换 id 后要从头建立 P2P 与时钟，先停住画面免得拿旧锚点假播放。
      freezePlayback('身份变更，等待重新进房')
    }
    signaling.close()
    // 上一次 clientId 触发的重连退避不能留着：它还会去开一条旧 id 的连接。
    signaling.connect(signalUrl(credentials.value))
  }

  // ---------- Peer 消息 ----------
  function handlePeerControl(peerId: string, msg: PeerControl) {
    if (msg.t === 'req') {
      // 只入队：读盘不在这里做（见 enqueueServe 的注释）。
      enqueueServe(peerId, msg)
      return
    }
    if (msg.t === 'err') {
      requester.handleControl(peerId, msg)
      return
    }
    if (msg.t === 'have') {
      topology.notePeerHave(peerId, msg.chunks ?? new Uint8Array(0))
      return
    }
    if (msg.t === 'progress' || msg.t === 'time-sync') {
      if (isHost.value) return
      // 只有主父的进度是权威的：备用父、以及**换防前遗留的直连**都会送来同一份进度，
      // 混着用会让偏移估计在两套基准之间来回跳。
      // 实测：链式拓扑收敛后，主播与深度 2 节点之间的旧直连仍在，节点收到的是
      // "主播直发（parentOffsetMs=0，偏移按整条路径算）"与"中继转发（按一跳算）"
      // 两套样本，最终偏移退回路径口径 —— 这正是 P1 要修掉的偏差。
      // primaryId 还没下发时先接受（入房引导期），否则时钟永远收敛不了。
      const primary = topology.primaryId.value
      const authoritative = primary === '' || peerId === primary
      let accepted = false
      if (authoritative && typeof msg.currentTime === 'number' && typeof msg.hostClockMs === 'number') {
        accepted = clock.onProgress({
          currentTime: msg.currentTime,
          hostClockMs: msg.hostClockMs,
          parentClockMs: msg.parentClockMs,
          parentOffsetMs: msg.parentOffsetMs,
          clockEpoch: msg.clockEpoch,
          paused: Boolean(msg.paused),
          rate: msg.rate ?? 1,
          seq: msg.seq ?? 0,
        })
        if (accepted) {
          if (msg.parentClockMs && msg.parentClockMs > 0) {
            relayStats.relayed += 1
          } else {
            relayStats.direct += 1
          }
          safe('应用播放状态', applyPlayback(clock.playback.value))
        }
      }
      if (!authoritative) {
        relayStats.nonPrimary += 1
      }
      // 中继职责：只把主父来的、刚被接受的样本往下传，并且带上**更新后**的偏移估计，
      // 这样子节点拿到的是"这一跳之后"的时钟锚点（顺序颠倒会让子节点永远慢一个样本）。
      if (authoritative && accepted) forwardToChildren(msg)
    }
  }

  function handlePeerMedia(peerId: string, media: DecodedMedia) {
    // 校验是异步的（crypto.subtle.digest）：用既有的 safe() 兜住"发射后不管"的异常，
    // 否则一次校验里的意外会变成未处理的 promise 拒绝，在控制台刷一片红。
    safe(`分片 ${media.chunkIndex} 校验`, ingestPeerMedia(peerId, media))
  }

  /**
   * 收到一个分片：**先校验内容，再入库**（F-11）。
   *
   * 三条设计约束（都是实的，不是漂亮话）：
   *   1. **每个分片只算一遍摘要**。已经拿到这一片时（并发副本 / 迟到响应 / push 分发）
   *      直接结算在途请求、不再算 —— 那是最常见的重复路径。
   *   2. **不引入新的写入顺序**。校验只推迟"入库时刻"（异步摘要），入库之后仍然是
   *      原来的 `put + flushOrdered`，`flushOrdered` 依旧按 `nextAppend` 起连续追加 ——
   *      乱序分片本来就靠它排队，所以校验没有破坏顺序落库语义。
   *   3. **校验不过就不结算在途请求**。那条请求会走**原有**的超时路径失败
   *      （`noteFailure` 记账 → 连续到上限即降权 → 转投下一个候选父），
   *      于是坏数据既不落库也不 append。坏字节来自哪个父节点也一并留在取数失败日志里。
   */
  async function ingestPeerMedia(peerId: string, media: DecodedMedia) {
    const index = media.chunkIndex
    const isInit = index === 0 || media.kind === KIND_INIT

    if (!isInit && chunkStore.has(index)) {
      // 已经有了（重复副本）：结算请求即可，别再算一遍摘要。
      requester.handleMedia(peerId, media)
      return
    }

    const expected = isInit ? '' : expectedSha256(index)
    if (expected !== '') {
      const epoch = mediaEpoch
      const actual = await sha256Hex(media.payload)
      if (epoch !== mediaEpoch) {
        // 缓冲/房间已经复位：这一片属于上一个会话，直接丢掉（在途请求由 reset 结算）。
        return
      }
      if (actual === null) {
        // 非安全上下文（明文 http + 局域网 IP）：没有 crypto.subtle，降级为不校验并计数。
        hashSkipped.value += 1
      } else if (actual !== expected) {
        hashMismatches.value += 1
        noteFetchFailure(
          `分片 ${index} ← ${peerId.slice(0, 8)}: sha256 与索引不符（对端内容不可信，已丢弃不入库）`,
        )
        // 这条父节点刚交出了坏字节：按原有失败机制记账（连续到上限即降权 + 立刻转投）。
        topology.noteFailure(index, peerId)
        return
      } else {
        hashVerified.value += 1
      }
    }

    const delivery = requester.handleMedia(peerId, media)
    if (isInit || delivery.index === 0 || delivery.kind === KIND_INIT) {
      chunkStore.putInit(delivery.payload)
    } else {
      chunkStore.put(delivery.index, delivery.payload)
    }
    flushOrdered()
  }

  /**
   * 当前播放头所在分片；取不到（没有索引 / 时钟未就绪）返回 null。
   *
   * 发送队列的紧迫度排序用它（T2-1）：观众用**权威进度**（主播此刻该播到的位置），
   * 因为门控期间播放头还钉在 0，用它会得出"所有分片都很远"的结论。
   * 主播没有权威进度（它就是源），退回自己的播放器位置。
   */
  function playheadSegment(): number | null {
    const index = mediaIndex.value
    if (!index || index.segments.length === 0) {
      return null
    }
    const time = isHost.value ? player.video.value?.currentTime : (clock.expectedAt() ?? null)
    if (time === null || time === undefined || !Number.isFinite(time)) {
      return null
    }
    return segmentIndexAt(index, time)
  }

  /**
   * 应答队列：把"读磁盘"从 DataChannel 的 onmessage 回调里挪出来。
   *
   * 之前 serveRequest 直接跑在消息回调里，一次读盘失败（NotFoundError：文件被移动/替换、
   * 句柄过期）就变成未处理的 promise 拒绝，在控制台刷成一片红，
   * 而且并发请求会一起挤在消息回调里互相拖慢。
   * 现在回调只入队，真正的读盘按并发上限在队列里执行，任何异常都在这里落地。
   *
   * 出队顺序**不是到达顺序**（T2-1）：每个任务记下入队时刻，并按"离播放头还有多远"取最急的一条
   * （见 utils/serveSchedule.ts）。改动前纯粹 FIFO，于是"马上要播的那一片"可能排在一堆
   * 远端分片后面 —— 这正是紧要分片迟到、观众卡一下的直接原因。
   */
  interface ServeTask {
    peerId: string
    rid: string
    index: number
    /** 入队序号：同紧迫度时先到先发。 */
    seq: number
  }

  const serveQueue: ServeTask[] = []
  let serveSeq = 0
  let serveRunning = 0
  const SERVE_CONCURRENCY = 3
  const SERVE_QUEUE_MAX = 64

  function enqueueServe(peerId: string, msg: PeerControl) {
    if (msg.idx === undefined) return
    if (serveQueue.length >= SERVE_QUEUE_MAX) {
      noteServe(`分片 ${msg.idx}: 队列已满，拒绝`)
      rtc.send(peerId, encodeControl({ t: 'err', rid: msg.rid, idx: msg.idx, code: 'BUSY' }))
      return
    }
    serveSeq += 1
    serveQueue.push({ peerId, rid: msg.rid ?? '', index: msg.idx, seq: serveSeq })
    void drainServeQueue()
  }

  async function drainServeQueue() {
    while (serveRunning < SERVE_CONCURRENCY && serveQueue.length > 0) {
      // 取"最急的那一条"：先算下标再 splice，避免并发 drain 把同一条发两次。
      const at = pickServeTaskIndex(serveQueue, playheadSegment())
      const task = serveQueue.splice(at < 0 ? 0 : at, 1)[0] as ServeTask
      serveRunning += 1
      try {
        await serveOne(task.peerId, task.rid, task.index)
      } catch (err) {
        // 兜底：异常必须在这里落地，绝不能再变成"未处理的拒绝"。
        noteServe(`分片 ${task.index}: 异常 ${(err as Error).name}: ${(err as Error).message}`)
        rtc.send(task.peerId, encodeControl({ t: 'err', rid: task.rid, idx: task.index, code: 'INTERNAL' }))
      } finally {
        serveRunning -= 1
      }
    }
  }

  /**
   * 本地取一片：主播读磁盘，中继读自己的分片仓库。
   *
   * 读盘失败重试一次：NotFoundError 多数是瞬时的（文件正在被替换、句柄刚过期），
   * 直接放弃会让整间屋子卡在"缺这一片"上。
   */
  async function readLocalChunk(index: number): Promise<Uint8Array<ArrayBuffer> | null> {
    if (!isHost.value) {
      return index === 0 ? chunkStore.getInit() : chunkStore.get(index)
    }
    for (let attempt = 0; attempt < 2; attempt += 1) {
      try {
        return await media.readChunk(index)
      } catch (err) {
        noteServe(`分片 ${index}: 读取失败 ${(err as Error).name}（第 ${attempt + 1} 次）`)
        if (attempt === 0) {
          await new Promise((resolve) => window.setTimeout(resolve, 120))
        }
      }
    }
    return null
  }

  /** 应答一个分片请求：先回 ack，再读本地，最后按 DataChannel 上限分片发出。 */
  async function serveOne(peerId: string, rid: string, index: number) {
    if (!rtc.send(peerId, encodeControl({ t: 'chunk', rid, idx: index }))) {
      noteServe(`分片 ${index}: 通道不可用，未受理`)
      return
    }

    const payload = await readLocalChunk(index)
    if (!payload) {
      // 这条以前是静默的：观众只会"一直缓冲"，谁也看不出是本地没有这一片。
      noteServe(`分片 ${index}: 本地没有这一片（NOT_FOUND）`)
      rtc.send(peerId, encodeControl({ t: 'err', rid, idx: index, code: 'NOT_FOUND' }))
      return
    }

    await throttleWait(payload.byteLength)
    // 发送失败必须记账：以前这里不检查返回值，于是"发不出去"和"没收到"看起来一模一样。
    // 分片由 rtc.sendMedia 按 DataChannel 单条消息上限切开、带背压地发。
    const sent = await rtc.sendMedia(peerId, index === 0 ? KIND_INIT : KIND_MEDIA, index, payload)
    if (!sent) {
      noteServe(`分片 ${index}: 发送失败（${payload.byteLength} 字节，通道 ${rtc.openChannels().length} 条）`)
      return
    }
    noteServe(`分片 ${index}: 已发送 ${payload.byteLength} 字节`)
  }

  // ---------- 验收钩子（仅调试用，生产路径不设置）----------
  let uploadThrottleBps = 0
  let throttleAllowance = 0
  let throttleLast = 0
  /**
   * 发送串行链：带宽上限必须**共享**。
   *
   * 早期实现是"每个请求各自等一下"，结果被并发放大：三个子节点各 4 个在途请求，
   * 即使限到 6 kbps，聚合吞吐照样够 0.5 片/秒 —— 验收脚本的"限速隔离"因此一直是空操作。
   * 把发送串成一条链，"带宽"才真正是这条上行共用的额度。
   */
  let throttleChain: Promise<void> = Promise.resolve()

  /** 应用层限速：模拟"这个节点上行只有 N bps"（SPEC §10 M3 验收 D 用）。 */
  function throttleWait(bytes: number): Promise<void> {
    if (uploadThrottleBps <= 0) {
      return Promise.resolve()
    }
    const next = throttleChain.then(async () => {
      const now = performance.now()
      throttleAllowance += ((now - throttleLast) / 1000) * uploadThrottleBps
      throttleLast = now
      if (throttleAllowance >= bytes) {
        throttleAllowance -= bytes
        return
      }
      // 单次等待封顶：真实链路里丢包/重传会打断"慢慢发"，这里也必须给个上限，
      // 否则一个分片会把发送链堵死几十秒，看起来像"节点卡死"而不是"带宽不足"。
      const waitMs = Math.min(((bytes - throttleAllowance) / uploadThrottleBps) * 1000, 8000)
      throttleAllowance = 0
      await new Promise((resolve) => window.setTimeout(resolve, waitMs))
    })
    throttleChain = next.catch(() => undefined)
    return next
  }

  /** 验收脚本用它注入实测上行：headless 下 getStats 不产生可用估计（C15）。 */
  function reportMetrics(uploadCapacityBps: number, rttMs = 20) {
    signaling.send({ type: T.Metrics, metrics: { uploadCapacityBps, rttMs } })
  }

  // ---------- 分片调度（主播与观众共用）----------
  /** 与某个成员建立连接（拓扑分配与主播回落都走它）。 */
  async function connectTo(peerId: string) {
    if (!peerId || peerId === clientId.value) return
    try {
      await rtc.connect(peerId)
    } catch (err) {
      lastError.value = `连接 ${peerId} 失败：${(err as Error).message}`
    }
  }

  /**
   * 逐跳中继：把权威进度转发给子节点，并改写成"以我为起点"的时钟锚点（SPEC §7.5）。
   *
   * 不改 currentTime / hostClockMs / seq —— 那是主播的权威值；
   * 只补 parentClockMs（我此刻的本地时钟）与 parentOffsetMs（我到主播的偏移估计）。
   * 子节点因此只需要测"它到我"这一跳，再与我的偏移相加，
   * 多跳链路不再把每一跳的排队延迟都累加进偏移估计（ALGORITHM P1）。
   */
  function forwardToChildren(msg: PeerControl) {
    const relayed: PeerControl = {
      ...msg,
      parentClockMs: Math.round(performance.now()),
      parentOffsetMs: Math.round(clock.offsetMs.value),
    }
    relayStats.forwarded += 1
    for (const childId of topology.children.value) {
      rtc.send(childId, encodeControl(relayed))
    }
  }

  /** 广播分片拥有位图，并同步上报服务端（服务端只记录，供监控与诊断）。 */
  function broadcastHaveState() {
    const result = topology.broadcastHave()
    if (result) {
      signaling.send({ type: T.ChunksReport, have: result.bits, complete: result.complete })
    }
  }

  /** 换父之后复位取数状态：旧父节点上的在途请求已经无意义。 */
  function resetFetchState() {
    requiredSegment = null
    requiredSegmentSince = 0
    initRequested = chunkStore.hasInit()
    topology.resetAttempts()
    requester.reset()
  }

  /**
   * 某个父节点实测的 ping RTT（毫秒）；通道没开或还没测到返回 0。
   *
   * 数据通道的 ping 每 3s 才刷一次（useWebRTC.PING_INTERVAL_MS），所以起播最初几秒必然
   * 读到 0 —— deriveRequestTimeoutMs 会用 800ms 兜底，不会退化成"0 毫秒超时"。
   */
  function perPeerRttMs(peerId: string): number {
    const peer = rtc.peers.value.get(peerId)
    if (!peer || !peer.channelOpen) {
      return 0
    }
    return peer.rttMs
  }

  /** 平均分片字节数（在途推导与超时估计共用）；索引为空或分片数为 0 时返回 0。 */
  function avgSegmentBytesOf(): number {
    const index = mediaIndex.value
    if (!index || index.segments.length === 0) {
      return 0
    }
    return index.totalBytes / index.segments.length
  }

  /**
   * 按该边**峰值速率**"传完一个平均分片"预计要多久（毫秒）；没有样本时返回 0。
   *
   * 数据通道 ping 测到的 RTT 只反映**控制报文**的往返，不含分片自身的传输时间
   * （跨运营商的慢边上，200 KB 分片要 800ms，而 3×RTT 可能只有 600ms），
   * 所以超时估计必须再叠上这一项，否则会必然误判超时 → 重复请求 → 白烧上行。
   *
   * 用峰值而不是当前 EWMA：退化的父节点当前速率很低，拿它估算会把超时越拖越长，
   * 恰好抵消"坏父快速换掉"（详见 utils/serveSchedule.ts 的说明）。
   */
  function expectedDeliveryMsOf(peerId: string): number {
    const peak = requester.edges.value.get(peerId)?.peakRateBps ?? 0
    const avg = avgSegmentBytesOf()
    if (!(peak > 0) || !(avg > 0)) {
      return 0
    }
    return (avg / peak) * 1000
  }

  const requester = useChunkRequester({
    send: (peerId, data) => rtc.send(peerId, data),
    // T3-1：改动前这里没有传值，用的是 useChunkRequester 里写死的 **3000ms**。
    // 现在按该父节点的实测 RTT + 实测边速率推导：
    // clamp(500ms, 10s, max(3×RTT, 2×预计传输时间))；两者都没测到就用 800ms。
    timeoutMs: (peerId) =>
      deriveRequestTimeoutMs(perPeerRttMs(peerId), {
        expectedDeliveryMs: expectedDeliveryMsOf(peerId),
      }),
  })

  /**
   * 当前的在途上限（T2-2）。
   *
   * 从"所有父节点的实测吞吐之和"倒推：`clamp(2, 8, ceil(edgeRate × 0.4s / 平均分片字节))`。
   * 没有测量数据（起播阶段）时回落 `MAX_INFLIGHT = 4`，与改动前一致。
   */
  const inflightLimit = computed(() =>
    deriveInflightLimit(requester.totalEdgeRateBps(), avgSegmentBytesOf(), { fallback: MAX_INFLIGHT }),
  )

  /** 进入加载门控：暂停播放，等到"主播时间戳所在分片起连续 n 片"再起播。 */
  function enterGate(reason: string, thresholdSegments = STARTUP_GATE_SEGMENTS) {
    if (isHost.value) return
    if (!gated.value) {
      gateStartedAt = performance.now()
      gateBufferedSegments.value = 0
    }
    gated.value = true
    gateReason.value = reason
    gateThresholdSegments.value = thresholdSegments
    syncMode.value = 'gated'
    player.video.value?.pause()
  }

  /**
   * 门控检查：缓冲够了就起播，不够就继续等。
   *
   * 判据是"主播时间戳所在分片 + 连续 n 片"（SPEC §7.3）：
   *   - 锚点取主播此刻应播到的位置 —— 门控期间播放头还停在 0，按播放头判会南辕北辙；
   *   - 数的是**完整连续**的分片，而不是追加游标：游标只能说明"曾经 append 过"。
   *
   * 永不超时是刻意的：宁可一直显示"加载中"，也不要在薄缓冲下起播 ——
   * 后者会立刻触发大幅漂移矫正，把"同步偏差"从几十毫秒放大到几百毫秒。
   */
  async function evaluateGate() {
    const video = player.video.value
    const index = mediaIndex.value
    if (!video || !index) return

    const anchor = clock.ready.value ? (clock.expectedAt() ?? 0) : clock.playback.value.currentTime
    gateBufferedSec.value = player.bufferedAhead(anchor)
    gateWaitedSec.value = (performance.now() - gateStartedAt) / 1000

    // 门控期间也要算滞后：播放头停住、主播继续往前走，这个差值就是"落后多少"。
    // 超过阈值就给用户一句话 —— 开闸时会 seek 到主播**当前**位置，
    // 等价于"提示并跳到主播进度"（SPEC §7.6）；目标分片缺失时游标会自动前移。
    if (clock.ready.value && video) {
      lagSec.value = anchor - video.currentTime
      if (lagSec.value > LAG_JUMP_SEC) {
        lagNotice.value = `落后主播 ${lagSec.value.toFixed(0)}s，正在跳到主播进度重新缓冲`
      }
    }

    const anchorSeg = segmentIndexAt(index, anchor)
    const contiguous = player.bufferedSegmentsFrom(index, anchorSeg)
    gateBufferedSegments.value = contiguous
    // 断线等待期间即使缓冲已经够了也不开闸：那不是"缓冲不足"，是"没有权威进度"。
    if (holdPlayback()) {
      syncMode.value = 'gated'
      return
    }
    // 时钟样本不够就再等：开闸瞬间的偏差尖峰全部来自还没收敛的偏移估计
    //（实测起播后 0.2s 的偏差 -387ms，随后被速率修正逐秒拉回）。
    const clockReady = clock.sampleCount() >= MIN_CLOCK_SAMPLES && clock.settledSeconds() >= CLOCK_SETTLE_SEC
    if (contiguous < gateThresholdSegments.value || !clockReady) {
      syncMode.value = 'gated'
      return
    }

    gated.value = false
    gateReason.value = ''
    // 数据已经在缓冲里：直接定位到权威位置，不清缓冲。
    noteLifecycle(
      `开闸 anchor=${anchor.toFixed(2)} seg=${anchorSeg} 连续=${contiguous} ` +
        `offset=${Math.round(clock.offsetMs.value)}(hop=${Math.round(clock.hopOffsetMs.value)}` +
        `+parent=${Math.round(clock.parentOffsetMs.value)}) samples=${clock.sampleCount()}`,
    )
    player.seekTo(anchor)
    noteLifecycle(`开闸后 video=${(video.currentTime ?? 0).toFixed(2)} expected=${(clock.expectedAt() ?? 0).toFixed(2)}`)
    if (!clock.playback.value.paused) {
      await tryPlay()
    }
  }

  async function pumpPrefetch() {
    const index = mediaIndex.value
    if (!index) return

    // 自愈：MediaSource 可能被平台悄悄关闭（元素被替换、资源被回收）。
    // 只要"有索引但没挂上"，就在这里重新挂载 —— 否则一旦掉线就永久卡死，
    // 表现为"画面永远不动，而日志里只有一行 appendBuffer 失败"。
    if (!player.attached.value) {
      noteLifecycle('播放器未挂载，调度器触发重新挂载')
      await ensurePlayer(index)
      resetFetchState()
      return
    }

    const host = isHost.value
    const peer = hostId.value
    if (!host && !peer) return

    // 观众在通道建立前不发请求：否则每个 tick 都会把整个窗口的请求打成一片失败。
    if (!host && rtc.openChannels().length === 0) {
      maybeReconnect()
      return
    }

    if (!chunkStore.hasInit() && !initRequested) {
      initRequested = true
      void fetchChunk(0)
    }

    // 门控期间播放头还停在 0，必须按"权威目标位置"预取：
    // 否则会从 0 开始拉一堆永远不会播的分片，而真正的起播位置一直没数据。
    const gatedAnchor = gated.value && clock.ready.value ? (clock.expectedAt() ?? 0) : null
    const time = gatedAnchor ?? player.video.value?.currentTime ?? 0
    const playheadSeg = chunkStore.hasInit() ? segmentIndexAt(index, time) : 1

    // 追加游标也要跟着门控目标走，否则 flushOrdered 会一直等一个不会被取到的分片。
    if (gatedAnchor !== null && nextAppend < playheadSeg) {
      nextAppend = playheadSeg
      // 只在这个位置确实还没数据时才"盯着它补取"；已经有缓冲就别再试，
      // 否则每 200ms 就会向下一个父节点发一次注定失败的请求（实测能刷出几十次）。
      requiredSegment = player.bufferedAhead(time) <= 0 ? playheadSeg : null
      requiredSegmentSince = performance.now()
    } else if (requiredSegment !== null && player.bufferedAhead(time) > 0) {
      requiredSegment = null
      requiredSegmentSince = 0
    }
    // 起点不能低于 nextAppend：否则会把已经 append 过、甚至已被淘汰的分片反复重取
    //（实测能刷出上千次无谓交付）。
    const start = Math.min(Math.max(playheadSeg, nextAppend), index.segments.length)
    // 终点必须以**播放头**为锚：以 nextAppend 为锚会让窗口一路前移，把整部片子拉完。
    const windowSize = host ? HOST_WINDOW : PREFETCH_WINDOW
    const end = Math.min(index.segments.length, playheadSeg + windowSize)

    for (let i = start; i <= end; i += 1) {
      if (chunkStore.has(i)) continue
      // 在途上限是**推导值**（T2-2）：固定值时快链路喂不饱、慢链路会被压垮。
      if (!host && requester.pendingCount() >= inflightLimit.value) break
      void fetchChunk(i)
    }

    // 跳转目标可能落在播放头窗口之外（播放头还停在旧位置），必须单独补取；
    // 取到之前一直重试，否则一次网络抖动就会让跳转永远停在旧位置。
    if (requiredSegment !== null) {
      if (nextAppend > requiredSegment || chunkStore.has(requiredSegment)) {
        requiredSegment = null
        requiredSegmentSince = 0
      } else if (performance.now() - requiredSegmentSince > REQUIRED_STALE_MS) {
        // 目标分片迟迟拿不到（多半是全网都还没有，例如抢跑太远的位置）：
        // 改按主播**当前**进度重新取片，而不是盯着一个旧位置无限等下去（SPEC §7.5）。
        const hostNow = clock.ready.value ? clock.expectedAt() : null
        const fresh = hostNow !== null ? segmentIndexAt(index, hostNow) : requiredSegment
        if (fresh > requiredSegment) {
          noteLifecycle(`目标分片 ${requiredSegment} 取不到，改按主播当前进度 ${fresh}`)
          requiredSegment = fresh
          // 游标跟着走：否则 flushOrdered 会一直等着那个永远不来的分片。
          nextAppend = fresh
        }
        requiredSegmentSince = performance.now()
      } else if (host || requester.pendingCount() < inflightLimit.value) {
        void fetchChunk(requiredSegment)
      }
    }
  }

  /**
   * 取一片（观众走网络 / 主播走磁盘）。
   *
   * 观众的失败处理是本轮改的重点（T3-2）：**超时后立刻转投下一个候选父节点**，
   * 而不是等下一个 200ms tick 再对同一个父重试一轮 ——
   * 那正是"卡在一个坏父上"的表现：每一轮都要把同一份超时再等一遍。
   *
   * 只对**超时**做立即转投：`send` 失败（通道没开）与远端明确拒绝（NOT_FOUND/BUSY）
   * 立刻换父也拿不到数据（前者是连接问题，后者说明对方真没有），交给下一个 tick 更干净。
   */
  async function fetchChunk(index: number) {
    if (isHost.value) {
      // 主播只从磁盘读（结构不变）。
      try {
        const payload = await media.readChunk(index)
        if (!payload) return
        if (index === 0) {
          chunkStore.putInit(payload)
        } else {
          chunkStore.put(index, payload)
        }
        flushOrdered()
      } catch (err) {
        chunkErrors.value += 1
        noteFetchFailure(`分片 ${index}: ${(err as Error).message ?? '未知失败'}`)
      }
      return
    }

    let excluded = ''
    for (let hop = 0; hop < MAX_FETCH_FAILOVER_HOPS; hop += 1) {
      // 并发请求之间可能已经有人把这一片拿到了：别再白问一遍。
      if (chunkStore.has(index)) return
      const peerId = topology.pickParent(index, excluded)
      if (!peerId) {
        chunkErrors.value += 1
        noteFetchFailure(`分片 ${index}: 没有可用父节点`)
        return
      }
      topology.noteAttempt(index)
      try {
        const delivery = await requester.request(peerId, index)
        topology.noteDelivered(index)
        if (index === 0 || delivery.kind === KIND_INIT) {
          chunkStore.putInit(delivery.payload)
        } else {
          chunkStore.put(delivery.index, delivery.payload)
        }
        flushOrdered()
        return
      } catch (err) {
        // 必须留痕：以前这里是空的 catch，于是"一片都没成功"在界面上完全看不出来。
        // T3 起带上**是从哪个父节点失败的** —— 换父取证必须能指出"从谁换到谁"。
        chunkErrors.value += 1
        noteFetchFailure(`分片 ${index} ← ${peerId.slice(0, 8)}: ${(err as Error).message ?? '未知失败'}`)
        const code = chunkErrorCode(err)
        if (code !== 'timeout') {
          // 远端明确说没有（NOT_FOUND/BUSY）不是"父节点坏"：多半只是位图过期，
          // 拿它去降权会把一个完全正常的父节点禁掉。只把**超时**计入连续失败。
          return
        }
        // 同一个 (父, 分片) 连续超时到上限 → 该父节点暂时降权（T3-2 的"不卡在坏父上"）。
        if (topology.noteFailure(index, peerId)) {
          noteLifecycle(
            `父节点 ${peerId.slice(0, 6)} 连续超时取不到分片 ${index}，暂时降权 ${PARENT_AVOID_TTL_MS / 1000}s`,
          )
        }
        // 超时换父：只有在"确实还有别的候选"时才计数与继续 ——
        // 否则计数会虚高（一个只有单父的观众永远换不了父）。
        const alternatives = topology.parents.value.filter((id) => id !== peerId)
        if (alternatives.length === 0) {
          return
        }
        timeoutFailovers.value += 1
        excluded = peerId
        noteLifecycle(`分片 ${index}: ${peerId.slice(0, 6)} 超时，立刻转投下一个候选父`)
      }
    }
  }

  /** 取数失败的环形日志（只留最近几条，够定位就行）。 */
  function noteFetchFailure(text: string) {
    fetchFailures.value = [...fetchFailures.value.slice(-7), `${Math.round(performance.now())}:${text}`]
  }

  /** 应答请求的环形日志：主播/中继侧"到底发出去没有"。 */
  function noteServe(text: string) {
    serveLog.value = [...serveLog.value.slice(-7), `${Math.round(performance.now())}:${text}`]
  }

  /**
   * 兜住"发射后不管"的异步调用。
   *
   * 以前到处是 `void someAsync()`：一旦它抛错就成了未处理的 promise 拒绝，
   * 浏览器控制台刷红，功能却"看起来还能用" —— 用户看到的那一堆错误就是这么来的。
   */
  function safe(what: string, task: Promise<unknown>): void {
    task.catch((err: unknown) => {
      const message = err instanceof Error ? `${err.name}: ${err.message}` : String(err)
      noteLifecycle(`${what} 失败 ${message}`)
    })
  }

  /** 严格按序号写入播放器：MSE 需要单调递增的时间戳，乱序 append 会报错。 */
  function flushOrdered() {
    const index = mediaIndex.value
    if (!index || !player.attached.value) return

    const init = chunkStore.getInit()
    if (init && !player.initAppended.value) {
      player.append(KIND_INIT, init)
    }

    while (chunkStore.has(nextAppend)) {
      const buf = chunkStore.get(nextAppend)
      if (!buf) break
      // 统计必须在 append **之前**读播放头：append 之后缓冲变了，但"到达时刻"已经过去。
      noteDeliveryTiming(nextAppend)
      player.append(KIND_MEDIA, buf)
      nextAppend += 1
    }

    // 丢开已经播过的分片，避免长视频吃满内存。
    const retention = topology.children.value.length > 0 ? 12 : 4
    chunkStore.evictBefore(Math.max(1, nextAppend - retention))
  }

  async function primeBuffer(segments: number, timeoutMs = 10000): Promise<boolean> {
    const deadline = performance.now() + timeoutMs
    while (performance.now() < deadline) {
      await pumpPrefetch()
      if (chunkStore.hasInit() && nextAppend > segments) return true
      await new Promise((resolve) => window.setTimeout(resolve, 50))
    }
    return chunkStore.hasInit() && nextAppend > 1
  }

  // ---------- 同步矫正 ----------
  async function applyPlayback(state: PlaybackState, action?: string) {
    const video = player.video.value
    if (!video || isHost.value) return

    // 加载中只更新权威状态（时钟锚点由调用方写入），不动播放器：
    // 没攒够缓冲就播，只会换来一轮同步振荡。
    if (gated.value) return

    // 跳转必须立即生效，即使此刻是暂停状态：
    // 否则暂停中跳转的观众会一直停在旧位置，等房主再次播放才被纠正（SPEC §7.4）。
    if (action === Action.Seek) {
      await hardSeek(state.currentTime)
    }

    if (state.paused) {
      video.pause()
      clock.setPaused(true)
      return
    }

    clock.setPaused(false)

    // 只在主播**改了**速率时套用权威速率；
    // 每条进度都无脑套用会把同步环的 ±5% 微调冲掉 —— 偏差会恒定卡死、永远修不回来。
    if (state.rate > 0 && state.rate !== authoritativeRate) {
      authoritativeRate = state.rate
      video.playbackRate = authoritativeRate
    }

    await tryPlay()
  }

  async function tryPlay() {
    const video = player.video.value
    if (!video || !video.paused) return
    try {
      await video.play()
      needsGesture.value = false
    } catch {
      // 浏览器自动播放策略：需要一次用户手势。
      needsGesture.value = true
    }
  }

  async function tickSync() {
    const video = player.video.value
    if (!video) return

    bufferedAhead.value = player.bufferedAhead(video.currentTime)
    if (isHost.value) {
      // 主播自己就是权威：本地状态必须反映真实播放器，否则视频在播而界面显示"已暂停"。
      clock.syncLocal(video.paused, video.currentTime, video.playbackRate)
      return
    }

    // 加载中：不矫正、不播放，只等缓冲够。
    if (gated.value) {
      await evaluateGate()
      return
    }

    const state = clock.playback.value
    if (!state.paused && video.paused && bufferedAhead.value > 0.3) {
      await tryPlay()
    }

    if (performance.now() < correctionSettleUntil) {
      syncMode.value = 'settling'
      return
    }

    // 当前位置没有可播数据时一律不矫正：空 MediaSource 上的 seek 只会把 currentTime
    // 改成一个"空位置"，看起来像在播，实际什么都没缓冲（验证脚本正是靠这一点抓到的）。
    if (bufferedAhead.value <= 0) {
      syncMode.value = state.paused ? 'idle' : 'buffering'
      return
    }

    // 滞后过久（SPEC §7.5 卡顿策略的最后一步）：速率追不回来了，
    // 提示用户并**直接跳到主播当前进度**；目标分片缺失时由门控与
    // requiredSegment 的"改按主播当前进度取片"兜底。
    const hostNow = clock.expectedAt()
    if (hostNow !== null && !state.paused) {
      lagSec.value = hostNow - video.currentTime
      if (lagSec.value > LAG_JUMP_SEC) {
        // 限流：追不回来时不要每 100ms 跳一次（那会变成"反复重灌"的死循环）。
        if (performance.now() - lastLagJumpAt > 5000) {
          lastLagJumpAt = performance.now()
          lagNotice.value = `落后主播 ${lagSec.value.toFixed(1)}s，正在跳转到主播进度`
          noteLifecycle(`滞后跳转 lag=${lagSec.value.toFixed(1)}s target=${hostNow.toFixed(2)}`)
          await hardSeek(hostNow)
        }
        return
      }
      if (lagNotice.value !== '' && lagSec.value < LAG_CLEAR_SEC) {
        lagNotice.value = ''
      }
    }

    const result = clock.correction(video.currentTime)
    syncMode.value = result.mode

    switch (result.mode) {
      case 'rate': {
        const adjusted = authoritativeRate * result.rate
        if (Math.abs(video.playbackRate - adjusted) > 0.001) video.playbackRate = adjusted
        break
      }
      case 'ok':
        if (Math.abs(video.playbackRate - authoritativeRate) > 0.001) video.playbackRate = authoritativeRate
        break
      case 'jump':
        if (!player.jumpWithinBuffer(result.target)) {
          await hardSeek(result.target)
        }
        break
      case 'seek':
        await hardSeek(result.target)
        break
      default:
        break
    }
  }

  /** 等待某个分片被真正 append 进播放器（nextAppend 越过它即表示已写入）。 */
  async function waitForAppend(segIndex: number, timeoutMs = 8000): Promise<boolean> {
    const deadline = performance.now() + timeoutMs
    while (performance.now() < deadline) {
      if (nextAppend > segIndex) return true
      await new Promise((resolve) => window.setTimeout(resolve, 50))
    }
    return nextAppend > segIndex
  }

  /**
   * 跳转流水线：清缓冲 → 补齐目标分片 → 定位。
   *
   * 顺序不能颠倒：空缓冲上给 currentTime 赋值会被浏览器直接丢弃
   * （HTML 规范：readyState 为 HAVE_NOTHING 时只记录"默认起始位置"）。
   * 之前先清空再定位，结果房主和观众都卡在旧位置 —— 验证脚本的轨迹把这一点暴露得很清楚。
   */
  async function seekPipeline(target: number, force = false) {
    const index = mediaIndex.value
    if (!index) return

    const now = performance.now()
    if (!force && Math.abs(target - lastHardSeekTarget) < 0.5 && now - lastHardSeekAt < 3000) {
      return
    }
    lastHardSeekAt = now
    lastHardSeekTarget = target
    // 只有观众的跳转算同步质量指标；主播自己按的跳转不是"矫正"。
    if (!isHost.value) {
      syncResets.value += 1
    }
    syncMode.value = 'seek'
    // 跳转后给 1.5s 稳定期：这段时间里缓冲还在重建，
    // 立刻按目标矫正只会在"seek → 追 → 再 seek"之间来回振荡，把偏差 spike 放大。
    correctionSettleUntil = performance.now() + 1500
    enterGate('跳转后重建缓冲', RESEEK_GATE_SEGMENTS)

    const segIndex = segmentIndexAt(index, target)
    await player.clearBuffered()
    resetChunkStore()
    initRequested = false
    nextAppend = segIndex
    requiredSegment = segIndex
    requiredSegmentSince = performance.now()

    await pumpPrefetch()
    await waitForAppend(segIndex)
    player.seekTo(target)
    await pumpPrefetch()
  }

  /** 同步环发现巨大偏差时的重灌。 */
  async function hardSeek(target: number) {
    await seekPipeline(target)
  }

  // ---------- 主播侧：进度与度量 ----------
  function buildProgress(): PeerControl | null {
    const video = player.video.value
    if (!video) return null
    progressSeq += 1
    const hostClockMs = Math.round(performance.now())
    return {
      t: 'progress',
      currentTime: video.currentTime,
      hostClockMs,
      clockEpoch: clockEpoch.value,
      paused: video.paused,
      rate: video.playbackRate,
      seq: progressSeq,
      // 主播本地时钟就是主播时钟：parentOffsetMs 为 0。
      // 中继节点转发时会用"它自己的时钟 + 它到主播的偏移"改写这两个字段。
      parentClockMs: hostClockMs,
      parentOffsetMs: 0,
    }
  }

  /**
   * 新接入的子节点连发几个进度样本。
   *
   * 时钟偏移用的是"窗口内最小值"滤波：只有一两个样本时它还很粗糙，
   * 新节点起播那几秒的偏差 spike 主要来自这里。多发几次能让它迅速收敛。
   */
  function sendProgressBurst(peerId: string) {
    for (let i = 0; i < 10; i += 1) {
      window.setTimeout(() => sendProgressTo(peerId), i * 110)
    }
  }

  function sendProgressTo(peerId: string) {
    const msg = buildProgress()
    if (msg) rtc.send(peerId, encodeControl(msg))
  }

  function clockStoreSendTimeSync(peerId: string) {
    const hostClockMs = Math.round(performance.now())
    rtc.send(
      peerId,
      encodeControl({
        t: 'time-sync',
        hostClockMs,
        clockEpoch: clockEpoch.value,
        seq: progressSeq,
        parentClockMs: hostClockMs,
        parentOffsetMs: 0,
      }),
    )
  }

  function tickProgress() {
    if (!isHost.value || !joined.value) return
    const msg = buildProgress()
    if (!msg) return
    rtc.broadcast(encodeControl(msg))
  }

  function tickTimeSync() {
    if (!isHost.value || !joined.value) return
    const hostClockMs = Math.round(performance.now())
    rtc.broadcast(
      encodeControl({
        t: 'time-sync',
        hostClockMs,
        clockEpoch: clockEpoch.value,
        seq: progressSeq,
        parentClockMs: hostClockMs,
        parentOffsetMs: 0,
      }),
    )
  }

  function tickMetrics() {
    if (!joined.value) return
    signaling.send({
      type: T.Metrics,
      metrics: {
        rttMs: rtc.averageRttMs(),
        uploadCapacityBps: rtc.uploadCapacityBps.value,
        depth: depth.value,
        // 健康度：服务器据此判断"这个节点是不是该换条路"
        bufferHealth: bufferedAhead.value,
        p95DeliveryMs: requester.p95DeliveryMs(),
        stallCount: player.stalls.value,
        degraded: gated.value || bufferedAhead.value < BUFFER_TARGET_SEC,
        primaryId: topology.primaryId.value,
      },
    })
  }

  // ---------- 循环 ----------
  function startLoops() {
    stopLoops()
    schedulerTimer = window.setInterval(() => void pumpPrefetch(), 200)
    syncTimer = window.setInterval(() => void tickSync(), 100)
    metricsTimer = window.setInterval(tickMetrics, 5000)
    // 先立刻广播一次：子节点越早知道"我有哪些分片"，越少把请求打给还没有数据的节点。
    broadcastHaveState()
    haveTimer = window.setInterval(broadcastHaveState, 3000)
    if (isHost.value) {
      progressTimer = window.setInterval(() => {
        tickProgress()
        tickTimeSync()
      }, 500)
    }
    rtc.startStats()
  }

  function stopLoops() {
    for (const timer of [schedulerTimer, syncTimer, progressTimer, metricsTimer, haveTimer]) {
      if (timer !== undefined) window.clearInterval(timer)
    }
    schedulerTimer = undefined
    syncTimer = undefined
    progressTimer = undefined
    metricsTimer = undefined
    haveTimer = undefined
    rtc.stopStats()
  }

  // ---------- 媒体索引 ----------
  async function applyRemoteMediaIndex(index: MediaIndex) {
    const changed =
      !mediaIndex.value ||
      mediaIndex.value.totalDuration !== index.totalDuration ||
      mediaIndex.value.segments.length !== index.segments.length

    mediaIndex.value = index
    await ensurePlayer(index)

    if (!isHost.value) {
      // 媒体索引可能比观众晚到（观众先进房、主播后选片开播）：此时取数游标、
      // 已缓存分片、在途请求全是"没有媒体时"的残留状态，必须整体复位，
      // 否则会卡在"缓冲中 3/4 片"这种位置再也上不去（刷新页面才恢复）。
      if (changed) {
        noteLifecycle(`媒体索引到达（${index.segments.length} 段），复位取数状态`)
        resetFetchState()
        resetChunkStore()
        initRequested = false
        nextAppend = 1
        requiredSegment = null
        requiredSegmentSince = 0
        await player.clearBuffered()
        enterGate('等待媒体就绪后重新缓冲', STARTUP_GATE_SEGMENTS)
      }
      await connectToHost()
    }
  }

  /**
   * 挂载播放器（幂等且**不可重入**）。
   *
   * 必须挡住并发调用：媒体索引与视频元素是两条独立的到达路径，
   * 两次并发 attach 会创建两个 MediaSource，后一个替换掉前一个，
   * 于是 sourceBuffer 指向被摘除的那个，之后每次 appendBuffer 都报
   * "This SourceBuffer has been removed from the parent media source"。
   */
  async function ensurePlayer(index: MediaIndex) {
    if (player.attached.value) return
    if (playerAttachInFlight) return playerAttachInFlight

    const el = videoEl.value
    if (!el) return

    playerAttachInFlight = (async () => {
      await player.attach(el, index.mimeType)
    })()
      .catch((err: Error) => {
        mediaError.value = err.message
      })
      .finally(() => {
        playerAttachInFlight = null
      })

    await playerAttachInFlight
    if (!player.attached.value) return

    resetChunkStore()
    initRequested = false
    nextAppend = 1
    requiredSegment = null
    startLoops()

    if (!isHost.value) {
      enterGate('新节点接入，等待缓冲')
    }
  }

  async function connectToHost(force = false) {
    if (isHost.value) return
    // 优先连"分配给我的主父"：叶子可能被安排在转发节点下，
    // 全都直连主播会把主播的上行打满（容量闸门正是按树算的）。
    const target = topology.primaryId.value || hostId.value
    if (!target) return
    if (!force && rtc.peerCount() > 0) return
    await connectTo(target)
  }

  /** 通道迟迟建不起来时（ICE 失败 / 主播刚重连）每隔 2s 重试一次，而不是干等。 */
  function maybeReconnect() {
    const now = performance.now()
    if (now - lastConnectAttemptAt < 2000) return
    lastConnectAttemptAt = now
    void connectToHost(true)
  }

  // ---------- 对外动作 ----------
  /**
   * 拉一次 `/api/ice`。
   *
   * 为什么必须主动拉：`POST /api/rooms` 的响应里带 ICE 配置，但**直接通过分享链接进房**
   * 的人不会经过那个调用，`joined` 信封里也没有这些字段 —— 结果 PeerConnection 用
   * `{iceServers: []}` 构造，配好的 STUN 永远不生效，对称 NAT 下就是"进得去房间、
   * 一直缓冲 0 片"（test/script/verify-ice.mjs 抓到的）。这条是实测结论，不是推测。
   * 时序由 useIceConfig 负责：失败退避重试，绝不挡住进房。
   */
  async function fetchIcePayload(): Promise<IcePayload> {
    const resp = await fetch('/api/ice')
    if (!resp.ok) {
      throw new Error(`HTTP ${resp.status}`)
    }
    return (await resp.json()) as IcePayload
  }

  /**
   * ICE restart：列表真的变了才走这里，而且**只有发起方**（下游子节点）会真的发出新 offer。
   * 判定发起方由 useWebRTC 按"这条连接是不是我 connect() 出去的"来做，
   * 所以这里不需要（也不应该）按角色判断。
   */
  async function restartIceForNewList(): Promise<void> {
    const restarted = await rtc.restartInitiators('ICE 列表变化')
    noteLifecycle(`ICE restart：${restarted} 条发起方连接，父节点侧只换配置不重启`)
  }

  /** 把一份 ICE 响应（`POST /api/rooms` 或 `/api/ice`）喂进缓存。 */
  function applyIceResponse(payload: IcePayload, label: string): void {
    iceConfig.seed(payload, label)
  }

  function enterRoom(creds: JoinCredentials) {
    noteLifecycle('enterRoom')
    leaveRoom()
    credentials.value = creds
    joined.value = false
    members.value = []
    chat.value = []
    roomClosed.value = ''
    lastError.value = ''
    needsGesture.value = false
    roomUnrecoverable.value = ''
    clientIdRotations.value = 0
    clientIdRotationStreak = 0
    rebuildAttempt = 0
    hostJoinedOnce = false

    // 先把 ICE 配置拿到手再连：PC 是在 connectTo 时构造的，晚拿到就白建了。
    // start() 会立刻拉一次并按 TTL 排下一次刷新，离开房间时由 stop() 收掉。
    iceConfig.start()
    // 主播页此前可能已经从 POST /api/rooms 拿到过列表（seed）：那时 onChanged 已经采过一次，
    // 这里的判空是为了"没有列表就不空采"，采样本身有并发保护。
    void rtc.sampleLocalCandidates(iceConfig.servers.value)

    signaling.connect(signalUrl(creds))
  }

  function leaveRoom() {
    noteLifecycle('leaveRoom')
    if (joined.value) signaling.send({ type: T.Leave })
    stopLoops()
    // 恢复路径上的定时器与提示必须一起清掉，否则离开房间后还会继续打服务端。
    stopResumeWait()
    cancelHostRejoin()
    clearHostOffline()
    rebuildAttempt = 0
    rebuildInFlight = false
    rebuildState.value = 'idle'
    roomUnrecoverable.value = ''
    requester.reset()
    // 边速率账本跟着房间一起作废：换房之后旧的父节点吞吐不该影响新房间的在途推导。
    requester.resetEdges()
    rtc.closeAll()
    topology.reset()
    player.detach()
    resetChunkStore()
    media.reset()
    // ICE 刷新定时器必须在这里停掉：否则切房之后残留的定时器会继续打旧房间的 /api/ice，
    // 并把旧房间的列表 setConfiguration 到新房间的连接上。
    iceConfig.stop()
    requiredSegment = null
    nextAppend = 1
    signaling.close()
    credentials.value = null
    joined.value = false
    members.value = []
    mediaIndex.value = null
  }

  /** 加载一份分片文件集合并开播（"选目录"与"服务端切片写回"两条路径共用）。 */
  async function publishLoadedMedia(files: FileList | File[]) {
    mediaError.value = ''
    try {
      const loaded = await media.loadDirectory(files)
      mediaIndex.value = loaded.index
      await ensurePlayer(loaded.index)
      signaling.send({ type: T.MediaIndex, mediaIndex: loaded.index })
    } catch (err) {
      mediaError.value = (err as Error).message
    }
  }

  /** 主播选择分片目录并开播。 */
  async function publishMediaDirectory(files: FileList) {
    await publishLoadedMedia(files)
  }

  /**
   * 主播用**已经拿到的文件列表**开播。
   *
   * 给「服务端切片」面板用：产物已经写进本地目录、文件名与索引都是已知的，
   * 不应该再要求用户选一次目录。行为与 publishMediaDirectory 完全一致。
   */
  async function publishMediaFiles(files: File[]) {
    await publishLoadedMedia(files)
  }

  function setVideoElement(el: HTMLVideoElement | null) {
    const previous = videoEl.value
    const lastTime = player.video.value?.currentTime ?? 0
    videoEl.value = el

    // 元素换了（组件重挂载）：旧元素上的 MediaSource 会随之关闭，
    // 必须重新挂载，并把"下一个要追加的分片"挪回当前播放位置。
    if (el && el !== previous && player.attached.value) {
      noteLifecycle('videoElement 被替换，重新挂载播放器')
      player.detach()
      const index = mediaIndex.value
      if (index) {
        nextAppend = segmentIndexAt(index, lastTime)
        requiredSegment = nextAppend
        initRequested = chunkStore.hasInit()
      }
    }

    if (el && mediaIndex.value && !player.attached.value) {
      void ensurePlayer(mediaIndex.value)
    }

    // 主播的播放器事件要能影响整个房间：原生控件暂停、浏览器自己停下都算。
    if (el && el !== previous) {
      relayHostControl(el)
    }
  }

  function sendChat(text: string) {
    const trimmed = text.trim()
    if (!trimmed || !joined.value) return
    // 不做本地追加：聊天的唯一次序由服务端给出（SPEC §5.1）。
    signaling.send({ type: T.Chat, text: trimmed })
  }

  function sendControl(action: string, patch: Partial<PlaybackState> = {}) {
    if (!isHost.value || !joined.value) return
    const video = player.video.value
    const paused = patch.paused ?? video?.paused ?? true
    const rate = patch.rate ?? video?.playbackRate ?? 1
    const currentTime = patch.currentTime ?? video?.currentTime ?? 0
    // 本地状态先跟上：主播界面的"播放中/已暂停"就是读它。
    clock.syncLocal(paused, currentTime, rate)
    lastControl = { paused, rate, at: performance.now() }
    signaling.send({
      type: T.RoomControl,
      action,
      currentTime,
      paused,
      rate,
      hostClockMs: Math.round(performance.now()),
      clockEpoch: clockEpoch.value,
    })
  }

  /**
   * 主播的播放器事件 → 广播控制。
   *
   * 必要性：以前只有"页面上的播放/暂停按钮"会下发控制。主播用**原生控件**、
   * 或者浏览器自己把视频停下（切后台、解码卡顿），观众端完全收不到通知，
   * 于是各播各的 —— 这就是"主播停了观众还在播、之后一直不同步"的来源。
   */
  function relayHostControl(el: HTMLVideoElement) {
    const push = (action: string, patch: Partial<PlaybackState> = {}, force = false) => {
      if (!isHost.value || !joined.value) return
      const paused = patch.paused ?? el.paused
      const rate = patch.rate ?? el.playbackRate
      // 去重：按钮自己也调 sendControl，播放器事件会再触发一次，
      // 500ms 内的相同状态只发一次，避免把房间刷成两条控制。
      if (
        !force &&
        lastControl.paused === paused &&
        lastControl.rate === rate &&
        performance.now() - lastControl.at < 500
      ) {
        return
      }
      sendControl(action, { ...patch, paused, rate })
    }

    el.addEventListener('play', () => push(Action.Play, { paused: false }))
    el.addEventListener('pause', () => {
      // seek 期间浏览器会先 pause 再 play，别把中间态当成"用户暂停"广播出去。
      if (el.seeking) return
      push(Action.Pause, { paused: true })
    })
    el.addEventListener('seeked', () => push(Action.Seek, { currentTime: el.currentTime }, true))
    el.addEventListener('ratechange', () => push(Action.Rate, { rate: el.playbackRate }, true))
  }

  async function play() {
    if (!isHost.value) return
    await primeBuffer(3)
    const video = player.video.value
    if (video) {
      try {
        await video.play()
        needsGesture.value = false
      } catch {
        needsGesture.value = true
      }
    }
    sendControl(Action.Play, { paused: false })
  }

  function pause() {
    if (!isHost.value) return
    player.video.value?.pause()
    sendControl(Action.Pause, { paused: true })
  }

  async function seekTo(seconds: number) {
    if (!isHost.value) return
    // 主播自己也走同一条流水线：它同样是 MediaSource 播放器，
    // 只改 currentTime 一样会被浏览器丢弃（这一点在验证中先被主播自己暴露出来）。
    await seekPipeline(seconds, true)
    sendControl(Action.Seek, { currentTime: seconds })
  }

  function setRate(rate: number) {
    if (!isHost.value) return
    const video = player.video.value
    if (video) video.playbackRate = rate
    sendControl(Action.Rate, { rate })
  }

  async function resumeAfterGesture() {
    needsGesture.value = false
    await tryPlay()
  }

  /**
   * 兼容旧入口：只喂 iceServers（没有 TTL/探测信息时）。
   * 新代码请用 applyIceResponse，把 ttlSeconds/expiresAt/probe 一起存下来。
   */
  function setIceServers(servers: RTCIceServer[]) {
    iceConfig.seed({ iceServers: servers }, 'setIceServers')
  }

  /** ICE 配置缓存的诊断快照（诊断抽屉、验收脚本、报告都用它）。 */
  function iceDiagnostics(): IceSnapshot {
    return iceConfig.snapshot()
  }

  /**
   * 播放健康度（T4）：这是用户判断"延迟/卡顿优化有没有效"的那块面板。
   *
   * 为什么用 computed 而不是普通函数：它要能被诊断抽屉依赖到 —— 普通函数只在抽屉
   * 恰好因别的响应式数据重渲染时才被重新求值，数值会滞后甚至定格（这个坑在
   * `playerDebugState` 的注释里已经踩过一次）。
   */
  const playbackHealth = computed(() => {
    const totalTimed = onTimeChunks.value + lateChunks.value
    const avgSegmentBytes = Math.round(avgSegmentBytesOf())

    const edges = requester.edgeStatList().map((stat) => {
      const member = members.value.find((m) => m.id === stat.peerId)
      const rttMs = perPeerRttMs(stat.peerId)
      const expectedDeliveryMs = Math.round(expectedDeliveryMsOf(stat.peerId))
      return {
        peerId: stat.peerId,
        label: member?.displayName || stat.peerId.slice(0, 6),
        primary: stat.peerId === topology.primaryId.value,
        rateBps: Math.round(stat.rateBps),
        peakRateBps: Math.round(stat.peakRateBps),
        rttMs,
        /**
         * 这条边此刻实际用的请求超时（T3-1 的公式结果）：
         * clamp(500ms, 10s, max(3×RTT, 2×预计传输时间))。
         */
        timeoutMs: deriveRequestTimeoutMs(rttMs, { expectedDeliveryMs }),
        /** 按该边实测速率传完一个平均分片的预计耗时：超时阈值的另一半输入。 */
        expectedDeliveryMs,
        deliveries: stat.deliveries,
        timeouts: stat.timeouts,
        samples: stat.samples,
      }
    })

    const parents = topology.parents.value
    const unassigned = !isHost.value && parents.length === 0
    return {
      onTimeRate: totalTimed > 0 ? onTimeChunks.value / totalTimed : -1,
      onTimeChunks: onTimeChunks.value,
      lateChunks: lateChunks.value,
      /** 卡顿次数：信号是 <video> 的 waiting 事件（播放头到了却没有可播数据）。 */
      stallCount: player.stalls.value,
      /** 因超时换父的次数（T3）：它涨说明"坏父"被绕过了。 */
      timeoutFailovers: timeoutFailovers.value,
      /** 当前被暂时降权的父节点数 / 累计降权次数。 */
      avoidedParents: topology.avoidedParents(),
      avoidEvents: topology.avoidEvents.value,
      /** 当前在途上限（T2-2 的推导值）与此刻实际在途请求数。 */
      inflight: inflightLimit.value,
      inflightNow: requester.pendingCount(),
      /** 实测边速率合计（Bytes/s）与平均分片字节数：在途上限就是由它们推出来的。 */
      edgeRateBps: Math.round(requester.totalEdgeRateBps()),
      avgSegmentBytes,
      edges,
      /** 自身深度与房间规模（"跳数"的可观测面）。 */
      depth: depth.value,
      members: members.value.length,
      parents,
      unassigned,
      /** 入站分片的内容校验（F-11）：坏片计数是"有对端在发替换内容"的直接证据。 */
      hashMismatches: hashMismatches.value,
      hashVerified: hashVerified.value,
      hashSkipped: hashSkipped.value,
      unassignedText: !unassigned
        ? ''
        : topology.mode.value === 'pending'
          ? '等待服务端分配上游（尚未下发分配）'
          : '未安置：当前房间已满 / 没有可用父节点（服务端没有给这个节点分配父节点）',
    }
  })

  /** IPv6 直连的地址族诊断：全局 IPv6 是否真的拿到、有没有候选被过滤掉。 */
  function ipv6Diagnostics() {
    return rtc.ipv6State()
  }

  function selectedPairInfo() {
    return rtc.selectedPair.value
  }

  return {
    // 房间
    connection,
    joined,
    roomId,
    clientId,
    displayName,
    role,
    isHost,
    hostId,
    self,
    depth,
    primaryId,
    parentName,
    members,
    chat,
    capacity,
    iceServers,
    lastError,
    roomClosed,
    needsGesture,
    // 断线恢复（缺陷 1）：主播离线等待态、观众等主播重建、不可恢复文案
    hostGraceSeconds: HOST_GRACE_SECONDS,
    hostOffline,
    hostOfflineText,
    hostOfflineSecondsLeft,
    resumeNotice,
    roomUnrecoverable,
    rebuildState,
    clientIdRotations,
    // 媒体
    mediaIndex,
    mediaError,
    mediaLoading: media.loading,
    videoReady,
    videoElement: player.video,
    playerError: player.error,
    // 同步与网络
    playback,
    drift,
    offsetMs: clock.offsetMs,
    syncMode,
    syncResets,
    chunkErrors,
    /** 入站分片内容校验（F-11）的三个计数。 */
    hashMismatches,
    hashVerified,
    hashSkipped,
    /**
     * 只读：某个分片当前是否在仓库里。
     * 给 F-11 的确定性验收用（"坏片没有落库"必须能直接读到，不能只靠计数推断）。
     */
    hasChunk: (index: number) => chunkStore.has(index),
    fetchFailures,
    serveLog,
    bufferedAhead,
    peerCount,
    // 注意：必须是函数。Pinia setup store 返回对象里的普通值只在创建时求值一次，
    // 直接写 player.debugState() 会永远返回"刚创建时"的状态 —— 那会把人带偏。
    gated,
    gateReason,
    gateBufferedSec,
    gateThresholdSegments,
    gateBufferedSegments,
    gateWaitedSec,
    // 滞后策略（卡顿 → 换路 → 追赶 → 跳转）的观测量
    lagSec,
    lagNotice,
    hopOffsetMs: clock.hopOffsetMs,
    parentOffsetMs: clock.parentOffsetMs,
    // 必须是函数：普通对象只在创建时求值一次，验收会永远读到 0。
    relayStats: () => ({ ...relayStats }),
    playerDebugState: () => player.debugState(),
    lifecycle: storeLifecycle,
    topologyAssignment: topology.assignment,
    topologyMode: topology.mode,
    topologyDepth: topology.depth,
    topologyReason: topology.reason,
    primaryParentId: topology.primaryId,
    backupParentIds: topology.backupIds,
    /**
     * 取数候选父节点（主父在前、备用父在后）。
     *
     * 与 primaryParentId 的区别：**长度为 0 才是"未安置"** ——
     * 服务端安置不下时下发的主父是空串，光看 primaryParentId 分不清
     * "还没下发"与"下发了一个空分配"。
     */
    parentIds: topology.parents,
    childrenIds: topology.children,
    distributorId: topology.distributorId,
    lastDistributorChange,
    peers: rtc.peers,
    uploadCapacityBps,
    // ---- 播放健康度（T4）与调度观测面 ----
    playbackHealth,
    inflightLimit,
    timeoutFailovers,
    edgeRateBps: () => Math.round(requester.totalEdgeRateBps()),
    edgeStats: () => requester.edgeStatList(),
    parentRttMs: (peerId: string) => perPeerRttMs(peerId),
    requestTimeoutMs: (peerId: string) => deriveRequestTimeoutMs(perPeerRttMs(peerId)),
    // ---- ICE 配置 TTL 与 IPv6 直连（打洞优化 ②③）----
    iceServerUrls: () => iceConfig.snapshot().serverUrls,
    iceDiagnostics,
    ipv6Diagnostics,
    selectedPairInfo,
    iceRestartCount: rtc.restartCount,
    iceRestartLog: rtc.restartLog,
    initiatedPeerCount: rtc.initiatedCount,
    iceRefreshCount: iceConfig.refreshCount,
    iceChangeCount: iceConfig.changeCount,
    iceLastRefreshReason: iceConfig.lastRefreshReason,
    iceSecondsUntilExpiry: () => iceConfig.secondsUntilExpiry(),
    delivered: requester.delivered,
    timedOut: requester.timedOut,
    failedRequests: requester.failed,
    p95DeliveryMs: requester.p95DeliveryMs,
    rejectedProgress: clock.rejected,
    clockReady: clock.ready,
    // 动作
    enterRoom,
    leaveRoom,
    publishMediaDirectory,
    publishMediaFiles,
    setVideoElement,
    setIceServers,
    applyIceResponse,
    sendChat,
    play,
    pause,
    seekTo,
    setRate,
    resumeAfterGesture,
    reportMetrics,
    /**
     * 验收钩子（仅调试用）：强制掐断信令 WS，模拟网络抖动/刷新/服务端重启。
     * 走的是真实的"断线 → 退避重连"路径，不做任何状态伪造。
     */
    dropSignaling: () => {
      noteLifecycle('验收钩子：强制掐断信令连接')
      signaling.drop()
    },
    /**
     * 验收钩子（仅调试用）：按住/放开信令。
     * 按住 = 断开当前连接 + 之后的连接尝试一律挡下（退避链照常推进），
     * 等价于"这个页面连不上信令服务"，用于确定性复现"断线超过宽限期"。
     */
    holdSignaling: (hold: boolean) => {
      noteLifecycle(`验收钩子：${hold ? '按住' : '放开'}信令`)
      signaling.setHold(hold)
    },
    setUploadThrottle: (bps: number) => {
      uploadThrottleBps = bps
      throttleAllowance = 0
      throttleLast = performance.now()
    },
    dismissError: () => {
      lastError.value = ''
    },
  }
})




















