#!/usr/bin/env node
/**
 * 断线恢复验收脚本（缺陷 1 + 缺陷 2）：真实 Chrome（CDP）+ 真实 Go 服务端二进制。
 *
 * 验的是三件"只存在于运行期"的事：
 *   1. **主播信令断线不再等于房间永久销毁**：主播掐线后，观众进入「主播掉线，等待重连」
 *      而不是收到房间销毁；服务端侧房间仍然存在（GET /api/rooms 是权威判据）。
 *   2. **恢复后房间码不变、播放继续跟随**：主播重连回到**同一个房间码**，
 *      观众退出等待态并重新起播，偏移仍满足 SPEC §10 的 <500ms 口径。
 *   3. **反例**：断线超过服务端宽限期（`PR_ROOM_HOST_GRACE`）→ 服务端广播 room-closed、
 *      房间销毁、客户端显示**权威原因**；主播回来后用同一个房间码自动重建房间。
 *
 * 另外两条确定性检查：
 *   A. 房间码复制：非安全上下文（navigator.clipboard 不存在，内网穿透的 http 域名就是这个形态）
 *      时必须走 textarea + execCommand 兜底并显示「已复制」；两条路都失败时必须显示
 *      「复制失败，请手动选中」，且房间码本身仍可手动选中。
 *   D. 重复 clientId：两个页面用同一个 clientId 进房，服务端下发 CLIENT_ID_TAKEN，
 *      客户端必须立刻换一个新 clientId 并重新进房（不干等旧连接被收尸）。
 *
 * 用法（在仓库根目录执行；服务端用**自己构建的真实二进制**、自己的端口，
 * 不要重启别人正在跑的 8080）：
 *
 *   go build -o %TEMP%\pr-resume\projectionroom.exe ./cmd
 *   set PR_ADDR=127.0.0.1:8090 & set PR_ROOM_HOST_GRACE=20s & %TEMP%\pr-resume\projectionroom.exe
 *   npm --prefix client run dev            # 另开一个终端，必要时 PR_SERVER_URL 指到 8090
 *   node test/script/verify-room-resume.mjs --server http://127.0.0.1:8090 \
 *        --client http://127.0.0.1:5173 --grace 20
 *
 * 参数：
 *   --server  测试服务端（默认 http://127.0.0.1:8080）
 *   --client  客户端入口（默认 http://127.0.0.1:5173；也可以是服务端的静态托管地址）
 *   --media   分片目录（默认 test/resource/short_video/cut）
 *   --grace   服务端 PR_ROOM_HOST_GRACE 的秒数（默认 20）：
 *             正例断网取 min(5, grace/3) 秒（必须留在宽限期内，还要给客户端
 *             1/2/4/8s 的指数退避留出余量）；反例断网取 grace + 4 秒。
 *   --observe 恢复后观察偏移的秒数（默认 8）
 *   --port    第一个 Chrome 的调试端口（默认 9500，另两个用 +1 / +2）
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
const MEDIA_DIR = argOf(argv, 'media', 'test/resource/short_video/cut')
const GRACE_S = Number(argOf(argv, 'grace', '20'))
const OBSERVE_S = Number(argOf(argv, 'observe', '8'))
const BASE_PORT = Number(argOf(argv, 'port', '9500'))
/** 正例的断网时长：留在宽限期内（还要给客户端的指数退避留出恢复余量）。 */
const OUTAGE_S = Math.max(2, Math.min(5, Math.floor(GRACE_S / 3)))
/** 反例的断网时长：必须**大于**宽限期。 */
const OUTAGE_LONG_S = GRACE_S + 4

// ---------- 判据收集 ----------
const checks = []
const report = { server: SERVER_URL, client: CLIENT_URL, graceSeconds: GRACE_S }
function record(name, pass, detail) {
  checks.push({ name, pass: Boolean(pass), detail })
  console.log(`[${pass ? 'PASS' : 'FAIL'}] ${name} — ${detail}`)
}

