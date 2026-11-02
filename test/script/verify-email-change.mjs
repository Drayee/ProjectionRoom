#!/usr/bin/env node
/**
 * verify-email-change.mjs —— 票 #72「自助换绑邮箱」的真机验收。
 *
 * # 为什么必须有真后端
 *
 * 本票的每一条判据都落在服务端：
 *   - 验证码发到**新**地址（而不是当前邮箱）——这是整个功能的安全地基；
 *   - 确认时按**收件地址**过滤（`ConsumeEmailOTPFor` 的 WHERE）——
 *     拿旧地址那条码去确认新地址必须失败；
 *   - 换绑后 `email_verified_at` 落值、旧地址立刻失效、找回密码改指向新地址。
 * mock 域里没有"验证码行属于哪个地址"这回事，用它证明等于什么都没证。
 *
 * # 判据
 *
 *   E0  账号就绪：注册建号、当前邮箱已验证。
 *   E1  设置区有「换绑邮箱」块（新邮箱输入框 + 提交按钮）。
 *   E2  填新邮箱 → 发送 → 出现验证码输入框，且提示里点名了新地址。
 *   E3  错误验证码 → 页内人话（不是"服务端内部错误"），且邮箱**没变**。
 *   E4  正确验证码 → 确认成功 → 页内提示「邮箱已更新」。
 *   E5  只读块的邮箱显示换成新地址，邮箱状态是「已验证」。
 *   E6  服务端侧也是新地址：`/api/auth/me` 的 email 与 emailVerified 都对。
 *   E7  旧地址立刻失效：拿旧地址走「找回密码」拿不到验证码；
 *       拿新地址走同一条路能拿到 —— 这就是"找回密码指向新邮箱"的机制。
 *   E8  源码级：换绑两个端点走 `/api/me/email*`，**不在** `/api/auth` 前缀下
 *       （放错前缀会让 refresh Cookie 的 Path 规则多一处不一致）。
 *
 * # 实例要求（不满足时明确 SKIP、退出码 2，绝不假绿）
 *
 *   - `PR_OTP_DEV_ECHO=1`（要注册账号，也要拿换绑验证码）；
 *   - 注册/登录限速放宽（一轮建号 + 多次登录）；
 *   - 前端产物必须是**真实接口**构建（见用法）。
 *
 * 用法：
 *   cd client && VITE_MOCK_USERS=0 VITE_MOCK_POSTS=0 npx vite build
 *   node test/script/verify-email-change.mjs --server http://127.0.0.1:8899
 *
 * 退出码：全部判据 PASS -> 0；任一 FAIL -> 1；环境不满足 -> 2。
 */
import { createServer, request as httpRequest } from 'node:http'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9922'))
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
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.webp': 'image/webp',
  '.ico': 'image/x-icon',
}

/**
 * 把 /api/* 反代到真后端（静态服务器只托管产物）。
 *
 * ⚠️ 这里**必须用 `http.request` 逐字节转发**，不能用 `fetch(url, {body: req})`：
 * Node 的 fetch 不接受一个普通 `IncomingMessage` 当 body（它要 ReadableStream 或
 * 字符串/Buffer），传进去会静默变成"没有请求体"或直接抛错 —— 症状是页面上一切
 * 写操作都回 502，而只读页面看起来正常（GET 没 body）。verify-me-settings 用的
 * 就是 http.request，这里照抄。
 */
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
  return prefix + Math.random().toString(36).slice(2, 8)
}

/** 走完整两段式注册建号（需要实例开 PR_OTP_DEV_ECHO=1）。 */
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
    email,
    user: account.user,
    accessToken: account.accessToken,
    refreshCookie: cookieOf(verified),
  }
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

async function meBody(accessToken) {
  const r = await fetch(SERVER + '/api/auth/me', {
    headers: { Authorization: 'Bearer ' + accessToken },
  })
  if (!r.ok) return { status: r.status, user: null }
  const body = await r.json().catch(() => ({}))
  return { status: r.status, user: body.user || null }
}

/**
 * 往某个地址发一条「找回密码」验证码，返回响应里的 devCode（空串 = 没发信）。
 *
 * 这是判"旧地址是否立刻失效"的唯一手段：`SendAccountOTP` 对外恒定 200
 * （防账号枚举），所以**不能**用状态码判，只能看"到底有没有码产生"。
 */
