#!/usr/bin/env node
/**
 * verify-favorites.mjs —— 票 #64 的收藏真机验收（需要真后端实例）。
 *
 * 为什么单独一个脚本，而不并进 verify-post-actions.mjs：
 *   那个脚本判的是**点赞**的三条硬判据（乐观更新、失败回滚、未登录拦截），
 *   它的实例要求是"帖子域在线"。收藏多出两件必须真机才能判的事：
 *
 *     1. **刷新后仍然亮着** —— 这判的是"真的落库了"，而 mock 态（内存集合）
 *        永远会亮。用 mock 验持久化等于用假实现证明真实现。
 *     2. **他人视角无痕迹** —— 这判的是"私人书签"这条产品承诺：换成另一个账号
 *        打开同一篇帖子，收藏位必须是灭的，且**不存在任何公开计数**。
 *
 *   第 2 条尤其不能用 mock：mock 的收藏集合是单个进程内的，换账号根本换不掉。
 *
 * 用法：
 *   VITE_MOCK_POSTS=0 VITE_MOCK_FAVORITES=0 npm --prefix client run build
 *   powershell -File scripts/run-local.ps1 -Port 8899
 *   node test/script/verify-favorites.mjs --server http://127.0.0.1:8899
 *
 * 实例要求（不满足时明确 SKIP、退出码 2，绝不假绿）：
 *   - `PR_OTP_DEV_ECHO=1`（要注册两个账号）；
 *   - 帖子列表非空（本脚本自己发一篇，所以只要帖子域在线即可）。
 *
 * 退出码：全部判据 PASS -> 0；任一 FAIL -> 1；环境不满足 -> 2。
 *
 * 判据：
 *   F1 收藏按钮可点：点一下 aria-pressed 立刻变 true（乐观更新）。
 *   F2 **刷新后仍然亮着**：重新加载页面，同一篇帖子的收藏位仍是 true。
 *   F3 取消收藏：再点一下变 false，刷新后仍是 false。
 *   F4 **他人视角无痕迹**：换第二个账号打开同一篇帖子，收藏位是 false。
 *   F5 没有公开计数：详情与列表里都读不到"被收藏 N 次"这类文案，
 *      且收藏按钮里没有 `.count` 的非空文本（对比点赞按钮是有的）。
 *   F6 列表页与详情页**同源**：列表里点亮后进详情页，那边也是亮的
 *      （两处各查一次服务端会漂移，这条钉住"同一个真源"）。
 */
import { createServer, request as httpRequest } from "node:http"
import { existsSync, readFileSync, statSync } from "node:fs"
import { extname, join, resolve, sep } from "node:path"
import { fileURLToPath } from "node:url"
import { findChrome, openTarget, sleep, startChrome, waitFor } from "./lib/browser.mjs"

const REPO_ROOT = resolve(fileURLToPath(new URL("../..", import.meta.url)))
const DIST = join(REPO_ROOT, "client", "dist")
const BASE_PORT = Number(argOf(process.argv, "port", "9915"))
const SERVER = argOf(process.argv, "server", "http://127.0.0.1:8888").replace(/\/+$/, "")

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

/** 生成一个绝不重复的账号名（符合服务端的用户名规则）。 */
function freshUsername(prefix) {
  return prefix + Date.now().toString(36)
}

/**
 * 走完整注册链路建一个账号（两段式 + 邮箱验证码）。
 *
 * 返回 `{ username, user, accessToken }`；任何一步不满足实例要求就 SKIP ——
 * 这条路径与 verify-post-actions.mjs 的同名段逐字一致（两处都改的时候记得同步）。
 */
