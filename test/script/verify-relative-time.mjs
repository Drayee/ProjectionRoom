#!/usr/bin/env node
/**
 * `verify-relative-time.mjs` —— 相对时间与全站单一刻度的**真机**验收（票 #60）。
 *
 * # 为什么需要真机
 *
 * 六档边界的**算术**由 `client/test/relative-time.test.mjs`（表驱动）钉住；
 * 但下面这几条只能��真浏览器里验：
 *
 *   T1  `<time datetime>` 的属性、`title`/`aria-label` 给的是**精确绝对时间**；
 *   T2  **全站单一刻度**：页面上有 N 个相对时间，**定时器只有 1 个**，周期 30s；
 *   T3  **`document.hidden` 暂停**：标签页隐藏时刻度不推进（后台不烧 CPU）；
 *   T4  **回可见补算**：从后台回来**立刻**显示正确的时间（不是等下一个 30s）；
 *   T5  **数字自己走**：刻度一到，文字自己从「2 分钟前」变成「3 分钟前」；
 *   T6  **reduced-motion 下仍换字**：降级只杀动画，不许把"换字"也一起杀掉；
 *   T7  **悬停有 title**：真鼠标移到元素上，命中的是那个 `<time>` 且带 title。
 *
 * # 它怎么做到"不靠真实等 30 秒"
 *
 * 两个**页面内钩子**（用 `Page.addScriptToEvaluateOnNewDocument` 在应用脚本之前注入）：
 *
 *   1. **可控时钟**：`Date.now()` 与 `new Date()` 都被套上一个可调 offset，
 *      于是"过了 10 分钟"是**真的**过了 10 分钟（对被测代码而言）。
 *   2. **定时器登记册**：`setInterval` 被包了一层，每次调用都记下 {ms, fn}。
 *      于是"30 秒周期"是**可断言的**（不是读源码猜的），
 *      而"刻度到了"可以通过**手动触发登记册里的回调**来模拟 —— 等价于
 *      定时器自己响了一次，但不用真等 30 秒。
 *
 * 钩子只包裹（仍调用真实实现），不改变行为；被测代码对"时间"与"定时器"无感知。
 *
 * # 前置
 *
 * **不需要后端**：帖子域与消息域默认走 mock（`api/mode.ts` 的默认），
 * 消息页靠往 sessionStorage/localStorage 里播种一份档案就能进（详见 `SEED`）。
 * 脚本自己起一个 Vite dev server（除非 `--client` 给了现成的地址）。
 *
 * # 用法
 *
 *   node test/script/verify-relative-time.mjs
 *   node test/script/verify-relative-time.mjs --client http://127.0.0.1:5173
 *
 * 参数：
 *   --client      复用现成的 Vite 地址（给了就不自己起）
 *   --vite-port   自己起 Vite 时的端口（默认 9517）
 *   --chrome-port headless Chrome 的 CDP 端口（默认 9518）
 *   CHROME_PATH   环境变量可指定浏览器路径
 */
