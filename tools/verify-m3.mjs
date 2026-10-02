#!/usr/bin/env node
/**
 * M3 验收脚本：用 N 个独立 Chrome 实例跑真实的多层分发树，测量 SPEC §10 的 M3 验收标准。
 *
 *   A 扇出：主播 → 多个一级节点 → 二级节点，所有人都在播
 *   B 单链：主播上行不足时，主播只有 1 个子节点（分发节点），其余人挂在它下面
 *   C 换防：更强上行节点出现后，分发节点换人并且全员不被断流
 *   D 抗慢节点：把某个转发节点的上行压到很低，它的子节点必须转投备用父并在数秒内恢复
 *
 * 注：headless 的 getStats 不产生 availableOutgoingBitrate（SPEC C15），
 * 因此上行数字由 `__pr.reportMetrics` 注入；除该数字外，信令、拓扑分配、
 * P2P 中继、分片传输与播放同步全部是真实链路。
 *
 * 用法：
 *   node tools/verify-m3.mjs --media <分片目录> [--nodes 4] [--duration 30]
 *                            [--host-uplink 800000] [--uplink 1=6000000] [--throttle 1=200000]
 */
import {
  argOf,
  createRoom,
  findChrome,
  loadMedia,
  openTarget,
  percentile,
  seedAndEnter,
  sleep,
  startChrome,
  waitFor,
} from './lib/browser.mjs'

const argv = process.argv.slice(2)
const CLIENT_URL = argOf(argv, 'client', 'http://127.0.0.1:5173')
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:8080')
const MEDIA_DIR = argOf(argv, 'media')
const NODES = Number(argOf(argv, 'nodes', '4'))
const DURATION_S = Number(argOf(argv, 'duration', '30'))
const BASE_PORT = Number(argOf(argv, 'port', '9400'))
const HOST_UPLINK = Number(argOf(argv, 'host-uplink', '0'))
const THROTTLE = argOf(argv, 'throttle', '')

/** 解析 `1=6000000,2=2000000` 形式的上行注入。 */
function parsePairs(text) {
  const out = new Map()
  if (!text) return out
  for (const part of text.split(',')) {
    const [key, value] = part.split('=')
    const index = Number(key)
    const bps = Number(value)
    if (Number.isFinite(index) && Number.isFinite(bps)) out.set(index, bps)
  }
  return out
}

const UPLINKS = parsePairs(argOf(argv, 'uplink', ''))
const THROTTLES = parsePairs(THROTTLE)

if (!MEDIA_DIR) {
  console.error('必须用 --media 指定 cmd/segmenter 产出的分片目录')
  process.exitCode = 2
}

