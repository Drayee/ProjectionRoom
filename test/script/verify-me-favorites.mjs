#!/usr/bin/env node
/**
 * `verify-me-favorites.mjs` —— `/me?tab=favorites` 收藏区块（票 #65）的**真机**验收。
 *
 * # 为什么需要真后端
 *
 * 收藏的判据里有一半是**失效态**：对象没了、条目还在。这要求对象**真的**消失
 * （放映室被取消公开 / 帖子被删），而不是前端假装一下。mock 域里 `post:9999`
 * 那条失效种子能验排版，但验不了"真对象失效后服务端怎么说" —— 那才是
 * `favoriteEntryView.valid` 的语义所在。
 *
 * # 它在什么环境里跑
 *
 * 需要真后端（`--server http://127.0.0.1:PORT`），且实例必须：
 *   - `PR_OTP_DEV_ECHO=1`（脚本要建账号，走两段式注册 + 邮箱验证码）
 *   - 放宽注册/登录限速（`PR_AUTH_REGISTER_PER_MINUTE` / `PR_AUTH_REGISTER_BURST`）
 * 构建必须 `cd client && VITE_MOCK_POSTS=0 VITE_MOCK_FAVORITES=0 npx vite build`
 * （**读的是 `client/dist`**，构建到别的目录会静默跑 mock 域）。
 *
 * # 前置
 *
 *   cd client && VITE_MOCK_POSTS=0 VITE_MOCK_FAVORITES=0 VITE_MOCK_USERS=0 npx vite build
 *   node test/script/verify-me-favorites.mjs --server http://127.0.0.1:8899
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1；环境不满足 → 2。
 *
 * # 判据（对应工单验收四条）
 *
 *  G0  账号 + 两篇帖 + 一间公开放映室就绪。
 *  G1  **`/me?tab=favorites` 列出收藏的帖子**：行是链接、标题非空。
 *  G2  **`?type=room` 切到放映室**：URL 生效、行是那间房。
 *  G3  **倒序**：先收藏的在后 —— 新收藏的排在最上面（服务端 `created_at DESC`）。
 *  G4  **空态是一句人话**：番剧 tab 没有收藏时给「还没有收藏番剧」+ 引导，
 *      **不是**裸「暂无数据」（决议 #44 禁止的形态）。
 *  G5  **失效态保留**（验收 ①）：把放映室改成不公开后，`?type=room` 里条目**仍在**、
 *      灰显（`.row.invalid`）、标题不是链接、带「已失效」说明、旁边有「移除」。
 *  G6  **可手动移除**（验收 ①）：点「移除」后行消失并给提示；刷新后仍然不在。
 *  G7  **仅自己可见**（验收 ③）：`/user/:id`（本人与他人）上都**没有**收藏块。
 *  G8  **分页走票 09 端点**（验收 ②）：`GET /api/favorites?type=post&limit=2` 回
 *      `hasMore=true` 且带 `cursor`；拿它当 `before` 取第二页，与第一页不重复。
 *  G9  **源码级没有公开计数**：`client/src` 里不得出现 `favoriteCount`。
 */
import { createServer, request as httpRequest } from 'node:http'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const SERVER = (argOf(process.argv, 'server', '') || '').replace(/\/$/, '')
const BASE_PORT = Number(argOf(process.argv, 'port', '9918'))

function argOf(argv, name, fallback) {
  const i = argv.indexOf('--' + name)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}

let passed = 0
let failed = 0

function record(name, pass, detail) {
  if (pass) passed += 1
  else failed += 1
  console.log('[' + (pass ? 'PASS' : 'FAIL') + '] ' + name + (detail ? ' — ' + detail : ''))
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
  return prefix + Date.now().toString(36)
}

/** 走完整注册链路建一个账号（两段式 + 邮箱验证码），与 verify-favorites.mjs 同形。 */
async function registerAccount(prefix) {
  const username = freshUsername(prefix)
  const password = 'Me65-pass-1'
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
  return { username, user: account.user, accessToken: account.accessToken }
}

function authHeaders(account, extra) {
  return { 'Content-Type': 'application/json', Authorization: 'Bearer ' + account.accessToken, ...(extra || {}) }
}

