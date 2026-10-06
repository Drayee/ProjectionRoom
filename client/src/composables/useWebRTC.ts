import { ref } from 'vue'
import { MEDIA_HEADER_BYTES, decodeFrame, encodeControl, encodeMediaFrame, type Bytes, type DecodedMedia } from '../types/codec'
import type { PeerControl, SignalPayload } from '../types/protocol'
import {
  classifyAddress,
  countFamily,
  emptyFamilyCounts,
  familyLabel,
  isCrossNetworkUsable,
  type CandidateFamily,
  type FamilyCounts,
} from '../utils/iceCandidate'

export interface PeerRuntime {
  id: string
  connection: RTCPeerConnectionState
  channelOpen: boolean
  rttMs: number
}

/**
 * 选中候选对（从 `getStats()` 的 `candidate-pair` 关联 local/remote-candidate 得到）。
 * 这是"IPv6 直连到底有没有生效"的**直接证据**：只有 family 是 v6-global 才叫生效。
 */
export interface SelectedPairInfo {
  peerId: string
  localType: string
  remoteType: string
  family: CandidateFamily
  remoteFamily: CandidateFamily
  protocol: string
  localAddress: string
  remoteAddress: string
  rttMs: number
}

/** 地址族诊断快照（诊断抽屉与验收脚本读它）。 */
export interface IceAddressState {
  hasGlobalLocal: boolean
  /** 真实连接的候选 ∪ 本地采样候选：本机到底有哪些可用的地址族。 */
  families: FamilyCounts
  remoteFamilies: FamilyCounts
  /** 被"不可跨网"判据挡下、没有发到信令里的候选条数（只统计真实连接）。 */
  filtered: number
  /** 被挡下的最近几条（证据：地址 + 地址族）。 */
  filteredSamples: string[]
  /** 本机观测到的候选样本（type 地址 族）。 */
  samples: string[]
  /**
   * host 候选的地址是否被 Chrome 的 mDNS 混淆藏起来了（表现为 `xxxx.local` 或空地址）。
   *
   * 这个标志是给读诊断的人用的：它解释了"选中候选对为什么显示 unknown" ——
   * 不是 IPv6 没生效，而是浏览器刻意不给地址。此时要判断全局 IPv6 是否存在，
   * 看的是 srflx 候选与独立采样。
   */
  mdnsHidden: boolean
  /**
   * 独立采样（一个不连对端的临时 PC）看到的候选。
   *
   * 为什么必须单独采一次：同局域网/同机场景下两个浏览器会靠 mDNS 的 host 候选**秒连**，
   * Chrome 随即结束 STUN 收集 —— 真实连接上就再也看不到全局 IPv6 的 srflx 候选了
   *（实测：v6Global 恒为 0，而同一台机器单独收集时稳定出现 2 条 v6 srflx）。
   * 采样的用途正是"在依赖 IPv6 之前，先知道本机有没有可跨网的 IPv6"。
   */
  sampler: {
    runs: number
    families: FamilyCounts
    samples: string[]
  }
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
  /**
   * "我是发起方"（offer 由我发）的 peer。
   *
   * ICE restart 只能由发起方做：本仓库 offer 恒由下游子节点发（见 useTopology.apply →
   * connectTo → connect），所以发起方由**调用过 connect() 这件事**决定，
   * 而不是按角色推断 —— 中继节点同时是"某个父的子"和"别人的父"，按角色判必然判错。
   */
  const initiated = new Set<string>()

  // ---------- ICE 诊断（"IPv6 直连有没有生效"必须能看出来）----------
  /** 本机候选（含被过滤掉的）：键 `type|address|protocol`，值 = 地址族。 */
  const localCandidates = new Map<string, CandidateFamily>()
  /** 对端候选（服务端只转发，不经我们的过滤）：同上。 */
  const remoteCandidates = new Map<string, CandidateFamily>()
  /** 独立采样（临时 PC，不连对端）看到的本机候选。 */
  const samplerCandidates = new Map<string, CandidateFamily>()
  const samplerRuns = ref(0)
  let samplerRunning = false
  const filteredSamples = ref<string[]>([])
  const filteredCount = ref(0)
  const selectedPair = ref<SelectedPairInfo | null>(null)
  /** 真正发出去的 ICE restart 次数（验收判据：列表未变时必须不增长）。 */
  const restartCount = ref(0)
  const restartLog = ref<string[]>([])