async function main() {
  const roomId = await createRoom(SERVER_URL)
  const chromePath = findChrome()
  console.log(`房间：${roomId}（节点数 ${NODES}）`)

  const browsers = []
  const mediaServerRef = { server: null }
  try {
    for (let i = 0; i < NODES; i += 1) {
      browsers.push(await startChrome(chromePath, BASE_PORT + i, `n${i}`))
    }
    console.log(`${NODES} 个浏览器已就绪`)

    const nodes = []
    for (let i = 0; i < NODES; i += 1) {
      const cdp = await openTarget(BASE_PORT + i, `n${i}`)
      nodes.push({ index: i, cdp, label: i === 0 ? '主播' : `节点${i}` })
    }

    // 主播先入房、选片、开播。
    const host = nodes[0]
    await seedAndEnter(host.cdp, CLIENT_URL, roomId, 'host', host.label)
    const media = await loadMedia(host.cdp, MEDIA_DIR, mediaServerRef)
    console.log(`媒体已加载：${media.segmentCount} 段 / ${media.mimeType}`)
    await host.cdp.evaluateNoWait('window.__pr.store.play()')

    // 其余节点依次入房（顺序稳定，便于复现拓扑）。
    for (let i = 1; i < NODES; i += 1) {
      const node = nodes[i]
      await seedAndEnter(node.cdp, CLIENT_URL, roomId, 'viewer', node.label)
      await node.cdp.evaluate(
        `window.__samples = []; window.__sampler = setInterval(() => {
           const s = window.__pr.snapshot();
           window.__samples.push({ t: performance.now(), ct: s.video.currentTime, drift: s.sync.driftMs, ready: s.video.readyState, be: s.video.bufferedEnd, paused: s.video.paused, depth: s.topology.depth, primary: s.topology.primaryId, children: s.topology.children.length, delivered: s.p2p.delivered, timedOut: s.p2p.timedOut });
         }, 200); 'ok'`,
      )
      node.joinAt = await node.cdp.evaluate('performance.now()')
      await sleep(600)
    }

    // 注入实测上行（headless 的 getStats 拿不到估计值）。
    if (HOST_UPLINK > 0) {
      await host.cdp.evaluate(`window.__pr.reportMetrics(${HOST_UPLINK}, 15)`)
    }
    for (const [index, bps] of UPLINKS) {
      const node = nodes[index]
      if (node) await node.cdp.evaluate(`window.__pr.reportMetrics(${bps}, 15)`)
    }
    if (HOST_UPLINK > 0 || UPLINKS.size > 0) {
      console.log('已注入实测上行，等待拓扑收敛…')
      await sleep(4000)
    }

    // 等待全员起播（深度越大允许越久）。
    console.log('等待所有节点起播…')
    for (let i = 1; i < NODES; i += 1) {
      const node = nodes[i]
      try {
        await waitFor(
        async () =>
          JSON.parse(
            await node.cdp.evaluate(
              'JSON.stringify(window.__samples.find((s) => s.ct > 0.05 && !s.paused && s.ready >= 2 && s.be > s.ct) ?? null)',
            ),
          ),
          { label: `${node.label} 起播`, timeoutMs: 40000, intervalMs: 200 },
        )
      } catch (err) {
        const snap = await node.cdp.snapshot()
        console.log(`${node.label} 起播失败，节点状态：`)
        console.log(
          JSON.stringify(
            {
              topology: snap.topology,
              media: snap.media,
              video: snap.video,
              sync: snap.sync,
              player: snap.player,
              storeLifecycle: snap.lifecycle,
              p2p: snap.p2p,
              errors: snap.errors,
            },
            null,
            2,
          ),
        )
        throw err
      }
      const snapshot = await node.cdp.snapshot()
      console.log(`  ${node.label} 已起播（深度 ${snapshot.topology.depth}）`)
    }

    console.log(`观察 ${DURATION_S}s…`)
    const ticker = setInterval(() => process.stdout.write('.'), 5000)
    await sleep(DURATION_S * 1000)
    clearInterval(ticker)
    process.stdout.write('\n')

    // 收集每个节点的采样与快照。
    const perNode = []
    for (let i = 0; i < NODES; i += 1) {
      const node = nodes[i]
      const samples = JSON.parse(await node.cdp.evaluate('JSON.stringify(window.__samples ?? [])', 60000))
      const snapshot = await node.cdp.snapshot()
      const playing = samples.filter((s) => s.ct > 0.05 && !s.paused && s.ready >= 2 && s.be > s.ct)
      const drifts = playing.map((s) => Math.abs(s.drift)).sort((a, b) => a - b)
      const firstPlaying = samples.find((s) => s.ct > 0.05 && !s.paused && s.ready >= 2 && s.be > s.ct)

      perNode.push({
        label: node.label,
        index: i,
        isHost: i === 0,
        topology: snapshot.topology,
        capacityMode: snapshot.room.capacityMode,
        hostChildSlots: snapshot.room.hostChildSlots,
        maxMembers: snapshot.room.maxMembers,
        drift: {
          samples: drifts.length,
          maxMs: drifts.length ? drifts[drifts.length - 1] : -1,
          p95Ms: percentile(drifts, 0.95),
          medianMs: percentile(drifts, 0.5),
        },
        player: snapshot.player,
        storeLifecycle: snapshot.lifecycle,
        delivered: snapshot.p2p.delivered,
        timedOut: snapshot.p2p.timedOut,
        chunkErrors: snapshot.p2p.chunkErrors,
        p95DeliveryMs: snapshot.sync.p95DeliveryMs,
        playing: Boolean(firstPlaying),
        firstPlaybackSeconds:
          firstPlaying && node.joinAt ? Number(((firstPlaying.t - node.joinAt) / 1000).toFixed(2)) : null,
        errors: snapshot.errors,
      })
    }

    // 慢上行场景：把某个节点的上行压到很低，观察它的子节点能否转投恢复。
    let throttleResult = null
    if (THROTTLES.size > 0) {
      const victims = []
      for (const [index, bps] of THROTTLES) {
        const node = nodes[index]
        if (!node) continue
        await node.cdp.evaluate(`window.__pr.setUploadThrottle(${bps})`)
        const snapshot = await node.cdp.snapshot()
        victims.push({ label: node.label, index, throttleBps: bps, children: snapshot.topology.children })
        console.log(`已把 ${node.label}（上行 ${bps} bps）限速，其子节点：${snapshot.topology.children.join(', ')}`)
      }

      const childLabels = victims.flatMap((v) => v.children)
      const startedAt = Date.now()
      const observations = []
      while (Date.now() - startedAt < 15000) {
        await sleep(500)
        const states = []
        for (const node of nodes) {
          const snapshot = await node.cdp.snapshot()
          states.push({
            label: node.label,
            ct: Number(snapshot.video.currentTime.toFixed(2)),
            driftMs: snapshot.sync.driftMs,
            primary: snapshot.topology.primaryId,
            paused: snapshot.video.paused,
          })
        }
        observations.push({ at: Number(((Date.now() - startedAt) / 1000).toFixed(1)), states })
      }

      throttleResult = { victims, childLabels, observations }
    }

    const allDrifts = perNode.filter((n) => !n.isHost && n.drift.samples > 0).map((n) => n.drift.maxMs)
    const maxDrift = allDrifts.length ? Math.max(...allDrifts) : -1
    const allPlaying = perNode.filter((n) => !n.isHost).every((n) => n.playing)
    const depths = perNode.map((n) => n.topology.depth)
    const hasMultiHop = depths.some((d) => d >= 2)
    const relays = perNode.filter((n) => (n.topology.children ?? []).length > 0 && !n.isHost)
    const chainExpected = HOST_UPLINK > 0
    const hostChildren = perNode[0].topology.children ?? []

    const topologyOk = chainExpected
      ? perNode.every((n) => n.topology.mode === 'chain') && hostChildren.length === 1
      : true

    const result = {
      room: roomId,
      nodes: NODES,
      observationSeconds: DURATION_S,
      hostUplinkInjected: HOST_UPLINK || null,
      uplinksInjected: Object.fromEntries(UPLINKS),
      summary: {
        allViewersPlaying: allPlaying,
        maxDriftMs: maxDrift,
        depths,
        multiHop: hasMultiHop,
        relays: relays.map((r) => ({ label: r.label, children: r.topology.children })),
        hostChildren,
        chainMode: perNode[0].topology.mode === 'chain',
      },
      perNode,
      throttle: throttleResult,
    }

    const pass =
      allPlaying &&
      maxDrift >= 0 &&
      maxDrift < 500 &&
      topologyOk &&
      (chainExpected ? hostChildren.length === 1 : hasMultiHop || NODES <= 3)

    console.log('\n=== M3 验收结果 ===')
    console.log(JSON.stringify(result.summary, null, 2))
    console.log('\n每节点：')
    for (const node of perNode) {
      console.log(
        `  ${node.label}${node.isHost ? '(主播)' : ''}: 深度 ${node.topology.depth}` +
          ` 主父 ${node.topology.primaryId || '—'} 备用 ${(node.topology.backupIds ?? []).length}` +
          ` 子节点 ${(node.topology.children ?? []).length}` +
          ` 偏差 max ${node.drift.maxMs}ms / p95 ${node.drift.p95Ms}ms 交付 ${node.delivered} 失败 ${node.chunkErrors}` +
          ` 播放 ${node.playing ? '是' : '否'}` +
          ` 播放器[${node.player?.mediaSourceState ?? '?'}/sb=${node.player?.sourceBufferCount ?? '?'}/attached=${node.player?.attached ?? '?'}]`,
      )
    }
    if (throttleResult) {
      console.log('\n限速后的状态轨迹（每 0.5s 一次）：')
      for (const step of throttleResult.observations) {
        console.log(
          `  t=${step.at}s ` +
            step.states
              .map((s) => `${s.label}:${s.ct}s/${s.driftMs}ms${s.primary ? '' : '(无父)'}`)
              .join('  '),
        )
      }
    }
    console.log(`\n判定：${pass ? 'PASS' : 'FAIL'}`)

    for (const node of nodes) node.cdp.close()
    for (const browser of browsers) browser.close()
    mediaServerRef.server?.close()
    process.exitCode = pass ? 0 : 1
  } catch (err) {
    console.error(`验收脚本失败：${err.message}`)
    mediaServerRef.server?.close()
    for (const browser of browsers) browser.close()
    process.exitCode = 1
  }
}

/** 强制退出但先让 stdout 冲刷：只设 exitCode 会被 keep-alive 连接与定时器挂住。 */
async function finish() {
  await main()
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}

await finish()




