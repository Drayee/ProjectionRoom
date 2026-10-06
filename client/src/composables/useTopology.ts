import { computed, ref } from 'vue'
import { MAX_FAILURES_PER_EDGE, PARENT_AVOID_TTL_MS } from '../utils/serveSchedule'
import type { PeerControl, TopologyAssignment } from '../types/protocol'

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
  /** 某个 peer 当前的取数在途请求数（条带化分流判据的输入；当前实现不用它，保留依赖以便回归）。 */
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

  /**
   * 同一个 (父节点, 分片) 的连续失败次数。
   *
   * 只对"同一个父节点的同一个分片"累计：一次抖动不该把一个父节点拉黑，
   * 但同一片连着要两次都拿不到，就说明这条边此刻真的有问题（T3-2）。
   */
  const edgeFailures = new Map<string, number>()

  /**
   * 暂时降权的父节点 → 解禁时刻（performance.now()）。
   *
   * "降权"不是"禁用"：pickParent 在还有别的候选时跳过它，只剩它时才退回它 ——
   * 宁可问一个已知很慢的父节点，也不要变成"没有可用父节点"。
   * 带 TTL 是为了能回来：链路抖动过去之后它照样是好父节点。
   */
  const avoided = ref(new Map<string, number>())
  /** 累计"因连续失败被降权"的次数（诊断用；与当前降权集合的大小不是一回事）。 */
  const avoidEvents = ref(0)

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

  /** 这个父节点现在是否被暂时降权（到点的条目顺手清掉，不需要额外的定时器）。 */
  function isAvoided(peerId: string): boolean {
    const until = avoided.value.get(peerId)
    if (until === undefined) {
      return false
    }
    if (until <= performance.now()) {
      avoided.value.delete(peerId)
      return false
    }
    return true
  }

  /** 把父节点暂时降权（TTL 到点自动解禁）。 */
  function suppress(peerId: string) {
    if (!peerId) return
    avoided.value.set(peerId, performance.now() + PARENT_AVOID_TTL_MS)
    avoidEvents.value += 1
  }

  /**
   * 当前被降权的父节点数（诊断用）。
   *
   * 只读、不改数据：它会被塞进 computed（诊断快照）里，而"删过期条目"这种写操作
   * 放在 computed 的读取路径上会自我触发。真正的过期清理在 isAvoided 里顺手做。
   */
  function avoidedParents(): number {
    const now = performance.now()
    let count = 0
    for (const until of avoided.value.values()) {
      if (until > now) count += 1
    }
    return count
  }

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
      // 换父之后"同一个父节点上的连续失败"记账也失去意义：旧的降权结论不该跟到新父节点上。
      edgeFailures.clear()
      avoided.value.clear()
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
   * 候选先做两级过滤，再做原有的选优：
   *   0. 排掉本次点名要跳过的父节点（`excludePeerId`，超时换父时用）与**暂时降权**的父节点；
   *      两级都遵守"只剩一个候选时就不过滤" —— 没有别的选择时，慢父节点也聊胜于无；
   *   1. 失败过 → 换下一个父节点（超时转投，L2）；
   *   2. 有父节点明确拥有这一片 → 主父拥有就用主父；主父没有才轮转到拥有者；
   *   3. 都没声明拥有 → 用主父（把分片丢给一个自己还在下载的节点只会换来 NOT_FOUND）。
   *
   * 注：这里**刻意不做**"主父积压就把分片分流给备用父"（原实现里那个判断在
   * `owners.length > 0` 分支之后，永远不可达）。本机实测（test/script/verify-health.mjs
   * 的构造，主播→中继→叶子单链）：一旦让它生效，叶子会用"备用父=主播"的完整位图
   * 把**全部**分片直接向主播取，一个超时都不再落到中继上 —— 等于绕开服务端按 K0
   * 算出来的中继，把上行压力全压回主播。这与本轮的"延迟/卡顿优先"无关，
   * 却会破坏容量规划，所以保持原样、只保留注释说明。
   */
  function pickParent(segmentIndex: number, excludePeerId = ''): string {
    const all = parents.value
    if (all.length === 0) {
      return ''
    }
    if (all.length === 1) {
      return all[0]
    }

    let list = all.filter((id) => id !== excludePeerId)
    const healthy = list.filter((id) => !isAvoided(id))
    if (healthy.length > 0) {
      list = healthy
    }
    if (list.length === 0) {
      list = all
    }
    if (list.length === 1) {
      return list[0]
    }

    const attempt = attempts.get(segmentIndex) ?? 0
    if (attempt > 0) {
      return list[attempt % list.length]
    }

    const primary = list[0]

    // 只问"明确声明拥有这一片"的父节点。位图是唯一的依据：
    // 盲发给一个自己还在下载的节点只会换来 NOT_FOUND，白白拖慢缓冲（实测每次多数十次失败）。
    const owners = list.filter((id) => knowsChunk(id, segmentIndex) === true)
    if (owners.length > 0) {
      // 主父拥有就优先它（离源更近、跳数更少）；否则在拥有者之间轮转，把负载摊开。
      return owners.includes(primary) ? primary : owners[segmentIndex % owners.length]
    }

    return primary
  }

  function noteAttempt(segmentIndex: number) {
    attempts.set(segmentIndex, (attempts.get(segmentIndex) ?? 0) + 1)
  }

  /**
   * 记一次失败（T3-2）。返回 true 表示这个父节点**因此被降权**。
   *
   * 到 `MAX_FAILURES_PER_EDGE` 次就把它放进降权集合、并把该分片的失败计数清零：
   * 清零是为了"解禁之后重新给机会"，而不是一进来又立刻被降权。
   */
  function noteFailure(segmentIndex: number, peerId: string): boolean {
    if (!peerId) return false
    const key = `${peerId}#${segmentIndex}`
    const next = (edgeFailures.get(key) ?? 0) + 1
    if (next >= MAX_FAILURES_PER_EDGE) {
      edgeFailures.delete(key)
      suppress(peerId)
      return true
    }
    edgeFailures.set(key, next)
    return false
  }

  function noteDelivered(segmentIndex: number) {
    attempts.delete(segmentIndex)
    // 拿到了：这一片的失败记账全部作废（含别的父节点上的）。
    const suffix = `#${segmentIndex}`
    for (const key of [...edgeFailures.keys()]) {
      if (key.endsWith(suffix)) {
        edgeFailures.delete(key)
      }
    }
  }

  function resetAttempts() {
    attempts.clear()
    edgeFailures.clear()
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
    edgeFailures.clear()
    avoided.value.clear()
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
    noteFailure,
    noteDelivered,
    resetAttempts,
    knowsChunk,
    notePeerHave,
    forgetPeer,
    broadcastHave,
    isAvoided,
    avoidedParents,
    avoidEvents,
  }
}

