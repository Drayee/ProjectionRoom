<script setup lang="ts">
// 诊断抽屉：把原先只存在于 window.__pr.snapshot() 里的排障信息搬到界面上。
//
// 设计取舍：
//   - 默认收起（<details> 不带 open）：这些日志每 3s 就可能新增，默认展开会刷屏，
//     把真正要看的状态挤走；需要排障时展开、复制一份发出来即可。
//   - 只读、不提供清空：日志是环形缓冲（store 里各留最近 8 条），清空会让"复现一次"
//     这件事变得没法复盘；要重新取证就刷新页面。
//   - 复制的是**纯文本报告**：用户把它贴进 issue / 群里，比截图有用得多。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useRoomStore } from '../stores/room'
import { copyText } from '../utils/clipboard'
import { maskAddress } from '../utils/addressMask'

const store = useRoomStore()
const copyState = ref<'idle' | 'ok' | 'failed'>('idle')
let timer: number | undefined

/**
 * 「包含完整地址（仅自己看）」（F-6）。
 *
 * 诊断报告是**要被贴出去**的（issue / 群聊），而 `localAddress ↔ remoteAddress`
 * 是本机内网地址与**陌生人的**公网地址：默认必须掩码。
 * 这条开关只影响显示，**不进 localStorage**（不落盘、不跨会话记住），
 * 刷新即回到"掩码"的安全默认 —— 这类隐私开关最怕的就是"上次开过就一直开着"。
 */
const includeFullAddress = ref(false)

/** 地址的展示形态：默认掩码；开关打开时显示原样（界面与复制内容用同一份输出）。 */
function shownAddress(address: string): string {
  if (!address) return '（地址不可见）'
  return includeFullAddress.value ? address : maskAddress(address)
}

function memberName(id: string): string {
  if (!id) return '—'
  return store.members.find((m) => m.id === id)?.displayName ?? id.slice(0, 6)
}

function humanBps(bps: number): string {
  if (!bps) return '未测得'
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(2)} Mbps`
  return `${Math.round(bps / 1000)} kbps`
}

function humanBytes(bytes: number): string {
  if (!bytes) return '未测得'
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(2)} MiB`
  return `${Math.round(bytes / 1024)} KiB`
}

const peers = computed(() => [...store.peers.values()])
const openChannels = computed(() => peers.value.filter((peer) => peer.channelOpen).length)
const player = computed(() => store.playerDebugState())

const connectionText = computed(() => {
  switch (store.connection) {
    case 'open':
      return '信令已连接'
    case 'connecting':
      return '正在连接信令（会自动重连）'
    case 'closed':
      return '信令已断开，正在自动重连'
    default:
      return '未连接'
  }
})

/**
 * 上游链路：观众说"上游"，主播说"观众通道"。
 *
 * "未安置"必须说出来（T4 补 T1 的缺口）：服务端安置不下时下发的是**空分配**
 * （主父为空串），客户端此前只是静默地什么都不取 —— 用户看到的是"一直加载中"，
 * 却看不出原因是"房间满了"。这里用显式文案区分"还没下发"与"确实没被安置"。
 */
const unassigned = computed(() => !store.isHost && store.parentIds.length === 0)

const upstreamText = computed(() => {
  if (store.isHost) {
    return `P2P ${peers.value.length} 条（已开通道 ${openChannels.value}）· 上行估计 ${humanBps(store.uploadCapacityBps)}`
  }
  const channels = `已开通道 ${openChannels.value}/${peers.value.length}`
  if (store.parentIds.length > 0) {
    const backup = store.backupParentIds.length > 0 ? `（备用 ${store.backupParentIds.map(memberName).join('、')}）` : ''
    return `主父 ${memberName(store.primaryParentId)}${backup} · ${channels}`
  }
  if (store.topologyMode === 'pending') {
    return `等待服务端分配上游（尚未下发分配）· ${channels}`
  }
  return `未安置：当前房间已满 / 没有可用父节点（服务端没有分配父节点）· ${channels}`
})

