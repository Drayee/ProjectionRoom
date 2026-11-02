#!/usr/bin/env node
/**
 * verify-me-settings.mjs —— 票 #71「设置区：当前密码改密 + 退出所有设备 + 只读信息」的真机验收。
 *
 * # 为什么必须有真后端
 *
 * 本票的核心判据全是**服务端的会话状态**：
 *   - 改密之后"其他设备被蹬、这一台还在"——这由 `token_version` 自增 + 撤销全部会话 +
 *     给当前设备补发新会话三件事共同决定，mock 域里根本没有会话这回事；
 *   - "退出所有设备"同理。
 * 用 mock 证明这三条等于什么都没证。
 *
 * # 判据
 *
 *   S0  两个账号会话就绪（同一账号登录两次，模拟两台设备）。
 *   S1  `/me?tab=settings` 渲染出三块：改密码 / 退出所有设备 / 账号信息，
 *       且只读块里加入时间用**绝对日期**（不是「三年前」）。
 *   S2  当前密码填错 → 页内给出人话（不是"服务端内部错误"），且**会话不受影响**
 *       （页面还在登录态、`/api/auth/me` 仍 200）。
 *   S3  两次新密码不一致 → 前端就地拦下（不发请求，服务端密码没被改）。
 *   S4  弱口令 → 页内报策略原因。
 *   S5  改密成功 → 提示「其他设备已退出」，页面**仍在登录态**（不是被蹬回游客），
 *       且 `sessionStorage` 里的 access token **换成了新的一枚**。
 *   S6  另一台设备的旧 access token 已失效（`/api/auth/me` 401），旧 refresh Cookie 也刷不动。
 *   S7  旧口令登录失败、新口令登录成功（口令真的换了）。
 *   S8  「退出所有设备」走页内确认条（不是 `window.confirm`），确认后页面变游客态。
 *   S9  退出所有设备之后，刚才那枚 token 也失效了。
 *
 * # 实例要求（不满足时明确 SKIP、退出码 2，绝不假绿）
 *
 *   - `PR_OTP_DEV_ECHO=1`（要注册账号）；
 *   - 注册/登录限速放宽（一轮建号 + 多次登录）；
 *   - 前端产物必须是**真实接口**构建（见用法）。
 *
 * 用法：
 *   cd client && VITE_MOCK_USERS=0 VITE_MOCK_POSTS=0 npx vite build
 *   node test/script/verify-me-settings.mjs --server http://127.0.0.1:8899
 *
 * 退出码：全部判据 PASS -> 0；任一 FAIL -> 1；环境不满足 -> 2。
 */
import { createServer, request as httpRequest } from 'node:http'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9920'))
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

/** 把 /api/* 与 /ws 反代到真后端（同源，避开 CSP/CORS 噪声）。 */
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

