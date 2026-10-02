import { ref } from 'vue'
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
  onControl: (peerId: string, msg: PeerControl, raw: string) => void
  onBinary: (peerId: string, data: ArrayBuffer) => void
  onOpen?: (peerId: string) => void
  onClose?: (peerId: string) => void
}) {
  const peers = ref(new Map<string, PeerRuntime>())
  const uploadCapacityBps = ref(0)

  const pcs = new Map<string, RTCPeerConnection>()
  const channels = new Map<string, RTCDataChannel>()
  const pendingCandidates = new Map<string, RTCIceCandidateInit[]>()
  const pingTimers = new Map<string, number>()

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
      if (typeof event.data === 'string') {
        handleControlText(peerId, event.data)
        return
      }
      if (event.data instanceof ArrayBuffer) {
        opts.onBinary(peerId, event.data)
      }
    }
  }

  function handleControlText(peerId: string, raw: string) {
    let msg: PeerControl
    try {
      msg = JSON.parse(raw) as PeerControl
    } catch {
      return
    }

    if (msg.t === 'ping') {
      send(peerId, JSON.stringify({ t: 'pong', ts: msg.ts } satisfies PeerControl))
      return
    }
    if (msg.t === 'pong') {
      const runtime = peers.value.get(peerId)
      if (runtime && typeof msg.ts === 'number') {
        runtime.rttMs = Math.round(performance.now() - msg.ts)
      }
      return
    }

    opts.onControl(peerId, msg, raw)
  }

  function startPing(peerId: string, channel: RTCDataChannel) {
    stopPing(peerId)
    const timer = window.setInterval(() => {
      if (channel.readyState === 'open') {
        try {
          channel.send(JSON.stringify({ t: 'ping', ts: performance.now() } satisfies PeerControl))
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

  function send(peerId: string, data: string | ArrayBuffer): boolean {
    const channel = channels.get(peerId)
    if (!channel || channel.readyState !== 'open') {
      return false
    }
    try {
      // 分开两个分支：RTCDataChannel.send 的重载无法从 string | ArrayBuffer 联合类型里推断。
      if (typeof data === 'string') {
        channel.send(data)
      } else {
        channel.send(data)
      }
      return true
    } catch {
      return false
    }
  }

  function broadcast(data: string | ArrayBuffer): number {
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


