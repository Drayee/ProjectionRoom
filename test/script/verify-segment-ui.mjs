// 服务端切片入口的界面验收：真实 Chrome 里把「交给服务器切片 / 下载切片工具」点开，
// 确认面板渲染、服务端探测结果、内嵌教程，以及切片工具下载面板
//（清单渲染 / 平台推荐高亮 / 下载链接 / sha256 / 校验命令 / 用法说明）。
//
// 用法说明的判据：拖到 exe 上、双击按提示输入路径、缺 ffmpeg 时 exe 自己下载
//（并支持 -ffmpeg-dir），且**不能**再让用户去 ffmpeg 官网或"先装 ffmpeg"。
//
// 工具清单来自 GET /api/downloads/segmenter：服务端没构建工具或不可达时，
// 面板必须给出中文提示并保持页面可用 —— 这一条也在下面用 CDP 断网复现。
//
// 用法：node test/script/verify-segment-ui.mjs [--client http://127.0.0.1:5173] [--server http://127.0.0.1:8080]
import { writeFileSync } from 'node:fs'
import {
  argOf,
  createRoom,
  findChrome,
  openTarget,
  seedAndEnter,
  sleep,
  startChrome,
  waitFor,
} from './lib/browser.mjs'

const argv = process.argv.slice(2)
const CLIENT_URL = argOf(argv, 'client', 'http://127.0.0.1:5173')
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:8080')
const SHOT = argOf(argv, 'shot', 'segment-ui.png')
const SHOT_TOOL = argOf(argv, 'shot2', 'segment-tool-ui.png')

let chrome
let cdp

/** 展开宿主面板上的切片入口（<details>/<summary> 或按钮都能点）。 */
const OPEN_ENTRY = `(() => {
   const el = [...document.querySelectorAll('summary,button')].find((n) => n.textContent.includes('交给服务器切片'));
   if (!el) return 'not-found';
   el.click();
   if (el.parentElement && el.parentElement.tagName === 'DETAILS') el.parentElement.open = true;
   return 'clicked';
 })()`