async function registerAccount(prefix) {
  const username = freshUsername(prefix)
  const password = "Fav64-pass-1"
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

/** 用某个账号的 token 发一篇帖子（返回 id）。 */
async function createPost(account, title) {
  const r = await fetch(SERVER + "/api/posts", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: "Bearer " + account.accessToken,
    },
    body: JSON.stringify({ title, content: "收藏验收用的帖子正文。" }),
  })
  if (!r.ok) skipEnv("发帖失败（HTTP " + r.status + "）：实例的帖子域不可用")
  const body = await r.json()
  if (!body.id) skipEnv("发帖响应里没有 id")
  return body.id
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

/** 读某个选择器上收藏按钮的状态。 */
function readFav(cdp, scope) {
  const sel = scope + " [data-action=favorite]"
  return cdp.evaluate(
    "(() => { const b = document.querySelector(" + JSON.stringify(sel) + "); if (!b) return null; " +
      "const c = b.querySelector('.count'); return { pressed: b.getAttribute('aria-pressed'), " +
      "label: b.getAttribute('aria-label') || '', countText: c ? (c.textContent || '').trim() : null, " +
      "countHidden: c ? c.hasAttribute('data-hidden') : null }; })()",
  )
}

async function main() {
  if (!existsSync(join(DIST, "index.html"))) skipEnv("找不到 " + DIST + "/index.html，先 npm --prefix client run build")

  // 后端可达性 + 两个域真的在线（不是 404 能力降级）。
  let probe = null
  try {
    const r = await fetch(SERVER + "/api/posts?limit=1")
    probe = { status: r.status, body: await r.text() }
  } catch (err) {
    skipEnv("连不上 " + SERVER + "（" + String(err) + "）")
  }
  if (!probe || probe.status === 404) skipEnv(SERVER + " 的 /api/posts 回 404：实例没开帖子域")

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, "favorites")
  const cdp = await openTarget(BASE_PORT, "favorites")

  try {
    // ---------- 准备：两个账号 + 一篇帖子 ----------
    const alice = await registerAccount("fav")
    const bob = await registerAccount("favb")
    evidence.accounts = { alice: alice.username, bob: bob.username }
    const postId = await createPost(alice, "收藏验收：" + Date.now())
    evidence.postId = postId
    record("F0 两个账号与一篇帖子就绪", true, "post=" + postId)

    // ---------- F1 收藏（乐观更新） ----------
    await seedSession(cdp, site, alice)
    await cdp.navigate(site.url + "/posts")
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.post-card').length")) > 0,
      { label: "帖子列表渲染出卡片", timeoutMs: 25000 },
    )
    // 找到自己那篇（按标题），点它的收藏位。
    const cardSel = ".post-card"
    const before = await readFav(cdp, cardSel)
    evidence.before = before
    if (!before) skipEnv("列表里找不到收藏按钮（前端产物可能不是 VITE_MOCK_FAVORITES=0 构建的）")
    record("F1a 收藏按钮初始是未收藏", before.pressed === "false", JSON.stringify(before))

    // 只点第一张卡（列表按最新排序，自己刚发的那篇在最前）。
    await cdp.evaluate(
      "(() => { const b = document.querySelector(" + JSON.stringify(cardSel + " [data-action=favorite]") + "); " +
        "if (b) b.click(); return Boolean(b) })()",
    )
    // 立刻读：乐观更新必须在响应回来之前就已生效。
    const instant = await readFav(cdp, cardSel)
    evidence.instant = instant
    record("F1b 点收藏后 aria-pressed 立刻变 true（乐观更新）", instant && instant.pressed === "true", JSON.stringify(instant))

    await sleep(1200)
    const settled = await readFav(cdp, cardSel)
    evidence.settled = settled
    record("F1c 响应落地后仍是已收藏（服务端确认）", settled && settled.pressed === "true", JSON.stringify(settled))

    // ---------- F5 没有公开计数 ----------
    // 收藏按钮的 `.count` 必须是隐藏的空文本（对比点赞按钮是有的）。
    record(
      "F5a 收藏按钮没有公开计数（.count 为空且隐藏）",
      Boolean(settled && (settled.countText === "" || settled.countText === null) && settled.countHidden !== false),
      JSON.stringify(settled),
    )
    const publicText = await cdp.evaluate(
      "(() => { const main = document.querySelector('main') || document.body; " +
        "const t = main.innerText || ''; " +
        "return { hasFavoriteCount: /被收藏|收藏\\s*\\d+\\s*次|\\d+\\s*人收藏/.test(t) }; })()",
    )
    evidence.publicText = publicText
    record("F5b 页面上没有任何公开收藏计数文案", publicText && publicText.hasFavoriteCount === false, JSON.stringify(publicText))

    // ---------- F2 刷新后仍然亮着 ----------
    await cdp.navigate(site.url + "/posts")
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.post-card').length")) > 0,
      { label: "刷新后列表渲染", timeoutMs: 25000 },
    )
    await sleep(600)
    const afterReload = await readFav(cdp, cardSel)
    evidence.afterReload = afterReload
    record("F2 刷新后收藏位仍然亮着（真的落库了）", afterReload && afterReload.pressed === "true", JSON.stringify(afterReload))

    // ---------- F6 列表与详情同源 ----------
    await cdp.navigate(site.url + "/posts/" + postId)
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('[data-testid=action-bar]').length")) > 0,
      { label: "详情页渲染出动作条", timeoutMs: 25000 },
    )
    const detailFav = await readFav(cdp, "[data-testid=action-bar]")
    evidence.detailFav = detailFav
    record("F6 详情页的收藏位与列表同源（也是已收藏）", detailFav && detailFav.pressed === "true", JSON.stringify(detailFav))

    // ---------- F4 他人视角无痕迹 ----------
    await seedSession(cdp, site, bob)
    await cdp.navigate(site.url + "/posts/" + postId)
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('[data-testid=action-bar]').length")) > 0,
      { label: "他人视角详情页渲染", timeoutMs: 25000 },
    )
    const othersView = await readFav(cdp, "[data-testid=action-bar]")
    evidence.othersView = othersView
    record("F4a 他人视角收藏位是灭的（私人书签）", othersView && othersView.pressed === "false", JSON.stringify(othersView))

    // 他人视角也不能从任何地方看出"这篇被收藏过"。
    const othersText = await cdp.evaluate(
      "(() => { const main = document.querySelector('main') || document.body; " +
        "const t = main.innerText || ''; " +
        "return { hasFavoriteCount: /被收藏|收藏\\s*\\d+\\s*次|\\d+\\s*人收藏/.test(t) }; })()",
    )
    evidence.othersText = othersText
    record("F4b 他人视角页面上没有收藏痕迹", othersText && othersText.hasFavoriteCount === false, JSON.stringify(othersText))

    // ---------- F3 取消收藏 ----------
    await seedSession(cdp, site, alice)
    await cdp.navigate(site.url + "/posts/" + postId)
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('[data-testid=action-bar]').length")) > 0,
      { label: "回到本人视角详情页", timeoutMs: 25000 },
    )
    await cdp.evaluate(
      "(() => { const b = document.querySelector('[data-testid=action-bar] [data-action=favorite]'); " +
        "if (b) b.click(); return Boolean(b) })()",
    )
    await sleep(1200)
    const afterUnfav = await readFav(cdp, "[data-testid=action-bar]")
    evidence.afterUnfav = afterUnfav
    record("F3a 再点一下变回未收藏", afterUnfav && afterUnfav.pressed === "false", JSON.stringify(afterUnfav))

    await cdp.navigate(site.url + "/posts/" + postId)
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('[data-testid=action-bar]').length")) > 0,
      { label: "取消后刷新详情页", timeoutMs: 25000 },
    )
    await sleep(600)
    const unfavReload = await readFav(cdp, "[data-testid=action-bar]")
    evidence.unfavReload = unfavReload
    record("F3b 取消后刷新仍是未收藏（真的删了行）", unfavReload && unfavReload.pressed === "false", JSON.stringify(unfavReload))

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
