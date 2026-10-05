#!/usr/bin/env node
/**
 * 诊断脚本：房主控制（播放/暂停）与"先入房后开播"的时序取证。
 *
 * 两个场景：
 *   --scenario control    主播播放 → 暂停 → 再播放，两端 200ms 采样，看谁在什么时候真正停下
 *   --scenario join-order 观众先进房（主播还没选片）→ 主播再开播，看观众能不能自己爬起来
 *
 * 用法（仓库根目录）：
 *   node test/script/diag-control.mjs [--scenario control|join-order] [--media test/resource/short_video/cut]
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
const SCENARIO = argOf(argv, 'scenario', 'control')
const BASE_PORT = Number(argOf(argv, 'port', '9800'))

const SAMPLE = `(() => {
  const s = window.__pr.snapshot();
  const store = window.__pr.store;
  return {
    videoPaused: s.video.paused,
    videoCt: Number(s.video.currentTime.toFixed(2)),
    statePaused: Boolean(store.playback.paused),
    stateCt: Number(Number(store.playback.currentTime ?? 0).toFixed(2)),
    mode: s.sync.mode,
    driftMs: s.sync.driftMs,
    buf: Number(s.sync.bufferedAhead.toFixed(1)),
    gated: s.gate.gated,
    segs: s.gate.bufferedSegments,
    thr: s.gate.thresholdSegments,
    delivered: s.p2p.delivered,
    failed: s.p2p.chunkErrors,
    mediaLoaded: s.media.loaded,
  };
})()`

async function sample(cdp) {
  return JSON.parse(await cdp.evaluate(`JSON.stringify(${SAMPLE})`))
}

async function timeline(label, host, viewer, seconds, stepMs = 200) {
  const rows = []
  for (let t = 0; t < seconds * 1000; t += stepMs) {
    const [h, v] = await Promise.all([sample(host), sample(viewer)])
    rows.push({ t, host: h, viewer: v })
    await sleep(stepMs)
  }
  console.log(`\n--- ${label} ---`)
  for (const row of rows) {
    console.log(
      `t=${String(row.t).padStart(4)}ms 主播[视频${row.host.videoPaused ? '停' : '播'}` +
        ` 状态${row.host.statePaused ? '停' : '播'} ct=${row.host.videoCt}]  ` +
        `观众[视频${row.viewer.videoPaused ? '停' : '播'} 状态${row.viewer.statePaused ? '停' : '播'}` +
        ` ct=${row.viewer.videoCt} mode=${row.viewer.mode} drift=${row.viewer.driftMs}ms buf=${row.viewer.buf}s` +
        ` 门控=${row.viewer.gated ? `${row.viewer.segs}/${row.viewer.thr}` : '开'}]`,
    )
  }
  return rows
}

async function main() {
  const chromePath = findChrome()
  const hostBrowser = await startChrome(chromePath, BASE_PORT, 'host')
  const viewerBrowser = await startChrome(chromePath, BASE_PORT + 1, 'viewer')
  const host = await openTarget(BASE_PORT, 'host')
  const viewer = await openTarget(BASE_PORT + 1, 'viewer')

  const roomId = await createRoom(SERVER_URL)
  console.log(`房间 ${roomId}  场景=${SCENARIO}`)
  await seedAndEnter(host, CLIENT_URL, roomId, 'host', '主播')

  const mediaServer = await startMediaServer(MEDIA_DIR)

  if (SCENARIO === 'join-order') {
    // 观众先入房：此时主播还没选片，房间没有任何媒体
    await seedAndEnter(viewer, CLIENT_URL, roomId, 'viewer', '观众')
    console.log('观众已先入房（主播尚未开播），等 3s…')
    await sleep(3000)
    console.log(`观众初态: ${JSON.stringify(await sample(viewer))}`)

    const err = await injectMediaInPage(host, mediaServer.url)
    if (err) throw new Error(`注入媒体失败：${err}`)
    await host.evaluate('window.__pr.store.play()')
    console.log('主播已开播，观察观众 18s…')
    await timeline('先入房后开播', host, viewer, 18, 2000)
    console.log(`\n观众末态: ${JSON.stringify(await sample(viewer))}`)
    mediaServer.close()
    return
  }

  // 场景 control：主播先开播，观众起播后再做暂停/播放
  const err = await injectMediaInPage(host, mediaServer.url)
  if (err) throw new Error(`注入媒体失败：${err}`)
  await host.evaluate('window.__pr.store.play()')
  await seedAndEnter(viewer, CLIENT_URL, roomId, 'viewer', '观众')

  const playing = await waitFor(
    async () => {
      const v = await sample(viewer)
      return !v.videoPaused && v.videoCt > 0.05 && !v.gated
    },
    { label: '观众起播', timeoutMs: 30000, intervalMs: 300 },
  ).catch(() => false)
  console.log(`观众起播: ${playing}`)

  await timeline('基线（都在播）', host, viewer, 1, 200)

  if (SCENARIO === 'native') {
    // 不用页面按钮，直接操作 <video>：模拟"主播用原生控件暂停"或浏览器自己把视频停下。
    // 这条路径以前完全不下发控制，观众就一直播下去。
    console.log('\n>>> 原生暂停（video.pause()，不经过页面按钮）')
    await host.evaluate("document.querySelector('video').pause(); 'ok'")
    await timeline('原生暂停后', host, viewer, 2, 200)
    console.log('\n>>> 原生播放（video.play()）')
    await host.evaluate("void document.querySelector('video').play(); 'ok'")
    await timeline('原生恢复后', host, viewer, 2, 400)
    mediaServer.close()
    return
  }

  console.log('\n>>> 主播暂停')
  await host.evaluate('window.__pr.store.pause()')
  await timeline('暂停后', host, viewer, 3, 200)
  console.log('\n>>> 主播播放')
  await host.evaluate('window.__pr.store.play()')
  await timeline('恢复后', host, viewer, 2, 400)

  mediaServer.close()
}

try {
  await main()
} catch (err) {
  console.error(`诊断失败：${err.message}`)
  process.exitCode = 1
} finally {
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
