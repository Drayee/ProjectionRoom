import { useRoomStore } from './stores/room'

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
        player: store.playerDebugState(),
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
  }

  ;(window as unknown as { __pr?: typeof api }).__pr = api
}



