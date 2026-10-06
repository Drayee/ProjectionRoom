import { useRoomStore } from './stores/room'
import { classifyAddress, isCrossNetworkUsable } from './utils/iceCandidate'

/**
 * 给自动化验收用的只读快照钩子（window.__pr）。
 *
 * 存在的理由：M2 的验收标准是"起播时间 1–3s、进度偏差 < 500ms"这类可测量的量，
 * 而它们只存在于真实浏览器的运行期状态里。让 CDP 驱动脚本读一份结构化快照，
 * 比在 DOM 文本里抠数字可靠得多。
 */
export interface DebugSnapshot {
  role: string
  roomId: string
  clientId: string
  joined: boolean
  connection: string
  hostId: string
  needsGesture: boolean
  media: {
    loaded: boolean
    mimeType: string | null
    segmentCount: number
    totalDuration: number
  }
  video: {
    currentTime: number
    paused: boolean
    playbackRate: number
    readyState: number
    bufferedEnd: number
  }
  sync: {
    clockReady: boolean
    driftMs: number
    mode: string
    offsetMs: number
    /** 逐跳中继：只含"我到父节点"这一跳的滤波结果。 */
    hopOffsetMs: number
    /** 逐跳中继：父节点上报的"它到主播"的偏移。 */
    parentOffsetMs: number
    /** 逐跳中继取证：转发/收到带戳/收到不带戳/非主父 的条数。 */
    relayStats: { forwarded: number; relayed: number; direct: number; nonPrimary: number }
    /** 落后主播的秒数（正数 = 落后）。 */
    lagSec: number
    lagNotice: string
    bufferedAhead: number
    p95DeliveryMs: number
    syncResets: number
    rejectedProgress: number
  }
  p2p: {
    peerCount: number
    openChannels: number
    uploadCapacityBps: number
    delivered: number
    timedOut: number
    chunkErrors: number
    /** 取数失败的最近几条原因（排障入口：以前这里是静默的）。 */
    fetchFailures: string[]
    /** 应答侧最近几条（主播/中继："到底发出去没有"）。 */
    serveLog: string[]
  }
  room: {
    members: number
    capacityMode: string
    hostChildSlots: number
    maxMembers: number
    chatCount: number
  }
  topology: {
    mode: string
    depth: number
    primaryId: string
    backupIds: string[]
    children: string[]
    distributorId: string
    reason: string
    lastDistributorChange: string
  }
  /**
   * 播放健康度（T4）：验收脚本与诊断抽屉读的是同一份数据。
   * "按时率 / 迟到 / 卡顿 / 每边速率 / 在途上限"都在这里，不需要去抠 DOM 文本。
   */
  health: {
    /** 按时交付率 0–1；还没有样本时为 -1（未测得）。 */
    onTimeRate: number
    onTimeChunks: number
    lateChunks: number
    /** 卡顿次数：信号是 <video> 的 waiting 事件。 */
    stallCount: number
    /** 因超时换父的次数（T3）。 */
    timeoutFailovers: number
    /** 当前被暂时降权的父节点数 / 累计降权次数。 */
    avoidedParents: number
    avoidEvents: number
    /** 当前在途上限（T2-2 的推导值）与此刻实际在途请求数。 */
    inflight: number
    inflightNow: number
    edgeRateBps: number
    avgSegmentBytes: number
    /** 自身深度与房间成员数（"跳数"的可观测面）。 */
    depth: number
    members: number
    /** 取数候选父节点；长度为 0 且不是主播 = 未被安置。 */
    parents: string[]
    unassigned: boolean
    unassignedText: string
    edges: Array<{
      peerId: string
      label: string
      primary: boolean
      rateBps: number
      peakRateBps: number
      rttMs: number
      timeoutMs: number
      /** 按该边实测速率传完一个平均分片的预计耗时：超时阈值的另一半输入。 */
      expectedDeliveryMs: number
      deliveries: number
      timeouts: number
      samples: number
    }>
  }
  gate: {
    gated: boolean
    reason: string
    bufferedSec: number
    /** 门控要求：从主播时间戳所在分片起需要连续多少片。 */
    thresholdSegments: number
    /** 当前从锚点分片起连续完整的缓冲分片数。 */
    bufferedSegments: number
    waitedSec: number
  }
  lifecycle: string[]
  /**
   * 断线恢复（缺陷 1）的观测面：主播离线等待、观众等主播重建、主播自动重建、换 clientId。
   * 验收脚本只读这里，不去抠 DOM 文案。
   */
  recovery: {
    /** 主播离线等待态（joined 且成员表里没有 host）。 */
    hostOffline: boolean
    /** 等待态文案（含剩余秒数）。 */
    hostOfflineText: string
    hostOfflineSecondsLeft: number
    /** 服务端宽限期基准（秒）。 */
    hostGraceSeconds: number
    /** 观众等待主播重建房间的提示（非空即表示正在重试）。 */
    resumeNotice: string
    /** 不可自动恢复时的明确文案。 */
    unrecoverable: string
    /** 主播重建房间的进度：idle / rebuilding / failed。 */
    rebuildState: string
    /** 本页面会话已经换过几次 clientId（CLIENT_ID_TAKEN）。 */
    clientIdRotations: number
    /** 服务端给出的权威关闭原因（非空即以它为准）。 */
    roomClosed: string
  }
  player: {
    attached: boolean
    ready: boolean
    queued: number
    initAppended: boolean
    stalls: number
    mediaSourceState: string
    sourceBufferCount: number
    hasObjectUrl: boolean
  }
  /**
   * ICE 配置缓存（打洞优化 ①②）：TTL、到期时刻、刷新次数、最近一次刷新原因与探测分数。
   * `restartCount` 是"列表真的变了才重启 ICE"的反面证据：列表没变时它必须不增长。
   */
  ice: {
    ttlSeconds: number
    expiresAt: number
    refreshCount: number
    changeCount: number
    failedRefreshCount: number
    lastRefreshReason: string
    lastRefreshAt: number
    nextRefreshAt: number
    refreshMarginSec: number
    lastError: string
    serverUrls: string[]
    scores: Array<{ url: string; rttMs: number; ok: boolean; score: number; selected?: boolean }>
    restartCount: number
    restartLog: string[]
    /** 我是发起方（offer 由我发）的连接数：ICE restart 只会发生在它们身上。 */
    initiatedPeers: number
  }
  /** IPv6 直连的地址族诊断（"IPv6 直连有没有生效"靠它验证）。 */
  ipv6: {
    hasGlobalLocal: boolean
    families: { v4: number; v6Global: number; v6LinkLocal: number; v6Ula: number; unknown: number }
    remoteFamilies: { v4: number; v6Global: number; v6LinkLocal: number; v6Ula: number; unknown: number }
    filtered: number
    filteredSamples: string[]
    samples: string[]
    /** host 候选地址是否被 mDNS 混淆藏起来（解释了选中候选对为什么是 unknown）。 */
    mdnsHidden: boolean
    /** 独立采样（临时 PC）看到的候选：真实连接秒连时会漏掉全局 IPv6，靠它兜底。 */
    sampler: {
      runs: number
      families: { v4: number; v6Global: number; v6LinkLocal: number; v6Ula: number; unknown: number }
      samples: string[]
    }
  }
  /** 选中候选对：null = 还没建立成功任何一对候选。 */
  selectedPair: {
    peerId: string
    localType: string
    remoteType: string
    family: string
    remoteFamily: string
    protocol: string
    localAddress: string
    remoteAddress: string
    rttMs: number
  } | null
  errors: {
    last: string
    media: string
    player: string
  }
}

