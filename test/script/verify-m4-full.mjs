#!/usr/bin/env node
/**
 * M4 验收（SPEC §10）：**房间满员必须返回 `ROOM_FULL`**。
 *
 * 为什么真机验：容量闸门是"实测上行 → K0 → 准入位"三段推导的结果，
 * 单测覆盖了推导，但"前端确实把最后一个观众挡在门外、并给出可读文案"只有真机能看到。
 *
 * 构造：素材 4.5 Mbps、主播上报 8 Mbps → K0 = floor(8×0.8/4.55) = 1 → 准入上限 1+K0 = 2
 *（主播 + 1 个观众）；第 2 个观众必须被拒。
 *
 * 用法（服务端与 Vite 都要在跑）：
 *   node test/script/verify-m4-full.mjs [--media test/resource/short_video/cut]
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
const HOST_UPLINK = Number(argOf(argv, 'host-uplink', '8000000'))
const BASE_PORT = Number(argOf(argv, 'port', '9980'))

async function main() {
  const chromePath = findChrome()
  const browsers = []
  const nodes = []
  for (let i = 0; i < 3; i += 1) {
    browsers.push(await startChrome(chromePath, BASE_PORT + i, `n${i}`))
    nodes.push(await openTarget(BASE_PORT + i, `n${i}`))
  }
  const [host, first, second] = nodes

  const roomId = await createRoom(SERVER_URL)
  console.log(`房间 ${roomId}，主播上报上行 ${HOST_UPLINK} bps`)
  await seedAndEnter(host, CLIENT_URL, roomId, 'host', '主播')

  const mediaServer = await startMediaServer(MEDIA_DIR)
  const injectErr = await injectMediaInPage(host, mediaServer.url)
  if (injectErr) throw new Error(`注入媒体失败：${injectErr}`)
  await host.evaluate(`window.__pr.reportMetrics(${HOST_UPLINK}, 15)`)
  await sleep(2500)

  const hostSnap = await host.snapshot()
  const maxMembers = hostSnap.room.maxMembers
  console.log(`容量: mode=${hostSnap.room.capacityMode} K0=${hostSnap.room.hostChildSlots} maxMembers=${maxMembers}`)

  await seedAndEnter(first, CLIENT_URL, roomId, 'viewer', '观众1')
  const firstJoined = (await first.snapshot()).joined
  console.log(`观众1 进房: ${firstJoined}`)

  // 第 2 个观众：应该被容量闸门挡住
  await seedAndEnter(second, CLIENT_URL, roomId, 'viewer', '观众2').catch(() => 'join-error')
  await sleep(1500)
  const secondSnap = await second.snapshot()
  const errorText = String(secondSnap.errors.last ?? '')
  console.log(`观众2 joined=${secondSnap.joined} 错误文案="${errorText}"`)

  const blocked = !secondSnap.joined && errorText.includes('房间已满')
  const capacityRight = maxMembers === 2
  console.log(`\n判定依据：容量=2 且被拒=${capacityRight && blocked}（maxMembers=${maxMembers} 被拒=${blocked}）`)
  const pass = firstJoined && capacityRight && blocked
  console.log(`判定：${pass ? 'PASS' : 'FAIL'}`)
  process.exitCode = pass ? 0 : 1

  mediaServer.close()
  for (const b of browsers) b.close()
}

try {
  await main()
} catch (err) {
  console.error(`ROOM_FULL 验收失败：${err.message}`)
  process.exitCode = 1
} finally {
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
