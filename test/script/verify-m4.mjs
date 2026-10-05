#!/usr/bin/env node
/**
 * M4 验收（SPEC §10）：**中途杀掉主父节点，子节点必须在 5s 内重挂载并恢复**。
 *
 * 为什么必须真机跑：这一条横跨服务端（成员离开 → 重算拓扑 → 下发）、
 * 信令（WS 断开检测）、客户端（换父 → 复位取数 → 重新开闸 → 起播）四段链路，
 * 任何一段慢了都会表现成"画面卡住"，而单测只能证明其中一段。
 *
 * 拓扑构造：3 个节点、主播上行 8 Mbps、素材 4.5 Mbps
 *   → K0 = floor(8×0.8/4.55) = 1 → 单链模式：主播 → 分发节点(v1) → 叶子(v2)
 * 然后**直接杀掉 v1 的整个浏览器进程**（等价于它掉线/崩溃），观测 v2。
 *
 * 用法（仓库根目录，服务端与 Vite 都要在跑）：
 *   node test/script/verify-m4.mjs [--media test/resource/short_video/cut]
 */
import {
  argOf,
  createRoom,
  findChrome,
  injectMediaInPage,
  openTarget,
  seedAndEnter,
  sleep,
  startChrome,
  startMediaServer,
  waitFor,
} from './lib/browser.mjs'

const argv = process.argv.slice(2)
const CLIENT_URL = argOf(argv, 'client', 'http://127.0.0.1:5173')
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:8080')
const MEDIA_DIR = argOf(argv, 'media', 'test/resource/short_video/cut')
const HOST_UPLINK = Number(argOf(argv, 'host-uplink', '8000000'))
const RECOVER_LIMIT_MS = Number(argOf(argv, 'limit-ms', '5000'))
const BASE_PORT = Number(argOf(argv, 'port', '9950'))

const isPlaying = (s) => !s.video.paused && s.video.currentTime > 0.05 && s.sync.bufferedAhead > 0

async function main() {
  const chromePath = findChrome()
  const browsers = []
  const nodes = []

  for (let i = 0; i < 3; i += 1) {
    const browser = await startChrome(chromePath, BASE_PORT + i, `n${i}`)
    browsers.push(browser)
    nodes.push(await openTarget(BASE_PORT + i, `n${i}`))
  }
  const [host, relay, leaf] = nodes

  const roomId = await createRoom(SERVER_URL)
  console.log(`房间 ${roomId}`)
  await seedAndEnter(host, CLIENT_URL, roomId, 'host', '主播')

  const mediaServer = await startMediaServer(MEDIA_DIR)
  const injectErr = await injectMediaInPage(host, mediaServer.url)
  if (injectErr) throw new Error(`注入媒体失败：${injectErr}`)
  await host.evaluate('window.__pr.store.play()')

  await seedAndEnter(relay, CLIENT_URL, roomId, 'viewer', '中继')
  await seedAndEnter(leaf, CLIENT_URL, roomId, 'viewer', '叶子')

  // 主播上报 8 Mbps → K0=1 → 单链：主播只连 v1，v2 挂在 v1 下（深度 2）
  await host.evaluate(`window.__pr.reportMetrics(${HOST_UPLINK}, 15)`)
  await relay.evaluate('window.__pr.reportMetrics(8000000, 15)')
  await sleep(4000)

  const relayId = (await relay.snapshot()).clientId
  const leafTopo = (await leaf.snapshot()).topology
  console.log(`拓扑: 叶子深度=${leafTopo.depth} 主父=${leafTopo.primaryId} 中继=${relayId} 模式=${leafTopo.mode}`)

  const playing = await waitFor(async () => isPlaying(await leaf.snapshot()), {
    label: '叶子起播',
    timeoutMs: 30000,
    intervalMs: 300,
  }).catch(() => false)
  console.log(`叶子起播: ${playing}`)

  if (!playing) throw new Error('叶子没起播，无法验证杀父恢复')
  if (leafTopo.primaryId !== relayId) {
    console.log(`提示：叶子当前主父不是中继（${leafTopo.primaryId}），杀掉中继未必影响它 —— 仍然继续观测`)
  }

  // 动手：杀掉中继的整个浏览器（等价于该节点掉线/崩溃）
  console.log('\n>>> 杀掉主父（中继节点）的浏览器进程')
  const beforeCt = (await leaf.snapshot()).video.currentTime
  const t0 = Date.now()
  browsers[1].close()

  let recoveredAt = null
  let lastTopo = null
  while (Date.now() - t0 < RECOVER_LIMIT_MS + 5000) {
    await sleep(200)
    const snap = await leaf.snapshot()
    lastTopo = snap.topology
    const ct = snap.video.currentTime
    if (lastTopo.primaryId !== relayId && isPlaying(snap) && ct > beforeCt) {
      recoveredAt = Date.now() - t0
      break
    }
  }

  const el = (recoveredAt ?? Date.now() - t0) / 1000
  console.log(`新主父: ${lastTopo?.primaryId ?? '?'}（原 ${relayId}）`)
  console.log(`恢复耗时: ${recoveredAt === null ? '未恢复' : `${el.toFixed(2)}s`}（要求 ≤ ${RECOVER_LIMIT_MS / 1000}s）`)

  const pass = recoveredAt !== null && recoveredAt <= RECOVER_LIMIT_MS
  console.log(`\n判定：${pass ? 'PASS' : 'FAIL'}`)
  process.exitCode = pass ? 0 : 1

  mediaServer.close()
  for (const b of [browsers[0], browsers[2]]) b.close()
}

try {
  await main()
} catch (err) {
  console.error(`M4 验收失败：${err.message}`)
  process.exitCode = 1
} finally {
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
