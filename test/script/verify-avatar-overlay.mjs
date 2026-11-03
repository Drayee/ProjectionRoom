#!/usr/bin/env node
/**
 * `verify-avatar-overlay.mjs` —— 票 #74（头像覆盖层）的**真机**验收。
 *
 * # 为什么是真机脚本
 *
 * 票 #74 的判据全是「DOM 里到底渲染了什么」：没有地址时是不是**一个 `<img>` 都没有**、
 * 有地址时那张图是不是装饰（`alt=""` + `aria-hidden`）、加载失败后**是不是真的消失了**
 * 而不是留一个破图图标、以及失败之后**同一次会话里还会不会再试**。这些在 node 单测里
 * 只能测「函数返回什么字符串」，测不出「浏览器把那个字符串渲染成了什么」。
 *
 * # 它在什么环境里跑
 *
 * **不需要后端**：本脚本自己起静态服务器托管 `client/dist`，`/api/*` 一律回 404 JSON。
 * 前端默认走 mock 域（`api/mode.ts`：只有显式配 `VITE_MOCK_*=0` 才翻真），所以
 * `/me`、`/user/:id` 的数据来自本地缓存里的种子档案 + 前端内存里的假数据。
 *
 * 头像地址本身也由种子给（`localStorage['pr:profile'].avatarUrl`）—— 本票判的是
 * **显示层的三态回落**，不是上传链路（那是票 #75），所以图片用静态站里现成的 SVG。
 *
 * # 前置
 *
 *   npm --prefix client run build      # 必须先构建出 client/dist
 *   node test/script/verify-avatar-overlay.mjs
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 *
 * # 判据
 *
 *  O1 **空态**：档案里没有 `avatarUrl` 时，`/me` 概览的大头像与顶栏头像里
 *     **没有** `.avatar-pic`，字母头与底色照常在。这条钉住「字母头是本体、
 *     像素图只是覆盖层」—— 反过来做（图是本体、没图就空着）会让绝大多数
 *     没设过头像的账号变成一片空白圆。
 *  O2 **有值态**：给了地址就出现 `.avatar-pic`，且
 *     a) `alt=""` + `aria-hidden="true"`（图片是装饰，名字由外层给；
 *        读屏软件念两遍名字是常见事故）；
 *     b) `object-fit: cover`（决议 #54 §1：服务端只验不重编码，显示层兜住非方图）；
 *     c) 字母 `.char` **仍在**（被盖住不等于被删掉 —— 删掉的话图一失败就再也回不去了）。
 *  O3 **onerror 回落**：地址指向不存在的对象时，`.avatar-pic` 消失、字母与底色回来。
 *     没有这条，用户删掉桶里那张图之后看到的是一个破图图标。
 *  O4 **会话内不重试**：失败过的地址，在同一次会话里重新挂载（切分区再切回来）
 *     **不会再**出现 `<img>`。没有这条，长列表里那一格会反复闪一下破图。
 *  O5 **本人视角也带**：`/user/1`（我自己的档案页）同样出现 `.avatar-pic`。
 *     这条钉住「只有我看不到自己头像」那种最难自查的缺陷。
 *  O6 **源码级 · 覆盖层只有一个实现**：全仓 `.vue` 里同时出现 `avatarUrl` 与
 *     `<img` 的文件**只有** `ui/AvatarImage.vue` 自己。抄第二份的话，
 *     「失败后不再重试」这条纪律迟早有一处漏掉，而它不报错、也没有断言能抓到。
 *  O7 **源码级 · 派生只有一处**：`avatarImageSrc` / `markAvatarImageFailed`
 *     只在 `utils/avatarImage.ts` 里定义，别处只能 import。
 *  O8 **源码级 · object-fit 只写一次**：`ui/AvatarImage.vue` 的**非注释**代码里
 *     `object-fit: cover` 恰好 1 次（注释里提它是允许的，那不是执行点）。
 */
import { createServer } from 'node:http'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const SRC = join(REPO_ROOT, 'client', 'src')
const BASE_PORT = Number(argOf(process.argv, 'port', '9911'))

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

