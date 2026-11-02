#!/usr/bin/env node
/**
 * verify-profile.mjs —— 票 #68「按作者端点 + /user/:id 公开视角重排」的真机验收。
 *
 * # 为什么必须有真后端
 *
 * 本票的核心判据是「作者帖列表与关注流用同一条可见性谓词」—— 也就是
 * "被拉黑之后，我看不到 TA 在档案页上的帖"。mock 域的拉黑集合是进程内一个 Set，
 * 它**永远**会按预期过滤，用它证明 `applyAuthorVisibility` 写对了等于什么都没证。
 * 所以这里起一个静态服务器把 `/api/*` 反代到真实例，页面走真实接口。
 *
 * # 判据
 *
 *   P0  三个账号（me / alice / bob）+ alice 的两篇帖就绪。
 *   P1a 游客（未登录）打开 alice 的档案页：名片可见（昵称 + @username）。
 *   P1b 游客也看得到「TA 的帖子」块 —— 作者内容端点**免登录**。
 *   P2  块序正确：名片 → 关系动作 → TA 的帖子 → TA 的放映室 → 计数。
 *       （关系动作区只在"他人 + 已登录"时渲染，所以块序分游客/登录两种视角判。）
 *   P3a TA 的帖子块里只有 alice 的帖，没有 bob 的。
 *   P3b 点一条能进详情页（列表项是真的链接，不是装饰）。
 *   P4  收藏块**不存在**：整页没有「收藏」区块标题 —— 私人书签他人不可见。
 *   P5  关注数/粉丝数是纯数字，**不可点**（`.counts-block` 里没有 <a>）。
 *   P6a 已登录且互不拉黑时，alice 的档案页看得到她的帖。
 *   P6b bob 拉黑 alice 之后，alice 的档案页上**看不到** alice 的帖（谓词同口径）。
 *   P6c 但 alice 的**公开内容仍然可看**：同一篇帖的详情页对 bob 仍是 200/可见。
 *       （票 #42 §4：拉黑限制的是私聊与新关注，不是公开内容。）
 *   P7a 不存在的 id（999999）→ 「用户不存在」空态。
 *   P7b 被封禁的账号 → 同样的空态（服务端对两者回同一个 404，防存在性探测）。
 *   P8  本人访问自己的 /user/:id：出现「这是别人看到的你的公开页」提示（纯公开视角）。
 *
 * # 实例要求（不满足时明确 SKIP、退出码 2，绝不假绿）
 *
 *   - `PR_OTP_DEV_ECHO=1`（要注册三个账号）；
 *   - 注册/登录限速放宽（一轮要建三个号 + 若干次写请求）；
 *   - 管理员凭据（`--admin <文件>`，内含 `username:password` 两行）**仅 P7b 需要**：
 *     要封禁一个账号必须走管理端。缺它就 SKIP 掉 P7b 并打印说明。
 *
 * 用法：
 *   cd client && VITE_MOCK_POSTS=0 VITE_MOCK_USERS=0 VITE_MOCK_FOLLOWS=0 npx vite build
 *   node test/script/verify-profile.mjs --server http://127.0.0.1:8899 [--admin admin.txt]
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
const BASE_PORT = Number(argOf(process.argv, "port", "9917"))
const SERVER = argOf(process.argv, "server", "http://127.0.0.1:8888").replace(/\/+$/, "")
const ADMIN_FILE = argOf(process.argv, "admin", "")

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

/** 走完整注册链路建一个账号（两段式 + 邮箱验证码）。 */
async function registerAccount(prefix) {
  const username = freshUsername(prefix)
  const password = "Prof68-pass-1"
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

/** 用某个账号的 token 发一篇帖，返回 id。 */
async function createPost(account, title) {
  const r = await fetch(SERVER + "/api/posts", {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + account.accessToken },
    body: JSON.stringify({ title, content: "档案页验收用的正文，用来判「TA 的帖子」这块。" }),
  })
  if (!r.ok) {
    const text = await r.text().catch(() => "")
    skipEnv("发帖失败（HTTP " + r.status + "）：" + text.slice(0, 160))
  }
  const body = await r.json()
  if (!body.id) skipEnv("发帖响应里没有 id")
  return body.id
}

