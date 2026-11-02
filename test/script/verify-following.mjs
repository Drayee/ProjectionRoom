#!/usr/bin/env node
/**
 * verify-following.mjs —— 票 #66 的关注流真机验收（需要真后端实例）。
 *
 * 为什么必须有真后端：关注流的全部判据都建立在"服务端按 follows 边过滤"这件事上。
 * mock 域的关注边是进程内一个 Set，它**永远**会按预期过滤 —— 用 mock 证明
 * `WHERE author_id IN (SELECT followee_id FROM follows ...)` 写对了，等于什么都没证。
 *
 * 判据（F1–F8）：
 *   F1  游客点「关注」pill：**不跳走**（还停在 /posts），就地渲染登录引导。
 *   F2  已登录但 0 关注：空态「还没有关注任何人」+ 深链 `/friends?focus=search`。
 *   F3  关注 alice 后：alice 的帖进流，未关注的 bob 的帖不进流。
 *   F4  板块主题串（animeId 的帖子）同样进流 —— 关注的是"人"，不是"帖子类型"。
 *   F5  取关即出：取关后刷新，alice 的帖从流里消失（不用等缓存过期）。
 *   F6  普通列表无泄漏：关注流不改变 latest 的口径（bob 的帖仍在 latest 里）。
 *   F7  三个 pill 的 `?sort=` 同步进 URL，刷新后选中态保得住。
 *   F8  从详情页后退回列表，选中态还在（URL 是唯一真源）。
 *       刻意不判"pill 之间来回后退"：切换 pill 走 `router.replace`，那是刻意的 ——
 *       每点一次 tab 都塞一条历史，用户按后退会被困在同一页里。
 *
 * 用法：
 *   cd client && VITE_MOCK_POSTS=0 VITE_MOCK_FOLLOWS=0 npx vite build
 *   node test/script/verify-following.mjs --server http://127.0.0.1:8899 --anime <番剧 id>
 *
 * 实例要求（不满足时明确 SKIP、退出码 2，绝不假绿）：
 *   - `PR_OTP_DEV_ECHO=1`（要注册三个账号）；
 *   - 至少存在一个番剧（`--anime <id>` 显式给，或 `/api/animes` 里能查到一个）。
 *     没有番剧就判不了 F4，而 F4 正是"主题串进流"这条验收的核心。
 *
 * 退出码：全部判据 PASS -> 0；任一 FAIL -> 1；环境不满足 -> 2。
 */
import { createServer, request as httpRequest } from "node:http"
import { existsSync, readFileSync, statSync } from "node:fs"
import { extname, join, resolve, sep } from "node:path"
import { fileURLToPath } from "node:url"
import { findChrome, openTarget, sleep, startChrome, waitFor } from "./lib/browser.mjs"

const REPO_ROOT = resolve(fileURLToPath(new URL("../..", import.meta.url)))
const DIST = join(REPO_ROOT, "client", "dist")
const BASE_PORT = Number(argOf(process.argv, "port", "9916"))
const SERVER = argOf(process.argv, "server", "http://127.0.0.1:8888").replace(/\/+$/, "")
const ANIME_ARG = argOf(process.argv, "anime", "")

function argOf(argv, name, fallback) {
  const i = argv.indexOf("--" + name)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}

let passed = 0
let failed = 0
const evidence = {}

function record(name, pass, detail) {
  if (pass) passed += 1
  else failed += 1
  console.log("[" + (pass ? "PASS" : "FAIL") + "] " + name + (detail ? " — " + detail : ""))
}

function skipEnv(reason) {
  console.error("[SKIP] 环境不满足：" + reason)
  process.exit(2)
}

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".woff2": "font/woff2",
  ".json": "application/json; charset=utf-8",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".map": "application/json; charset=utf-8",
}

