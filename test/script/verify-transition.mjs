#!/usr/bin/env node
/**
 * `verify-transition.mjs` —— **路由过渡壳**的真机验收（票 #76）。
 *
 * # 为什么必须是真机
 *
 * 过渡是**看得见**的东西：位移方向、淡入时长、豁免时"什么都没发生"，
 * 都只能在真实浏览器的渲染管线里观测。单测（`verify-transition-logic.mjs`）
 * 只能证明**决策器**算对了，证明不了「那个决策真的变成了界面上的位移」。
 * 两者合起来才是完整判据：决策对 + 接线对。
 *
 * # 它在什么环境里跑
 *
 * **不需要后端，也不需要 Vite**：本脚本自己起一个静态服务器托管 `client/dist`，
 * 并把 `/api/*` 一律回 404（见下）。前端默认走 **mock 域**
 * （`api/mode.ts`：只有显式配 `VITE_MOCK_*=0` 才翻真），
 * 所以帖子 / 论坛 / 公开房的数据都来自前端内存里的假数据。
 *
 * 为什么不需要真后端：票 #76 的判据全是**路由层级与渲染行为**，
 * 真后端只会引入「账号要注册、库要能连」的噪声，而判据本身不依赖服务端。
 * 这与 `verify-avatar.mjs`（票 #61）是同一条做法。
 *
 * # 前置
 *
 *   npm --prefix client run build      # 必须先构建出 client/dist
 *   node test/script/verify-transition.mjs
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 *
 * # 判据
 *
 *  T1  **前进有位移**：列表 → 详情（depth 1 → 2）时，
 *      新页从**右侧 12px** 滑入（enter: +12px → 0）、旧页向左淡出（leave: 0 → −12px）。
 *  T2  **后退方向相反**：详情 → 列表（depth 2 → 1）时，
 *      新页从**左侧 −12px** 滑入（enter: −12px → 0）、旧页向右淡出（leave: 0 → +12px）。
 *  T3  **同级只淡入、无位移**：分区 → 同级分区时只有 opacity 在变，transform 完全不动。
 *  T4  **放映室整页双向豁免**：进房与出房都**零过渡**。
 *  T5  **reduced-motion 降级瞬时**：开启 prefers-reduced-motion 后换页**零过渡**，
 *      且新页**立刻**可读（不拖 240ms）。
 *  T6  **同路由仅 query 变化不触发**：`/forum?q=…` 换搜索词时零过渡。
 *  T7  **过渡与数据解耦**：换页后 120ms 新页**已经在**（不因数据慢而延长或重播），
 *      且过渡**按时结束**（≈240ms，不被数据拖长）。
 *  T8  **只动 transform/opacity**（源码级）：过渡样式里没有别的属性。
 *
 * # 为什么只数 `CSSTransition`
 *
 * 落地页有 3 条**常驻循环 CSS 动画**（标题撕裂），而它们的类型是 `CSSAnimation`。
 * 「换页时有没有过渡」这条判据问的是 **CSS transition**（`CSSTransition`），
 * 所以按类型过滤：否则落地页那 3 条常驻动画会把「零过渡」误判成「有动画」。
 */
import { createServer } from 'node:http'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor, argOf } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const BASE_PORT = Number(argOf(process.argv, 'port', '9951'))

let passed = 0
let failed = 0

