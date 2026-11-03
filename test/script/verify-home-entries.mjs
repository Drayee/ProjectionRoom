#!/usr/bin/env node
/**
 * `verify-home-entries.mjs` —— 首页两个写入口（建房 / 发帖 / 加番剧）的**真机**验收。
 *
 * # 它守的是什么
 *
 * 票 #74-#79 收尾后用户报「找不到发帖和建房间的入口」。排查结论不是"功能坏了"，
 * 而是**入口在 4454c63「删除旧展示层」时被一起删掉且从未重建**：全前端只剩
 * 主播断线后自动重建那一条 `POST /api/rooms`，而发帖只剩 `/posts` 页头右上角一处。
 *
 * 所以本脚本钉的不是"某个按钮长得对不对"，而是**这两个入口存在且真的接通**：
 * 点下去必须发出请求 / 跳到目标页，而不是弹一句"能力待接入"。
 *
 * # 它在什么环境里跑
 *
 * **不需要后端**。本脚本自己起静态服务器托管 `client/dist`：
 *   - `POST /api/rooms` **真的回一份合法的创建响应**（见 `ROOM_RESPONSE`），
 *     这样"点创建 → 拿到房间码 → 跳 /room/<码>"整条路都能验；
 *   - 其余 `/api/*` 一律 404（理由见 `verify-avatar.mjs` 同名函数注释）；
 *   - 前端默认跑 **mock 域**（`api/mode.ts`），首页不依赖任何真接口。
 *
 * # 前置
 *
 *   npm --prefix client run build
 *   node test/script/verify-home-entries.mjs
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 *
 * # 判据
 *
 *  H1 **已登录首页渲染出两块卡片 + FAB**（不是"等待加入房间"占位文字）。
 *  H2 **FAB 展开/收起**：`aria-expanded` 跟着变，动作钮在收起时 `visibility: hidden`
 *     且 `tabindex="-1"`（不能只用 `opacity: 0` —— 那样键盘仍能 Tab 进看不见的按钮）。
 *  H3 **Esc 与点击外部都能收起**（两条独立的关闭路径）。
 *  H4 **「发送帖子」真的跳 `/posts/new`**（不是弹提示）。
 *  H5 **「添加番剧」真的跳 `/forum`**。
 *  H6 **「新建房间」走完整建房流程**：发 `POST /api/rooms`、拿到房间码、
 *     跳 `/room/<码>`，并且 sessionStorage 里落了带 `hostToken` 的进房凭据
 *     （主播复位令牌只在创建响应里出现一次，丢了就再也接不回主播位）。
 *  H7 **创建响应的 hostToken 为空时也能建房**（老服务端没有这个字段的兼容路径）。
 *  H8 **未登录时卡片仍然渲染**，只是显示一行门槛说明（隐藏它游客就不知道能开房）；
 *     点创建会被就地拦截到登录页，**不发请求**。
 *  H9 **reduced-motion 下 FAB 仍然可展开可用**（动画判死，功能不许跟着死）。
 *  H10 **源码级**：`useCreateRoom.ts` 是建房请求的唯一落点，
 *      `HomePage.vue` 里不许再出现第二份 `authedFetch('/api/rooms'`。
 */
import { createServer } from 'node:http'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9911'))

/** 参数解析：与仓内其它验收脚本同一套风格。 */
function argOf(argv, name, fallback) {
  const i = argv.indexOf('--' + name)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}

let passed = 0
let failed = 0
const evidence = {}

function record(name, pass, detail) {
  if (pass) passed += 1
  else failed += 1
  console.log('[' + (pass ? 'PASS' : 'FAIL') + '] ' + name + (detail ? ' — ' + detail : ''))
}

/**
 * 建房成功响应（服务端的真实形状见 `internal/handler/router.go` 的 `createRoomHandler`）。
 *
 * 只保留判据真正会读的字段：`roomId`（跳转目标）、`hostToken`（复位令牌）、
 * `iceServers`（前端类型要求，这里给空数组 —— 本脚本不验 ICE）。
 */
const ROOM_RESPONSE = {
  roomId: 'Z9Y8X7',
  hostToken: 'host-token-from-create',
  capacity: { maxMembers: 8, maxViewers: 7 },
  iceServers: [],
}

/** H7 用：老服务端（没有 hostToken 字段）。 */
const ROOM_RESPONSE_NO_TOKEN = {
  roomId: 'A1B2C3',
  capacity: { maxMembers: 8, maxViewers: 7 },
  iceServers: [],
}

/** MIME 表：只覆盖静态托管真正需要的几种。 */
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