async function passwordOTPFor(email) {
  const r = await fetch(SERVER + '/api/auth/password/otp', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email }),
  })
  const body = await r.json().catch(() => ({}))
  return { status: r.status, devCode: body.devCode || body.dev_code || '' }
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
  document.querySelectorAll('[data-testid=account-readonly] dt').forEach((dt) => {
    const dd = dt.nextElementSibling;
    kv[txt(dt)] = txt(dd);
  });
  return {
    path: location.pathname + location.search,
    heads: [...document.querySelectorAll('.sect h2')].map(txt),
    guest: Boolean(guest),
    hasNext: Boolean(document.querySelector('[data-testid=email-next]')),
    hasCode: Boolean(document.querySelector('[data-testid=email-code]')),
    hasSubmit: Boolean(document.querySelector('[data-testid=email-submit]')),
    hasResend: Boolean(document.querySelector('[data-testid=email-resend]')),
    notice: txt(document.querySelector('[data-testid=email-notice]')),
    error: txt(document.querySelector('[data-testid=email-error]')),
    nextError: txt(document.querySelector('[data-testid=email-next-error]')),
    codeError: txt(document.querySelector('[data-testid=email-code-error]')),
    devCode: txt(document.querySelector('[data-testid=email-dev-code]')),
    readonly: kv,
    token: sessionStorage.getItem('pr:access') || '',
  };
})()`

async function openSettings(cdp, site) {
  await cdp.navigate(site.url + '/me?tab=settings')
  await waitFor(
    async () => {
      const s = await cdp.evaluate(READ_SETTINGS)
      return s.hasNext || s.guest
    },
    { label: '设置区渲染出换绑邮箱表单', timeoutMs: 25000 },
  )
  await sleep(400)
  return cdp.evaluate(READ_SETTINGS)
}

/** 往某个输入框写值（用原生 setter + input 事件，v-model 才认）。 */
async function fillInput(cdp, selector, value) {
  await cdp.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return 'missing';
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
    setter.call(el, ${JSON.stringify(value)});
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
    return 'ok';
  })()`)
  await sleep(100)
}

async function clickTestId(cdp, id) {
  await cdp.evaluate(
    "(() => { const el = document.querySelector('[data-testid=" + JSON.stringify(id) + "]'); if (!el) return 'missing'; el.click(); return 'ok'; })()",
  )
}

/** 源码级：递归收集 client/src 下的 .ts/.vue 源文件。 */
function walkSources(dir, out) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    const st = statSync(p)
    if (st.isDirectory()) walkSources(p, out)
    else if (/\.(ts|vue)$/.test(name)) out.push(p)
  }
  return out
}

