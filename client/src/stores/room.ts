import { computed, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import { useSignaling } from '../composables/useSignaling'
import { useMediaIndex } from '../composables/useMediaIndex'
import { useChunkStore } from '../composables/useChunkStore'
import { useChunkRequester } from '../composables/useChunkRequester'
import { useChunkPlayer } from '../composables/useChunkPlayer'
import { useSyncClock } from '../composables/useSyncClock'
import { useWebRTC } from '../composables/useWebRTC'
import { useTopology } from '../composables/useTopology'
import { segmentIndexAt } from '../types/media'
import { KIND_INIT, KIND_MEDIA, encodeControl, type DecodedMedia } from '../types/codec'
import { Action, T } from '../types/protocol'
import type { MediaIndex } from '../types/media'
import type {
  Capacity,
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
}

/** 观众预取窗口（分片数）。2s/片 ≈ 60s 缓冲，是抖动的缓冲池。 */
const PREFETCH_WINDOW = 30
/** 主播本地读取，窗口不需要那么大。 */
const HOST_WINDOW = 8
/** 同时在途的远程请求上限。 */
const MAX_INFLIGHT = 4
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

export function newClientId(): string {
  const rand = crypto.randomUUID?.() ?? Math.random().toString(36).slice(2)
  return `c_${rand.replace(/-/g, '').slice(0, 16)}`
}

function newClockEpoch(): string {
  return `e_${Math.random().toString(36).slice(2, 10)}`
}

export const useRoomStore = defineStore('room', () => {
  // ---------- 房间状态 ----------
  const credentials = ref<JoinCredentials | null>(null)
  const joined = ref(false)
  const members = ref<MemberInfo[]>([])
  const chat = ref<ChatMessage[]>([])
  const capacity = ref<Capacity | null>(null)
  const hostId = ref('')
  const lastError = ref('')
  const roomClosed = ref('')
  const iceServers = ref<RTCIceServer[]>([])
  const needsGesture = ref(false)

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
  /** 取数失败与应答情况的环形日志：排障时先看这两个（以前失败是静默的）。 */
  const fetchFailures = ref<string[]>([])
  const serveLog = ref<string[]>([])
  const bufferedAhead = ref(0)
  const videoEl = shallowRef<HTMLVideoElement | null>(null)

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
        hostId.value = env.hostId ?? ''
        members.value = env.members ?? []
        capacity.value = env.capacity ?? null
        if (env.playback) {
          clock.onAnchor(toSample(env.playback))
        }
        if (env.mediaIndex) {
          void applyRemoteMediaIndex(env.mediaIndex)
        } else if (!isHost.value && hostId.value) {
          void connectToHost()
        }
        if (env.topology) {
          void topology.apply(env.topology)
        }
        break

      case T.MemberJoined:
      case T.MemberLeft:
      case T.MemberList:
        if (env.members) members.value = env.members
        break

      case T.MediaIndex:
        if (env.mediaIndex) void applyRemoteMediaIndex(env.mediaIndex)
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
        if (env.topology) void topology.apply(env.topology)
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
          void applyPlayback(env.playback, env.action)
        }
        break

      case T.RoomClosed:
        joined.value = false
        roomClosed.value = env.message ?? '房间已关闭'
        stopLoops()
        break

      case T.Error:
        lastError.value = ERROR_TEXT[env.code ?? ''] ?? env.message ?? '未知错误'
        break

      default:
        break
    }
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
    })
  }

  // ---------- Peer 消息 ----------
  function handlePeerControl(peerId: string, msg: PeerControl) {
    if (msg.t === 'req') {
      void serveRequest(peerId, msg)
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
          void applyPlayback(clock.playback.value)
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
    const delivery = requester.handleMedia(peerId, media)
    if (delivery.index === 0 || delivery.kind === KIND_INIT) {
      chunkStore.putInit(delivery.payload)
    } else {
      chunkStore.put(delivery.index, delivery.payload)
    }
    flushOrdered()
  }

  /** 主播应答分片请求：本地读文件 → 先发控制消息再发二进制帧。 */
  async function serveRequest(peerId: string, msg: PeerControl) {
    if (msg.idx === undefined) return
    const index = msg.idx

    if (!rtc.send(peerId, encodeControl({ t: 'chunk', rid: msg.rid, idx: index }))) {
      noteServe(`分片 ${index}: 通道不可用，未受理`)
      return
    }

    // 主播从本地文件读；转发节点从自己已经收到的分片里取（SPEC §6.4 的中继职责）。
    let payload: Uint8Array<ArrayBuffer> | null = null
    if (isHost.value) {
      payload = await media.readChunk(index)
    } else if (index === 0) {
      payload = chunkStore.getInit()
    } else {
      payload = chunkStore.get(index)
    }
    if (!payload) {
      // 这条以前是静默的：观众只会"一直缓冲"，谁也看不出是本地没有这一片。
      noteServe(`分片 ${index}: 本地没有这一片（NOT_FOUND）`)
      rtc.send(peerId, encodeControl({ t: 'err', rid: msg.rid, idx: index, code: 'NOT_FOUND' }))
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

  const requester = useChunkRequester({
    send: (peerId, data) => rtc.send(peerId, data),
  })

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
      if (!host && requester.pendingCount() >= MAX_INFLIGHT) break
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
      } else if (host || requester.pendingCount() < MAX_INFLIGHT) {
        void fetchChunk(requiredSegment)
      }
    }
  }

  async function fetchChunk(index: number) {
    try {
      if (isHost.value) {
        const payload = await media.readChunk(index)
        if (!payload) return
        if (index === 0) {
          chunkStore.putInit(payload)
        } else {
          chunkStore.put(index, payload)
        }
      } else {
        const peerId = topology.pickParent(index)
        if (!peerId) {
          chunkErrors.value += 1
          noteFetchFailure(`分片 ${index}: 没有可用父节点`)
          return
        }
        topology.noteAttempt(index)
        const delivery = await requester.request(peerId, index)
        topology.noteDelivered(index)
        if (index === 0 || delivery.kind === KIND_INIT) {
          chunkStore.putInit(delivery.payload)
        } else {
          chunkStore.put(delivery.index, delivery.payload)
        }
      }
      flushOrdered()
    } catch (err) {
      // 超时/失败：下一次 tick 会重新请求（M3 会在这里转投其他父节点）。
      // 但**必须留痕**：以前这里是空的 catch，于是"一片都没成功"在界面上完全看不出来。
      chunkErrors.value += 1
      noteFetchFailure(`分片 ${index}: ${(err as Error).message ?? '未知失败'}`)
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
    if (isHost.value) return

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
    chunkStore.reset()
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
    mediaIndex.value = index
    await ensurePlayer(index)
    if (!isHost.value) {
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

    chunkStore.reset()
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

    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const url = `${proto}://${window.location.host}/ws?roomId=${encodeURIComponent(creds.roomId)}&clientId=${encodeURIComponent(creds.clientId)}`
    signaling.connect(url)
  }

  function leaveRoom() {
    noteLifecycle('leaveRoom')
    if (joined.value) signaling.send({ type: T.Leave })
    stopLoops()
    requester.reset()
    rtc.closeAll()
    topology.reset()
    player.detach()
    chunkStore.reset()
    media.reset()
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
    signaling.send({
      type: T.RoomControl,
      action,
      currentTime: patch.currentTime ?? video?.currentTime ?? 0,
      paused: patch.paused ?? video?.paused ?? true,
      rate: patch.rate ?? video?.playbackRate ?? 1,
      hostClockMs: Math.round(performance.now()),
      clockEpoch: clockEpoch.value,
    })
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

  function setIceServers(servers: RTCIceServer[]) {
    iceServers.value = servers
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
    childrenIds: topology.children,
    distributorId: topology.distributorId,
    lastDistributorChange,
    peers: rtc.peers,
    uploadCapacityBps,
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
    sendChat,
    play,
    pause,
    seekTo,
    setRate,
    resumeAfterGesture,
    reportMetrics,
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




















