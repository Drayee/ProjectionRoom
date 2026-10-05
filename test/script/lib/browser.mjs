// 真实浏览器验收的公共管线：启动 Chrome、CDP 客户端、媒体注入、房间准备。
// verify-m2.mjs 与 verify-m3.mjs 共用这一份，避免两套拷贝各自漂移。
import { spawn } from 'node:child_process'
import { createServer } from 'node:http'
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { extname, join, resolve, sep } from 'node:path'

export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

export function findChrome() {
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

export async function waitFor(fn, { timeoutMs = 20000, intervalMs = 200, label = '条件' } = {}) {
  const deadline = Date.now() + timeoutMs
  let last
  while (Date.now() < deadline) {
    last = await fn()
    if (last) return last
    await sleep(intervalMs)
  }
  throw new Error(`等待超时：${label}（最后状态：${JSON.stringify(last)}）`)
}

export class CdpTarget {
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

  /** 触发可能长时间运行的动作但不等待它的 Promise。 */
  async evaluateNoWait(expression) {
    return this.evaluate(`void (${expression}); 'ok'`)
  }

  async snapshot() {
    return JSON.parse(await this.evaluate('JSON.stringify(window.__pr.snapshot())'))
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

export async function startChrome(chromePath, port, label) {
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
      // 验收测的是同步精度，不是自动播放策略。
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

export async function openTarget(port, label) {
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

/**
 * 媒体注入：CDP 的 DOM.setFileInputFiles 在 <input webkitdirectory> 上不可靠，
 * 而目录选择对话框无法自动化。兜底用页面内构造 FileList，再走与真实按钮完全相同的 store 动作。
 */
export function startMediaServer(dir) {
  const types = { '.json': 'application/json', '.mp4': 'video/mp4', '.m4s': 'video/iso.segment' }
  // 规范化后再比较：Windows 上 join() 会把 'a/b' 变成 'a\\b'，
  // 拿它和未规范化的 dir 做 startsWith 永远为假 —— 表现为所有文件 404
  //（用绝对 TEMP 路径时看不出来，换成仓库内的相对路径立刻暴露）。
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

export async function injectMediaInPage(cdp, baseUrl) {
  const script = `(async () => {
    const base = ${JSON.stringify(baseUrl)};
    const index = await (await fetch(base + '/index.json')).json();
    const names = ['index.json', index.initFile, ...index.segments.map((s) => s.file)];
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

export async function loadMedia(host, mediaDir, mediaServerRef) {
  try {
    await host.uploadDirectory('#media-dir-input', mediaDir)
  } catch {
    // 走注入路径
  }
  const filesCount = await host
    .evaluate("document.querySelector('#media-dir-input')?.files?.length ?? -1")
    .catch(() => -1)

  if (!(await host.evaluate('window.__pr.snapshot().media.loaded')) && filesCount > 0) {
    await host.evaluate(
      `document.querySelector('#media-dir-input').dispatchEvent(new Event('change', { bubbles: true })); 'ok'`,
    )
    await sleep(500)
  }

  if (!(await host.evaluate('window.__pr.snapshot().media.loaded'))) {
    mediaServerRef.server = await startMediaServer(mediaDir)
    const err = await injectMediaInPage(host, mediaServerRef.server.url)
    if (err) throw new Error(`注入媒体失败：${err}`)
  }

  return (await host.snapshot()).media
}

export async function createRoom(serverUrl) {
  const resp = await fetch(`${serverUrl}/api/rooms`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  })
  if (!resp.ok) throw new Error(`创建房间失败：HTTP ${resp.status}`)
  return (await resp.json()).roomId
}

export async function seedAndEnter(cdp, clientUrl, roomId, role, displayName) {
  await cdp.navigate(clientUrl)
  await cdp.evaluate(
    `sessionStorage.setItem('pr:join:${roomId}', ${JSON.stringify(
      JSON.stringify({ password: '', role, displayName }),
    )})`,
  )
  await cdp.navigate(`${clientUrl}/room/${roomId}`)
  await waitFor(async () => cdp.evaluate('typeof window.__pr !== "undefined"'), { label: '调试钩子就绪' })
  await waitFor(async () => (await cdp.snapshot()).joined, { label: `${displayName} 加入房间` })
}

export function percentile(sorted, ratio) {
  if (sorted.length === 0) return -1
  return sorted[Math.min(sorted.length - 1, Math.ceil(sorted.length * ratio) - 1)]
}

export function argOf(argv, name, fallback) {
  const i = argv.indexOf(`--${name}`)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}