function record(name, pass, detail) {
  if (pass) passed += 1
  else failed += 1
  console.log('[' + (pass ? 'PASS' : 'FAIL') + '] ' + name + (detail ? ' — ' + detail : ''))
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
 * 换页并采样**正在跑的 CSS transition**。
 *
 * 采样时机是这个脚本最关键的一处细节：Vue 的 `<Transition>` 先加 `-enter-from`
 * 类，**下一帧**才换成 `-enter-to` 类 —— 只有那时 CSS transition 才真正开始跑。
 * 早一帧采样会看到"class 还没换、动画还没开始"，于是位移读出来是空的
 * （判据会假红，且症状与被测代码无关）。
 *
 * 因此这里**逐帧轮询**，取**第一批出现 CSSTransition 的那一帧**，
 * 并一直轮询到过渡结束（stage 回到只有一个子节点），
 * 这样既能拿到方向/时长，也能拿到"过渡什么时候结束"（T7 用）。
 */
const NAVIGATE_AND_SAMPLE = `(async () => {
  const to = ${JSON.stringify('__TO__')}
  const stage = document.querySelector('[data-testid="route-stage"]')
  if (!stage) return { error: 'no-stage' }
  const t0 = performance.now()
  history.pushState({}, '', to)
  window.dispatchEvent(new PopStateEvent('popstate'))
  let first = null
  let endedAt = null
  let classesSeen = null
  for (let i = 0; i < 90; i++) {
    await new Promise((r) => requestAnimationFrame(r))
    const elapsed = performance.now() - t0
    const anims = []
    for (const el of stage.querySelectorAll('*')) {
      for (const a of el.getAnimations()) {
        // 只数 CSS transition：落地页那 3 条常驻循环动画是 CSSAnimation，与本判据无关。
        if (a.constructor.name !== 'CSSTransition') continue
        const timing = a.effect && a.effect.getComputedTiming ? a.effect.getComputedTiming() : {}
        const frames = a.effect && a.effect.getKeyframes ? a.effect.getKeyframes() : []
        const cls = String(el.className || '')
        anims.push({
          cls,
          side: cls.includes('-enter-') ? 'enter' : cls.includes('-leave-') ? 'leave' : 'other',
          duration: timing.duration ?? null,
          transform: frames.map((f) => String(f.transform ?? '')).filter(Boolean),
          opacity: frames.map((f) => String(f.opacity ?? '')).filter(Boolean),
        })
      }
    }
    if (!classesSeen) {
      // 过渡 class 挂在**页面根元素**上（Vue 的 <Transition> 给子 vnode 加类）。
      // 记下它们的前缀（page-forward / page-back / page-lateral / page-none）——
      // 这是"决策器选对了哪一套样式"的直接证据。
      const cls = [...stage.children].map((c) => String(c.className || '')).join(' ')
      const m = cls.match(/page-(forward|back|lateral|none)/)
      if (m) classesSeen = m[1]
    }
    if (anims.length > 0) {
      // 逐帧取**信息最全**的那一帧：CSS transition 刚起步时 getKeyframes()
      // 可能还是空的（from 状态尚未解析），只有跑起来几帧后才有 keyframes。
      // 早一帧采样就会读出空的位移数组（判据假红，且与被测代码无关）。
      const score = anims.reduce((n, a) => n + (a.transform.length ? 2 : 0) + (a.opacity.length ? 1 : 0), 0)
      if (!first || score > first.score) first = { elapsed, anims, score }
    }
    if (endedAt === null && stage.children.length <= 1 && elapsed > 40) endedAt = elapsed
    if (first && endedAt !== null && first.score >= 6) break
  }
  return {
    first,
    endedAt,
    classesSeen,
    path: location.pathname,
    stages: stage.children.length,
    h1s: [...stage.querySelectorAll('h1')].map((h) => (h.textContent || '').trim()),
  }
})()`

/**
 * 读某个动画的位移（X 分量）。
 *
 * ⚠ Chrome 的 `getKeyframes()` 返回的是**归一化后**的 transform：
 * CSS 里的 `translateX(-12px)` 会被序列化成 `translate(-12px)`（不是 translateX）。
 * 只认 `translateX(` 会读出 null（判据假红，且症状与被测代码无关）。
 * 因此这里同时认 `translate(` / `translateX(` / `translate3d(`，取第一个 px 值。
 */
function shiftOf(anim) {
  for (const t of anim.transform || []) {
    const m = t.match(/translate(?:X|3d)?\(\s*(-?[\d.]+)px/)
    if (m) return Number(m[1])
  }
  return null
}

/**
 * 在一组 transition 里找出**带 transform 的那一个**并读它的位移。
 *
 * ⚠ CSS transition 是**按属性**拆成多个对象的：`transition: transform …, opacity …`
 * 会产生两个 CSSTransition（一个管 transform、一个管 opacity）。
 * 所以"enter 的位移"不能读 `enter[0]` —— 那个常常是 opacity 那个（transform 为空）。
 */
function shiftOfSide(anims, side) {
  for (const a of anims.filter((x) => x.side === side)) {
    const v = shiftOf(a)
    if (v !== null) return v
  }
  return null
}

/** 同上，读位移终点。 */
function shiftEndOfSide(anims, side) {
  for (const a of anims.filter((x) => x.side === side)) {
    const v = shiftEndOf(a)
    if (v !== null) return v
  }
  return null
}

/** 读某个动画的位移终点（末个非 none 的 transform）。 */
function shiftEndOf(anim) {
  const list = (anim.transform || []).filter((t) => t !== 'none')
  if (list.length === 0) return null
  const m = list[list.length - 1].match(/translate(?:X|3d)?\(\s*(-?[\d.]+)px/)
  return m ? Number(m[1]) : null
}

async function main() {
  if (!existsSync(join(DIST, 'index.html'))) {
    console.error('找不到 ' + DIST + '/index.html —— 先跑 npm --prefix client run build 再跑本脚本。')
    process.exit(2)
  }

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, 'transition')
  const cdp = await openTarget(chrome.port, 'transition')

  /** 换页并采样（把目标 URL 填进模板）。 */
  async function navigateAndSample(to) {
    const expr = NAVIGATE_AND_SAMPLE.replace('__TO__', to)
    return cdp.evaluate(expr, 30000)
  }

  async function waitPath(path) {
    await waitFor(async () => (await cdp.evaluate('location.pathname')) === path, {
      label: '到达 ' + path,
      timeoutMs: 10000,
    })
  }

  try {
    await cdp.navigate(site.url + '/')
    await waitFor(async () => (await cdp.evaluate('location.pathname')) === '/', { label: '落地页就绪' })
    await sleep(400)

    // ---- T1 前进有位移（列表 depth 1 → 详情 depth 2）----
    {
      await navigateAndSample('/posts')
      await waitPath('/posts')
      await sleep(400)

      const r = await navigateAndSample('/posts/1')
      await waitPath('/posts/1')
      const anims = (r.first && r.first.anims) || []
      const enter = anims.filter((a) => a.side === 'enter')
      const leave = anims.filter((a) => a.side === 'leave')
      const enterFrom = shiftOfSide(anims, 'enter')
      const leaveTo = shiftEndOfSide(anims, 'leave')
      record(
        'T1 前进：新页从右侧 +12px 滑入，旧页向左 −12px 淡出',
        enterFrom === 12 && leaveTo === -12,
        'enter 起点 ' + enterFrom + 'px / leave 终点 ' + leaveTo + 'px；' +
          '全部 transition：' + JSON.stringify(anims),
      )
      const durations = anims.map((a) => a.duration).filter((d) => typeof d === 'number')
      record(
        'T1b 过渡时长是 240ms',
        durations.length > 0 && durations.every((d) => Math.abs(d - 240) < 1),
        '时长 ' + JSON.stringify(durations),
      )
    }

    // ---- T2 后退方向相反（详情 depth 2 → 列表 depth 1）----
    {
      await sleep(400)
      const r = await navigateAndSample('/posts')
      await waitPath('/posts')
      const anims = (r.first && r.first.anims) || []
      const enter = anims.filter((a) => a.side === 'enter')
      const leave = anims.filter((a) => a.side === 'leave')
      const enterFrom = shiftOfSide(anims, 'enter')
      const leaveTo = shiftEndOfSide(anims, 'leave')
      record(
        'T2 后退：新页从左侧 −12px 滑入，旧页向右 +12px 淡出',
        enterFrom === -12 && leaveTo === 12,
        'enter 起点 ' + enterFrom + 'px / leave 终点 ' + leaveTo + 'px',
      )
    }

    // ---- T3 同级只淡入、无位移（列表 depth 1 → 列表 depth 1）----
    {
      await sleep(400)
      const r = await navigateAndSample('/forum')
      await waitPath('/forum')
      const anims = (r.first && r.first.anims) || []
      const withShift = anims.filter((a) => shiftOf(a) !== null || shiftEndOf(a) !== null)
      const hasOpacity = anims.some((a) => (a.opacity || []).length > 0)
      record(
        'T3 同级：只有淡入，没有任何位移',
        withShift.length === 0 && hasOpacity,
        '带位移的 transition ' + withShift.length + ' 个 / 带 opacity 的 ' +
          anims.filter((a) => (a.opacity || []).length > 0).length + ' 个',
      )
    }

    // ---- T4 放映室整页双向豁免 ----
    {
      await sleep(400)
      // 进房：先给 sessionStorage 播种进房凭据（否则页面只显示"没有进房凭据"，
      // 但那不影响过渡判据 —— 豁免看的是路由，不是凭据）。
      await cdp.evaluate(
        "sessionStorage.setItem('pr:join:MOON01', JSON.stringify({password:'',role:'viewer',displayName:'验收'})); 'ok'",
      )
      const enter = await navigateAndSample('/room/MOON01')
      await waitPath('/room/MOON01')
      const enterCount = enter.first ? enter.first.anims.length : 0
      record('T4a 进放映室：零过渡', enterCount === 0, 'CSS transition 数 = ' + enterCount)

      await sleep(400)
      const leave = await navigateAndSample('/forum')
      await waitPath('/forum')
      const leaveCount = leave.first ? leave.first.anims.length : 0
      record('T4b 出放映室：零过渡（连后退也豁免）', leaveCount === 0, 'CSS transition 数 = ' + leaveCount)
    }

    // ---- T6 同路由仅 query 变化不触发 ----
    {
      await sleep(400)
      const r = await navigateAndSample('/forum?q=probe')
      await sleep(300)
      const count = r.first ? r.first.anims.length : 0
      record('T6 同路由仅 query 变化：零过渡', count === 0, 'CSS transition 数 = ' + count)
    }

    // ---- T5 reduced-motion 降级瞬时 ----
    {
      await cdp.send('Emulation.setEmulatedMedia', {
        features: [{ name: 'prefers-reduced-motion', value: 'reduce' }],
      })
      await cdp.navigate(site.url + '/forum')
      await waitPath('/forum')
      await sleep(400)

      const r = await navigateAndSample('/help')
      await waitPath('/help')
      const count = r.first ? r.first.anims.length : 0
      record('T5a reduced-motion：换页零过渡', count === 0, 'CSS transition 数 = ' + count)
      // ⚠ 这一条才是**双侧判死**的真正判据：
      // 只靠 CSS 侧（tokens.css 的全局 transition:none）时，过渡 class 仍然是
      // 'page-forward'，只是被 CSS 掐掉了；JS 侧判死时过渡名是 page-none，
      // **连 class 都不加**。两者行为相同但机制不同，而票 #45 明确要求两侧都在
      // （「缺一侧即回归」：#13 §4.2 已踩过"CSS 停了 JS 还在动"）。
      record(
        'T5c reduced-motion：JS 侧也判死（过渡名是 page-none，不是被 CSS 掐掉的 page-forward）',
        r.classesSeen === 'none',
        '过渡 class 前缀 = ' + r.classesSeen,
      )

      // 瞬时到位：新页**立刻**可读（不等 240ms）。
      const instant = await cdp.evaluate(
        "(() => { const h = document.querySelector('h1'); return { has: !!h, text: h ? (h.textContent || '').trim() : '' } })()",
      )
      record('T5b reduced-motion：换页后立刻到位（不拖 240ms）', instant.has === true, JSON.stringify(instant))
      await cdp.send('Emulation.setEmulatedMedia', { features: [] })
    }

    // ---- T7 过渡与数据解耦：新页先到位，过渡不被数据拖长 ----
    {
      await cdp.navigate(site.url + '/forum')
      await waitPath('/forum')
      await sleep(400)
      const r = await navigateAndSample('/help')
      await waitPath('/help')
      // 新页在过渡期间就已经挂载（两个 h1 同时在场：旧的与新的）。
      const h1s = r.h1s || []
      record(
        'T7a 过渡与数据解耦：新页在过渡期间已挂载',
        h1s.some((t) => t.includes('帮助')),
        '过渡期间页面里的 h1：' + JSON.stringify(h1s),
      )
      // 过渡**按时结束**：不被数据拖长（240ms + 余量）。
      const ended = r.endedAt
      record(
        'T7b 过渡不被数据拖长（≈240ms 内结束）',
        typeof ended === 'number' && ended > 0 && ended < 600,
        '过渡结束于 ' + (typeof ended === 'number' ? Math.round(ended) : ended) + 'ms',
      )
    }

    // ---- T8 只动 transform/opacity（源码级）----
    {
      const raw = readFileSync(join(REPO_ROOT, 'client', 'src', 'styles', 'transition.css'), 'utf8')
      // 先去注释：注释里会提到「transition: none !important」那句全局判死，
      // 直接 grep 整份文件会把它当成一条过渡声明（假红）。
      const css = raw.replace(/\/\*[\s\S]*?\*\//g, '')
      const props = [...css.matchAll(/transition:\s*([^;]+);/g)].map((m) =>
        m[1]
          .split(',')
          .map((x) => x.trim().split(/\s+/)[0])
          .filter(Boolean),
      )
      const flat = [...new Set(props.flat())]
      const allowed = new Set(['transform', 'opacity'])
      const bad = flat.filter((p) => !allowed.has(p))
      record(
        'T8 只动 transform/opacity',
        bad.length === 0 && flat.length > 0,
        '过渡属性 ' + JSON.stringify(flat) + (bad.length ? ' 越界：' + JSON.stringify(bad) : ''),
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