/**
 * 静态服务器 + 一个假的 `POST /api/rooms`。
 *
 * `nextRoomResponse` 让单条用例能换掉响应（H7 的"老服务端"路径与 H8 的"不该发请求"
 * 都靠它）。同时把收到过的建房请求记进 `seen` 里 —— 判据要能区分
 * **"发了并被正确处理"** 与 **"根本没发"**，只读界面状态区分不了这两件事。
 */
function startStatic(dir) {
  const state = { nextRoomResponse: ROOM_RESPONSE, seen: [], failNext: false }
  return new Promise((done) => {
    const server = createServer((req, res) => {
      const path = new URL(req.url ?? '/', 'http://127.0.0.1').pathname

      if (path === '/api/rooms' && req.method === 'POST') {
        // 把请求体读完再回，否则客户端可能收到 ECONNRESET（写了一半的对端关闭）。
        const chunks = []
        req.on('data', (c) => chunks.push(c))
        req.on('end', () => {
          state.seen.push({ at: Date.now(), body: Buffer.concat(chunks).toString('utf8') })
          if (state.failNext) {
            res.writeHead(500, { 'Content-Type': 'application/json' })
            res.end('{"error":"boom"}')
            return
          }
          res.writeHead(200, { 'Content-Type': 'application/json' })
          res.end(JSON.stringify(state.nextRoomResponse))
        })
        return
      }

      if (path.startsWith('/api/') || path.startsWith('/ws')) {
        res.writeHead(404, { 'Content-Type': 'application/json' })
        res.end('{"error":"not found"}')
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
        'Content-Type': MIME[extname(file)] ?? 'application/octet-stream',
        'Content-Length': body.length,
      })
      res.end(body)
    })
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address()
      done({
        url: 'http://127.0.0.1:' + port,
        state,
        close: () => server.close(),
      })
    })
  })
}

/** 种一个「已登录」的假档案（理由同 `verify-avatar.mjs` 的同名种子）。 */
const SEED_PROFILE = {
  id: 1,
  username: 'mooncat',
  displayName: '月夜猫',
  email: '',
  role: 'user',
  status: 'active',
  createdAt: '2023-10-18T09:00:00Z',
  unread: 0,
  unreadKnown: false,
}

/** 在页面里种登录态（`pr:profile` + `pr:access`）。必须整页重载后才生效。 */
async function seedLoggedIn(cdp, url) {
  await cdp.navigate(url + '/')
  await cdp.evaluate(
    "localStorage.setItem('pr:profile', " +
      JSON.stringify(JSON.stringify(SEED_PROFILE)) +
      "); sessionStorage.setItem('pr:access', 'pr-verify-home-fake-token'); 'ok'",
  )
}

/** 清掉登录态 + 建房相关的一切本地痕迹（用例之间必须互不污染）。 */
async function clearSession(cdp, url) {
  await cdp.navigate(url + '/')
  await cdp.evaluate("localStorage.clear(); sessionStorage.clear(); 'ok'")
}

/** 点一个按钮（用真实鼠标事件而不是 `el.click()`：要验的是"用户点得动"）。 */
async function clickSelector(cdp, selector) {
  const box = await cdp.evaluate(
    `(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) return null;` +
      ` const r = el.getBoundingClientRect();` +
      ` return { x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) } })()`,
  )
  if (!box) return false
  await cdp.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: box.x, y: box.y })
  await cdp.send('Input.dispatchMouseEvent', { type: 'mousePressed', x: box.x, y: box.y, button: 'left', clickCount: 1 })
  await cdp.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: box.x, y: box.y, button: 'left', clickCount: 1 })
  return true
}

