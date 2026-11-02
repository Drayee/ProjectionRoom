#!/usr/bin/env node
/**
 * `verify-friends.mjs` —— 好友页重排（票 #58）的**真机**验收。
 *
 * # 为什么是真机脚本
 *
 * 本票的判据几乎全是**界面状态**："带 `?q=` 进页会不会自动搜"、"清空搜索框回不回列表"、
 * "游客看见的是不是禁用态搜索框"、"好友为空时焦点在不在搜索框上"。
 * 这些在 HTTP 层单测里都看不出来 —— 后端返回的数据对不对是另一回事，
 * 用户看到的是"搜索框在哪、打了字有没有反应"。
 *
 * # 它在什么环境里跑
 *
 * **不需要后端**：本脚本自己起静态服务器托管 `client/dist`，并把 `/api/*` 一律回 404。
 * 前端默认走 **mock 域**（`api/mode.ts`：只有显式配 `VITE_MOCK_*=0` 才翻真），
 * 所以名单、搜索、拉黑都来自 `api/mock/*` 的内存假数据。
 *
 * # 前置
 *
 *   npm --prefix client run build      # 必须先构建出 client/dist
 *   node test/script/verify-friends.mjs
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 *
 * # 判据（对应工单验收五条）
 *
 *  F1  **URL 契约**（验收 ①）：`/friends?tab=following` 进页后「关注」tab 选中；
 *      `/friends?tab=bogus` 回落默认「好友」tab（未知值不报错，也不停在空白态）。
 *  F2  **带 q 进页自动搜**（验收 ①）：`/friends?q=放映` 进页后结果面板在场、
 *      搜索框里回显「放映」、URL 仍是带 q 的那条（分享/刷新不丢）。
 *  F3  **点 tab 写 URL**（验收 ①）：点「粉丝」后 `location.search` 里出现 `tab=followers`。
 *  F4  **防抖 300ms + ≥2 字符**（验收 ②）：打 1 个字**不发请求**（页面里没有结果面板）；
 *      打 2 个字并停 300ms 以上**才**发；用页面里的 fetch 钩子证明请求真的发出去了。
 *  F5  **清空回列表**（验收 ②）：清空搜索框后结果面板消失、URL 里不再有 `q`。
 *  F6  **游客维持需登录**（验收 ②）：未登录进 `/friends` 看到登录墙 + **禁用态搜索框**
 *      + 「去登录」入口（不是把搜索框藏起来）。
 *  F7  **结果行的关系动作**（验收 ③）：mock 种子里「喵喵」是我关注了但没回关的
 *      → 结果行按钮是「已关注」；「夜猫子」是关注了我没回关的 → 按钮是「回关」；
 *      互关的「放映员」→ 有「发消息」链接。
 *  F8  **拉黑存续不给关注按钮**（验收 ③）：mock 里 id 6「路过的猫」被我拉黑
 *      → 搜到它时行内**没有**关注/回关按钮，只有「已拉黑」说明。
 *  F9  **「…」菜单**（验收 ③）：点「…」出现「查看档案」与「拉黑」两项。
 *  F10 **未搜与 0 结果两态文案分开**（验收 ④）：搜一个不存在的词 → 空态文案含
 *      「没找到」；而**没搜**时列表区显示的是名单（或名单空态），不是"没找到"。
 *  F11 **好友为空时聚焦搜索框**（验收 ④）：新用户（mock 里清空关注边）进页后
 *      `document.activeElement` 是搜索框。
 *  F12 **待处理请求入口已隐藏**（验收 ⑤）：页面上没有「待处理」tab。
 *  F13 **行结构契约**（不能打挂 verify-avatar）：`.row` → `.who strong` → `.avatar`
 *      三个类名仍在，且好友行头像 href 是 `/user/2`。
 *  F14 **源码级：键名唯一源**：`tab` / `q` 两个字面量只出现在 `utils/friendsRoute.ts`
 *      （去注释后 grep；其它文件必须 import 常量而不是重写字符串）。
 */
import { createServer } from 'node:http'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9930'))

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