const lagText = computed(() => {
  if (store.isHost) return '不适用（主播是源）'
  if (store.lagNotice) return store.lagNotice
  if (Math.abs(store.lagSec) < 0.5) return '与主播基本同步'
  return store.lagSec > 0
    ? `落后主播 ${store.lagSec.toFixed(1)}s`
    : `领先主播 ${Math.abs(store.lagSec).toFixed(1)}s`
})

const gateText = computed(() => {
  if (store.isHost) return '不适用（主播是源）'
  if (!store.gated) return '已开闸'
  return (
    `加载中：${store.gateReason || '等待缓冲'} · 连续 ${store.gateBufferedSegments}/${store.gateThresholdSegments} 片` +
    ` · 已等 ${store.gateWaitedSec.toFixed(0)}s（不设超时）`
  )
})

const clockText = computed(() => {
  if (store.isHost) return '不适用（主播是源）'
  return (
    `样本就绪 ${store.clockReady ? '是' : '否'} · 偏差 ${(store.drift * 1000).toFixed(0)}ms` +
    ` · 偏移 ${Math.round(store.offsetMs)}ms（本跳 ${Math.round(store.hopOffsetMs)} + 父 ${Math.round(store.parentOffsetMs)}）`
  )
})

const topologyText = computed(() => {
  const parts = [`模式 ${store.topologyMode}`, `深度 ${store.topologyDepth}`]
  if (store.primaryParentId) parts.push(`主父 ${memberName(store.primaryParentId)}`)
  if (store.backupParentIds.length > 0) {
    parts.push(`备用父 ${store.backupParentIds.map(memberName).join('、')}`)
  }
  if (store.childrenIds.length > 0) parts.push(`下游 ${store.childrenIds.length} 个`)
  if (store.distributorId) parts.push(`分发节点 ${memberName(store.distributorId)}`)
  if (store.lastDistributorChange) parts.push(`最近换防 ${store.lastDistributorChange}`)
  return parts.join(' · ')
})

const topologyReason = computed(() => store.topologyReason || '服务端还没下发分配依据')

const playerText = computed(() => {
  const state = player.value
  return (
    `已挂载 ${state.attached ? '是' : '否'} · 就绪 ${state.ready ? '是' : '否'}` +
    ` · 待追加 ${state.queued} 片 · init ${state.initAppended ? '已写入' : '未写入'}` +
    ` · 卡顿 ${state.stalls} 次 · MediaSource ${state.mediaSourceState || '—'}`
  )
})

const mediaText = computed(() => {
  const index = store.mediaIndex
  if (!index) return '尚未加载分片索引'
  return (
    `${index.mimeType} · ${index.segments.length} 段 · ${index.totalDuration.toFixed(1)}s` +
    ` · ${(index.totalBytes / 1024 / 1024).toFixed(1)} MiB`
  )
})

const runtimeText = computed(
  () =>
    `交付 ${store.delivered} · 超时 ${store.timedOut} · 取数失败 ${store.chunkErrors}` +
    ` · 内容校验 坏片 ${store.hashMismatches} / 通过 ${store.hashVerified}` +
    (store.hashSkipped > 0 ? ` / 跳过 ${store.hashSkipped}（非安全上下文没有 crypto.subtle）` : '') +
    ` · p95 交付 ${store.p95DeliveryMs}ms · 跳转重灌 ${store.syncResets} 次 · 丢弃非主父进度 ${store.rejectedProgress} 条`,
)

/**
 * 内容校验（F-11）：坏片计数是"有对端在发替换内容"的唯一直接证据，
 * 必须出现在诊断里，而不是只躺在 store 里。
 */
const hashText = computed(() => {
  if (store.hashMismatches === 0 && store.hashVerified === 0 && store.hashSkipped === 0) {
    return store.isHost ? '不适用（主播是源，收到的分片只来自本机磁盘）' : '还没有收到分片'
  }
  const parts = [`坏片 ${store.hashMismatches}`, `通过 ${store.hashVerified}`]
  if (store.hashSkipped > 0) {
    parts.push(`跳过 ${store.hashSkipped}（当前上下文没有 crypto.subtle：明文 http 的局域网地址就是这样）`)
  }
  return parts.join(' · ') + (store.hashMismatches > 0 ? ' · 坏片已丢弃且不会进播放器' : '')
})