/**
 * 观众的"真实起播"口径（与 verify-m2 一致）：不是空 MediaSource 上的假 seek。
 * 样本是页面采样器 push 的**扁平**形状（ct/paused/ready/be）。
 */
function isRealSample(s) {
  return s.ct > 0.05 && !s.paused && s.ready >= 2 && s.be > s.ct
}

async function roomInfo(roomId) {
  const resp = await fetch(`${SERVER_URL}/api/rooms/${encodeURIComponent(roomId)}`)
  if (resp.status === 404) return { exists: false }
  if (!resp.ok) return { exists: false, http: resp.status }
  return await resp.json()
}

/** 把某个页面"断网"，并确认它确实退出了房间。 */
async function dropNetwork(cdp, label) {
  await cdp.send('Network.enable')
  await cdp.send('Network.emulateNetworkConditions', {
    offline: true,
    latency: 0,
    downloadThroughput: -1,
    uploadThroughput: -1,
  })
  let mode = null
  // 先看 CDP 的离线仿真是否真的拆掉了已建立的 WebSocket（很多 Chrome 上不会）。
  try {
    await waitFor(async () => (await cdp.snapshot()).connection === 'closed', {
      label: `${label} CDP 离线后信令断开`,
      timeoutMs: 1200,
      intervalMs: 100,
    })
    mode = 'cdp-offline'
  } catch {
    mode = null
  }
  if (mode) return mode

  // 确定性路径：先把离线仿真撤掉 —— Chrome 的 offline 会让 close 帧也发不出去，
  // 服务端迟迟看不到断开，宽限期就不是从"现在"开始算了。
  // 然后按住信令：断开当前连接 + 之后的连接尝试一律挡下（退避链照常推进），
  // 等价于"这个页面连不上信令服务"，但断开是干净且立刻可见的。
  await cdp.send('Network.emulateNetworkConditions', {
    offline: false,
    latency: 0,
    downloadThroughput: -1,
    uploadThroughput: -1,
  })
  await cdp.evaluate('window.__pr.holdSignaling(true)')
  await waitFor(
    async () => {
      const s = await cdp.snapshot()
      return s.connection !== 'open' && s.joined === false ? s : false
    },
    { label: `${label} 已退出房间（connection != open）`, timeoutMs: 5000, intervalMs: 100 },
  )
  return 'hold-hook'
}

async function restoreNetwork(cdp) {
  await cdp.send('Network.emulateNetworkConditions', {
    offline: false,
    latency: 0,
    downloadThroughput: -1,
    uploadThroughput: -1,
  })
  // 放开信令：立刻重连（等价于网络恢复后浏览器马上重试）。
  await cdp.evaluate('window.__pr.holdSignaling(false)')
}

// ---------- A. 房间码复制（缺陷 2）----------
/**
 * 页面里的三次确定性检查：
 *   1) navigator.clipboard 不存在（内网穿透的 http 域名）→ 必须走 textarea 兜底；
 *   2) 兜底成功 → 出现「已复制」，且交给 execCommand 的正是房间码；
 *   3) 两条路都失败 → 必须出现「复制失败，请手动选中」（不能再静默），房间码仍可选中。
 */