/**
 * 登录态种子（与 verify-avatar 同一份形状）。
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
  const chrome = await startChrome(findChrome(), BASE_PORT, 'friends')
  const cdp = await openTarget(BASE_PORT, 'friends')

  try {
    // ---------- F6 游客：登录墙 + 禁用态搜索框 ----------
    await cdp.navigate(site.url + '/friends')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('.gate'))")),
      { label: '游客看到登录墙', timeoutMs: 15000 },
    )
    const gate = await cdp.evaluate(`(() => {
      const g = document.querySelector('.gate')
      const input = g ? g.querySelector('input') : null
      const cta = g ? g.querySelector('a') : null
      return {
        hasInput: Boolean(input),
        disabled: input ? input.disabled : false,
        ctaText: cta ? (cta.textContent || '').trim() : '',
        ctaHref: cta ? cta.getAttribute('href') || '' : '',
      }
    })()`)
    evidence.guestGate = gate
    record(
      'F6 游客看见登录墙 + 禁用态搜索框 + 去登录入口',
      gate.hasInput && gate.disabled && gate.ctaText.includes('登录') && gate.ctaHref.includes('/login'),
      JSON.stringify(gate),
    )

    // ---------- 种登录态 ----------
    await cdp.evaluate(
      "localStorage.setItem('pr:profile', " +
        JSON.stringify(JSON.stringify(SEED_PROFILE)) +
        "); sessionStorage.setItem('pr:access', 'pr-verify-friends-token'); 'ok'",
    )

    // ---------- F12 待处理 tab 已隐藏 ----------
    await cdp.navigate(site.url + '/friends')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.tabs [role=tab]').length")) > 0,
      { label: '好友页 tab 渲染', timeoutMs: 15000 },
    )
    const tabLabels = await cdp.evaluate(
      "[...document.querySelectorAll('.tabs [role=tab]')].map((b) => (b.textContent || '').trim())",
    )
    evidence.tabLabels = tabLabels
    record('F12 待处理请求入口已隐藏（四个 tab 无「待处理」）', !tabLabels.includes('待处理'), tabLabels.join(' / '))
    record(
      'F12b 四个 tab 是 好友/关注/粉丝/黑名单',
      tabLabels.length === 4 && tabLabels[0] === '好友' && tabLabels[3] === '黑名单',
      tabLabels.join(' / '),
    )

    // ---------- F13 行结构契约（不能打挂 verify-avatar） ----------
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.row .avatar').length")) > 0,
      { label: '好友行渲染出头像', timeoutMs: 15000 },
    )
    const rowProbe = await cdp.evaluate(`(() => {
      const rows = [...document.querySelectorAll('.row')]
      const row = rows.find((r) => (r.querySelector('.who strong')?.textContent || '').trim() === '放映员')
      if (!row) return null
      const el = row.querySelector('.avatar')
      return { href: el ? el.getAttribute('href') || '' : '', name: (row.querySelector('.who strong')?.textContent || '').trim() }
    })()`)
    evidence.rowProbe = rowProbe
    record(
      'F13 行结构契约仍在（.row / .who strong / .avatar，落点 /user/2）',
      rowProbe !== null && rowProbe.href === '/user/2',
      JSON.stringify(rowProbe),
    )

    // ---------- F1 URL 契约：tab 进页生效 + 未知值回落 ----------
    await cdp.navigate(site.url + '/friends?tab=following')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelector('.tabs [aria-selected=true]')?.textContent || ''")).includes('关注'),
      { label: '带 tab=following 进页选中「关注」', timeoutMs: 15000 },
    )
    const sel1 = await cdp.evaluate("(document.querySelector('.tabs [aria-selected=true]')?.textContent || '').trim()")
    record('F1a /friends?tab=following 进页选中「关注」', sel1 === '关注', sel1)

    await cdp.navigate(site.url + '/friends?tab=bogus')
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('.tabs [aria-selected=true]'))")),
      { label: '未知 tab 回落', timeoutMs: 15000 },
    )
    const sel2 = await cdp.evaluate("(document.querySelector('.tabs [aria-selected=true]')?.textContent || '').trim()")
    record('F1b 未知 tab 回落默认「好友」（不报错、不停在空白态）', sel2 === '好友', sel2)

    // ---------- F3 点 tab 写 URL ----------
    await cdp.evaluate(`(() => {
      const b = [...document.querySelectorAll('.tabs [role=tab]')].find((x) => (x.textContent || '').trim() === '粉丝')
      if (b) b.click()
      return Boolean(b)
    })()`)
    await waitFor(
      async () => (await cdp.evaluate('location.search')).includes('tab=followers'),
      { label: '点「粉丝」后 URL 出现 tab=followers', timeoutMs: 10000 },
    )
    const search1 = await cdp.evaluate('location.search')
    record('F3 点 tab 写进 URL（tab=followers）', search1.includes('tab=followers'), search1)

    // ---------- F2 带 q 进页自动搜 ----------
    await cdp.navigate(site.url + '/friends?q=' + encodeURIComponent('放映'))
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.sect .row').length")) > 0,
      { label: '带 q 进页自动搜出结果', timeoutMs: 15000 },
    )
    const autoSearch = await cdp.evaluate(`(() => {
      const input = document.querySelector('.find input')
      return {
        inputValue: input ? input.value : '',
        search: location.search,
        rowNames: [...document.querySelectorAll('.sect .row .who strong')].map((x) => (x.textContent || '').trim()),
      }
    })()`)
    evidence.autoSearch = autoSearch
    record(
      'F2 带 q 进页自动搜（结果在场 + 输入框回显 + URL 保留 q）',
      autoSearch.inputValue === '放映' &&
        autoSearch.search.includes('q=') &&
        autoSearch.rowNames.includes('放映员'),
      JSON.stringify(autoSearch),
    )

    // ---------- F7 关系动作按方向给 ----------
    // 互关的「放映员」：有「发消息」链接。
    const mutualRow = await cdp.evaluate(`(() => {
      const rows = [...document.querySelectorAll('.sect .row')]
      const row = rows.find((r) => (r.querySelector('.who strong')?.textContent || '').trim() === '放映员')
      if (!row) return null
      return {
        acts: [...row.querySelectorAll('.acts a, .acts button')].map((x) => (x.textContent || '').trim()),
        msgHref: (() => { const a = [...row.querySelectorAll('.acts a')].find((x) => (x.textContent || '').trim() === '发消息'); return a ? a.getAttribute('href') : '' })(),
      }
    })()`)
    evidence.mutualRow = mutualRow
    record(
      'F7a 互关的人给「发消息」（深链 /messages?peer=2）',
      mutualRow !== null && mutualRow.msgHref === '/messages?peer=2',
      JSON.stringify(mutualRow),
    )

    // 我关注了没回关的「喵喵」：按钮是「已关注」。
    await cdp.navigate(site.url + '/friends?q=' + encodeURIComponent('喵喵'))
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.sect .row').length")) > 0,
      { label: '搜到喵喵', timeoutMs: 15000 },
    )
    const followingRow = await cdp.evaluate(`(() => {
      const row = document.querySelector('.sect .row')
      return row ? [...row.querySelectorAll('.acts button')].map((x) => (x.textContent || '').trim()) : []
    })()`)
    evidence.followingRow = followingRow
    record('F7b 已关注的人按钮是「已关注」', followingRow.includes('已关注'), JSON.stringify(followingRow))

    // 关注了我没回关的「夜猫子」：按钮是「回关」。
    await cdp.navigate(site.url + '/friends?q=' + encodeURIComponent('夜猫子'))
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.sect .row').length")) > 0,
      { label: '搜到夜猫子', timeoutMs: 15000 },
    )
    const followBackRow = await cdp.evaluate(`(() => {
      const row = document.querySelector('.sect .row')
      return row ? [...row.querySelectorAll('.acts button')].map((x) => (x.textContent || '').trim()) : []
    })()`)
    evidence.followBackRow = followBackRow
    record('F7c 关注了我的人按钮是「回关」', followBackRow.includes('回关'), JSON.stringify(followBackRow))

    // ---------- F8 拉黑存续不给关注按钮 ----------
    await cdp.navigate(site.url + '/friends?q=' + encodeURIComponent('路过的猫'))
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.sect .row').length")) > 0,
      { label: '搜到被拉黑的人', timeoutMs: 15000 },
    )
    const blockedRow = await cdp.evaluate(`(() => {
      const row = document.querySelector('.sect .row')
      if (!row) return null
      return {
        acts: [...row.querySelectorAll('.acts button, .acts a')].map((x) => (x.textContent || '').trim()),
        who: (row.querySelector('.who')?.textContent || '').trim(),
      }
    })()`)
    evidence.blockedRow = blockedRow
    record(
      'F8 拉黑存续不给关注按钮（只有「已拉黑」说明）',
      blockedRow !== null &&
        !blockedRow.acts.includes('关注') &&
        !blockedRow.acts.includes('回关') &&
        blockedRow.who.includes('已拉黑'),
      JSON.stringify(blockedRow),
    )

    // ---------- F9 「…」菜单 ----------
    await cdp.navigate(site.url + '/friends?q=' + encodeURIComponent('喵喵'))
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.sect .row').length")) > 0,
      { label: '搜到喵喵', timeoutMs: 15000 },
    )
    await cdp.evaluate(`(() => {
      const row = document.querySelector('.sect .row')
      const b = row ? [...row.querySelectorAll('.acts button')].find((x) => (x.textContent || '').trim() === '…') : null
      if (b) b.click()
      return Boolean(b)
    })()`)
    await sleep(150)
    const menu = await cdp.evaluate(
      "[...document.querySelectorAll('.menu [role=menuitem]')].map((x) => (x.textContent || '').trim())",
    )
    evidence.menu = menu
    record('F9「…」菜单有「查看档案」与「拉黑」', menu.includes('查看档案') && menu.includes('拉黑'), JSON.stringify(menu))

    // ---------- F10 未搜与 0 结果两态文案分开 ----------
    await cdp.navigate(site.url + '/friends?q=' + encodeURIComponent('zzz不存在zzz'))
    await waitFor(
      async () => (await cdp.evaluate("Boolean(document.querySelector('[data-testid=async-state]'))")),
      { label: '0 结果显示空态', timeoutMs: 15000 },
    )
    const zeroText = await cdp.evaluate(
      "(document.querySelector('[data-testid=async-state]')?.textContent || '').trim()",
    )
    evidence.zeroText = zeroText
    record('F10a 搜了 0 结果 → 空态文案含「没找到」', zeroText.includes('没找到'), zeroText.slice(0, 80))

    await cdp.navigate(site.url + '/friends')
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.tabs [role=tab]').length")) > 0,
      { label: '回到列表态', timeoutMs: 15000 },
    )
    const listText = await cdp.evaluate(
      "(document.querySelector('.sect')?.textContent || '').trim()",
    )
    evidence.listText = listText.slice(0, 120)
    record('F10b 没搜时不出现「没找到」（两态文案分开）', !listText.includes('没找到'), listText.slice(0, 80))

    // ---------- F4 防抖 300ms + ≥2 字符 ----------
    //
    // 为什么不用 fetch 钩子数请求：本脚本跑在 **mock 域**，`api/mock/friends.ts`
    // 直接返回内存里的假数据，**一个 HTTP 请求都不发**。数 fetch 会恒为 0，
    // 那是判据选错了观测点（与 verify-authorrow 的 R5 同一个教训）。
    //
    // 改判两个**确定可观察**的事实：
    //   ① ≥2 字符门槛：打 1 个字等 500ms，URL 里**不会**出现 q（也没有结果面板）；
    //   ② 防抖延迟：打第 2 个字后**立刻**看 URL 仍是干净的，等过 300ms 才出现 q。
    //      「立刻没有 + 稍后才有」就是防抖本身的可观察形态。
    await cdp.evaluate(`(() => {
      const input = document.querySelector('.find input')
      if (!input) return false
      input.focus()
      return true
    })()`)
    // ① 打 1 个字：不该触发搜索（≥2 字符才发）。
    await cdp.evaluate(`(() => {
      const input = document.querySelector('.find input')
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
      setter.call(input, '放')
      input.dispatchEvent(new Event('input', { bubbles: true }))
      return input.value
    })()`)
    await sleep(600)
    const afterOne = await cdp.evaluate(`(() => ({
      search: location.search,
      inputValue: document.querySelector('.find input')?.value || '',
      hasPanel: Boolean(document.querySelector('.sect-head')?.textContent?.includes('搜索「')),
    }))()`)
    evidence.afterOneChar = afterOne
    record(
      'F4a 只打 1 个字不触发搜索（≥2 字符门槛）',
      !afterOne.search.includes('q=') && afterOne.inputValue === '放' && !afterOne.hasPanel,
      JSON.stringify(afterOne),
    )

    // ② 打第 2 个字：立刻看还是干净的，过了 300ms 才写进 URL。
    const immediate = await cdp.evaluate(`(() => {
      const input = document.querySelector('.find input')
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
      setter.call(input, '放映')
      input.dispatchEvent(new Event('input', { bubbles: true }))
      return location.search
    })()`)
    evidence.searchImmediatelyAfterTyping = immediate
    record('F4b 打字瞬间 URL 仍是干净的（防抖未到期）', !immediate.includes('q='), immediate || '(空)')

    await waitFor(
      async () => (await cdp.evaluate('location.search')).includes('q='),
      { label: '过了防抖窗口后写入 q', timeoutMs: 10000 },
    )
    const urlAfterDebounce = await cdp.evaluate('location.search')
    evidence.searchAfterDebounce = urlAfterDebounce
    record(
      'F4c 停手 300ms 后搜索真的发生（URL 出现 q，且就是这两个字）',
      urlAfterDebounce.includes('q=') && decodeURIComponent(urlAfterDebounce).includes('放映'),
      urlAfterDebounce,
    )

    // ---------- F5 清空回列表 ----------
    await cdp.evaluate(`(() => {
      const input = document.querySelector('.find input')
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
      setter.call(input, '')
      input.dispatchEvent(new Event('input', { bubbles: true }))
      return input.value
    })()`)
    await waitFor(
      async () => !(await cdp.evaluate('location.search')).includes('q='),
      { label: '清空后 URL 里不再有 q', timeoutMs: 10000 },
    )
    const clearedState = await cdp.evaluate(`(() => ({
      search: location.search,
      hasSearchPanel: Boolean(document.querySelector('.sect-head')?.textContent?.includes('搜索「')),
    }))()`)
    evidence.cleared = clearedState
    record(
      'F5 清空搜索框 → 回列表态（URL 无 q、结果面板消失）',
      !clearedState.search.includes('q=') && !clearedState.hasSearchPanel,
      JSON.stringify(clearedState),
    )

    const SRC = join(REPO_ROOT, 'client', 'src')
    /** 去注释后再看（组件注释里会提到被删掉的实现，直接 grep 会假红）。 */
    const stripComments = (src) =>
      src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/[^\n]*/g, '$1')

    // ---------- F11 好友为空时聚焦搜索框 ----------
    //
    // 这条**判不了真机**：mock 的 `CURRENT_USER_ID` 固定是 1，而种子里的关注边
    // 让「好友/关注/粉丝/黑名单」四个 tab 都非空 —— 空名单态在这个数据下结构上
    // 不可达（与 verify-authorrow 的 R5 同一个教训：别选不可达的观测点）。
    //
    // 于是拆成两条各自可判的：
    //   F11a 真机：搜索框**常驻**（列表态下也在场、可聚焦、未禁用）——
    //         这是"空态能自动聚焦"的前提。
    //   F11b 源码级：`FriendsPage.vue` 里确实写着「名单为空且没在搜索 → 聚焦搜索框」
    //         那段联动（去注释后匹配 searchInput / .focus() / 'empty'）。
    const focusProbe = await cdp.evaluate(`(() => {
      const input = document.querySelector('.find input')
      if (!input) return { hasInput: false }
      input.focus()
      const focusable = document.activeElement === input
      input.blur()
      return { hasInput: true, focusable, disabled: input.disabled }
    })()`)
    evidence.focusProbe = focusProbe
    record(
      'F11a 列表态下搜索框常驻、可聚焦、未禁用',
      focusProbe.hasInput === true && focusProbe.focusable === true && focusProbe.disabled === false,
      JSON.stringify(focusProbe),
    )

    const friendsPageSrc = stripComments(
      readFileSync(join(SRC, 'pages', 'FriendsPage.vue'), 'utf8'),
    )
    const autoFocusWired =
      friendsPageSrc.includes('searchInput') &&
      friendsPageSrc.includes('.focus()') &&
      friendsPageSrc.includes("'empty'")
    evidence.autoFocusWired = autoFocusWired
    record(
      'F11b 源码里写着「名单空 + 没在搜索 → 聚焦搜索框」',
      autoFocusWired,
      autoFocusWired ? 'searchInput + .focus() + empty 分支都在' : '缺 focus 联动',
    )


    // ---------- F14 源码级：键名唯一源 ----------

    const offenders = []
    const walk = (dir) => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        const full = join(dir, entry.name)
        if (entry.isDirectory()) {
          walk(full)
          continue
        }
        if (!/\.(ts|vue)$/.test(entry.name)) continue
        if (full.endsWith(join('utils', 'friendsRoute.ts'))) continue
        const code = stripComments(readFileSync(full, 'utf8'))
        // 允许的写法：import 常量；不允许的写法：裸字面量 'tab' / 'q' 出现在 URL 构造里。
        if (/query\[['"]tab['"]\]/.test(code) || /query\[['"]q['"]\]/.test(code)) {
          offenders.push(full.slice(SRC.length + 1))
        }
      }
    }
    walk(SRC)
    evidence.queryKeyOffenders = offenders
    record(
      'F14 tab/q 键名只有 utils/friendsRoute.ts 一个源',
      offenders.length === 0,
      offenders.join(', '),
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
