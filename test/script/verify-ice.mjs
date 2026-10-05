#!/usr/bin/env node
/**
 * ICE（STUN/TURN）配置的端到端验证：配置 → /api/rooms → pinia store → RTCPeerConnection。
 *
 * 为什么要单独验这一条：TURN 只有在"配置真的进了 PeerConnection 的 iceServers"时才可能生效。
 * 之前只验到"配置文件里有这几个字段"，最后一跳（构造 PC 时是否带上）没有证据 ——
 * 打洞失败时观众会卡在"缓冲 0 片"，而这类问题最难从现象倒推。
 *
 * 用法（先按下面这样起服务端，再跑脚本）：
 *   PR_ADDR=127.0.0.1:8094 PR_TURN_URLS=turn:127.0.0.1:3478 \
 *   PR_TURN_USER=demo PR_TURN_PASS=secret ./bin/projectionroom.exe
 *   node test/script/verify-ice.mjs --server http://127.0.0.1:8094 --client http://127.0.0.1:8094
 *
 * 判定：/api/rooms 返回的 iceServers 与浏览器里 PeerConnection 实际拿到的 iceServers
 * 都包含配置里的 TURN 条目 → PASS。注意这**只证明配置送达**，不证明 TURN 能中继媒体
 *（那需要一台真实 coturn）。
 */
import { argOf, createRoom, findChrome, openTarget, seedAndEnter, sleep, startChrome, startMediaServer, waitFor } from './lib/browser.mjs'

const argv = process.argv.slice(2)
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:8094')
const CLIENT_URL = argOf(argv, 'client', SERVER_URL)
const TURN_HINT = argOf(argv, 'turn', 'turn:')
/** 强制只走中继：把 host/srflx 候选全禁掉，能连通就只可能是 TURN 在转发。 */
const RELAY_ONLY = argv.includes('--relay-only')
const BASE_PORT = Number(argOf(argv, 'port', '9900'))

/** 在页面加载前挂钩 RTCPeerConnection，把每次构造的 iceServers 记下来。 */
const HOOK = `(() => {
  const relayOnly = ${RELAY_ONLY};
  const Original = window.RTCPeerConnection;
  window.__iceCaptured = [];
  window.RTCPeerConnection = class extends Original {
    constructor(config) {
      const cfg = Object.assign({}, config || {});
      // --relay-only：只允许 relay 候选。此时 DataChannel 还能建起来，
      // 就说明媒体确实是从 TURN 服务器转发的（没有别的路径可走）。
      if (relayOnly) cfg.iceTransportPolicy = 'relay';
      window.__iceCaptured.push(JSON.parse(JSON.stringify(cfg)));
      super(cfg);
    }
  };
})()`

async function main() {
  // 1) 服务端这一侧：创建房间的响应里就该带上 iceServers
  const roomResp = await fetch(`${SERVER_URL}/api/rooms`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  })
  const roomBody = await roomResp.json()
  const apiServers = roomBody.iceServers ?? []
  console.log(`/api/rooms -> HTTP ${roomResp.status}，iceServers=${JSON.stringify(apiServers)}`)

  // 2) 浏览器这一侧：PC 构造时到底拿到了什么
  const chrome = await startChrome(findChrome(), BASE_PORT, 'ice')
  const cdp = await openTarget(BASE_PORT, 'ice')
  await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source: HOOK })

  const roomId = roomBody.roomId
  await seedAndEnter(cdp, CLIENT_URL, roomId, 'host', '主播')
  await sleep(500)
  const storeServers = JSON.parse(await cdp.evaluate('JSON.stringify(window.__pr.store.iceServers ?? [])'))
  console.log(`store.iceServers=${JSON.stringify(storeServers)}`)

  // 3) 真正建连才会构造 PC：再进一个观众，主播与它之间就会建立 RTCPeerConnection
  const viewerBrowser = await startChrome(findChrome(), BASE_PORT + 1, 'ice-viewer')
  const viewer = await openTarget(BASE_PORT + 1, 'ice-viewer')
  await viewer.send('Page.addScriptToEvaluateOnNewDocument', { source: HOOK })
  await seedAndEnter(viewer, CLIENT_URL, roomId, 'viewer', '观众')
  await sleep(2500)

  const hostCaptured = JSON.parse(await cdp.evaluate('JSON.stringify(window.__iceCaptured ?? [])'))
  const viewerCaptured = JSON.parse(await viewer.evaluate('JSON.stringify(window.__iceCaptured ?? [])'))
  const captured = [...hostCaptured, ...viewerCaptured]
  console.log(`PeerConnection 收到的 ICE 配置（主播 ${hostCaptured.length} 次 / 观众 ${viewerCaptured.length} 次）`)
  if (captured.length > 0) {
    console.log(`  首次构造: ${JSON.stringify(captured[0])}`)
  }

  const flat = JSON.stringify({ apiServers, storeServers, captured })
  const hasTurn = TURN_HINT === 'turn:' ? flat.includes('"turn:') : flat.includes(TURN_HINT)
  const pcGotServers = captured.some((c) => Array.isArray(c.iceServers) && c.iceServers.length > 0)
  const apiToStore = JSON.stringify(apiServers) === JSON.stringify(storeServers)

  // --relay-only：连通性就是"中继可用"的证据（没有 host/srflx 候选可走）
  let relayed = null
  if (RELAY_ONLY) {
    relayed = await waitFor(
      async () => {
        const s = await viewer.snapshot()
        return s.p2p.openChannels >= 1
      },
      { label: '仅中继下建立通道', timeoutMs: 20000, intervalMs: 300 },
    ).catch(() => false)
    console.log(`仅中继模式下 DataChannel 建起: ${relayed}`)
  }

  console.log(
    `\n判定依据：api→store 一致=${apiToStore}  PC 拿到 iceServers=${pcGotServers}  含 TURN=${hasTurn}` +
      (RELAY_ONLY ? `  仅中继连通=${relayed}` : ''),
  )
  const pass = apiServers.length > 0 && apiToStore && pcGotServers && hasTurn && (!RELAY_ONLY || relayed === true)
  console.log(`判定：${pass ? 'PASS' : 'FAIL'}`)
  process.exitCode = pass ? 0 : 1

  cdp.close()
  viewerBrowser.close()
  chrome.close()
}

try {
  await main()
} catch (err) {
  console.error(`ICE 验证失败：${err.message}`)
  process.exitCode = 1
} finally {
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
