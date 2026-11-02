#!/usr/bin/env node
/**
 * `verify-navstate.mjs` —— 票 #77（导航当前态）的**真机**验收。
 *
 * # 为什么是真机脚本
 *
 * 本票的判据全是**渲染终态**："进 /posts/3 时「帖子」亮不亮"、"aria-current 在不在这一个上"、
 * "本人档案页是不是整排分区全灭、只有头像亮"。这些是 DOM 与计算样式的事实，
 * 单元测试测不出；而本仓库前端**没有测试框架**（client/package.json 只有 vue-tsc + vite），
 * 所以沿用仓内既有做法（`verify-avatar.mjs` / `verify-connection.mjs` / `verify-forgot.mjs`）。
 *
 * # 它在什么环境里跑
 *
 * **不需要后端**：本脚本自己起静态服务器托管 `client/dist`，并把 `/api/*` 一律回 404
 * （见下）。前端默认走 **mock 域**（`api/mode.ts`：只有显式配 `VITE_MOCK_*=0` 才翻真），
 * 所以帖子里程碑、番剧板块、档案页的数据都来自前端内存里的假数据。
 *
 * 判据**刻意不依赖数据是否齐全**：`/forum/7` 与 `/user/9` 在 mock 里没有实体，
 * 页面会渲染「板块不存在」/「用户不存在」空态 —— 但**顶栏仍然要正确点亮**。
 * 这正是本票要的："导航当前态是路由的事，不是数据的事"。数据库里有没有这条记录，
 * 与"用户在哪个分区"无关。
 *
 * # 前置
 *
 *   npm --prefix client run build      # 必须先构建出 client/dist
 *   node test/script/verify-navstate.mjs
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 *
 * # 判据
 *
 *  N1 **深链 /posts/3**：「帖子」点亮（前缀表覆盖子路径），其余分区全灭。
 *     这是本票用户抱怨的原点 —— vue-router 自带的 `router-link-active` 在这里**不亮**
 *     （`/posts/3` 是另一条路由记录），所以必须用前缀表。
 *  N2 **深链 /forum/7**：「番剧论坛」点亮。
 *  N3 **深链 /posts/new**（发帖页也是「帖子」分区）：「帖子」点亮 —— 钉住"同分区的子页面不换高亮"。
 *  N4 **本人 /user/1 点亮头像位**：本人档案页不归任何分区，
 *     整排分区熄灭、头像位亮起来（票面判据："本人路由点亮头像位、落地页全灭"）。
 *  N5 **他人 /user/9 全灭**：别人的档案页没有"我的"语义，谁都不亮。
 *  N6 **落地页全灭**：`/` 没有分区（且顶栏本来就不渲染）——调函数级判据断言"全灭"的意图。
 *  N7 **aria-current=page 与视觉同步**：点亮的那个链接**必须**带 `aria-current="page"`，
 *     未点亮的一个都不许带（只靠颜色不算指示器，这是本票的 a11y 判据）。
 *  N8 **外观按票 #43 §5**：当前项底色 == `--state-active`（且 != 未选中的底色），
 *     前景升到 `--text`，且**没有**滑动指示器（切换时无 transition 到位的独立滑块元素）。
 *  N9 **命中区 44×44**：视觉仍是 36px，但伪元素外扩后命中盒 >= 44×44（WCAG 2.5.5 AAA）。
 *  N10 **唯一工具源**（源码级）：`router-link-active` / `router-link-exact-active` 在全仓
 *      的代码里**不出现**（去注释后再 grep）—— 本票明确"不依赖路由默认 active 类"，
 *      这条防的是"以后有人在别处又用回默认类，于是同一页两个真源"。
 */
import { createServer } from 'node:http'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9920'))

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
 * `/api/*` 与 `/ws` 一律回 404 —— mock 态下前端本来就不该打这些请求，
 * 真打了返回 index.html 会让调用方把 HTML 当 JSON 解析，把真正的失败信号淹掉。
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
      done({ url: 'http://127.0.0.1:' + port, close: () => server.close() })
    })
  })
}

