#!/usr/bin/env node
/**
 * `verify-avatar.mjs` —— 票 #61（UserAvatar：单一取色源 + 可点导航）的**真机**验收。
 *
 * # 为什么是真机脚本而不是 node 单测
 *
 * 票 #61 的判据全是**DOM 可见终态**：同一个用户在不同页面是不是同一个颜色、点头像落到哪、
 * 键盘能不能走到它、嵌套在链接/按钮里的那几处是不是仍然可点。这些判据在单元测试里
 * 只能测「函数返回什么」，测不出「渲染出来是不是这样」—— 而本仓库前端**没有测试框架**
 * （client/package.json 只有 vue-tsc + vite），所以沿用仓内既有做法：真机脚本
 * （见 `verify-connection.mjs` / `verify-auth.mjs` / `verify-forgot.mjs`）。
 *
 * # 它在什么环境里跑
 *
 * **不需要后端**：本脚本自己起一个静态服务器托管 `client/dist`，并把 `/api/*` 一律回
 * 404（见下）。前端默认走 **mock 域**（`api/mode.ts`：只有显式配 `VITE_MOCK_*=0`
 * 才翻真），所以好友页 / 论坛 / 档案的数据都来自前端内存里的假数据。
 *
 * 为什么不需要真后端：票 #61 的判据全是**同一份 mock 数据在不同页面的呈现是否一致**，
 * 真后端只会引入「账号要注册、库要能连」的噪声，而判据本身不依赖服务端。
 *
 * # 前置
 *
 *   npm --prefix client run build      # 必须先构建出 client/dist
 *   node test/script/verify-avatar.mjs
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 *
 * # 判据
 *
 *  A1 **同一用户跨页同色**：好友页里「放映员」（id=2）的头像底色 == `/user/2` 档案页
 *     头像底色。两处曾经是两个真源（顶栏还另有一份 `hsl(hash)`），所以这条钉住
 *     「消除同一用户两处不同色」这个目标本身。
 *  A2 **点击落点**：点好友行的头像 → URL 变成 `/user/2`（他人进公开档案）。
 *     顶栏头像是「本人」：`/me` 属票 #69、**现在还不存在**，所以落点是本人公开档案
 *     `/user/1`（链接到一个不存在的路由会掉进空白页，那比退回公开档案差得多）。
 *  A3 **键盘可达**：Tab 能走到头像链接上，且它带 `aria-label="查看 <昵称> 的档案"`。
 *  A4 **嵌套处不可点**：论坛主题行（外层是 RouterLink）与消息会话行（外层是 button）
 *     里的头像**不是链接** —— 链接套链接是无效 HTML，浏览器会把内层链接从可点区域摘掉，
 *     症状是「点头像没反应」。这条钉住 `interactive=false` 的用法。
 *  A5 **hover 反馈**：悬停时底色提亮（filter）且有轻微位移（transform）。
 *  A6 **reduced-motion**：开启「减少动态效果」后，悬停**仍然换色**（反馈不能消失），
 *     但**不再位移**（动画该判死）。
 */
import { createServer } from 'node:http'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9910'))

/** 参数解析：与仓内其它验收脚本同一套风格（lib/browser.mjs 里也有一份）。 */
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

/** MIME 表：只覆盖静态托管真正需要的几种（字体/图标/脚本/样式）。 */
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
 * 静态服务器：托管 `client/dist`，SPA 回退到 index.html。
 *
 * `/api/*` 一律回 404（不是 index.html）：前端在 mock 态下**本来就不该打这些请求**，
 * 而一旦真打了，返回 index.html 会让调用方把一段 HTML 当 JSON 解析，
 * 报出一个与判据无关的错，把真正的失败信号淹掉。
 */
function startStatic(dir) {
  return new Promise((done) => {
    const server = createServer((req, res) => {
      const path = new URL(req.url ?? '/', 'http://127.0.0.1').pathname
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
        close: () => server.close(),
      })
    })
  })
}

/**
 * 在页面里种一个「已登录」的假档案。
 *
 * 为什么能这么干：`stores/auth.ts` 的启动引导看到 sessionStorage 里有 access token
 * 就会把状态置成 `authenticated`，随后打一次 `/me`；本脚本的静态服务器把 `/api/*`
 * 回 404，那次请求失败是**静默**的（只有明确的 401 才会清会话），所以登录态成立。
 *
 * 为什么需要它：好友页的列表只在 `auth.isLoggedIn` 时才加载（未登录显示引导），
 * 而 mock 域没有账号系统 —— 用真实后端注册账号会把判据变成「账号系统能不能跑」。
 * 种子只影响**显示层**（顶层头像的昵称/首字母），不伪造任何授权结论。
 */
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

