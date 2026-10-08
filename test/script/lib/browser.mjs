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

/**
 * roomId（大写）→ 主播复位令牌。
 *
 * 为什么需要：S-7 之后服务端只在 `POST /api/rooms` 的响应里下发一次 hostToken，
 * 主播在 60s 宽限期内接回主播位必须带上它（否则 HOST_TOKEN_REQUIRED）。脚本用
 * `seedAndEnter` 直接写 sessionStorage，若不把令牌一并写进去，就会出现"脚本自己
 * 把令牌丢了"导致的假失败（verify-room-resume 的 B6/B*）。这里由 createRoom 记录、
 * seedAndEnter 消费，调用方无需改签名。
 */
const hostTokens = new Map()

/**
 * serverUrl → `{token, user}` | `null`（见 ensureAccount 的说明）。
 *
 * 只装**确定性结论**：拿到凭据的会话，或者"账号能力确实关闭"的 null。
 * 暂时性失败（限速/网络/5xx）**不写这里** —— 否则同一个进程内后续调用会永远拿到 null。
 */
const accountCache = new Map()

/**
 * 最小 Cookie jar。
 *
 * 为什么需要：Node 的 fetch（undici）**不会**自动保存/回送 cookie，而一期的
 * refresh token 是 HttpOnly Cookie —— 刷新、重放、登出这些判据全部依赖它。
 * 浏览器侧由 Chrome 自己管，这里只为 Node 侧的 API 级验收服务。
 */
export function newCookieJar() {
  const jar = new Map()
  return {
    absorb(resp) {
      const list = typeof resp.headers.getSetCookie === 'function' ? resp.headers.getSetCookie() : []
      for (const raw of list) {
        const [pair] = String(raw).split(';')
        const idx = pair.indexOf('=')
        if (idx > 0) jar.set(pair.slice(0, idx).trim(), pair.slice(idx + 1).trim())
      }
      return jar
    },
    header() {
      return [...jar.entries()].map(([k, v]) => `${k}=${v}`).join('; ')
    },
    get(name) {
      return jar.get(name) ?? null
    },
    /** 只删某个 cookie（用于模拟"客户端丢了 cookie"这类判据）。 */
    clear(name) {
      jar.delete(name)
    },
  }
}

async function readAuthBody(resp) {
  const body = await resp.json().catch(() => null)
  return body
}

/**
 * 账号接口的失败错误。
 *
 * 把 **HTTP 状态码挂在错误对象上**（`err.status`）：调用方（ensureAccount）必须区分
 * "确定性的拒绝"（404/503 = 账号能力关闭；401/400 = 凭据/参数确实不行）与
 * "暂时性的失败"（429 限速、其它 5xx、网络不可达）—— 后者重试就可能成功，
 * 靠正则去匹配 message 太脆，所以这里显式给出。
 */
function authError(resp, body, what) {
  const code = body?.error?.code ?? body?.code ?? ''
  const message = body?.error?.message ?? body?.error ?? ''
  const err = new Error(
    `${what}失败：HTTP ${resp.status}${code ? ` ${code}` : ''}${message ? ` ${message}` : ''}`,
  )
  err.status = resp.status
  return err
}

/**
 * 注册/登录的限速退避：429 时按 1s / 2s / 4s 重试（最多 3 次重试、共 4 次请求）。
 *
 * 为什么要在这里退避而不是让调用方重试：注册桶默认只有 5/分钟、容量 3，而一次验收运行
 * 里"注册新账号"是该跑的（多个脚本各注册一个）—— 撞上限速是**预期内**的瞬时状态，
 * 用固定等待或直接放弃会把"跑得快"变成假失败。指数退避是这里唯一诚实的处理方式。
 */
const RATE_LIMIT_BACKOFF_MS = [1000, 2000, 4000]

/** 发一次请求，只在 429（限速）时退避重试；其它状态码原样返回给调用方判定。 */
async function fetchWithRateLimitRetry(url, init) {
  let resp
  for (let attempt = 0; ; attempt += 1) {
    resp = await fetch(url, init)
    if (resp.status !== 429 || attempt >= RATE_LIMIT_BACKOFF_MS.length) return resp
    await sleep(RATE_LIMIT_BACKOFF_MS[attempt])
  }
}