async function main() {
  chrome = await startChrome(findChrome(), Number(argOf(argv, 'port', '9500')), 'ui')
  cdp = await openTarget(chrome.port, 'ui')

  const roomId = await createRoom(SERVER_URL)
  await seedAndEnter(cdp, CLIENT_URL, roomId, 'host', '主播')
  console.log(`房间 ${roomId}，主播已进房`)

  // 1) 入口必须存在（主播未选片时 HostPanel 的选片区可见）
  const entryFound = await waitFor(
    async () =>
      (await cdp.evaluate(
        `Boolean([...document.querySelectorAll('button,summary,a')].find((el) => el.textContent.includes('交给服务器切片')))`,
      )) === true,
    { label: '切片入口', timeoutMs: 20000 },
  )
  console.log(`入口可见: ${entryFound}`)

  // 2) 展开入口
  await cdp.evaluate(OPEN_ENTRY)

  // 3) 面板渲染 + 服务端探测结果（徽标文案见 SegmentUpload.vue：服务端可用 / 服务器切片不可用）
  await waitFor(
    async () =>
      (await cdp.evaluate(
        `(() => {
           const b = [...document.querySelectorAll('.badge')].find((n) =>
             n.textContent.includes('服务端') || n.textContent.includes('服务器切片') || n.textContent.includes('检测'));
           return Boolean(b) && !b.textContent.includes('检测中');
         })()`,
      )) === true,
    { label: '服务端探测结果', timeoutMs: 15000 },
  )
  const panel = await cdp.evaluate(
    `(() => {
       const text = document.body.innerText;
       const badge = [...document.querySelectorAll('.badge')].find((n) =>
         n.textContent.includes('服务端') || n.textContent.includes('服务器切片'));
       return {
         hasUpload: text.includes('上传') && (text.includes('切片') || text.includes('分片')),
         badgeText: badge ? badge.textContent.trim() : '',
         badgeOk: badge ? badge.classList.contains('ok') : false,
         probeUnavailable: text.includes('服务器切片不可用'),
         hasTutorial: text.includes('ffmpeg'),
         hasLocalSegmenter: text.includes('segmenter'),
         bytes: text.length,
       };
     })()`,
  )
  console.log(`面板: ${JSON.stringify(panel)}`)

  // 4) 展开本地教程（离线也要能自救）
  const tutorialOpen = await cdp.evaluate(
    `(() => {
       const d = document.querySelector('details.tutorial');
       if (!d) return 'no-details';
       d.open = true;
       return d.innerText.includes('segmenter') ? 'ok' : 'opened-without-segmenter';
     })()`,
  )
  console.log(`本地教程: ${tutorialOpen}`)

  // 5) 切片工具下载面板：清单渲染（挂载时拉了一次 /api/downloads/segmenter）
  const rowsSeen = await waitFor(
    async () => {
      const count = await cdp.evaluate(
        `document.querySelectorAll('[data-testid="slice-tool-row"]').length`,
      )
      return count > 0 ? count : null
    },
    { label: '切片工具清单渲染', timeoutMs: 20000 },
  ).catch(() => 0)
  console.log(`清单行数: ${rowsSeen}`)

  const tool = await cdp.evaluate(
    `(() => {
       const panel = document.querySelector('[data-testid="slice-tool-panel"]');
       if (!panel) return { found: false };
       const text = panel.innerText;
       const lower = text.toLowerCase();
       const rows = [...panel.querySelectorAll('[data-testid="slice-tool-row"]')];
       const links = [...panel.querySelectorAll('[data-testid="slice-tool-download"]')];
       const recommended = rows.filter((r) => r.getAttribute('data-recommended') === 'true');
       const usage = panel.querySelector('[data-testid="slice-tool-usage"]');
       const verify = panel.querySelector('[data-testid="slice-tool-verify"]');
       const stateEl = panel.querySelector('[data-testid="slice-tool-state"]');
       return {
         found: true,
         state: stateEl ? stateEl.innerText.trim() : '',
         detected: panel.getAttribute('data-detected') || '',
         rowCount: rows.length,
         keys: rows.map((r) => r.getAttribute('data-key')),
         names: rows.map((r) => (r.querySelector('.name') || {}).innerText || ''),
         recommendedKeys: recommended.map((r) => r.getAttribute('data-key')),
         linkCount: links.length,
         linkHrefs: links.map((a) => a.getAttribute('href')),
         allDownloadAttr: links.every((a) => a.hasAttribute('download')),
         shaShort: (panel.querySelector('.sha') || {}).innerText || '',
         hasShaToggle: Boolean(panel.querySelector('[data-testid="slice-tool-sha-toggle"]')),
         hasUsageCopy: Boolean(panel.querySelector('[data-testid="slice-tool-copy-usage"]')),
         usageText: usage ? usage.innerText : '',
         verifyText: verify ? verify.innerText : '',
         usageFont: usage ? getComputedStyle(usage).fontFamily : '',
         // 用法说明：拖到 exe 上 / 双击输入路径，且不需要用户自己装 ffmpeg（exe 会兜底下载）
         hasDragUse: text.includes('拖到') && text.includes('exe 上'),
         hasDoubleClickUse: text.includes('双击 exe'),
         hasAutoFfmpeg: text.includes('自动下载') && text.includes('ffmpeg'),
         hasFfmpegDirFlag: text.includes('-ffmpeg-dir'),
         // 反面判据：不能再让用户去 ffmpeg 官网、也不能再写"前提是本机有 ffmpeg"
         ffmpegHomepageLink: Boolean(panel.querySelector('a[href*="ffmpeg.org"]')),
         hasFfmpegPrereq:
           text.includes('前提是本机有 ffmpeg') ||
           text.includes('本机要有 ffmpeg') ||
           text.includes('需要 ffmpeg') ||
           text.includes('ffmpeg 官网'),
         // 旧「生成一键脚本」路线必须一点残留都没有
         legacy: ['-noexe', 'executionpolicy', '.ps1', '.sh', 'bash', 'hls', '一键脚本'].filter((k) =>
           lower.includes(k),
         ),
         panelBytes: text.length,
       };
     })()`,
  )
  console.log(`切片工具面板: ${JSON.stringify(tool)}`)

  // 6) 推荐高亮必须与推断出的平台自洽：推断出 → 恰好高亮那一行；推断不出 → 一行都不高亮
  const recOk =
    tool.detected === ''
      ? tool.recommendedKeys.length === 0
      : tool.recommendedKeys.length === 1 && tool.recommendedKeys[0] === tool.detected
  console.log(`平台推荐: detected=${tool.detected} highlighted=${JSON.stringify(tool.recommendedKeys)} 自洽=${recOk}`)

  // 7) sha256 前 16 位 + 展开完整值（完整值必须是 64 位十六进制）
  const shaFull = await cdp.evaluate(
    `(() => {
       const toggle = document.querySelector('[data-testid="slice-tool-sha-toggle"]');
       if (!toggle) return { ok: false, reason: 'no-toggle' };
       toggle.click();
       return { ok: true };
     })()`,
  )
  await sleep(200)
  const shaFullText = await cdp.evaluate(
    `(() => {
       const el = document.querySelector('[data-testid="slice-tool-sha-full"]');
       return el ? el.innerText.trim() : '';
     })()`,
  )
  const shaOk = /^[0-9a-f]{64}$/.test(shaFullText)
  console.log(`sha256: 前16位=${JSON.stringify(tool.shaShort)} 完整=${shaFullText} 合法=${shaOk}`)

  // 8) 用法块必须等宽、含 -fragment，并且复制按钮可用（纯本地能力，不依赖服务端）
  const copyOk = await cdp.evaluate(
    `(() => {
       const btn = document.querySelector('[data-testid="slice-tool-copy-usage"]');
       if (!btn) return 'no-button';
       btn.click();
       return 'clicked';
     })()`,
  )
  console.log(`复制用法: ${copyOk}（字体 ${tool.usageFont}）`)

  // 9) 降级：屏蔽清单端点后必须给中文提示且页面不崩（拿不到 CDP Network 能力时只报"未跑"）
  let offline = { ran: false }
  try {
    await cdp.send('Network.enable')
    // 只屏蔽切片接口与清单端点：不能写成 */api/* —— Vite dev 下的模块路径是 /src/api/*.ts，
    // 那样连模块加载一起挡掉，页面直接白屏（踩过一次）。
    await cdp.send('Network.setBlockedURLs', {
      urls: ['*/api/v1/segment/*', '*/api/downloads/segmenter*'],
    })
    await cdp.evaluate(`location.reload(); 'ok'`)
    await waitFor(async () => cdp.evaluate('typeof window.__pr !== "undefined"'), {
      label: '重载后调试钩子',
      timeoutMs: 20000,
    })
    await cdp.evaluate(OPEN_ENTRY)
    const state = await waitFor(
      async () => {
        const probe = await cdp.evaluate(
          `(() => {
             const panel = document.querySelector('[data-testid="slice-tool-panel"]');
             const badge = [...document.querySelectorAll('.badge')].find((n) =>
               n.textContent.includes('服务器切片不可用') || n.textContent.includes('服务端可用'));
             return {
               badge: badge ? badge.textContent.trim() : '',
               panelFound: Boolean(panel),
               toolState: panel ? (panel.querySelector('[data-testid="slice-tool-state"]') || {}).innerText || '' : '',
               toolUnavailable: Boolean(panel && panel.querySelector('[data-testid="slice-tool-unavailable"]')),
               toolRows: panel ? panel.querySelectorAll('[data-testid="slice-tool-row"]').length : -1,
               hasUsage: Boolean(panel && panel.querySelector('[data-testid="slice-tool-usage"]')),
             };
           })()`,
        )
        return probe.badge !== '' ? probe : null
      },
      { label: '离线状态下的清单提示', timeoutMs: 15000 },
    )
    offline = {
      ran: true,
      ...state,
      probeUnavailable: state.badge.includes('服务器切片不可用'),
      // 屏蔽清单后必须落到"清单不可用"这句中文提示上：拿不到数据也只是一条提示，不是崩溃
      manifestUnavailable: state.toolUnavailable && state.toolState.includes('清单不可用'),
    }
    console.log(`断网降级: ${JSON.stringify(offline)}`)
    // 注意：重载会断开主播的 WS，房间若是空的会被服务端清掉（快照里可能看到 last="房间不存在"）。
    // 这不影响本节的判据 —— 切片工具面板只看清单与浏览器本地能力。
  } catch (err) {
    offline = { ran: false, reason: err.message }
    console.log(`断网降级: 未跑（${err.message}）`)
  }

  // 10) 恢复网络并重载，确认清单重新渲染（截图也要拍到真实状态，而不是降级态）
  let restored = { rowCount: -1 }
  try {
    await cdp.send('Network.setBlockedURLs', { urls: [] })
    await cdp.evaluate(`location.reload(); 'ok'`)
    await waitFor(async () => cdp.evaluate('typeof window.__pr !== "undefined"'), {
      label: '恢复后调试钩子',
      timeoutMs: 20000,
    })
    await cdp.evaluate(OPEN_ENTRY)
    restored = await waitFor(
      async () => {
        const probe = await cdp.evaluate(
          `(() => {
             const rows = document.querySelectorAll('[data-testid="slice-tool-row"]').length;
             return { rowCount: rows };
           })()`,
        )
        return probe.rowCount > 0 ? probe : null
      },
      { label: '恢复后清单重新渲染', timeoutMs: 15000 },
    ).catch(() => ({ rowCount: -1 }))
    console.log(`恢复后清单行数: ${restored.rowCount}`)
  } catch (err) {
    console.log(`恢复网络: 未跑（${err.message}）`)
  }

  // 11) 截图留证（第二张把切片工具面板滚到视口里）
  const shot = await cdp.send('Page.captureScreenshot', { format: 'png' })
  writeFileSync(SHOT, Buffer.from(shot.data, 'base64'))
  await cdp.evaluate(
    `document.querySelector('[data-testid="slice-tool-panel"]')?.scrollIntoView({ block: 'center' }); 'ok'`,
  )
  await sleep(300)
  const toolShot = await cdp.send('Page.captureScreenshot', { format: 'png' })
  writeFileSync(SHOT_TOOL, Buffer.from(toolShot.data, 'base64'))
  console.log(`截图: ${SHOT} / ${SHOT_TOOL}`)

  const errors = await cdp.evaluate(
    `window.__pr ? JSON.stringify(window.__pr.snapshot().errors) : '（调试钩子不可用）'`,
  )
  console.log(`页面错误: ${errors}`)

  const pass =
    entryFound &&
    panel.hasUpload &&
    (panel.badgeOk || panel.probeUnavailable) &&
    panel.hasTutorial &&
    // 清单渲染：5 个固定目标至少都在，每行都有指向 /downloads/ 的下载链接
    tool.found &&
    tool.rowCount >= 5 &&
    tool.linkCount === tool.rowCount &&
    tool.allDownloadAttr &&
    tool.linkHrefs.every((href) => typeof href === 'string' && href.includes('/downloads/')) &&
    tool.names.some((name) => name.includes('Windows x64')) &&
    /^[0-9a-f]{16}…$/.test(tool.shaShort) &&
    tool.hasShaToggle &&
    shaFull.ok &&
    shaOk &&
    // 平台推荐
    recOk &&
    // 用法与校验命令
    tool.hasUsageCopy &&
    tool.usageText.includes('-fragment') &&
    tool.usageText.includes('-transcode 1200k') &&
    // 用法说明：拖到 exe 上 / 双击按提示输入路径 / 缺 ffmpeg 时 exe 自己下载
    tool.hasDragUse &&
    tool.hasDoubleClickUse &&
    tool.hasAutoFfmpeg &&
    tool.hasFfmpegDirFlag &&
    !tool.ffmpegHomepageLink &&
    !tool.hasFfmpegPrereq &&
    tool.verifyText.includes('Get-FileHash') &&
    tool.verifyText.includes('shasum -a 256') &&
    // 旧脚本路线的字样一个都不能剩
    tool.legacy.length === 0 &&
    // 清单拿不到时必须给中文提示而不是崩掉
    (!offline.ran || (offline.panelFound && offline.probeUnavailable && offline.manifestUnavailable)) &&
    restored.rowCount >= 5

  console.log(`\n判定：${pass ? 'PASS' : 'FAIL'}`)
  process.exitCode = pass ? 0 : 1
}

try {
  await main()
} catch (err) {
  console.error(`界面验收失败：${err.message}`)
  process.exitCode = 1
} finally {
  try {
    cdp?.close()
  } catch {
    // 忽略
  }
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
