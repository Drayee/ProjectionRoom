#!/usr/bin/env node
/**
 * verify-account-deletion.mjs —— 票 #73「注销冷静期」的真机验收。
 *
 * # 为什么必须有真后端
 *
 * 本票的判据**全部**是服务端事实，mock 域一条都证不了：
 *
 *   - "对外立刻消失"是五条独立的可见性谓词（公开档案 404、搜索、三份名单、
 *     粉丝红点、作者帖流）+ 两处**单点**查询（关注 / 查关系）的结果。
 *     mock 里没有"谁看得见谁"这回事；
 *   - "作者名变成「已注销用户」"是 SQL 里的 CASE 表达式，前端只是把服务端
 *     给的字符串画出来；
 *   - "到期真删"是后台清扫协程 + 外键 CASCADE 的结果；
 *   - "冷静期内还能登录、能撤销"是会话没被蹬掉的证据。
 *
 * # 判据
 *
 *   D0  账号就绪：两个账号（待注销的 A、看客 B）互关，A 有一篇帖。
 *   D1  设置区有「注销账号」危险区，两道前置条件不满足时按钮不可点。
 *   D2  填密码 + 勾选 → 出现**页内确认条**（不是 window.confirm），再点才发请求。
 *   D3  发起成功后：页内提示出现；`/me` 顶部出现撤销横幅，且写明还有 N 天。
 *   D4  对外立刻消失（B 的视角，全部走真 HTTP）：
 *         a) `GET /api/users/:id` → 404；
 *         b) `GET /api/users/search?q=` → 搜不到；
 *         c) `GET /api/follows?tab=following` → 名单里没有；
 *         d) `GET /api/follows/:id` → 404（单点查询也守）；
 *         e) `POST /api/follows` → 404（不能再长出新的边）；
 *         f) `POST /api/messages` → 404（可读不可回）。
 *   D5  帖还在、但作者名是「已注销用户」（内容不跟着消失，署名脱敏）。
 *   D6  冷静期内 A 仍然登录着（access token 有效、`/api/auth/me` 200）。
 *   D7  点横幅上的「撤销注销」→ 横幅消失，且 D4 的六条**全部复原**。
 *   D8  撤销之后能重新关注、能重新私聊（证明 D4 的门是跟着那一列走的）。
 *   D9  源码级：注销的三个端点在 `api/auth.ts` 里走 `requestURL`，
 *       没有任何文件把它们拼进 `AUTH_BASE`。
 *
 * # 到期真删（可选，`--purge`）
 *
 * 需要实例把 `PR_DELETION_GRACE` 压到很短（如 `2m`）且
 * `PR_DELETION_SWEEP_INTERVAL=1s`。脚本等到到期后再断言：
 *   P1  账号行真的没了（`/api/users/:id` 404 且**重新注册同名账号能成功**——
 *       后者才证明行被删了，而不只是被标记）；
 *   P2  它的帖子没了（外键 CASCADE）；
 *   P3  关注边没了。
 *
 * 不给 `--purge` 时这三条 SKIP（默认冷静期 30 天，等不起）。
 *
 * # 实例要求（不满足时明确 SKIP、退出码 2，绝不假绿）
 *
 *   - `PR_OTP_DEV_ECHO=1`（要注册账号）；
 *   - 注册/登录限速放宽（一轮建两个号 + 多次登录）；
 *   - 前端产物必须是**真实接口**构建（见用法）。
 *
 * 用法：
 *   cd client && VITE_MOCK_USERS=0 VITE_MOCK_POSTS=0 VITE_MOCK_FOLLOWS=0 npx vite build
 *   node test/script/verify-account-deletion.mjs --server http://127.0.0.1:8899
 *   node test/script/verify-account-deletion.mjs --server http://127.0.0.1:8899 --purge --grace 120
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
const BASE_PORT = Number(argOf(process.argv, 'port', '9933'))
const SERVER = argOf(process.argv, 'server', 'http://127.0.0.1:8888').replace(/\/+$/, '')
/** 是否判"到期真删"。需要实例把冷静期压短（默认 30 天，等不起）。 */
const CHECK_PURGE = process.argv.includes('--purge')
/** 实例上的冷静期秒数（判 P1–P3 时要等过它）。 */
const GRACE_SECONDS = Number(argOf(process.argv, 'grace', '120'))

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
 * Node 的 fetch 不接受一个普通 `IncomingMessage` 当 body，传进去会静默变成
 * "没有请求体"——症状是页面上一切写操作都回 502，而只读页面看起来正常。
 * （verify-email-change 踩过这个坑，这里照抄修好的版本。）
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
  if (!reg.ok) skipEnv('注册失败（HTTP ' + reg.status + '）：' + (await reg.text()))
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
  }
}