/** MIME 表：只覆盖静态托管真正需要的几种（字体/图标/脚本/样式/图片）。 */
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
 * `/api/*` 与 `/ws` 一律回 404（不是 index.html）：前端在 mock 态下本来就不该打
 * 这些请求，真打了也要立刻失败而不是把一段 HTML 当 JSON 解析。
 *
 * 这条规则同时给 O3 提供了一个**确定会 error 的图片地址**（`/api/broken-avatar`）：
 * 它回的是 JSON，浏览器解码成图片必然失败。用不存在的静态路径不行 —— 那个会
 * 被 SPA 回退成 index.html，虽然也会 error，但"为什么 error"就说不清了。
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
 * 种子档案：与 `verify-avatar.mjs` 同款，只多一个可选的 `avatarUrl`。
 *
 * 为什么这么种就能"登录"：`stores/auth.ts` 的启动引导看到 sessionStorage 里有
 * access token 就把状态置成 authenticated，随后打一次 `/me`；本脚本把 `/api/*`
 * 回 404，那次失败是**静默**的（只有明确的 401 才会清会话），所以登录态成立。
 * 种子只影响显示层，不伪造任何授权结论。
 */
function seedProfile(avatarUrl) {
  const base = {
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
  // 传 undefined 时**不加这个键** —— 与真实服务端 omitempty 的行为一致，
  // 也正是 O1 要测的那一态（"从没有过头像"）。
  if (typeof avatarUrl === 'string') base.avatarUrl = avatarUrl
  return base
}

/** 读大头像（`/me` 概览与 `/user/:id` 都是 `.avatar-lg`）的完整状态。 */
const READ_LG = `(() => {
  const el = document.querySelector('.avatar-lg')
  if (!el) return null
  const pic = el.querySelector('.avatar-pic')
  // 字母头有两种写法：ui/UserAvatar 里是 <span class="char">，
  // 而 /me 与 /user/:id 的大头像直接把字符写在容器里。两种都要认，
  // 否则这条判据会因为在"另一种写法"的页面上读不到字母而假红。
  const charEl = el.querySelector('.char')
  return {
    hasPic: Boolean(pic),
    picAlt: pic ? pic.getAttribute('alt') : null,
    picAriaHidden: pic ? pic.getAttribute('aria-hidden') : null,
    picObjectFit: pic ? getComputedStyle(pic).objectFit : '',
    picSrc: pic ? pic.getAttribute('src') : '',
    char: (charEl ? charEl.textContent : el.textContent || '').trim(),
    bg: getComputedStyle(el).backgroundColor,
    overflow: getComputedStyle(el).overflow,
    position: getComputedStyle(el).position,
  }
})()`

/** 读顶栏头像（`ui/UserAvatar` 的 `.avatar`）的同一组状态。 */
const READ_HEADER = `(() => {
  const el = document.querySelector('.avatar-slot .avatar')
  if (!el) return null
  const pic = el.querySelector('.avatar-pic')
  const charEl = el.querySelector('.char')
  return {
    hasPic: Boolean(pic),
    char: (charEl ? charEl.textContent : el.textContent || '').trim(),
    bg: getComputedStyle(el).backgroundColor,
  }
})()`

/** 种档案 + 整页导航（整页导航会重置模块级"失败记忆"，正好隔离 O1~O3 三态）。 */
async function openWith(cdp, site, path, avatarUrl) {
  await cdp.navigate(site.url + '/')
  await cdp.evaluate(
    "localStorage.setItem('pr:profile', " +
      JSON.stringify(JSON.stringify(seedProfile(avatarUrl))) +
      "); sessionStorage.setItem('pr:access', 'pr-verify-overlay-fake-token'); 'ok'",
  )
  await cdp.navigate(site.url + path)
}

/** 去掉 TS/Vue 的块注释与行注释（判源码时不能把注释里提到的当执行点）。 */
function stripComments(src) {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/[^\n]*/g, '$1')
}

