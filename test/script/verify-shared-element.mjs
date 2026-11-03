#!/usr/bin/env node
/**
 * `verify-shared-element.mjs` —— **共享元素 FLIP** 的真机验收（票 #78）。
 *
 * # 为什么必须是真机
 *
 * 共享元素是"两个矩形之间飞一块东西"。四件事只有真机才判得出来：
 *   1. 幽灵**真的**出现了，且它是 `position:fixed`、挂在 `<body>` 上（否则会被页面的
 *      transform/opacity 一起带走，看起来像"两个东西各抖一下"）；
 *   2. 它飞的是 **440ms**，而页面过渡同时是 **240ms** —— 两者**并行**，不是先飞完再换页；
 *   3. 真元素在飞行期间被藏起来（`visibility:hidden`）、飞完还原 —— 这是"飞的是矩形，
 *      不是共享组件实例"的可观测证据；
 *   4. 降级时**零等待**：目标缺失/源未解码/源零尺寸，一帧都不多等，页面照旧 240ms 位移。
 *
 * 纯单测（`client/test/shared-element.test.mjs`）只能证明**判定器**算对了，
 * 证明不了"那个判定真的变成了屏幕上的一个幽灵"。
 *
 * # 它在什么环境里跑
 *
 * **不需要后端，也不需要 Vite**：脚本自起静态服务器托管 `client/dist`，`/api/*` 一律回 404，
 * 前端因此走 **mock 域**。封面图用同源 `/demo/cover.svg`（`client/public/demo/`）——
 * mock 帖子此前 `images: []`，卡片封面这条渲染路径从来没被走过，也就没有可飞的东西。
 *
 * # 前置
 *
 *   npm --prefix client run build
 *   node test/script/verify-shared-element.mjs
 *
 * # 判据
 *
 *  S1  头像：`/friends?tab=followers` → `/user/2` 时出现幽灵，**440ms**，
 *      同一帧页面过渡（240ms）也在跑 ⇒ 并行；幽灵是 `<body>` 的直接子节点；
 *      幽灵上**没有** `data-shared`（它绝不能再当匹配目标）；
 *      目标头像飞行期间 `visibility:hidden`、飞完恢复 `visible`、幽灵消失。
 *  S2  帖封面：`/posts` → `/posts/1` 同样起飞（同一套契约，另一对端点）。
 *  S3  房间卡→房：**豁免优先** —— `/official` → `/room/MOON01` 决策是 exempt，
 *      零过渡、零幽灵（"卡→房内共享随之禁用"）。
 *  S4a 目标缺失：把源端的 key 改成目标页不存在的值 ⇒ 不飞，页面仍 240ms 位移。
 *  S4b 源零尺寸：把源元素宽高设 0 ⇒ 不飞（`beforeEach` 就把它丢了）。
 *  S4c 源未解码：阻断 `/demo/cover.svg` ⇒ 图 `naturalWidth === 0` ⇒ 不飞。
 *  S5  方向：同一对页面**后退**不飞（只有前进触发）。
 *  S6  reduced-motion：整条判死（零幽灵，且过渡名是 `page-none`）。
 *  S7  源码级：440 与 240 各自只有一个定义处；三处 `kind:key` 两端都在；
 *      幽灵只由接线层造；`data-shared` 不出现在任何样式里。
 *
 * 退出码：全 PASS → 0；任一 FAIL → 1。
 */
import { createServer } from 'node:http'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor, argOf } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9953'))

let passed = 0
let failed = 0

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
 * 静态服务器：托管 `client/dist`，SPA 回退 index.html，`/api/*` 与 `/ws` 回 404。
 *
 * `/api/*` 回 404 而不是 index.html：前端在 mock 态下本就不该打这些请求；
 * 真打了的话，把一段 HTML 当 JSON 解析会报出一个与判据无关的错，把真信号淹掉。
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
 * 换页并**逐帧**观测幽灵与页面过渡。
 *
 * 逐帧而不是等一个固定延时：幽灵只有 440ms 生命，且"页面过渡是否同时在场"这件事
 * 只在那一小段窗口里成立。取一帧快照会随机漏掉它们里的一个。
 *
 * ⚠ 这个表达式里**不能出现反引号**（它本身嵌在模板字面量里），一律用单引号拼接。
 */
