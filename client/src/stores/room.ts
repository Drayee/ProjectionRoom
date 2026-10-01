import { computed, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import { useSignaling } from '../composables/useSignaling'
import { useMediaIndex } from '../composables/useMediaIndex'
import { useChunkStore } from '../composables/useChunkStore'
import { encodeFrame, useChunkRequester } from '../composables/useChunkRequester'
import { useChunkPlayer } from '../composables/useChunkPlayer'
import { useSyncClock } from '../composables/useSyncClock'
import { useWebRTC } from '../composables/useWebRTC'
import { segmentIndexAt } from '../types/media'
import { Action, FrameType, T } from '../types/protocol'
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
  const bufferedAhead = ref(0)
  const videoEl = shallowRef<HTMLVideoElement | null>(null)

  /** 计时纪元：主播页面每次加载生成一个，随进度下发（C13）。 */
  const clockEpoch = ref(newClockEpoch())

  let progressSeq = 0
  let nextAppend = 1
  let initRequested = false
  /** 跳转后必须先取到的分片序号；取到之前调度器会一直优先补取它。 */
  let requiredSegment: number | null = null
  let lastHardSeekAt = 0
  let lastHardSeekTarget = -1
  let lastConnectAttemptAt = 0

  let schedulerTimer: number | undefined
  let syncTimer: number | undefined
  let progressTimer: number | undefined
  let metricsTimer: number | undefined

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
    onBinary: handlePeerBinary,
    onOpen: (peerId) => {
      // 主播给新连上的观众补齐当前播放状态与时钟锚点。
      if (isHost.value) {
        sendProgressTo(peerId)
        clockStoreSendTimeSync(peerId)
        return
      }
      // 观众连上主播后立刻开始拉分片。
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
    if (msg.t === 'progress' || msg.t === 'time-sync') {
      if (isHost.value) return
      if (typeof msg.currentTime === 'number' && typeof msg.hostClockMs === 'number') {
        const accepted = clock.onProgress({
          currentTime: msg.currentTime,
          hostClockMs: msg.hostClockMs,
          clockEpoch: msg.clockEpoch,
          paused: Boolean(msg.paused),
          rate: msg.rate ?? 1,
          seq: msg.seq ?? 0,
        })
        if (accepted) void applyPlayback(clock.playback.value)
      }
    }
  }

  function handlePeerBinary(peerId: string, data: ArrayBuffer) {
    const delivery = requester.handleBinary(peerId, data)
    if (!delivery) return
    if (delivery.type === FrameType.Init || delivery.index === 0) {
      chunkStore.putInit(delivery.payload)
    } else {
      chunkStore.put(delivery.index, delivery.payload)
    }
    flushOrdered()
  }

  /** 主播应答分片请求：本地读文件 → 先发控制消息再发二进制帧。 */
  async function serveRequest(peerId: string, msg: PeerControl) {
    if (!isHost.value || msg.idx === undefined) return
    const index = msg.idx

    if (!rtc.send(peerId, JSON.stringify({ t: 'chunk', rid: msg.rid, idx: index } satisfies PeerControl))) {
      return
    }

    const payload = await media.readChunk(index)
    if (!payload) {
      rtc.send(peerId, JSON.stringify({ t: 'err', rid: msg.rid, idx: index, code: 'NOT_FOUND' } satisfies PeerControl))
      return
    }

    rtc.send(peerId, encodeFrame(index === 0 ? FrameType.Init : FrameType.Media, index, payload))
  }

  // ---------- 分片调度（主播与观众共用）----------
  const requester = useChunkRequester({
    send: (peerId, data) => rtc.send(peerId, data),
  })

  async function pumpPrefetch() {
    const index = mediaIndex.value
    if (!index || !player.attached.value) return

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

    const time = player.video.value?.currentTime ?? 0
    const playheadSeg = chunkStore.hasInit() ? segmentIndexAt(index, time) : 1
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
        const delivery = await requester.request(hostId.value, index)
        if (index === 0 || delivery.type === FrameType.Init) {
          chunkStore.putInit(delivery.payload)
        } else {
          chunkStore.put(delivery.index, delivery.payload)
        }
      }
      flushOrdered()
    } catch {
      // 超时/失败：下一次 tick 会重新请求（M3 会在这里转投其他父节点）。
      chunkErrors.value += 1
    }
  }

  /** 严格按序号写入播放器：MSE 需要单调递增的时间戳，乱序 append 会报错。 */
  function flushOrdered() {
    const index = mediaIndex.value
    if (!index || !player.attached.value) return

    const init = chunkStore.getInit()
    if (init && !player.initAppended.value) {
      player.append(FrameType.Init, init)
    }

    while (chunkStore.has(nextAppend)) {
      const buf = chunkStore.get(nextAppend)
      if (!buf) break
      player.append(FrameType.Media, buf)
      nextAppend += 1
    }

    // 丢开已经播过的分片，避免长视频吃满内存。
    chunkStore.evictBefore(Math.max(1, nextAppend - 4))
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
    if (video.playbackRate !== state.rate && state.rate > 0) {
      video.playbackRate = state.rate
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

    const state = clock.playback.value
    if (!state.paused && video.paused && bufferedAhead.value > 0.3) {
      await tryPlay()
    }

    // 当前位置没有可播数据时一律不矫正：空 MediaSource 上的 seek 只会把 currentTime
    // 改成一个"空位置"，看起来像在播，实际什么都没缓冲（验证脚本正是靠这一点抓到的）。
    if (bufferedAhead.value <= 0) {
      syncMode.value = state.paused ? 'idle' : 'buffering'
      return
    }

    const result = clock.correction(video.currentTime)
    syncMode.value = result.mode

    switch (result.mode) {
      case 'rate':
        if (video.playbackRate !== result.rate) video.playbackRate = result.rate
        break
      case 'ok':
        if (video.playbackRate !== 1) video.playbackRate = 1
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

    const segIndex = segmentIndexAt(index, target)
    await player.clearBuffered()
    chunkStore.reset()
    initRequested = false
    nextAppend = segIndex
    requiredSegment = segIndex

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
    return {
      t: 'progress',
      currentTime: video.currentTime,
      hostClockMs: Math.round(performance.now()),
      clockEpoch: clockEpoch.value,
      paused: video.paused,
      rate: video.playbackRate,
      seq: progressSeq,
    }
  }

  function sendProgressTo(peerId: string) {
    const msg = buildProgress()
    if (msg) rtc.send(peerId, JSON.stringify(msg))
  }

  function clockStoreSendTimeSync(peerId: string) {
    rtc.send(
      peerId,
      JSON.stringify({
        t: 'time-sync',
        hostClockMs: Math.round(performance.now()),
        clockEpoch: clockEpoch.value,
        seq: progressSeq,
      } satisfies PeerControl),
    )
  }

  function tickProgress() {
    if (!isHost.value || !joined.value) return
    const msg = buildProgress()
    if (!msg) return
    rtc.broadcast(JSON.stringify(msg))
  }

  function tickTimeSync() {
    if (!isHost.value || !joined.value) return
    rtc.broadcast(
      JSON.stringify({
        t: 'time-sync',
        hostClockMs: Math.round(performance.now()),
        clockEpoch: clockEpoch.value,
        seq: progressSeq,
      } satisfies PeerControl),
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
      },
    })
  }

  // ---------- 循环 ----------
  function startLoops() {
    stopLoops()
    schedulerTimer = window.setInterval(() => void pumpPrefetch(), 200)
    syncTimer = window.setInterval(() => void tickSync(), 100)
    metricsTimer = window.setInterval(tickMetrics, 5000)
    if (isHost.value) {
      progressTimer = window.setInterval(() => {
        tickProgress()
        tickTimeSync()
      }, 500)
    }
    rtc.startStats()
  }

  function stopLoops() {
    for (const timer of [schedulerTimer, syncTimer, progressTimer, metricsTimer]) {
      if (timer !== undefined) window.clearInterval(timer)
    }
    schedulerTimer = undefined
    syncTimer = undefined
    progressTimer = undefined
    metricsTimer = undefined
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

  async function ensurePlayer(index: MediaIndex) {
    const el = videoEl.value
    if (!el) return
    if (player.attached.value) return

    try {
      await player.attach(el, index.mimeType)
    } catch (err) {
      mediaError.value = (err as Error).message
      return
    }

    chunkStore.reset()
    initRequested = false
    nextAppend = 1
    requiredSegment = null
    startLoops()
  }

  async function connectToHost(force = false) {
    if (isHost.value || !hostId.value) return
    if (!force && rtc.peerCount() > 0) return
    try {
      await rtc.connect(hostId.value)
    } catch (err) {
      lastError.value = `连接主播失败：${(err as Error).message}`
    }
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
    if (joined.value) signaling.send({ type: T.Leave })
    stopLoops()
    requester.reset()
    rtc.closeAll()
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

  /** 主播选择分片目录并开播。 */
  async function publishMediaDirectory(files: FileList) {
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

  function setVideoElement(el: HTMLVideoElement | null) {
    videoEl.value = el
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
    bufferedAhead,
    peerCount,
    peers: rtc.peers,
    uploadCapacityBps,
    delivered: requester.delivered,
    timedOut: requester.timedOut,
    p95DeliveryMs: requester.p95DeliveryMs,
    rejectedProgress: clock.rejected,
    clockReady: clock.ready,
    // 动作
    enterRoom,
    leaveRoom,
    publishMediaDirectory,
    setVideoElement,
    setIceServers,
    sendChat,
    play,
    pause,
    seekTo,
    setRate,
    resumeAfterGesture,
    dismissError: () => {
      lastError.value = ''
    },
  }
})