/** 把 /api/* 与 /ws 反代到真后端（同源，避开 CSP/CORS 噪声）。 */
function proxy(req, res) {
  const target = new URL(SERVER)
  const upstream = httpRequest(
    {
      hostname: target.hostname,
      port: target.port,
      path: req.url,
      method: req.method,
      headers: { ...req.headers, host: target.host, origin: SERVER },
    },
    (up) => {
      res.writeHead(up.statusCode == null ? 502 : up.statusCode, up.headers)
      up.pipe(res)
    },
  )
  upstream.on("error", () => {
    if (!res.headersSent) res.writeHead(502, { "Content-Type": "application/json" })
    res.end(JSON.stringify({ error: "upstream unreachable" }))
  })
  req.pipe(upstream)
}

function startStatic(dir) {
  return new Promise((done) => {
    const server = createServer((req, res) => {
      const path = new URL(req.url || "/", "http://127.0.0.1").pathname
      if (path.startsWith("/api/") || path === "/ws") {
        proxy(req, res)
        return
      }
      let file = resolve(dir, "." + path)
      if (file !== dir && !file.startsWith(dir + sep)) {
        res.writeHead(403)
        res.end()
        return
      }
      if (existsSync(file) && statSync(file).isDirectory()) file = join(file, "index.html")
      if (!existsSync(file) || !statSync(file).isFile()) file = join(dir, "index.html")
      const body = readFileSync(file)
      res.writeHead(200, {
        "Content-Type": MIME[extname(file)] || "application/octet-stream",
        "Content-Length": body.length,
      })
      res.end(body)
    })
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address()
      done({ url: "http://127.0.0.1:" + port, close: () => server.close() })
    })
  })
}

function freshUsername(prefix) {
  return prefix + Date.now().toString(36) + Math.floor(Math.random() * 1000).toString(36)
}

/** 走完整注册链路建一个账号（两段式 + 邮箱验证码）。 */
async function registerAccount(prefix) {
  const username = freshUsername(prefix)
  const password = "Flow66-pass-1"
  const reg = await fetch(SERVER + "/api/auth/register/start", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() },
    body: JSON.stringify({ username, password }),
  })
  if (!reg.ok) skipEnv("注册失败（HTTP " + reg.status + "）")
  const regStart = await reg.json()
  const email = username + "@example.test"
  const sent = await fetch(SERVER + "/api/auth/register/email", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() },
    body: JSON.stringify({ ticket: regStart.ticket, email }),
  })
  if (!sent.ok) skipEnv("发验证码失败（HTTP " + sent.status + "）：需要 PR_OTP_DEV_ECHO=1 的实例")
  const sentBody = await sent.json().catch(() => ({}))
  const code = sentBody.devCode || sentBody.dev_code || ""
  if (!code) skipEnv("响应里没有 devCode —— 实例没开 PR_OTP_DEV_ECHO=1，拿不到验证码")
  const verified = await fetch(SERVER + "/api/auth/register/verify", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() },
    body: JSON.stringify({ ticket: regStart.ticket, code }),
  })
  if (!verified.ok) skipEnv("建号失败（HTTP " + verified.status + "）")
  const account = await verified.json()
  if (!account.user || !account.user.id) skipEnv("注册响应里没有 user.id")
  return { username, user: account.user, accessToken: account.accessToken }
}

/** 用某个账号的 token 发一篇帖子（普通帖或板块主题串），返回 id。 */
async function createPost(account, title, animeId) {
  const payload = { title, content: "关注流验收用的正文。" }
  if (animeId != null) payload.animeId = animeId
  const r = await fetch(SERVER + "/api/posts", {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + account.accessToken },
    body: JSON.stringify(payload),
  })
  if (!r.ok) {
    const text = await r.text().catch(() => "")
    skipEnv("发帖失败（HTTP " + r.status + "）：" + text.slice(0, 160))
  }
  const body = await r.json()
  if (!body.id) skipEnv("发帖响应里没有 id")
  return body.id
}

async function followAs(viewer, targetUserId, on) {
  const r = await fetch(SERVER + "/api/follows" + (on ? "" : "/" + targetUserId), {
    method: on ? "POST" : "DELETE",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + viewer.accessToken },
    body: on ? JSON.stringify({ userId: targetUserId }) : undefined,
  })
  if (!r.ok) skipEnv("关注/取关失败（HTTP " + r.status + "）：关注域不可用")
}