async function checkClipboard(cdp, roomId) {
  const script = `(async () => {
    const out = { execCalls: [], copiedText: '', okText: '', failText: '', selectable: '', roomCode: '' };
    const btn = document.querySelector('[data-testid="room-code"]');
    if (!btn) return JSON.stringify({ error: '找不到房间码按钮' });
    const codeEl = btn.querySelector('.code-text');
    out.roomCode = codeEl ? codeEl.textContent.trim() : '';
    out.selectable = codeEl ? getComputedStyle(codeEl).userSelect : 'missing';

    // 非安全上下文：navigator.clipboard 是 undefined
    const desc = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    const origExec = document.execCommand.bind(document);

    // 兜底可用：execCommand('copy') 返回 true
    document.execCommand = (cmd) => {
      out.execCalls.push(cmd);
      const el = document.activeElement;
      out.copiedText = el && 'value' in el ? String(el.value) : '';
      return true;
    };
    btn.click();
    await new Promise((r) => setTimeout(r, 200));
    out.okText = ((document.querySelector('[data-testid="room-code-copied"]') || {}).textContent || '').trim();

    // 两条路都失败：等上一次提示复位后再点
    document.execCommand = () => false;
    await new Promise((r) => setTimeout(r, 2300));
    btn.click();
    await new Promise((r) => setTimeout(r, 200));
    out.failText = ((document.querySelector('[data-testid="room-code-copy-failed"]') || {}).textContent || '').trim();

    document.execCommand = origExec;
    if (desc) Object.defineProperty(navigator, 'clipboard', desc);
    else delete navigator.clipboard;
    return JSON.stringify(out);
  })()`
  const out = JSON.parse(await cdp.evaluate(script))
  const want = roomId.toUpperCase()
  if (out.error) {
    record('A1 非安全上下文走 textarea 兜底并显示「已复制」', false, out.error)
    record('A2 两条路都失败时显示「复制失败，请手动选中」', false, out.error)
    record('A3 房间码本身仍是可选中文本', false, out.error)
    return out
  }
  record(
    'A1 非安全上下文走 textarea 兜底并显示「已复制」',
    out.okText.includes('已复制') && out.execCalls.includes('copy') && out.copiedText === want,
    `execCommand=${JSON.stringify(out.execCalls)} 兜底文本=${JSON.stringify(out.copiedText)} 期望=${want} 按钮文案=${JSON.stringify(out.okText)}`,
  )
  record(
    'A2 两条路都失败时显示「复制失败，请手动选中」',
    out.failText.includes('复制失败') && out.failText.includes('手动选中'),
    `按钮文案=${JSON.stringify(out.failText)}`,
  )
  record(
    'A3 房间码本身仍是可选中文本',
    out.selectable === 'text' && out.roomCode === want,
    `user-select=${out.selectable} 文本=${JSON.stringify(out.roomCode)}`,
  )
  return out
}

