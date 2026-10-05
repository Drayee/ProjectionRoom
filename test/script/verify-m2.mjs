#!/usr/bin/env node
/**
 * M2 验收脚本：用真实 Chrome（CDP）驱动"主播"和"观众"两个独立浏览器，测量 SPEC §10 的 M2 验收标准。
 *
 *   - 起播时间：观众从进房到画面开始推进的秒数（目标 1–3s）
 *   - 同步偏差：观众 video.currentTime 与主播权威位置的差（目标 < 500ms）
 *   - 房主控制：暂停 / 跳转 / 继续 是否被全员跟随
 *
 * 为什么不用单元测试：这三项只存在于"真实浏览器的 MediaSource + WebRTC 运行期状态"里。
 * 页面通过 window.__pr.snapshot() 暴露结构化快照（client/src/debug.ts），脚本只读快照，不抠 DOM 文本。
 *
 * 为什么用两个浏览器实例而不是两个标签页：同一个 Chrome 里的后台标签会被冻结/节流，
 * 既会让 CDP evaluate 挂死，也会把 200ms 的同步循环拖成 1s —— 那测的就不是同步精度了。
 *
 * 用法（在仓库根目录执行）：
 *   node test/script/verify-m2.mjs [--media test/resource/short_video/cut] [--duration 20]
 */
import { spawn } from 'node:child_process'
import { createServer } from 'node:http'
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { extname, join, resolve, sep } from 'node:path'

// ---------- 参数 ----------
const argv = process.argv.slice(2)
function arg(name, fallback) {
  const i = argv.indexOf(`--${name}`)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}

const CLIENT_URL = arg('client', 'http://127.0.0.1:5173')
const SERVER_URL = arg('server', 'http://127.0.0.1:8080')
// 默认用仓库自带的测试资源；素材只有 25s，所以默认观察时长也短。
const MEDIA_DIR = arg('media', 'test/resource/short_video/cut')
const DURATION_S = Number(arg('duration', '20'))
const BASE_PORT = Number(arg('port', '9333'))
const CHECK_CONTROLS = !argv.includes('--no-controls')

if (!MEDIA_DIR) {
  console.error('必须用 --media 指定 cmd/segmenter 产出的分片目录')
  process.exitCode = 2
}
if (!existsSync(join(MEDIA_DIR, 'index.json'))) {
  console.error(`目录里没有 index.json：${MEDIA_DIR}`)
  process.exitCode = 2
}

// ---------- 小工具 ----------
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

function findChrome() {
  const candidates = [
    process.env.CHROME_PATH,
    'C:/Program Files/Google/Chrome/Application/chrome.exe',
    'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
    process.env.LOCALAPPDATA ? join(process.env.LOCALAPPDATA, 'Google/Chrome/Application/chrome.exe') : null,
    '/usr/bin/google-chrome',
    '/usr/bin/chromium',
  ].filter(Boolean)

  for (const path of candidates) {
    if (existsSync(path)) return path
  }
  throw new Error('找不到 Chrome，可用 CHROME_PATH 环境变量指定')
}

async function waitFor(fn, { timeoutMs = 20000, intervalMs = 200, label = '条件' } = {}) {
  const deadline = Date.now() + timeoutMs
  let last
  while (Date.now() < deadline) {
    last = await fn()
    if (last) return last
    await sleep(intervalMs)
  }
  throw new Error(`等待超时：${label}（最后状态：${JSON.stringify(last)}）`)
}

