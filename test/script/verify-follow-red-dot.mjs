#!/usr/bin/env node
/**
 * verify-follow-red-dot.mjs —— 票 #67「粉丝红点 + 双 badge + 在线轻提示」的真机验收。
 *
 * # 为什么必须有真后端
 *
 * 三条核心判据都只存在于真实现里：
 *   1. `newCount` 是服务端按 `followers_seen_at` 水位线算出来的（mock 里是一组 id）；
 *   2. 水位线更新端点真的落库（`PUT /api/me/followers-seen`）；
 *   3. 轻提示靠 `/ws/user` 推送 —— mock 态下没有任何东西在推。
 *
 * # 判据
 *
 *   R0  两账号就绪，且 alice 的粉丝红点（API 口径）为 1、消息未读为 0
 *       —— 这一条同时钉住"两数两源"：红点亮着而 unread 是 0。
 *   R1  顶栏「好友」出现角标，数字与 API 一致（同一个数字的第二个显示位置）。
 *   R2  在线时底部飘出一条轻提示，文案含对方名字与「关注」。
 *   R3  点提示 → 落到 `/friends?tab=followers`。
 *   R4  进粉丝 tab 后红点灭（顶栏角标与 tab 角标都消失）。
 *   R5  API 口径也归零（水位线真的落库了，不只是本地藏起来）。
 *   R6  互关时提示说「好友」（决议 #42 的措辞），且红点不会再亮（回关的人不算新粉丝）。
 *   R7  取关/被取关**静默**：没有新提示（决议 §7）。
 *   R8  源码级：`ToastLayer.vue` 有 reduced-motion 分支且**只取消动画、不取消提示**；
 *       红点数**没有**写进 `stores/auth.ts`（决议 §3 的"绝不进 unread"）。
 *
 * # 实例要求（不满足时明确 SKIP、退出码 2，绝不假绿）
 *
 *   - `PR_OTP_DEV_ECHO=1`（要注册账号）；
 *   - 注册/登录限速放宽（一轮建两个号 + 多次登录）；
 *   - `PR_ALLOWED_ORIGINS` **不必**包含本脚本的静态服务器端口：本脚本在
 *     反代 `/ws/user` 的 Upgrade 时把 Origin 改写成后端自己的 origin
 *     （与 HTTP 反代同一处理）。这是有意的 —— 否则每跑一次脚本都要改实例配置。
 *   - 前端产物必须是**真实接口**构建（见用法）。
 *
 * 用法：
 *   cd client && VITE_MOCK_FOLLOWS=0 VITE_MOCK_FRIENDS=0 VITE_MOCK_USERS=0 \
 *     VITE_MOCK_POSTS=0 npx vite build
 *   node test/script/verify-follow-red-dot.mjs --server http://127.0.0.1:8899
 *
 * 退出码：全部判据 PASS -> 0；任一 FAIL -> 1；环境不满足 -> 2。
 */
import { createServer, request as httpRequest } from 'node:http'
import { connect as netConnect } from 'node:net'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9921'))
const SERVER = argOf(process.argv, 'server', 'http://127.0.0.1:8888').replace(/\/+$/, '')

function argOf(argv, name, fallback) {
  const i = argv.indexOf('--' + name)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}

let passed = 0
let failed = 0
let skipped = 0
const evidence = {}

function record(name, pass, detail) {
  if (pass) passed += 1
  else failed += 1
  console.log('[' + (pass ? 'PASS' : 'FAIL') + '] ' + name + (detail ? ' — ' + detail : ''))
}

function skipOne(name, reason) {
  skipped += 1
  console.log('[SKIP] ' + name + ' —— ' + reason)
}

function skipEnv(reason) {
  console.error('[SKIP] 环境不满足：' + reason)
  process.exit(2)
}

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.json': 'application/json; charset=utf-8',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
  '.map': 'application/json; charset=utf-8',
}

