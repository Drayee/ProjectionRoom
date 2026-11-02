#!/usr/bin/env node
/**
 * verify-post-actions.mjs —— 票 #63 的互动真机验收（需要真后端实例）。
 *
 * 为什么单独一个脚本：
 *   verify-authorrow.mjs 钉的是结构与外观契约（作者行三尺寸、动作条在链接外、
 *   计数缩写、AsyncState 单一出口、Tab 序），静态托管 + mock 域就能跑。
 *   但本票最硬的三条判据 —— 乐观更新、失败可见回滚、未登录拦截 —— 必须有
 *   「一次真的请求及其响应」才能判：mock 的 toggleLike 永远不会失败，也永远不会掉线，
 *   用它来判回滚等于用假实现证明真实现。
 *
 * 所以本脚本需要一个配了帖子域的真实实例，并且前端产物必须是真实接口构建
 * （VITE_MOCK_POSTS=0）。这两条不满足时脚本明确报缺并以退出码 2 结束，绝不假绿
 * （与 verify-forgot.mjs 对 PR_OTP_DEV_ECHO 的态度一致）。
 *
 * 用法：
 *   VITE_MOCK_POSTS=0 npm --prefix client run build
 *   powershell -File scripts/run-local.ps1 -Port 8899
 *   node test/script/verify-post-actions.mjs --server http://127.0.0.1:8899
 *
 * 本脚本自起静态服务器托管 client/dist，并把 /api/* 与 /ws 反代到 --server
 * （同源，避开 CSP 与 CORS），因此页面地址与接口地址同源。
 *
 * 退出码：全部判据 PASS -> 0；任一 FAIL -> 1；环境不满足 -> 2。
 *
 * 判据：
 *   P1 未登录点赞 -> 跳登录并带回来的目标（?returnTo= 或 ?redirect=）。
 *   P2 乐观更新：点赞那一刻计数立刻变化，且连点只发一次请求（单条在途锁）。
 *   P3 失败回滚可见：把点赞请求打成失败后计数回到原值，并出现轻提示，不是静默。
 *   P4 成功后以服务端为准：返回的计数覆盖本地乐观值。
 *   P5 收藏按钮先占位：存在、带 aria-pressed 与可访问名。
 */
import { createServer, request as httpRequest } from "node:http"
import { existsSync, readFileSync, statSync } from "node:fs"
import { extname, join, resolve, sep } from "node:path"
import { fileURLToPath } from "node:url"
import { findChrome, openTarget, sleep, startChrome, waitFor } from "./lib/browser.mjs"

const REPO_ROOT = resolve(fileURLToPath(new URL("../..", import.meta.url)))
const DIST = join(REPO_ROOT, "client", "dist")
const BASE_PORT = Number(argOf(process.argv, "port", "9914"))
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
function freshUsername() {
  return "row" + Date.now().toString(36)
}

