#!/usr/bin/env node
/**
 * 诊断脚本：观众一直"缓冲中"、却没有任何一片缓存成功时，分别看两边到底发生了什么。
 *
 * 它不是验收脚本（不判定 PASS/FAIL），而是把两端的真实状态并列打印出来：
 * 连接是否建立、请求有没有发出去、有没有应答、门控卡在哪一步、播放器有没有报错。
 *
 * 用法（仓库根目录）：
 *   node test/script/diag-chunks.mjs [--media test/resource/short_video/cut] [--wait 12]
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
} from './lib/browser.mjs'

const argv = process.argv.slice(2)
const CLIENT_URL = argOf(argv, 'client', 'http://127.0.0.1:5173')
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:8080')
const MEDIA_DIR = argOf(argv, 'media', 'test/resource/short_video/cut')
const WAIT_S = Number(argOf(argv, 'wait', '12'))
const BASE_PORT = Number(argOf(argv, 'port', '9600'))

/** 两端共用的状态快照：把和"取不到分片"有关的量都抓出来。 */
const DIAG = `(() => {
  const s = window.__pr.snapshot();
  const store = window.__pr.store;
  const peers = [];
  for (const [id, peer] of store.peers.entries()) {
    peers.push({ id, channelOpen: Boolean(peer.channelOpen), connectionState: peer.connectionState ?? '' });
  }
  return {
    role: s.role,
    joined: s.joined,
    hostId: s.hostId,
    connection: s.connection,
    media: s.media,
    topology: s.topology,
    p2p: s.p2p,
    peers,
    gate: s.gate,
    video: s.video,
    player: s.player,
    errors: s.errors,
    lifecycle: s.lifecycle.slice(-10),
  };
})()`

const hostBrowsers = []
let mediaServer

async function main() {
  const chromePath = findChrome()
  const hostBrowser = await startChrome(chromePath, BASE_PORT, 'host')
  const viewerBrowser = await startChrome(chromePath, BASE_PORT + 1, 'viewer')
  hostBrowsers.push(hostBrowser, viewerBrowser)

  const host = await openTarget(BASE_PORT, 'host')
  const viewer = await openTarget(BASE_PORT + 1, 'viewer')

  const roomId = await createRoom(SERVER_URL)
  console.log(`房间 ${roomId}`)

  await seedAndEnter(host, CLIENT_URL, roomId, 'host', '主播')
  mediaServer = await startMediaServer(MEDIA_DIR)
  const injectErr = await injectMediaInPage(host, mediaServer.url)
  if (injectErr) throw new Error(`注入媒体失败：${injectErr}`)
  console.log(`主播媒体：${JSON.stringify((await host.snapshot()).media)}`)

  await host.evaluate('window.__pr.store.play()')
  await sleep(500)

  await seedAndEnter(viewer, CLIENT_URL, roomId, 'viewer', '观众')
  console.log(`观众已进房，观察 ${WAIT_S}s…`)
  await sleep(WAIT_S * 1000)

  const hostDiag = JSON.parse(await host.evaluate(`JSON.stringify(${DIAG})`))
  const viewerDiag = JSON.parse(await viewer.evaluate(`JSON.stringify(${DIAG})`))

  console.log('\n=== 主播 ===')
  console.log(JSON.stringify(hostDiag, null, 2))
  console.log('\n=== 观众 ===')
  console.log(JSON.stringify(viewerDiag, null, 2))

  console.log('\n=== 判读 ===')
  const openPeers = viewerDiag.peers.filter((p) => p.channelOpen).length
  console.log(`观众已建连接数: ${viewerDiag.peers.length}，其中 channelOpen: ${openPeers}`)
  console.log(`观众交付/超时/分片错误: ${viewerDiag.p2p.delivered}/${viewerDiag.p2p.timedOut}/${viewerDiag.p2p.chunkErrors}`)
  console.log(`观众取数失败原因:\n  ${(viewerDiag.p2p.fetchFailures ?? []).join('\n  ') || '（无）'}`)
  console.log(`主播应答记录:\n  ${(hostDiag.p2p.serveLog ?? []).join('\n  ') || '（无）'}`)
  console.log(`观众门控: gated=${viewerDiag.gate.gated} 连续分片=${viewerDiag.gate.bufferedSegments}/${viewerDiag.gate.thresholdSegments} 等待=${viewerDiag.gate.waitedSec}s，reason=${viewerDiag.gate.reason}`)
  console.log(`观众播放器: ${JSON.stringify(viewerDiag.player)}`)
  console.log(`观众错误: ${JSON.stringify(viewerDiag.errors)}`)
}

try {
  await main()
} catch (err) {
  console.error(`诊断失败：${err.message}`)
  process.exitCode = 1
} finally {
  mediaServer?.close()
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