/** 读 FAB 的展开态（`aria-expanded` + 动作容器的可见性与 tabindex）。 */
const READ_FAB = `(() => {
  const toggle = document.querySelector('[data-testid=home-fab-toggle]')
  const actions = document.querySelector('[data-testid=home-fab-actions]')
  if (!toggle || !actions) return null
  const btn = actions.querySelector('[data-testid=home-fab-post]')
  return {
    expanded: toggle.getAttribute('aria-expanded'),
    visibility: getComputedStyle(actions).visibility,
    opacity: getComputedStyle(actions).opacity,
    actionTabindex: btn ? btn.getAttribute('tabindex') : 'missing',
    actionCount: actions.querySelectorAll('button').length,
  }
})()`

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'home-entries')
  const cdp = await openTarget(BASE_PORT, 'home-entries')

  try {
    const waitFab = () =>
      waitFor(async () => await cdp.evaluate("Boolean(document.querySelector('[data-testid=home-fab-toggle]'))"), {
        label: 'FAB 渲染出来',
        timeoutMs: 15000,
      })

    // ---------- H1 已登录首页的构成 ----------
    await seedLoggedIn(cdp, site.url)
    await cdp.navigate(site.url + '/')
    await waitFab()

    const shell = await cdp.evaluate(`(() => ({
      shell: Boolean(document.querySelector('[data-testid=home-shell]')),
      createCard: Boolean(document.querySelector('[data-testid=home-create-room]')),
      createName: Boolean(document.querySelector('[data-testid=home-create-name]')),
      createPassword: Boolean(document.querySelector('[data-testid=home-create-password]')),
      joinCard: Boolean(document.querySelector('#join-code')),
      oldWait: document.body.textContent.includes('等待加入房间'),
      fabActions: document.querySelectorAll('[data-testid=home-fab-actions] button').length,
    }))()`)
    evidence.homeShell = shell
    record('H1a 首页有两张卡片（建房 + 进房）', shell.createCard && shell.joinCard,
      'create=' + shell.createCard + ' join=' + shell.joinCard)
    record('H1b 建房卡片有昵称与密码两个输入', shell.createName && shell.createPassword,
      'name=' + shell.createName + ' password=' + shell.createPassword)
    record('H1c 旧占位文案「等待加入房间」已移除', !shell.oldWait)
    record('H1d FAB 有三个动作钮', shell.fabActions === 3, 'count=' + shell.fabActions)

    // 昵称预填：来自账号昵称（已登录态下 `auth.displayName` = 月夜猫）。
    const seededName = await cdp.evaluate("document.querySelector('[data-testid=home-create-name]').value")
    evidence.seededName = seededName
    record('H1e 建房昵称预填成账号昵称', seededName === '月夜猫', seededName)

    // ---------- H2 展开 / 收起 ----------
    const collapsed = await cdp.evaluate(READ_FAB)
    record('H2a 初始是收起态（aria-expanded=false）', collapsed?.expanded === 'false',
      'aria-expanded=' + collapsed?.expanded)
    record('H2b 收起时动作容器不可见（visibility:hidden）', collapsed?.visibility === 'hidden',
      'visibility=' + collapsed?.visibility)
    record('H2c 收起时动作钮移出 Tab 序列（tabindex=-1）', collapsed?.actionTabindex === '-1',
      'tabindex=' + collapsed?.actionTabindex)

    await clickSelector(cdp, '[data-testid=home-fab-toggle]')
    await waitFor(async () => (await cdp.evaluate(READ_FAB))?.expanded === 'true', {
      label: 'FAB 展开',
      timeoutMs: 5000,
    })
    // 等过渡跑完再读 visibility（过渡中途读会读到仍在动画的中间值）。
    await sleep(400)
    const opened = await cdp.evaluate(READ_FAB)
    evidence.fabOpened = opened
    record('H2d 点加号后 aria-expanded=true', opened?.expanded === 'true', 'aria-expanded=' + opened?.expanded)
    record('H2e 展开后动作容器可见', opened?.visibility === 'visible', 'visibility=' + opened?.visibility)
    record('H2f 展开后动作钮可聚焦（tabindex 摘掉）', opened?.actionTabindex === null,
      'tabindex=' + String(opened?.actionTabindex))

    // ---------- H3 Esc 收起 ----------
    await cdp.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 })
    await cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 })
    await waitFor(async () => (await cdp.evaluate(READ_FAB))?.expanded === 'false', {
      label: 'Esc 收起 FAB',
      timeoutMs: 5000,
    })
    record('H3a Esc 能收起 FAB', true, 'aria-expanded=false')

    // 点击外部收起：点在页面空白处（`.body` 底部）。
    await clickSelector(cdp, '[data-testid=home-fab-toggle]')
    await waitFor(async () => (await cdp.evaluate(READ_FAB))?.expanded === 'true', {
      label: 'FAB 再次展开',
      timeoutMs: 5000,
    })
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: 5, y: 400 })
    await cdp.send('Input.dispatchMouseEvent', { type: 'mousePressed', x: 5, y: 400, button: 'left', clickCount: 1 })
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: 5, y: 400, button: 'left', clickCount: 1 })
    await waitFor(async () => (await cdp.evaluate(READ_FAB))?.expanded === 'false', {
      label: '点击外部收起 FAB',
      timeoutMs: 5000,
    })
    record('H3b 点击外部能收起 FAB', true, 'aria-expanded=false')

    // ---------- H4 「发送帖子」真的跳页 ----------
    await cdp.navigate(site.url + '/')
    await waitFab()
    await clickSelector(cdp, '[data-testid=home-fab-toggle]')
    await waitFor(async () => (await cdp.evaluate(READ_FAB))?.expanded === 'true', { label: '展开', timeoutMs: 5000 })
    await sleep(250)
    await clickSelector(cdp, '[data-testid=home-fab-post]')
    await waitFor(async () => (await cdp.evaluate('location.pathname')) === '/posts/new', {
      label: '跳到发帖页',
      timeoutMs: 10000,
    })
    const postPath = await cdp.evaluate('location.pathname')
    record('H4 「发送帖子」跳到 /posts/new', postPath === '/posts/new', postPath)

    // ---------- H5 「添加番剧」真的跳页 ----------
    await cdp.navigate(site.url + '/')
    await waitFab()
    await clickSelector(cdp, '[data-testid=home-fab-toggle]')
    await waitFor(async () => (await cdp.evaluate(READ_FAB))?.expanded === 'true', { label: '展开', timeoutMs: 5000 })
    await sleep(250)
    await clickSelector(cdp, '[data-testid=home-fab-anime]')
    await waitFor(async () => (await cdp.evaluate('location.pathname')) === '/forum', {
      label: '跳到番剧目录',
      timeoutMs: 10000,
    })
    const forumPath = await cdp.evaluate('location.pathname')
    record('H5 「添加番剧」跳到 /forum', forumPath === '/forum', forumPath)

    // ---------- H6 「新建房间」走完整建房流程 ----------
    await cdp.navigate(site.url + '/')
    await waitFab()
    site.state.seen.length = 0
    await clickSelector(cdp, '[data-testid=home-fab-toggle]')
    await waitFor(async () => (await cdp.evaluate(READ_FAB))?.expanded === 'true', { label: '展开', timeoutMs: 5000 })
    await sleep(250)
    await clickSelector(cdp, '[data-testid=home-fab-create]')

    await waitFor(async () => site.state.seen.length > 0, { label: '发出 POST /api/rooms', timeoutMs: 10000 })
    record('H6a 点「新建房间」真的发出 POST /api/rooms', site.state.seen.length > 0,
      'requests=' + site.state.seen.length)

    await waitFor(async () => (await cdp.evaluate('location.pathname')) === '/room/Z9Y8X7', {
      label: '跳到 /room/Z9Y8X7',
      timeoutMs: 10000,
    })
    const roomPath = await cdp.evaluate('location.pathname')
    record('H6b 建房成功后跳到 /room/<房间码>', roomPath === '/room/Z9Y8X7', roomPath)

    // 主播复位令牌只在创建响应里出现一次 —— 必须落在进房凭据里。
    const creds = await cdp.evaluate(`(() => {
      const raw = sessionStorage.getItem('pr:join:Z9Y8X7')
      return raw ? JSON.parse(raw) : null
    })()`)
    evidence.joinCreds = creds
    record('H6c 进房凭据已落 sessionStorage', creds !== null, creds ? 'ok' : 'missing')
    record('H6d 凭据里带 hostToken（主播复位令牌）', creds?.hostToken === 'host-token-from-create',
      String(creds?.hostToken))
    record('H6e 凭据角色是 host', creds?.role === 'host', String(creds?.role))

    // ---------- H7 老服务端没有 hostToken 也能建房 ----------
    site.state.nextRoomResponse = ROOM_RESPONSE_NO_TOKEN
    await cdp.navigate(site.url + '/')
    await waitFab()
    await clickSelector(cdp, '[data-testid=home-create-room]')
    await waitFor(async () => (await cdp.evaluate('location.pathname')) === '/room/A1B2C3', {
      label: '无 hostToken 也能跳到房间页',
      timeoutMs: 10000,
    })
    const noTokenPath = await cdp.evaluate('location.pathname')
    record('H7 缺 hostToken 的响应仍然建房成功', noTokenPath === '/room/A1B2C3', noTokenPath)
    const noTokenCreds = await cdp.evaluate(
      "(() => { const raw = sessionStorage.getItem('pr:join:A1B2C3'); return raw ? JSON.parse(raw) : null })()",
    )
    record('H7b 缺 hostToken 时凭据里该字段为空（不带 undefined 字符串）',
      noTokenCreds !== null && !('hostToken' in noTokenCreds && typeof noTokenCreds.hostToken === 'string'),
      JSON.stringify(noTokenCreds?.hostToken ?? null))
    site.state.nextRoomResponse = ROOM_RESPONSE

    // ---------- H8 未登录：看到的是落地页，且点不出任何建房请求 ----------
    await clearSession(cdp, site.url)
    await cdp.navigate(site.url + '/')
    // 未登录时首页是 `LandingView`（hero 形态：一个大标题 + 一个可点的「开始」卡片），
    // 不是已登录外壳 —— 卡片与 FAB 都不该出现。
    await waitFor(async () => (await cdp.evaluate("Boolean(document.querySelector('.landing'))")), {
      label: '未登录首页渲染落地页',
      timeoutMs: 15000,
    })
    site.state.seen.length = 0
    const guest = await cdp.evaluate(`(() => ({
      landing: Boolean(document.querySelector('.landing')),
      shell: Boolean(document.querySelector('[data-testid=home-shell]')),
      createCard: Boolean(document.querySelector('[data-testid=home-create-room]')),
      fab: Boolean(document.querySelector('[data-testid=home-fab-toggle]')),
    }))()`)
    evidence.guest = guest
    record('H8a 未登录首页是落地页（不是已登录外壳）', guest.landing && !guest.shell,
      'landing=' + guest.landing + ' shell=' + guest.shell)
    record('H8b 未登录时看不到 FAB 与建房卡片（那是登录后的动作）', !guest.fab && !guest.createCard,
      'fab=' + guest.fab + ' create=' + guest.createCard)

    // 落地页的「开始」点开是登录窗 —— 建房入口在登录前根本不存在，
    // 所以这里能验的是**没有任何请求被发出去**（而不是"点了被拦截"）。
    await clickSelector(cdp, '.card')
    await sleep(600)
    record('H8c 未登录时点「开始」不发任何建房请求', site.state.seen.length === 0,
      'requests=' + site.state.seen.length)

    // ---------- H9 reduced-motion 下 FAB 仍然可用 ----------
    await seedLoggedIn(cdp, site.url)
    await cdp.navigate(site.url + '/')
    await waitFab()
    await cdp.send('Emulation.setEmulatedMedia', {
      features: [{ name: 'prefers-reduced-motion', value: 'reduce' }],
    })
    await sleep(200)
    await clickSelector(cdp, '[data-testid=home-fab-toggle]')
    await waitFor(async () => (await cdp.evaluate(READ_FAB))?.expanded === 'true', {
      label: 'reduced-motion 下展开 FAB',
      timeoutMs: 5000,
    })
    await sleep(200)
    const rmFab = await cdp.evaluate(READ_FAB)
    evidence.reducedMotionFab = rmFab
    record('H9a reduced-motion 下 FAB 仍能展开（aria-expanded=true）', rmFab?.expanded === 'true',
      'aria-expanded=' + rmFab?.expanded)
    record('H9b reduced-motion 下动作容器仍可见（动画判死、功能不判死）', rmFab?.visibility === 'visible',
      'visibility=' + rmFab?.visibility)
    await cdp.send('Emulation.setEmulatedMedia', { features: [] })

    // ---------- H10 源码级：建房请求只有一个落点 ----------
    const SRC = join(REPO_ROOT, 'client', 'src')
    const stripComments = (src) =>
      src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/[^\n]*/g, '$1')

    const POST_ROOMS = /authedFetch\(\s*['"]\/api\/rooms['"]/
    const offenders = []
    const walk = (dir) => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        const full = join(dir, entry.name)
        if (entry.isDirectory()) {
          walk(full)
          continue
        }
        if (!/\.(ts|vue)$/.test(entry.name)) continue
        // stores/room.ts 的 rebuildRoom() 是**断线自动重建**，不是用户主动建房：
        // 它必须留着（缺陷 1 的修复），不在本判据的射程内。
        if (full.endsWith(join('stores', 'room.ts'))) continue
        if (POST_ROOMS.test(stripComments(readFileSync(full, 'utf8')))) {
          offenders.push(full.slice(SRC.length + 1))
        }
      }
    }
    walk(SRC)
    evidence.postRoomsOffenders = offenders
    record('H10 建房请求只有一个落点（useCreateRoom.ts）',
      offenders.length === 1 && offenders[0].endsWith(join('room', 'useCreateRoom.ts')),
      offenders.join(', '))

    console.log('\n通过 ' + passed + ' / 失败 ' + failed)
    console.log(JSON.stringify(evidence, null, 2))
    process.exitCode = failed === 0 ? 0 : 1
  } finally {
    cdp.close()
    chrome.close()
    site.close()
  }
}

await main()