/** 把 /api/* 反代到真后端（同源，避开 CSP/CORS 噪声）。 */
function proxy(req, res) {
  const target = new URL(SERVER)
  const upstream = httpRequest(
    {
      hostname: target.hostname,
      port: target.port,
      path: req.url,
      method: req.method,
      headers: { ...req.headers, host: target.host, origin: SERVER },
    },
    (up) => {
      res.writeHead(up.statusCode == null ? 502 : up.statusCode, up.headers)
      up.pipe(res)
    },
  )
  upstream.on('error', () => {
    if (!res.headersSent) res.writeHead(502, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ error: 'upstream unreachable' }))
  })
  req.pipe(upstream)
}

/**
 * `/ws/user` 的 Upgrade 反代（本票独有）。
 *
 * 前几个真机脚本都不需要它 —— 它们判的是 REST。本票的轻提示**只**走 WS，
 * 所以这里必须把 Upgrade 请求也送过去，否则页面永远收不到事件，
 * 判据会以"等提示超时"的形式假红。
 *
 * Origin 改写成后端自己的 origin：后端的 `/ws` 有 Origin 白名单（默认只含
 * 5173 与 8888），而本脚本的静态服务器每次跑都在**随机端口**上。
 * 让脚本去改实例配置是错的解法 —— 反代本来就该站在后端角度发请求。
 */
function proxyUpgrade(req, socket, head) {
  const target = new URL(SERVER)
  const headers = { ...req.headers, host: target.host, origin: SERVER }
  const lines = [`${req.method} ${req.url} HTTP/1.1`]
  for (const [k, v] of Object.entries(headers)) {
    if (Array.isArray(v)) for (const one of v) lines.push(`${k}: ${one}`)
    else if (v !== undefined) lines.push(`${k}: ${v}`)
  }
  const up = netConnect(Number(target.port), target.hostname, () => {
    up.write(lines.join('\r\n') + '\r\n\r\n')
    if (head && head.length) up.write(head)
    up.pipe(socket)
    socket.pipe(up)
  })
  up.on('error', () => socket.destroy())
  socket.on('error', () => up.destroy())
}

function startStatic(dir) {
  return new Promise((done) => {
    const server = createServer((req, res) => {
      const path = new URL(req.url || '/', 'http://127.0.0.1').pathname
      if (path.startsWith('/api/')) {
        proxy(req, res)
        return
      }
      let file = resolve(dir, '.' + path)
      if (file !== dir && !file.startsWith(dir + sep)) {
        res.writeHead(403)
        res.end()
        return
      }
      if (existsSync(file) && statSync(file).isDirectory()) file = join(file, 'index.html')
      if (!existsSync(file) || !statSync(file).isFile()) file = join(dir, 'index.html')
      const body = readFileSync(file)
      res.writeHead(200, {
        'Content-Type': MIME[extname(file)] || 'application/octet-stream',
        'Content-Length': body.length,
      })
      res.end(body)
    })
    server.on('upgrade', proxyUpgrade)
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address()
      done({ url: 'http://127.0.0.1:' + port, close: () => server.close() })
    })
  })
}

function freshUsername(prefix) {
  return prefix + Date.now().toString(36) + Math.floor(Math.random() * 1000).toString(36)
}