// ---------- CDP 客户端 ----------
class CdpTarget {
  constructor(ws, label) {
    this.ws = ws
    this.label = label
    this.seq = 0
    this.pending = new Map()
    this.handlers = new Map()
    this.crashed = false

    ws.addEventListener('message', (event) => {
      const msg = JSON.parse(event.data)
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id)
        this.pending.delete(msg.id)
        if (msg.error) reject(new Error(`${this.label} CDP ${msg.error.message}`))
        else resolve(msg.result)
        return
      }
      if (msg.method === 'Inspector.targetCrashed') {
        this.crashed = true
      }
      const handlers = this.handlers.get(msg.method)
      if (handlers) {
        for (const cb of handlers) cb(msg.params)
      }
    })
  }

  static async open(wsUrl, label) {
    const ws = new WebSocket(wsUrl)
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, { once: true })
      ws.addEventListener('error', () => reject(new Error(`${label} 无法连接 CDP`)), { once: true })
    })
    return new CdpTarget(ws, label)
  }

  send(method, params = {}, timeoutMs = 15000) {
    const id = ++this.seq
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.ws.send(JSON.stringify({ id, method, params }))
      setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id)
          reject(new Error(`${this.label} CDP 超时：${method}`))
        }
      }, timeoutMs)
    })
  }

  on(method, cb) {
    if (!this.handlers.has(method)) this.handlers.set(method, [])
    this.handlers.get(method).push(cb)
  }

  async evaluate(expression, timeoutMs = 15000) {
    const result = await this.send(
      'Runtime.evaluate',
      { expression, awaitPromise: true, returnByValue: true },
      timeoutMs,
    )
    if (result.exceptionDetails) {
      const text = result.exceptionDetails.exception?.description ?? result.exceptionDetails.text
      throw new Error(`${this.label} 页面异常：${text}`)
    }
    return result.result.value
  }

  async snapshot() {
    return JSON.parse(await this.evaluate('JSON.stringify(window.__pr.snapshot())'))
  }

  /**
   * 触发一个可能长时间运行的动作，但不等待它的 Promise。
   * 有些 store 动作（如 seek 到未缓冲区间）会让页面忙上一阵，
   * 用 awaitPromise 等它会把 CDP 调用一起拖死。
   */
  async evaluateNoWait(expression) {
    return this.evaluate(`void (${expression}); 'ok'`)
  }

  async navigate(url) {
    await this.send('Page.navigate', { url })
    await waitFor(async () => (await this.evaluate('document.readyState')) === 'complete', {
      label: `页面加载 ${url}`,
    })
  }

  async uploadDirectory(selector, dir) {
    const files = readdirSync(dir).map((name) => join(dir, name))
    const { root } = await this.send('DOM.getDocument', { depth: -1 })
    const { nodeId } = await this.send('DOM.querySelector', { nodeId: root.nodeId, selector })
    if (!nodeId) throw new Error(`${this.label} 找不到文件输入框 ${selector}`)
    await this.send('DOM.setFileInputFiles', { nodeId, files })
  }

  close() {
    try {
      this.ws.close()
    } catch {
      // 忽略
    }
  }
}

// ---------- 浏览器进程 ----------
async function startChrome(chromePath, port, label) {
  const profileDir = mkdtempSync(join(tmpdir(), `pr-chrome-${label}-`))
  const proc = spawn(
    chromePath,
    [
      '--headless=new',
      `--remote-debugging-port=${port}`,
      `--user-data-dir=${profileDir}`,
      '--no-first-run',
      '--no-default-browser-check',
      '--disable-gpu',
      '--mute-audio',
      '--window-size=800,600',
      // 验收要测的是"同步精度"，不是自动播放策略：放开手势要求。
      '--autoplay-policy=no-user-gesture-required',
      'about:blank',
    ],
    { stdio: 'ignore' },
  )

  await waitFor(
    async () => {
      try {
        const resp = await fetch(`http://127.0.0.1:${port}/json/version`)
        return resp.ok
      } catch {
        return false
      }
    },
    { label: `${label} 浏览器 CDP 就绪`, timeoutMs: 30000 },
  )

  return {
    proc,
    port,
    close: () => {
      try {
        proc.kill()
      } catch {
        // 忽略
      }
      try {
        rmSync(profileDir, { recursive: true, force: true })
      } catch {
        // 忽略
      }
    },
  }
}

async function openTarget(port, label) {
  const resp = await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' })
  if (!resp.ok) throw new Error(`创建标签页失败：HTTP ${resp.status}`)
  const target = await resp.json()
  const cdp = await CdpTarget.open(target.webSocketDebuggerUrl, label)
  await cdp.send('Page.enable')
  await cdp.send('Runtime.enable')
  await cdp.send('DOM.enable')
  await cdp.send('Inspector.enable')
  await cdp.send('Page.bringToFront')
  return cdp
}