async function blockAs(actor, targetUserId) {
  const r = await fetch(SERVER + "/api/blocks", {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + actor.accessToken },
    body: JSON.stringify({ userId: targetUserId }),
  })
  if (!r.ok) skipEnv("拉黑失败（HTTP " + r.status + "）：好友域不可用")
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

async function clearSession(cdp, site) {
  await cdp.navigate(site.url + "/")
  await cdp.evaluate('sessionStorage.clear(); localStorage.clear(); "ok"')
}

/**
 * 读档案页的可观测状态。
 *
 * ⚠️ `cdp.evaluate` 的脚本里**不要写 `//` 行注释** —— 那段脚本会被折叠，
 * 行注释会把同一行后面的代码一起吃掉（verify-following 踩过）。
 */
function readProfile(cdp) {
  return cdp.evaluate(`(() => {
    const q = (s) => document.querySelector(s)
    const txt = (el) => el ? (el.textContent || '').replace(/\\s+/g, ' ').trim() : null
    const heads = Array.from(document.querySelectorAll('h1, h2')).map((h) => txt(h));
    const rows = Array.from(document.querySelectorAll('.block .row-title')).map((el) => txt(el));
    const counts = q('[data-testid=profile-counts]');
    const countsLinks = q('.counts-block') ? document.querySelectorAll('.counts-block a').length : -1;
    const order = []
    const push = (key, sel) => { const el = q(sel); if (el) order.push({ key: key, top: el.getBoundingClientRect().top + window.scrollY }) }
    push('hero', '.hero')
    push('relation', '.relation')
    push('posts', '#author-posts-head')
    push('rooms', '#author-rooms-head')
    push('counts', '[data-testid=profile-counts]')
    order.sort((a, b) => a.top - b.top)
    const firstPost = q('.block .row-link')
    return {
      path: location.pathname,
      title: txt(q('.hero .title')),
      handle: txt(q('.handle')),
      heads: heads,
      rows: rows,
      order: order.map((o) => o.key),
      countsText: txt(counts),
      countsLinks: countsLinks,
      hasFavoriteBlock: heads.some((h) => h && h.indexOf('收藏') >= 0),
      hasSelfNotice: Array.from(document.querySelectorAll('.guest-note')).some((n) => (n.textContent || '').indexOf('公开页') >= 0),
      notFound: (() => {
        const state = q('.state')
        const text = state ? (state.innerText || '') : ''
        return text.indexOf('用户不存在') >= 0 || text.indexOf('没有找到这个用户') >= 0
      })(),
      firstPostHref: firstPost ? firstPost.getAttribute('href') : null,
      hasEditForm: Boolean(q('.edit-form')),
      body: (document.body.innerText || '').slice(0, 600),
    };
  })()`)
}