async function createPost(account, title) {
  const r = await fetch(SERVER + '/api/posts', {
    method: 'POST',
    headers: authHeaders(account),
    body: JSON.stringify({ title, content: '收藏区块验收用的帖子正文。' }),
  })
  if (!r.ok) skipEnv('发帖失败（HTTP ' + r.status + '）：实例的帖子域不可用')
  const body = await r.json()
  if (!body.id) skipEnv('发帖响应里没有 id')
  return body.id
}

/** 建一间房并把它设为公开（收藏放映室要求 `is_public ∧ 未关闭`）。 */
async function createPublicRoom(account, title) {
  const r = await fetch(SERVER + '/api/rooms', {
    method: 'POST',
    headers: authHeaders(account),
    body: JSON.stringify({}),
  })
  if (!r.ok) skipEnv('建房失败（HTTP ' + r.status + '）')
  const body = await r.json()
  const roomId = body.roomId
  if (!roomId) skipEnv('建房响应里没有 roomId')

  // 元数据是**异步**落库的（I4）：先轮询到能 PATCH 成功，再写公开标记。
  // 直接 PATCH 会撞上"内存里有房、库里还没这一行"的窗口，读到的期望状态是
  // 「缺行按未公开起步」，于是 title 被写成空串、is_public 仍是 false。
  let patched = false
  for (let i = 0; i < 40 && !patched; i += 1) {
    const p = await fetch(SERVER + '/api/rooms/' + encodeURIComponent(roomId) + '/meta', {
      method: 'PATCH',
      headers: authHeaders(account),
      body: JSON.stringify({ title, isPublic: true }),
    })
    if (p.ok) {
      const body2 = await p.json().catch(() => ({}))
      if (body2.isPublic === true) patched = true
      else await sleep(120)
    } else {
      await sleep(120)
    }
  }
  if (!patched) skipEnv('放映室元数据没能写成公开（PATCH /api/rooms/:id/meta 一直不成功）')
  return roomId
}

async function favorite(account, type, targetId, on) {
  const seg = type === 'post' ? 'posts' : type === 'anime' ? 'animes' : 'rooms'
  const r = await fetch(SERVER + '/api/' + seg + '/' + encodeURIComponent(targetId) + '/favorite', {
    method: on ? 'PUT' : 'DELETE',
    headers: authHeaders(account),
  })
  return { ok: r.ok, status: r.status, body: await r.json().catch(() => ({})) }
}

async function seedSession(cdp, site, account) {
  await cdp.navigate(site.url + '/')
  await cdp.evaluate(
    'sessionStorage.setItem("pr:access", ' + JSON.stringify(account.accessToken) + '); ' +
      'localStorage.setItem("pr:profile", ' + JSON.stringify(JSON.stringify(account.user)) + '); ' +
      '"ok"',
  )
}

/**
 * 读收藏区块。
 *
 * `ready` 用来给 `waitFor` 用：空态/错误态也算"读完了"，否则在没有收藏的
 * 类型 tab 上会一直等到超时（与 verify-profile 的 `notFound` 同一条教训）。
 */
const READ_BLOCK = `(() => {
  const sect = document.querySelector('section.fav')
  if (!sect) return null
  const rows = [...sect.querySelectorAll('ul.rows > li.row')].map((li) => {
    const titleEl = li.querySelector('.row-title')
    const link = titleEl && titleEl.tagName === 'A' ? titleEl.getAttribute('href') : null
    return {
      title: (titleEl ? titleEl.textContent || '' : '').trim(),
      href: link,
      invalid: li.classList.contains('invalid'),
      hint: (li.querySelector('.invalid-hint') ? li.querySelector('.invalid-hint').textContent || '' : '').trim(),
      acts: [...li.querySelectorAll('button')].map((b) => (b.textContent || '').trim()),
    }
  })
  const selected = [...sect.querySelectorAll('nav.types [role=tab]')].find((b) => b.getAttribute('aria-selected') === 'true')
  const state = sect.querySelector('[data-testid=async-state]')
  return {
    types: [...sect.querySelectorAll('nav.types [role=tab]')].map((b) => (b.textContent || '').trim()),
    selected: selected ? (selected.textContent || '').trim() : '',
    rows,
    state: state ? state.getAttribute('data-state') : null,
    stateText: state ? (state.innerText || '').replace(/\\s+/g, ' ').trim() : '',
    notice: (sect.querySelector('.ok') ? sect.querySelector('.ok').textContent || '' : '').trim(),
    search: location.search,
    ready: rows.length > 0 || !!state,
  }
})()`