// ---------- 媒体注入 ----------
/**
 * CDP 的 DOM.setFileInputFiles 在 <input webkitdirectory> 上不可靠（实测 input.files 仍为 0），
 * 而 Chrome 的目录选择对话框本身无法自动化。
 * 兜底：由本脚本起一个只读静态服务，页面侧把文件构造成 FileList，
 * 再调用与真实按钮完全相同的 store 动作 publishMediaDirectory —— 媒体链路本身完全一致。
 */
function startMediaServer(dir) {
  const types = { '.json': 'application/json', '.mp4': 'video/mp4', '.m4s': 'video/iso.segment' }
  // 规范化后再比较（与 lib/browser.mjs 同一处修正）：Windows 上 join() 会把 'a/b' 变成 'a\\b'，
  // 拿它跟未规范化的 dir 做 startsWith 永远为假 —— 所有文件 404，表现为页面里 "Failed to fetch"。
  const root = resolve(dir)

  const server = createServer((req, res) => {
    const name = decodeURIComponent((req.url ?? '/').replace(/^\/+/, ''))
    try {
      const full = resolve(root, name)
      if (!full.startsWith(root + sep) || !statSync(full).isFile()) {
        res.writeHead(404).end('not found')
        return
      }
      const body = readFileSync(full)
      res.writeHead(200, {
        'Content-Type': types[extname(full)] ?? 'application/octet-stream',
        'Content-Length': body.length,
        'Access-Control-Allow-Origin': '*',
      })
      res.end(body)
    } catch {
      res.writeHead(404).end('not found')
    }
  })

  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address()
      resolve({ url: `http://127.0.0.1:${port}`, close: () => server.close() })
    })
  })
}

async function injectMediaInPage(cdp, baseUrl) {
  const script = `(async () => {
    const base = ${JSON.stringify(baseUrl)};
    const index = await (await fetch(base + '/index.json')).json();
    // 打包后多个分片共用同一个 .bin：必须去重，否则同一个十几 MB 的包会被抓上百次。
    const names = [...new Set(['index.json', index.initFile, ...index.segments.map((s) => s.file)])];
    const dt = new DataTransfer();
    for (const name of names) {
      const buf = await (await fetch(base + '/' + name)).arrayBuffer();
      dt.items.add(new File([buf], name, { type: 'video/mp4' }));
    }
    await window.__pr.store.publishMediaDirectory(dt.files);
    return window.__pr.snapshot().errors.media || '';
  })()`
  return cdp.evaluate(script, 120000)
}

// ---------- 主流程 ----------
async function createRoom() {
  const resp = await fetch(`${SERVER_URL}/api/rooms`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  })
  if (!resp.ok) throw new Error(`创建房间失败：HTTP ${resp.status}`)
  return (await resp.json()).roomId
}

async function seedAndEnter(cdp, roomId, role, displayName) {
  await cdp.navigate(CLIENT_URL)
  await cdp.evaluate(
    `sessionStorage.setItem('pr:join:${roomId}', ${JSON.stringify(
      JSON.stringify({ password: '', role, displayName }),
    )})`,
  )
  await cdp.navigate(`${CLIENT_URL}/room/${roomId}`)
  await waitFor(async () => cdp.evaluate('typeof window.__pr !== "undefined"'), { label: '调试钩子就绪' })
  await waitFor(async () => (await cdp.snapshot()).joined, { label: `${role} 加入房间` })
}

function percentile(sorted, ratio) {
  if (sorted.length === 0) return -1
  return sorted[Math.min(sorted.length - 1, Math.ceil(sorted.length * ratio) - 1)]
}

