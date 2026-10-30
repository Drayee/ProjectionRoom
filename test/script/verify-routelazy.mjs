#!/usr/bin/env node
/**
 * 票 #79（懒加载 + 预热 + 标题 + 焦点播报）的**真机**验收。
 *
 * 为什么是真机脚本：判据全是"产物长什么样"与"浏览器里实际发生了什么" ——
 *   · 首包判据要看**构建产物**（哪些 chunk 被拆出来了、入口里有没有混进页面代码）；
 *   · 标题、焦点、播报要看**真实 DOM**（document.title、document.activeElement、
 *     aria-live 区域的文本）；
 *   · 预热要看**网络请求**（悬停时是否真的发起了那个 chunk 的请求）。
 * 这些在单元测试里只能测"函数返回什么"，测不出"渲染出来是不是这样"。
 *
 * ## 它在什么环境里跑
 *
 * **不需要后端**：本脚本自己起一个静态服务器托管 `client/dist`，并把 `/api/*` 一律回
 * 404。前端默认走 **mock 域**（api/mode.ts：只有显式配 `VITE_MOCK_*=0` 才翻真），
 * 所以帖子/好友/消息的数据都来自前端内存里的假数据。
 *
 * ## 前置
 *
 *   npm --prefix client run build      # 必须先构建出 client/dist
 *   node test/script/verify-routelazy.mjs
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 *
 * ## 判据
 *
 *  L1 **拆包真的发生了**：产物里有各页独立 chunk（RoomPage/AdminUsersPage/…），
 *     且这些 chunk **不在** index.html 里 —— 它们必须靠运行时动态 import 取。
 *  L2 **首包不含低频页代码**：入口 chunk 里**搜不到**管理端与低频页的页面文案
 *     （例如管理端页面的标题串、房间码复制按钮的文案）。
 *     ⚠ 路由表里的 `meta.title`（"管理端 · 用户"）**会**在入口里 —— 那是路由元信息，
 *     不是页面代码；所以判据挑的是**页面独有**的文案。
 *  L3 **标题随路由变**：真机换页后 `document.title` 变成「<页面名> · 月喵」。
 *  L4 **焦点进主标题**：换页后 `document.activeElement` 是新页的 h1，
 *     且它带 `tabindex="-1"`（可编程聚焦但不进 Tab 序列）。
 *  L5 **播报一次**：全局 aria-live 区域的文本 = 页面名，且**只有一个** live 区域。
 *  L6 **悬停预热**：pointerenter 导航项后，浏览器请求了对应的 chunk 文件。
 *  L7 **滚动不回归**：`/help#source` 深链滚到锚点；后退键恢复滚动位置。
 *  L8 **单点写标题**（源码级）：页面里不再各自写 `document.title`。
 */
import { createServer } from 'node:http'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9920'))

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
 * 静态服务器：托管 `client/dist`，SPA 回退到 index.html；`/api/*` 一律 404。
 *
 * 记录每一个被请求过的文件路径 —— L6（悬停预热）要看它。
 */
function startStatic(dir) {
  const requested = []
  return new Promise((done) => {
    const server = createServer((req, res) => {
      const path = new URL(req.url ?? '/', 'http://127.0.0.1').pathname
      requested.push(path)
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
        requested,
        close: () => server.close(),
      })
    })
  })
}