/** 走完整注册链路建一个账号（两段式 + 邮箱验证码），返回首个会话。 */
async function registerAccount(prefix, password) {
  const username = freshUsername(prefix)
  const reg = await fetch(SERVER + '/api/auth/register/start', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() },
    body: JSON.stringify({ username, password }),
  })
  if (!reg.ok) skipEnv('注册失败（HTTP ' + reg.status + '）')
  const regStart = await reg.json()
  const email = username + '@example.test'
  const sent = await fetch(SERVER + '/api/auth/register/email', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() },
    body: JSON.stringify({ ticket: regStart.ticket, email }),
  })
  if (!sent.ok) skipEnv('发验证码失败（HTTP ' + sent.status + '）：需要 PR_OTP_DEV_ECHO=1 的实例')
  const sentBody = await sent.json().catch(() => ({}))
  const code = sentBody.devCode || sentBody.dev_code || ''
  if (!code) skipEnv('响应里没有 devCode —— 实例没开 PR_OTP_DEV_ECHO=1，拿不到验证码')
  const verified = await fetch(SERVER + '/api/auth/register/verify', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() },
    body: JSON.stringify({ ticket: regStart.ticket, code }),
  })
  if (!verified.ok) skipEnv('建号失败（HTTP ' + verified.status + '）')
  const account = await verified.json()
  if (!account.user || !account.user.id) skipEnv('注册响应里没有 user.id')
  return { username, password, user: account.user, accessToken: account.accessToken }
}

/** 以某个账号发一次 REST（本脚本只用它造关注边与读红点）。 */
async function callApi(account, method, path, body) {
  const init = { method, headers: { Authorization: 'Bearer ' + account.accessToken } }
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  const r = await fetch(SERVER + path, init)
  const text = await r.text()
  let json = null
  try {
    json = text ? JSON.parse(text) : null
  } catch {
    json = null
  }
  return { status: r.status, body: json, raw: text }
}

async function follow(actor, targetUserId) {
  return callApi(actor, 'POST', '/api/follows', { userId: targetUserId })
}

async function unfollow(actor, targetUserId) {
  return callApi(actor, 'DELETE', '/api/follows/' + targetUserId)
}

/** 粉丝 tab 的 `newCount`（服务端口径）。字段缺失时返回 null —— 与 0 不同。 */
async function apiNewCount(account) {
  const r = await callApi(account, 'GET', '/api/follows?tab=followers')
  if (r.status !== 200 || !r.body) return { status: r.status, newCount: null, items: [] }
  return {
    status: r.status,
    newCount: typeof r.body.newCount === 'number' ? r.body.newCount : null,
    items: r.body.items ?? [],
  }
}

/** 消息未读数（`/api/auth/me` 的 `unread`），用来钉"两数两源"。 */
async function apiUnread(account) {
  const r = await callApi(account, 'GET', '/api/auth/me')
  if (r.status !== 200 || !r.body) return null
  return typeof r.body.unread === 'number' ? r.body.unread : null
}

/** 把会话种进页面：access token 只进 sessionStorage（规格 §5），profile 供显示。 */
async function seedSession(cdp, site, account) {
  await cdp.navigate(site.url + '/')
  await cdp.evaluate(
    'sessionStorage.setItem("pr:access", ' +
      JSON.stringify(account.accessToken) +
      '); localStorage.setItem("pr:profile", ' +
      JSON.stringify(JSON.stringify(account.user)) +
      '); "ok"',
  )
}

/**
 * 读红点与提示的可观测状态。
 *
 * ⚠️ `cdp.evaluate` 的脚本里**不要写 `//` 行注释** —— 那段脚本会被折叠，
 * 行注释会把同一行后面的代码一起吃掉（verify-following 踩过）。
 */
const READ = `(() => {
  const txt = (el) => (el ? (el.textContent || '').replace(/\\s+/g, ' ').trim() : null);
  const headerDot = document.querySelector('[data-testid=follow-red-dot]');
  const tabDot = document.querySelector('[data-testid=followers-red-dot]');
  const toasts = Array.from(document.querySelectorAll('[data-testid=follow-toast]')).map((t) =>
    (t.textContent || '').replace(/\\s+/g, ' ').trim(),
  );
  return {
    path: location.pathname + location.search,
    headerDot: headerDot ? txt(headerDot) : null,
    tabDot: tabDot ? txt(tabDot) : null,
    toasts,
  };
})()`

function readState(cdp) {
  return cdp.evaluate(READ)
}