const WATCH = `(async () => {
  const to = ${JSON.stringify('__TO__')}
  const key = ${JSON.stringify('__KEY__')}
  const stage = document.querySelector('[data-testid="route-stage"]')
  if (!stage) return { error: 'no-stage' }

  // 离场页里的同名元素要排除：同一篇帖的列表卡与详情头图会短暂同时在 DOM 里。
  const insideLeaving = (el) => {
    let n = el
    while (n) {
      if (/-(leave-active|leave-from|leave-to)/.test(String(n.className || ''))) return true
      n = n.parentElement
    }
    return false
  }
  const entering = () => {
    const out = []
    for (const el of document.querySelectorAll('[data-shared="' + key + '"]')) {
      if (!insideLeaving(el)) out.push(el)
    }
    return out
  }

  const t0 = performance.now()
  history.pushState({}, '', to)
  window.dispatchEvent(new PopStateEvent('popstate'))

  let ghostAt = null
  let ghostDuration = null
  let ghostFrom = null
  let ghostTo = null
  let ghostShared = null
  let ghostInBody = null
  let ghostFrameHadPage = null
  let ghostTag = null
  let ghostText = null
  const pageDurations = []
  let classesSeen = null
  let presentDuring = null
  let visDuring = null
  let pageDuringGhost = null
  let targetHiddenEver = false
  let hiddenWho = null
  let ghostFramesWithPage = 0

  for (let i = 0; i < 60; i++) {
    await new Promise((r) => requestAnimationFrame(r))
    const elapsed = performance.now() - t0

    let hasPage = false
    for (const el of stage.querySelectorAll('*')) {
      for (const a of el.getAnimations()) {
        // 只数 CSS transition：落地页那几条常驻循环动画是 CSSAnimation，与本判据无关。
        if (a.constructor.name !== 'CSSTransition') continue
        const timing = a.effect && a.effect.getComputedTiming ? a.effect.getComputedTiming() : {}
        const d = timing.duration
        if (typeof d === 'number' && d > 0) {
          hasPage = true
          if (pageDurations.indexOf(d) < 0) pageDurations.push(d)
        }
      }
    }

    if (!classesSeen) {
      const cls = [...stage.children].map((c) => String(c.className || '')).join(' ')
      const m = cls.match(/page-(forward|back|lateral|none)/)
      if (m) classesSeen = m[1]
    }

    const g = document.querySelector('.shared-ghost')
    // 幽灵在场且页面过渡同时活着的帧数：判"并行"要的是**整段里重叠过**，
    // 而不是恰好幽灵出现那一帧（两者起跑只差一两帧，取单帧会假红）。
    if (g && hasPage) ghostFramesWithPage = (ghostFramesWithPage || 0) + 1
    // 这一对的目标被藏起来了没有（接线层的 hideElement 用的是 visibility）。
    {
      const t = entering()[0] || null
      if (t && getComputedStyle(t).visibility === 'hidden') {
        targetHiddenEver = true
        if (!hiddenWho) hiddenWho = String(t.className || t.tagName)
      }
    }
    if (g && ghostAt === null) {
      ghostAt = elapsed
      ghostFrameHadPage = hasPage
      ghostInBody = g.parentElement === document.body
      ghostShared = g.querySelectorAll('[data-shared]').length
      // 幽灵里面装的是谁：帖封面克隆出来是 <img>，头像是 <a>/<span> 字母块。
      // 判"某一对没飞"必须看这个 —— 一次换页里可能有另一对正常起飞。
      ghostTag = g.firstElementChild ? String(g.firstElementChild.tagName).toLowerCase() : ''
      ghostText = String(g.textContent || '').trim().slice(0, 12)
      const anims = g.getAnimations()
      if (anims.length > 0) {
        const a = anims[0]
        ghostDuration = a.effect && a.effect.getComputedTiming
          ? a.effect.getComputedTiming().duration
          : null
        const frames = a.effect && a.effect.getKeyframes ? a.effect.getKeyframes() : []
        if (frames.length > 0) ghostFrom = String(frames[0].transform || '')
        if (frames.length > 0) ghostTo = String(frames[frames.length - 1].transform || '')
      }
      const t = entering()[0] || null
      presentDuring = t !== null
      visDuring = t ? getComputedStyle(t).visibility : null
    }
  }

  const tAfter = entering()[0] || null
  return {
    path: location.pathname,
    ghostAt: ghostAt,
    ghostDuration: ghostDuration,
    ghostFrom: ghostFrom,
    ghostTo: ghostTo,
    ghostShared: ghostShared,
    ghostInBody: ghostInBody,
    ghostTag: ghostTag,
    ghostText: ghostText,
    ghostFrameHadPage: ghostFrameHadPage,
    ghostFramesWithPage: ghostFramesWithPage,
    targetHiddenEver: targetHiddenEver,
    hiddenWho: hiddenWho,
    ghostGone: document.querySelector('.shared-ghost') === null,
    pageDurations: pageDurations,
    classesSeen: classesSeen,
    presentDuring: presentDuring,
    visDuring: visDuring,
    presentAfter: tAfter !== null,
    visAfter: tAfter ? getComputedStyle(tAfter).visibility : null,
  }
})()`