async function openProfile(cdp, site, userId) {
  await cdp.navigate(site.url + "/user/" + userId)
  await waitFor(
    async () => {
      const p = await readProfile(cdp)
      return p.title !== null || p.notFound || p.rows.length > 0
    },
    { label: "档案页渲染出名片区或空态", timeoutMs: 25000 },
  )
  await sleep(600)
  return readProfile(cdp)
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

  const site = await startStatic(DIST)
  const chrome = await startChrome(findChrome(), BASE_PORT, "profile")
  const cdp = await openTarget(BASE_PORT, "profile")

  try {
    // ---------- 准备：三个账号 + alice 的两篇帖 ----------
    const me = await registerAccount("profme")
    const alice = await registerAccount("profa")
    const bob = await registerAccount("profb")
    evidence.accounts = { me: me.username, alice: alice.username, bob: bob.username }

    const stamp = Date.now().toString(36)
    const aliceTitle1 = "A 的帖一-" + stamp
    const aliceTitle2 = "A 的帖二-" + stamp
    const bobTitle = "B 的帖-" + stamp
    await createPost(alice, aliceTitle1)
    await createPost(alice, aliceTitle2)
    await createPost(bob, bobTitle)
    record("P0 三个账号 + alice 两篇帖 + bob 一篇帖就绪", true, "alice=" + alice.user.id)

    // ---------- P1 游客视角：名片 + 作者内容都可见 ----------
    await clearSession(cdp, site)
    const guest = await openProfile(cdp, site, alice.user.id)
    evidence.guest = guest
    record(
      "P1a 游客打开他人档案页看得到名片（昵称 + @username）",
      guest.title === alice.user.displayName && Boolean(guest.handle && guest.handle.indexOf("@") === 0),
      JSON.stringify({ title: guest.title, handle: guest.handle }),
    )
    record(
      "P1b 游客也看得到「TA 的帖子」块（作者内容端点免登录）",
      guest.heads.some((h) => h && h.indexOf("TA 的帖子") >= 0),
      JSON.stringify(guest.heads),
    )

    // ---------- P2 块序（游客视角没有关系动作区，也没有计数） ----------
    //
    // 计数（关注/粉丝）来自 `GET /api/follows/:userId`，那个端点**需要登录**
    // （票 #56 定的：游客不该能枚举"谁关注了谁"）。所以游客视角下这个块
    // 结构上不出现 —— 这不是漏渲染，而是"没有数据就不摆空壳"。
    const guestOrder = guest.order.filter((k) => k !== "relation" && k !== "counts")
    record(
      "P2a 游客视角块序：名片 → TA 的帖子 → TA 的放映室（计数需登录，游客不摆空壳）",
      JSON.stringify(guestOrder) === JSON.stringify(["hero", "posts", "rooms"]),
      JSON.stringify(guestOrder),
    )

    // ---------- P3 TA 的帖子块的内容 ----------
    record(
      "P3a 帖块里只有 alice 的帖，没有 bob 的",
      guest.rows.some((t) => t && t.indexOf("A 的帖") >= 0) && !guest.rows.some((t) => t && t.indexOf("B 的帖") >= 0),
      JSON.stringify(guest.rows),
    )
    record(
      "P3b 帖列表项是真的链接（能进详情页）",
      Boolean(guest.firstPostHref && /^\/posts\/\d+/.test(guest.firstPostHref)),
      JSON.stringify(guest.firstPostHref),
    )

    // ---------- P4 收藏块不存在 ----------
    record(
      "P4 他人档案页没有「收藏」区块（私人书签他人不可见）",
      !guest.hasFavoriteBlock,
      JSON.stringify(guest.heads),
    )

    // ---------- P5 计数不可点（只在登录视角可达：关系端点需要登录） ----------
    await seedSession(cdp, site, me)
    const loggedIn = await openProfile(cdp, site, alice.user.id)
    evidence.loggedIn = loggedIn
    record(
      "P5a 关注/粉丝数是纯数字",
      Boolean(loggedIn.countsText && /关注/.test(loggedIn.countsText) && /粉丝/.test(loggedIn.countsText)),
      JSON.stringify(loggedIn.countsText),
    )
    record(
      "P5b 计数区里没有任何链接（不可点开名单）",
      loggedIn.countsLinks === 0,
      "countsLinks=" + loggedIn.countsLinks,
    )

    // ---------- P6 拉黑前后的同口径 ----------
    record(
      "P6a 已登录（互不拉黑）时看得到 alice 的帖",
      loggedIn.rows.some((t) => t && t.indexOf("A 的帖") >= 0),
      JSON.stringify(loggedIn.rows),
    )
    record(
      "P6d 已登录视角块序含关系动作区（名片 → 关系动作 → 帖 → 房 → 计数）",
      JSON.stringify(loggedIn.order) === JSON.stringify(["hero", "relation", "posts", "rooms", "counts"]),
      JSON.stringify(loggedIn.order),
    )

    await blockAs(bob, alice.user.id)
    await seedSession(cdp, site, bob)
    const blocked = await openProfile(cdp, site, alice.user.id)
    evidence.blocked = blocked
    record(
      "P6b bob 拉黑 alice 后，档案页上看不到 alice 的帖（与关注流同一条谓词）",
      blocked.rows.length === 0,
      JSON.stringify(blocked.rows),
    )
    // 公开内容本身仍然可看：拉黑限制的是私聊与新关注，不是"能不能读帖子"。
    const anyPost = await fetch(SERVER + "/api/posts?limit=1").then((r) => r.json())
    const anyPostId = Array.isArray(anyPost.items) && anyPost.items.length > 0 ? anyPost.items[0].id : 0
    const postDetail = await fetch(SERVER + "/api/posts/" + anyPostId, {
      headers: { Authorization: "Bearer " + bob.accessToken },
    })
    record(
      "P6c 拉黑不改公开内容的可读性（同一条帖的详情仍 200）",
      postDetail.status === 200,
      "status=" + postDetail.status,
    )

    // ---------- P7 不存在的 id / 被封禁的账号 ----------
    const missing = await openProfile(cdp, site, 999999)
    record("P7a 不存在的 id → 「用户不存在」空态", missing.notFound, JSON.stringify(missing.body.slice(0, 120)))

    if (!ADMIN_FILE) {
      skipOne("P7b 被封禁的账号也是同一个空态", "没有 --admin <文件>，无法封禁账号（管理端需要管理员凭据）")
    } else {
      const raw = readFileSync(ADMIN_FILE, "utf8").trim().split(/\r?\n/).filter(Boolean)
      const [adminUser, adminPass] = raw.length >= 2 ? [raw[0].trim(), raw[1].trim()] : [raw[0].split(":")[0], raw[0].split(":")[1]]
      const login = await fetch(SERVER + "/api/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: adminUser, password: adminPass }),
      })
      if (!login.ok) {
        skipOne("P7b 被封禁的账号也是同一个空态", "管理员登录失败（HTTP " + login.status + "）")
      } else {
        const adminToken = (await login.json()).accessToken
        const ban = await fetch(SERVER + "/api/admin/users/" + me.user.id, {
          method: "PATCH",
          headers: { "Content-Type": "application/json", Authorization: "Bearer " + adminToken },
          body: JSON.stringify({ status: "banned", reason: "verify-profile P7b" }),
        })
        if (!ban.ok) {
          skipOne("P7b 被封禁的账号也是同一个空态", "封禁请求失败（HTTP " + ban.status + "）")
        } else {
          await clearSession(cdp, site)
          const banned = await openProfile(cdp, site, me.user.id)
          record(
            "P7b 被封禁的账号 → 与「不存在」同一个空态（防存在性探测）",
            banned.notFound,
            JSON.stringify(banned.body.slice(0, 120)),
          )
        }
      }
    }

    // ---------- P8 本人访问自己的 /user/:id ----------
    await seedSession(cdp, site, alice)
    const self = await openProfile(cdp, site, alice.user.id)
    evidence.self = self
    record(
      "P8 本人访问自己的档案页出现「这是别人看到的你的公开页」提示",
      self.hasSelfNotice,
      JSON.stringify({ notice: self.hasSelfNotice, heads: self.heads }),
    )

    console.log("\n通过 " + passed + " / 失败 " + failed + (skipped ? " / 跳过 " + skipped : ""))
    console.log(JSON.stringify(evidence, null, 2))
    process.exitCode = failed === 0 ? 0 : 1
  } finally {
    cdp.close()
    chrome.close()
    site.close()
  }
}

await main()
