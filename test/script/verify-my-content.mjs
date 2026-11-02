#!/usr/bin/env node
/**
 * verify-my-content.mjs —— 票 #70「我的帖管理 + 我的放映室」的真机验收。
 *
 * # 为什么必须有真后端
 *
 * 本票的两条核心判据都在服务端：
 *   1. **删帖只认作者**（I3：授权 100% 在服务端）—— mock 域的 `deletePost` 是
 *      一个 if 判断，用它证明服务端写对了等于什么都没证；
 *   2. **我的放映室是 owner 全口径**（含未公开与已关闭）—— 这条口径与
 *      「他人档案页只列公开且未关闭」是一对，必须用同一份真实数据同时看两边，
 *      才能证明"私密房只在 /me 出现"。
 *
 * # 判据
 *
 *   D0  一个账号 + 两篇自己的帖 + 一间公开放映室 + 一间私密放映室就绪。
 *   D1  `/me` 概览的帖子块**有删除按钮**（`[data-testid=delete-post]`），
 *       且按钮数量等于帖子数 —— 不是只给第一条。
 *   D2  点删除后，那条帖**立刻从列表消失**（乐观移除），且提示为成功。
 *   D3  刷新后仍然不在（真落库，不是只改了内存）。
 *   D4  被删的帖详情页对别人是 404（不是"还在但看不见"）。
 *   D5  **别人删不掉我的帖**：bob 直接打 `DELETE /api/posts/:id` → 403，
 *       且帖子仍在（服务端拒了，不是静默吞掉）。
 *   D6  `/me` 的「我的放映室」块列出**私密房**并标「私密」——
 *       这是它与他人档案页那块最关键的区别。
 *   D7  同一间私密房**不出现**在他人档案页（`GET /api/users/:id/rooms`）与
 *       公开房列表里 —— 两边口径必须互补。
 *   D8  `GET /api/rooms/mine` 未登录 → 401（不泄露任何人的房）。
 *
 * # 实例要求（不满足时明确 SKIP、退出码 2，绝不假绿）
 *
 *   - `PR_OTP_DEV_ECHO=1`（要注册两个账号）；
 *   - 注册/登录限速放宽（一轮建两个号 + 若干次写请求）；
 *   - 前端产物必须是**真实接口**构建（见用法）。
 *
 * 用法：
 *   cd client && VITE_MOCK_POSTS=0 VITE_MOCK_USERS=0 VITE_MOCK_ROOMS=0 npx vite build
 *   node test/script/verify-my-content.mjs --server http://127.0.0.1:8899
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
const BASE_PORT = Number(argOf(process.argv, "port", "9919"))
const SERVER = argOf(process.argv, "server", "http://127.0.0.1:8888").replace(/\/+$/, "")

function argOf(argv, name, fallback) {
  const i = argv.indexOf("--" + name)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}

let passed = 0
let failed = 0
let skipped = 0
const evidence = {}

function record(name, pass, detail) {
  if (pass) passed += 1
  else failed += 1
  console.log("[" + (pass ? "PASS" : "FAIL") + "] " + name + (detail ? " — " + detail : ""))
}

function skipOne(name, reason) {
  skipped += 1
  console.log("[SKIP] " + name + " —— " + reason)
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

function authHeaders(account) {
  return { "Content-Type": "application/json", Authorization: "Bearer " + account.accessToken }
}

/** 走完整注册链路建一个账号（两段式 + 邮箱验证码）。 */
async function registerAccount(prefix) {
  const username = freshUsername(prefix)
  const password = "Myc70-pass-1"
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
  return { username, password, user: account.user, accessToken: account.accessToken }
}

async function createPost(account, title) {
  const r = await fetch(SERVER + "/api/posts", {
    method: "POST",
    headers: authHeaders(account),
    body: JSON.stringify({ title, content: "我的内容管理验收用的正文。" }),
  })
  if (!r.ok) {
    const text = await r.text().catch(() => "")
    skipEnv("发帖失败（HTTP " + r.status + "）：" + text.slice(0, 160))
  }
  const body = await r.json()
  if (!body.id) skipEnv("发帖响应里没有 id")
  return body.id
}

