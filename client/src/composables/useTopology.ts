import { computed, ref } from 'vue'
import type { PeerControl, TopologyAssignment } from '../types/protocol'

/** 主父在途请求达到这个数就把后续分片分流给备用父。 */
const STRIPE_THRESHOLD = 3

export interface TopologyDeps {
  selfId: () => string
  isHost: () => boolean
  /** 与某个成员建立（或复用）DataChannel。 */
  connectToPeer: (peerId: string) => Promise<void>
  sendToPeer: (peerId: string, msg: PeerControl) => boolean
  connectedPeers: () => string[]
  /** 索引里的分片总数。 */
  segmentCount: () => number
  /** 本节点已经拥有的分片序号（含 init 用 0 表示）。 */
  ownedSegments: () => number[]
  /** 某个 peer 当前的取数在途请求数（用于自适应条带化）。 */
  pendingFor: (peerId: string) => number
  /** 主父变化时的回调：调度器需要清掉重试计数并重新预取。 */
  onPrimaryChanged?: () => void
}


/**
 * 拓扑与多父调度（SPEC §6.1–§6.4）。
 *
 * 服务端只做决策，真正的"从谁那里取数据"必须由客户端按分配执行：
 *   - 主父用于控制消息与默认取数；
 *   - 备用父参与**多父条带化**（L1）：分片按序号轮流分给不同父节点，
 *     所以一个慢父节点只会拖慢它那一份，不会拖死整条流；
 *   - 任何一次失败/超时都让该分片**转投下一个父节点**（L2）；
 *   - `have` 位图让"这一片该找谁"有依据，而不是盲试。
 */
