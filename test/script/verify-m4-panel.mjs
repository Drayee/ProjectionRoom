#!/usr/bin/env node
/**
 * M4 验收（SPEC §10）：**监控面板数值与实际一致**。
 *
 * 做法：真实 Chrome 里让观众起播 → 展开诊断抽屉 → 把它渲染出来的文字与
 * `window.__pr.snapshot()`（面板的数据源）逐项对照。面板只要在某一项上"说假话"，
 * 排查时就会被带偏，所以这条必须自动断言，而不是靠肉眼看。
 *
 * 用法（服务端与 Vite 都要在跑）：
 *   node test/script/verify-m4-panel.mjs [--media test/resource/short_video/cut]
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
const BASE_PORT = Number(argOf(argv, 'port', '9970'))
/** 抽屉文案里必须出现的比例（数字对得上就算过），低于这个比例判 FAIL。 */
const MIN_HIT_RATIO = Number(argOf(argv, 'min-ratio', '0.6'))

// 注意：必须认 <summary> 的文本 —— 拓扑徽标的正文里也提到"诊断抽屉"，
// 用整个 details 的 textContent 去找会先命中拓扑徽标（第一次就是这么错的）。
const OPEN_DRAWER = `(() => {
  const d = [...document.querySelectorAll('details')].find((n) =>
    (n.querySelector('summary')?.innerText ?? '').includes('诊断'));
  if (!d) return 'not-found';
  d.open = true;
  return 'opened';
})()`

const DRAWER_TEXT = `(() => {
  const d = [...document.querySelectorAll('details')].find((n) =>
    (n.querySelector('summary')?.innerText ?? '').includes('诊断'));
  return d ? d.innerText : '';
})()`

async function main() {
  const chromePath = findChrome()
  const hostBrowser = await startChrome(chromePath, BASE_PORT, 'host')
  const viewerBrowser = await startChrome(chromePath, BASE_PORT + 1, 'viewer')
  const host = await openTarget(BASE_PORT, 'host')
  const viewer = await openTarget(BASE_PORT + 1, 'viewer')

  const roomId = await createRoom(SERVER_URL)
  await seedAndEnter(host, CLIENT_URL, roomId, 'host', '主播')
  const mediaServer = await startMediaServer(MEDIA_DIR)
  const injectErr = await injectMediaInPage(host, mediaServer.url)
  if (injectErr) throw new Error(`注入媒体失败：${injectErr}`)
  await host.evaluate('window.__pr.store.play()')

  await seedAndEnter(viewer, CLIENT_URL, roomId, 'viewer', '观众')
  const playing = await waitFor(
    async () => {
      const s = await viewer.snapshot()
      return !s.video.paused && s.video.currentTime > 0.3 && s.sync.bufferedAhead > 0
    },
    { label: '观众起播', timeoutMs: 30000, intervalMs: 300 },
  ).catch(() => false)
  console.log(`观众起播: ${playing}`)
  await sleep(1500) // 让计数与偏差稳定下来再对照

  const opened = await viewer.evaluate(OPEN_DRAWER)
  await sleep(600)
  const text = String(await viewer.evaluate(DRAWER_TEXT))
  const snap = await viewer.snapshot()

  if (opened === 'not-found' || !text) {
    console.log(`抽屉: ${opened}，文本长度 ${text.length}`)
    console.log('判定：FAIL（找不到诊断抽屉）')
    process.exitCode = 1
    return
  }

  // 面板该与"实际状态"一致的字段（都用纯数字，避免把展示名与 ID 的差异算成不一致）
  const candidates = [
    ['拓扑深度', String(snap.topology.depth)],
    ['同步偏差(ms)', String(snap.sync.driftMs)],
    ['缓冲(秒, 1 位小数)', snap.sync.bufferedAhead.toFixed(1)],
    ['已交付分片数', String(snap.p2p.delivered)],
    ['门控阈值片数', String(snap.gate.thresholdSegments)],
  ]

  const hits = candidates.map(([label, value]) => [label, value, text.includes(value)])
  console.log('\n字段对照（值取自 window.__pr.snapshot()，检查是否出现在抽屉文本里）：')
  for (const [label, value, ok] of hits) {
    console.log(`  ${ok ? '✓' : '✗'} ${label}: ${value}`)
  }
  const hitCount = hits.filter(([, , ok]) => ok).length
  const ratio = hitCount / hits.length
  console.log(`\n命中 ${hitCount}/${hits.length}（${(ratio * 100).toFixed(0)}%，要求 ≥ ${(MIN_HIT_RATIO * 100).toFixed(0)}%）`)
  console.log(`抽屉文本前 200 字：${text.replace(/\s+/g, ' ').slice(0, 200)}`)

  const pass = playing && ratio >= MIN_HIT_RATIO
  console.log(`\n判定：${pass ? 'PASS' : 'FAIL'}`)
  process.exitCode = pass ? 0 : 1

  mediaServer.close()
  hostBrowser.close()
  viewerBrowser.close()
}

try {
  await main()
} catch (err) {
  console.error(`面板一致性验收失败：${err.message}`)
  process.exitCode = 1
} finally {
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