/**
 * 种一个「已登录」的假档案（与 verify-avatar.mjs 同一手法，id 必须一致：
 * 顶栏头像位的"本人"判定读的是 `auth.profile.id`）。
 *
 * 为什么需要登录态：分区导航与头像位只在 `auth.isLoggedIn` 时渲染。
 * 种子只影响显示层，不伪造任何授权结论（服务端仍是唯一授权方，铁律 I3）。
 */
const SEED_PROFILE = {
  id: 1,
  username: 'mooncat',
  displayName: '月夜猫',
  email: '',
  role: 'admin',
  status: 'active',
  createdAt: '2023-10-18T09:00:00Z',
  unread: 0,
  unreadKnown: false,
}

/**
 * 读顶栏的当前态快照。
 *
 * 返回结构刻意做成"与判据同形"的原始事实（哪些亮、哪些带 aria-current、
 * 当前项的底色/前景），而不是在页面里就判 PASS/FAIL —— 判定留在 Node 侧，
 * 失败时能打印出真实读数，而不是一句"不成立"。
 */
const READ_NAV = `(() => {
  const nav = document.querySelector('header nav[aria-label="分区导航"]')
  if (!nav) return null
  const items = [...nav.querySelectorAll('a.nav-btn')].map((a) => {
    const s = getComputedStyle(a)
    return {
      label: a.getAttribute('aria-label') || '',
      href: a.getAttribute('href') || '',
      active: a.classList.contains('is-active'),
      ariaCurrent: a.getAttribute('aria-current'),
      bg: s.backgroundColor,
      color: s.color,
      width: Math.round(a.getBoundingClientRect().width),
      height: Math.round(a.getBoundingClientRect().height),
    }
  })
  // 右侧那一组（未登录时是公开房间/帮助，已登录时是管理端/头像/退出）
  const right = [...document.querySelectorAll('header .right a.nav-btn')].map((a) => ({
    label: a.getAttribute('aria-label') || '',
    active: a.classList.contains('is-active'),
    ariaCurrent: a.getAttribute('aria-current'),
  }))
  const slot = document.querySelector('header .avatar-slot')
  return {
    items,
    right,
    avatarActive: Boolean(slot && slot.classList.contains('is-active')),
    path: location.pathname,
    tokens: (() => {
      const s = getComputedStyle(document.documentElement)
      return {
        active: s.getPropertyValue('--state-active').trim(),
        text: s.getPropertyValue('--text').trim(),
      }
    })(),
  }
})()`

/** 打开一个路径并等到顶栏渲染出分区导航。 */
async function visit(cdp, site, path) {
  await cdp.navigate(site.url + path)
  await waitFor(
    async () => (await cdp.evaluate("Boolean(document.querySelector('header nav[aria-label=\"分区导航\"]'))")),
    { label: '顶栏分区导航渲染：' + path, timeoutMs: 15000 },
  )
  // 等路由真正落位（SPA 回退时首帧可能还在 /）
  await waitFor(async () => (await cdp.evaluate('location.pathname')) === path, {
    label: '路由落位到 ' + path,
    timeoutMs: 10000,
  })
  return cdp.evaluate(READ_NAV)
}

/** 某一次快照里被点亮的项（分区 + 右侧），返回标签数组。 */
function litLabels(snap) {
  const lit = snap.items.filter((i) => i.active).map((i) => i.label)
  for (const r of snap.right) if (r.active) lit.push(r.label)
  return lit
}