/** 注册一个账号，返回 {token, user}。成功状态码是 **201**（见 internal/handler/auth.go）。 */
export async function registerUser(serverUrl, { username, password, displayName, cookieJar } = {}) {
  const resp = await fetchWithRateLimitRetry(`${serverUrl}/api/auth/register`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password, displayName: displayName ?? username }),
  })
  cookieJar?.absorb(resp)
  const body = await readAuthBody(resp)
  if (!resp.ok) throw authError(resp, body, '注册')
  return { token: body.accessToken, user: body.user, username, status: resp.status }
}

/** 登录，返回 {token, user}。 */
export async function loginUser(serverUrl, { username, password, cookieJar } = {}) {
  const resp = await fetchWithRateLimitRetry(`${serverUrl}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  cookieJar?.absorb(resp)
  const body = await readAuthBody(resp)
  if (!resp.ok) throw authError(resp, body, '登录')
  return { token: body.accessToken, user: body.user, username, status: resp.status }
}

/**
 * 取一个可用的测试账号 token。
 *
 * 三条路径，按优先级：
 *  1. 已取过 → 直接复用（一次运行只注册/登录一次）；
 *  2. 设了 PR_TEST_USER/PR_TEST_PASS → 先登录（复用既有账号，避免每跑一次脚本
 *     就往库里塞一个用户）；登录失败再尝试注册；
 *  3. 否则注册一个随机账号（t_<随机>）。
 *
 * **账号能力未开启时返回 null 而不是抛错**：那时服务端不注册 /api/auth/*（返回 404），
 * 脚本应当退化为"匿名建房"，这样同一份脚本既能跑带账号的实例、也能跑没账号的实例
 *（部署是分两步走的：先上代码、再配 PR_DB_DSN）。
 */
export async function ensureAccount(serverUrl, { cookieJar } = {}) {
  // 缓存必须**按 serverUrl 分键**：同一次运行里脚本可能同时对着本地实例与公网实例
  // （例如"本地跑通再对线上复跑"），共用一个缓存会把 A 的 token 拿去打 B ——
  // 症状是 B 上出现"不该存在的登录态"（实测踩到过）。
  if (accountCache.has(serverUrl)) return accountCache.get(serverUrl)?.token ?? null
  const username = process.env.PR_TEST_USER || `t_${Math.random().toString(36).slice(2, 10)}`
  // 口令必须满足服务端策略：≥8 字符且同时含字母与数字。
  const password = process.env.PR_TEST_PASS || 'pr-test-pass1'

  // 缓存只写**确定性结论**（拿到 token，或"账号能力关闭"这种稳定事实）。
  // 暂时性失败（429 限速、5xx、网络不可达）**不写缓存** —— 否则一次限速会让整个
  // 进程在此后永远认为"没有账号"，症状是几条判据莫名其妙地全红（实测踩到过：
  // 注册桶 5/分钟用完后，同一进程内的后续调用再也拿不到 token）。
  const definitive = (acc) => {
    accountCache.set(serverUrl, acc)
    return acc?.token ?? null
  }
  const transient = () => null

  if (process.env.PR_TEST_USER) {
    try {
      const r = await withTransientBackoff(() => loginUser(serverUrl, { username, password, cookieJar }))
      return definitive({ token: r.token, user: r.user })
    } catch (err) {
      if (process.env.PR_TEST_STRICT === '1') throw err
      if (isTransientError(err)) return transient()
      // 非暂时性（口令错/账号不存在）→ 落到注册分支：固定的测试账号可能还没建出来
    }
  }
  try {
    const r = await withTransientBackoff(() => registerUser(serverUrl, { username, password, cookieJar }))
    return definitive({ token: r.token, user: r.user })
  } catch (err) {
    const msg = String(err?.message ?? err)
    if (msg.includes('HTTP 404') || msg.includes('HTTP 503')) {
      // 账号能力关闭：这是**合法部署形态**，不是错误，且是稳定事实 → 缓存。
      return definitive(null)
    }
    if (msg.includes('HTTP 409')) {
      // 随机用户名撞车（或固定账号已存在）→ 登录
      try {
        const r = await withTransientBackoff(() => loginUser(serverUrl, { username, password, cookieJar }))
        return definitive({ token: r.token, user: r.user })
      } catch (loginErr) {
        if (process.env.PR_TEST_STRICT === '1') throw loginErr
        return isTransientError(loginErr) ? transient() : definitive(null)
      }
    }
    if (process.env.PR_TEST_STRICT === '1') throw err
    return isTransientError(err) ? transient() : definitive(null)
  }
}

/** 该实例最近一次拿到的账号（含档案）；没拿到过返回 null。 */
export function lastAccount(serverUrl) {
  return accountCache.get(serverUrl) ?? null
}

/**
 * 清掉账号缓存。
 *
 * 用途：脚本明知服务端的限速桶已经回满（例如等过一个窗口）时，可以主动重试而
 * 不必重启进程。不传 serverUrl 就全清。
 */
export function resetAccountCache(serverUrl) {
  if (serverUrl) accountCache.delete(serverUrl)
  else accountCache.clear()
}

/** 429/5xx/网络类错误算"暂时性"：值得退避重试，且不该被缓存成结论。 */
export function isTransientError(err) {
  const msg = String(err?.message ?? err)
  return /HTTP 429|HTTP 5\d\d|ECONNREFUSED|ECONNRESET|fetch failed|网络|timeout|timed out/i.test(msg)
}

/**
 * 对暂时性错误做指数退避重试（1s/2s/4s，最多 4 次尝试）。
 *
 * 为什么注册/登录需要它：注册桶默认 5/分钟、容量 3，而一条完整验收会连续用到
 * 注册、建房、ws-ticket 几个入口；没有退避的话，脚本会因为"跑得快"而自己撞上限速。
 * 注意这是**脚手架**的退避，不改变任何服务端判据。
 */
async function withTransientBackoff(fn, attempts = 4, baseMs = 1000) {
  let lastErr
  for (let i = 0; i < attempts; i++) {
    try {
      return await fn()
    } catch (err) {
      lastErr = err
      if (!isTransientError(err) || i === attempts - 1) throw err
      await new Promise((r) => setTimeout(r, baseMs * 2 ** i))
    }
  }
  throw lastErr
}

export async function createRoom(serverUrl, { token } = {}) {
  // 建房必须登录（ACCOUNTS §6）。ensureAccount 会在账号能力关闭时返回 null，
  // 此时不带 Authorization —— 那样脚本仍能对"无账号"的实例跑通。
  const bearer = token ?? (await ensureAccount(serverUrl))
  const resp = await fetch(`${serverUrl}/api/rooms`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(bearer ? { Authorization: `Bearer ${bearer}` } : {}),
    },
    body: '{}',
  })
  if (!resp.ok) throw new Error(`创建房间失败：HTTP ${resp.status}`)
  const data = await resp.json()
  if (data.roomId && data.hostToken) {
    hostTokens.set(String(data.roomId).toUpperCase(), data.hostToken)
  }
  return data.roomId
}

/**
 * 直接写账号会话（供 `seedAndEnter` 与需要"已登录窗口"的脚本复用）。
 *
 * 两个键名以 `client/src/stores/auth.ts` 为准：access token 只在
 * `sessionStorage['pr:access']`，档案在 `localStorage['pr:profile']`（仅显示用）。
 * 必须在导航到目标页**之前**写入，否则页面初始化时读不到。
 */
export async function seedAccount(cdp, account) {
  if (!account?.token) return
  await cdp.evaluate(`sessionStorage.setItem('pr:access', ${JSON.stringify(account.token)})`)
  if (account.user) {
    await cdp.evaluate(
      `localStorage.setItem('pr:profile', ${JSON.stringify(JSON.stringify(account.user))})`,
    )
  }
}

export async function seedAndEnter(cdp, clientUrl, roomId, role, displayName, hostToken, account) {
  await cdp.navigate(clientUrl)
  // 显式传入优先；否则用 createRoom 记下的令牌（键与客户端 joinSession 一致：大写房间码）。
  const token = hostToken ?? hostTokens.get(String(roomId).toUpperCase())
  const stored = { password: '', role, displayName }
  if (token) stored.hostToken = token
  await cdp.evaluate(
    `sessionStorage.setItem('pr:join:${String(roomId).toUpperCase()}', ${JSON.stringify(
      JSON.stringify(stored),
    )})`,
  )
  // 账号会话：**只有传了 account 才播种**。
  //
  // 为什么房主侧必须传：主播重建房间走 `auth.authedFetch('/api/rooms')`（建房需登录），
  // 而房间被宽限期回收后重建失败会让 verify-room-resume 的恢复判据假失败 ——
  // 服务端日志里只会看到"加入 XXX 被拒绝: 房间不存在"，没有任何新建房记录（A/B 实证）。
  // 为什么观众侧**不能**传：游客进房是产品决定，判据"无账号会话也能进房"依赖它。
  await seedAccount(cdp, account)
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