/** 在页面里读一个元素的背景色（归一成 `rgb(...)` 字符串）。 */
const READ_BG = '(el) => el ? getComputedStyle(el).backgroundColor : ""'

/** 找到「昵称是给定值」的那一行（好友页的行结构：`who strong` 是昵称）。 */
const FIND_ROW = `(() => {
  const rows = [...document.querySelectorAll('.row')]
  const row = rows.find((r) => (r.querySelector('.who strong')?.textContent || '').trim() === %NAME%)
  if (!row) return null
  const el = row.querySelector('.avatar')
  return el ? { bg: getComputedStyle(el).backgroundColor, href: el.getAttribute('href') || '' } : null
})()`

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'avatar')
  const cdp = await openTarget(BASE_PORT, 'avatar')

  try {
    // ---------- 种登录态 + 打开好友页 ----------
    await cdp.navigate(site.url + '/')
    await cdp.evaluate(
      "localStorage.setItem('pr:profile', " +
        JSON.stringify(JSON.stringify(SEED_PROFILE)) +
        "); sessionStorage.setItem('pr:access', 'pr-verify-avatar-fake-token'); 'ok'",
    )
    await cdp.navigate(site.url + '/friends')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.row .avatar').length")) > 0,
      { label: '好友列表渲染出头像', timeoutMs: 15000 },
    )

    // ---------- A1 同一用户跨页同色 ----------
    const friendsAvatar = await cdp.evaluate(FIND_ROW.replace('%NAME%', JSON.stringify('放映员')))
    const friendsColor = friendsAvatar ? friendsAvatar.bg : ''
    evidence.friendsAvatarColor = friendsColor
    record('A1a 好友页「放映员」头像有底色', Boolean(friendsColor), friendsColor)

    await cdp.navigate(site.url + '/user/2')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('.avatar-lg'))")),
      { label: '档案页渲染出头像', timeoutMs: 15000 },
    )
    const profileColor = await cdp.evaluate(
      "(() => { const el = document.querySelector('.avatar-lg'); return el ? getComputedStyle(el).backgroundColor : '' })()",
    )
    evidence.profileAvatarColor = profileColor
    record(
      'A1b 同一用户跨页同色（好友页 == 档案页）',
      Boolean(friendsColor) && friendsColor === profileColor,
      '好友页 ' + friendsColor + ' / 档案页 ' + profileColor,
    )

    // ---------- A2 点击落点 ----------
    await cdp.navigate(site.url + '/friends')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.row .avatar').length")) > 0,
      { label: '好友列表渲染出头像', timeoutMs: 15000 },
    )
    const rowInfo = await cdp.evaluate(FIND_ROW.replace('%NAME%', JSON.stringify('放映员')))
    evidence.friendAvatarHref = rowInfo ? rowInfo.href : ''
    record('A2a 好友行头像是链接且指向 /user/2', rowInfo !== null && rowInfo.href === '/user/2',
      rowInfo ? rowInfo.href : 'no row')

    await cdp.evaluate(
      "(() => { const rows = [...document.querySelectorAll('.row')]; " +
        "const row = rows.find((r) => (r.querySelector('.who strong')?.textContent || '').trim() === '放映员'); " +
        "const a = row ? row.querySelector('a.avatar') : null; if (a) a.click(); return Boolean(a) })()",
    )
    await waitFor(async () => (await cdp.evaluate('location.pathname')) === '/user/2', {
      label: '点击头像后跳到 /user/2',
      timeoutMs: 10000,
    })
    const landed = await cdp.evaluate('location.pathname')
    record('A2b 点击头像后落到 /user/2', landed === '/user/2', landed)

    // 顶栏头像（本人）：/me 尚未存在（票 #69），落点必须是本人公开档案。
    await cdp.navigate(site.url + '/friends')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('header a.avatar'))")),
      { label: '顶栏头像渲染', timeoutMs: 15000 },
    )
    const headerHref = await cdp.evaluate(
      "(() => { const a = document.querySelector('header a.avatar'); return a ? a.getAttribute('href') : '' })()",
    )
    evidence.headerAvatarHref = headerHref
    record('A2c 顶栏头像（本人）落点是本人公开档案 /user/1', headerHref === '/user/1', headerHref)

    // ---------- A3 键盘可达 + 无障碍名 ----------
    const a11y = await cdp.evaluate(
      "(() => { const a = document.querySelector('header a.avatar'); if (!a) return { tag: '', label: '', tabbable: false }; " +
        "return { tag: a.tagName, label: a.getAttribute('aria-label') || '', tabbable: a.tabIndex >= 0 }; })()",
    )
    record('A3a 顶栏头像是 <a>（键盘可达）', a11y.tag === 'A' && a11y.tabbable, JSON.stringify(a11y))
    record('A3b 头像带可访问名', /查看 .+ 的档案/.test(a11y.label), a11y.label)

    // Tab 真的能走到它（headless 里用真实按键事件，而不是 element.focus()：
    // 程序化 focus 与 :focus-visible 的匹配规则在 Chrome 里不是一回事）。
    let focusedIsAvatar = false
    for (let i = 0; i < 40 && !focusedIsAvatar; i += 1) {
      await cdp.send('Input.dispatchKeyEvent', {
        type: 'rawKeyDown',
        key: 'Tab',
        code: 'Tab',
        windowsVirtualKeyCode: 9,
      })
      await cdp.send('Input.dispatchKeyEvent', {
        type: 'keyUp',
        key: 'Tab',
        code: 'Tab',
        windowsVirtualKeyCode: 9,
      })
      focusedIsAvatar = await cdp.evaluate(
        "Boolean(document.activeElement && document.activeElement.classList.contains('avatar'))",
      )
    }
    record('A3c Tab 能聚焦到头像', focusedIsAvatar, focusedIsAvatar ? '已聚焦' : '40 次 Tab 未落到头像')

    // ---------- A4 嵌套处不可点 ----------
    await cdp.navigate(site.url + '/forum/1')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.thread .avatar').length")) > 0,
      { label: '论坛主题行渲染出头像', timeoutMs: 15000 },
    )
    const forumAnchors = await cdp.evaluate("document.querySelectorAll('.thread a.avatar').length")
    record('A4a 论坛主题行里的头像不是链接（外层已是 RouterLink）', forumAnchors === 0, 'a.avatar=' + forumAnchors)

    await cdp.navigate(site.url + '/messages')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.conv .avatar').length")) > 0,
      { label: '消息会话行渲染出头像', timeoutMs: 15000 },
    )
    const msgAnchors = await cdp.evaluate("document.querySelectorAll('.conv a.avatar').length")
    record('A4b 消息会话行里的头像不是链接（外层是 button）', msgAnchors === 0, 'a.avatar=' + msgAnchors)

    // ---------- A5 / A6 hover 反馈与 reduced-motion ----------
    await cdp.navigate(site.url + '/friends')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('header a.avatar'))")),
      { label: '顶栏头像渲染', timeoutMs: 15000 },
    )
    const box = await cdp.evaluate(
      "(() => { const a = document.querySelector('header a.avatar'); if (!a) return null; " +
        "const r = a.getBoundingClientRect(); return { x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) }; })()",
    )
    const READ_STYLE =
      "(() => { const a = document.querySelector('header a.avatar'); const s = getComputedStyle(a); " +
      "return { filter: s.filter, transform: s.transform }; })()"
    const before = await cdp.evaluate(READ_STYLE)
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: box.x, y: box.y })
    await sleep(400)
    const after = await cdp.evaluate(READ_STYLE)
    evidence.hoverBefore = before
    evidence.hoverAfter = after
    record('A5a 悬停时底色提亮（filter 变化）', after.filter !== before.filter, before.filter + ' → ' + after.filter)
    record('A5b 悬停时有轻微位移（transform 变化）', after.transform !== before.transform,
      before.transform + ' → ' + after.transform)

    // reduced-motion：反馈仍在（换色），位移消失。
    await cdp.send('Emulation.setEmulatedMedia', {
      features: [{ name: 'prefers-reduced-motion', value: 'reduce' }],
    })
    await sleep(300)
    // 先把鼠标移开再读"静止态"：否则读到的 filter 已经是 hover 态的 brightness(1.12)，
    // 与 hover 后的读数相同，"是否换色"就无从判断（判据会假红）。
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: 2, y: 2 })
    await sleep(400)
    const rmBefore = await cdp.evaluate(READ_STYLE)
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: box.x, y: box.y })
    await sleep(400)
    const rmAfter = await cdp.evaluate(READ_STYLE)
    evidence.reducedMotion = { before: rmBefore, after: rmAfter }
    record('A6a reduced-motion 下仍换色（反馈不消失）', rmAfter.filter !== rmBefore.filter,
      rmBefore.filter + ' → ' + rmAfter.filter)
    record('A6b reduced-motion 下不再位移', rmAfter.transform === rmBefore.transform,
      rmBefore.transform + ' → ' + rmAfter.transform)

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