/**
 * 建一间房并把元数据写成期望的公开性。
 *
 * 元数据是**异步**落库的（I4）：先轮询到 PATCH 成功为止，再断言回显 ——
 * 直接 PATCH 会撞上"内存里有房、库里还没这一行"的窗口（verify-me-favorites 踩过）。
 */
async function createRoom(account, title, isPublic) {
  const r = await fetch(SERVER + "/api/rooms", {
    method: "POST",
    headers: authHeaders(account),
    body: JSON.stringify({}),
  })
  if (!r.ok) skipEnv("建房失败（HTTP " + r.status + "）")
  const body = await r.json()
  const roomId = body.roomId
  if (!roomId) skipEnv("建房响应里没有 roomId")

  let ok = false
  for (let i = 0; i < 40 && !ok; i += 1) {
    const p = await fetch(SERVER + "/api/rooms/" + encodeURIComponent(roomId) + "/meta", {
      method: "PATCH",
      headers: authHeaders(account),
      body: JSON.stringify({ title, isPublic }),
    })
    if (p.ok) {
      const b = await p.json().catch(() => ({}))
      if (b.isPublic === isPublic) ok = true
      else await sleep(120)
    } else {
      await sleep(120)
    }
  }
  if (!ok) skipEnv("放映室元数据没能写成 isPublic=" + isPublic)
  return roomId
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

/**
 * 读 /me 概览的可观测状态。
 *
 * ⚠️ `cdp.evaluate` 的脚本里**不要写 `//` 行注释** —— 那段脚本会被折叠，
 * 行注释会把同一行后面代码一起吃掉（verify-following 踩过）。
 */
function readOverview(cdp) {
  return cdp.evaluate(`(() => {
    const txt = (el) => el ? (el.textContent || '').replace(/\\\\s+/g, ' ').trim() : null
    const postsBlock = document.querySelector('#author-posts-head');
    const roomsBlock = document.querySelector('#me-rooms-head');
    const postRows = postsBlock
      ? Array.from(postsBlock.closest('section').querySelectorAll('.row-title')).map(txt)
      : [];
    const deleteBtns = Array.from(document.querySelectorAll('[data-testid=delete-post]'));
    const roomSection = roomsBlock ? roomsBlock.closest('section') : null;
    const roomRows = roomSection
      ? Array.from(roomSection.querySelectorAll('.row')).map((row) => ({
          text: txt(row),
          badges: Array.from(row.querySelectorAll('.badge')).map(txt),
        }))
      : [];
    return {
      path: location.pathname + location.search,
      heads: Array.from(document.querySelectorAll('h1, h2')).map(txt),
      hasPostsBlock: Boolean(postsBlock),
      postRows: postRows,
      deleteCount: deleteBtns.length,
      hasRoomsBlock: Boolean(roomsBlock),
      roomRows: roomRows,
      roomState: roomSection && roomSection.querySelector('[data-testid=async-state]')
        ? roomSection.querySelector('[data-testid=async-state]').getAttribute('data-state')
        : null,
      body: (document.body.innerText || '').slice(0, 800),
    };
  })()`)
}

async function openMe(cdp, site) {
  await cdp.navigate(site.url + "/me")
  await waitFor(
    async () => {
      const o = await readOverview(cdp)
      return o.hasPostsBlock && (o.postRows.length > 0 || o.deleteCount > 0 || (o.body || "").length > 0)
    },
    { label: "/me 概览渲染出帖子块", timeoutMs: 25000 },
  )
  await sleep(800)
  return readOverview(cdp)
}

/** 等某个帖子标题从 /me 的帖块里消失（乐观移除是异步的）。 */
async function waitGone(cdp, title) {
  return waitFor(
    async () => {
      const o = await readOverview(cdp)
      return !o.postRows.some((t) => t && t.indexOf(title) >= 0)
    },
    { label: "帖子从 /me 列表消失", timeoutMs: 15000 },
  )
    .then(() => true)
    .catch(() => false)
}

async function main() {
  if (!existsSync(join(DIST, "index.html"))) skipEnv("找不到 " + DIST + "/index.html，先构建前端")

  let probe
  try {
    const r = await fetch(SERVER + "/api/posts?limit=1")
    probe = { status: r.status }
  } catch (err) {
    skipEnv("连不上 " + SERVER + "（" + String(err) + "）")
  }
  if (!probe || probe.status === 404) skipEnv(SERVER + " 的 /api/posts 回 404：实例没开帖子域")

  // 先把"端点存在"确认掉：老实例没有 /api/rooms/mine，会落到 NoRoute 的 404。
  const mineProbe = await fetch(SERVER + "/api/rooms/mine")
  if (mineProbe.status === 404) {
    skipEnv(SERVER + " 的 /api/rooms/mine 回 404：实例没有票 #70 的端点")
  }

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, "my-content")
  const cdp = await openTarget(BASE_PORT, "my-content")

  try {
    // ---------- 准备 ----------
    const me = await registerAccount("mycme")
    const bob = await registerAccount("mycbob")
    const stamp = Date.now().toString(36)
    const keepTitle = "留着的那帖-" + stamp
    const dropTitle = "要删掉的那帖-" + stamp
    const keepId = await createPost(me, keepTitle)
    const dropId = await createPost(me, dropTitle)
    const publicRoom = await createRoom(me, "公开验收房-" + stamp, true)
    const privateRoom = await createRoom(me, "私密验收房-" + stamp, false)
    evidence.setup = { me: me.username, bob: bob.username, keepId, dropId, publicRoom, privateRoom }
    record("D0 一个账号 + 两篇帖 + 一间公开房 + 一间私密房就绪", true,
      "keep=" + keepId + " drop=" + dropId + " rooms=" + publicRoom + "," + privateRoom)

    // ---------- D1 删除入口存在 ----------
    await seedSession(cdp, site, me)
    const overview = await openMe(cdp, site)
    evidence.overview = overview
    record(
      "D1 /me 的帖子块有删除按钮，且每条帖都有一个",
      overview.deleteCount >= 2 && overview.deleteCount === overview.postRows.length,
      JSON.stringify({ deleteCount: overview.deleteCount, rows: overview.postRows.length }),
    )

    // ---------- D2 点删除 → 乐观移除 ----------
    const clicked = await cdp.evaluate(`(() => {
      const rows = Array.from(document.querySelectorAll('#author-posts-head'))
        .map((h) => h.closest('section'))
        .filter(Boolean)[0];
      if (!rows) return { ok: false, why: 'no posts section' };
      const li = Array.from(rows.querySelectorAll('.row')).find((r) => {
        const t = r.querySelector('.row-title');
        return t && (t.textContent || '').indexOf(${JSON.stringify(dropTitle)}) >= 0;
      });
      if (!li) return { ok: false, why: 'row not found' };
      const btn = li.querySelector('[data-testid=delete-post]');
      if (!btn) return { ok: false, why: 'no delete button in row' };
      btn.click();
      return { ok: true };
    })()`)
    record("D2a 找到目标帖并点到删除按钮", clicked.ok === true, JSON.stringify(clicked))
    const gone = await waitGone(cdp, dropTitle)
    record("D2b 删除后那条帖立刻从列表消失", gone, JSON.stringify((await readOverview(cdp)).postRows))

    // ---------- D3 刷新后仍然不在 ----------
    const afterReload = await openMe(cdp, site)
    evidence.afterReload = afterReload
    record(
      "D3 刷新后那条帖仍然不在（真落库，不是只改内存）",
      !afterReload.postRows.some((t) => t && t.indexOf(dropTitle) >= 0),
      JSON.stringify(afterReload.postRows),
    )
    record(
      "D3b 没被删的那条帖还在",
      afterReload.postRows.some((t) => t && t.indexOf(keepTitle) >= 0),
      JSON.stringify(afterReload.postRows),
    )

    // ---------- D4 被删帖的详情对别人是 404 ----------
    const detail = await fetch(SERVER + "/api/posts/" + dropId, {
      headers: { Authorization: "Bearer " + bob.accessToken },
    })
    record("D4 被删的帖详情对别人是 404（不是还在但看不见）", detail.status === 404, "status=" + detail.status)

    // ---------- D5 别人删不掉我的帖 ----------
    const forbidden = await fetch(SERVER + "/api/posts/" + keepId, {
      method: "DELETE",
      headers: authHeaders(bob),
    })
    const stillThere = await fetch(SERVER + "/api/posts/" + keepId, {
      headers: { Authorization: "Bearer " + bob.accessToken },
    })
    record(
      "D5a 别人删我的帖 → 403（服务端判 I3，不是静默吞掉）",
      forbidden.status === 403,
      "status=" + forbidden.status,
    )
    record("D5b 被拒之后帖子仍在", stillThere.status === 200, "status=" + stillThere.status)

    // ---------- D6 我的放映室列出私密房 ----------
    record(
      "D6a /me 有「我的放映室」块",
      afterReload.hasRoomsBlock,
      JSON.stringify(afterReload.heads),
    )
    const privateRow = afterReload.roomRows.find((r) => r.text && r.text.indexOf(privateRoom) >= 0)
    evidence.roomRows = afterReload.roomRows
    record(
      "D6b 私密房出现在这一页并标「私密」（这是与档案页那块的关键区别）",
      Boolean(privateRow) && privateRow.badges.indexOf("私密") >= 0,
      JSON.stringify(privateRow || null),
    )
    const publicRow = afterReload.roomRows.find((r) => r.text && r.text.indexOf(publicRoom) >= 0)
    record(
      "D6c 公开房也在，且不带「私密」标记",
      Boolean(publicRow) && publicRow.badges.indexOf("私密") < 0,
      JSON.stringify(publicRow || null),
    )

    // ---------- D7 私密房不进他人档案页、不进公开房列表 ----------
    const authorRooms = await fetch(SERVER + "/api/users/" + me.user.id + "/rooms")
    const authorRoomsBody = await authorRooms.json().catch(() => ({}))
    const authorRoomIds = (authorRoomsBody.items || []).map((r) => r.roomId)
    record(
      "D7a 私密房不出现在他人档案页（对外橱窗只列公开且未关闭的）",
      !authorRoomIds.includes(privateRoom),
      JSON.stringify(authorRoomIds),
    )
    record(
      "D7b 公开房出现在他人档案页（两边口径互补，不是一边全空）",
      authorRoomIds.includes(publicRoom),
      JSON.stringify(authorRoomIds),
    )
    const publicList = await fetch(SERVER + "/api/public-rooms?limit=100")
    const publicBody = await publicList.json().catch(() => ({}))
    const publicIds = (publicBody.items || []).map((r) => r.roomId)
    record(
      "D7c 私密房不出现在公开房列表",
      !publicIds.includes(privateRoom),
      "publicCount=" + publicIds.length,
    )

    // ---------- D8 未登录不能读别人的房 ----------
    const anon = await fetch(SERVER + "/api/rooms/mine")
    record("D8 GET /api/rooms/mine 未登录 → 401", anon.status === 401, "status=" + anon.status)

    console.log("\n判据：" + passed + "/" + (passed + failed) + " 通过" + (skipped ? "（跳过 " + skipped + "）" : ""))
    console.log(JSON.stringify(evidence, null, 2))
    process.exitCode = failed === 0 ? 0 : 1
  } finally {
    cdp.close()
    chrome.close()
    site.close()
  }
}

await main()
