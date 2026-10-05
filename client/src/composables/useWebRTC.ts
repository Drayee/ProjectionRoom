import { ref } from 'vue'
import { MEDIA_HEADER_BYTES, decodeFrame, encodeControl, encodeMediaFrame, type Bytes, type DecodedMedia } from '../types/codec'
import type { PeerControl, SignalPayload } from '../types/protocol'

export interface PeerRuntime {
  id: string
  connection: RTCPeerConnectionState
  channelOpen: boolean
  rttMs: number
}

const PING_INTERVAL_MS = 3000
const STATS_INTERVAL_MS = 5000

/**
 * WebRTC 连接管理（M2 为星形：观众连主播，主播被动接客）。
 *
 * 三个刻意的设计：
 *   1. DataChannel 用 negotiated + 固定 id=0：两端各自创建同一条通道，
 *      不需要 ondatachannel 协商，也就不会出现"谁先建通道"的竞态。
 *   2. ICE candidate 早于 remoteDescription 到达时必须缓存，否则 addIceCandidate 会抛错并丢掉候选。
 *   3. 上行容量用 getStats().availableOutgoingBitrate，并且是**对所有 peer 求和**：
 *      每条 transport 的估计只反映它分到的那份带宽，N 个观众各估到 U/N，求和才还原出主播的总上行（C11）。
 */