/** 把会话种进页面：access token 只进 sessionStorage（规格 §5），profile 供显示。 */
async function seedSession(cdp, site, account) {
  await cdp.navigate(site.url + "/")
  await cdp.evaluate(
    'sessionStorage.setItem("pr:access", ' + JSON.stringify(account.accessToken) + "); " +
      'localStorage.setItem("pr:profile", ' + JSON.stringify(JSON.stringify(account.user)) + "); " +
      '"ok"',
  )
}

/** 清掉会话，回到游客态。 */
async function clearSession(cdp, site) {
  await cdp.navigate(site.url + "/")
  await cdp.evaluate('sessionStorage.clear(); localStorage.clear(); "ok"')
}

/** 读当前页的 URL、选中 pill、列表标题、以及 AsyncState 的三元组。
 *
 * ⚠️ 注意：`cdp.evaluate` 的脚本里**不要写 `//` 行注释** —— 那段脚本会被折叠，
 * 行注释会把同一行后面的代码一起吃掉（踩过：`ReferenceError: cta is not defined`）。
 * 需要说明就写在这里。
 *
 * `.empty-cta` 本身就是那个链接（RouterLink 带 class），不是它的容器。
 */
function readPage(cdp) {
  return cdp.evaluate(`(() => {
    const cards = Array.from(document.querySelectorAll('.post-card'));
    const titles = cards.map((c) => ((c.querySelector('.card-title') || {}).textContent || '').trim());
    const pills = Array.from(document.querySelectorAll('.tabs [data-sort]')).map((b) => ({
      sort: b.getAttribute('data-sort'),
      selected: b.getAttribute('aria-selected') === 'true',
    }));
    const state = document.querySelector('[data-testid=async-state]');
    const cta = state ? state.querySelector('.empty-cta') : null;
    return {
      search: location.search,
      path: location.pathname,
      titles,
      pills,
      state: state ? state.getAttribute('data-state') : null,
      stateText: state ? (state.innerText || '').trim() : null,
      ctaText: cta ? (cta.textContent || '').trim() : null,
      ctaHref: cta ? cta.getAttribute('href') : null,
    };
  })()`)
}

async function openPosts(cdp, site, query) {
  await cdp.navigate(site.url + "/posts" + (query || ""))
  await waitFor(
    async () => {
      const p = await readPage(cdp)
      return p.titles.length > 0 || p.state !== null
    },
    { label: "帖子页渲染出卡片或状态位", timeoutMs: 25000 },
  )
  await sleep(500)
  return readPage(cdp)
}