  let statsTimer: number | undefined

  function ensure(peerId: string): RTCPeerConnection {
    const existing = pcs.get(peerId)
    if (existing) {
      return existing
    }

    const pc = new RTCPeerConnection({ iceServers: opts.iceServers() })
    pcs.set(peerId, pc)
    peers.value.set(peerId, { id: peerId, connection: pc.connectionState, channelOpen: false, rttMs: 0 })

    // 统计循环跟着"有连接"走，而不是跟着"播放器挂上了"走：
    // 没有媒体（纯信令调试、主播还没选片）时也必须能读到选中候选对与地址族，
    // 否则"IPv6 直连有没有生效"在最该看的时刻恰好是空的。
    startStats()

    const channel = pc.createDataChannel('pr', { negotiated: true, id: 0, ordered: true, protocol: 'pr/1' })
    setupChannel(peerId, channel)

    pc.onicecandidate = (event) => {
      if (!event.candidate) {
        return
      }
      const candidate = event.candidate
      const family = classifyAddress(candidate.address)
      const label = `${candidate.type ?? '?'} ${candidate.address ?? '（地址不可见）'} ${familyLabel(family)}`
      rememberCandidate(localCandidates, candidate)

      // 链路本地 IPv6 / ULA / 回环：**不可跨网**。这类候选发出去只会让对端
      // 在一个注定失败的候选对上耗时间（还要等它超时），所以直接不发。
      // 注意"判定不了"（Chrome 默认 mDNS 混淆下 host 候选的地址是 xxxx.local 或空串）
      // 时必须保留 —— 同局域网直连正是靠它。
      if (!isCrossNetworkUsable(family)) {
        filteredCount.value += 1
        filteredSamples.value = [...filteredSamples.value.slice(-4), label]
        return
      }

      opts.sendSignal(peerId, { kind: 'candidate', candidate: candidate.toJSON() })
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

  /** 记一条本机/对端候选（同一地址多次出现只算一条：候选会随重启重复上报）。 */
  function rememberCandidate(
    into: Map<string, CandidateFamily>,
    candidate: { type?: string | null; address?: string | null; protocol?: string | null },
  ) {
    const address = candidate.address ?? ''
    const key = `${candidate.type ?? '?'}|${address}|${candidate.protocol ?? '?'}`
    if (!into.has(key)) {
      into.set(key, classifyAddress(address))
    }
  }

  function countsOf(into: Map<string, CandidateFamily>): FamilyCounts {
    const counts = emptyFamilyCounts()
    for (const family of into.values()) {
      countFamily(counts, family)
    }
    return counts
  }

  /** 地址族诊断：`hasGlobalLocal` 只有在真的拿到**全局单播** IPv6 时才为真。 */
  function ipv6State(): IceAddressState {
    // 真实连接 ∪ 采样：单看真实连接会在"同机/同局域网秒连"时漏掉全局 IPv6（见 sampler 注释）。
    const merged = new Map<string, CandidateFamily>([...localCandidates, ...samplerCandidates])
    const families = countsOf(merged)
    const describe = (into: Map<string, CandidateFamily>, limit: number) =>
      [...into.entries()]
        .slice(-limit)
        .map(([key, family]) => {
          const [type, address, protocol] = key.split('|')
          return `${type}/${protocol} ${address || '（地址不可见）'} ${familyLabel(family)}`
        })
    return {
      hasGlobalLocal: families.v6Global > 0,
      families,
      remoteFamilies: countsOf(remoteCandidates),
      filtered: filteredCount.value,
      filteredSamples: filteredSamples.value,
      samples: describe(localCandidates, 8),
      mdnsHidden: [...localCandidates.keys()].some((key) => {
        const [type, address] = key.split('|')
        return type === 'host' && (address === '' || address.toLowerCase().endsWith('.local'))
      }),
      sampler: {
        runs: samplerRuns.value,
        families: countsOf(samplerCandidates),
        samples: describe(samplerCandidates, 8),
      },
    }
  }

  /**
   * 独立采一次本机候选：一个**不连对端**的临时 PC，用同一份 ICE 列表收集候选。
   *
   * 只观测，不发信令、不进 pcs（不参与连接），收集完就关掉。
   * 为什么要它：真实连接一旦靠 mDNS host 候选秒连，Chrome 就停止 STUN 收集，
   * 本机"有没有可跨网的全局 IPv6"就永远看不出来 —— 而那正是本次优化的前提。
   */
  async function sampleLocalCandidates(iceServers: RTCIceServer[], durationMs = 8000): Promise<void> {
    if (samplerRunning || iceServers.length === 0) {
      return
    }
    samplerRunning = true
    const pc = new RTCPeerConnection({ iceServers })
    try {
      pc.createDataChannel('pr-ice-sample')
      pc.onicecandidate = (event) => {
        if (event.candidate) {
          rememberCandidate(samplerCandidates, event.candidate)
        }
      }
      await pc.setLocalDescription(await pc.createOffer())
      await new Promise((resolve) => window.setTimeout(resolve, durationMs))
      samplerRuns.value += 1
    } catch {
      // 采样失败只是没有诊断数据，不影响任何连接。
    } finally {
      samplerRunning = false
      try {
        pc.close()
      } catch {
        // 忽略重复关闭。
      }
    }
  }

  /**
   * 把新的 ICE 列表推到**所有已存在**的 PC 上（不重建连接）。
   *
   * 为什么要 setConfiguration：ICE server 列表只在"下一次收集候选"时生效，
   * 已建立的 transport 不会自己换 —— 这一步是让父节点侧在下一次应答/收集时
   * 用上最新列表，而不是等它自己发现。
   */
  function applyIceConfiguration(servers: RTCIceServer[]): number {
    let applied = 0
    for (const pc of pcs.values()) {
      try {
        // 注意：setConfiguration 是**整份替换**。这里只给 iceServers 是有意的 ——
        // 本仓库的 PC 从来只用 `{ iceServers }` 构造，且 iceTransportPolicy 必须保持默认的
        // all（TURN 退役后不再需要 relay）。将来若给 pc 加了别的字段，必须在这里一起带上，
        // 否则会被静默清掉。
        pc.setConfiguration({ iceServers: servers })
        applied += 1
      } catch {
        // 单个 PC 更新失败不影响其它 PC：连接继续用手上的旧列表，不抛给上层。
      }
    }
    return applied
  }

  async function restartOne(peerId: string, pc: RTCPeerConnection, reason: string): Promise<boolean> {
    // 信令状态不是 stable 时（正在收对方的 offer / 等 answer）不动它：
    // 这里**不能**抛异常，restart 是"尽力而为"的优化，失败不该污染任何调用链。
    if (pc.signalingState !== 'stable') {
      restartLog.value = [...restartLog.value.slice(-4), `${Math.round(performance.now())} ${peerId.slice(0, 6)} 跳过（${pc.signalingState}）`]
      return false
    }
    try {
      if (typeof pc.restartIce === 'function') {
        pc.restartIce()
      }
      const offer = await pc.createOffer({ iceRestart: true })
      // 期间可能收到对方的新 offer（glare）：这时必须放弃自己这份，
      // 与 connect() 走的是同一条判据（回滚由 handleSignal 的 have-local-offer 分支负责）。
      if (pc.signalingState !== 'stable') {
        return false
      }
      await pc.setLocalDescription(offer)
      opts.sendSignal(peerId, { kind: 'offer', sdp: pc.localDescription?.sdp ?? offer.sdp ?? '' })
      restartCount.value += 1
      restartLog.value = [
        ...restartLog.value.slice(-4),
        `${Math.round(performance.now())} ${peerId.slice(0, 6)} ICE restart（${reason}）`,
      ]
      return true
    } catch (err) {
      restartLog.value = [
        ...restartLog.value.slice(-4),
        `${Math.round(performance.now())} ${peerId.slice(0, 6)} restart 失败 ${(err as Error).name}`,
      ]
      return false
    }
  }

  /**
   * 对"我是发起方"的连接触发 ICE restart（列表真的变了才调）。
   *
   * 父节点侧不在这里：它只要在 ensure()/收到 offer 时用最新列表，
   * 并在应答前完成自己的 setConfiguration。强制双方都 restart 会互相打架。
   */
  async function restartInitiators(reason: string): Promise<number> {
    const targets = [...pcs.entries()].filter(([peerId]) => initiated.has(peerId))
    if (targets.length === 0) {
      return 0
    }
    const results = await Promise.all(targets.map(([peerId, pc]) => restartOne(peerId, pc, reason)))
    return results.filter(Boolean).length
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
    // 记下"这一条是我发起的"：ICE restart 只有发起方能做（父节点侧不许主动 restart）。
    initiated.add(peerId)
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
        // 应答前先按**最新**的 ICE 列表刷新本 PC 的配置：父节点侧不主动 restart，
        // 但它必须保证"这次应答（可能是子节点的 ICE restart offer）用的是最新列表"，
        // 否则父节点会用陈旧列表收集候选，子节点换列表就成了单边行为。
        // （与 applyIceConfiguration 同样的注意：只给 iceServers 就是这份配置的全部。）
        try {
          pc.setConfiguration({ iceServers: opts.iceServers() })
        } catch {
          // 列表非法就沿用旧配置：应答本身比换列表重要。
        }
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
    initiated.delete(peerId)
    peers.value.delete(peerId)
    opts.onClose?.(peerId)
  }

  function closeAll() {
    for (const peerId of [...pcs.keys()]) {
      close(peerId)
    }
    peers.value.clear()
    // 候选诊断是"本次房间会话"的观测：换房/重连后重新统计，避免把旧房间的地址族
    // 混进来当成新房间的证据。
    localCandidates.clear()
    remoteCandidates.clear()
    samplerCandidates.clear()
    samplerRuns.value = 0
    filteredCount.value = 0
    filteredSamples.value = []
    selectedPair.value = null
    restartCount.value = 0
    restartLog.value = []
    stopStats()
  }

  /**
   * 每 5s 采集一次统计：上行容量估算 + ICE 地址族/选中候选对。
   *
   * 容量取不到就保持 0（让服务端继续显示 pending，而不是编一个数字）；
   * 候选统计要**对所有 PC** 做（包括还没 connected 的），否则"连不上"这种最需要
   * 看候选的时刻恰好什么都看不到。
   */
  async function collectStats() {
    let sum = 0
    let seen = false
    let best: SelectedPairInfo | null = null

    for (const [peerId, pc] of pcs) {
      let report: RTCStatsReport
      try {
        report = await pc.getStats()
      } catch {
        // 统计失败不影响播放。
        continue
      }

      const byId = new Map<string, any>()
      const pairs: any[] = []

      report.forEach((stat: any) => {
        if (stat.type === 'local-candidate') {
          rememberCandidate(localCandidates, stat)
          byId.set(stat.id, stat)
          return
        }
        if (stat.type === 'remote-candidate') {
          rememberCandidate(remoteCandidates, stat)
          byId.set(stat.id, stat)
          return
        }
        if (stat.type === 'candidate-pair' && stat.state === 'succeeded' && stat.nominated) {
          pairs.push(stat)
          if (
            pc.connectionState === 'connected' &&
            typeof stat.availableOutgoingBitrate === 'number' &&
            stat.availableOutgoingBitrate > 0
          ) {
            sum += stat.availableOutgoingBitrate
            seen = true
          }
        }
      })

      // 选中候选对：local/remote-candidate 与 pair 在同一份 report 里，按 id 关联。
      for (const pair of pairs) {
        const local = byId.get(pair.localCandidateId)
        const remote = byId.get(pair.remoteCandidateId)
        const rttMs =
          typeof pair.currentRoundTripTime === 'number' ? Math.round(pair.currentRoundTripTime * 1000) : 0
        if (best && rttMs >= best.rttMs) {
          continue
        }
        const localAddress = local?.address ?? ''
        const remoteAddress = remote?.address ?? ''
        best = {
          peerId,
          localType: local?.candidateType ?? '?',
          remoteType: remote?.candidateType ?? '?',
          family: classifyAddress(localAddress),
          remoteFamily: classifyAddress(remoteAddress),
          protocol: local?.protocol ?? '?',
          localAddress,
          remoteAddress,
          rttMs,
        }
      }
    }

    if (seen) {
      uploadCapacityBps.value = Math.round(sum)
    }
    // 连接全部断掉时清空：留着一条已经不存在路径的"选中候选对"会骗人。
    selectedPair.value = best ?? (pcs.size === 0 ? null : selectedPair.value)
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
    // ---- ICE 配置与 IPv6 直连（打洞优化 ②③）----
    applyIceConfiguration,
    restartInitiators,
    sampleLocalCandidates,
    ipv6State,
    selectedPair,
    restartCount,
    restartLog,
    initiatedCount: () => initiated.size,
  }
}