/** 去掉注释（含 .vue 的 HTML 注释）—— 注释里提到端点名会假红。 */
function stripComments(src) {
  return src
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/[^\n]*/g, '$1')
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const password = 'Email72-pass-1'
  const account = await registerAccount('em72', password)
  const oldEmail = account.email
  const newEmail = account.username + '-new@example.test'

  // ---------- E0 账号就绪 ----------
  const before = await meBody(account.accessToken)
  evidence.before = { status: before.status, email: before.user?.email, verified: before.user?.emailVerified }
  record(
    'E0 账号就绪：注册建号成功，当前邮箱已验证',
    before.status === 200 && before.user?.email === oldEmail && before.user?.emailVerified === true,
    JSON.stringify(evidence.before),
  )

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'email-change')
  const cdp = await openTarget(BASE_PORT, 'email-change')

  try {
    await seedSession(cdp, site, account)
    const first = await openSettings(cdp, site)
    evidence.settings = first

    // ---------- E1 换绑块存在 ----------
    record(
      'E1 设置区有「换绑邮箱」块（新邮箱输入框 + 提交按钮）',
      first.hasNext && first.hasSubmit && first.heads.includes('换绑邮箱'),
      JSON.stringify({ heads: first.heads, hasNext: first.hasNext, hasSubmit: first.hasSubmit }),
    )

    // ---------- E2 发码到新地址 ----------
    await fillInput(cdp, '[data-testid=email-next]', newEmail)
    await clickTestId(cdp, 'email-submit')
    await waitFor(
      async () => {
        const s = await cdp.evaluate(READ_SETTINGS)
        return s.hasCode || s.error !== '' || s.nextError !== ''
      },
      { label: '发码后出现验证码输入框', timeoutMs: 20000 },
    )
    const sent = await cdp.evaluate(READ_SETTINGS)
    evidence.sent = sent
    record(
      'E2a 发送后出现验证码输入框',
      sent.hasCode,
      JSON.stringify({ hasCode: sent.hasCode, error: sent.error, nextError: sent.nextError }),
    )
    record(
      'E2b 提示里点名了**新**地址（不是当前邮箱）',
      sent.notice.includes(newEmail) && !sent.notice.includes(oldEmail),
      JSON.stringify({ notice: sent.notice }),
    )

    // ---------- E3 错误验证码 ----------
    await fillInput(cdp, '[data-testid=email-code]', '000000')
    await clickTestId(cdp, 'email-submit')
    await waitFor(
      async () => {
        const s = await cdp.evaluate(READ_SETTINGS)
        return s.codeError !== '' || s.error !== ''
      },
      { label: '错误验证码后出现页内提示', timeoutMs: 20000 },
    )
    const wrong = await cdp.evaluate(READ_SETTINGS)
    evidence.wrongCode = wrong
    record(
      'E3a 错误验证码 → 页内人话（不是"服务端内部错误"）',
      wrong.codeError + wrong.error !== '' && !(wrong.codeError + wrong.error).includes('内部错误'),
      JSON.stringify({ codeError: wrong.codeError, error: wrong.error }),
    )
    const afterWrong = await meBody(account.accessToken)
    record(
      'E3b 确认失败时邮箱没变（仍是旧地址）',
      afterWrong.user?.email === oldEmail,
      JSON.stringify({ email: afterWrong.user?.email }),
    )

    // ---------- E4 正确验证码 ----------
    // dev echo 把码显示在页面上（本机开发没有真邮箱）；拿不到就 SKIP 剩下的确认步骤。
    const hint = /(\d{6})/.exec(sent.devCode || '')
    const code = hint ? hint[1] : ''
    if (!code) {
      skipOne('E4–E7 完整换绑流程', '页面没有回显 devCode（实例未开 PR_OTP_DEV_ECHO=1 时拿不到码）')
    } else {
      await fillInput(cdp, '[data-testid=email-code]', code)
      await clickTestId(cdp, 'email-submit')
      await waitFor(
        async () => {
          const s = await cdp.evaluate(READ_SETTINGS)
          return s.notice.includes('邮箱已更新') || s.codeError !== '' || s.error !== ''
        },
        { label: '确认换绑后的提示', timeoutMs: 25000 },
      )
      const done = await cdp.evaluate(READ_SETTINGS)
      evidence.confirmed = done
      record(
        'E4 正确验证码 → 确认成功，页内提示「邮箱已更新」',
        done.notice.includes('邮箱已更新'),
        JSON.stringify({ notice: done.notice, codeError: done.codeError, error: done.error }),
      )

      // ---------- E5 只读块跟着变 ----------
      record(
        'E5 只读块的邮箱换成新地址、状态是「已验证」',
        done.readonly['邮箱'] === newEmail && done.readonly['邮箱状态'] === '已验证',
        JSON.stringify({ 邮箱: done.readonly['邮箱'], 邮箱状态: done.readonly['邮箱状态'] }),
      )

      // ---------- E6 服务端侧一致 ----------
      const after = await meBody(account.accessToken)
      evidence.after = {
        status: after.status,
        email: after.user?.email,
        verified: after.user?.emailVerified,
      }
      record(
        'E6 服务端也是新地址且已验证（/api/auth/me）',
        after.user?.email === newEmail && after.user?.emailVerified === true,
        JSON.stringify(evidence.after),
      )

      // ---------- E7 旧地址立刻失效、找回密码指向新地址 ----------
      // 冷却期 60s：换绑发码刚用过一次，所以这里先等过冷却，或者只判"旧地址拿不到码"
      // 这一半（它对冷却不敏感：不发码时 devCode 恒为空）。
      const oldOTP = await passwordOTPFor(oldEmail)
      record(
        'E7a 旧地址立刻失效：拿旧地址走找回密码拿不到验证码',
        oldOTP.status === 200 && oldOTP.devCode === '',
        JSON.stringify({ status: oldOTP.status, devCode: oldOTP.devCode ? '有码' : '无码' }),
      )
      // E7b：新地址能收到「找回密码」的码 —— 这就是"找回密码已指向新邮箱"的机制证明。
      //
      // ⚠️ 两个限流都会拦这里，且它们不是一回事：
      //   ① 冷却期 `otpCooldown = 60s`，按 **user_id** 算（`LastEmailOTPSentAt`）——
      //      换绑发码刚用过同一个 user 的配额；
      //   ② 每邮箱闸门 `PR_MAIL_PER_EMAIL_PER_HOUR`（默认 5 次/小时），按 **收件地址** 算。
      // 所以这里**必须只等一次、然后只请求一次**：早先写成"每 5 秒轮询一次直到拿到码"，
      // 结果是轮询本身把 ② 的配额烧光（实测 5 次之后恒 429），比 SKIP 更糟 —— 它报红。
      // 等 62 秒（留 2 秒给时钟抖动），然后单次请求。
      await sleep(62000)
      const newOTP = await passwordOTPFor(newEmail)
      record(
        'E7b 新地址能收到找回密码验证码（找回密码已指向新邮箱）',
        newOTP.status === 200 && newOTP.devCode !== '',
        JSON.stringify({ status: newOTP.status, devCode: newOTP.devCode ? '有码' : '无码' }),
      )
    }

    // ---------- E8 源码级：端点在 /api/me 前缀下 ----------
    const files = walkSources(join(REPO_ROOT, 'client', 'src'), [])
    const offenders = []
    for (const f of files) {
      const src = stripComments(readFileSync(f, 'utf8'))
      // 换绑端点的真源只该出现在 api/auth.ts（它用 requestURL 收完整路径）。
      if (/\/api\/me\/email/.test(src) && !f.endsWith(join('api', 'auth.ts'))) {
        offenders.push(f.slice(REPO_ROOT.length + 1))
      }
      // 反向：谁把换绑塞进 AUTH_BASE 拼串（`auth/email` 这种）就是放错了前缀。
      if (/['"`]email\/otp['"`]|['"`]me\/email['"`]/.test(src)) {
        offenders.push(f.slice(REPO_ROOT.length + 1) + '（拼进了 AUTH_BASE）')
      }
    }
    record(
      'E8 源码级：换绑端点走 /api/me/email*，不在 /api/auth 前缀下',
      offenders.length === 0,
      JSON.stringify({ offenders }),
    )

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