/** 遍历 `client/src` 下的所有 .vue / .ts 文件。 */
function walkSources() {
  const out = []
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name)
      if (entry.isDirectory()) {
        walk(full)
        continue
      }
      if (!/\.(ts|vue)$/.test(entry.name)) continue
      out.push({ rel: full.slice(SRC.length + 1), full })
    }
  }
  walk(SRC)
  return out
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'overlay')
  const cdp = await openTarget(BASE_PORT, 'overlay')

  try {
    // ---------- O1 空态：没有 avatarUrl ----------
    await openWith(cdp, site, '/me?tab=overview', undefined)
    await waitFor(async () => (await cdp.evaluate('Boolean(document.querySelector(".avatar-lg"))')), {
      label: '/me 概览渲染出大头像',
      timeoutMs: 15000,
    })
    const emptyLg = await cdp.evaluate(READ_LG)
    const emptyHeader = await cdp.evaluate(READ_HEADER)
    evidence.empty = { lg: emptyLg, header: emptyHeader }
    record('O1a 没有头像地址时大头像里没有 <img>', emptyLg !== null && emptyLg.hasPic === false)
    record('O1b 没有头像地址时顶栏头像里没有 <img>', emptyHeader !== null && emptyHeader.hasPic === false)
    record(
      'O1c 空态仍显示字母头与底色',
      Boolean(emptyLg && emptyLg.char !== '' && emptyLg.bg !== '' && emptyLg.bg !== 'rgba(0, 0, 0, 0)'),
      emptyLg ? emptyLg.char + ' / ' + emptyLg.bg : '',
    )
    record(
      'O1d 大头像容器已为覆盖层准备好（position: relative + overflow: hidden）',
      Boolean(emptyLg && emptyLg.position === 'relative' && emptyLg.overflow === 'hidden'),
      emptyLg ? emptyLg.position + ' / ' + emptyLg.overflow : '',
    )

    // ---------- O2 有值态：给一个真能加载的图 ----------
    const goodUrl = '/icons/heart.svg'
    await openWith(cdp, site, '/me?tab=overview', goodUrl)
    await waitFor(async () => (await cdp.evaluate('Boolean(document.querySelector(".avatar-lg .avatar-pic"))')), {
      label: '大头像出现 .avatar-pic',
      timeoutMs: 15000,
    })
    const goodLg = await cdp.evaluate(READ_LG)
    evidence.good = goodLg
    record('O2a 有头像地址时出现 .avatar-pic', goodLg !== null && goodLg.hasPic === true)
    record(
      'O2b 图片是装饰（alt="" + aria-hidden="true"）',
      goodLg !== null && goodLg.picAlt === '' && goodLg.picAriaHidden === 'true',
      goodLg ? 'alt=' + JSON.stringify(goodLg.picAlt) + ' aria-hidden=' + goodLg.picAriaHidden : '',
    )
    record(
      'O2c 覆盖层用 object-fit: cover 兜住非方图',
      goodLg !== null && goodLg.picObjectFit === 'cover',
      goodLg ? goodLg.picObjectFit : '',
    )
    record(
      'O2d 字母头仍在（被盖住不等于被删掉）',
      goodLg !== null && goodLg.char !== '' && goodLg.bg !== 'rgba(0, 0, 0, 0)',
      goodLg ? goodLg.char : '',
    )

    // ---------- O5 本人档案页也带覆盖层 ----------
    await openWith(cdp, site, '/user/1', goodUrl)
    await waitFor(async () => (await cdp.evaluate('Boolean(document.querySelector(".avatar-lg"))')), {
      label: '/user/1 渲染出大头像',
      timeoutMs: 15000,
    })
    // 图片解码是异步的，`<img>` 一渲染就在，但给它一点点时间避免读到"刚好在重渲染"的中间态。
    await sleep(200)
    const selfLg = await cdp.evaluate(READ_LG)
    evidence.self = selfLg
    record('O5 本人档案页（/user/1）也显示像素头像', selfLg !== null && selfLg.hasPic === true)

    // ---------- O3 onerror 回落 ----------
    const badUrl = '/api/broken-avatar'
    await openWith(cdp, site, '/me?tab=overview', badUrl)
    await waitFor(async () => (await cdp.evaluate('Boolean(document.querySelector(".avatar-lg"))')), {
      label: '/me 概览渲染出大头像（坏地址）',
      timeoutMs: 15000,
    })
    // 等到 error 真的发生：`.avatar-pic` 被 v-if 摘掉。
    const fellBack = await waitFor(
      async () => (await cdp.evaluate('Boolean(document.querySelector(".avatar-lg")) && !document.querySelector(".avatar-lg .avatar-pic")')),
      { label: '加载失败后 .avatar-pic 消失', timeoutMs: 15000 },
    ).then(() => true).catch(() => false)
    const badLg = await cdp.evaluate(READ_LG)
    evidence.bad = badLg
    record('O3a 图片加载失败后 <img> 被摘掉（不留破图）', fellBack && badLg !== null && badLg.hasPic === false)
    record(
      'O3b 失败后字母头与底色回来了',
      badLg !== null && badLg.char !== '' && badLg.bg !== 'rgba(0, 0, 0, 0)',
      badLg ? badLg.char + ' / ' + badLg.bg : '',
    )

    // ---------- O4 会话内不重试 ----------
    // 切分区再切回来 = 组件卸载重挂（Vue 会重建 AvatarImage），但模块级"失败记忆"
    // 还在，所以不会重新发起请求、也不会再出现 `<img>`。
    // 这里必须用 SPA 内导航，不能用 cdp.navigate —— 整页导航会重新加载 JS，
    // 模块状态被清空，那测的就不是"会话内"了。
    await cdp.evaluate(
      `(() => {
        const tabs = [...document.querySelectorAll('nav[role="tablist"] button')]
        const target = tabs.find((b) => (b.textContent || '').includes('关系'))
        if (!target) return 'no-tab'
        target.click()
        return 'clicked'
      })()`,
    )
    await waitFor(async () => (await cdp.evaluate("location.search.includes('tab=relations')")), {
      label: '切到「关系」分区',
      timeoutMs: 10000,
    })
    await cdp.evaluate(
      `(() => {
        const tabs = [...document.querySelectorAll('nav[role="tablist"] button')]
        const target = tabs.find((b) => (b.textContent || '').includes('概览'))
        if (!target) return 'no-tab'
        target.click()
        return 'clicked'
      })()`,
    )
    await waitFor(async () => (await cdp.evaluate('Boolean(document.querySelector(".avatar-lg"))')), {
      label: '切回「概览」分区',
      timeoutMs: 10000,
    })
    // 给足时间：如果实现是"失败后没记住"，这里会重新渲染 `<img>` 并再失败一次。
    await sleep(600)
    const retryLg = await cdp.evaluate(READ_LG)
    evidence.retry = retryLg
    record(
      'O4 失败过的地址在本次会话内不再重试（重挂后仍无 <img>）',
      retryLg !== null && retryLg.hasPic === false,
    )

    // ---------- O6 源码级 · 覆盖层只有一个实现 ----------
    // 判据是**覆盖层自己那个 class**（`.avatar-pic`）出现在哪个文件里：
    // 它同时出现在模板与样式里，是"这一层由谁渲染"最直接的指纹。
    // 不能用 `avatarUrl` 当判据 —— 覆盖层组件的 prop 叫 `url`，`avatarUrl`
    // 是调用方传进来的字段名，两者本来就不在同一个文件里。
    //
    // ⚠️ 必须用 `avatar-pic(?!k)`，不能裸找 `avatar-pic`：
    // 裁剪器的按钮 testid 是 `avatar-pick`（选图），它是 `avatar-pic` + `k`。
    // 裸子串匹配会把 `AvatarCropDialog.vue` 也算成"第二份覆盖层"—— 一条假红。
    const OVERLAY_CLASS = /avatar-pic(?!k)/
    const sources = walkSources()
    const overlayOwners = sources
      .filter(({ full }) => OVERLAY_CLASS.test(stripComments(readFileSync(full, 'utf8'))))
      .map(({ rel }) => rel)
    evidence.overlayOwners = overlayOwners
    record(
      'O6 覆盖层只有一个实现（只有 ui/AvatarImage.vue 出现 .avatar-pic）',
      overlayOwners.length === 1 && overlayOwners[0] === join('components', 'ui', 'AvatarImage.vue'),
      overlayOwners.join(', '),
    )

    // ---------- O7 源码级 · 失败记忆只有一处 ----------
    const DERIVATION = /(function\s+avatarImageSrc|function\s+markAvatarImageFailed|const\s+failedAvatarUrls)/
    const defs = sources
      .filter(({ full }) => DERIVATION.test(stripComments(readFileSync(full, 'utf8'))))
      .map(({ rel }) => rel)
    evidence.avatarImageDefs = defs
    record(
      'O7 失败记忆只有一处（utils/avatarImage.ts 之外没有第二份）',
      defs.length === 1 && defs[0] === join('utils', 'avatarImage.ts'),
      defs.join(', '),
    )

    // ---------- O8 源码级 · object-fit 的执行点只有一处 ----------
    const overlayCode = stripComments(readFileSync(join(SRC, 'components', 'ui', 'AvatarImage.vue'), 'utf8'))
    const coverHits = overlayCode.split('object-fit: cover').length - 1
    evidence.coverHits = coverHits
    record('O8 ui/AvatarImage.vue 的非注释代码里 object-fit: cover 恰好 1 次', coverHits === 1, String(coverHits))

    console.log('\n通过 ' + passed + ' / 失败 ' + failed)
    console.log(JSON.stringify(evidence, null, 2))
    process.exitCode = failed === 0 ? 0 : 1
  } finally {
    // 顺序与 `verify-avatar.mjs` 一致：先关标签页（释放 CDP 连接），再关浏览器进程。
    // 漏掉 `chrome.close()` 会让脚本跑完全部判据后**挂住不退**（进程还活着，
    // 而 Node 不会自己退出）—— 那样看起来像超时，其实是清理没做。
    try {
      cdp.close()
    } catch {
      /* 退出阶段关不掉标签页不算判据失败 */
    }
    try {
      chrome.close()
    } catch {
      /* 同上 */
    }
    site.close()
  }
}

main().catch((err) => {
  console.error(err)
  process.exitCode = 1
})