export function useTopology(deps: TopologyDeps) {
  const assignment = ref<TopologyAssignment | null>(null)
  const distributorVersion = ref(0)
  const peerHave = ref(new Map<string, Uint8Array>())

  /** 每个分片已经尝试过的父节点次数（用于超时转投）。 */
  const attempts = new Map<number, number>()

  const mode = computed(() => assignment.value?.mode ?? 'pending')
  const depth = computed(() => assignment.value?.depth ?? 0)
  const primaryId = computed(() => assignment.value?.primaryId ?? '')
  const backupIds = computed(() => assignment.value?.backupIds ?? [])
  const children = computed(() => assignment.value?.children ?? [])
  const distributorId = computed(() => assignment.value?.distributorId ?? '')
  const reason = computed(() => assignment.value?.reason ?? '')

  /** 取数候选：主父在前，备用父在后。主播没有父节点（它是源）。 */
  const parents = computed<string[]>(() => {
    if (deps.isHost()) {
      return []
    }
    const self = deps.selfId()
    return [primaryId.value, ...backupIds.value].filter((id) => id && id !== self)
  })

  async function apply(next: TopologyAssignment) {
    const previousPrimary = primaryId.value
    assignment.value = next

    for (const peerId of [next.primaryId, ...(next.backupIds ?? [])]) {
      if (peerId && peerId !== deps.selfId()) {
        try {
          await deps.connectToPeer(peerId)
        } catch {
          // 单个父节点连不上不影响其他候选；调度器会在超时后转投。
        }
      }
    }

    if (previousPrimary !== (next.primaryId ?? '')) {
      attempts.clear()
      deps.onPrimaryChanged?.()
    }
  }

  function noteDistributorChange() {
    distributorVersion.value += 1
  }

  /** 返回 true/false 表示"明确拥有/没有"，null 表示对方还没上报。 */
  function knowsChunk(peerId: string, segmentIndex: number): boolean | null {
    const bits = peerHave.value.get(peerId)
    if (!bits || bits.length === 0) {
      return null
    }
    const bit = segmentIndex - 1
    if (bit < 0) {
      return null
    }
    const byte = bits[bit >> 3]
    if (byte === undefined) {
      return false
    }
    return (byte & (1 << (bit & 7))) !== 0
  }

  /**
   * 这一片该向谁要（SPEC §6.4 L1/L2）。
   *
   * 自适应条带化，而不是简单轮转：
   *   1. 失败过 → 换下一个父节点（超时转投，L2）；
   *   2. 主父明确没有、备用父明确有 → 直接找备用父；
   *   3. 主父在途请求还没堆起来 → 就用主父（把分片丢给一个自己还在下载的节点，
   *      实测会产生大量 NOT_FOUND，白白拖慢缓冲）；
   *   4. 主父已经积压 → 把这一片分流给备用父，这就是"一个慢父节点只影响它那一份"。
   */
  function pickParent(segmentIndex: number): string {
    const list = parents.value
    if (list.length === 0) {
      return ''
    }
    if (list.length === 1) {
      return list[0]
    }

    const attempt = attempts.get(segmentIndex) ?? 0
    if (attempt > 0) {
      return list[attempt % list.length]
    }

    const primary = list[0]
    const backups = list.slice(1)

    // 只问"明确声明拥有这一片"的父节点。位图是唯一的依据：
    // 盲发给一个自己还在下载的节点只会换来 NOT_FOUND，白白拖慢缓冲（实测每次多数十次失败）。
    const owners = list.filter((id) => knowsChunk(id, segmentIndex) === true)
    if (owners.length > 0) {
      // 主父拥有就优先它（离源更近、跳数更少）；否则在拥有者之间轮转，把负载摊开。
      return owners.includes(primary) ? primary : owners[segmentIndex % owners.length]
    }

    // 主父积压时把分片分流给备用父 —— 但只能分给"明确拥有这一片"的备用父。
    // 分给一个没有该片的节点只会换来 NOT_FOUND，实测会刷出几十次无谓失败。
    if (owners.length > 0 && deps.pendingFor(primary) >= STRIPE_THRESHOLD) {
      return owners[segmentIndex % owners.length]
    }

    return primary
  }

  function noteAttempt(segmentIndex: number) {
    attempts.set(segmentIndex, (attempts.get(segmentIndex) ?? 0) + 1)
  }

  function noteDelivered(segmentIndex: number) {
    attempts.delete(segmentIndex)
  }

  function resetAttempts() {
    attempts.clear()
  }

  function notePeerHave(peerId: string, chunks: Uint8Array<ArrayBuffer>) {
    if (!chunks || chunks.length === 0) {
      return
    }
    // protobuf 里位图就是 bytes：不需要 base64 往返。
    peerHave.value.set(peerId, chunks)
  }

  function forgetPeer(peerId: string) {
    peerHave.value.delete(peerId)
  }

  /** 生成并广播本节点的分片拥有位图；返回值同时用于上报服务端。 */
  function broadcastHave(): { bits: Uint8Array<ArrayBuffer>; complete: boolean } | null {
    const total = deps.segmentCount()
    if (total <= 0) {
      return null
    }

    const bytes = new Uint8Array(Math.ceil((total + 1) / 8))
    const owned = deps.ownedSegments()
    for (const index of owned) {
      const bit = index - 1
      if (bit >= 0) {
        bytes[bit >> 3] |= 1 << (bit & 7)
      }
    }

    const bits = bytes
    const complete = owned.filter((index) => index >= 1).length >= total
    const message: PeerControl = { t: 'have', chunks: bits, complete }

    for (const peerId of deps.connectedPeers()) {
      deps.sendToPeer(peerId, message)
    }

    return { bits, complete }
  }

  function reset() {
    assignment.value = null
    peerHave.value.clear()
    attempts.clear()
  }

  return {
    assignment,
    reset,
    mode,
    depth,
    primaryId,
    backupIds,
    children,
    distributorId,
    reason,
    parents,
    distributorVersion,
    apply,
    noteDistributorChange,
    pickParent,
    noteAttempt,
    noteDelivered,
    resetAttempts,
    knowsChunk,
    notePeerHave,
    forgetPeer,
    broadcastHave,
  }
}