/**
 * 轮询页面表达式直到它返回真值。
 *
 * `lib/browser.mjs` 的 `waitFor(fn, opts)` 收的是一个**函数**，不是 (cdp, 表达式)；
 * 本脚本的判据全是页面表达式，所以在本地包一层，免得每处都写闭包。
 * 超时**不抛**，返回最后一次的值 —— 判据自己会用 `record` 报红，报错栈会盖掉真正的
 * 失败原因（"等超时"和"值不对"是两件事，混在一起就看不出是哪条判据红的）。
 */
async function waitEval(cdp, expression, timeoutMs = 12000, intervalMs = 150) {
  const deadline = Date.now() + timeoutMs
  let last = null
  while (Date.now() < deadline) {
    last = await cdp.evaluate(expression)
    if (last) return last
    await sleep(intervalMs)
  }
  return last
}

/** 把 READ_BLOCK 这个 IIFE 包成"等它 ready"的表达式。 */
function waitBlock(extra = '') {
  return '(() => { const r = ' + READ_BLOCK + '; return r && r.ready' + (extra ? ' && (' + extra + ')' : '') + ' ? r : null })()'
}

/** 源码级判据的取样：去掉注释再 grep。 */
function stripComments(text) {
  return (
    text
      // .vue 的模板里写的是 HTML 注释，TS 的注释剥离管不到它。
      // 少了这一条，注释里提到被删掉的字面量就会让判据假红（踩过）。
      .replace(/<!--[\s\S]*?-->/g, '')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/(^|[^:])\/\/[^\n]*/g, '$1')
  )
}

function walk(dir, out) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    const st = statSync(full)
    if (st.isDirectory()) walk(full, out)
    else if (/\.(ts|vue|mjs)$/.test(name)) out.push(full)
  }
  return out
}