import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { argOf, findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const REPO = resolve(HERE, '..', '..')
const CLIENT_DIR = resolve(REPO, 'client')

const argv = process.argv.slice(2)
const GIVEN_CLIENT = argOf(argv, 'client', '')
const VITE_PORT = Number(argOf(argv, 'vite-port', '9517'))
const CHROME_PORT = Number(argOf(argv, 'chrome-port', '9518'))

/** 判据结果。 */
const checks = []

function record(name, pass, detail) {
  checks.push({ name, pass, detail })
  console.log(`${pass ? '✓' : '✗'} ${name}${detail ? `\n    ${detail}` : ''}`)
}

// ---------- 页面内钩子（可控时钟 + 定时器登记册）----------

/**
 * 在应用脚本**之前**注入的钩子。
 *
 * 为什么要可控时钟：T3/T4/T5 要验的是"时间过去了，界面跟着变"，
 * 而本机的真实时间我们改不了（也不该改）。给 `Date.now()` 加一个可调 offset，
 * 对被测代码来说就等于时间真的走过去了。
 *
 * 为什么要定时器登记册：T2 要断言"**全站只有 1 个**定时器、周期 30s"。
 * 只包一层 `setInterval` 就够了：登记册里 30000ms 的条目必须**恰好一条**，
 * 而页面上渲染的相对时间远不止一个 —— 两者一比，"单一刻度"就是观测事实，
 * 而不是读源码得出的结论。
 *
 * 注意：钩子**仍然调用真实实现**（只是多记一笔），所以真实定时器照常跑、
 * 真实时钟照常走 —— 被测代码察觉不到任何区别。
 */
const HOOK = `
(() => {
  const REAL_NOW = Date.now.bind(Date)
  const RealDate = Date
  window.__prClock = { offset: 0 }
  window.__prTimers = []
  window.__prHidden = false

  function fakeNow() {
    return REAL_NOW() + window.__prClock.offset
  }

  function FakeDate(...args) {
    if (args.length === 0) return new RealDate(fakeNow())
    return new RealDate(...args)
  }
  FakeDate.prototype = RealDate.prototype
  FakeDate.now = fakeNow
  FakeDate.parse = RealDate.parse
  FakeDate.UTC = RealDate.UTC
  window.Date = FakeDate

  // document.hidden 是 Document 原型上的只读 getter：实例上重新定义一个可配置的。
  Object.defineProperty(document, 'hidden', {
    configurable: true,
    get() { return window.__prHidden },
  })

  const realSetInterval = window.setInterval.bind(window)
  const realClearInterval = window.clearInterval.bind(window)
  window.clearInterval = function (handle) {
    const rec = window.__prTimers.find((t) => t.id === handle)
    if (rec) rec.live = false
    return realClearInterval(handle)
  }
  window.setInterval = function (fn, ms, ...rest) {
    const rec = {
      ms,
      fn,
      // live：定时器是否**还活着**（被 clearInterval 掉就置 false）。
      // "隐藏时暂停"的可观测形态就是"表被收了" —— 断言它比断言"文案没变"更贴切。
      live: true,
      // 调用栈：用来把**开发期脚手架**的定时器（Vite 的 HMR 客户端自己也用
      // 30s 轮询）与应用自己的刻度区分开。没有它，"全站只有 1 个 30s 定时器"
      // 这条判据会把 HMR 的那一个算进来 —— 那是开发工具的，不是应用的。
      stack: (new Error().stack || '').split(String.fromCharCode(10)).slice(1, 10).join(' | '),
    }
    const id = realSetInterval(fn, ms, ...rest)
    rec.id = id
    window.__prTimers.push(rec)
    return id
  }
  return 'hooked'
})()
`

/** 往页面里塞一个已知时刻的 `<time>`：用来断言六档文案与绝对时间格式。 */
const SETTLE = `(() => {
  const el = document.querySelector('time.relative-time')
  if (!el) return null
  return {
    text: el.textContent.trim(),
    datetime: el.getAttribute('datetime'),
    title: el.getAttribute('title'),
    aria: el.getAttribute('aria-label'),
    count: document.querySelectorAll('time.relative-time').length,
    timers: window.__prTimers.map((t) => ({ ms: t.ms, live: t.live !== false, stack: t.stack })),
  }
})()`

/** 六档文案（正则是"表"的镜像：期望值写死，不按实现重算）。 */
const TIER_RE = /^(刚刚|\d+ 分钟前|\d+ 小时前|\d+ 天前|\d{1,2}月\d{1,2}日|\d{4}年\d{1,2}月\d{1,2}日)$/
/** 精确绝对时间：`YYYY-MM-DD HH:mm`（决策票 #44 原文的形状，**不带秒**）。 */
const ABS_RE = /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/

/**
 * 从登记册里挑出**应用自己的**刻度定时器。
 *
 * 为什么要按调用栈筛：开发模式下 Vite 的 HMR 客户端自己也注册了一个
 * 30 秒的 `setInterval`（轮询服务端更新）。那是**开发脚手架**的定时器，
 * 不是应用的 —— 不筛掉的话，"全站只有 1 个 30s 定时器"这条判据
 * 永远差一个，测的其实是 HMR 而不是被测代码。
 *
 * 判据按栈里出现 `nowTick` 来认：**刻度的唯一来源**就是那个模块
 * （决策票 #44：组件禁自建定时器）。栈里没有它 = 不是刻度。
 */
function appTickTimers(timers) {
  return (timers ?? []).filter((t) => t.ms === 30000 && /nowTick/.test(t.stack ?? ''))
}

// ---------- 工具 ----------

/** 起一个 Vite dev server（页面自己带 mock 数据，不需要后端）。 */
async function startVite(port) {
  const bin = resolve(CLIENT_DIR, 'node_modules/vite/bin/vite.js')
  if (!existsSync(bin)) {
    throw new Error('client/node_modules 缺失：先跑 npm --prefix client ci（或从主仓拷一份）')
  }
  const proc = spawn(
    process.execPath,
    [bin, '--port', String(port), '--strictPort', '--host', '127.0.0.1'],
    { cwd: CLIENT_DIR, stdio: ['ignore', 'pipe', 'pipe'] },
  )
  let log = ''
  proc.stdout.on('data', (d) => {
    log += d
  })
  proc.stderr.on('data', (d) => {
    log += d
  })
  await waitFor(
    async () => {
      try {
        const r = await fetch(`http://127.0.0.1:${port}/`, { signal: AbortSignal.timeout(2000) })
        return r.ok
      } catch {
        return false
      }
    },
    { label: 'Vite dev 就绪', timeoutMs: 90000 },
  )
  return { proc, url: `http://127.0.0.1:${port}`, close: () => { try { proc.kill() } catch { /* 忽略 */ } } }
}

/**
 * 把会话播种进浏览器（消息页要"已登录"才渲染会话列表）。
 *
 * 两个键名以 `client/src/stores/auth.ts` 为准：access token 只在
 * sessionStorage[`pr:access`]，档案在 localStorage[`pr:profile`]（仅显示用）。
 *
 * 为什么不打真后端：消息域是 mock（`api/messages.ts` 的默认分支），
 * 数据在前端内存里；而 `/api/auth/me` 失败会被引导**静默**吞掉
 * （`stores/auth.ts` 的 `runBootstrap`：只有明确的 401 才去换 token）。
 * 所以播种一份假档案就能进消息页，不需要任何后端实例。
 */
async function seedSession(cdp) {
  const profile = {
    id: 1,
    username: 'mooncat',
    displayName: '月夜猫',
    email: 'mooncat@example.test',
    role: 'user',
    status: 'active',
    createdAt: '2023-10-18T09:00:00Z',
    unread: 0,
    unreadKnown: false,
  }
  await cdp.evaluate(`sessionStorage.setItem('pr:access', 'mock-access-token')`)
  await cdp.evaluate(`localStorage.setItem('pr:profile', ${JSON.stringify(JSON.stringify(profile))})`)
}

// ---------- 主流程 ----------

async function main() {
  const vite = GIVEN_CLIENT ? null : await startVite(VITE_PORT)
  const clientUrl = GIVEN_CLIENT || vite.url
  console.log(`前端：${clientUrl}`)

  const chrome = findChrome()
  const browser = await startChrome(chrome, CHROME_PORT, 'rel-time')
  const page = await openTarget(CHROME_PORT, 'rel-time')

  try {
    // 钩子必须在**任何应用脚本之前**注入。
    await page.send('Page.addScriptToEvaluateOnNewDocument', { source: HOOK })

    // ========== 帖子详情页 ==========
    await page.navigate(`${clientUrl}/posts/1`)
    await waitFor(async () => (await page.evaluate(`document.querySelectorAll('time.relative-time').length`)) > 0, {
      label: '帖子详情页出现 <time>',
      timeoutMs: 30000,
    })
    await sleep(400)

    // ---- T1 语义与绝对时间 ----
    const first = await page.evaluate(SETTLE)
    record(
      'T1-a 帖子详情的时间是 <time datetime> 且 datetime 保留原值',
      Boolean(first && first.datetime && /T\d{2}:\d{2}/.test(first.datetime)),
      `datetime=${JSON.stringify(first?.datetime)}`,
    )
    record(
      'T1-b title 与 aria-label 都是**精确绝对时间**（YYYY-MM-DD HH:mm）',
      Boolean(first && ABS_RE.test(first.title ?? '') && ABS_RE.test(first.aria ?? '')),
      `title=${JSON.stringify(first?.title)}；aria-label=${JSON.stringify(first?.aria)}`,
    )
    record(
      'T1-c 屏幕上的字是六档之一（刚刚/N 分钟前/N 小时前/N 天前/M月D日/YYYY年M月D日）',
      Boolean(first && TIER_RE.test(first.text ?? '')),
      `text=${JSON.stringify(first?.text)}`,
    )

    // ---- T2 全站单一刻度 ----
    // 帖子详情页只有 1 个相对时间，列表页有多个；两处都必须是**同一个**定时器。
    await page.navigate(`${clientUrl}/posts`)
    await waitFor(async () => (await page.evaluate(`document.querySelectorAll('time.relative-time').length`)) > 1, {
      label: '帖子列表页出现多个 <time>',
      timeoutMs: 30000,
    })
    await sleep(400)
    const list = await page.evaluate(SETTLE)
    const tickTimers = appTickTimers(list?.timers)
    const devTimers = (list?.timers ?? []).filter((t) => t.ms === 30000 && !/useNowTick/.test(t.stack ?? ''))
    record(
      'T2 列表页有多个相对时间，但应用的 30000ms 刻度定时器**只有 1 个**（全站单一刻度）',
      (list?.count ?? 0) > 1 && tickTimers.length === 1,
      `相对时间元素=${list?.count} 个；应用刻度定时器=${tickTimers.length} 条（栈=${JSON.stringify(tickTimers.map((t) => t.stack))}）；开发期脚手架定时器=${devTimers.length} 条（已剔除）`,
    )

    // ---- T5 数字自己走（刻度到了，文字自己变）----
    await page.navigate(`${clientUrl}/posts/1`)
    await waitFor(async () => (await page.evaluate(`document.querySelectorAll('time.relative-time').length`)) > 0, {
      label: '帖子详情页出现 <time>',
      timeoutMs: 30000,
    })
    await sleep(400)
    const before = await page.evaluate(SETTLE)
    // 让时间真的过去 90 秒（对被测代码而言）。
    await page.evaluate(`window.__prClock.offset += 90_000; 'ok'`)
    // 模拟"30 秒定时器响了一次"：触发登记册里那条 30s 的回调。
    const fired = await page.evaluate(`(() => {
      const t = window.__prTimers.find((x) => x.ms === 30000 && /nowTick/.test(x.stack || ''))
      if (!t) return 'NO-TICK'
      t.fn()
      return 'fired'
    })()`)
    await sleep(300)
    const after = await page.evaluate(SETTLE)
    record(
      'T5 刻度到了之后，文字自己从旧档换到新档（页面开着数字自己走）',
      fired === 'fired' && after?.text !== before?.text && TIER_RE.test(after?.text ?? ''),
      `触发=${fired}；换字前=${JSON.stringify(before?.text)}；换字后=${JSON.stringify(after?.text)}`,
    )

    // ---- T3 隐藏时暂停 ----
    //
    // 判据落在**机制**上而不是"文案恰好没变"：隐藏的那一刻，
    // 刻度的定时器必须被**收掉**（表停了 = 不烧后台 CPU）。
    // 若只断言"文案没变"，一个把定时器留着、只是不更新文案的实现也能蒙混过关。
    const beforeHide = await page.evaluate(SETTLE)
    const liveBefore = appTickTimers(beforeHide?.timers).filter((t) => t.live).length
    await page.evaluate(`window.__prClock.offset += 600_000; window.__prHidden = true; document.dispatchEvent(new Event('visibilitychange')); 'ok'`)
    await sleep(250)
    const afterHide = await page.evaluate(SETTLE)
    const liveAfter = appTickTimers(afterHide?.timers).filter((t) => t.live).length
    record(
      'T3 标签页隐藏时刻度被收掉（document.hidden 暂停，不再烧后台 CPU）',
      liveBefore === 1 && liveAfter === 0,
      `隐藏前活着的刻度定时器=${liveBefore} 条；隐藏后=${liveAfter} 条（时钟已 +10 分钟）`,
    )

    // ---- T4 回可见立刻补算 ----
    //
    // 判据：回可见的那一刻**立刻**把文案更新到当前（后台待了 10 分钟 → 回来就是「13 分钟前」），
    // 而不是等下一个 30 秒刻度。
    await page.evaluate(`window.__prHidden = false; document.dispatchEvent(new Event('visibilitychange')); 'ok'`)
    await sleep(250)
    const resumed = await page.evaluate(SETTLE)
    const liveResumed = appTickTimers(resumed?.timers).filter((t) => t.live).length
    record(
      'T4 重新可见时**立刻**补算（不等下一个 30s），并恢复定时器',
      resumed?.text !== afterHide?.text &&
        TIER_RE.test(resumed?.text ?? '') &&
        liveResumed === 1,
      `恢复前=${JSON.stringify(afterHide?.text)}；恢复后=${JSON.stringify(resumed?.text)}；恢复后活着的刻度定时器=${liveResumed} 条`,
    )

    // ---- T7 悬停有 title（真鼠标）----
    const box = await page.evaluate(`(() => {
      const el = document.querySelector('time.relative-time')
      if (!el) return null
      const r = el.getBoundingClientRect()
      return { x: r.x + r.width / 2, y: r.y + r.height / 2, w: r.width, h: r.height }
    })()`)
    let hovered = null
    if (box) {
      await page.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: box.x, y: box.y })
      await sleep(200)
      hovered = await page.evaluate(`(() => {
        const el = document.elementFromPoint(${box.x}, ${box.y})
        const t = el ? el.closest('time') : null
        return { tag: el ? el.tagName : null, title: t ? t.getAttribute('title') : null, datetime: t ? t.getAttribute('datetime') : null }
      })()`)
    }
    record(
      'T7 鼠标悬停命中的是那个 <time>，且它带精确绝对时间的 title',
      Boolean(hovered && hovered.title && ABS_RE.test(hovered.title)),
      `命中标签=${JSON.stringify(hovered?.tag)}；title=${JSON.stringify(hovered?.title)}`,
    )

    // ---- T6 reduced-motion 下仍换字 ----
    await page.send('Emulation.setEmulatedMedia', {
      features: [{ name: 'prefers-reduced-motion', value: 'reduce' }],
    })
    await page.navigate(`${clientUrl}/posts/1`)
    await waitFor(async () => (await page.evaluate(`document.querySelectorAll('time.relative-time').length`)) > 0, {
      label: 'reduced-motion 下帖子详情页出现 <time>',
      timeoutMs: 30000,
    })
    await sleep(400)
    const rmBefore = await page.evaluate(SETTLE)
    await page.evaluate(`window.__prClock.offset += 120_000; 'ok'`)
    await page.evaluate(`(() => { const t = window.__prTimers.find((x) => x.ms === 30000 && /useNowTick/.test(x.stack || '')); if (t) t.fn(); return 'ok' })()`)
    await sleep(300)
    const rmAfter = await page.evaluate(SETTLE)
    const rmStyle = await page.evaluate(`(() => {
      const el = document.querySelector('time.relative-time')
      if (!el) return null
      const cs = getComputedStyle(el)
      return { animation: cs.animationName, transition: cs.transitionDuration }
    })()`)
    record(
      'T6 reduced-motion 下没有动画，但**仍然换字**（降级不许把更新一起杀掉）',
      rmAfter?.text !== rmBefore?.text && TIER_RE.test(rmAfter?.text ?? ''),
      `换字前=${JSON.stringify(rmBefore?.text)}；换字后=${JSON.stringify(rmAfter?.text)}；动画=${JSON.stringify(rmStyle)}`,
    )
    await page.send('Emulation.setEmulatedMedia', { features: [] })

    // ========== 消息页（删掉私有 shortTime 副本后的落点）==========
    await seedSession(page)
    await page.navigate(`${clientUrl}/messages?peer=2`)
    await waitFor(async () => (await page.evaluate(`document.querySelectorAll('time.relative-time').length`)) > 0, {
      label: '消息页出现 <time>',
      timeoutMs: 30000,
    })
    await sleep(600)
    const msg = await page.evaluate(`(() => {
      const els = [...document.querySelectorAll('time.relative-time')]
      return {
        count: els.length,
        items: els.map((el) => ({
          text: el.textContent.trim(),
          datetime: el.getAttribute('datetime'),
          title: el.getAttribute('title'),
          aria: el.getAttribute('aria-label'),
        })),
      }
    })()`)
    record(
      'T8 消息页（会话行 + 气泡）的时间也走同一个 <time> 组件',
      (msg?.count ?? 0) > 0,
      `<time> 元素=${msg?.count} 个；样例=${JSON.stringify(msg?.items?.slice(0, 3))}`,
    )
    record(
      'T8-b 消息页每个时间的文案是六档之一，且 title 是精确绝对时间',
      (msg?.items ?? []).every((it) => TIER_RE.test(it.text) && ABS_RE.test(it.title ?? '')),
      `逐条=${JSON.stringify((msg?.items ?? []).slice(0, 4))}`,
    )
  } finally {
    try {
      page.close()
    } catch {
      /* 忽略 */
    }
    try {
      browser.close()
    } catch {
      /* 忽略 */
    }
    if (vite) vite.close()
  }
}

await main().catch((err) => {
  record('流程完整执行', false, `中断：${err?.stack ?? err}`)
})

console.log('\n=== 相对时间 / 全站单一刻度 真机验收 ===')
const failed = checks.filter((c) => !c.pass)
for (const c of failed) console.log(`  ✗ ${c.name} — ${c.detail}`)
console.log(`\n判据：${checks.length - failed.length}/${checks.length} 通过`)
console.log(failed.length === 0 && checks.length > 0 ? '判定：PASS' : '判定：FAIL')
await sleep(200)
process.exit(failed.length === 0 && checks.length > 0 ? 0 : 1)