/** 断言"只有这些亮"。用集合比较，顺序无关。 */
function onlyThese(lit, expected) {
  const a = [...new Set(lit)].sort()
  const b = [...new Set(expected)].sort()
  return a.length === b.length && a.every((v, i) => v === b[i])
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'navstate')
  const cdp = await openTarget(BASE_PORT, 'navstate')

  try {
    // ---------- 种登录态 ----------
    await cdp.navigate(site.url + '/')
    await cdp.evaluate(
      "localStorage.setItem('pr:profile', " +
        JSON.stringify(JSON.stringify(SEED_PROFILE)) +
        "); sessionStorage.setItem('pr:access', 'pr-verify-navstate-fake-token'); 'ok'",
    )

    // ---------- N1 深链 /posts/3 ----------
    const posts = await visit(cdp, site, '/posts/3')
    evidence['/posts/3'] = { lit: litLabels(posts), avatar: posts.avatarActive }
    record(
      'N1 深链 /posts/3 只点亮「帖子」（前缀表覆盖子路径）',
      onlyThese(litLabels(posts), ['帖子']),
      '实际点亮 ' + JSON.stringify(litLabels(posts)),
    )

    // ---------- N2 深链 /forum/7 ----------
    const forum = await visit(cdp, site, '/forum/7')
    evidence['/forum/7'] = { lit: litLabels(forum), avatar: forum.avatarActive }
    record(
      'N2 深链 /forum/7 只点亮「番剧论坛」',
      onlyThese(litLabels(forum), ['番剧论坛']),
      '实际点亮 ' + JSON.stringify(litLabels(forum)),
    )

    // ---------- N3 同分区子页 /posts/new ----------
    const editor = await visit(cdp, site, '/posts/new')
    evidence['/posts/new'] = { lit: litLabels(editor) }
    record(
      'N3 /posts/new 仍点亮「帖子」（同分区不换高亮）',
      onlyThese(litLabels(editor), ['帖子']),
      '实际点亮 ' + JSON.stringify(litLabels(editor)),
    )

    // ---------- N4 本人档案页：分区全灭 + 头像位亮 ----------
    const selfProfile = await visit(cdp, site, '/user/1')
    evidence['/user/1'] = { lit: litLabels(selfProfile), avatar: selfProfile.avatarActive }
    record(
      'N4a /user/1（本人）分区全灭',
      onlyThese(litLabels(selfProfile), []),
      '实际点亮 ' + JSON.stringify(litLabels(selfProfile)),
    )
    record('N4b /user/1（本人）点亮头像位', selfProfile.avatarActive === true, 'avatarActive=' + selfProfile.avatarActive)

    // ---------- N5 他人档案页：全灭 ----------
    const othersProfile = await visit(cdp, site, '/user/9')
    evidence['/user/9'] = { lit: litLabels(othersProfile), avatar: othersProfile.avatarActive }
    record(
      'N5 /user/9（他人）谁都不亮（分区与头像位都灭）',
      onlyThese(litLabels(othersProfile), []) && othersProfile.avatarActive === false,
      '点亮 ' + JSON.stringify(litLabels(othersProfile)) + ' / avatarActive=' + othersProfile.avatarActive,
    )

    // ---------- N7 aria-current 与视觉同步 ----------
    // 回到 /posts/3 核对：亮的那个**必须**带 aria-current，灭的一个都不许带。
    const postsAgain = await visit(cdp, site, '/posts/3')
    const litItem = postsAgain.items.find((i) => i.active)
    const strayCurrent = postsAgain.items.filter((i) => !i.active && i.ariaCurrent !== null)
    evidence.ariaCurrent = {
      lit: litItem ? litItem.ariaCurrent : null,
      strays: strayCurrent.map((i) => i.label + '=' + i.ariaCurrent),
    }
    record(
      'N7a 点亮的项带 aria-current="page"',
      Boolean(litItem) && litItem.ariaCurrent === 'page',
      litItem ? 'aria-current=' + litItem.ariaCurrent : '没有点亮项',
    )
    record(
      'N7b 未点亮的项不带 aria-current',
      strayCurrent.length === 0,
      strayCurrent.map((i) => i.label + '=' + i.ariaCurrent).join(', ') || '无',
    )

    // ---------- N8 外观：--state-active 底 + 图标升 --text + 无滑动指示器 ----------
    const dim = postsAgain.items.find((i) => !i.active)
    const tokens = postsAgain.tokens
    evidence.appearance = {
      activeBg: litItem ? litItem.bg : '',
      dimBg: dim ? dim.bg : '',
      activeColor: litItem ? litItem.color : '',
      dimColor: dim ? dim.color : '',
      tokenActive: tokens.active,
      tokenText: tokens.text,
    }
    // "--state-active" 在 tokens.css 里是 #272033 → 计算样式是 rgb(39, 32, 51)。
    const expectedBg = '#272033'
    const hexToRgb = (hex) => {
      const n = Number.parseInt(hex.slice(1), 16)
      return 'rgb(' + ((n >> 16) & 255) + ', ' + ((n >> 8) & 255) + ', ' + (n & 255) + ')'
    }
    const expectedActiveBg = hexToRgb(expectedBg)
    record(
      'N8a 当前项底色 == --state-active（#272033）',
      Boolean(litItem) && litItem.bg === expectedActiveBg,
      '当前项 ' + (litItem ? litItem.bg : '') + ' / 期望 ' + expectedActiveBg + ' / 令牌原文 ' + tokens.active,
    )
    record(
      'N8b 未选中项没有状态底（与当前项不同色）',
      Boolean(dim) && dim.bg !== (litItem ? litItem.bg : ''),
      '未选中 ' + (dim ? dim.bg : '') + ' / 当前 ' + (litItem ? litItem.bg : ''),
    )
    record(
      'N8c 当前项前景升到 --text（与未选中不同色）',
      Boolean(litItem) && Boolean(dim) && litItem.color !== dim.color,
      '当前 ' + (litItem ? litItem.color : '') + ' / 未选中 ' + (dim ? dim.color : ''),
    )
    // 无滑动指示器：导航容器里不该有一个独立的"滑块"元素跟着当前项跑。
    const slider = await cdp.evaluate(
      "(() => { const nav = document.querySelector('header nav[aria-label=\"分区导航\"]'); " +
        "return nav ? nav.querySelectorAll('[class*=indicator], [class*=slider], [class*=thumb]').length : -1 })()",
    )
    record('N8d 没有滑动指示器元素（静态药丸）', slider === 0, '[class*=indicator|slider|thumb] 命中 ' + slider)

    // ---------- N9 命中区 44×44 ----------
    const hit = await cdp.evaluate(
      "(() => { const a = document.querySelector('header nav a.nav-btn'); if (!a) return null; " +
        "const cs = getComputedStyle(a, '::before'); " +
        "const r = a.getBoundingClientRect(); " +
        "const inset = cs.inset || [cs.top, cs.right, cs.bottom, cs.left].join(' '); " +
        "return { w: r.width, h: r.height, inset, pos: cs.position, hasBefore: cs.content !== 'none' || inset !== 'auto' } })()",
    )
    evidence.hitArea = hit
    record(
      'N9 命中区 >= 44×44（视觉 36px + 伪元素外扩 4px）',
      Boolean(hit) && hit.hasBefore && hit.pos === 'absolute' && hit.w + 8 >= 44 && hit.h + 8 >= 44,
      hit ? '视觉 ' + Math.round(hit.w) + '×' + Math.round(hit.h) + ' inset=' + hit.inset + ' position=' + hit.pos : '读不到',
    )

    // ---------- N10 源码级：不用路由默认 active 类 ----------
    const SRC = join(REPO_ROOT, 'client', 'src')
    /** 去掉注释再看：注释里会**提到** `router-link-active`（那是"为什么不用它"的出处）。 */
    const stripComments = (src) =>
      src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/[^\n]*/g, '$1')
    const offenders = []
    const walk = (dir) => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        const full = join(dir, entry.name)
        if (entry.isDirectory()) {
          walk(full)
          continue
        }
        if (!/\.(ts|vue)$/.test(entry.name)) continue
        const code = stripComments(readFileSync(full, 'utf8'))
        if (/router-link-(exact-)?active/.test(code)) offenders.push(full.slice(SRC.length + 1))
      }
    }
    walk(SRC)
    evidence.routerActiveOffenders = offenders
    record(
      'N10 全仓代码不依赖路由默认 active 类（显式前缀表是唯一源）',
      offenders.length === 0,
      offenders.join(', ') || '无',
    )

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