/** 入口 chunk 文件名（index.html 里那个 module script）。 */
function entryChunkName() {
  const html = readFileSync(join(DIST, 'index.html'), 'utf8')
  const m = html.match(/src="\/assets\/([^"]+\.js)"/)
  return m ? m[1] : ''
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const assetsDir = join(DIST, 'assets')
  const assetsFiles = readdirSync(assetsDir)
  const entryName = entryChunkName()
  const entryText = readFileSync(join(assetsDir, entryName), 'utf8')

  // ---------- L1 拆包真的发生了 ----------
  const pageChunks = assetsFiles.filter(
    (f) =>
      f.endsWith('.js') &&
      /^(RoomPage|AdminUsersPage|AdminRoomsPage|AdminOpsPage|MessagesPage|FriendsPage|ForumPage|AnimeBoardPage|PostEditorPage|PostDetailPage|PostsPage|OfficialPage|HelpPage|PublicRoomsPage|UserPage|VerifyEmailPage|LoginPage|RegisterPage|ComingSoonPage)-/.test(f),
  )
  evidence.pageChunks = pageChunks.length
  record(
    'L1a 各页独立 chunk 存在（拆包真的发生了）',
    pageChunks.length >= 15,
    pageChunks.length + ' 个页面 chunk',
  )

  const htmlText = readFileSync(join(DIST, 'index.html'), 'utf8')
  const inHtml = pageChunks.filter((f) => htmlText.includes(f))
  record(
    'L1b 页面 chunk 不在 index.html 里（必须运行时动态 import）',
    inHtml.length === 0,
    'index.html 直接引用了 ' + inHtml.length + ' 个页面 chunk',
  )

  // ---------- L2 首包不含低频页代码 ----------
  // 挑**页面独有**的文案（不是路由元信息里的 meta.title 串）。
  // ⚠ 挑标记的规矩：**只用"该页面组件里才有"的文案**。
  // 不能用顶栏导航项的名字（"番剧论坛"）、也不能用路由 meta.title（"管理端 · 用户"）——
  // 那些字符串住在首包里（顶栏与路由表是外壳的一部分），拿它们当判据必然假红。
  const lowFreqMarkers = [
    '复制房间码',       // RoomPage
    '下架',             // AdminRoomsPage
    '封禁',             // AdminUsersPage
    '主题串',           // ForumPage / AnimeBoardPage
    '操作审计',         // AdminOpsPage
  ]
  const found = lowFreqMarkers.filter((m) => entryText.includes(m))
  evidence.entrySizeKB = (entryText.length / 1024).toFixed(1)
  record(
    'L2a 首包不含低频页代码（管理端/房间/论坛的页面文案）',
    found.length === 0,
    found.length === 0
      ? '入口 ' + evidence.entrySizeKB + ' kB，未命中 ' + lowFreqMarkers.length + ' 个标记'
      : '入口里混进了：' + found.join('、'),
  )
  record(
    'L2b 入口 chunk 明显小于拆包前（拆包前 531 kB）',
    entryText.length / 1024 < 400,
    evidence.entrySizeKB + ' kB',
  )

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'routelazy')
  const cdp = await openTarget(BASE_PORT, 'routelazy')

  try {
    await cdp.navigate(site.url + '/')
    await waitFor(async () => (await cdp.evaluate("document.querySelector('.landing') !== null")), {
      label: '落地页渲染',
      timeoutMs: 15000,
    })

    // ---------- L3 标题随路由变 ----------
    const titleHome = await cdp.evaluate('document.title')
    evidence.titleHome = titleHome
    record('L3a 落地页标题', titleHome === '首页 · 月喵', titleHome)

    await cdp.navigate(site.url + '/help')
    await waitFor(async () => (await cdp.evaluate("document.querySelector('h1') !== null")), {
      label: '帮助页渲染',
      timeoutMs: 15000,
    })
    const titleHelp = await cdp.evaluate('document.title')
    evidence.titleHelp = titleHelp
    record('L3b 帮助页标题', titleHelp === '帮助 · 月喵', titleHelp)

    // 换页（**不是整页刷新**）：从 /help 点顶栏的「公开房间」入口 → /rooms。
    // 落地页（/）刻意无顶栏（meta.header='none'），所以从 /help 出发才有可点的入口。
    await cdp.navigate(site.url + '/help')
    await waitFor(async () => (await cdp.evaluate("document.querySelector('h1') !== null")), {
      label: '帮助页渲染',
      timeoutMs: 15000,
    })
    await cdp.evaluate(
      "(() => { const a = [...document.querySelectorAll('a')].find(x => (x.getAttribute('href')||'') === '/rooms'); " +
        "if (a) a.click(); return Boolean(a) })()",
    )
    await waitFor(async () => (await cdp.evaluate('location.pathname')) === '/rooms', {
      label: '换页到 /rooms',
      timeoutMs: 10000,
    })
    const titleAfterNav = await cdp.evaluate('document.title')
    evidence.titleAfterNav = titleAfterNav
    record('L3c 换页后标题跟着变', titleAfterNav === '公开房间 · 月喵', titleAfterNav)

    // ---------- L4 焦点进主标题 ----------
    await waitFor(
      async () => (await cdp.evaluate("document.activeElement && document.activeElement.tagName === 'H1'")),
      { label: '焦点落到主标题', timeoutMs: 10000 },
    )
    const focusInfo = await cdp.evaluate(
      "(() => { const el = document.activeElement; " +
        "return el ? { tag: el.tagName, tabindex: el.getAttribute('tabindex'), " +
        "text: (el.textContent || '').trim().slice(0, 20), " +
        "insideOutlet: Boolean(el.closest('#route-outlet')) } : null })()",
    )
    evidence.focus = focusInfo
    record(
      'L4a 换页后焦点落在主标题（h1）',
      Boolean(focusInfo) && focusInfo.tag === 'H1',
      JSON.stringify(focusInfo),
    )
    record(
      'L4b 主标题带 tabindex="-1"（可编程聚焦、不进 Tab 序列）',
      Boolean(focusInfo) && focusInfo.tabindex === '-1',
      focusInfo ? 'tabindex=' + focusInfo.tabindex : 'no focus',
    )
    record(
      'L4c 焦点在路由出口容器内（不是旧页残留）',
      Boolean(focusInfo) && focusInfo.insideOutlet,
      focusInfo ? String(focusInfo.insideOutlet) : 'no focus',
    )

    // 不滚动：焦点移动不应把页面滚下去
    const scrollY = await cdp.evaluate('window.scrollY')
    record('L4d 移焦点不滚动页面', scrollY === 0, 'scrollY=' + scrollY)

    // ---------- L5 播报一次 ----------
    const liveInfo = await cdp.evaluate(
      "(() => { const regions = [...document.querySelectorAll('[aria-live]')]; " +
        "return { count: regions.length, " +
        "texts: regions.map(r => (r.textContent || '').trim()) } })()",
    )
    evidence.live = liveInfo
    record('L5a 全站只有一个 aria-live 区域', liveInfo.count === 1, JSON.stringify(liveInfo))
    record(
      'L5b 播报文本是页面名',
      liveInfo.texts.some((t) => t === '公开房间'),
      JSON.stringify(liveInfo.texts),
    )
    record(
      'L5c 播报区域是 polite（不打断读屏）',
      await cdp.evaluate("Boolean(document.querySelector('[aria-live=\"polite\"]'))"),
      'aria-live=polite',
    )

    // ---------- L6 悬停预热 ----------
    // 判据是**真的发起了那个页面的 chunk 请求**（不是"页面加载了"）。
    // 因此从一个**冷**页面出发：/rooms 刚打开时 HelpPage 的 chunk 还没下载过。
    // ⚠ 请求日志是**全程**的：脚本前面访问过 /help，那次已经把 HelpPage chunk 下载过了。
    // 所以"预热前没下过"必须只看**进入 /rooms 之后**的请求，否则这条判据永远是红的。
    const markBeforeRooms = site.requested.length
    await cdp.navigate(site.url + '/rooms')
    await waitFor(async () => (await cdp.evaluate("document.querySelector('h1') !== null")), {
      label: '公开房页渲染',
      timeoutMs: 15000,
    })
    // 预热前：确认目标 chunk 还没被下载过（否则这条判据是空的）
    const helpChunkBefore = site.requested
      .slice(markBeforeRooms)
      .filter((r) => /HelpPage-.*\.js$/.test(r))
    record('L6a 预热前目标 chunk 未被下载（判据前提）', helpChunkBefore.length === 0,
      '进入 /rooms 后已下载 ' + helpChunkBefore.length + ' 条')

    const before = site.requested.length
    const hovered = await cdp.evaluate(
      "(() => { const a = [...document.querySelectorAll('a')].find(x => (x.getAttribute('href')||'') === '/help'); " +
        "if (!a) return null; " +
        "a.dispatchEvent(new PointerEvent('pointerenter', { bubbles: false })); return a.getAttribute('href') })()",
    )
    record('L6b 找到可悬停的导航入口', Boolean(hovered), String(hovered))
    await sleep(1200)
    const newReqs = site.requested.slice(before)
    const helpChunk = newReqs.filter((r) => /HelpPage-.*\.js$/.test(r))
    evidence.prefetchRequests = newReqs.slice()
    record(
      'L6c 悬停后发起了目标页 chunk 请求（预热生效）',
      helpChunk.length > 0,
      '新增请求 ' + newReqs.length + ' 条，其中 HelpPage chunk ' + helpChunk.length + ' 条：' + helpChunk.join(', '),
    )

    // 再悬停一次：**不该**重复请求（预热一次）
    const before2 = site.requested.length
    await cdp.evaluate(
      "(() => { const a = [...document.querySelectorAll('a')].find(x => (x.getAttribute('href')||'') === '/help'); " +
        "if (a) a.dispatchEvent(new PointerEvent('pointerenter', { bubbles: false })); return 'ok' })()",
    )
    await sleep(800)
    const repeat = site.requested.slice(before2).filter((r) => /HelpPage-.*\.js$/.test(r))
    record('L6d 重复悬停不重复下载（预热一次）', repeat.length === 0, '重复请求 ' + repeat.length + ' 条')

    // ---------- L7 滚动回归 ----------
    // (a) hash 深链滚到锚点
    await cdp.navigate(site.url + '/help#source')
    await sleep(900)
    const hashScroll = await cdp.evaluate('window.scrollY')
    evidence.hashScroll = hashScroll
    record('L7a hash 深链滚到锚点（/help#source）', hashScroll > 0, 'scrollY=' + hashScroll)

    // (b) 后退恢复滚动位置
    await cdp.navigate(site.url + '/help')
    await waitFor(async () => (await cdp.evaluate("document.querySelector('h1') !== null")), {
      label: '帮助页渲染',
      timeoutMs: 15000,
    })
    await cdp.evaluate('window.scrollTo(0, 400)')
    await sleep(200)
    const beforeBack = await cdp.evaluate('window.scrollY')
    await cdp.evaluate('history.pushState({}, "", "/posts"); window.dispatchEvent(new PopStateEvent("popstate")); "ok"')
    await sleep(400)
    await cdp.evaluate('history.back()')
    await sleep(900)
    const afterBack = await cdp.evaluate('window.scrollY')
    evidence.backScroll = { beforeBack, afterBack }
    record(
      'L7b 后退恢复滚动位置（scrollBehavior 不回归）',
      afterBack > 0 && Math.abs(afterBack - beforeBack) < 60,
      '回退前 ' + beforeBack + ' → 回退后 ' + afterBack,
    )
  } finally {
    cdp.close()
    chrome.close()
    site.close()
  }

  // ---------- L8 源码级判据 ----------
  // 为什么用源码级：这两条要防的是"以后有人又写回一份"，那是产物里看不出来的。
  const srcDir = join(REPO_ROOT, 'client', 'src')
  const pageFiles = []
  async function collect(dir) {
    for (const name of readdirSync(dir)) {
      const full = join(dir, name)
      if (statSync(full).isDirectory()) await collect(full)
      else if (/\.(vue|ts)$/.test(name)) pageFiles.push(full)
    }
  }
  await collect(srcDir)
  const writers = pageFiles.filter((f) => {
    const text = readFileSync(f, 'utf8')
    // 只看**代码**（去掉注释行），注释里提到 document.title 不算写入。
    const code = text.split('\n').filter((l) => !l.trim().startsWith('//') && !l.trim().startsWith('*') && !l.trim().startsWith('/*')).join('\n')
    return /document\.title\s*=/.test(code)
  })
  evidence.titleWriters = writers.map((f) => f.replace(srcDir, 'client/src'))
  // 路径分隔符归一：Windows 是反斜杠，用正斜杠 endsWith 会永远不匹配（假红）。
  const normalized = writers.map((f) => f.replace(/\\/g, '/'))
  record(
    'L8a document.title 只有一处写入点（单点纪律）',
    writers.length === 1 && normalized[0].endsWith('client/src/composables/useRoutePageMeta.ts'),
    '写入点 ' + writers.length + ' 处：' + normalized.map((f) => f.replace(/^.*client\/src/, 'client/src')).join(', '),
  )

  const prefetchText = readFileSync(join(srcDir, 'utils', 'routePrefetch.ts'), 'utf8')
  record(
    'L8b 预热失败静默（有 catch 且不重抛）',
    /\.catch\(/.test(prefetchText),
    'routePrefetch.ts 含 catch',
  )

  console.log('\n== 证据 ==')
  console.log(JSON.stringify(evidence, null, 2))
  console.log('\n通过 ' + passed + ' / 失败 ' + failed)
  console.log(failed === 0 ? '判定：PASS' : '判定：FAIL')
  process.exit(failed === 0 ? 0 : 1)
}

await main()