export function installDebugHook(): void {
  const store = useRoomStore()

  const api = {
    snapshot(): DebugSnapshot {
      const video = store.videoElement as HTMLVideoElement | null
      let bufferedEnd = 0
      if (video && video.buffered.length > 0) {
        bufferedEnd = video.buffered.end(video.buffered.length - 1)
      }

      return {
        role: store.role,
        roomId: store.roomId,
        clientId: store.clientId,
        joined: store.joined,
        connection: store.connection,
        hostId: store.hostId,
        needsGesture: store.needsGesture,
        media: {
          loaded: store.mediaIndex !== null,
          mimeType: store.mediaIndex?.mimeType ?? null,
          segmentCount: store.mediaIndex?.segments.length ?? 0,
          totalDuration: store.mediaIndex?.totalDuration ?? 0,
        },
        video: {
          currentTime: video?.currentTime ?? -1,
          paused: video?.paused ?? true,
          playbackRate: video?.playbackRate ?? 0,
          readyState: video?.readyState ?? -1,
          bufferedEnd,
        },
        sync: {
          clockReady: store.clockReady,
          driftMs: Math.round(store.drift * 1000),
          mode: store.syncMode,
          offsetMs: Math.round(store.offsetMs),
          hopOffsetMs: Math.round(store.hopOffsetMs),
          parentOffsetMs: Math.round(store.parentOffsetMs),
          relayStats: store.relayStats(),
          lagSec: Number(store.lagSec.toFixed(2)),
          lagNotice: store.lagNotice,
          bufferedAhead: Number(store.bufferedAhead.toFixed(2)),
          p95DeliveryMs: store.p95DeliveryMs(),
          syncResets: store.syncResets,
          rejectedProgress: store.rejectedProgress,
        },
        p2p: {
          peerCount: store.peerCount,
          openChannels: [...store.peers.values()].filter((peer) => peer.channelOpen).length,
          uploadCapacityBps: store.uploadCapacityBps,
          delivered: store.delivered,
          timedOut: store.timedOut,
          chunkErrors: store.chunkErrors,
          fetchFailures: store.fetchFailures,
          serveLog: store.serveLog,
        },
        room: {
          members: store.members.length,
          capacityMode: store.capacity?.mode ?? 'unknown',
          hostChildSlots: store.capacity?.hostChildSlots ?? 0,
          maxMembers: store.capacity?.maxMembers ?? 0,
          chatCount: store.chat.length,
        },
        gate: {
          gated: store.gated,
          reason: store.gateReason,
          bufferedSec: Number(store.gateBufferedSec.toFixed(2)),
          thresholdSegments: store.gateThresholdSegments,
          bufferedSegments: store.gateBufferedSegments,
          waitedSec: Number(store.gateWaitedSec.toFixed(1)),
        },
        lifecycle: store.lifecycle,
        recovery: {
          hostOffline: store.hostOffline,
          hostOfflineText: store.hostOfflineText,
          hostOfflineSecondsLeft: store.hostOfflineSecondsLeft,
          hostGraceSeconds: store.hostGraceSeconds,
          resumeNotice: store.resumeNotice,
          unrecoverable: store.roomUnrecoverable,
          rebuildState: store.rebuildState,
          clientIdRotations: store.clientIdRotations,
          roomClosed: store.roomClosed,
        },
        player: store.playerDebugState(),
        ice: {
          ...store.iceDiagnostics(),
          restartCount: store.iceRestartCount,
          restartLog: [...store.iceRestartLog],
          initiatedPeers: store.initiatedPeerCount(),
        },
        ipv6: store.ipv6Diagnostics(),
        selectedPair: store.selectedPairInfo(),
        topology: {
          mode: store.topologyMode,
          depth: store.topologyDepth,
          primaryId: store.primaryParentId,
          backupIds: store.backupParentIds,
          children: store.childrenIds,
          distributorId: store.distributorId,
          reason: store.topologyReason,
          lastDistributorChange: store.lastDistributorChange,
        },
        health: store.playbackHealth,
        errors: {
          last: store.lastError,
          media: store.mediaError,
          player: store.playerError,
        },
      }
    },
    store,
    /** 注入实测上行（headless 的 getStats 不产生估计值）。 */
    reportMetrics: (uploadCapacityBps: number, rttMs = 20) => store.reportMetrics(uploadCapacityBps, rttMs),
    /** 应用层限速，模拟慢上行节点。 */
    setUploadThrottle: (bps: number) => store.setUploadThrottle(bps),
    /**
     * 验收钩子：强制掐断信令 WS（模拟断网/服务端重启），随后仍会自动重连。
     * CDP 的 emulateNetworkConditions(offline) 在部分 Chrome 上不会立刻拆掉已建立的
     * WebSocket，验收脚本用这条确定性路径兜底。
     */
    dropSignaling: () => store.dropSignaling(),
    /**
     * 验收钩子：按住/放开信令（按住 = 断开 + 之后连不上），
     * 用于确定性复现"断线超过宽限期"。
     */
    holdSignaling: (hold: boolean) => store.holdSignaling(hold),
    /**
     * 验收钩子：把候选地址分类器暴露出来。
     * 为什么要暴露纯函数：链路本地/ULA 候选在一台有公网 IPv6 的机器上**根本收集不到**，
     * 只靠真实候选无法证明"过滤判据生效"。分类器是确定性函数，可以直接喂样本断言。
     */
    iceClassify: (address: string) => classifyAddress(address),
    iceCrossNetworkUsable: (address: string) => isCrossNetworkUsable(classifyAddress(address)),
  }

  ;(window as unknown as { __pr?: typeof api }).__pr = api
}