async function main() {
  if (!existsSync(join(DIST, "index.html"))) skipEnv("找不到 " + DIST + "/index.html，先构建前端")

  let probe
  try {
    const r = await fetch(SERVER + "/api/posts?limit=1")
    probe = { status: r.status, body: await r.text() }
  } catch (err) {
    skipEnv("连不上 " + SERVER + "（" + String(err) + "）")
  }
  if (!probe || probe.status === 404) skipEnv(SERVER + " 的 /api/posts 回 404：实例没开帖子域")

  // 番剧：优先用 --anime，否则从目录里取一个；都没有就 SKIP（F4 判不了）。
  let animeId = ANIME_ARG ? Number(ANIME_ARG) : 0
  if (!animeId) {
    const r = await fetch(SERVER + "/api/animes?limit=1")
    const body = await r.json().catch(() => ({}))
    const items = Array.isArray(body.items) ? body.items : []
    if (items.length > 0) animeId = Number(items[0].id)
  }
  if (!animeId || !Number.isFinite(animeId) || animeId <= 0) {
    skipEnv("实例里没有番剧目录项，判不了「主题串进流」（F4）；请用 --anime <id> 指定一个，或先建一个番剧")
  }
  evidence.animeId = animeId

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, "following")
  const cdp = await openTarget(BASE_PORT, "following")

  try {
    // ---------- 准备：三个账号（me 是观众，alice/bob 是内容作者） ----------
    const me = await registerAccount("flowme")
    const alice = await registerAccount("flowa")
    const bob = await registerAccount("flowb")
    evidence.accounts = { me: me.username, alice: alice.username, bob: bob.username }

    const stamp = Date.now().toString(36)
    const aliceTitle = "A 的帖-" + stamp
    const bobTitle = "B 的帖-" + stamp
    const threadTitle = "A 的主题串-" + stamp
    await createPost(alice, aliceTitle)
    await createPost(bob, bobTitle)
    await createPost(alice, threadTitle, animeId)
    record("F0 三个账号 + 两篇普通帖 + 一篇主题串就绪", true, "anime=" + animeId)

    // ---------- F1 游客点「关注」pill：不跳走，就地登录引导 ----------
    await clearSession(cdp, site)
    await openPosts(cdp, site, "")
    await cdp.evaluate("(() => { const b = document.querySelector('.tabs [data-sort=following]'); if (b) b.click(); return Boolean(b) })()")
    await sleep(900)
    const guest = await readPage(cdp)
    evidence.guest = guest
    record("F1a 游客点关注 pill 后仍停在 /posts（没被弹到登录页）", guest.path === "/posts", "path=" + guest.path)
    record("F1b URL 保住了 ?sort=following", guest.search.includes("sort=following"), guest.search)
    record(
      "F1c 就地渲染登录引导（含「去登录」且带 redirect）",
      Boolean(guest.stateText && guest.stateText.includes("登录")) && guest.ctaText === "去登录" &&
        Boolean(guest.ctaHref && guest.ctaHref.includes("/login") && guest.ctaHref.includes("redirect")),
      JSON.stringify({ text: guest.stateText, cta: guest.ctaText, href: guest.ctaHref }),
    )

    // ---------- F2 已登录 0 关注：空态 + 深链找人 ----------
    await seedSession(cdp, site, me)
    const empty = await openPosts(cdp, site, "?sort=following")
    evidence.empty = empty
    record(
      "F2a 0 关注时空态文案是「还没有关注任何人」",
      Boolean(empty.stateText && empty.stateText.includes("还没有关注任何人")),
      JSON.stringify({ text: empty.stateText, titles: empty.titles }),
    )
    record(
      "F2b 空态带「去找人关注」并深链 /friends?focus=search",
      empty.ctaText === "去找人关注" && Boolean(empty.ctaHref && empty.ctaHref.includes("/friends?focus=search")),
      JSON.stringify({ cta: empty.ctaText, href: empty.ctaHref }),
    )

    // ---------- F3 关注 alice 后：她的帖进流，bob 的不进 ----------
    await followAs(me, alice.user.id, true)
    const flowed = await openPosts(cdp, site, "?sort=following")
    evidence.flowed = flowed
    record("F3a 关注后 alice 的普通帖进流", flowed.titles.includes(aliceTitle), JSON.stringify(flowed.titles))
    record("F3b 未关注的 bob 的帖不在流里", !flowed.titles.includes(bobTitle), JSON.stringify(flowed.titles))
    record("F4 alice 的板块主题串也进流（关注的是人不是帖子类型）", flowed.titles.includes(threadTitle), JSON.stringify(flowed.titles))

    // ---------- F6 普通列表口径不变（无泄漏） ----------
    const latest = await openPosts(cdp, site, "")
    evidence.latest = latest
    record("F6a latest 里仍然看得到 bob 的帖（关注流不改变普通列表口径）", latest.titles.includes(bobTitle), JSON.stringify(latest.titles))
    record("F6b latest 里也有 alice 的帖", latest.titles.includes(aliceTitle), JSON.stringify(latest.titles))

    // ---------- F7 三个 pill 的 ?sort= 同步 ----------
    const pillsOk =
      Array.isArray(latest.pills) &&
      latest.pills.length === 3 &&
      latest.pills[0].sort === "latest" && latest.pills[0].selected &&
      latest.pills[1].sort === "hot" && !latest.pills[1].selected &&
      latest.pills[2].sort === "following" && !latest.pills[2].selected
    record("F7a 三个 pill 顺序 latest/hot/following 且默认选中 latest", pillsOk, JSON.stringify(latest.pills))

    await cdp.evaluate("(() => { const b = document.querySelector('.tabs [data-sort=hot]'); if (b) b.click(); return Boolean(b) })()")
    await sleep(800)
    const hot = await readPage(cdp)
    record("F7b 点「热门」后 URL 写进 sort=hot", hot.search.includes("sort=hot"), hot.search)
    record(
      "F7c 选中态跟着 URL 走（hot 被选中、latest 不选）",
      hot.pills.some((p) => p.sort === "hot" && p.selected) && hot.pills.some((p) => p.sort === "latest" && !p.selected),
      JSON.stringify(hot.pills),
    )

    // 刷新后选中态保得住（URL 是唯一真源，不是内存 ref）。
    await cdp.navigate(site.url + "/posts?sort=following")
    await waitFor(async () => (await readPage(cdp)).pills.length === 3, { label: "刷新后 pill 渲染", timeoutMs: 20000 })
    await sleep(500)
    const reloaded = await readPage(cdp)
    record(
      "F7d 直接打开 ?sort=following 时关注 pill 被选中（刷新/分享都成立）",
      reloaded.pills.some((p) => p.sort === "following" && p.selected) &&
        reloaded.pills.some((p) => p.sort === "latest" && !p.selected),
      JSON.stringify(reloaded.pills),
    )

    // ---------- F8 后退键：从详情页退回列表，pill 选中态还在 ----------
    //
    // 这里刻意**不**判"pill 之间来回后退" —— 切换 pill 走 `router.replace`，
    // 那是刻意的：每次点 tab 都塞一条历史，用户按后退会被困在同一个页面里。
    // 后退键真正要判的是"离开列表再回来，选中态没丢"，而那正是
    // "URL 是唯一真源"的可观察形态。
    await openPosts(cdp, site, "?sort=following")
    await cdp.evaluate(
      "(() => { const a = document.querySelector('.post-card .card-link'); if (a) a.click(); return Boolean(a) })()",
    )
    await waitFor(async () => (await cdp.evaluate("location.pathname")).startsWith("/posts/"), {
      label: "点卡片进了详情页",
      timeoutMs: 20000,
    })
    await cdp.evaluate("history.back(); 'ok'")
    await sleep(1200)
    const back = await readPage(cdp)
    evidence.back = back
    record(
      "F8 从详情页后退回到关注流，pill 选中态还在（URL 是唯一真源）",
      back.path === "/posts" &&
        back.search.includes("sort=following") &&
        back.pills.some((p) => p.sort === "following" && p.selected),
      JSON.stringify({ path: back.path, search: back.search, pills: back.pills }),
    )

    // ---------- F5 取关即出 ----------
    await followAs(me, alice.user.id, false)
    const afterUnfollow = await openPosts(cdp, site, "?sort=following")
    evidence.afterUnfollow = afterUnfollow
    record(
      "F5 取关后 alice 的帖从流里消失（不用等缓存）",
      !afterUnfollow.titles.includes(aliceTitle) && !afterUnfollow.titles.includes(threadTitle),
      JSON.stringify(afterUnfollow.titles),
    )
    record(
      "F5b 取关后回到「还没有关注任何人」空态",
      Boolean(afterUnfollow.stateText && afterUnfollow.stateText.includes("还没有关注任何人")),
      JSON.stringify(afterUnfollow.stateText),
    )

    console.log("\n通过 " + passed + " / 失败 " + failed)
    console.log(JSON.stringify(evidence, null, 2))
    process.exitCode = failed === 0 ? 0 : 1
  } finally {
    cdp.close()
    chrome.close()
    site.close()
  }
}

await main()