// ---------- 播放健康度（T4）----------
//
// 这一块是"优化到底有没有效"的判据面，四项都要能读到**真实数值**：
//   ① 按时交付率（onTimeRate）—— 迟到分片才是用户感知到的卡顿来源；
//   ② 卡顿次数（<video> waiting 事件）—— 播放头到了却没有可播数据；
//   ③ 每边速率 / RTT / 交付·超时次数 —— 在途上限与超时值就是从这里推出来的；
//   ④ 深度 + 成员数、当前在途上限。
// 沿用抽屉原有样式（.kv 网格 + .mono），不重做 UI。

const health = computed(() => store.playbackHealth)

const healthText = computed(() => {
  const h = health.value
  const stalls = `${h.stallCount} 次（信号：<video> waiting 事件）`
  if (h.onTimeRate < 0) {
    return `按时率 未测得（还没有分片进入可播缓冲）· 卡顿 ${stalls}`
  }
  return (
    `按时率 ${(h.onTimeRate * 100).toFixed(1)}%（按时 ${h.onTimeChunks} / 迟到 ${h.lateChunks}）` +
    ` · 卡顿 ${stalls}`
  )
})

const edgeText = computed(() => {
  const h = health.value
  if (h.edges.length === 0) {
    return store.isHost ? '不适用（主播是源，不向父节点取数）' : '尚无交付样本（还没从父节点收到分片）'
  }
  return h.edges
    .map((edge) => {
      const peak = edge.peakRateBps > edge.rateBps ? `／峰值 ${humanBps(edge.peakRateBps)}` : ''
      const rtt = edge.rttMs > 0 ? `${edge.rttMs}ms` : '未测'
      // 超时阈值由两部分取大：3×RTT 与 2×预计传输耗时。后者能解释"为什么阈值比 RTT 大很多"。
      const budget =
        edge.expectedDeliveryMs > 0
          ? `超时阈值 ${edge.timeoutMs}ms（含预计传输 ${edge.expectedDeliveryMs}ms）`
          : `超时阈值 ${edge.timeoutMs}ms`
      return (
        `${edge.label}${edge.primary ? '*' : ''} ${humanBps(edge.rateBps)}${peak}` +
        ` · rtt ${rtt} · ${budget} · 交付 ${edge.deliveries}／超时 ${edge.timeouts}`
      )
    })
    .join(' ｜ ')
})

const scheduleText = computed(() => {
  const h = health.value
  return (
    `在途上限 ${h.inflight}（实测边速率 ${humanBps(h.edgeRateBps)} / 均片 ${humanBytes(h.avgSegmentBytes)}` +
    ` / 此刻在途 ${h.inflightNow}）· 发送队列按"离播放头距离"升序（已播过的排最后）` +
    ` · 超时换父 ${h.timeoutFailovers} 次 · 降权父 ${h.avoidedParents}（累计 ${h.avoidEvents}）` +
    ` · 深度 ${h.depth} · 成员 ${h.members}`
  )
})

/**
 * ICE 配置（TTL 缓存 + 刷新）。
 * 重点是"列表变了才连"：刷新次数会随 TTL 一直涨，而重启次数只应该在列表真的变了时才涨。
 */
const iceText = computed(() => {
  const ice = store.iceDiagnostics()
  if (ice.refreshCount === 0) return '尚未拉到 ICE 配置（等待 /api/ice）'
  const scores = ice.scores
    .filter((s) => s.ok)
    .slice(0, 4)
    .map((s) => `${hostOf(s.url)} ${s.rttMs}ms${s.selected ? '*' : ''}`)
    .join(' · ')
  return (
    `TTL ${ice.ttlSeconds}s（余量 ${ice.refreshMarginSec}s）· 距到期 ${store.iceSecondsUntilExpiry()}s` +
    ` · 刷新 ${ice.refreshCount} 次（列表变化 ${ice.changeCount} · ICE 重启 ${store.iceRestartCount}）` +
    ` · 最近 ${ice.lastRefreshReason || '—'}` +
    (ice.failedRefreshCount > 0 ? ` · 刷新失败 ${ice.failedRefreshCount}` : '') +
    // F-12：白名单挡下的条目必须能在界面上看到（否则"STUN 不生效"会变成玄学排障）。
    (ice.filteredIceServers > 0 ? ` · ${iceFilterText.value}` : '') +
    (scores ? ` · 探测 ${scores}` : '')
  )
})

