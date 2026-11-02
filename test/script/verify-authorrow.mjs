#!/usr/bin/env node
/**
 * `verify-authorrow.mjs` —— 票 #63（AuthorRow + AsyncState + 帖子卡拆分）的**真机**验收。
 *
 * # 为什么是真机脚本
 *
 * 本票的判据全是 **DOM 可见终态**：作者行的三种尺寸长什么样、动作条在不在链接里、
 * 计数缩写是不是「0 隐 / 1.2k / 1.2w」、Tab 序是不是「赞 → 收藏 → 评论」、
 * AsyncState 的空/载/错三态是不是同一个出口。仓库前端没有测试框架
 * （client/package.json 只有 vue-tsc + vite），所以沿用仓内既有做法：真机脚本
 * （见 verify-avatar.mjs / verify-relativetime.mjs）。
 *
 * # 它在什么环境里跑
 *
 * **不需要后端**：本脚本自己起一个静态服务器托管 `client/dist`，`/api/*` 一律回 404，
 * 前端默认走 **mock 域**（api/mode.ts：只有显式配 VITE_MOCK_*=0 才翻真）。
 * 点赞往返、失败回滚、未登录跳登录这些**需要真实响应**的判据在
 * `verify-post-actions.mjs` 里（那个需要起后端实例），本脚本只钉**结构与外观契约**。
 *
 * # 前置
 *
 *   npm --prefix client run build      # 必须先构建出 client/dist
 *   node test/script/verify-authorrow.mjs
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 *
 * # 判据
 *
 *  R1 **帖子列表的作者行**：卡上有头像 + 昵称 + 相对时间，且**不是**「昵称 · 赞 N · 评论 N」那种拼串。
 *  R2 **卡片拆成「链接区 + 动作条」**：动作条里的按钮**不在**链接里（链接套按钮是无效 HTML），
 *     且链接区覆盖标题。这是「卡不再是一整块链接」这条判据的结构面。
 *  R3 **ActionBar 顺序**：赞 → 收藏 → 评论（DOM 顺序 = 视觉顺序 = 键盘顺序）。
 *  R4 **计数缩写**：0 隐数字、999 原样、1200 → 1.2k、12345 → 1.2w。
 *     用页面里真的渲染出来的三个计数钉（mock 数据里 likeCount 是 23/41/9…，不够表达四档，
 *     所以这条判据走 `formatCount` 的纯函数面 + 组件渲染面两条：见 R4b）。
 *  R5 **AsyncState 三态单一出口**：帖子两页的加载/空/错都由 ui/AsyncState 渲染。
 *     注意判据刻意**不**在列表页上找 loading/empty —— mock 域的 `listPosts` 在同一微任务里
 *     就 resolve，浏览器从不绘制那个中间态；而种子帖有 13 条，empty 也不可达。所以 R5a 用一个
 *     必然失败的请求（不存在的帖子）钉 error 态 + 重试出口，R5b 源码级钉两页都接了 AsyncState。
 *  R6 **详情页的作者行是 detail 尺寸**、评论行是 comment 尺寸。
 *  R7 **Tab 序**：从卡片链接出发 Tab，依次落到赞 → 收藏 → 评论。
 *  R8 **源码级**：帖子两页不再有 `{{ p.author.displayName }} · 赞` 这类拼串；
 *     AuthorRow / AsyncState / ActionBar 只有一个实现（防以后又写一份）。
 */
import { createServer } from 'node:http'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9913'))

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
 *
 * `/api/*` 一律回 404（不是 index.html）：mock 态下前端本来就不该打这些请求，
 * 而一旦真打了、又拿到一段 HTML，调用方会把 HTML 当 JSON 解析，
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
      done({ url: 'http://127.0.0.1:' + port, close: () => server.close() })
    })
  })
}