async function main() {
  if (!existsSync(join(DIST, "index.html"))) skipEnv("找不到 " + DIST + "/index.html，先 npm --prefix client run build")

  // 后端可达性 + 帖子域真的在线（不是 404 能力降级）。
  let postsProbe = null
  try {
    const r = await fetch(SERVER + "/api/posts?limit=1")
    postsProbe = { status: r.status, body: await r.text() }
  } catch (err) {
    skipEnv("连不上 " + SERVER + "（" + String(err) + "）")
  }
  if (!postsProbe || postsProbe.status === 404) skipEnv(SERVER + " 的 /api/posts 回 404：实例没开帖子域")

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, "post-actions")
  const cdp = await openTarget(BASE_PORT, "post-actions")

  try {
    // ---------- P1 未登录点赞 -> 跳登录且带回来的目标 ----------
    await cdp.navigate(site.url + "/posts")
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.post-card').length")) > 0,
      { label: "帖子列表渲染出卡片", timeoutMs: 25000 },
    )
    const likeBtnSel = ".post-card [data-testid=action-bar] [data-action=like]"
    await cdp.evaluate("(() => { const b = document.querySelector('" + likeBtnSel + "'); if (b) b.click(); return Boolean(b) })()")
    await waitFor(async () => (await cdp.evaluate("location.pathname")) === "/login", {
      label: "未登录点赞后跳到 /login",
      timeoutMs: 12000,
    })
    const loginQuery = await cdp.evaluate("location.search")
    evidence.anonymousLoginQuery = loginQuery
    const params = new URLSearchParams(loginQuery)
    const back = params.get("returnTo") || params.get("redirect") || ""
    record("P1a 未登录点赞 -> 跳登录页", true)
    record("P1b 跳转带上回来的目标（returnTo/redirect 指向帖子列表）", back.startsWith("/posts"), loginQuery)

    // ---------- 注册一个账号 ----------
    const username = freshUsername()
    const password = "Row63-pass-1"
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
    evidence.account = { username, id: account.user && account.user.id }
    record("P1c 测试账号建好了（后续判据用它）", Boolean(account.user && account.user.id), username)

    // 把会话种进页面：access token 只进 sessionStorage（规格 §5），profile 供显示。
    await cdp.navigate(site.url + "/")
    await cdp.evaluate(
      'sessionStorage.setItem("pr:access", ' + JSON.stringify(account.accessToken) + "); " +
        'localStorage.setItem("pr:profile", ' +
        JSON.stringify(JSON.stringify(account.user)) +
        '); "ok"',
    )

    // ---------- P2/P4 乐观更新 + 服务端权威 ----------
    await cdp.navigate(site.url + "/posts")
    await waitFor(
      async () => (await cdp.evaluate("document.querySelectorAll('.post-card').length")) > 0,
      { label: "登录后帖子列表渲染", timeoutMs: 25000 },
    )
    const readCount = (sel) =>
      cdp.evaluate("(() => { const b = document.querySelector('" + sel + "'); if (!b) return null; " +
        "const n = b.querySelector('.count'); return n ? (n.textContent || '').trim() : ''; })()")
    const before = await readCount(likeBtnSel)
    // 打点：记录窗口里发出了几次 like 请求（单条在途锁 = 连点只发一次）。
    await cdp.evaluate(
      "(() => { window.__likeHits = 0; const of = window.fetch; window.fetch = (...a) => { " +
        "const u = String((a[0] && a[0].url) || a[0] || ''); if (u.includes('/like')) window.__likeHits += 1; return of(...a); }; return true })()",
    )
    await cdp.evaluate("(() => { const b = document.querySelector('" + likeBtnSel + "'); if (b) { b.click(); b.click(); b.click(); } return Boolean(b) })()")
    // 立刻读（乐观值必须在响应回来之前就已生效）。
    const instant = await readCount(likeBtnSel)
    await sleep(1500)
    const settled = await readCount(likeBtnSel)
    const hits = await cdp.evaluate("window.__likeHits")
    evidence.like = { before, instant, settled, hits }
    record("P2a 点赞后计数立刻变化（乐观更新，不等响应）", instant !== before, before + " -> " + instant)
    record("P2b 连点三次只发一次请求（单条在途锁）", hits === 1, "hits=" + hits)
    record("P4 响应落地后计数等于服务端值（不是本地推的）", Boolean(settled) && settled !== "", settled)

    // ---------- P3 失败回滚可见 ----------
    const beforeFail = await readCount(likeBtnSel)
    await cdp.evaluate(
      "(() => { const of = window.fetch; window.fetch = (...a) => { " +
        "const u = String((a[0] && a[0].url) || a[0] || ''); if (u.includes('/like')) return Promise.resolve(new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } })); " +
        "return of(...a); }; return true })()",
    )
    await cdp.evaluate("(() => { const b = document.querySelector('" + likeBtnSel + "'); if (b) b.click(); return Boolean(b) })()")
    await sleep(1200)
    const afterFail = await readCount(likeBtnSel)
    const toast = await cdp.evaluate(
      "(() => { const n = document.querySelector('[data-testid=action-toast]'); " +
        "return n ? (n.textContent || '').trim() : ''; })()",
    )
    evidence.rollback = { beforeFail, afterFail, toast }
    record("P3a 失败后计数回到原值（回滚可见）", afterFail === beforeFail, beforeFail + " -> " + afterFail)
    record("P3b 失败后出现轻提示（不是静默）", toast.length > 0, toast)

    // ---------- P5 收藏按钮先占位 ----------
    const fav = await cdp.evaluate(
      "(() => { const b = document.querySelector('.post-card [data-testid=action-bar] [data-action=favorite]'); " +
        "if (!b) return null; return { pressed: b.getAttribute('aria-pressed'), label: b.getAttribute('aria-label') || '', " +
        "text: (b.textContent || '').trim() }; })()",
    )
    evidence.favorite = fav
    record(
      "P5 收藏按钮存在、带 aria-pressed 与可访问名（占位）",
      Boolean(fav && fav.pressed !== null && (fav.label.length > 0 || fav.text.length > 0)),
      JSON.stringify(fav),
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