/** 等到某个提示出现（或超时后返回当时的快照，由判据自己报红）。 */
async function waitToast(cdp, needle, timeoutMs = 12000) {
  const deadline = Date.now() + timeoutMs
  let last = null
  while (Date.now() < deadline) {
    last = await readState(cdp)
    if (last.toasts.some((t) => t.includes(needle))) return last
    await sleep(200)
  }
  return last
}

function stripComments(src) {
  return src
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '')
}

function readRepoFile(rel) {
  const p = join(REPO_ROOT, rel)
  if (!existsSync(p)) return ''
  return readFileSync(p, 'utf8')
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    skipEnv('没有 client/dist/index.html —— 先构建前端')
  }

  // 环境探针：票 #67 的端点在不在。不在就明确 SKIP，不假装绿。
  const probe = await fetch(SERVER + '/api/me/followers-seen', { method: 'PUT' })
  if (probe.status === 404) skipEnv('实例没有 PUT /api/me/followers-seen（票 #67 的端点）')

  const alice = await registerAccount('rdalice', 'AlicePass123')
  const bob = await registerAccount('rdbob', 'BobPass123')
  evidence.accounts = { alice: alice.user.id, bob: bob.user.id }

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'follow-red-dot')
  let cdp
  try {
    cdp = await openTarget(BASE_PORT, 'alice')
    await seedSession(cdp, site, alice)
    // 会话种好后必须**重载**：`stores/auth.ts` 的 bootstrap 在应用启动时读
    // sessionStorage，种完之后不重载的话页面还是游客态（通道也不会连）。
    await cdp.navigate(site.url + '/')
    await waitFor(async () => (await cdp.evaluate('!!sessionStorage.getItem("pr:access")')) === true, {
      label: '会话已就绪',
    })
    // 给 WS 通道一点时间连上（它要在登录态确定后才取票 + 握手）。
    await sleep(1500)

    // ---- R0：造一条粉丝边，并确认"两数两源" ----
    const followResp = await follow(bob, alice.user.id)
    if (followResp.status !== 201) {
      skipEnv('bob 关注 alice 失败（HTTP ' + followResp.status + '）：' + followResp.raw.slice(0, 120))
    }
    const firstCount = await apiNewCount(alice)
    const unread = await apiUnread(alice)
    evidence.R0 = { newCount: firstCount.newCount, unread }
    record(
      'R0 粉丝红点为 1，且消息未读为 0（两数两源）',
      firstCount.newCount === 1 && unread === 0,
      JSON.stringify(evidence.R0),
    )

    // ---- R1 + R2：顶栏角标与在线轻提示 ----
    const afterFollow = await waitToast(cdp, bob.username)
    evidence.R1 = { headerDot: afterFollow.headerDot, toasts: afterFollow.toasts }
    record(
      'R1 顶栏「好友」角标出现且数字是 1',
      afterFollow.headerDot === '1',
      'headerDot=' + JSON.stringify(afterFollow.headerDot),
    )
    const toastText = afterFollow.toasts.find((t) => t.includes(bob.username)) ?? ''
    record(
      'R2 在线时飘出一条提示，文案含对方名字与「关注」',
      toastText.includes(bob.username) && toastText.includes('关注'),
      'toast=' + JSON.stringify(toastText),
    )

    // ---- R3：点提示落到粉丝 tab ----
    const clicked = await cdp.evaluate(`(() => {
      const t = document.querySelector('[data-testid=follow-toast]');
      if (!t) return { ok: false, why: 'no-toast' };
      t.click();
      return { ok: true };
    })()`)
    let landed = null
    const deadline = Date.now() + 8000
    while (Date.now() < deadline) {
      landed = await readState(cdp)
      if (landed.path.startsWith('/friends') && landed.path.includes('tab=followers')) break
      await sleep(200)
    }
    record(
      'R3 点提示落到 /friends?tab=followers',
      clicked.ok === true && landed !== null && landed.path.includes('tab=followers'),
      'path=' + JSON.stringify(landed && landed.path),
    )

    // ---- R4 + R5：进 tab 即灭（界面 + 服务端） ----
    await sleep(1200) // 等水位线那条 PUT 走完（服务端异步单写，给它一点时间）
    const cleared = await readState(cdp)
    const clearedApi = await apiNewCount(alice)
    evidence.R4 = { headerDot: cleared.headerDot, tabDot: cleared.tabDot, apiNewCount: clearedApi.newCount }
    record(
      'R4 进粉丝 tab 后红点灭（顶栏与 tab 都没有角标）',
      cleared.headerDot === null && cleared.tabDot === null,
      JSON.stringify(evidence.R4),
    )
    record(
      'R5 服务端口径也归零（水位线真的落库了）',
      clearedApi.newCount === 0,
      'newCount=' + JSON.stringify(clearedApi.newCount),
    )

    // ---- R6：互关的提示说「好友」，且不会再点亮红点 ----
    const backFollow = await follow(alice, bob.user.id)
    if (backFollow.status !== 201) {
      skipOne('R6 互关提示', 'alice 回关失败（HTTP ' + backFollow.status + '）')
    } else {
      const mutualToast = await waitToast(cdp, '好友')
      const mutualText = mutualToast.toasts.find((t) => t.includes('好友')) ?? ''
      const afterMutual = await apiNewCount(alice)
      evidence.R6 = { toast: mutualText, newCount: afterMutual.newCount }
      record(
        'R6 互关提示说「好友」，且回关后红点不亮',
        mutualText.includes('好友') && afterMutual.newCount === 0,
        JSON.stringify(evidence.R6),
      )
    }

    // ---- R7：取关静默 ----
    const before = (await readState(cdp)).toasts.length
    await unfollow(bob, alice.user.id)
    await sleep(2500) // 足够一条提示走完 4s 的前半程：若真的推了，这里必然能看见
    const afterUnfollow = await readState(cdp)
    const grew = afterUnfollow.toasts.length > before
    record(
      'R7 取关一律静默（没有新提示）',
      !grew,
      'before=' + before + ' after=' + afterUnfollow.toasts.length,
    )
  } finally {
    try {
      if (cdp) await cdp.close?.()
    } catch {
      /* 关不掉不影响结论 */
    }
    chrome.close()
    site.close()
  }

  // ---- R8：源码级（不依赖浏览器） ----
  const toastSrc = stripComments(readRepoFile('client/src/components/ui/ToastLayer.vue'))
  const hasReduced = /prefers-reduced-motion/.test(toastSrc)
  const keepsToast = !/prefers-reduced-motion[\s\S]{0,400}?display:\s*none/.test(toastSrc)
  record(
    'R8a ToastLayer 有 reduced-motion 分支，且只取消动画不取消提示',
    hasReduced && keepsToast,
    'hasReduced=' + hasReduced + ' keepsToast=' + keepsToast,
  )

  const authStoreSrc = stripComments(readRepoFile('client/src/stores/auth.ts'))
  const followStoreSrc = stripComments(readRepoFile('client/src/stores/follows.ts'))
  const leaked = /newFollowers|newCount|followers_seen/.test(authStoreSrc)
  record(
    'R8b 粉丝红点数没有写进 stores/auth.ts（决议 §3：绝不进 unread）',
    !leaked && /newFollowers/.test(followStoreSrc),
    'authLeak=' + leaked,
  )

  console.log('')
  console.log('判据：' + passed + '/' + (passed + failed) + ' 通过' + (skipped ? '，' + skipped + ' 条 SKIP' : ''))
  console.log('判定：' + (failed === 0 ? 'PASS' : 'FAIL'))
  if (Object.keys(evidence).length) console.log('证据：' + JSON.stringify(evidence, null, 2))
  process.exit(failed === 0 ? 0 : 1)
}

main().catch((err) => {
  console.error('[ERROR] ' + (err && err.stack ? err.stack : String(err)))
  process.exit(1)
})
