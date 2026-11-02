#!/usr/bin/env node
/**
 * `verify-me.mjs` —— me 管理台路由切分（票 #69）的**真机**验收。
 *
 * # 为什么是真机脚本
 *
 * 本票判据全是**地址与界面状态**："`/me?tab=relations` 进来选中哪个分区"、
 * "游客会不会被弹走"、"顶栏头像现在落到哪"、"本人的 `/user/:id` 上还有没有编辑表单"。
 * 这些在 HTTP 层单测里都看不出来 —— 后端返回的数据对不对是另一回事。
 *
 * # 它在什么环境里跑
 *
 * **不需要后端**：本脚本自己起静态服务器托管 `client/dist`，并把 `/api/*` 一律回 404。
 * 前端默认走 **mock 域**（`api/mode.ts`：只有显式配 `VITE_MOCK_*=0` 才翻真），
 * 所以关系名单、公开档案都来自 `api/mock/*` 的内存假数据。
 *
 * # 前置
 *
 *   npm --prefix client run build      # 必须先构建出 client/dist
 *   node test/script/verify-me.mjs
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 *
 * # 判据（对应工单验收四条）
 *
 *  M1  **未登录访问就地引导**（验收 ④）：`/me` 上出现「登录后才能进管理台」+ 去登录
 *      按钮（带 `?redirect=/me`），且**地址仍是 `/me`**（读动作被弹走最烦）。
 *  M2  **`/me` 概览各块渲染**（验收 ①③）：标题、昵称输入框、我的帖子块、我的放映室块、
 *      三个分区 tab 都在。
 *  M3  **`?tab=relations` 进 URL 生效**（验收 ①）：关系分区选中，四份名单 tab 在场。
 *  M4  **`?list=` 只在关系分区生效**（验收 ①）：`?tab=relations&list=blocked` 选中黑名单，
 *      行里给的是「解除拉黑」而不是关注类动作。
 *  M5  **点名单 tab 写回 URL**（验收 ①）：点「粉丝」后 `location.search` 同时含
 *      `tab=relations` 与 `list=followers`。
 *  M6  **`?tab=settings` 槽位存在**（验收 ③）：设置分区选中，正文里出现
 *      `/me?tab=settings` 这个已钉住的地址。
 *  M7  **未知 tab 回落默认**（验收 ①）：`?tab=bogus` 停在概览，不空白、不报错。
 *  M8  **顶栏头像落点切到 `/me`**（验收 ①）：`header a.avatar` 的 href 是 `/me`。
 *  M9  **「查看我的公开主页」互链**（验收 ①）：href 是 `/user/1`。
 *  M10 **本人 `/user/:id` 变回纯橱窗**（验收 ①）：只有一条去管理台的提示，
 *      **没有**昵称输入框、没有「修改昵称」标题（旧同页双视角已退役）。
 *  M11 **`/me` 上头像位点亮、分区全灭**（票 #77 的当前态语义随 #69 扩展）。
 *  M12 **源码级：键名唯一源**（去注释后 grep）：`client/src` 里除
 *      `utils/meRoute.ts` 外不得出现 `query['tab']` / `query['list']` 这两个字面量。
 */
import { createServer } from 'node:http'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9931'))

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

/** 静态服务器：托管 dist，SPA 回退到 index.html，`/api/*` 一律 404。 */
function startStatic(root) {
  const server = createServer((req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1')
    if (url.pathname.startsWith('/api/')) {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end('{"error":"no backend","code":"HTTP_404"}')
      return
    }
    let file = join(root, decodeURIComponent(url.pathname))
    if (!file.startsWith(root)) {
      res.writeHead(403)
      res.end()
      return
    }
    if (existsSync(file) && statSync(file).isDirectory()) file = join(file, 'index.html')
    if (!existsSync(file)) file = join(root, 'index.html')
    const body = readFileSync(file)
    res.writeHead(200, { 'content-type': MIME[extname(file)] ?? 'application/octet-stream' })
    res.end(body)
  })
  return new Promise((ok) => {
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address()
      ok({ url: 'http://127.0.0.1:' + port, close: () => server.close() })
    })
  })
}