/** F-12：被白名单过滤掉的 ICE 条目（条数 + 可读样本）。 */
const iceFilterText = computed(() => {
  const ice = store.iceDiagnostics()
  if (ice.filteredIceServers <= 0) return ''
  return `被白名单过滤 ${ice.filteredIceServers} 条：${ice.filteredIceSamples.join('；')}`
})

/** IPv6 直连的地址族分布：全局 IPv6 有没有真的拿到、有没有候选被判为不可跨网而没发出去。 */
const ipv6Text = computed(() => {
  const state = store.ipv6Diagnostics()
  const f = state.families
  const r = state.remoteFamilies
  const s = state.sampler.families
  const verdict = state.hasGlobalLocal ? '全局 IPv6 可用' : '没有全局 IPv6（不按 IPv6 直连对待）'
  return (
    `${verdict} · 本机候选 IPv4 ${f.v4} / IPv6 全局 ${f.v6Global} / 链路本地 ${f.v6LinkLocal} / ULA ${f.v6Ula}` +
    ` · 对端 IPv4 ${r.v4} / IPv6 全局 ${r.v6Global}` +
    ` · 已挡下不可跨网候选 ${state.filtered} 条` +
    ` · 独立采样 ${state.sampler.runs} 次（全局 ${s.v6Global} / 链路本地 ${s.v6LinkLocal}）` +
    (state.mdnsHidden ? ' · host 地址被 mDNS 隐藏（看 srflx）' : '')
  )
})

/** 选中候选对：`family=v6-global` 才是"IPv6 直连真的生效了"。地址默认掩码（见 includeFullAddress）。 */
const selectedPairText = computed(() => {
  const pair = store.selectedPairInfo()
  if (!pair) return '尚未建立成功的候选对（等 P2P 连通）'
  return (
    `${pair.family || 'unknown'} · ${pair.localType} ${shownAddress(pair.localAddress)}` +
    ` ↔ ${pair.remoteType} ${shownAddress(pair.remoteAddress)} · ${pair.protocol} · rtt ${pair.rttMs}ms`
  )
})