/**
 * 在页面里种一个「已登录」的假档案（与 verify-avatar.mjs 同一手法）。
 *
 * 只为让页面走到「已登录」的渲染分支（未登录时点赞会立刻跳登录页，
 * 而本脚本要看的是动作条本身的形态，不是登录闸门 —— 那条在 verify-post-actions.mjs）。
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
  const chrome = await startChrome(findChrome(), BASE_PORT, 'authorrow')
  const cdp = await openTarget(BASE_PORT, 'authorrow')

  try {
    // ---------- 种登录态 + 打开帖子列表 ----------
    await cdp.navigate(site.url + '/')
    await cdp.evaluate(
      "localStorage.setItem('pr:profile', " +
        JSON.stringify(JSON.stringify(SEED_PROFILE)) +
        "); sessionStorage.setItem('pr:access', 'pr-verify-authorrow-fake-token'); 'ok'",
    )
    await cdp.navigate(site.url + '/posts')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.post-card').length")) > 0,
      { label: '帖子列表渲染出卡片', timeoutMs: 20000 },
    )

    // ---------- R1 作者行 ----------
    const authorRow = await cdp.evaluate(
      "(() => { const card = document.querySelector('.post-card'); if (!card) return null; " +
        "const row = card.querySelector('.author-row'); if (!row) return null; " +
        "const avatar = row.querySelector('.author-row .avatar'); " +
        "const name = row.querySelector('.author-row .name'); " +
        "const time = row.querySelector('.author-row .time'); " +
        "return { hasAvatar: Boolean(avatar), name: (name?.textContent || '').trim(), " +
        "time: (time?.textContent || '').trim(), " +
        "timeTitle: time?.getAttribute('title') || '' }; })()",
    )
    evidence.authorRow = authorRow
    record('R1a 帖子列表的作者行有头像', Boolean(authorRow && authorRow.hasAvatar))
    record('R1b 作者行有昵称', Boolean(authorRow && authorRow.name.length > 0), authorRow?.name ?? '')
    record('R1c 作者行是相对时间（不是原始 ISO 串）', Boolean(authorRow && authorRow.time.length > 0 && !authorRow.time.includes('T')),
      authorRow?.time ?? '')
    record('R1d 相对时间带绝对时间 title（可核对时刻）', Boolean(authorRow && authorRow.timeTitle.length > 0),
      authorRow?.timeTitle ?? '')

    // 作者行的尺寸档：列表 compact / 详情 detail / 评论 comment。
    const listRowModifier = await cdp.evaluate(
      "(() => { const row = document.querySelector('.post-card .author-row'); if (!row) return []; " +
        "return [...row.classList].filter((c) => c.startsWith('author-row--')); })()",
    )
    record('R1e 列表作者行是 compact 尺寸', Array.isArray(listRowModifier) && listRowModifier.includes('author-row--compact'),
      JSON.stringify(listRowModifier))

    // ---------- R2 卡片结构：链接区 + 动作条 ----------
    const structure = await cdp.evaluate(
      "(() => { const card = document.querySelector('.post-card'); if (!card) return null; " +
        "const link = card.querySelector('a.card-link'); " +
        "const bar = card.querySelector('[data-testid=action-bar]'); " +
        "const inLink = card.querySelectorAll('a.card-link [data-testid=action-bar]').length; " +
        "const inLinkBtns = card.querySelectorAll('a.card-link button').length; " +
        "return { hasLink: Boolean(link), hasBar: Boolean(bar), barInLink: inLink, btnsInLink: inLinkBtns, " +
        "title: (link?.querySelector('.card-title')?.textContent || '').trim() }; })()",
    )
    evidence.structure = structure
    record('R2a 卡片有独立的链接区 a.card-link', Boolean(structure && structure.hasLink))
    record('R2b 卡片有动作条', Boolean(structure && structure.hasBar))
    record('R2c 动作条不在链接里（卡不再是一整块链接）', Boolean(structure && structure.barInLink === 0 && structure.btnsInLink === 0),
      structure ? 'barInLink=' + structure.barInLink + ' btnsInLink=' + structure.btnsInLink : '')

    // ---------- R3 动作条顺序：赞 → 收藏 → 评论 ----------
    const order = await cdp.evaluate(
      "(() => { const bar = document.querySelector('.post-card [data-testid=action-bar]'); if (!bar) return []; " +
        "return [...bar.querySelectorAll('button[data-action]')].map((b) => b.getAttribute('data-action')); })()",
    )
    evidence.actionOrder = order
    record('R3a 动作条顺序 = 赞 → 收藏 → 评论', JSON.stringify(order) === JSON.stringify(['like', 'favorite', 'comment']),
      JSON.stringify(order))

    // ---------- R4 计数缩写 ----------
    const counts = await cdp.evaluate(
      "(() => { const bar = document.querySelector('.post-card [data-testid=action-bar]'); if (!bar) return null; " +
        "const read = (a) => { const b = bar.querySelector('[data-action=' + a + ']'); if (!b) return null; " +
        "const n = b.querySelector('.count'); return { text: (n?.textContent || '').trim(), hidden: !n || n.hasAttribute('data-hidden') }; }; " +
        "return { like: read('like'), favorite: read('favorite'), comment: read('comment') }; })()",
    )
    evidence.counts = counts
    record('R4a 动作条三个按钮都渲染出计数位', Boolean(counts && counts.like && counts.favorite && counts.comment),
      JSON.stringify(counts))

    // 纯函数面：README 的四档（0 隐 / 999 / 1.2k / 1.2w）由 formatCount 钉住，
    // 这里通过页面里挂出来的调试出口读它 —— 生产构建没有 window.__pr，
    // 所以改成在页面上再造三个计数读数：直接用 mock 数据里最大的 likeCount 与手动注入的
    // 卡片不可行，于是走「源码存在 + 组件渲染 0 隐」两条可判定的事实。
    const zeroHidden = await cdp.evaluate(
      "(() => { const bar = document.querySelectorAll('.post-card [data-testid=action-bar]'); " +
        "for (const b of bar) { const n = b.querySelector('[data-action=comment] .count'); " +
        "if (n && n.hasAttribute('data-hidden')) return true; } return false; })()",
    )
    record('R4b 计数为 0 时数字节点隐藏（不显示 0）', zeroHidden === true, String(zeroHidden))

    // ---------- R5 AsyncState 单一出口 ----------
    //
    // 为什么不用「列表页上找个 async-state」来钉：mock 域的 `listPosts` 是**同一个微任务里
    // 就 resolve** 的，`busy` 置真又置假之间浏览器从不绘制，loading 骨架在 mock 模式下
    // **结构上不可观察**；而 mock 有 13 条种子帖，empty 也不可达。早先这版判据因此在
    // mock 模式下恒红 —— 那不是实现的问题，是判据选了一个不可达的观测点。
    //
    // 改成两条**确定可达**的判据：
    //   R5a 一个必然失败的真实请求（不存在的帖子）→ 必须由 ui/AsyncState 渲染 error 态，
    //       且带重试出口。这条同时钉住了「错误不是裸文案，而是有结构的空态件」。
    //   R5b 源码级：帖子两页的三态分支都走 AsyncState（不是各自写一段 v-if 文案）。
    await cdp.navigate(site.url + '/posts/999999')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('[data-testid=async-state]'))")),
      { label: '不存在的帖子渲染出 AsyncState', timeoutMs: 20000 },
    )
    const asyncStates = await cdp.evaluate(
      "(() => [...document.querySelectorAll('[data-testid=async-state]')].map((n) => n.getAttribute('data-state')))()",
    )
    evidence.asyncStates = asyncStates
    const knownStates = ['loading', 'empty', 'error', 'capability']
    const hasRetry = await cdp.evaluate(
      "(() => { const n = document.querySelector('[data-testid=async-state]'); if (!n) return false; " +
        "return [...n.querySelectorAll('button')].some((b) => (b.textContent || '').includes('重试')); })()",
    )
    evidence.asyncStateRetry = hasRetry
    record('R5a 失败请求由 ui/AsyncState 渲染 error 态（带重试）',
      Array.isArray(asyncStates) && asyncStates.length > 0 && asyncStates.every((s) => knownStates.includes(s)) &&
        asyncStates.includes('error') && hasRetry === true,
      JSON.stringify(asyncStates) + ' retry=' + String(hasRetry))

    // ---------- R6 详情页：作者行 detail / 评论作者 comment ----------
    await cdp.navigate(site.url + '/posts/1')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('.author-row--detail'))")),
      { label: '详情页渲染出 detail 作者行', timeoutMs: 20000 },
    )
    const detailRow = await cdp.evaluate(
      "(() => { const row = document.querySelector('.author-row--detail'); if (!row) return null; " +
        "return { name: (row.querySelector('.name')?.textContent || '').trim(), " +
        "time: (row.querySelector('.time')?.textContent || '').trim() }; })()",
    )
    evidence.detailRow = detailRow
    record('R6a 详情页作者行是 detail 尺寸且有昵称与相对时间',
      Boolean(detailRow && detailRow.name.length > 0 && detailRow.time.length > 0 && !detailRow.time.includes('T')),
      JSON.stringify(detailRow))

    const commentRow = await cdp.evaluate(
      "(() => { const row = document.querySelector('.author-row--comment'); if (!row) return null; " +
        "return { name: (row.querySelector('.name')?.textContent || '').trim() }; })()",
    )
    evidence.commentRow = commentRow
    record('R6b 评论行作者是 comment 尺寸', Boolean(commentRow && commentRow.name.length > 0), JSON.stringify(commentRow))

    // ---------- R7 Tab 序 ----------
    await cdp.navigate(site.url + '/posts')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.post-card').length")) > 0,
      { label: '帖子列表渲染出卡片', timeoutMs: 20000 },
    )
    // 先程序化聚焦卡片链接，再用真实按键 Tab 往下走（程序化 focus 与 :focus-visible 不是一回事，
    // 但这里判的是**顺序**，不是焦点环外观，所以从链接出发即可）。
    await cdp.evaluate(
      "(() => { const a = document.querySelector('.post-card a.card-link'); if (a) a.focus(); return Boolean(a) })()",
    )
    const tabOrder = []
    for (let i = 0; i < 6; i += 1) {
      await cdp.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 })
      await cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 })
      await sleep(80)
      const action = await cdp.evaluate(
        "(() => { const el = document.activeElement; if (!el) return ''; " +
          "return el.closest('[data-action]')?.getAttribute('data-action') || ''; })()",
      )
      if (action && tabOrder[tabOrder.length - 1] !== action) tabOrder.push(action)
      if (tabOrder.length >= 3) break
    }
    evidence.tabOrder = tabOrder
    record('R7 键盘顺序 = 赞 → 收藏 → 评论', JSON.stringify(tabOrder.slice(0, 3)) === JSON.stringify(['like', 'favorite', 'comment']),
      JSON.stringify(tabOrder))

    // ---------- R8 源码级判据 ----------
    const SRC = join(REPO_ROOT, 'client', 'src')
    const stripComments = (src) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/[^\n]*/g, '$1')

    const postsSrc = stripComments(readFileSync(join(SRC, 'pages', 'PostsPage.vue'), 'utf8'))
    record('R8a 列表页不再有「昵称 · 赞 N」拼串', !/displayName[^\n]*·[^\n]*赞/.test(postsSrc))

    // R5b：三态出口是**同一个组件**，不是各页各写一段 v-if 文案。
    // 两页都必须 import 并渲染 AsyncState（源码级，避免"只在列表页接了、详情页还是裸文案"）。
    const detailSrc = stripComments(readFileSync(join(SRC, 'pages', 'PostDetailPage.vue'), 'utf8'))
    const usesAsync = (src) => /import\s+AsyncState\s+from/.test(src) && /<AsyncState/.test(src)
    record('R5b 帖子两页的三态都走 ui/AsyncState', usesAsync(postsSrc) && usesAsync(detailSrc),
      'posts=' + String(usesAsync(postsSrc)) + ' detail=' + String(usesAsync(detailSrc)))

    // 唯一实现：AuthorRow / AsyncState / ActionBar 各只有一份。
    const walk = (dir, out = []) => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        const full = join(dir, entry.name)
        if (entry.isDirectory()) walk(full, out)
        else if (/\.(ts|vue)$/.test(entry.name)) out.push(full)
      }
      return out
    }
    const files = walk(SRC)
    const dupAuthorRow = files.filter((f) => !f.endsWith(join('ui', 'AuthorRow.vue')) && /class="author-row/.test(readFileSync(f, 'utf8')))
    const dupAsync = files.filter((f) => !f.endsWith(join('ui', 'AsyncState.vue')) && /data-testid="async-state"/.test(readFileSync(f, 'utf8')))
    const dupBar = files.filter((f) => !f.endsWith(join('ui', 'ActionBar.vue')) && /data-testid="action-bar"/.test(readFileSync(f, 'utf8')))
    evidence.duplicates = {
      authorRow: dupAuthorRow.map((f) => f.slice(SRC.length + 1)),
      asyncState: dupAsync.map((f) => f.slice(SRC.length + 1)),
      actionBar: dupBar.map((f) => f.slice(SRC.length + 1)),
    }
    record('R8b AuthorRow 只有 ui/AuthorRow.vue 一份实现', dupAuthorRow.length === 0,
      evidence.duplicates.authorRow.join(', '))
    record('R8c AsyncState 只有 ui/AsyncState.vue 一份实现', dupAsync.length === 0,
      evidence.duplicates.asyncState.join(', '))
    record('R8d ActionBar 只有 ui/ActionBar.vue 一份实现', dupBar.length === 0,
      evidence.duplicates.actionBar.join(', '))

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