/** 登录态种子（与 verify-avatar / verify-friends 同一份形状）。 */
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

/** 当前分区：读 tab 条上 `aria-selected="true"` 的那一个的文案。 */
const READ_TAB = `(() => {
  const t = [...document.querySelectorAll('nav.tabs [role=tab]')].find((b) => b.getAttribute('aria-selected') === 'true')
  return t ? (t.textContent || '').trim() : ''
})()`

/** 关系分区当前选中的名单（`nav.lists` 里的 `aria-selected`）。 */
const READ_LIST = `(() => {
  const t = [...document.querySelectorAll('nav.lists [role=tab]')].find((b) => b.getAttribute('aria-selected') === 'true')
  return t ? (t.textContent || '').trim() : ''
})()`

/**
 * 源码级判据的取样：去掉注释再 grep。
 *
 * 组件注释里会提到 `?tab=` 的来龙去脉，直接 grep 会假红 —— 这是 verify-avatar
 * 早就踩过的坑（那次是注释里提到被删掉的 `hsl(hash)`）。
 */
function stripComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/[^\n]*/g, '$1')
}

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (/\.(ts|vue)$/.test(name)) out.push(p)
  }
  return out
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'me')
  const cdp = await openTarget(BASE_PORT, 'me')

  try {
    // ---------- M1 游客：就地引导，地址不变 ----------
    await cdp.navigate(site.url + '/me')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('[data-testid=async-state]'))")),
      { label: '游客看到登录引导', timeoutMs: 15000 },
    )
    const guest = await cdp.evaluate(`(() => {
      const box = document.querySelector('[data-testid=async-state]')
      const a = box ? box.querySelector('a') : null
      return {
        text: box ? (box.textContent || '').trim() : '',
        ctaText: a ? (a.textContent || '').trim() : '',
        ctaHref: a ? a.getAttribute('href') || '' : '',
        path: location.pathname,
        search: location.search,
      }
    })()`)
    evidence.guest = guest
    record(
      'M1 游客访问 /me 就地渲染登录引导（不跳转，带 redirect）',
      guest.text.includes('登录后才能进管理台') &&
        guest.ctaHref.includes('/login') &&
        guest.ctaHref.includes('redirect') &&
        guest.path === '/me',
      JSON.stringify(guest),
    )

    // ---------- 种登录态 ----------
    await cdp.evaluate(
      "localStorage.setItem('pr:profile', " +
        JSON.stringify(JSON.stringify(SEED_PROFILE)) +
        "); sessionStorage.setItem('pr:access', 'pr-verify-me-token'); 'ok'",
    )

    // ---------- M2 /me 概览各块渲染 ----------
    await cdp.navigate(site.url + '/me')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('#me-display-name'))")),
      { label: '/me 概览渲染出昵称输入框', timeoutMs: 15000 },
    )
    const overview = await cdp.evaluate(`(() => {
      const heads = [...document.querySelectorAll('h1, h2')].map((h) => (h.textContent || '').trim())
      const tabs = [...document.querySelectorAll('nav.tabs [role=tab]')].map((b) => (b.textContent || '').trim())
      return {
        title: (document.querySelector('.head .title')?.textContent || '').trim(),
        heads,
        tabs,
        hasInput: Boolean(document.querySelector('#me-display-name')),
        publicHref: (() => {
          const a = [...document.querySelectorAll('.head a')].find((x) => (x.textContent || '').includes('公开主页'))
          return a ? a.getAttribute('href') || '' : ''
        })(),
      }
    })()`)
    evidence.overview = overview
    record('M2a /me 标题与三个分区 tab', overview.title === '我的管理台' && overview.tabs.join('/') === '概览/关系/设置', JSON.stringify(overview.tabs))
    record('M2b 概览有昵称输入框', overview.hasInput, String(overview.hasInput))
    record(
      'M2c 概览有「我的帖子」与「我的放映室」块',
      overview.heads.includes('我的帖子') && overview.heads.includes('我的放映室'),
      overview.heads.join(' / '),
    )
    record('M9 「查看我的公开主页」指向 /user/1', overview.publicHref === '/user/1', overview.publicHref)

    // ---------- M8 顶栏头像落点 ----------
    const headerHref = await cdp.evaluate(
      "(() => { const a = document.querySelector('header a.avatar'); return a ? a.getAttribute('href') : '' })()",
    )
    evidence.headerHref = headerHref
    record('M8 顶栏头像落点切到 /me', headerHref === '/me', headerHref)

    // ---------- M11 /me 上头像位点亮、分区全灭 ----------
    const navState = await cdp.evaluate(`(() => {
      const slot = document.querySelector('header .avatar-slot')
      const on = [...document.querySelectorAll('header nav .nav-btn.is-active')].length
      return { slotActive: slot ? slot.classList.contains('is-active') : false, activeSections: on }
    })()`)
    evidence.navState = navState
    record('M11 /me 上头像位点亮、分区图标全灭', navState.slotActive === true && navState.activeSections === 0, JSON.stringify(navState))

    // ---------- M7 未知 tab 回落概览 ----------
    await cdp.navigate(site.url + '/me?tab=bogus')
    await waitFor(
      async () => (await cdp.evaluate(READ_TAB)) !== '',
      { label: '未知 tab 也渲染出分区条', timeoutMs: 15000 },
    )
    const fallbackTab = await cdp.evaluate(READ_TAB)
    record('M7 未知 tab 回落「概览」', fallbackTab === '概览', fallbackTab)

    // ---------- M3 ?tab=relations 生效 ----------
    await cdp.navigate(site.url + '/me?tab=relations')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('nav.lists [role=tab]').length")) > 0,
      { label: '关系分区渲染出名单 tab', timeoutMs: 15000 },
    )
    const relTab = await cdp.evaluate(READ_TAB)
    const listTabs = await cdp.evaluate(
      "[...document.querySelectorAll('nav.lists [role=tab]')].map((b) => (b.textContent || '').trim())",
    )
    evidence.listTabs = listTabs
    record('M3a ?tab=relations 进页选中「关系」', relTab === '关系', relTab)
    record(
      'M3b 四份名单是 好友/关注/粉丝/黑名单',
      listTabs.join('/') === '好友/关注/粉丝/黑名单',
      listTabs.join(' / '),
    )

    // 名单行真的渲染出来（mock 的种子关注边保证互关名单非空）。
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('[data-testid=me-relation-rows] .row').length")) > 0,
      { label: '互关名单渲染出行', timeoutMs: 15000 },
    )
    const mutualNames = await cdp.evaluate(
      "[...document.querySelectorAll('[data-testid=me-relation-rows] .row .who strong')].map((e) => (e.textContent || '').trim())",
    )
    evidence.mutualNames = mutualNames
    record('M3c 互关名单列出人（mock 种子）', mutualNames.length > 0, mutualNames.join(' / '))

    // ---------- M4 ?list=blocked 生效 ----------
    await cdp.navigate(site.url + '/me?tab=relations&list=blocked')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('[data-testid=me-relation-rows] .row').length")) > 0,
      { label: '黑名单渲染出行', timeoutMs: 15000 },
    )
    const blockedProbe = await cdp.evaluate(`(() => {
      const sel = [...document.querySelectorAll('nav.lists [role=tab]')].find((b) => b.getAttribute('aria-selected') === 'true')
      const rows = [...document.querySelectorAll('[data-testid=me-relation-rows] .row')]
      return {
        selected: sel ? (sel.textContent || '').trim() : '',
        names: rows.map((r) => (r.querySelector('.who strong')?.textContent || '').trim()),
        actTexts: rows.length ? [...rows[0].querySelectorAll('.acts button')].map((b) => (b.textContent || '').trim()) : [],
      }
    })()`)
    evidence.blockedProbe = blockedProbe
    record('M4a ?list=blocked 选中「黑名单」', blockedProbe.selected === '黑名单', blockedProbe.selected)
    record(
      'M4b 黑名单行只给「解除拉黑」（没有关注类动作）',
      blockedProbe.actTexts.length === 1 && blockedProbe.actTexts[0] === '解除拉黑',
      JSON.stringify(blockedProbe.actTexts),
    )

    // ---------- M5 点名单 tab 写回 URL ----------
    await cdp.evaluate(`(() => {
      const t = [...document.querySelectorAll('nav.lists [role=tab]')].find((b) => (b.textContent || '').trim() === '粉丝')
      if (t) t.click()
      return true
    })()`)
    await waitFor(
      async () => (await cdp.evaluate('location.search')).includes('list=followers'),
      { label: '点「粉丝」后 URL 写入 list=followers', timeoutMs: 10000 },
    )
    const followersSearch = await cdp.evaluate('location.search')
    evidence.followersSearch = followersSearch
    record(
      'M5 点名单 tab 写回 URL（tab 与 list 同时在场）',
      followersSearch.includes('tab=relations') && followersSearch.includes('list=followers'),
      followersSearch,
    )

    // ---------- M6 ?tab=settings 槽位 ----------
    await cdp.navigate(site.url + '/me?tab=settings')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('#me-settings-head'))")),
      { label: '设置分区渲染', timeoutMs: 15000 },
    )
    const settings = await cdp.evaluate(`(() => {
      const sel = [...document.querySelectorAll('nav.tabs [role=tab]')].find((b) => b.getAttribute('aria-selected') === 'true')
      const body = (document.querySelector('.sect')?.textContent || '').trim()
      return { selected: sel ? (sel.textContent || '').trim() : '', mentionsPath: body.includes('/me?tab=settings') }
    })()`)
    evidence.settings = settings
    record('M6a ?tab=settings 选中「设置」', settings.selected === '设置', settings.selected)
    record('M6b 设置区说明里钉住 /me?tab=settings 地址', settings.mentionsPath, String(settings.mentionsPath))

    // ---------- M10 本人 /user/:id 变回纯橱窗 ----------
    await cdp.navigate(site.url + '/user/1')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('.guest-note'))")),
      { label: '本人档案页出现公开视角提示', timeoutMs: 15000 },
    )
    const selfPage = await cdp.evaluate(`(() => {
      const note = document.querySelector('.guest-note')
      const link = note ? note.querySelector('a') : null
      const heads = [...document.querySelectorAll('h1, h2')].map((h) => (h.textContent || '').trim())
      return {
        noteText: note ? (note.textContent || '').trim() : '',
        noteHref: link ? link.getAttribute('href') || '' : '',
        hasEditInput: Boolean(document.querySelector('#display-name, #me-display-name')),
        heads,
      }
    })()`)
    evidence.selfPage = selfPage
    record(
      'M10a 本人 /user/1 上只有公开视角提示 + 去管理台的链接',
      selfPage.noteText.includes('公开页') && selfPage.noteHref === '/me',
      JSON.stringify({ href: selfPage.noteHref }),
    )
    record(
      'M10b 本人 /user/1 上不再有昵称编辑（旧双视角已退役）',
      !selfPage.hasEditInput && !selfPage.heads.includes('修改昵称'),
      JSON.stringify({ hasEditInput: selfPage.hasEditInput, heads: selfPage.heads }),
    )

    // ---------- M12 源码级：键名唯一源 ----------
    const offenders = []
    for (const file of walk(join(REPO_ROOT, 'client', 'src'))) {
      const rel = file.slice(REPO_ROOT.length + 1).replace(/\\/g, '/')
      if (rel.endsWith('utils/meRoute.ts')) continue
      const src = stripComments(readFileSync(file, 'utf8'))
      if (/query\['tab'\]/.test(src) || /query\['list'\]/.test(src)) offenders.push(rel)
    }
    evidence.meQueryOffenders = offenders
    record('M12 tab / list 键名只在 utils/meRoute.ts 里出现', offenders.length === 0, JSON.stringify(offenders))
  } finally {
    try {
      await chrome.close()
    } catch {
      /* 关闭失败不影响判定 */
    }
    site.close()
  }

  console.log('')
  console.log('判据：' + passed + '/' + (passed + failed) + ' 通过')
  console.log('判定：' + (failed === 0 ? 'PASS' : 'FAIL'))
  console.log('证据：' + JSON.stringify(evidence, null, 2))
  process.exit(failed === 0 ? 0 : 1)
}

main().catch((err) => {
  console.error(err)
  process.exit(1)
})