function hostOf(url: string): string {
  return url.replace(/^[a-z0-9+.-]+:\/\//i, '').split('/')[0] ?? url
}

/** 复制出去的纯文本报告：按"先状态、后日志"排，日志保持时间顺序。 */
const report = computed(() => {
  const lines: string[] = []
  lines.push('ProjectionRoom 诊断报告')
  lines.push(`时间: ${new Date().toISOString()}`)
  lines.push(`房间: ${store.roomId || '—'}  角色: ${store.isHost ? '主播' : '观众'}  客户端: ${store.clientId || '—'}`)
  lines.push(`连接: ${connectionText.value}  已加入: ${store.joined}  错误: ${store.lastError || '无'}`)
  lines.push(`上游: ${upstreamText.value}`)
  lines.push(`同步: ${clockText.value}  滞后: ${lagText.value}`)
  lines.push(`门控: ${gateText.value}`)
  lines.push(`播放健康度: ${healthText.value}`)
  lines.push(`每边速率: ${edgeText.value}`)
  lines.push(`调度: ${scheduleText.value}`)
  lines.push(`播放器: ${playerText.value}`)
  lines.push(`媒体: ${mediaText.value}`)
  lines.push(`计数: ${runtimeText.value}`)
  lines.push(`内容校验: ${hashText.value}`)
  lines.push(`拓扑: ${topologyText.value}`)
  lines.push(`分配依据: ${topologyReason.value}`)
  lines.push(`ICE: ${iceText.value}`)
  lines.push(`IPv6: ${ipv6Text.value}`)
  lines.push(`选中候选对: ${selectedPairText.value}`)
  if (!includeFullAddress.value) {
    lines.push('（本机与对端地址已掩码；需要完整地址时勾选「包含完整地址（仅自己看）」后重新复制）')
  }
  for (const line of store.iceRestartLog) {
    lines.push(`  ICE restart ${line}`)
  }
  for (const peer of peers.value) {
    lines.push(
      `  节点 ${memberName(peer.id)}(${peer.id.slice(0, 8)}) 连接=${peer.connection} 通道=${peer.channelOpen ? '开' : '关'} rtt=${peer.rttMs}ms`,
    )
  }
  lines.push('')
  lines.push(`--- 取数失败（最近 ${store.fetchFailures.length} 条）---`)
  lines.push(...(store.fetchFailures.length ? store.fetchFailures : ['（无）']))
  lines.push('')
  lines.push(`--- 服务端应答（最近 ${store.serveLog.length} 条）---`)
  lines.push(...(store.serveLog.length ? store.serveLog : ['（无）']))
  lines.push('')
  lines.push(`--- 生命周期（最近 ${store.lifecycle.length} 条）---`)
  lines.push(...(store.lifecycle.length ? store.lifecycle : ['（无）']))
  return lines.join('\n')
})

async function copyReport() {
  const done = await copyText(report.value)
  copyState.value = done ? 'ok' : 'failed'
  if (timer !== undefined) window.clearTimeout(timer)
  timer = window.setTimeout(() => (copyState.value = 'idle'), 2500)
}

onBeforeUnmount(() => {
  if (timer !== undefined) window.clearTimeout(timer)
})
</script>

<template>
  <details class="diag" data-testid="diagnostics-drawer">
    <summary>
      <span>诊断 / 排障日志</span>
      <span class="muted tiny">（默认收起：这里的日志会持续刷新，避免刷屏）</span>
      <span class="muted tiny mono">
        连接 {{ store.connection }} · P2P {{ peers.length }} · 取数失败 {{ store.chunkErrors }}
      </span>
    </summary>

    <div class="body">
      <div class="toolbar">
        <button type="button" data-testid="diag-copy" @click="copyReport">复制诊断报告</button>
        <!--
          F-6：默认掩码本机/对端 IP（报告是要贴出去的，完整地址能定位到具体的人）。
          开关不写 localStorage：刷新技术性回到"掩码"这一安全默认。
        -->
        <label class="toggle" data-testid="diag-full-address-toggle">
          <input type="checkbox" v-model="includeFullAddress" />
          包含完整地址（仅自己看）
        </label>
        <span class="muted small" v-if="copyState === 'ok'">已复制，贴进反馈里即可。</span>
        <span class="muted small warn-text" v-else-if="copyState === 'failed'">
          剪贴板不可用（非 HTTPS 或被拒）：请手动选中下面的文本复制。
        </span>
        <span class="muted small" v-else>日志是环形缓冲，只保留最近几条。</span>
      </div>

      <p class="muted tiny warn-block" v-if="includeFullAddress" data-testid="diag-full-address-notice">
        已包含完整地址：这份报告里会有你的内网地址与对端的公网地址，只发给自己信任的人。
      </p>

      <div class="kv">
        <span class="k">房间连接</span>
        <span class="v">{{ connectionText }}<template v-if="!store.joined"> · 未加入房间</template></span>

        <span class="k">上游链路</span>
        <span class="v" :class="{ warn: unassigned }" data-testid="diag-upstream">{{ upstreamText }}</span>

        <span class="k">播放健康度</span>
        <span class="v mono small" data-testid="diag-health">{{ healthText }}</span>

        <span class="k">每边速率</span>
        <span class="v mono small" data-testid="diag-health-edges">{{ edgeText }}</span>

        <span class="k">调度</span>
        <span class="v mono small" data-testid="diag-health-schedule">{{ scheduleText }}</span>

        <span class="k">与主播的差</span>
        <span class="v" :class="{ warn: Math.abs(store.lagSec) > 2 && !store.isHost }">{{ lagText }}</span>

        <span class="k">门控</span>
        <span class="v">{{ gateText }}</span>

        <span class="k">时钟</span>
        <span class="v">{{ clockText }}</span>

        <span class="k">播放器</span>
        <span class="v">{{ playerText }}</span>

        <span class="k">媒体</span>
        <span class="v">{{ mediaText }}</span>

        <span class="k">计数</span>
        <span class="v">{{ runtimeText }}</span>

        <span class="k">内容校验</span>
        <span class="v mono small" data-testid="diag-hash">{{ hashText }}</span>

        <span class="k">拓扑</span>
        <span class="v">{{ topologyText }}</span>

        <span class="k">分配依据</span>
        <span class="v">{{ topologyReason }}</span>

        <span class="k">ICE 配置</span>
        <span class="v" data-testid="diag-ice">{{ iceText }}</span>

        <template v-if="iceFilterText">
          <span class="k">ICE 白名单</span>
          <span class="v mono small warn-text" data-testid="diag-ice-filter">{{ iceFilterText }}</span>
        </template>

        <span class="k">IPv6 直连</span>
        <span class="v" data-testid="diag-ipv6">{{ ipv6Text }}</span>

        <span class="k">选中候选对</span>
        <span class="v" data-testid="diag-selected-pair">{{ selectedPairText }}</span>

        <span class="k">节点</span>
        <span class="v">
          <template v-if="peers.length === 0">暂无 P2P 连接</template>
          <template v-else>
            <span v-for="peer in peers" :key="peer.id" class="peer mono">
              {{ memberName(peer.id) }} · {{ peer.connection }} ·
              {{ peer.channelOpen ? '通道开' : '通道关' }} · {{ peer.rttMs }}ms
            </span>
          </template>
        </span>
      </div>

      <div class="logs">
        <section>
          <h5>取数失败（{{ store.fetchFailures.length }}）</h5>
          <ul class="mono">
            <li v-for="line in [...store.fetchFailures].reverse()" :key="line">{{ line }}</li>
            <li v-if="store.fetchFailures.length === 0" class="muted">（无：这一路没有失败记录）</li>
          </ul>
        </section>

        <section>
          <h5>服务端应答（{{ store.serveLog.length }}）</h5>
          <ul class="mono">
            <li v-for="line in [...store.serveLog].reverse()" :key="line">{{ line }}</li>
            <li v-if="store.serveLog.length === 0" class="muted">（无）</li>
          </ul>
        </section>

        <section>
          <h5>生命周期（{{ store.lifecycle.length }}）</h5>
          <ul class="mono">
            <li v-for="line in [...store.lifecycle].reverse()" :key="line">{{ line }}</li>
            <li v-if="store.lifecycle.length === 0" class="muted">（无）</li>
          </ul>
        </section>
      </div>
    </div>
  </details>
</template>

<style scoped>
.diag {
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--panel);
  padding: 6px 10px;
}