export function useWebRTC(opts: {
  iceServers: () => RTCIceServer[]
  sendSignal: (to: string, payload: SignalPayload) => void
  onControl: (peerId: string, msg: PeerControl) => void
  onMedia: (peerId: string, media: DecodedMedia) => void
  onOpen?: (peerId: string) => void
  onClose?: (peerId: string) => void
}) {
  const peers = ref(new Map<string, PeerRuntime>())
  const uploadCapacityBps = ref(0)

  const pcs = new Map<string, RTCPeerConnection>()
  const channels = new Map<string, RTCDataChannel>()
  const pendingCandidates = new Map<string, RTCIceCandidateInit[]>()
  const pingTimers = new Map<string, number>()
  /** 正在重组的分片（键 `<peerId>#<chunkIndex>`）：高码率素材的大分片会走这里。 */
  const reassembly = new Map<string, { kind: number; parts: Bytes[]; bytes: number; next: number }>()

  let statsTimer: number | undefined

  function ensure(peerId: string): RTCPeerConnection {
    const existing = pcs.get(peerId)
    if (existing) {
      return existing
    }

    const pc = new RTCPeerConnection({ iceServers: opts.iceServers() })
    pcs.set(peerId, pc)
    peers.value.set(peerId, { id: peerId, connection: pc.connectionState, channelOpen: false, rttMs: 0 })

    const channel = pc.createDataChannel('pr', { negotiated: true, id: 0, ordered: true, protocol: 'pr/1' })
    setupChannel(peerId, channel)

    pc.onicecandidate = (event) => {
      if (event.candidate) {
        opts.sendSignal(peerId, { kind: 'candidate', candidate: event.candidate.toJSON() })
      }
    }

    pc.onconnectionstatechange = () => {
      const runtime = peers.value.get(peerId)
      if (runtime) {
        runtime.connection = pc.connectionState
      }
      if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
        close(peerId)
      }
    }

    return pc
  }

  function setupChannel(peerId: string, channel: RTCDataChannel) {
    channels.set(peerId, channel)
    channel.binaryType = 'arraybuffer'

    channel.onopen = () => {
      const runtime = peers.value.get(peerId)
      if (runtime) {
        runtime.channelOpen = true
      }
      startPing(peerId, channel)
      opts.onOpen?.(peerId)
    }

    channel.onclose = () => {
      const runtime = peers.value.get(peerId)
      if (runtime) {
        runtime.channelOpen = false
      }
      stopPing(peerId)
      opts.onClose?.(peerId)
    }

    channel.onmessage = (event) => {
      if (!(event.data instanceof ArrayBuffer)) {
        return
      }
      // DataChannel 上控制消息（protobuf）与分片帧都是二进制，
      // 由 1 字节 kind 前缀区分，见 types/codec.ts。
      const decoded = decodeFrame(new Uint8Array(event.data))
      if (!decoded) {
        return
      }
      if (decoded.control) {
        handleControl(peerId, decoded.control)
        return
      }
      if (decoded.media) {
        const media = reassemble(peerId, decoded.media)
        if (media) {
          opts.onMedia(peerId, media)
        }
      }
    }
  }

  /**
   * 接收端重组：把被切开的分片拼回一个完整 chunk。
   *
   * 单帧（未分片）走零拷贝快路径，直接交给上层 —— 低码率素材的绝大多数分片都在这里。
   * 只有真的要重组时才发生一次内存拷贝（高码率素材的大分片）。
   */
  function reassemble(peerId: string, media: DecodedMedia): DecodedMedia | null {
    const key = `${peerId}#${media.chunkIndex}`

    if (media.fragmentIndex === 0 && !media.more) {
      reassembly.delete(key)
      return media
    }

    const pending = reassembly.get(key) ?? { kind: media.kind, parts: [] as Bytes[], bytes: 0, next: 0 }
    // 通道是 ordered 的，正常不会乱序；真乱序就丢弃这一轮，等上层超时重取。
    if (media.fragmentIndex !== pending.next) {
      reassembly.delete(key)
      return null
    }
    pending.parts.push(media.payload)
    pending.bytes += media.payload.length
    pending.next += 1

    // 防御：单个 chunk 重组上限 32MB（正常最大也就几 MB），避免异常输入吃内存。
    if (pending.bytes > 32 * 1024 * 1024) {
      reassembly.delete(key)
      return null
    }
    if (media.more) {
      reassembly.set(key, pending)
      return null
    }

    reassembly.delete(key)
    const merged = new Uint8Array(pending.bytes)
    let offset = 0
    for (const part of pending.parts) {
      merged.set(part, offset)
      offset += part.length
    }
    return { kind: pending.kind, chunkIndex: media.chunkIndex, fragmentIndex: 0, more: false, payload: merged }
  }

  function handleControl(peerId: string, msg: PeerControl) {
    if (msg.t === 'ping') {
      send(peerId, encodeControl({ t: 'pong', ts: msg.ts }))
      return
    }
    if (msg.t === 'pong') {
      const runtime = peers.value.get(peerId)
      if (runtime && typeof msg.ts === 'number') {
        runtime.rttMs = Math.round(performance.now() - msg.ts)
      }
      return
    }

    opts.onControl(peerId, msg)
  }

  function startPing(peerId: string, channel: RTCDataChannel) {
    stopPing(peerId)
    const timer = window.setInterval(() => {
      if (channel.readyState === 'open') {
        try {
          channel.send(encodeControl({ t: 'ping', ts: performance.now() }))
        } catch {
          stopPing(peerId)
        }
      }
    }, PING_INTERVAL_MS)
    pingTimers.set(peerId, timer)
  }

  function stopPing(peerId: string) {
    const timer = pingTimers.get(peerId)
    if (timer !== undefined) {
      window.clearInterval(timer)
      pingTimers.delete(peerId)
    }
  }

  /** 主动发起连接（观众侧调用）。 */
  async function connect(peerId: string): Promise<void> {
    const pc = ensure(peerId)
    if (pc.signalingState !== 'stable') {
      return
    }
    const offer = await pc.createOffer()
    // 期间可能已经收到对方的 offer（glare）：这时必须放弃自己这份，别把状态机搞坏。
    if (pc.signalingState !== 'stable') {
      return
    }
    await pc.setLocalDescription(offer)
    opts.sendSignal(peerId, { kind: 'offer', sdp: pc.localDescription?.sdp ?? offer.sdp ?? '' })
  }

  async function flushCandidates(peerId: string, pc: RTCPeerConnection) {
    const queued = pendingCandidates.get(peerId)
    if (!queued || queued.length === 0) {
      return
    }
    pendingCandidates.delete(peerId)
    for (const candidate of queued) {
      try {
        await pc.addIceCandidate(candidate)
      } catch {
        // 候选过期就丢掉，连接可以靠其余候选建立。
      }
    }
  }

  async function handleSignal(from: string, payload: SignalPayload) {
    const pc = ensure(from)

    switch (payload.kind) {
      case 'offer': {
        if (!payload.sdp) return
        // Glare：双方同时发 offer 时，先回滚自己那份，永远让位于收到的 offer。
        // 没有这一步会在 setLocalDescription / setRemoteDescription 上抛
        // "Called in wrong state: have-remote-offer"，连接就再也建不起来。
        if (pc.signalingState === 'have-local-offer') {
          try {
            await pc.setLocalDescription({ type: 'rollback' } as RTCLocalSessionDescriptionInit)
          } catch {
            // 回滚失败就继续尝试设置远端描述，失败会被上层捕获。
          }
        }
        await pc.setRemoteDescription({ type: 'offer', sdp: payload.sdp })
        await flushCandidates(from, pc)
        const answer = await pc.createAnswer()
        await pc.setLocalDescription(answer)
        opts.sendSignal(from, { kind: 'answer', sdp: pc.localDescription?.sdp ?? answer.sdp ?? '' })
        return
      }
      case 'answer': {
        if (!payload.sdp || pc.signalingState === 'stable') return
        await pc.setRemoteDescription({ type: 'answer', sdp: payload.sdp })
        await flushCandidates(from, pc)
        return
      }
      case 'candidate': {
        if (!payload.candidate) return
        // remoteDescription 还没设好时先缓存，否则 addIceCandidate 会抛错并丢掉这个候选。
        if (!pc.remoteDescription) {
          const queued = pendingCandidates.get(from) ?? []
          queued.push(payload.candidate)
          pendingCandidates.set(from, queued)
          return
        }
        try {
          await pc.addIceCandidate(payload.candidate)
        } catch {
          // 同上：单个候选失败不该让整条连接失败。
        }
        return
      }
      default:
        return
    }
  }

  /** 发送一条已编码的二进制帧（控制消息或分片帧）。 */
  function send(peerId: string, data: Bytes): boolean {
    const channel = channels.get(peerId)
    if (!channel || channel.readyState !== 'open') {
      return false
    }
    try {
      channel.send(data)
      return true
    } catch {
      return false
    }
  }

  /**
   * 单个 DataChannel 消息里能装多少分片数据。
   *
   * 上限来自 SCTP 协商（Chrome↔Chrome 是 256KiB，规范默认 64KiB）。
   * 这里取 64KiB 封顶：分片小一点、条数多一点，跨浏览器更稳，进度也更平滑。
   */
  function fragmentPayloadSize(channel: RTCDataChannel): number {
    // maxMessageSize 是较新的属性，TS 的 lib.dom 还没有它；老浏览器上取不到就用规范默认值。
    const negotiated = (channel as RTCDataChannel & { maxMessageSize?: number }).maxMessageSize
    const limit = typeof negotiated === 'number' && negotiated > 0 ? negotiated : 65535
    return Math.max(4096, Math.min(limit, 65536) - MEDIA_HEADER_BYTES)
  }

  /** 背压：缓冲积压太多就先等它排空，否则 send() 会直接抛（消息发不出去）。 */
  async function waitForDrain(channel: RTCDataChannel, fragmentBytes: number): Promise<void> {
    const limit = Math.max(4 * fragmentBytes, 262144)
    const deadline = performance.now() + 5000
    while (channel.bufferedAmount > limit && channel.readyState === 'open' && performance.now() < deadline) {
      await new Promise((resolve) => window.setTimeout(resolve, 10))
    }
  }

  /**
   * 发送一个分片（可能被切成多条 DataChannel 消息）。
   *
   * 为什么必须切：DataChannel 单条消息超过协商上限时 `send()` 会**抛异常**。
   * 高码率素材的单个分片可以到 1–2MB（比如一个大 I 帧），
   * 于是"整片发不出去 → 观众一直缓冲 → 却什么都不报"。
   */
  async function sendMedia(peerId: string, kind: number, chunkIndex: number, payload: Uint8Array): Promise<boolean> {
    const channel = channels.get(peerId)
    if (!channel || channel.readyState !== 'open') {
      return false
    }

    const size = fragmentPayloadSize(channel)
    const total = Math.max(1, Math.ceil(payload.length / size))
    for (let i = 0; i < total; i += 1) {
      if (channel.readyState !== 'open') {
        return false
      }
      await waitForDrain(channel, size)
      const start = i * size
      const slice = payload.subarray(start, Math.min(payload.length, start + size))
      try {
        channel.send(encodeMediaFrame(kind, chunkIndex, slice, i, i < total - 1))
      } catch {
        return false
      }
    }
    return true
  }

  function broadcast(data: Bytes): number {
    let sent = 0
    for (const peerId of channels.keys()) {
      if (send(peerId, data)) {
        sent += 1
      }
    }
    return sent
  }

  /** 已建立数据通道的 peer 列表。 */
  function openChannels(): string[] {
    const open: string[] = []
    for (const [peerId, channel] of channels) {
      if (channel.readyState === 'open') {
        open.push(peerId)
      }
    }
    return open
  }

  function close(peerId: string) {
    stopPing(peerId)
    const channel = channels.get(peerId)
    if (channel) {
      channel.onopen = null
      channel.onclose = null
      channel.onmessage = null
      try {
        channel.close()
      } catch {
        // 忽略已关闭的通道。
      }
      channels.delete(peerId)
    }
    const pc = pcs.get(peerId)
    if (pc) {
      pc.onicecandidate = null
      pc.onconnectionstatechange = null
      try {
        pc.close()
      } catch {
        // 忽略重复关闭。
      }
      pcs.delete(peerId)
    }
    pendingCandidates.delete(peerId)
    peers.value.delete(peerId)
    opts.onClose?.(peerId)
  }

  function closeAll() {
    for (const peerId of [...pcs.keys()]) {
      close(peerId)
    }
    peers.value.clear()
    stopStats()
  }

  /** 每 5s 采集一次上行容量估算。取不到就保持 0 —— 让服务端继续显示 pending，而不是编一个数字。 */
  async function collectStats() {
    let sum = 0
    let seen = false

    for (const pc of pcs.values()) {
      if (pc.connectionState !== 'connected') {
        continue
      }
      try {
        const report = await pc.getStats()
        report.forEach((stat: any) => {
          if (
            stat.type === 'candidate-pair' &&
            stat.state === 'succeeded' &&
            stat.nominated &&
            typeof stat.availableOutgoingBitrate === 'number' &&
            stat.availableOutgoingBitrate > 0
          ) {
            sum += stat.availableOutgoingBitrate
            seen = true
          }
        })
      } catch {
        // 统计失败不影响播放。
      }
    }

    if (seen) {
      uploadCapacityBps.value = Math.round(sum)
    }
  }

  function startStats(intervalMs = STATS_INTERVAL_MS) {
    stopStats()
    statsTimer = window.setInterval(() => {
      void collectStats()
    }, intervalMs)
  }

  function stopStats() {
    if (statsTimer !== undefined) {
      window.clearInterval(statsTimer)
      statsTimer = undefined
    }
  }

  function averageRttMs(): number {
    const values = [...peers.value.values()].filter((p) => p.channelOpen).map((p) => p.rttMs)
    if (values.length === 0) {
      return 0
    }
    return Math.round(values.reduce((a, b) => a + b, 0) / values.length)
  }

  return {
    peers,
    uploadCapacityBps,
    connect,
    handleSignal,
    send,
    sendMedia,
    broadcast,
    openChannels,
    close,
    closeAll,
    startStats,
    stopStats,
    collectStats,
    averageRttMs,
    peerCount: () => pcs.size,
  }
}