/** 带凭据发一个请求，返回 `{status, body}`（body 解析失败时给空对象）。 */
async function api(token, method, path, payload) {
  const headers = { Authorization: 'Bearer ' + token }
  const init = { method, headers }
  if (payload !== undefined) {
    headers['Content-Type'] = 'application/json'
    headers['Idempotency-Key'] = crypto.randomUUID()
    init.body = JSON.stringify(payload)
  }
  const r = await fetch(SERVER + path, init)
  const text = await r.text()
  let body = {}
  try {
    body = text ? JSON.parse(text) : {}
  } catch {
    body = { raw: text }
  }
  return { status: r.status, body }
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
  const btn = (id) => document.querySelector('[data-testid=' + id + ']');
  const start = btn('deletion-start');
  const confirm = btn('deletion-confirm');
  const ack = btn('deletion-ack');
  const pwd = btn('deletion-password');
  return {
    path: location.pathname + location.search,
    heads: [...document.querySelectorAll('.sect h2')].map(txt),
    guest: Boolean(document.querySelector('[data-testid=async-state]')),
    hasStart: Boolean(start),
    startDisabled: start ? Boolean(start.disabled) : null,
    hasConfirm: Boolean(confirm),
    confirmDialog: confirm ? Boolean(confirm.closest('[role=alertdialog]')) : false,
    hasAck: Boolean(ack),
    ackChecked: ack ? Boolean(ack.checked) : null,
    hasPassword: Boolean(pwd),
    hint: txt(btn('deletion-hint')),
    pendingNote: txt(btn('deletion-pending-note')),
    notice: txt(btn('deletion-request-notice')),
    error: txt(btn('deletion-request-error')),
    banner: (() => {
      const el = document.querySelector('.banner');
      return el ? txt(el) : '';
    })(),
    bannerHasCancel: Boolean(btn('deletion-cancel')),
    token: sessionStorage.getItem('pr:access') || '',
  };
})()`

async function openSettings(cdp, site) {
  await cdp.navigate(site.url + '/me?tab=settings')
  await waitFor(
    async () => {
      const s = await cdp.evaluate(READ_SETTINGS)
      return s.hasStart || s.guest || s.pendingNote !== ''
    },
    { label: '设置区渲染出注销危险区', timeoutMs: 25000 },
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
  await sleep(120)
}

async function clickTestId(cdp, id) {
  const r = await cdp.evaluate(
    "(() => { const el = document.querySelector('[data-testid=" + JSON.stringify(id) + "]'); if (!el) return 'missing'; el.click(); return 'ok'; })()",
  )
  await sleep(120)
  return r
}

/** 勾选复选框（v-model 认 change 事件）。 */
async function checkTestId(cdp, id, checked) {
  await cdp.evaluate(`(() => {
    const el = document.querySelector('[data-testid=${id}]');
    if (!el) return 'missing';
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'checked').set;
    setter.call(el, ${checked});
    el.dispatchEvent(new Event('change', { bubbles: true }));
    el.dispatchEvent(new Event('click', { bubbles: true }));
    return 'ok';
  })()`)
  await sleep(120)
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

/**
 * 判"B 看不见 A"的六条，一次跑完并返回逐条结果。
 *
 * 为什么打包成一个函数：D4（注销后全灭）与 D7（撤销后全恢复）判的是
 * **同一组**判据，只是期望值相反。写成两份的话，"撤销后恢复了五条、
 * 漏了一条"这种退化只会在其中一份里被发现。
 */
async function visibilityOf(viewerToken, targetID, username) {
  const out = {}
  const profile = await api(viewerToken, 'GET', '/api/users/' + targetID)
  out.profile = profile.status
  const search = await api(viewerToken, 'GET', '/api/users/search?q=' + encodeURIComponent(username))
  const hits = Array.isArray(search.body.items) ? search.body.items : []
  // 搜索结果的键是 `userId`（不是 `id`）：那是"这个人"的标识，
  // 而 `searchHitView` 里没有 `id` 字段 —— 写成 `u.id` 会恒为 undefined，
  // 于是"搜得到"永远判假、这条判据永远不会红（假绿）。
  out.searchHit = hits.some((u) => Number(u.userId ?? u.id) === Number(targetID))
  out.searchStatus = search.status
  const list = await api(viewerToken, 'GET', '/api/follows?tab=following')
  const rows = Array.isArray(list.body.items) ? list.body.items : []
  out.listHit = rows.some((u) => Number(u.userId ?? u.id) === Number(targetID))
  out.listStatus = list.status
  const rel = await api(viewerToken, 'GET', '/api/follows/' + targetID)
  out.relation = rel.status
  const follow = await api(viewerToken, 'POST', '/api/follows', { userId: targetID })
  out.follow = follow.status
  const dm = await api(viewerToken, 'POST', '/api/messages', { recipientId: targetID, body: '你好' })
  out.message = dm.status
  return out
}

/** 六条里"看不见"的形状：档案/关系/关注/私聊全 404，搜索与名单都没有。 */
function hiddenEverything(v) {
  return (
    v.profile === 404 &&
    v.searchHit === false &&
    v.listHit === false &&
    v.relation === 404 &&
    v.follow === 404 &&
    v.message === 404
  )
}

/**
 * 六条里"看得见"的形状（撤销后必须全恢复）。
 *
 * 关注是 **200 或 201**：端点幂等，新建边回 201、边已存在回 200。
 * 只认其中一个会让"本来就已经关注了"这种正常状态被误判成失败。
 */
function visibleAgain(v) {
  return (
    v.profile === 200 &&
    v.searchHit === true &&
    v.listHit === true &&
    v.relation === 200 &&
    (v.follow === 200 || v.follow === 201) &&
    v.message === 201
  )
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const password = 'Del73-pass-1'
  const victim = await registerAccount('del73a', password)
  const viewer = await registerAccount('del73b', password)
  const victimID = victim.user.id
  const viewerID = viewer.user.id

  // ---------- D0 账号就绪 ----------
  // 互关（私聊门闩是互关）+ 受害者发一篇帖（判 D5 的署名脱敏）。
  const f1 = await api(victim.accessToken, 'POST', '/api/follows', { userId: viewerID })
  const f2 = await api(viewer.accessToken, 'POST', '/api/follows', { userId: victimID })
  const post = await api(victim.accessToken, 'POST', '/api/posts', {
    title: '注销前的帖 ' + victim.username,
    content: '正文内容',
  })
  const before = await visibilityOf(viewer.accessToken, victimID, victim.username)
  evidence.before = { followA: f1.status, followB: f2.status, post: post.status, visibility: before }
  record(
    'D0 账号就绪：两账号互关、受害者有帖，且此刻对方**看得见**',
    (f1.status === 200 || f1.status === 201) &&
      (f2.status === 200 || f2.status === 201) &&
      post.status === 201 &&
      before.profile === 200 &&
      before.searchHit === true &&
      before.listHit === true,
    JSON.stringify(evidence.before),
  )
  const postID = post.body.id

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'account-deletion')
  const cdp = await openTarget(BASE_PORT, 'account-deletion')

  try {
    await seedSession(cdp, site, victim)
    const first = await openSettings(cdp, site)
    evidence.settings = first

    // ---------- D1 危险区存在且门是关的 ----------
    record(
      'D1a 设置区有「注销账号」危险区（密码框 + 勾选框 + 按钮）',
      first.hasPassword && first.hasAck && first.hasStart && first.heads.includes('注销账号'),
      JSON.stringify({ heads: first.heads, hasPassword: first.hasPassword, hasAck: first.hasAck }),
    )
    record(
      'D1b 两道前置条件都不满足时按钮**不可点**（密码空 + 未勾选）',
      first.startDisabled === true && first.ackChecked === false && first.hint !== '',
      JSON.stringify({ startDisabled: first.startDisabled, hint: first.hint }),
    )

    // ---------- D2 页内确认条 ----------
    await fillInput(cdp, '[data-testid=deletion-password]', password)
    await checkTestId(cdp, 'deletion-ack', true)
    await sleep(250)
    const ready = await cdp.evaluate(READ_SETTINGS)
    record(
      'D2a 密码 + 勾选齐了之后按钮可点',
      ready.startDisabled === false,
      JSON.stringify({ startDisabled: ready.startDisabled }),
    )

    await clickTestId(cdp, 'deletion-start')
    await waitFor(
      async () => (await cdp.evaluate(READ_SETTINGS)).hasConfirm,
      { label: '点按钮后展开页内确认条', timeoutMs: 10000 },
    )
    const confirming = await cdp.evaluate(READ_SETTINGS)
    evidence.confirming = confirming
    record(
      'D2b 点「注销账号」先展开**页内**确认条（role=alertdialog，不是系统弹窗）',
      confirming.hasConfirm && confirming.confirmDialog,
      JSON.stringify({ hasConfirm: confirming.hasConfirm, dialog: confirming.confirmDialog }),
    )

    // ---------- D3 发起成功 + 横幅 ----------
    await clickTestId(cdp, 'deletion-confirm')
    await waitFor(
      async () => {
        const s = await cdp.evaluate(READ_SETTINGS)
        return s.notice !== '' || s.error !== '' || s.banner !== ''
      },
      { label: '发起注销后的提示或横幅', timeoutMs: 25000 },
    )
    const submitted = await cdp.evaluate(READ_SETTINGS)
    evidence.submitted = submitted
    record(
      'D3a 发起成功：页内提示出现、无报错',
      submitted.notice.includes('已提交注销申请') && submitted.error === '',
      JSON.stringify({ notice: submitted.notice, error: submitted.error }),
    )
    record(
      'D3b `/me` 顶部出现撤销横幅，且写明还有几天',
      submitted.banner !== '' &&
        submitted.bannerHasCancel &&
        /还有\s*\d+\s*天/.test(submitted.banner),
      JSON.stringify({ banner: submitted.banner }),
    )

    // ---------- D4 对外立刻消失 ----------
    const hidden = await visibilityOf(viewer.accessToken, victimID, victim.username)
    evidence.hidden = hidden
    record(
      'D4 对外立刻消失（档案 404 / 搜不到 / 名单无 / 关系 404 / 不能关注 / 不能私聊）',
      hiddenEverything(hidden),
      JSON.stringify(hidden),
    )

    // ---------- D5 帖还在、署名脱敏 ----------
    const postAfter = await api(viewer.accessToken, 'GET', '/api/posts/' + postID)
    const authorName = postAfter.body.author?.displayName ?? ''
    evidence.postAfter = { status: postAfter.status, authorName }
    record(
      'D5 帖仍可读，但作者名是「已注销用户」（内容不跟着消失，署名脱敏）',
      postAfter.status === 200 && authorName === '已注销用户',
      JSON.stringify(evidence.postAfter),
    )

    // ---------- D6 冷静期内本人仍登录 ----------
    const meNow = await api(victim.accessToken, 'GET', '/api/auth/me')
    evidence.meNow = {
      status: meNow.status,
      deletionRequestedAt: meNow.body.user?.deletionRequestedAt,
    }
    record(
      'D6 冷静期内本人仍登录着（access 有效、/me 回 deletionRequestedAt）',
      meNow.status === 200 && Boolean(meNow.body.user?.deletionRequestedAt),
      JSON.stringify(evidence.meNow),
    )

    // ---------- D7 撤销 ----------
    await cdp.navigate(site.url + '/me')
    await waitFor(
      async () => (await cdp.evaluate(READ_SETTINGS)).bannerHasCancel,
      { label: '回到 /me 看到撤销横幅', timeoutMs: 20000 },
    )
    await clickTestId(cdp, 'deletion-cancel')
    await waitFor(
      async () => {
        const s = await cdp.evaluate(READ_SETTINGS)
        return s.banner === '' || s.error !== ''
      },
      { label: '撤销后横幅消失', timeoutMs: 20000 },
    )
    const cancelled = await cdp.evaluate(READ_SETTINGS)
    evidence.cancelled = { banner: cancelled.banner, error: cancelled.error }
    record(
      'D7a 点「撤销注销」→ 横幅消失',
      cancelled.banner === '' && cancelled.error === '',
      JSON.stringify(evidence.cancelled),
    )

    const restored = await visibilityOf(viewer.accessToken, victimID, victim.username)
    evidence.restored = restored
    record(
      'D7b 撤销后六条**全部复原**（档案 200 / 搜得到 / 名单有 / 关系 200 / 能关注 / 能私聊）',
      visibleAgain(restored),
      JSON.stringify(restored),
    )

    // ---------- D8 撤销后关系端点真的写回去了 ----------
    // D7b 里的 follow 是一次 POST，它建了一条边；再取关再关注一遍，
    // 证明"能关注"不只是回了 200 而是真的能改状态。
    await api(viewer.accessToken, 'DELETE', '/api/follows/' + victimID)
    const refollow = await api(viewer.accessToken, 'POST', '/api/follows', { userId: victimID })
    const relAfter = await api(viewer.accessToken, 'GET', '/api/follows/' + victimID)
    evidence.refollow = { status: refollow.status, relation: relAfter.status }
    record(
      'D8 撤销后能取关再关注（关系真的可写，不是只回一个状态码）',
      (refollow.status === 200 || refollow.status === 201) && relAfter.status === 200,
      JSON.stringify(evidence.refollow),
    )

    // ---------- D9 源码级 ----------
    const files = walkSources(join(REPO_ROOT, 'client', 'src'), [])
    const offenders = []
    for (const f of files) {
      const src = stripComments(readFileSync(f, 'utf8'))
      const rel = f.slice(REPO_ROOT.length + 1)
      // 注销端点的真源只该出现在 api/auth.ts（它用 requestURL 收完整路径）。
      if (/\/api\/me\/deletion/.test(src) && !f.endsWith(join('api', 'auth.ts'))) {
        offenders.push(rel)
      }
      // 反向：谁把注销塞进 AUTH_BASE 拼串（`me/deletion` 这种）就是放错了前缀。
      if (/['"`]me\/deletion['"`]/.test(src)) {
        offenders.push(rel + '（拼进了 AUTH_BASE）')
      }
    }
    record(
      'D9 源码级：注销端点走 /api/me/deletion，不在 /api/auth 前缀下',
      offenders.length === 0,
      JSON.stringify({ offenders }),
    )

    // ---------- P1–P3 到期真删（可选） ----------
    if (!CHECK_PURGE) {
      skipOne(
        'P1–P3 到期真删',
        '未给 --purge（默认冷静期 30 天等不起）；要判它请把实例的 PR_DELETION_GRACE 压到分钟级',
      )
    } else {
      // 再发起一次注销，然后等过冷静期 + 一轮清扫。
      const again = await api(victim.accessToken, 'POST', '/api/me/deletion', {
        currentPassword: password,
      })
      if (again.status !== 200) {
        skipOne('P1–P3 到期真删', '第二次发起注销失败（HTTP ' + again.status + '）')
      } else {
        console.log('[INFO] 等待冷静期 ' + GRACE_SECONDS + 's + 清扫轮次…')
        await sleep((GRACE_SECONDS + 8) * 1000)

        // P1：账号行真的没了。**重新注册同名账号能成功**才是"行被删了"的证据 ——
        // 只看 404 的话，"标记着但没删"与"删了"长得一模一样。
        const reuse = await fetch(SERVER + '/api/auth/register/start', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() },
          body: JSON.stringify({ username: victim.username, password }),
        })
        evidence.purged = { reuseStatus: reuse.status }
        record(
          'P1 到期后账号行真的被删（同名可重新注册）',
          reuse.status === 200 || reuse.status === 201,
          JSON.stringify({ reuseStatus: reuse.status }),
        )

        // P2：帖没了（外键 CASCADE）。用**看客**的 token 读，避免被"帖子作者不可见"干扰。
        const postGone = await api(viewer.accessToken, 'GET', '/api/posts/' + postID)
        record('P2 到期后它的帖子一并消失（外键 CASCADE）', postGone.status === 404, 'HTTP ' + postGone.status)

        // P3：关注边没了（用 DB 之外能观测到的形状：看客的关注名单里没有它）。
        const listAfter = await api(viewer.accessToken, 'GET', '/api/follows?tab=following')
        const rows = Array.isArray(listAfter.body.items) ? listAfter.body.items : []
        const stillThere = rows.some((u) => Number(u.userId ?? u.id) === Number(victimID))
        record('P3 到期后关注边一并消失', listAfter.status === 200 && stillThere === false, JSON.stringify({ stillThere }))
      }
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