summary {
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--text-dim);
}

summary .mono {
  margin-left: auto;
}

.body {
  margin-top: 8px;
  border-top: 1px dashed var(--border);
  padding-top: 8px;
}

/* 抽屉是房间页的页脚，展开时不能把播放器挤没：内部自己滚。 */
.diag[open] .body {
  max-height: 42vh;
  overflow: auto;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}

.toggle {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--text-dim);
  cursor: pointer;
  user-select: none;
}

.toggle input {
  margin: 0;
}

.warn-block {
  margin: 0 0 8px;
  color: var(--accent-2);
  font-size: 11.5px;
}

.kv {
  display: grid;
  grid-template-columns: 96px minmax(0, 1fr);
  gap: 4px 10px;
  font-size: 12px;
  line-height: 1.65;
}

.kv .k {
  color: var(--text-dim);
}

.kv .v {
  min-width: 0;
  word-break: break-word;
}

.kv .warn,
.warn-text {
  color: var(--accent-2);
}

.peer {
  display: inline-block;
  margin-right: 10px;
  white-space: nowrap;
}

.logs {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 10px;
  margin-top: 10px;
}

.logs h5 {
  margin: 0 0 4px;
  font-size: 12px;
  color: var(--text-dim);
}

.logs ul {
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 140px;
  overflow: auto;
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 6px 8px;
  font-size: 11.5px;
  line-height: 1.6;
}

.logs li {
  word-break: break-all;
}

.small {
  font-size: 12px;
}

.tiny {
  font-size: 11px;
}
</style>