async function main() {
  if (!SERVER) skipEnv('缺少 --server http://127.0.0.1:PORT（本脚本需要真后端）')
  if (!existsSync(join(DIST, 'index.html'))) {
    skipEnv('找不到 ' + DIST + '/index.html，先 cd client && VITE_MOCK_POSTS=0 VITE_MOCK_FAVORITES=0 npx vite build')
  }

  // 收藏域必须真的在线（不是 404 能力降级）。
  const probe = await fetch(SERVER + '/api/favorites?type=post', { headers: { Authorization: 'Bearer x' } })
  if (probe.status === 404) skipEnv('实例的收藏域未装配（GET /api/favorites 回 404）')

  const account = await registerAccount('me65')
  const other = await registerAccount('me65b')
  const postA = await createPost(account, '收藏区块验收 · 第一帖')
  const postB = await createPost(account, '收藏区块验收 · 第二帖')
  const roomId = await createPublicRoom(account, '收藏区块验收房')

  // 先收第一帖，再收第二帖 —— 让"倒序"有可判的事实（G3）。
  const favA = await favorite(account, 'post', String(postA), true)
  if (!favA.ok) skipEnv('收藏帖子失败（HTTP ' + favA.status + '）')
  await sleep(60)
  const favB = await favorite(account, 'post', String(postB), true)
  if (!favB.ok) skipEnv('收藏第二帖失败（HTTP ' + favB.status + '）')
  const favRoom = await favorite(account, 'room', roomId, true)
  if (!favRoom.ok) skipEnv('收藏放映室失败（HTTP ' + favRoom.status + '）')
  record('G0 账号 + 两篇帖 + 一间公开放映室就绪', true, 'post=' + postA + ',' + postB + ' room=' + roomId)

  const chromePath = findChrome()
  if (!chromePath) skipEnv('找不到 Chrome（脚本用 headless Chrome 跑真机判据）')
  const site = await startStatic(DIST)
  const chrome = await startChrome(chromePath, BASE_PORT, 'me-favorites')
  const cdp = await openTarget(BASE_PORT, 'me-favorites')

  try {
    await seedSession(cdp, site, account)

    // ---------- G1 收藏的帖子出现在 /me?tab=favorites ----------
    await cdp.navigate(site.url + '/me?tab=favorites')
    const posts = await waitEval(cdp, waitBlock())
    record(
      'G1a 收藏分区列出收藏的帖子（行是真链接）',
      posts && posts.rows.length >= 2 && posts.rows.every((r) => typeof r.href === 'string' && r.href.startsWith('/posts/')),
      JSON.stringify(posts && posts.rows),
    )
    record(
      'G1b 收藏分区选中「帖子」tab',
      posts && posts.selected === '帖子' && posts.types.join('/') === '帖子/番剧/放映室',
      JSON.stringify(posts && { selected: posts.selected, types: posts.types }),
    )

    // ---------- G3 倒序 ----------
    const titles = (posts && posts.rows ? posts.rows.map((r) => r.title) : []).join(' | ')
    record(
      'G3 新收藏的排在最上面（created_at DESC）',
      posts && posts.rows.length >= 2 && posts.rows[0].title.includes('第二帖') && titles.includes('第一帖'),
      titles,
    )

    // ---------- G2 切到放映室 ----------
    await cdp.evaluate(
      "(() => { const b = [...document.querySelectorAll('nav.types [role=tab]')].find((x) => (x.textContent||'').trim() === '放映室'); if (b) b.click(); return 'ok' })()",
    )
    const rooms = await waitEval(cdp, waitBlock("location.search.includes('type=room')"))
    record(
      'G2 点「放映室」后 ?type=room 进 URL 且列出那间房',
      rooms && rooms.search.includes('type=room') && rooms.rows.length === 1 && rooms.rows[0].title === '收藏区块验收房',
      JSON.stringify(rooms && { search: rooms.search, rows: rooms.rows }),
    )

    // ---------- G4 空态是一句人话 ----------
    await cdp.navigate(site.url + '/me?tab=favorites&type=anime')
    const animeEmpty = await waitEval(cdp, waitBlock())
    record(
      'G4 番剧 tab 空态是一句人话（不是裸「暂无数据」）',
      animeEmpty &&
        animeEmpty.state === 'empty' &&
        animeEmpty.stateText.includes('还没有收藏番剧') &&
        !animeEmpty.stateText.includes('暂无数据'),
      JSON.stringify(animeEmpty && { state: animeEmpty.state, text: animeEmpty.stateText }),
    )

    // ---------- G5 失效态保留 ----------
    const unpub = await fetch(SERVER + '/api/rooms/' + encodeURIComponent(roomId) + '/meta', {
      method: 'PATCH',
      headers: authHeaders(account),
      body: JSON.stringify({ isPublic: false }),
    })
    if (!unpub.ok) skipEnv('取消放映室公开失败（HTTP ' + unpub.status + '）')

    // 失效是"库里那一行的 join 条件不再满足"，落库是异步的 —— 轮询到它真的失效为止。
    let invalid = null
    for (let i = 0; i < 40; i += 1) {
      await cdp.navigate(site.url + '/me?tab=favorites&type=room')
      const view = await waitEval(cdp, waitBlock())
      const row = view && view.rows[0]
      if (row && row.invalid) {
        invalid = row
        break
      }
      await sleep(200)
    }
    record(
      'G5a 放映室取消公开后条目仍在（不静默删除）',
      !!invalid,
      JSON.stringify(invalid),
    )
    record(
      'G5b 失效条目灰显、不可点、带「已失效」说明',
      !!invalid && invalid.href === null && invalid.hint.includes('已失效'),
      JSON.stringify(invalid && { href: invalid.href, hint: invalid.hint }),
    )
    record(
      'G5c 失效条目旁边有「移除」',
      !!invalid && invalid.acts.includes('移除'),
      JSON.stringify(invalid && invalid.acts),
    )

    // ---------- G6 手动移除 ----------
    await cdp.evaluate(
      "(() => { const b = [...document.querySelectorAll('section.fav li.row button')].find((x) => (x.textContent||'').trim() === '移除'); if (b) b.click(); return 'ok' })()",
    )
    const removed = await waitEval(
      cdp,
      "(() => { const s = document.querySelector('section.fav'); if (!s) return null; const rows = s.querySelectorAll('ul.rows > li.row').length; const n = s.querySelector('.ok'); const out = { rows, notice: n ? (n.textContent||'').trim() : '' }; return out.rows === 0 && out.notice !== '' ? out : null })()",
      12000,
    )
    record(
      'G6a 点「移除」后行消失并给提示',
      removed && removed.rows === 0 && removed.notice.includes('移除'),
      JSON.stringify(removed),
    )

    await cdp.navigate(site.url + '/me?tab=favorites&type=room')
    const afterReload = await waitEval(cdp, waitBlock())
    record(
      'G6b 刷新后仍然不在（真的删了行）',
      afterReload && afterReload.rows.length === 0,
      JSON.stringify(afterReload && afterReload.rows),
    )

    // ---------- G7 仅自己可见 ----------
    const profileOther = await fetch(SERVER + '/api/users/' + other.user.id + '/posts?limit=5')
    record('G7a 对照：他人档案的子资源端点可用（HTTP ' + profileOther.status + '）', profileOther.ok)

    await cdp.navigate(site.url + '/user/' + account.user.id)
    await waitEval(cdp, "(() => !!document.querySelector('main.body h1, main.body .title'))()", 12000)
    const ownProfile = await cdp.evaluate(
      "(() => ({ hasFav: [...document.querySelectorAll('h1,h2')].some((h) => (h.textContent||'').includes('收藏')) }))()",
    )
    await cdp.navigate(site.url + '/user/' + other.user.id)
    await waitEval(cdp, "(() => !!document.querySelector('main.body h1, main.body .title'))()", 12000)
    const otherProfile = await cdp.evaluate(
      "(() => ({ hasFav: [...document.querySelectorAll('h1,h2')].some((h) => (h.textContent||'').includes('收藏')) }))()",
    )
    record(
      'G7b /user/:id 上（本人与他人）都没有收藏块',
      ownProfile && otherProfile && ownProfile.hasFav === false && otherProfile.hasFav === false,
      JSON.stringify({ own: ownProfile, other: otherProfile }),
    )

    // ---------- G8 分页走票 09 端点 ----------
    const page1 = await fetch(SERVER + '/api/favorites?type=post&limit=1', { headers: authHeaders(account) })
    const page1Body = await page1.json().catch(() => ({}))
    const cursor = page1Body.items && page1Body.items[0] ? page1Body.items[0].cursor : ''
    let page2Body = {}
    if (cursor) {
      const page2 = await fetch(
        SERVER + '/api/favorites?type=post&limit=1&before=' + encodeURIComponent(cursor),
        { headers: authHeaders(account) },
      )
      page2Body = await page2.json().catch(() => ({}))
    }
    const firstId = page1Body.items && page1Body.items[0] ? page1Body.items[0].targetId : ''
    const secondId = page2Body.items && page2Body.items[0] ? page2Body.items[0].targetId : ''
    record(
      'G8 游标分页：limit=1 有 hasMore 与 cursor，第二页不重复',
      page1Body.hasMore === true && cursor !== '' && secondId !== '' && secondId !== firstId,
      JSON.stringify({ first: firstId, second: secondId, hasMore: page1Body.hasMore }),
    )

    // ---------- G9 源码级没有公开计数 ----------
    const srcFiles = walk(join(REPO_ROOT, 'client', 'src'), [])
    const offenders = srcFiles.filter((f) => stripComments(readFileSync(f, 'utf8')).includes('favoriteCount'))
    record('G9 源码里没有 favoriteCount（收藏不计任何公开数）', offenders.length === 0, JSON.stringify(offenders.map((f) => f.slice(REPO_ROOT.length + 1))))
  } finally {
    try {
      await chrome.close()
    } catch {
      /* 关浏览器失败不影响判据 */
    }
    site.close()
  }

  console.log('')
  console.log('判据：' + passed + '/' + (passed + failed) + ' 通过')
  console.log('判定：' + (failed === 0 ? 'PASS' : 'FAIL'))
  process.exit(failed === 0 ? 0 : 1)
}

main().catch((err) => {
  console.error('[ERROR] ' + (err && err.stack ? err.stack : err))
  process.exit(1)
})