/** 去注释再判（注释里提到这些名字是允许的，直接 grep 会假红）。 */
function stripComments(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/.*$/gm, '$1')
}

function walkSource(dir, acc = []) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) walkSource(full, acc)
    else if (/\.(vue|ts|css)$/.test(name)) acc.push(full)
  }
  return acc
}

/**
 * 登录态种子（与 verify-friends / verify-avatar 同一份形状）。
 *
 * 没有它好友页渲染的是登录引导，名单行根本不存在 —— 也就没有一个 `avatar:<id>`
 * 的源元素可量。S1 那组一开始连"幽灵 null"都拿不到，根因就在这里，不在接线层。
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

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'shared-element')
  const cdp = await openTarget(chrome.port, 'shared-element')

  async function watch(to, key) {
    const expr = WATCH.replace('__TO__', to).replace('__KEY__', key)
    return cdp.evaluate(expr, 30000)
  }

  async function waitPath(path) {
    await waitFor(async () => (await cdp.evaluate('location.pathname')) === path, {
      label: '到达 ' + path,
      timeoutMs: 10000,
    })
  }

  /** 打开一个 URL 并等页面稳（等布局、字体、图片都有机会落定）。 */
  async function open(path) {
    await cdp.navigate(site.url + path)
    await waitPath(path.split('?')[0])
    await sleep(500)
  }

  try {
    // ---------------- 种登录态 ----------------
    // 先打开一次站点拿到同源环境（localStorage 是**按 origin** 的，白页上写不进去）。
    await cdp.navigate(site.url + '/posts')
    await sleep(400)
    await cdp.evaluate(
      "localStorage.setItem('pr:profile', " +
        JSON.stringify(JSON.stringify(SEED_PROFILE)) +
        "); sessionStorage.setItem('pr:access', 'pr-verify-shared-token'); 'ok'",
      )
    // 种完必须整页重载一次：`stores/auth.ts` 在应用启动时读一次存储，
    // 不重载的话内存里还是游客态（这一点和 verify-friends 的写法一致）。
    await cdp.navigate(site.url + '/posts')
    await sleep(600)

    // ---------------- S1 头像：/friends → /user/2 ----------------
    {
      await open('/friends?tab=followers')
      const r = await watch('/user/2', 'avatar:2')
      await waitPath('/user/2')
      record(
        'S1 头像共享元素：前进时出现幽灵',
        r.ghostAt !== null,
        '第 ' + (r.ghostAt === null ? '—' : Math.round(r.ghostAt)) + 'ms 出现；' +
          '页面过渡时长 ' + JSON.stringify(r.pageDurations),
      )
      record(
        'S1b 幽灵飞 440ms，且与页面 240ms 过渡并行',
        r.ghostDuration === 440 && r.pageDurations.indexOf(240) >= 0 && r.ghostFramesWithPage > 0,
        '幽灵 ' + r.ghostDuration + 'ms / 页面 ' + JSON.stringify(r.pageDurations) +
          ' / 同时在场帧数 ' + r.ghostFramesWithPage,
      )
      record(
        'S1c 幽灵挂在 <body> 上（与页面 transform/opacity 解耦）',
        r.ghostInBody === true,
        'body 直接子节点 = ' + r.ghostInBody,
      )
      record(
        'S1d 不共享组件实例：真元素还在、只是飞行期间被藏起来，飞完还原',
        r.presentDuring === true && r.visDuring === 'hidden' &&
          r.presentAfter === true && r.visAfter === 'visible' && r.ghostGone === true,
        '飞行中 present=' + r.presentDuring + ' visibility=' + r.visDuring +
          ' / 结束后 present=' + r.presentAfter + ' visibility=' + r.visAfter +
          ' / 幽灵已清 = ' + r.ghostGone,
      )
      record(
        'S1e 幽灵上不带 data-shared（它不能再成为匹配目标）',
        r.ghostShared === 0,
        '幽灵内的 data-shared 数 = ' + r.ghostShared,
      )
      record(
        'S1f 起点是源矩形、终点是目标矩形（矩阵里有平移与缩放）',
        typeof r.ghostFrom === 'string' && /translate\(/.test(r.ghostFrom) &&
          typeof r.ghostTo === 'string' && /translate\(/.test(r.ghostTo) &&
          /scale\(/.test(r.ghostTo),
        '首帧 ' + r.ghostFrom + ' → 末帧 ' + r.ghostTo,
      )
    }

    // ---------------- S5 方向：同一对页面后退不飞 ----------------
    {
      const r = await watch('/friends?tab=followers', 'avatar:2')
      await waitPath('/friends')
      record(
        'S5 后退不飞（只有前进触发）',
        r.ghostAt === null && r.classesSeen === 'back',
        '幽灵 ' + r.ghostAt + ' / 过渡名 ' + r.classesSeen,
      )
    }

    // ---------------- S2 帖封面：/posts → /posts/1 ----------------
    {
      await open('/posts')
      const hasCover = await cdp.evaluate(
        "document.querySelectorAll('[data-shared^=\"post-cover:\"]').length",
      )
      const r = await watch('/posts/1', 'post-cover:1')
      await waitPath('/posts/1')
      record(
        'S2 帖封面共享元素：卡片封面 → 详情头图',
        Number(hasCover) > 0 && r.ghostAt !== null && r.ghostDuration === 440,
        '列表里的封面数 = ' + hasCover + ' / 幽灵 ' + r.ghostDuration + 'ms / 第 ' +
          (r.ghostAt === null ? '—' : Math.round(r.ghostAt)) + 'ms 出现',
      )
    }

    // ---------------- S3 房间卡→房：豁免优先 ----------------
    {
      await open('/official')
      const hasCard = await cdp.evaluate(
        "document.querySelectorAll('[data-shared^=\"room:\"]').length",
      )
      const r = await watch('/room/MOON01', 'room:MOON01')
      await waitPath('/room/MOON01')
      record(
        'S3 房间卡→房：两端契约都在，但房页豁免优先 ⇒ 零过渡、零幽灵',
        Number(hasCard) > 0 && r.classesSeen === 'none' &&
          r.ghostAt === null && r.pageDurations.length === 0,
        '卡片数 ' + hasCard + ' / 过渡名 ' + r.classesSeen +
          ' / 幽灵 ' + r.ghostAt + ' / 页面过渡 ' + JSON.stringify(r.pageDurations),
      )
    }

    // ---------------- S4a 目标缺失：改掉源端 key ----------------
    {
      await open('/friends?tab=followers')
      const renamed = await cdp.evaluate(
        "(() => { const el = document.querySelector('[data-shared=\"avatar:2\"]');" +
          "if (!el) return 0; el.setAttribute('data-shared', 'avatar:4242'); return 1 })()",
      )
      const r = await watch('/user/2', 'avatar:2')
      await waitPath('/user/2')
      record(
        'S4a 目标缺失 ⇒ 零等待降级：不飞，页面仍是 240ms 位移',
        Number(renamed) === 1 && r.ghostAt === null &&
          r.pageDurations.indexOf(240) >= 0 && r.classesSeen === 'forward',
        '改名 ' + renamed + ' / 幽灵 ' + r.ghostAt + ' / 页面 ' +
          JSON.stringify(r.pageDurations) + ' / 过渡名 ' + r.classesSeen,
      )
    }

    // ---------------- S4b 源零尺寸 ----------------
    {
      await open('/friends?tab=followers')
      const zeroed = await cdp.evaluate(
        "(() => { const els = document.querySelectorAll('[data-shared=\"avatar:2\"]');" +
          "if (els.length === 0) return { n: 0 };" +
          // 用 display:none 而不是 width/height:0 —— 头像根元素带着 Vue 的 :style
          // （boxStyle 里有宽高），导航前只要组件重渲染一次，inline 宽高就被写回去，
          // 于是"清除 × 1"会变成"没清"。display 不在 boxStyle 里，不会被覆盖。
          'for (const el of els) el.style.display = \'none\';' +
          'const b = els[0].getBoundingClientRect();' +
          'return { n: els.length, w: b.width, h: b.height } })()',
      )
      const r = await watch('/user/2', 'avatar:2')
      await waitPath('/user/2')
      record(
        'S4b 源零尺寸 ⇒ 不飞（beforeEach 就丢掉它），页面照旧 240ms',
        zeroed && zeroed.n === 1 && zeroed.w === 0 && zeroed.h === 0 &&
          r.targetHiddenEver === false && r.pageDurations.indexOf(240) >= 0,
        '清零 ' + JSON.stringify(zeroed) + ' / 这一对的目标被藏起来过 = ' + r.targetHiddenEver +
          '（' + r.hiddenWho + '） / 幽灵 = ' + r.ghostTag + ' "' + r.ghostText + '" / 页面 ' +
          JSON.stringify(r.pageDurations),
      )
    }

    // ---------------- S4c 源未解码（阻断封面图）----------------
    {
      await cdp.send('Network.enable')
      await cdp.send('Network.setBlockedURLs', { urls: ['*/demo/cover.svg'] })
      await open('/posts')
      const broken = await cdp.evaluate(
        "(() => { const img = document.querySelector('[data-shared^=\"post-cover:\"]');" +
          'if (!img) return null; return { complete: img.complete, naturalWidth: img.naturalWidth } })()',
      )
      const r = await watch('/posts/1', 'post-cover:1')
      await waitPath('/posts/1')
      await cdp.send('Network.setBlockedURLs', { urls: [] })
      record(
        'S4c 源图未解码（naturalWidth=0）⇒ 不飞，页面照旧 240ms',
        broken !== null && broken.naturalWidth === 0 && r.targetHiddenEver === false &&
          r.pageDurations.indexOf(240) >= 0,
        '源图 ' + JSON.stringify(broken) + ' / 这一对的目标被藏起来过 = ' + r.targetHiddenEver +
          ' / 页面 ' + JSON.stringify(r.pageDurations),
      )
    }

    // ---------------- S6 reduced-motion 整条判死 ----------------
    {
      await cdp.send('Emulation.setEmulatedMedia', {
        features: [{ name: 'prefers-reduced-motion', value: 'reduce' }],
      })
      await open('/friends?tab=followers')
      const r = await watch('/user/2', 'avatar:2')
      await waitPath('/user/2')
      await cdp.send('Emulation.setEmulatedMedia', { features: [] })
      record(
        'S6 reduced-motion：零幽灵，且过渡名是 page-none（JS 侧也判死）',
        r.ghostAt === null && r.classesSeen === 'none' && r.pageDurations.length === 0,
        '幽灵 ' + r.ghostAt + ' / 过渡名 ' + r.classesSeen +
          ' / 页面过渡 ' + JSON.stringify(r.pageDurations),
      )
    }

    // ---------------- S7 源码级 ----------------
    {
      const shared = stripComments(
        readFileSync(join(REPO_ROOT, 'client', 'src', 'utils', 'sharedElement.ts'), 'utf8'),
      )
      const morphDefs = [...shared.matchAll(/MORPH_DURATION_MS\s*=\s*(\d+)/g)].map((m) => Number(m[1]))
      const route = stripComments(
        readFileSync(join(REPO_ROOT, 'client', 'src', 'utils', 'routeTransition.ts'), 'utf8'),
      )
      const pageDefs = [...route.matchAll(/TRANSITION_DURATION_MS\s*=\s*(\d+)/g)].map((m) =>
        Number(m[1]),
      )
      const repoShared = [...shared.matchAll(/MORPH_DURATION_MS\s*=\s*(\d+)/g)].length
      record(
        'S7 440 与 240 各自只有一个定义处',
        morphDefs.length === 1 && morphDefs[0] === 440 && pageDefs.length === 1 && pageDefs[0] === 240,
        'morph=' + JSON.stringify(morphDefs) + ' page=' + JSON.stringify(pageDefs) +
          ' / sharedElement 内定义 ' + repoShared + ' 处',
      )

      const files = walkSource(join(REPO_ROOT, 'client', 'src'))
      const sources = new Map(files.map((f) => [f, stripComments(readFileSync(f, 'utf8'))]))
      const pairs = [
        ['post-cover:', 'pages/PostsPage.vue', 'pages/PostDetailPage.vue'],
        ['avatar:', 'components/ui/UserAvatar.vue', 'pages/UserPage.vue'],
        ['room:', 'components/features/rooms/RoomCard.vue', 'pages/RoomPage.vue'],
      ]
      const missing = []
      for (const [prefix, fromTail, toTail] of pairs) {
        const from = files.find((f) => f.replace(/\\/g, '/').endsWith(fromTail))
        const to = files.find((f) => f.replace(/\\/g, '/').endsWith(toTail))
        if (!from || !to) {
          missing.push(prefix + '（文件缺失）')
          continue
        }
        if (!sources.get(from).includes(prefix)) missing.push(prefix + '@' + fromTail)
        if (!sources.get(to).includes(prefix)) missing.push(prefix + '@' + toTail)
      }
      record(
        'S7b 三处契约两端都在（帖封面 / 头像 / 房间卡）',
        missing.length === 0,
        missing.length === 0 ? '三对齐全' : '缺 ' + JSON.stringify(missing),
      )

      const ghostMakers = files.filter((f) => sources.get(f).includes("'shared-ghost'"))
      record(
        'S7c 幽灵只有接线层会造（组件里没有第二份实现）',
        ghostMakers.length === 1 &&
          ghostMakers[0].replace(/\\/g, '/').endsWith('composables/useSharedElement.ts'),
        '造幽灵的文件 ' + JSON.stringify(ghostMakers.map((f) => f.replace(REPO_ROOT, ''))),
      )

      const cssWithShared = files
        .filter((f) => f.endsWith('.css'))
        .filter((f) => sources.get(f).includes('data-shared'))
      record(
        'S7d 样式里不出现 data-shared（契约只在标记里，不在选择器里）',
        cssWithShared.length === 0,
        JSON.stringify(cssWithShared.map((f) => f.replace(REPO_ROOT, ''))),
      )
    }
  } catch (err) {
    record('脚本执行', false, String(err && err.stack ? err.stack : err))
  } finally {
    cdp.close()
    chrome.close()
    site.close()
  }

  console.log('')
  console.log('通过 ' + passed + ' / 失败 ' + failed)
  console.log(failed === 0 ? '判定：PASS' : '判定：FAIL')
  process.exit(failed === 0 ? 0 : 1)
}

void main()
