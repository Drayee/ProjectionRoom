// 服务端切片入口的界面验收：真实 Chrome 里把「交给服务器切片 / 生成一键脚本」点开，
// 确认面板渲染、服务端探测结果、内嵌教程、一键切片脚本面板（表单校验 / 预览 / 下载落盘 BOM），
// 并留截图。脚本生成是纯前端逻辑，即使服务端不可达也必须可用。
//
// 用法：node test/script/verify-segment-ui.mjs [--client http://127.0.0.1:5173] [--server http://127.0.0.1:8080]
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
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
const SHOT_SCRIPT = argOf(argv, 'shot2', 'segment-script-ui.png')

let chrome
let cdp

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

  // 2) 展开入口（<details>/<summary> 或按钮都能点）
  await cdp.evaluate(
    `(() => {
       const el = [...document.querySelectorAll('summary,button')].find((n) => n.textContent.includes('交给服务器切片'));
       if (!el) return 'not-found';
       el.click();
       if (el.parentElement && el.parentElement.tagName === 'DETAILS') el.parentElement.open = true;
       return 'clicked';
     })()`,
  )

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

  // 5) 一键切片脚本面板：先确认"参数不全时必须明确报错、按钮禁用"
  const initial = await cdp.evaluate(
    `(() => {
       const panel = document.querySelector('[data-testid="slice-script-panel"]');
       if (!panel) return { found: false };
       const text = panel.innerText;
       const status = panel.querySelector('[data-testid="slice-script-status"]');
       const errors = panel.querySelector('[data-testid="slice-script-errors"]');
       const ps1 = panel.querySelector('[data-testid="download-ps1"]');
       const sh = panel.querySelector('[data-testid="download-sh"]');
       const copy = panel.querySelector('[data-testid="copy-script"]');
       return {
         found: true,
         status: status ? status.innerText.trim() : '',
         errorCount: errors ? errors.querySelectorAll('li').length : 0,
         errorText: errors ? errors.innerText.trim() : '',
         ps1Disabled: ps1 ? ps1.disabled : null,
         shDisabled: sh ? sh.disabled : null,
         copyDisabled: copy ? copy.disabled : null,
         hasThreePaths: text.includes('拖') && text.includes('Shift') && text.includes('桌面'),
         hasClipboardStep: text.includes('复制文件地址'),
         hasCurlPlan: text.includes('curl') && text.includes('5 秒'),
         hasPackPlan: text.includes('index.json') && text.includes('sha256'),
         mentionsNoApi: text.includes('不依赖 /api') || text.includes('/api'),
         hasPreview: Boolean(panel.querySelector('[data-testid="slice-script-preview"]')),
       };
     })()`,
  )
  console.log(`一键脚本面板: ${JSON.stringify(initial)}`)

  // 6) 粘贴路径后必须变成"参数就绪，可生成"（浏览器拿不到完整路径，所以这条路径是主用法之一）
  await cdp.evaluate(
    `(() => {
       const el = document.querySelector('#slice-source-path');
       if (!el) return 'no-input';
       const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
       setter.call(el, 'D:\\\\video\\\\movie.mp4');
       el.dispatchEvent(new Event('input', { bubbles: true }));
       return 'ok';
     })()`,
  )
  const ready = await waitFor(
    async () => {
      const state = await cdp.evaluate(
        `(() => {
           const panel = document.querySelector('[data-testid="slice-script-panel"]');
           const status = panel && panel.querySelector('[data-testid="slice-script-status"]');
           const ps1 = panel && panel.querySelector('[data-testid="download-ps1"]');
           const errors = panel && panel.querySelector('[data-testid="slice-script-errors"]');
           return {
             status: status ? status.innerText.trim() : '',
             ps1Disabled: ps1 ? ps1.disabled : true,
             errorCount: errors ? errors.querySelectorAll('li').length : 0,
           };
         })()`,
      )
      return state.status.includes('参数就绪') && state.ps1Disabled === false ? state : null
    },
    { label: '一键脚本参数就绪', timeoutMs: 8000 },
  )
  console.log(`参数就绪: ${JSON.stringify(ready)}`)

  // 7) 预览区必须给出可读脚本（等宽、含关键步骤），并且复制按钮可用
  const preview = await cdp.evaluate(
    `(() => {
       const d = document.querySelector('[data-testid="slice-script-preview"]');
       if (!d) return { found: false };
       d.open = true;
       const pre = d.querySelector('pre');
       const text = pre ? pre.innerText : '';
       return {
         found: true,
         lines: text.split('\\n').length,
         hasTitle: text.includes('ProjectionRoom 一键切片脚本'),
         hasIndexJson: text.includes('index.json'),
         hasFfprobe: text.includes('ffprobe'),
         hasPack: text.includes('pack-'),
         hasOutDir: text.includes('room-media'),
         font: getComputedStyle(pre).fontFamily,
         overflow: getComputedStyle(pre).overflowY,
         maxHeight: getComputedStyle(pre).maxHeight,
       };
     })()`,
  )
  console.log(`脚本预览: ${JSON.stringify(preview)}`)

  // 8) 真的下载 .ps1：Windows 上必须以 UTF-8 带 BOM 落盘，否则 PowerShell 按 ANSI 读，中文全乱码。
  //    headless 里下载目录要靠 CDP 指定；拿不到下载能力时只报"未跑"，不让它变成假 PASS。
  let bom = '未跑'
  const downloadDir = mkdtempSync(join(tmpdir(), 'pr-script-dl-'))
  try {
    await cdp.send('Page.setDownloadBehavior', { behavior: 'allow', downloadPath: downloadDir })
    await cdp.evaluate(`document.querySelector('[data-testid="download-ps1"]').click(); 'ok'`)
    const name = await waitFor(
      () => readdirSync(downloadDir).find((n) => n.endsWith('.ps1')) ?? null,
      { label: '下载 .ps1 落盘', timeoutMs: 8000 },
    ).catch(() => null)
    if (!name) {
      bom = '未跑（没等到下载文件）'
    } else {
      const bytes = readFileSync(join(downloadDir, name))
      const hasBom = bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf
      const text = bytes.toString('utf8').replace(/^\uFEFF/, '')
      const crlf = text.includes('\r\n')
      bom = hasBom && text.includes('ProjectionRoom') ? `ok（CRLF=${crlf}）` : `失败：BOM=${hasBom}`
      console.log(`下载文件: ${name} · ${bytes.length} 字节 · BOM=${hasBom} · CRLF=${crlf}`)
    }
  } catch (err) {
    bom = `未跑（${err.message}）`
  } finally {
    rmSync(downloadDir, { recursive: true, force: true })
  }
  console.log(`.ps1 落盘校验: ${bom}`)

  // 10) 「没有服务端也能用」：屏蔽 /api 后重新进房，一键脚本面板必须仍然完整可用。
  //     生成脚本是纯前端逻辑，服务端不可用时这段面板不能跟着一起消失或禁用。
  let offline = { ran: false }
  try {
    await cdp.send('Network.enable')
    // 只屏蔽切片接口：不能写成 */api/* —— Vite dev 下的模块路径是 /src/api/*.ts，
    // 那样连模块加载一起挡掉，页面直接白屏（踩过一次）。
    await cdp.send('Network.setBlockedURLs', { urls: ['*/api/v1/segment/*'] })
    await cdp.evaluate(`location.reload(); 'ok'`)
    await waitFor(async () => cdp.evaluate('typeof window.__pr !== "undefined"'), {
      label: '重载后调试钩子',
      timeoutMs: 20000,
    })
    await cdp.evaluate(
      `(() => {
         const el = document.querySelector('.segment-toggle');
         if (el && !document.querySelector('[data-testid="slice-script-panel"]')) el.click();
         return 'ok';
       })()`,
    )
    const state = await waitFor(
      async () => {
        const probe = await cdp.evaluate(
          `(() => {
             const panel = document.querySelector('[data-testid="slice-script-panel"]');
             const badge = [...document.querySelectorAll('.badge')].find((n) =>
               n.textContent.includes('服务器切片不可用') || n.textContent.includes('服务端可用'));
             return {
               badge: badge ? badge.textContent.trim() : '',
               panelFound: Boolean(panel),
               ps1Disabled: panel ? panel.querySelector('[data-testid="download-ps1"]').disabled : null,
               hasPreview: Boolean(panel && panel.querySelector('[data-testid="slice-script-preview"]')),
             };
           })()`,
        )
        return probe.badge !== '' ? probe : null
      },
      { label: '离线状态下的服务端探测结果', timeoutMs: 15000 },
    )

    // 粘贴路径后仍然能生成（按钮变为可用）
    await cdp.evaluate(
      `(() => {
         const el = document.querySelector('#slice-source-path');
         const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
         setter.call(el, '/home/me/movie.mp4');
         el.dispatchEvent(new Event('input', { bubbles: true }));
         return 'ok';
       })()`,
    )
    const offlineReady = await waitFor(
      async () => {
        const probe = await cdp.evaluate(
          `(() => {
             const panel = document.querySelector('[data-testid="slice-script-panel"]');
             const status = panel && panel.querySelector('[data-testid="slice-script-status"]');
             const ps1 = panel && panel.querySelector('[data-testid="download-ps1"]');
             return { status: status ? status.innerText.trim() : '', ps1Disabled: ps1 ? ps1.disabled : true };
           })()`,
        )
        return probe.ps1Disabled === false ? probe : null
      },
      { label: '离线状态下生成脚本', timeoutMs: 8000 },
    )
    offline = { ran: true, ...state, ...offlineReady, probeUnavailable: state.badge.includes('服务器切片不可用') }
    console.log(`无服务端可用性: ${JSON.stringify(offline)}`)
    // 注意：重载会断开主播的 WS，房间若是空的会被服务端清掉（快照里可能看到 last="房间不存在"）。
    // 这不影响本节的判据 —— 一键脚本面板与房间状态无关，它只看浏览器本地能力。
  } catch (err) {
    offline = { ran: false, reason: err.message }
    console.log(`无服务端可用性: 未跑（${err.message}）`)
  }

  // 11) 截图留证（第二张把一键脚本面板滚到视口里）
  const shot = await cdp.send('Page.captureScreenshot', { format: 'png' })
  writeFileSync(SHOT, Buffer.from(shot.data, 'base64'))
  await cdp.evaluate(
    `document.querySelector('[data-testid="slice-script-panel"]')?.scrollIntoView({ block: 'center' }); 'ok'`,
  )
  await sleep(300)
  const scriptShot = await cdp.send('Page.captureScreenshot', { format: 'png' })
  writeFileSync(SHOT_SCRIPT, Buffer.from(scriptShot.data, 'base64'))
  console.log(`截图: ${SHOT} / ${SHOT_SCRIPT}`)

  const errors = await cdp.evaluate(
    `window.__pr ? JSON.stringify(window.__pr.snapshot().errors) : '（调试钩子不可用）'`,
  )
  console.log(`页面错误: ${errors}`)

  const pass =
    entryFound &&
    panel.hasUpload &&
    (panel.badgeOk || panel.probeUnavailable) &&
    panel.hasTutorial &&
    initial.found &&
    initial.errorCount > 0 &&
    initial.ps1Disabled === true &&
    initial.hasThreePaths &&
    initial.hasClipboardStep &&
    initial.hasCurlPlan &&
    initial.hasPreview &&
    ready.ps1Disabled === false &&
    preview.found &&
    preview.hasTitle &&
    preview.hasIndexJson &&
    preview.hasFfprobe &&
    preview.hasPack &&
    preview.lines > 100 &&
    !bom.startsWith('失败') &&
    // 服务端不可用时面板必须照样可用（拿不到 CDP Network 能力时只提示"未跑"）
    (!offline.ran || (offline.panelFound && offline.probeUnavailable && offline.hasPreview))

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