// ---------- D. 重复 clientId ----------
/** 由 clientId 反推出"客户端第一次 randomUUID 会拿到的 uuid"。 */
function uuidForClientId(clientId) {
  const hex = clientId.replace(/^c_/, '').padEnd(16, '0').slice(0, 16)
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-8000-000000000000`
}

async function main() {
  const chromePath = findChrome()
  const browsers = []
  const mediaServerRef = { server: null }
  let host = null
  let viewer = null
  let dup = null

  const cleanup = () => {
    try {
      mediaServerRef.server?.close()
    } catch {
      // 忽略
    }
    for (const cdp of [host, viewer, dup]) {
      try {
        cdp?.close()
      } catch {
        // 忽略
      }
    }
    for (const browser of browsers) browser.close()
  }

  try {
    const roomId = await createRoom(SERVER_URL)
    report.room = roomId
    report.outageSeconds = OUTAGE_S
    report.outageLongSeconds = OUTAGE_LONG_S
    console.log(
      `房间：${roomId}（服务端 ${SERVER_URL}，客户端 ${CLIENT_URL}）\n` +
        `宽限期 ${GRACE_S}s · 正例断网 ${OUTAGE_S}s · 反例断网 ${OUTAGE_LONG_S}s`,
    )
    if (GRACE_S < 15) {
      console.log(
        `  ⚠ 宽限期 ${GRACE_S}s 偏小：客户端重连是指数退避（1/2/4/8s，8s 封顶），\n` +
          `    正例要稳定通过建议 grace ≥ 断网时长 + 12s（推荐 --grace 20）。`,
      )
    }

    browsers.push(await startChrome(chromePath, BASE_PORT, 'host'))
    browsers.push(await startChrome(chromePath, BASE_PORT + 1, 'viewer'))
    browsers.push(await startChrome(chromePath, BASE_PORT + 2, 'dup'))
    host = await openTarget(BASE_PORT, 'host')
    viewer = await openTarget(BASE_PORT + 1, 'viewer')
    dup = await openTarget(BASE_PORT + 2, 'dup')
    console.log('三个浏览器已就绪')

    // ---------- 准备：主播开播 + 观众在播 ----------
    await seedAndEnter(host, CLIENT_URL, roomId, 'host', '主播')
    const media = await loadMedia(host, MEDIA_DIR, mediaServerRef)
    console.log(`媒体已加载：${media.segmentCount} 段 / ${media.mimeType}`)

    await seedAndEnter(viewer, CLIENT_URL, roomId, 'viewer', '观众')
    await viewer.evaluate(
      `window.__samples = []; window.__sampler = setInterval(() => {
         const s = window.__pr.snapshot();
         window.__samples.push({
           t: performance.now(), ct: s.video.currentTime, drift: s.sync.driftMs, mode: s.sync.mode,
           paused: s.video.paused, ready: s.video.readyState, be: s.video.bufferedEnd,
           conn: s.connection, joined: s.joined, hostOffline: s.recovery.hostOffline,
           hostOfflineText: s.recovery.hostOfflineText, roomClosed: s.recovery.roomClosed,
           resume: s.recovery.resumeNotice, unrecoverable: s.recovery.unrecoverable,
           roomCode: s.roomId, clientId: s.clientId,
         });
       }, 200); 'ok'`,
    )
    const joinAt = await viewer.evaluate('performance.now()')
    await host.evaluateNoWait('window.__pr.store.play()')

    const firstPlay = await waitFor(
      async () => {
        const found = JSON.parse(
          await viewer.evaluate(
            `JSON.stringify(window.__samples.find((s) => s.ct > 0.05 && !s.paused && s.ready >= 2 && s.be > s.ct) ?? null)`,
          ),
        )
        return found || false
      },
      { label: '观众起播（需真实缓冲）', timeoutMs: 40000, intervalMs: 200 },
    )
    report.startSeconds = Number(((firstPlay.t - joinAt) / 1000).toFixed(2))
    console.log(`观众起播耗时：${report.startSeconds}s`)

    // ---------- A. 房间码复制（缺陷 2）----------
    console.log('\n=== A. 房间码复制 ===')
    await checkClipboard(viewer, roomId)

    // ---------- B. 正例：主播断线（未超宽限期）→ 恢复 ----------
    console.log('\n=== B. 正例：主播断线（未超宽限期）→ 恢复 ===')
    try {
      const hostClientIdBefore = (await host.snapshot()).clientId
      report.dropMode = await dropNetwork(host, '主播')
      const droppedAt = Date.now()

      const waited = await waitFor(
        async () => {
          const s = await viewer.snapshot()
          return s.recovery.hostOffline ? s : false
        },
        { label: '观众进入主播掉线等待态', timeoutMs: Math.max(6000, GRACE_S * 1000), intervalMs: 150 },
      )
      report.hostOfflineLatencyMs = Date.now() - droppedAt
      const viewerBodyText = await viewer.evaluate('document.body.innerText')
      const infoDuringOutage = await roomInfo(roomId)

      record(
        'B1 观众进入「主播掉线，等待重连」等待态',
        waited.recovery.hostOfflineText.includes('主播掉线'),
        `文案=${JSON.stringify(waited.recovery.hostOfflineText)}（掐线方式 ${report.dropMode}，进入等待耗时 ${report.hostOfflineLatencyMs}ms）`,
      )
      record(
        'B2 等待态里**没有**出现「房间不存在」',
        !viewerBodyText.includes('房间不存在'),
        `页面文本里「房间不存在」=${viewerBodyText.includes('房间不存在')}`,
      )
      record(
        'B3 服务端侧房间仍然存在（没有被销毁）',
        infoDuringOutage.exists === true,
        `GET /api/rooms/${roomId} → ${JSON.stringify(infoDuringOutage)}`,
      )

      // 断线期间观众画面必须停住（不是拿外推时钟假播放）
      const pauseSamples = []
      for (let i = 0; i < 6; i += 1) {
        await sleep(150)
        pauseSamples.push(await viewer.snapshot())
      }
      const pausedNow = pauseSamples.filter((s) => s.video.paused).length
      const moved =
        pauseSamples[pauseSamples.length - 1].video.currentTime - pauseSamples[0].video.currentTime
      record(
        'B4 断线期间观众画面停住（不假播放）',
        pausedNow >= 4 && Math.abs(moved) < 0.05,
        `paused=${pausedNow}/${pauseSamples.length} 进度差=${moved.toFixed(3)}s`,
      )

      // 把断网时长补足到 OUTAGE_S（断言跑得比预期快得多，不留这段就只测了瞬时抖动）
      const elapsed = Date.now() - droppedAt
      if (elapsed < OUTAGE_S * 1000) await sleep(OUTAGE_S * 1000 - elapsed)
      report.positiveOutageMs = Date.now() - droppedAt
      await restoreNetwork(host)
      const restoredAt = Date.now()
      console.log(`  正例实际断网 ${report.positiveOutageMs}ms（宽限期 ${GRACE_S}s，必须小于它）`)

      const hostBack = await waitFor(
        async () => {
          const s = await host.snapshot()
          return s.joined && s.roomId === roomId ? s : false
        },
        { label: '主播重连回到同一房间码', timeoutMs: 30000, intervalMs: 150 },
      )
      report.hostReconnectMs = Date.now() - restoredAt
      record(
        'B5 主播恢复后回到**同一个**房间码',
        hostBack.roomId === roomId && hostBack.joined === true,
        `roomId=${hostBack.roomId}（期望 ${roomId}），重连耗时 ${report.hostReconnectMs}ms，` +
          `clientId ${hostClientIdBefore}→${hostBack.clientId}（换号 ${hostBack.recovery.clientIdRotations} 次）`,
      )

      const hostState = await host.snapshot()
      const viewerState = await viewer.snapshot()
      const infoAlive = await roomInfo(roomId)
      record(
        'B6 正例全程房间没有被销毁（没人看到 room-closed，主播没走重建）',
        hostState.recovery.roomClosed === '' &&
          viewerState.recovery.roomClosed === '' &&
          hostState.recovery.rebuildState === 'idle' &&
          infoAlive.exists === true &&
          infoAlive.hasHost === true,
        `host.roomClosed=${JSON.stringify(hostState.recovery.roomClosed)} viewer.roomClosed=${JSON.stringify(viewerState.recovery.roomClosed)} ` +
          `host.rebuildState=${hostState.recovery.rebuildState} GET /api/rooms → ${JSON.stringify(infoAlive)}`,
      )

      const offlineCleared = await waitFor(
        async () => {
          const s = await viewer.snapshot()
          return s.recovery.hostOffline === false && s.joined === true ? s : false
        },
        { label: '观众退出等待态并重新进房', timeoutMs: 20000, intervalMs: 150 },
      )
      record(
        'B7 主播回来后观众的等待态自动消失',
        offlineCleared.recovery.hostOffline === false && offlineCleared.joined === true,
        `hostOffline=${offlineCleared.recovery.hostOffline} joined=${offlineCleared.joined}`,
      )

      const resumed = await waitFor(
        async () => {
          const samples = JSON.parse(
            await viewer.evaluate('JSON.stringify(window.__samples.slice(-15))'),
          )
          return samples.some(isRealSample) ? samples : false
        },
        { label: '观众恢复播放', timeoutMs: 40000, intervalMs: 250 },
      )
      record(
        'B8 恢复后观众重新起播',
        resumed.some(isRealSample),
        `最近样本里真实播放 ${resumed.filter(isRealSample).length}/${resumed.length}`,
      )

      // 恢复后观察偏移（与 verify-m2 同一口径）
      await viewer.evaluate('window.__samples = []')
      await sleep(OBSERVE_S * 1000)
      const after = JSON.parse(await viewer.evaluate('JSON.stringify(window.__samples)'))
      const playing = after.filter(isRealSample)
      const drifts = playing.map((s) => Math.abs(s.drift)).sort((a, b) => a - b)
      report.driftAfterResume = {
        samples: playing.length,
        total: after.length,
        maxMs: drifts.length ? drifts[drifts.length - 1] : -1,
        p95Ms: percentile(drifts, 0.95),
        medianMs: percentile(drifts, 0.5),
      }
      record(
        'B9 恢复后播放继续跟随（偏移 <500ms）',
        report.driftAfterResume.samples > 0 &&
          report.driftAfterResume.maxMs >= 0 &&
          report.driftAfterResume.maxMs < 500,
        `播放样本 ${report.driftAfterResume.samples}/${report.driftAfterResume.total} · ` +
          `偏差 max ${report.driftAfterResume.maxMs}ms / p95 ${report.driftAfterResume.p95Ms}ms`,
      )
      record(
        'B10 恢复后房间码不变且房间仍存在',
        (await host.snapshot()).roomId === roomId && (await roomInfo(roomId)).exists === true,
        `GET /api/rooms/${roomId} → ${JSON.stringify(await roomInfo(roomId))}`,
      )
    } catch (err) {
      record('B* 正例流程完整执行', false, `中断：${err.message}`)
    }

    // ---------- C. 反例：断线超过宽限期 ----------
    console.log('\n=== C. 反例：断线超过宽限期（服务端权威关闭）===')
    try {
      await dropNetwork(host, '主播')
      const closedAt = Date.now()
      const closedSnap = await waitFor(
        async () => {
          const s = await viewer.snapshot()
          return s.recovery.roomClosed ? s : false
        },
        { label: '观众收到 room-closed', timeoutMs: (GRACE_S + 15) * 1000, intervalMs: 250 },
      )
      const closedAfterMs = Date.now() - closedAt
      const closedBodyText = await viewer.evaluate('document.body.innerText')
      const infoAfterClose = await roomInfo(roomId)

      record(
        'C1 超过宽限期后服务端广播 room-closed（客户端拿到权威原因）',
        Boolean(closedSnap.recovery.roomClosed),
        `原因=${JSON.stringify(closedSnap.recovery.roomClosed)}（断网后 ${closedAfterMs}ms，宽限期 ${GRACE_S}s）`,
      )
      record(
        'C2 权威原因出现在页面上',
        closedBodyText.includes(closedSnap.recovery.roomClosed),
        `页面文本包含原因=${closedBodyText.includes(closedSnap.recovery.roomClosed)}`,
      )
      record(
        'C3 服务端侧房间已被销毁',
        infoAfterClose.exists === false,
        `GET /api/rooms/${roomId} → ${JSON.stringify(infoAfterClose)}`,
      )
      const viewerNotice = await viewer.snapshot()
      const hasExplicit =
        viewerNotice.recovery.roomClosed !== '' ||
        viewerNotice.recovery.resumeNotice !== '' ||
        viewerNotice.recovery.unrecoverable !== ''
      record(
        'C4 观众侧有明确文案（权威关闭原因 / 等待主播重建 / 失效）',
        hasExplicit,
        `roomClosed=${JSON.stringify(viewerNotice.recovery.roomClosed)} ` +
          `resume=${JSON.stringify(viewerNotice.recovery.resumeNotice)} ` +
          `unrecoverable=${JSON.stringify(viewerNotice.recovery.unrecoverable)}`,
      )

      // 主播回来 → 用同一个房间码自动重建
      await restoreNetwork(host)
      const rebuiltAt = Date.now()
      const rebuilt = await waitFor(
        async () => {
          const s = await host.snapshot()
          if (!s.joined || s.roomId !== roomId) return false
          const info = await roomInfo(roomId)
          return info.exists && info.hasHost ? { snap: s, info } : false
        },
        { label: '主播自动重建房间并重新进房', timeoutMs: 40000, intervalMs: 250 },
      )
      report.rebuildMs = Date.now() - rebuiltAt
      report.hostLifecycle = JSON.parse(await host.evaluate('JSON.stringify(window.__pr.snapshot().lifecycle)'))
      record(
        'C5 宽限期过后主播用同一个房间码自动重建并重新进房',
        rebuilt.snap.roomId === roomId && rebuilt.snap.joined === true && rebuilt.info.hasHost === true,
        `重建耗时 ${report.rebuildMs}ms · GET /api/rooms/${roomId} → ${JSON.stringify(rebuilt.info)}`,
      )
      record(
        'C6 主播重建后重新发布了分片索引（服务端 hasMedia）',
        rebuilt.info.hasMedia === true,
        `hasMedia=${rebuilt.info.hasMedia} · lifecycle=${JSON.stringify(report.hostLifecycle)}`,
      )
    } catch (err) {
      record('C* 反例流程完整执行', false, `中断：${err.message}`)
    }

    // ---------- D. 重复 clientId ----------
    console.log('\n=== D. 重复 clientId（CLIENT_ID_TAKEN）===')
    let dupViewer = null
    try {
      const dupRoomId = await createRoom(SERVER_URL)
      // 第一个页面：正常进房，拿到它的 clientId；第二个页面被钉成同一个 id。
      await seedAndEnter(dup, CLIENT_URL, dupRoomId, 'host', '顶号甲')
      const takenId = await dup.evaluate('window.__pr.store.clientId')

      dupViewer = await openTarget(BASE_PORT + 2, 'dup-viewer')
      // 必须在导航前注入：把新文档里**第一次** randomUUID 钉成"已被占用的那个 id"，
      // 之后恢复正常随机（换号时才是真正的新身份，不会又撞回同一个）。
      await dupViewer.send('Page.addScriptToEvaluateOnNewDocument', {
        source: `(() => {
          const orig = Crypto.prototype.randomUUID;
          let used = false;
          Crypto.prototype.randomUUID = function () {
            if (!used) { used = true; return ${JSON.stringify(uuidForClientId(takenId))}; }
            return orig.call(this);
          };
        })()`,
      })
      await seedAndEnter(dupViewer, CLIENT_URL, dupRoomId, 'viewer', '顶号乙')
      const dupSnap = await dupViewer.snapshot()
      report.dupClientId = { taken: takenId, next: dupSnap.clientId, rotations: dupSnap.recovery.clientIdRotations }
      record(
        'D1 收到 CLIENT_ID_TAKEN 后立刻换新 clientId 并重新进房',
        dupSnap.clientId !== takenId &&
          dupSnap.recovery.clientIdRotations >= 1 &&
          dupSnap.joined === true,
        `原 id=${takenId} 新 id=${dupSnap.clientId} 换号次数=${dupSnap.recovery.clientIdRotations} joined=${dupSnap.joined}`,
      )
    } catch (err) {
      record('D1 收到 CLIENT_ID_TAKEN 后立刻换新 clientId 并重新进房', false, `未完成：${err.message}`)
    } finally {
      try {
        dupViewer?.close()
      } catch {
        // 忽略
      }
    }

    finishUp()
  } catch (err) {
    console.error(`验收脚本失败：${err.message}`)
    cleanup()
    finishUp()
  }

  function finishUp() {
    const failed = checks.filter((c) => !c.pass)
    console.log('\n=== 断线恢复验收结果 ===')
    console.log(
      JSON.stringify(
        { ...report, checks: checks.map((c) => ({ name: c.name, pass: c.pass, detail: c.detail })) },
        null,
        2,
      ),
    )
    console.log(`\n判据：${checks.length - failed.length}/${checks.length} 通过`)
    for (const c of failed) console.log(`  ✗ ${c.name} — ${c.detail}`)
    const pass = failed.length === 0 && checks.length > 0
    console.log(`\n判定：${pass ? 'PASS' : 'FAIL'}`)
    cleanup()
    process.exitCode = pass ? 0 : 1
  }
}

/** 强制退出但先让 stdout 冲刷：只设 exitCode 会被 keep-alive 连接与定时器挂住。 */
async function finish() {
  await main()
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}

await finish()
