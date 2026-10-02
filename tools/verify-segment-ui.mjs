// 服务端切片入口的界面验收：真实 Chrome 里把「交给服务器切片」点开，
// 确认面板渲染、服务端探测结果、内嵌教程，并留一张截图。
//
// 用法：node tools/verify-segment-ui.mjs [--client http://127.0.0.1:5173] [--server http://127.0.0.1:8080]
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
       const d = [...document.querySelectorAll('details')].find((n) => n.textContent.includes('ffmpeg'));
       if (!d) return 'no-details';
       d.open = true;
       return d.innerText.includes('segmenter') ? 'ok' : 'opened-without-segmenter';
     })()`,
  )
  console.log(`本地教程: ${tutorialOpen}`)

  // 5) 截图留证
  const shot = await cdp.send('Page.captureScreenshot', { format: 'png' })
  writeFileSync(SHOT, Buffer.from(shot.data, 'base64'))
  console.log(`截图: ${SHOT}`)

  const errors = await cdp.evaluate(`JSON.stringify(window.__pr.snapshot().errors)`)
  console.log(`页面错误: ${errors}`)

  const pass = entryFound && panel.hasUpload && (panel.badgeOk || panel.probeUnavailable) && panel.hasTutorial
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