async function main() {
  const roomId = await createRoom()
  const chromePath = findChrome()
  console.log(`房间：${roomId}`)

  const hostBrowser = await startChrome(chromePath, BASE_PORT, 'host')
  const viewerBrowser = await startChrome(chromePath, BASE_PORT + 1, 'viewer')
  let mediaServer = null

  const cleanup = () => {
    try {
      mediaServer?.close()
    } catch {
      // 忽略
    }
    hostBrowser.close()
    viewerBrowser.close()
  }

  try {
    const host = await openTarget(BASE_PORT, 'host')
    const viewer = await openTarget(BASE_PORT + 1, 'viewer')

    console.log('两个浏览器已就绪')
    await seedAndEnter(host, roomId, 'host', '主播')

    // 主播选片
    try {
      await host.uploadDirectory('#media-dir-input', MEDIA_DIR)
    } catch {
      // 走兜底路径
    }
    const filesCount = await host
      .evaluate("document.querySelector('#media-dir-input')?.files?.length ?? -1")
      .catch(() => -1)

    let mediaLoaded = await host.evaluate('window.__pr.snapshot().media.loaded')
    if (!mediaLoaded && filesCount > 0) {
      await host.evaluate(
        `document.querySelector('#media-dir-input').dispatchEvent(new Event('change', { bubbles: true })); 'ok'`,
      )
      await sleep(500)
      mediaLoaded = await host.evaluate('window.__pr.snapshot().media.loaded')
    }
    if (!mediaLoaded) {
      console.log(`注入路径（input.files=${filesCount}）：构造 FileList → publishMediaDirectory`)
      mediaServer = await startMediaServer(MEDIA_DIR)
      const mediaErr = await injectMediaInPage(host, mediaServer.url)
      if (mediaErr) throw new Error(`注入媒体失败：${mediaErr}`)
    }

    const media = (await host.snapshot()).media
    console.log(`媒体已加载：${media.segmentCount} 段 / ${media.mimeType}`)

    // 观众进房（在主播放之前），并开始采样
    await seedAndEnter(viewer, roomId, 'viewer', '观众')
    await viewer.evaluate(
      `window.__samples = []; window.__sampler = setInterval(() => {
         const s = window.__pr.snapshot();
         window.__samples.push({ t: performance.now(), ct: s.video.currentTime, drift: s.sync.driftMs, mode: s.sync.mode, buf: s.sync.bufferedAhead, paused: s.video.paused, ready: s.video.readyState, be: s.video.bufferedEnd, channels: s.p2p.openChannels });
       }, 200); 'ok'`,
    )
    const viewerJoinAt = await viewer.evaluate('performance.now()')

    // 主播开播
    await host.evaluateNoWait('window.__pr.store.play()')
    console.log('主播已开播，等待观众起播…')

    const firstPlayback = await waitFor(
      async () =>
        JSON.parse(
          await viewer.evaluate(
            // 必须是真的有数据在播：readyState >= HAVE_CURRENT_DATA 且当前位置已被缓冲。
            // 否则空 MediaSource 上的 seek 会把 currentTime 改成一个"空位置"，看起来像起播。
            'JSON.stringify(window.__samples.find((s) => s.ct > 0.05 && !s.paused && s.ready >= 2 && s.be > s.ct) ?? null)',
          ),
        ),
      { label: '观众起播（需真实缓冲）', timeoutMs: 30000, intervalMs: 200 },
    )
    const startSeconds = Number(((firstPlayback.t - viewerJoinAt) / 1000).toFixed(2))
    console.log(`观众起播耗时：${startSeconds}s`)

    console.log(`观察 ${DURATION_S}s 的同步偏差…`)
    const ticker = setInterval(() => process.stdout.write('.'), 5000)
    await sleep(DURATION_S * 1000)
    clearInterval(ticker)
    process.stdout.write('\n')

    const samples = JSON.parse(await viewer.evaluate('JSON.stringify(window.__samples)', 60000))
    await viewer.evaluate('clearInterval(window.__sampler); "ok"')

    const playing = samples.filter((s) => s.ct > 0.05 && !s.paused && s.ready >= 2 && s.be > s.ct)
    const drifts = playing.map((s) => Math.abs(s.drift)).sort((a, b) => a - b)

    const stats = {
      samples: drifts.length,
      totalSamples: samples.length,
      noDataSamples: samples.length - playing.length,
      maxMs: drifts.length ? drifts[drifts.length - 1] : -1,
      p95Ms: percentile(drifts, 0.95),
      medianMs: percentile(drifts, 0.5),
    }

    // 房主控制跟随（失败不中断，如实记录）
    const controls = { pauseFollowed: null, seekDeltaSeconds: null, resumeFollowed: null }
    if (CHECK_CONTROLS) {
      try {
        await host.evaluateNoWait('window.__pr.store.pause()')
        controls.pauseFollowed = await waitFor(async () => (await viewer.snapshot()).video.paused, {
          label: '观众跟随暂停',
          timeoutMs: 6000,
          intervalMs: 100,
        }).catch(() => false)

        // 跳转目标必须落在素材时长之内：仓库自带的素材只有 25s，
        // 硬编码 120s 会跳到片尾之外，于是"跳转跟随"恒判失败（脚本假设了长素材，不是产品问题）。
        const mediaDuration = Number((await host.snapshot()).media.totalDuration) || 120
        const seekTarget = Number(Math.min(120, Math.max(5, mediaDuration * 0.5)).toFixed(1))
        console.log(`跳转目标 ${seekTarget}s（素材总长 ${mediaDuration.toFixed(1)}s）`)
        await host.evaluateNoWait(`window.__pr.store.seekTo(${seekTarget})`)
        const seekTrace = []
        for (let i = 0; i < 12; i += 1) {
          await sleep(500)
          const v = await viewer.snapshot()
          const clockPaused = await viewer.evaluate('window.__pr.store.playback.paused')
          const h = await host.snapshot()
          seekTrace.push({
            t: Number(((i + 1) * 0.5).toFixed(1)),
            viewerCt: Number(v.video.currentTime.toFixed(2)),
            viewerPaused: v.video.paused,
            clockPaused,
            mode: v.sync.mode,
            resets: v.sync.syncResets,
            buf: Number(v.sync.bufferedAhead.toFixed(1)),
            hostCt: Number(h.video.currentTime.toFixed(2)),
          })
        }
        controls.seekTrace = seekTrace
        controls.seekDeltaSeconds = Number(Math.abs(seekTrace[seekTrace.length - 1].viewerCt - seekTarget).toFixed(2))

        await host.evaluateNoWait('window.__pr.store.play()')
        controls.resumeFollowed = await waitFor(
          async () => !(await viewer.snapshot()).video.paused,
          { label: '观众跟随继续播放', timeoutMs: 15000, intervalMs: 100 },
        ).catch(() => false)
      } catch (err) {
        console.log(`控制跟随测试未完成：${err.message}`)
      }
    }

    const hostSnap = await host.snapshot()
    const viewerSnap = await viewer.snapshot()

    const result = {
      room: roomId,
      observationSeconds: DURATION_S,
      startSeconds,
      drift: stats,
      controls,
      host: {
        delivered: hostSnap.p2p.delivered,
        peers: hostSnap.p2p.peerCount,
        uploadCapacityBps: hostSnap.p2p.uploadCapacityBps,
        capacity: hostSnap.room,
      },
      viewer: {
        delivered: viewerSnap.p2p.delivered,
        timedOut: viewerSnap.p2p.timedOut,
        chunkErrors: viewerSnap.p2p.chunkErrors,
        p95DeliveryMs: viewerSnap.sync.p95DeliveryMs,
        syncResets: viewerSnap.sync.syncResets,
        rejectedProgress: viewerSnap.sync.rejectedProgress,
        bufferedAhead: viewerSnap.sync.bufferedAhead,
        mode: viewerSnap.sync.mode,
      },
      errors: { host: hostSnap.errors, viewer: viewerSnap.errors },
    }

    const pass =
      startSeconds >= 0 &&
      startSeconds <= 3 &&
      stats.maxMs >= 0 &&
      stats.maxMs < 500 &&
      controls.pauseFollowed !== false &&
      controls.resumeFollowed !== false &&
      (controls.seekDeltaSeconds === null || controls.seekDeltaSeconds < 3)

    console.log('\n=== M2 验收结果 ===')
    console.log(JSON.stringify(result, null, 2))
    console.log(
      `\n起播 ${startSeconds}s（目标 ≤3s）· 偏差 max ${stats.maxMs}ms / p95 ${stats.p95Ms}ms（目标 <500ms）\n` +
        `暂停跟随 ${controls.pauseFollowed} · 跳转偏差 ${controls.seekDeltaSeconds}s · 继续跟随 ${controls.resumeFollowed}`,
    )
    console.log(pass ? '\n判定：PASS' : '\n判定：FAIL')

    host.close()
    viewer.close()
    cleanup()
    process.exitCode = pass ? 0 : 1
  } catch (err) {
    console.error(`验收脚本失败：${err.message}`)
    cleanup()
    process.exitCode = 1
  }
}

/** 强制退出但先让 stdout 冲刷：只设 exitCode 会被 keep-alive 连接与定时器挂住。 */
async function finish() {
  await main()
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}

await finish()