function startStatic(dir) {
  return new Promise((done) => {
    const server = createServer((req, res) => {
      const path = new URL(req.url || '/', 'http://127.0.0.1').pathname
      if (path.startsWith('/api/') || path === '/ws') {
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
  return {
    username,
    password,
    user: account.user,
    accessToken: account.accessToken,
    refreshCookie: cookieOf(verified),
  }
}

/** 登录一次，返回一个独立会话（模拟另一台设备）。 */
async function loginAccount(username, password) {
  const r = await fetch(SERVER + '/api/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!r.ok) return null
  const body = await r.json()
  if (!body.accessToken) return null
  return { accessToken: body.accessToken, refreshCookie: cookieOf(r) }
}

/** 从响应头里取 `pr_refresh` 的值（真机脚本里没有 CookieJar，手取）。 */
function cookieOf(resp) {
  const raw = resp.headers.getSetCookie ? resp.headers.getSetCookie() : []
  for (const line of raw) {
    const m = /(?:^|,\s*)pr_refresh=([^;]+)/.exec(line)
    if (m) return m[1]
  }
  const single = resp.headers.get('set-cookie') || ''
  const m = /pr_refresh=([^;]+)/.exec(single)
  return m ? m[1] : ''
}

async function meStatus(accessToken) {
  const r = await fetch(SERVER + '/api/auth/me', {
    headers: { Authorization: 'Bearer ' + accessToken },
  })
  return r.status
}

async function refreshStatus(cookie) {
  if (!cookie) return 0
  const r = await fetch(SERVER + '/api/auth/refresh', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Cookie: 'pr_refresh=' + cookie },
  })
  return r.status
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
 * 读设置区的可观测状态。
 *
 * ⚠️ `cdp.evaluate` 的脚本里**不要写 `//` 行注释** —— 那段脚本会被折叠，
 * 行注释会把同一行后面的代码一起吃掉（verify-following 踩过）。
 */
const READ_SETTINGS = `(() => {
  const txt = (el) => (el ? (el.textContent || '').replace(/\\s+/g, ' ').trim() : '');
  const sect = document.querySelector('.sect');
  const guest = document.querySelector('[data-testid=async-state]');
  const kv = {};
  const kvTitle = {};
  document.querySelectorAll('[data-testid=account-readonly] dt').forEach((dt) => {
    const dd = dt.nextElementSibling;
    kv[txt(dt)] = txt(dd);
    const t = dd ? dd.querySelector('time') : null;
    kvTitle[txt(dt)] = t ? (t.getAttribute('title') || '') : '';
  });
  return {
    path: location.pathname + location.search,
    selected: (() => {
      const t = [...document.querySelectorAll('nav.tabs [role=tab]')].find((b) => b.getAttribute('aria-selected') === 'true');
      return t ? txt(t) : '';
    })(),
    heads: [...document.querySelectorAll('.sect h2')].map(txt),
    guest: Boolean(guest),
    guestText: txt(guest),
    notice: txt(document.querySelector('[data-testid=settings-notice]')),
    error: txt(document.querySelector('[data-testid=settings-error]')),
    currentError: txt(document.querySelector('#me-current-password-error')),
    newError: txt(document.querySelector('#me-new-password-error')),
    hasCurrent: Boolean(document.querySelector('[data-testid=current-password]')),
    hasNew: Boolean(document.querySelector('[data-testid=new-password]')),
    hasConfirm: Boolean(document.querySelector('[data-testid=confirm-password]')),
    hasKickStart: Boolean(document.querySelector('[data-testid=kickall-start]')),
    hasKickConfirm: Boolean(document.querySelector('[data-testid=kickall-confirm]')),
    readonly: kv,
    readonlyTitle: kvTitle,
    token: sessionStorage.getItem('pr:access') || '',
    body: txt(sect || document.body).slice(0, 400),
  };
})()`

async function openSettings(cdp, site) {
  await cdp.navigate(site.url + '/me?tab=settings')
  await waitFor(
    async () => {
      const s = await cdp.evaluate(READ_SETTINGS)
      return s.hasCurrent || s.guest
    },
    { label: '设置区渲染出改密码表单', timeoutMs: 25000 },
  )
  await sleep(400)
  return cdp.evaluate(READ_SETTINGS)
}

/** 填三栏并提交（清空后逐栏输入，避免 v-model 抢焦点）。 */
async function fillAndSubmit(cdp, current, next, confirm) {
  await cdp.evaluate(`(() => {
    const set = (sel, value) => {
      const el = document.querySelector(sel);
      if (!el) return false;
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(el, value);
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
      return true;
    };
    set('[data-testid=current-password]', ${JSON.stringify(current)});
    set('[data-testid=new-password]', ${JSON.stringify(next)});
    set('[data-testid=confirm-password]', ${JSON.stringify(confirm)});
    return 'ok';
  })()`)
  await sleep(120)
  await cdp.evaluate(
    "document.querySelector('[data-testid=change-password-submit]').click(); 'ok'",
  )
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const oldPassword = 'Set71-old-1'
  const newPassword = 'Set71-new-2'

  // ---------- S0 环境与两个会话 ----------
  const account = await registerAccount('set71', oldPassword)
  const otherDevice = await loginAccount(account.username, oldPassword)
  if (!otherDevice || !otherDevice.refreshCookie) skipEnv('第二次登录没拿到独立会话，无法判"其他设备被蹬"')
  if (account.accessToken === otherDevice.accessToken) skipEnv('两次登录拿到同一枚 access token（会话没有轮换）')
  evidence.sessions = {
    user: account.username,
    device1: account.accessToken.slice(0, 12) + '…',
    device2: otherDevice.accessToken.slice(0, 12) + '…',
  }
  record(
    'S0 两个账号会话就绪（同一账号登录两次，模拟两台设备）',
    (await meStatus(account.accessToken)) === 200 && (await meStatus(otherDevice.accessToken)) === 200,
    JSON.stringify({ device1: evidence.sessions.device1, device2: evidence.sessions.device2 }),
  )

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'me-settings')
  const cdp = await openTarget(BASE_PORT, 'me-settings')

  try {
    await seedSession(cdp, site, account)
    const first = await openSettings(cdp, site)
    evidence.settings = first
    record('S1a ?tab=settings 选中「设置」', first.selected === '设置', first.selected)
    record(
      'S1b 三块都在：改密码 / 退出所有设备 / 账号信息',
      first.hasCurrent && first.hasNew && first.hasConfirm && first.hasKickStart &&
        ['改密码', '退出所有设备', '账号信息'].every((h) => first.heads.includes(h)),
      first.heads.join(' / '),
    )
    record(
      'S1c 只读块：加入时间显示精确绝对时间（YYYY-MM-DD HH:mm，不是「刚刚」）',
      /\d{4}-\d{2}-\d{2}/.test(first.readonly['加入时间'] || ''),
      JSON.stringify({ shown: first.readonly['加入时间'] }),
    )

    // ---------- S2 当前密码填错 ----------
    await fillAndSubmit(cdp, 'definitely-wrong-1', newPassword, newPassword)
    await waitFor(
      async () => {
        const s = await cdp.evaluate(READ_SETTINGS)
        return s.currentError !== '' || s.error !== ''
      },
      { label: '错密码后出现页内提示', timeoutMs: 20000 },
    )
    const wrong = await cdp.evaluate(READ_SETTINGS)
    evidence.wrongCurrent = wrong
    record(
      'S2a 当前密码填错 → 页内给出人话（不是"服务端内部错误"）',
      (wrong.currentError + wrong.error).includes('当前密码') &&
        !(wrong.currentError + wrong.error).includes('内部错误'),
      JSON.stringify({ currentError: wrong.currentError, error: wrong.error }),
    )
    record(
      'S2b 失败的改密不影响当前会话（页面仍在登录态）',
      !wrong.guest && (await meStatus(account.accessToken)) === 200,
      JSON.stringify({ guest: wrong.guest, me: await meStatus(account.accessToken) }),
    )

    // ---------- S3 两次输入不一致（前端拦下） ----------
    await fillAndSubmit(cdp, oldPassword, newPassword, newPassword + 'x')
    await sleep(600)
    const mismatch = await cdp.evaluate(READ_SETTINGS)
    evidence.mismatch = mismatch
    record(
      'S3 两次新密码不一致 → 前端就地拦下，且口令没被改',
      (mismatch.error + mismatch.newError).includes('不一致') &&
        (await loginAccount(account.username, oldPassword)) !== null,
      JSON.stringify({ error: mismatch.error }),
    )

    // ---------- S4 弱口令 ----------
    await fillAndSubmit(cdp, oldPassword, 'short', 'short')
    await waitFor(
      async () => {
        const s = await cdp.evaluate(READ_SETTINGS)
        return s.newError !== '' || s.error !== ''
      },
      { label: '弱口令后出现页内提示', timeoutMs: 20000 },
    )
    const weak = await cdp.evaluate(READ_SETTINGS)
    evidence.weak = weak
    record(
      'S4 弱口令 → 页内报策略原因（至少 8 位 / 含字母与数字）',
      /至少|字母|数字/.test(weak.newError + weak.error),
      JSON.stringify({ newError: weak.newError, error: weak.error }),
    )

    // ---------- S5 改密成功 ----------
    await fillAndSubmit(cdp, oldPassword, newPassword, newPassword)
    await waitFor(
      async () => {
        const s = await cdp.evaluate(READ_SETTINGS)
        return s.notice !== ''
      },
      { label: '改密成功提示', timeoutMs: 25000 },
    )
    const ok = await cdp.evaluate(READ_SETTINGS)
    evidence.changed = ok
    record(
      'S5a 改密成功 → 提示里说明"其他设备已退出"',
      ok.notice.includes('其他设备') && ok.notice.includes('密码'),
      ok.notice,
    )
    record('S5b 改密后当前设备仍在登录态（没被蹬回游客）', !ok.guest, String(ok.guest))
    record(
      'S5c sessionStorage 里的 access token 换成了新的一枚',
      ok.token !== '' && ok.token !== account.accessToken && (await meStatus(ok.token)) === 200,
      JSON.stringify({ changed: ok.token !== account.accessToken, me: await meStatus(ok.token) }),
    )

    // ---------- S6 另一台设备被蹬 ----------
    const otherMe = await meStatus(otherDevice.accessToken)
    const otherRefresh = await refreshStatus(otherDevice.refreshCookie)
    evidence.other = { me: otherMe, refresh: otherRefresh }
    record('S6a 另一台设备的旧 access token 已失效（401）', otherMe === 401, String(otherMe))
    record('S6b 另一台设备的旧 refresh Cookie 也刷不动', otherRefresh !== 200, String(otherRefresh))

    // ---------- S7 口令真的换了 ----------
    const oldLogin = await loginAccount(account.username, oldPassword)
    const newLogin = await loginAccount(account.username, newPassword)
    evidence.relogin = { old: Boolean(oldLogin), fresh: Boolean(newLogin) }
    record('S7a 旧口令登录失败', oldLogin === null, JSON.stringify(evidence.relogin))
    record('S7b 新口令登录成功', newLogin !== null, JSON.stringify(evidence.relogin))

    // ---------- S8 退出所有设备（页内确认条） ----------
    const beforeKick = await openSettings(cdp, site)
    // 重新打开页面会走 bootstrap → refresh，拿到的是改密后那枚会话的延续。
    const tokenBeforeKick = beforeKick.token
    await cdp.evaluate("document.querySelector('[data-testid=kickall-start]').click(); 'ok'")
    await waitFor(
      async () => (await cdp.evaluate(READ_SETTINGS)).hasKickConfirm,
      { label: '退出所有设备的确认条出现', timeoutMs: 15000 },
    )
    const confirming = await cdp.evaluate(READ_SETTINGS)
    record(
      'S8a 点「退出所有设备」先出页内确认条（不是 window.confirm）',
      confirming.hasKickConfirm,
      JSON.stringify({ hasConfirm: confirming.hasKickConfirm }),
    )
    await cdp.evaluate("document.querySelector('[data-testid=kickall-confirm]').click(); 'ok'")
    await waitFor(
      async () => (await cdp.evaluate(READ_SETTINGS)).guest,
      { label: '退出所有设备后页面变游客态', timeoutMs: 25000 },
    )
    const afterKick = await cdp.evaluate(READ_SETTINGS)
    evidence.afterKick = afterKick
    record('S8b 确认后页面变游客态（就地引导，不跳转）', afterKick.guest && afterKick.path.startsWith('/me'), afterKick.path)
    record('S8c 本机 access token 已清空', afterKick.token === '', JSON.stringify({ token: afterKick.token }))

    // ---------- S9 刚才那枚 token 也失效了 ----------
    if (tokenBeforeKick) {
      const kicked = await meStatus(tokenBeforeKick)
      record('S9 退出所有设备后，刚才那枚 token 也失效了（401）', kicked === 401, String(kicked))
    } else {
      skipOne('S9 退出所有设备后旧 token 失效', '确认条出现前没读到 token（页面重新引导过）')
    }

    console.log(JSON.stringify(evidence, null, 2))
  } finally {
    cdp.close()
    chrome.close()
    site.close()
  }

  console.log('\n判据：' + passed + '/' + (passed + failed) + ' 通过' + (skipped ? '（' + skipped + ' 条 SKIP）' : ''))
  console.log('判定：' + (failed === 0 ? 'PASS' : 'FAIL'))
  process.exit(failed === 0 ? 0 : 1)
}

main().catch((err) => {
  console.error('[ERROR] ' + (err && err.stack ? err.stack : err))
  process.exit(1)
})
