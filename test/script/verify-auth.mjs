#!/usr/bin/env node
/**
 * 账号体系一期验收脚本（ACCOUNTS §5/§6/§10 + 计划 T11）。
 *
 * 验的是"只在运行期才存在"的事：真实 PostgreSQL（经 SSH 隧道）+ 真实 Go 二进制 +
 * 真实 Chrome。九条判据按**依赖顺序**排列：
 *
 *   ① 注册 201 且响应体不含口令字段；`GET /api/auth/me` 读得到刚注册的账号
 *   ② 登录下发 `Set-Cookie: pr_refresh=…`，属性 HttpOnly / SameSite=Lax / Path=/api/auth，
 *      且**没有** Secure（无 TLS 部署的决定，见 §13.1）
 *   ③ 刷新走 Cookie → 200 换回新 access token，且 refresh **值发生轮换**
 *   ④ 重放**轮换前**的旧 refresh → 401 REFRESH_REPLAY，且该账号**全部**会话被撤销
 *      （拿最新 Cookie 再刷也失败）
 *   ⑤ 封禁两件事：(a) 旧 access token 立刻失效（/api/auth/me → 401/403）；
 *      (b) 封禁**前**用 ws-ticket 建好的 WS 连接被以 **1008** 关闭且 reason 可读；
 *      (c) 附带：封禁**前**签发、封禁**后**才用的票据，握手后立刻被 1008 拒掉；
 *      收尾必须解封（否则脚本不可重复运行）
 *   ⑥ 游客（无任何 token）仍能按房间码进房：真实浏览器打开 /room/<code>，
 *      不被跳去 /login 且进入 joined 态（需要房里已有一个已登录的主播，
 *      空房会被服务端以「主播尚未进房」拒绝 join）
 *   ⑦ 匿名 `POST /api/rooms` → 401，且**没有**因此创建出房间
 *   ⑧ 归属一致：建房响应的 roomId ↔ `GET /api/rooms/:id` ↔ 管理端用户列表里的 owner
 *   ⑨ 附加：`GET /api/admin/logs?limit=5` 分页且**新→旧**；非 admin → 403
 *
 * 时序上有两条**必须**的约束（踩过的坑）：
 *   - 房间有 TTL（本实例 `PR_ICE_TTL=30s`，房间创建响应里带 `expiresAt`）。判据 ⑥/⑧
 *     用的房间必须在**紧接着**它的那一步创建，否则浏览器打开时房间已被回收，
 *     表现是"游客 join 超时"这类看起来像产品缺陷的假失败。
 *   - 判据 ⑤ 的连接必须在**封禁之前**建立：服务端对握手之后才判定的封禁用
 *     1008 + 可读 reason 关闭（那是浏览器唯一能读到关闭原因的通道）。
 *
 * 用法（仓库根目录；服务端用**自己构建的**二进制 + 自己的端口，别碰别人跑的 8080）：
 *
 *   go build -o %TEMP%\pr-t11\pr-t11.exe ./cmd
 *   # 起实例（环境变量清单见 docs/ACCOUNTS.md §12 的验收口径）：
 *   #   PR_ADDR=127.0.0.1:18099
 *   #   PR_DB_DSN=$(cat %TEMP%\pr-deploy\test-dsn-5433.txt)   ← 必须是以 _test 结尾的库
 *   #   PR_JWT_SECRET=<64 位十六进制>
 *   #   PR_ALLOWED_ORIGINS=127.0.0.1:18099
 *   #   PR_STATIC_DIR=<前端产物目录>（既有脚本依赖 window.__pr 调试钩子，见 F-5）
 *   node test/script/verify-auth.mjs --server http://127.0.0.1:18099 --client http://127.0.0.1:18099 \
 *        --dsn %TEMP%\pr-deploy\test-dsn-5433.txt
 *
 * 参数：
 *   --server  测试服务端（默认 http://127.0.0.1:8080）
 *   --client  客户端入口（默认 = --server；同端口部署时两者相同）
 *   --dsn     测试库连接串文件；给定时脚本校验 dbname 以 _test 结尾，否则**拒绝运行**
 *   --admin   管理员凭据文件（默认 %TEMP%\pr-deploy\admin-cred.txt，最多等 60 秒）
 *   --port    真实浏览器的 CDP 调试端口（默认 9600）
 *
 * 环境变量（可选）：PR_T11_DSN_FILE / PR_T11_ADMIN_FILE 覆盖上面两个默认路径。
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 */
import { existsSync, readFileSync } from 'node:fs'
import {
  argOf,
  ensureAccount,
  findChrome,
  loginUser,
  newCookieJar,
  openTarget,
  registerUser,
  seedAndEnter,
  sleep,
  startChrome,
  waitFor,
} from './lib/browser.mjs'

const argv = process.argv.slice(2)
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:8080').replace(/\/+$/, '')
const CLIENT_URL = argOf(argv, 'client', SERVER_URL).replace(/\/+$/, '')
const DSN_FILE = argOf(argv, 'dsn', process.env.PR_T11_DSN_FILE ?? '')
const ADMIN_FILE = argOf(
  argv,
  'admin',
  process.env.PR_T11_ADMIN_FILE ??
    (process.env.TEMP ? `${process.env.TEMP}\\pr-deploy\\admin-cred.txt` : ''),
)
const BASE_PORT = Number(argOf(argv, 'port', '9600'))

// 测试账号口令：必须满足服务端策略（≥8 字符且同时含字母与数字）。
const PASSWORD = 'pr-test-pass1'

// ---------- 判据收集（与 verify-room-resume.mjs 同一形态）----------
const checks = []
const report = { server: SERVER_URL, client: CLIENT_URL }
function record(name, pass, detail) {
  checks.push({ name, pass: Boolean(pass), detail })
  console.log(`[${pass ? 'PASS' : 'FAIL'}] ${name} — ${detail}`)
}
/** 一条判据里的原始证据（响应头、响应体、关闭帧），报告里原样保留供人工复核。 */
function evidence(key, value) {
  report.evidence = report.evidence ?? {}
  report.evidence[key] = value
  return value
}

// ---------- 环境前置校验（脚本自己拒绝跑在错误的库上）----------

/**
 * 从 PostgreSQL 连接串里取出 dbname。支持 key=value 与 URL 两种形态。
 * 空 DSN 返回空串（调用方按"前置不通过"处理）。
 */
function dbNameOf(dsn) {
  const text = String(dsn ?? '').trim()
  if (!text) return ''
  if (/^postgres(ql)?:\/\//i.test(text)) {
    try {
      return decodeURIComponent(new URL(text).pathname.replace(/^\//, ''))
    } catch {
      return ''
    }
  }
  const m = /(^|\s)dbname\s*=\s*('([^']*)'|"([^"]*)"|(\S+))/i.exec(text)
  return m ? m[3] ?? m[4] ?? m[5] ?? '' : ''
}

/**
 * 运行前置：必须有测试库 DSN，且库名以 `_test` 结尾。
 *
 * 为什么要在脚本里硬挡：这个测试库是**真库**（经 SSH 隧道）。一个名字写错的 DSN
 * 会让整套用例（注册、封禁、改角色、建房）落到开发/生产库上 —— 那是不可逆的污染。
 * 因此"看起来对"不够，必须有可判定的证据（库名后缀），否则拒绝运行。
 */
function preflight() {
  const problems = []
  let dsn = ''
  if (DSN_FILE && existsSync(DSN_FILE)) {
    dsn = readFileSync(DSN_FILE, 'utf8').trim()
  } else {
    problems.push(
      DSN_FILE
        ? `--dsn/PR_T11_DSN_FILE 指向的文件不存在：${DSN_FILE}`
        : '未提供 --dsn（默认 %TEMP%\\pr-deploy\\test-dsn-5433.txt 也不存在）',
    )
  }
  const dbname = dbNameOf(dsn)
  report.dbname = dbname
  if (!dsn) problems.push('测试库连接串为空')
  else if (!/_test$/i.test(dbname)) {
    problems.push(`测试库名必须以 _test 结尾，实际 dbname=${JSON.stringify(dbname)}（拒绝在非测试库上跑）`)
  }
  if (problems.length) {
    console.error('前置校验未通过，拒绝运行：')
    for (const p of problems) console.error(`  ✗ ${p}`)
    process.exitCode = 1
    return null
  }
  console.log(`前置校验通过：测试库 dbname=${dbname}（连接串来自 ${DSN_FILE}）`)
  return dsn
}

/**
 * 读管理员凭据（`username=…` / `password=…` 两行）。
 * 凭据只进内存与请求头，**绝不写进报告或仓库**。
 */
async function readAdminCred(deadlineMs = 60000) {
  const deadline = Date.now() + deadlineMs
  while (Date.now() < deadline) {
    if (ADMIN_FILE && existsSync(ADMIN_FILE)) {
      const text = readFileSync(ADMIN_FILE, 'utf8')
      const username = /^\s*username\s*=\s*(.+)$/m.exec(text)?.[1]?.trim()
      const password = /^\s*password\s*=\s*(.+)$/m.exec(text)?.[1]?.trim()
      if (username && password) return { username, password }
      throw new Error(`管理员凭据文件格式不正确（需要 username=… 与 password=… 两行）：${ADMIN_FILE}`)
    }
    await sleep(1000)
  }
  throw new Error(`等不到管理员凭据文件（60 秒超时）：${ADMIN_FILE}`)
}

// ---------- 原始 HTTP 小工具（判据要的是**原始**响应，不能只看解析后的 JSON）----------

/** 发一次请求并保留 status / 原始响应头 / 文本体。 */
async function raw(url, { method = 'GET', headers = {}, body, cookie } = {}) {
  const h = { ...headers }
  if (cookie) h.Cookie = cookie
  let payload
  if (body !== undefined) {
    h['Content-Type'] = h['Content-Type'] ?? 'application/json'
    payload = typeof body === 'string' ? body : JSON.stringify(body)
  }
  const resp = await fetch(url, { method, headers: h, body: payload, redirect: 'manual' })
  const text = await resp.text()
  let json = null
  try {
    json = JSON.parse(text)
  } catch {
    // 非 JSON（204 空体等）：保持 null
  }
  const setCookie = typeof resp.headers.getSetCookie === 'function' ? resp.headers.getSetCookie() : []
  return { status: resp.status, headers: resp.headers, setCookie, text, json }
}

/** 从一组 Set-Cookie 里挑出指定名字的**原始**那一行。 */
function setCookieLine(setCookieList, name) {
  return setCookieList.find((line) => line.split(';')[0].split('=')[0].trim() === name) ?? ''
}

/** 从原始 Set-Cookie 行里取出 cookie 值。 */
function cookieValueOf(line) {
  const pair = String(line).split(';')[0]
  const idx = pair.indexOf('=')
  return idx > 0 ? pair.slice(idx + 1).trim() : ''
}

/** 只取凭据的前后几位用于日志对账，避免把完整令牌写进报告。 */
function fingerprint(secret) {
  const s = String(secret ?? '')
  if (s.length <= 12) return s ? `<${s.length}字符>` : '(空)'
  return `${s.slice(0, 6)}…${s.slice(-4)}(len=${s.length})`
}

/**
 * 429 重试：注册/建房的每 IP 令牌桶是本用例会主动去撞的东西。
 *
 * 实测形态：连跑第二遍脚本时，注册桶（默认 5/分钟、容量 3）已经被前一跑用掉，
 * 第 4 次注册直接 429 `RATE_LIMITED`。此时**不能**改实例的限速配置来"让它过"——
 * 限速本身是 §5 的交付物，改配置等于把判据架在被测对象之外。正确做法是承认限速
 * 存在并等它回血（`PR_AUTH_REGISTER_PER_MINUTE=5` ⇒ 每次重试等 15 秒足够）。
 */
async function withRateLimitRetry(label, fn, { attempts = 4, waitMs = 16000 } = {}) {
  let last
  for (let i = 1; i <= attempts; i += 1) {
    last = await fn()
    if (last.status !== 429) return last
    if (i < attempts) {
      console.log(`  · ${label} 撞到限速（HTTP 429），等 ${waitMs / 1000}s 后重试（第 ${i}/${attempts - 1} 次重试）`)
      await sleep(waitMs)
    }
  }
  return last
}

/**
 * 调 lib/browser.mjs 的封装，但把 429 转成"重试"而不是"直接失败"。
 *
 * 只对**会抛错**的封装有效（loginUser / registerUser 这类）；ensureAccount 是个例外 ——
 * 它把 429 吞成 null 并缓存，见主流程 ⑩ 的说明。
 */
async function withLibRateLimitRetry(label, fn, { attempts = 4, waitMs = 16000 } = {}) {
  let lastErr = null
  for (let i = 1; i <= attempts; i += 1) {
    try {
      return await fn()
    } catch (err) {
      lastErr = err
      if (!/HTTP 429/.test(String(err?.message ?? err))) throw err
      if (i < attempts) {
        console.log(`  · ${label} 撞到限速（HTTP 429），等 ${waitMs / 1000}s 后重试（第 ${i}/${attempts - 1} 次重试）`)
        await sleep(waitMs)
      }
    }
  }
  throw lastErr
}

// ---------- 判据 ① 注册 ----------

/**
 * 注册一个新账号（**不**复用 lib/browser.mjs 的 registerUser：判据 ① 要的是原始响应体
 * 文本 + 状态码 + Set-Cookie 三样东西，那个封装只返回 {token,user,username,status}）。
 * 注册桶默认 5/分钟 ⇒ 连跑第二遍脚本时可能先撞 429，必须等它回血再判。
 */
async function step1Register(server) {
  const username = `t11_${Math.random().toString(36).slice(2, 10)}`
  const resp = await withRateLimitRetry('①注册', () =>
    raw(`${server}/api/auth/register`, {
      method: 'POST',
      body: { username, password: PASSWORD, displayName: 'T11 受害者' },
    }),
  )
  evidence('register', { status: resp.status, body: resp.text, setCookie: resp.setCookie })
  const leaked = /passwordHash|password_hash|"password"\s*:/i.test(resp.text)
  record(
    '①-1 注册返回 201',
    resp.status === 201,
    `POST /api/auth/register → HTTP ${resp.status}（期望 201）`,
  )
  record(
    '①-2 注册响应体不含 passwordHash / password',
    !leaked,
    leaked
      ? `响应体里出现口令字段：${resp.text.slice(0, 400)}`
      : `响应体字段=${JSON.stringify(Object.keys(resp.json ?? {}))}，明文口令字段 0 命中`,
  )
  const user = resp.json?.user ?? null
  if (!user) {
    record('①-3 /api/auth/me 能读到刚注册的账号', false, '注册响应里没有 user 对象，无法继续')
    return null
  }

  const me = await raw(`${server}/api/auth/me`, {
    headers: { Authorization: `Bearer ${resp.json.accessToken}` },
  })
  evidence('me', { status: me.status, body: me.text })
  const meUser = me.json?.user ?? {}
  record(
    '①-3 GET /api/auth/me 能读到刚注册的账号',
    me.status === 200 && meUser.username === username && meUser.id === user.id,
    `HTTP ${me.status} user.id=${meUser.id}（注册时 id=${user.id}）username=${JSON.stringify(meUser.username)} ` +
      `role=${meUser.role} status=${meUser.status} 字段=${JSON.stringify(Object.keys(user))}`,
  )
  return { username, user, accessToken: resp.json.accessToken }
}

// ---------- 判据 ② 登录 Cookie ----------

/**
 * 登录并逐条断言 Set-Cookie 的属性。
 *
 * 关键口径：**用原始头**判断（`getSetCookie()`），不看 cookie jar —— jar 会把属性吃掉，
 * 而"有没有 Secure / SameSite 是什么"正是本节要钉住的协议面。
 */
async function step2LoginCookie(server, username) {
  const resp = await raw(`${server}/api/auth/login`, {
    method: 'POST',
    body: { username, password: PASSWORD },
  })
  const line = setCookieLine(resp.setCookie, 'pr_refresh')
  evidence('login', {
    status: resp.status,
    setCookie: resp.setCookie,
    bodyKeys: Object.keys(resp.json ?? {}),
  })

  const attrs = {
    raw: line,
    httpOnly: /;\s*httponly\s*(;|$)/i.test(line),
    sameSiteLax: /;\s*samesite\s*=\s*lax/i.test(line),
    pathAuth: /;\s*path\s*=\s*\/api\/auth\s*(;|$)/i.test(line),
    secure: /;\s*secure\s*(;|$)/i.test(line),
    maxAgePresent: /;\s*max-age\s*=\s*\d+/i.test(line),
    value: cookieValueOf(line),
  }
  report.cookie = { ...attrs, value: fingerprint(attrs.value) }

  record(
    '②-1 登录返回 200 且下发 Set-Cookie: pr_refresh',
    resp.status === 200 && Boolean(line),
    `HTTP ${resp.status}；pr_refresh 那一行=${JSON.stringify(line || '(缺失)')}`,
  )
  record(
    '②-2 Cookie 属性含 HttpOnly / SameSite=Lax / Path=/api/auth',
    attrs.httpOnly && attrs.sameSiteLax && attrs.pathAuth,
    `HttpOnly=${attrs.httpOnly} SameSite=Lax=${attrs.sameSiteLax} Path=/api/auth=${attrs.pathAuth} Max-Age 存在=${attrs.maxAgePresent}`,
  )
  record(
    '②-3 Cookie **不含** Secure（无 TLS 部署的决定）',
    attrs.secure === false,
    attrs.secure
      ? `原始行里出现了 Secure：${line}`
      : `原始行里 Secure 0 命中；原始行=${JSON.stringify(line)}`,
  )
  return { accessToken: resp.json?.accessToken ?? '', refreshValue: attrs.value }
}

// ---------- 判据 ③④ 刷新轮换 + 重放 ----------

/**
 * ③ 拿 Cookie 刷新 → 200 + 新 access token + Cookie **值轮换**。
 * ④ 用轮换**前**的值再刷一次 → 401 REFRESH_REPLAY，且该账号**全部**会话被撤销
 *    （最新的那个 Cookie 也必须失效）。
 *
 * 为什么要手工发 Cookie 而不是靠 jar：jar 只保留"当前最新"，而本判据正需要
 * "把已经被轮换掉的旧值再发一次"这个动作。
 */
async function step34Rotation(server, session) {
  const oldValue = session.refreshValue
  const refreshed = await raw(`${server}/api/auth/refresh`, {
    method: 'POST',
    cookie: `pr_refresh=${oldValue}`,
  })
  const newLine = setCookieLine(refreshed.setCookie, 'pr_refresh')
  const newValue = cookieValueOf(newLine)
  evidence('refresh', {
    status: refreshed.status,
    setCookie: refreshed.setCookie,
    bodyKeys: Object.keys(refreshed.json ?? {}),
  })

  record(
    '③-1 刷新 200 且换回新的 access token',
    refreshed.status === 200 && Boolean(refreshed.json?.accessToken),
    `POST /api/auth/refresh → HTTP ${refreshed.status}；accessToken=${fingerprint(refreshed.json?.accessToken)} ` +
      `（刷新前的旧 token=${fingerprint(session.accessToken)}）`,
  )
  record(
    '③-2 刷新后 refresh Cookie 值发生轮换',
    Boolean(newValue) && newValue !== oldValue,
    `旧值=${fingerprint(oldValue)} 新值=${fingerprint(newValue)} 变化=${newValue !== oldValue && Boolean(newValue)}`,
  )

  // ④ 重放：把轮换前的值手工再发一次。
  const replay = await raw(`${server}/api/auth/refresh`, {
    method: 'POST',
    cookie: `pr_refresh=${oldValue}`,
  })
  evidence('refreshReplay', { status: replay.status, body: replay.text, sentCookie: fingerprint(oldValue) })
  const code = replay.json?.code ?? replay.json?.error?.code ?? ''
  record(
    '④-1 重放旧 refresh → 401 REFRESH_REPLAY',
    replay.status === 401 && code === 'REFRESH_REPLAY',
    `HTTP ${replay.status} code=${JSON.stringify(code)} body=${replay.text}（重放的是轮换前的旧值 ${fingerprint(oldValue)}）`,
  )

  // 全部会话被撤销：拿**最新**的 Cookie 再刷也必须失败。
  const afterReplay = await raw(`${server}/api/auth/refresh`, {
    method: 'POST',
    cookie: `pr_refresh=${newValue}`,
  })
  evidence('refreshAfterReplay', {
    status: afterReplay.status,
    body: afterReplay.text,
    sentCookie: fingerprint(newValue),
  })
  const afterCode = afterReplay.json?.code ?? afterReplay.json?.error?.code ?? ''
  record(
    '④-2 重放导致该账号全部会话被撤销（最新 Cookie 也失效）',
    afterReplay.status === 401,
    `用轮换后的最新值再刷 → HTTP ${afterReplay.status} code=${JSON.stringify(afterCode)} body=${afterReplay.text}`,
  )
  return { oldValue, newValue }
}

// ---------- 判据 ⑧ 归属 ----------

/** 用房主身份建房（原始响应留下 expiresAt，供后续步骤判断房间还在不在）。 */
async function ownerCreateRoom(server, ownerToken) {
  const resp = await withRateLimitRetry('⑧建房', () =>
    raw(`${server}/api/rooms`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${ownerToken}` },
      body: {},
    }),
  )
  evidence('createRoom', { status: resp.status, body: resp.text })
  return resp
}

/** ⑧ 归属一致：建房响应 ↔ GET /api/rooms/:id ↔ 管理端用户列表。 */
async function step8Ownership(server, admin, owner, created) {
  if (created.status !== 200) {
    record('⑧-1 登录用户建房 200 且响应形状完整', false, `HTTP ${created.status} body=${created.text}`)
    record('⑧-2 建房响应的 roomId 与 GET /api/rooms/:id 一致', false, '建房未成功')
    record('⑧-3 管理端用户列表里能查到该账号', false, '建房未成功')
    return
  }
  const roomId = created.json.roomId
  record(
    '⑧-1 登录用户建房 200 且响应形状完整',
    Boolean(roomId) &&
      Boolean(created.json.hostToken) &&
      created.json.iceServers !== undefined &&
      created.json.expiresAt !== undefined &&
      created.json.probe !== undefined,
    `HTTP 200 roomId=${roomId} 字段=${JSON.stringify(Object.keys(created.json))} ` +
      `hostToken=${fingerprint(created.json.hostToken)} ttlSeconds=${created.json.ttlSeconds} expiresAt=${created.json.expiresAt}`,
  )

  const info = await raw(`${server}/api/rooms/${roomId}`)
  evidence('roomInfoByRoomId', { status: info.status, body: info.text })
  record(
    '⑧-2 建房响应的 roomId 与 GET /api/rooms/:id 一致',
    info.status === 200 && info.json?.roomId === roomId && info.json?.exists === true,
    `GET /api/rooms/${roomId} → HTTP ${info.status} roomId=${info.json?.roomId}（期望 ${roomId}）exists=${info.json?.exists}`,
  )

  const users = await raw(
    `${server}/api/admin/users?search=${encodeURIComponent(owner.username)}&limit=50`,
    { headers: { Authorization: `Bearer ${admin.accessToken}` } },
  )
  const item = (users.json?.items ?? []).find((u) => u.username === owner.username)
  evidence('adminUsersSearch', {
    status: users.status,
    total: users.json?.total,
    matched: item ?? null,
    limit: users.json?.limit,
    offset: users.json?.offset,
  })
  record(
    '⑧-3 管理端用户列表里存在该账号（归属可对上）',
    users.status === 200 && Boolean(item) && item.id === owner.user.id,
    `GET /api/admin/users?search=${owner.username} → HTTP ${users.status} total=${users.json?.total} ` +
      `命中 id=${item?.id}（期望 ${owner.user.id}）role=${item?.role} status=${item?.status}`,
  )
}

// ---------- 判据 ⑦ 匿名建房 ----------

/** ⑦ 匿名建房 → 401，且**没有**因此产生房间（用同一个房间码回查必须是 404）。 */
async function step7AnonymousCreate(server) {
  // 自己指定房间码：这样才能证明"这一次匿名请求**没有**创建出**这个**房间"。
  const wanted = `T11${Math.random().toString(36).slice(2, 6).toUpperCase()}`
  const resp = await withRateLimitRetry('⑦匿名建房', () =>
    raw(`${server}/api/rooms`, { method: 'POST', body: { roomId: wanted } }),
  )
  evidence('anonCreate', { status: resp.status, body: resp.text, wantedRoomId: wanted })
  record(
    '⑦-1 匿名 POST /api/rooms → 401',
    resp.status === 401,
    `HTTP ${resp.status} body=${resp.text}（请求 body={"roomId":${JSON.stringify(wanted)}}）`,
  )

  const after = await raw(`${server}/api/rooms/${wanted}`)
  evidence('anonCreateRoomInfo', { status: after.status, body: after.text })
  record(
    '⑦-2 匿名请求没有创建出房间',
    !(after.status === 200 && after.json?.exists === true),
    `GET /api/rooms/${wanted} → HTTP ${after.status} body=${after.text}`,
  )
}

// ---------- 判据 ⑥ 游客进房 ----------

/**
 * ⑥ 游客（无任何 token）用房间码进房。
 *
 * **为什么必须有一个已登录的主播先待在房间里**：服务端对"房里还没有主播"的连接会拒绝
 * join（日志原文：`加入 XXXX 被拒绝: 主播尚未进房`），客户端表现是页面停在
 * 「信令已连上，正在加入房间…」——那不是游客被挡在登录墙外，而是空房没有可跟随的主播。
 * 因此这里开**两个**浏览器：主播（已登录、带 hostToken）先进房，然后一个**不带任何
 * token** 的游客窗口打开同一个 /room/<code>。
 *
 * 三条判据：游客**没被跳去 /login**、进入 joined 态、并且服务端日志里没有把它算成
 * 匿名被拒（判据 ⑥-3 用房间快照证明它真的进了成员表）。
 */
async function step6Guest(server, clientUrl, roomId, hostToken, cdpHost, cdpGuest) {
  // 主播先进房：空房会让游客的 join 被"主播尚未进房"拒绝（见上）。
  await seedAndEnter(cdpHost, clientUrl, roomId, 'host', 'T11 主播', hostToken)
  const hostAlive = await raw(`${server}/api/rooms/${roomId}`)
  evidence('roomWhenHostJoined', { status: hostAlive.status, body: hostAlive.text })
  record(
    '⑥-0 游客进房时房间里有主播（空房会以「主播尚未进房」拒绝 join）',
    hostAlive.status === 200 && hostAlive.json?.hasHost === true,
    `GET /api/rooms/${roomId} → HTTP ${hostAlive.status} body=${hostAlive.text}`,
  )

  // 游客：**不带任何 token**，只有 sessionStorage 里的进房凭据（与 verify-room-resume 的观众路径一致）。
  await cdpGuest.navigate(clientUrl)
  await cdpGuest.evaluate(
    `sessionStorage.setItem('pr:join:${String(roomId).toUpperCase()}', ${JSON.stringify(
      JSON.stringify({ password: '', role: 'viewer', displayName: 'T11 游客' }),
    )})`,
  )
  await cdpGuest.navigate(`${clientUrl}/room/${roomId}`)
  await waitFor(async () => cdpGuest.evaluate('typeof window.__pr !== "undefined"'), {
    label: '调试钩子就绪（生产构建没有 window.__pr，见 F-5）',
  })

  let snap = null
  let joinedErr = ''
  try {
    snap = await waitFor(
      async () => {
        const s = await cdpGuest.snapshot()
        return s.joined ? s : false
      },
      { label: '游客进入房间态（joined）', timeoutMs: 20000, intervalMs: 200 },
    )
  } catch (err) {
    joinedErr = err.message
    snap = await cdpGuest.snapshot()
  }

  const url = await cdpGuest.evaluate('location.href')
  const bodyText = await cdpGuest.evaluate('document.body.innerText')
  const passwordFields = await cdpGuest.evaluate(
    `document.querySelectorAll('input[type="password"]').length`,
  )
  const storedToken = await cdpGuest.evaluate(
    `JSON.stringify({ session: Object.keys(sessionStorage), auth: localStorage.getItem('pr:auth') })`,
  )
  evidence('guest', {
    href: url,
    pathname: new URL(url).pathname,
    joined: snap.joined,
    roomId: snap.roomId,
    role: snap.role,
    connection: snap.connection,
    clientId: snap.clientId,
    passwordFields,
    lastError: snap.errors?.last ?? '',
    roomClosed: snap.recovery?.roomClosed ?? '',
    storage: JSON.parse(storedToken),
    bodyText: bodyText.slice(0, 300),
  })
  record(
    '⑥-1 游客没被跳去 /login（页面仍停在该房间）',
    new URL(url).pathname === `/room/${roomId}` && passwordFields === 0,
    `location.pathname=${JSON.stringify(new URL(url).pathname)}（期望 /room/${roomId}）页面上密码输入框=${passwordFields} 个 ` +
      `存储键=${JSON.stringify(JSON.parse(storedToken).session)}`,
  )
  record(
    '⑥-2 游客进入房间态（joined=true）',
    snap.joined === true,
    snap.joined
      ? `snapshot.joined=true roomId=${snap.roomId}（期望 ${roomId}）role=${snap.role} connection=${snap.connection} clientId=${snap.clientId}`
      : `joined=${snap.joined} connection=${snap.connection} errors.last=${JSON.stringify(snap.errors?.last ?? '')} ` +
        `roomClosed=${JSON.stringify(snap.recovery?.roomClosed ?? '')} 页面片段=${JSON.stringify(bodyText.slice(0, 180))}` +
        `${joinedErr ? `（等待异常：${joinedErr}）` : ''}`,
  )

  const info = await raw(`${server}/api/rooms/${roomId}`)
  evidence('roomInfoWithGuest', { status: info.status, body: info.text })
  record(
    '⑥-3 服务端把游客算进了房间成员（成员数 ≥ 2：主播 + 游客）',
    info.status === 200 && Number(info.json?.memberCount ?? 0) >= 2,
    `GET /api/rooms/${roomId} → memberCount=${info.json?.memberCount} hasHost=${info.json?.hasHost} body=${info.text}`,
  )
  return snap
}

// ---------- 判据 ⑤ 封禁两件事 ----------

/** 用真实 Node WebSocket 连 /ws?ticket=…，记录升级结果与**关闭帧**（code + reason）。 */
async function openTicketWS(server, roomId, clientId, ticket, timeoutMs = 20000) {
  const wsBase = `${server.replace(/^http/, 'ws')}/ws?roomId=${encodeURIComponent(
    roomId,
  )}&clientId=${encodeURIComponent(clientId)}&ticket=${encodeURIComponent(ticket)}`
  const state = {
    displayUrl: `${server.replace(/^http/, 'ws')}/ws?roomId=${roomId}&clientId=${clientId}&ticket=<ticket>`,
    opened: false,
    code: null,
    reason: '',
  }
  const ws = new WebSocket(wsBase)
  const opened = await new Promise((resolveOpened) => {
    const timer = setTimeout(() => resolveOpened(false), timeoutMs)
    ws.addEventListener(
      'open',
      () => {
        clearTimeout(timer)
        state.opened = true
        resolveOpened(true)
      },
      { once: true },
    )
    ws.addEventListener(
      'error',
      () => {
        clearTimeout(timer)
        resolveOpened(false)
      },
      { once: true },
    )
  })
  const closed = new Promise((resolveClosed) => {
    const timer = setTimeout(() => resolveClosed(false), timeoutMs)
    ws.addEventListener(
      'close',
      (ev) => {
        clearTimeout(timer)
        state.code = ev.code
        state.reason = ev.reason
        resolveClosed(true)
      },
      { once: true },
    )
  })
  return { ws, state, opened, closed }
}

/**
 * ⑤ 封禁的两件事。
 *
 * 时序是判据的**核心**，必须按这个顺序：
 *   1. 受害者登录（拿 access token）；
 *   2. 受害者用 access token 换 ws-ticket，**先建好** WS 连接（此时账号还是 active）；
 *   3. 管理员 PATCH status=banned；
 *   4. 断言 (a) 旧 access token 打 /api/auth/me → 401/403；
 *             (b) 已经建好的那条 WS 连接被以 **1008** 关闭且 reason 可读；
 *   5. 解封（收尾，保证脚本可重复运行）。
 *
 * 为什么必须"先建连接再封禁"：服务端对**握手之后**才拿到封禁结论的连接用 1008 + 可读
 * reason 拒绝（见 internal/handler/ws.go 的 bannedReason 注释）——那是浏览器唯一能读到
 * 关闭原因的通道。反过来，封禁**之后**才去换 ticket 会先撞上 401（token_version 已失效），
 * 那是另一条判据，不在这里冒充 1008。
 */
async function step5Ban(server, admin, victim, roomId) {
  // 1. 受害者登录：换一套新的 access token（前四条判据的会话已被"重放"撤销）。
  const login = await raw(`${server}/api/auth/login`, {
    method: 'POST',
    body: { username: victim.username, password: PASSWORD },
  })
  if (login.status !== 200 || !login.json?.accessToken) {
    record('⑤-0 封禁前 POST /api/auth/ws-ticket → 200 且带 ticket/expiresIn', false, `受害者重新登录失败：HTTP ${login.status} ${login.text.slice(0, 200)}`)
    record('⑤-1 封禁前建立一条带票据的 WS 连接', false, '登录失败，无法继续')
    return
  }
  const victimToken = login.json.accessToken

  // 2. 换 ws-ticket（此时 active，应当成功）。换**两张**：第一张用来建立连接，第二张留着
  //    验证"封禁后拿旧票据握手会被服务端在升级之后以 1008 拒掉"这条路（见 ⑤-6）。
  const ticketResp = await withRateLimitRetry('⑤ws-ticket#1', () =>
    raw(`${server}/api/auth/ws-ticket`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${victimToken}` },
    }),
  )
  const ticket = ticketResp.json?.ticket ?? ''
  evidence('wsTicket', { status: ticketResp.status, body: ticketResp.text })
  record(
    '⑤-0 封禁前 POST /api/auth/ws-ticket → 200 且带 ticket/expiresIn',
    ticketResp.status === 200 && Boolean(ticket) && typeof ticketResp.json?.expiresIn === 'number',
    `HTTP ${ticketResp.status} ticket=${fingerprint(ticket)} expiresIn=${ticketResp.json?.expiresIn}`,
  )

  const spareResp = await withRateLimitRetry('⑤ws-ticket#2', () =>
    raw(`${server}/api/auth/ws-ticket`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${victimToken}` },
    }),
  )
  const spareTicket = spareResp.json?.ticket ?? ''
  evidence('wsTicketSpare', { status: spareResp.status, body: spareResp.text })

  const clientId = `c_t11_${Math.random().toString(36).slice(2, 10)}`
  const conn = await openTicketWS(server, roomId, clientId, ticket)
  evidence('wsBeforeBan', { ...conn.state, ticket: fingerprint(ticket) })
  record(
    '⑤-1 封禁前建立一条带票据的 WS 连接',
    conn.opened === true,
    `${conn.state.displayUrl} → 升级成功=${conn.opened}`,
  )
  if (!conn.opened) {
    record('⑤-4 封禁前建立的 WS 连接被以 1008 关闭且 reason 可读', false, '连接未建立，无法断言关闭帧')
    return
  }

  // 3. 管理员封禁。
  const ban = await raw(`${server}/api/admin/users/${victim.user.id}`, {
    method: 'PATCH',
    headers: { Authorization: `Bearer ${admin.accessToken}` },
    body: { status: 'banned', reason: 'T11 验收：封禁即时生效' },
  })
  evidence('banPatch', { status: ban.status, body: ban.text, targetId: victim.user.id })
  record(
    '⑤-2 管理员 PATCH status=banned → 200',
    ban.status === 200 && ban.json?.user?.status === 'banned',
    `HTTP ${ban.status} user.status=${ban.json?.user?.status} body=${ban.text}`,
  )

  // 4a. 旧 access token 立刻失效。
  const meAfterBan = await raw(`${server}/api/auth/me`, {
    headers: { Authorization: `Bearer ${victimToken}` },
  })
  evidence('meAfterBan', { status: meAfterBan.status, body: meAfterBan.text })
  record(
    '⑤-3 封禁后受害者旧 access token 立刻失效（/api/auth/me → 401/403）',
    meAfterBan.status === 401 || meAfterBan.status === 403,
    `HTTP ${meAfterBan.status} body=${meAfterBan.text}`,
  )

  // 4b. 已建立的 WS 被以 1008 关闭。
  let closedOk = false
  try {
    closedOk = await conn.closed
  } catch {
    closedOk = false
  }
  evidence('wsClose', { code: conn.state.code, reason: conn.state.reason })
  record(
    '⑤-4 封禁前建立的 WS 连接被以 1008 关闭且 reason 可读',
    closedOk === true && conn.state.code === 1008 && String(conn.state.reason).trim().length > 0,
    `close code=${conn.state.code}（期望 1008 = 策略违规）reason=${JSON.stringify(conn.state.reason)}`,
  )

  // 4c. 附带证据：封禁**之后**拿封禁**之前**签发的票据去握手 → 服务端在升级完成后
  //     立刻以 1008 + 「账号已被封禁，请勿重连」关闭（internal/handler/ws.go 的 bannedReason 路径）。
  //     它与 ⑤-4 是两条不同的代码路径（断开在线连接 / 拒绝新连接），两条都必须可读。
  if (spareTicket) {
    const late = await openTicketWS(server, roomId, `c_t11b_${Math.random().toString(36).slice(2, 8)}`, spareTicket)
    let lateClosed = false
    try {
      lateClosed = await late.closed
    } catch {
      lateClosed = false
    }
    evidence('wsTicketAfterBan', {
      opened: late.opened,
      code: late.state.code,
      reason: late.state.reason,
      ticket: fingerprint(spareTicket),
    })
    record(
      '⑤-6 封禁前签发、封禁后才用的票据：握手后立刻以 1008 + 封禁文案关闭',
      late.opened === true &&
        lateClosed === true &&
        late.state.code === 1008 &&
        String(late.state.reason).trim().length > 0,
      `升级成功=${late.opened} close code=${late.state.code}（期望 1008）reason=${JSON.stringify(late.state.reason)}`,
    )
    try {
      late.ws.close()
    } catch {
      // 忽略
    }
  } else {
    record(
      '⑤-6 封禁前签发、封禁后才用的票据：握手后立刻以 1008 + 封禁文案关闭',
      false,
      `第二张票据未取到（HTTP ${spareResp.status} ${spareResp.text.slice(0, 160)}）`,
    )
  }

  // 5. 收尾解封：不解封这个账号就永远是 banned，脚本第二次跑会全盘假失败。
  const unban = await raw(`${server}/api/admin/users/${victim.user.id}`, {
    method: 'PATCH',
    headers: { Authorization: `Bearer ${admin.accessToken}` },
    body: { status: 'active' },
  })
  evidence('unbanPatch', { status: unban.status, body: unban.text })
  record(
    '⑤-5 收尾解封（保证脚本可重复运行）',
    unban.status === 200 && unban.json?.user?.status === 'active',
    `HTTP ${unban.status} user.status=${unban.json?.user?.status}`,
  )
  try {
    conn.ws.close()
  } catch {
    // 忽略：连接可能已被服务端关闭
  }
}

// ---------- 判据 ⑨ 管理端日志 ----------

/**
 * ⑨ `GET /api/admin/logs?limit=5`：分页形状 + **新→旧**；非 admin → 403。
 *
 * 排序判定用每行开头的 Go 标准日志时间戳（`2006/01/02 15:04:05`）。行里没有可解析
 * 时间戳时退化为"offset=0 的首行必须等于 `limit=1&offset=0` 的首行"这条弱一些但
 * 可判定的口径，并在证据里注明（不假装通过）。
 */
async function step9AdminLogs(server, admin, nonAdminToken) {
  const resp = await raw(`${server}/api/admin/logs?limit=5`, {
    headers: { Authorization: `Bearer ${admin.accessToken}` },
  })
  const lines = Array.isArray(resp.json?.lines) ? resp.json.lines : []
  evidence('adminLogs', {
    status: resp.status,
    total: resp.json?.total,
    limit: resp.json?.limit,
    offset: resp.json?.offset,
    lines,
  })
  const stampOf = (line) => {
    const m = /^(\d{4})\/(\d{2})\/(\d{2}) (\d{2}:\d{2}:\d{2})/.exec(String(line))
    if (!m) return null
    return Date.parse(`${m[1]}-${m[2]}-${m[3]}T${m[4]}+08:00`)
  }
  const stamps = lines.map(stampOf).filter((v) => v !== null)
  const descending = stamps.every((v, i) => i === 0 || stamps[i - 1] >= v)
  const headSingle = (
    await raw(`${server}/api/admin/logs?limit=1&offset=0`, {
      headers: { Authorization: `Bearer ${admin.accessToken}` },
    })
  ).json?.lines?.[0]

  record(
    '⑨-1 GET /api/admin/logs?limit=5 返回分页形状（含 total，行数 ≤ limit）',
    resp.status === 200 &&
      Array.isArray(resp.json?.lines) &&
      typeof resp.json?.total === 'number' &&
      lines.length <= 5,
    `HTTP ${resp.status} 返回 ${lines.length} 行 total=${resp.json?.total} limit=${resp.json?.limit} offset=${resp.json?.offset} ` +
      `响应键=${JSON.stringify(Object.keys(resp.json ?? {}))}`,
  )
  record(
    '⑨-2 日志顺序为**新→旧**（offset=0 是最新一行）',
    stamps.length >= 2 ? descending && lines[0] === headSingle : lines.length > 0 && lines[0] === headSingle,
    stamps.length >= 2
      ? `可解析时间戳 ${stamps.length}/${lines.length} 行；降序=${descending}；最近三行=${JSON.stringify(lines.slice(0, 3))}`
      : `行内无标准时间戳（回退判据）：offset=0 首行=${JSON.stringify(lines[0])} vs limit=1 首行=${JSON.stringify(headSingle)}`,
  )

  const denied = await raw(`${server}/api/admin/logs?limit=5`, {
    headers: { Authorization: `Bearer ${nonAdminToken}` },
  })
  evidence('adminLogsNonAdmin', { status: denied.status, body: denied.text })
  record(
    '⑨-3 非 admin 调 /api/admin/logs → 403',
    denied.status === 403,
    `用普通用户 token 调 → HTTP ${denied.status} body=${denied.text}`,
  )
}

// ---------- 主流程 ----------

async function main() {
  if (!preflight()) return

  const health = await raw(`${SERVER_URL}/healthz`)
  report.health = health.status
  if (health.status !== 200) {
    console.error(`服务端 ${SERVER_URL}/healthz → HTTP ${health.status}，无法继续`)
    process.exitCode = 1
    return
  }
  const probe = await raw(`${SERVER_URL}/api/auth/me`)
  if (probe.status === 404) {
    console.error(`服务端 ${SERVER_URL} 未开启账号能力（GET /api/auth/me → 404）：确认实例带 PR_DB_DSN 启动`)
    process.exitCode = 1
    return
  }
  console.log(`服务端 ${SERVER_URL} 就绪（/healthz 200，/api/auth/me → ${probe.status}）`)

  const adminCred = await readAdminCred()
  console.log(`管理员凭据已就绪（username=${adminCred.username}，口令不落报告）`)
  const adminResp = await withRateLimitRetry('管理员登录', () =>
    raw(`${SERVER_URL}/api/auth/login`, {
      method: 'POST',
      body: { username: adminCred.username, password: adminCred.password },
    }),
  )
  if (adminResp.status !== 200 || !adminResp.json?.accessToken) {
    throw new Error(`管理员登录失败：HTTP ${adminResp.status} ${adminResp.text.slice(0, 200)}`)
  }
  if ((adminResp.json?.user?.role ?? '') !== 'admin') {
    throw new Error(`凭据对应的账号 role=${adminResp.json?.user?.role}，不是 admin`)
  }
  const admin = { accessToken: adminResp.json.accessToken, username: adminCred.username }
  report.adminUser = admin.username

  let hostBrowser = null
  let guestBrowser = null
  let cdpHost = null
  let cdpGuest = null
  try {
    // ---------- ① 注册 ----------
    console.log('\n=== ① 注册 ===')
    const victim = await step1Register(SERVER_URL)
    if (!victim) return
    report.victim = { id: victim.user.id, username: victim.username }

    // ---------- ② 登录 Cookie ----------
    console.log('\n=== ② 登录 Cookie 属性 ===')
    const session = await step2LoginCookie(SERVER_URL, victim.username)
    report.victim.accessToken = fingerprint(session.accessToken)

    // ---------- ③④ 刷新轮换 / 重放 ----------
    console.log('\n=== ③④ 刷新轮换与重放 ===')
    await step34Rotation(SERVER_URL, session)

    // ---------- ⑧ 建房与归属（房主身份 = 受害者账号重新登录）----------
    console.log('\n=== ⑧ 归属一致 ===')
    const ownerLogin = await loginUser(SERVER_URL, { username: victim.username, password: PASSWORD })
    const owner = { ...victim, accessToken: ownerLogin.token }
    const created = await ownerCreateRoom(SERVER_URL, owner.accessToken)
    report.room = created.json?.roomId ?? null
    await step8Ownership(SERVER_URL, admin, owner, created)
    if (created.status !== 200 || !created.json?.roomId) return

    // ---------- ⑦ 匿名建房 ----------
    console.log('\n=== ⑦ 匿名建房 ===')
    await step7AnonymousCreate(SERVER_URL)

    // ---------- ⑥ 游客进房（两个真实浏览器，房间还新鲜）----------
    console.log('\n=== ⑥ 游客进房（真实浏览器） ===')
    const chromePath = findChrome()
    hostBrowser = await startChrome(chromePath, BASE_PORT, 't11-host')
    guestBrowser = await startChrome(chromePath, BASE_PORT + 1, 't11-guest')
    cdpHost = await openTarget(BASE_PORT, 't11-host')
    cdpGuest = await openTarget(BASE_PORT + 1, 't11-guest')
    await step6Guest(
      SERVER_URL,
      CLIENT_URL,
      created.json.roomId,
      created.json.hostToken,
      cdpHost,
      cdpGuest,
    )

    // ---------- ⑤ 封禁两件事（同一个房间，连接生命周期内房间仍在）----------
    console.log('\n=== ⑤ 封禁两件事 ===')
    await step5Ban(SERVER_URL, admin, victim, created.json.roomId)

    // ---------- ⑨ 管理端日志 ----------
    console.log('\n=== ⑨ 管理端日志（附加） ===')
    const plain = await withLibRateLimitRetry('⑨非管理员对照账号注册', () =>
      registerUser(SERVER_URL, {
        username: `t11n_${Math.random().toString(36).slice(2, 10)}`,
        password: PASSWORD,
      }),
    )
    await step9AdminLogs(SERVER_URL, admin, plain.token)

    // ---------- 公共管线自检：既有回归脚本建房的前提 ----------
    // 注意 ensureAccount 的**真实语义**：它在 register 抛错时（含 429 限速）直接返回 null 并
    // **把这个 null 缓存一辈子**（`accountCache` 一旦写入就只返回它），所以"同一个进程里多调几次"
    // 不是有效的重试策略 —— 实测形态是 5 次调用全返回同一个 null，而 60 秒后换一个新进程调用
    // 立刻拿到 token（说明账号能力是好的，被撞的是注册限速桶：默认 5/分钟、容量 3）。
    // 因此这里先等 70 秒让桶回满，再调一次；重试仍保留，但会说明等的是限速而不是产品缺陷。
    console.log('  · ⑩ 之前先等 70 秒：本用例前面的注册已把注册限速桶（默认 5/分钟、容量 3）用掉')
    await sleep(70000)
    let shared = await ensureAccount(SERVER_URL)
    for (let i = 1; i <= 3 && !shared; i += 1) {
      console.log(`  · ensureAccount 返回 null（撞注册限速），再等 25s（第 ${i}/3 次）`)
      await sleep(25000)
      shared = await ensureAccount(SERVER_URL)
    }
    record(
      '⑩ 公共管线 ensureAccount 能拿到 token（既有回归脚本建房的前提）',
      Boolean(shared),
      shared
        ? `取得 token=${fingerprint(shared)}`
        : '等满 145 秒后仍返回 null（此时更可能是账号能力关闭，而不是限速；见脚本注释）',
    )
  } finally {
    for (const cdp of [cdpHost, cdpGuest]) {
      try {
        cdp?.close()
      } catch {
        // 忽略
      }
    }
    for (const browser of [hostBrowser, guestBrowser]) {
      try {
        browser?.close()
      } catch {
        // 忽略
      }
    }
  }
}

/** 强制退出但先让 stdout 冲刷（与既有脚本同一约定）。 */
async function finish() {
  try {
    await main()
  } catch (err) {
    record('流程完整执行', false, `中断：${err.message}`)
  }
  const failed = checks.filter((c) => !c.pass)
  console.log('\n=== 账号一期验收结果 ===')
  console.log(
    JSON.stringify(
      { ...report, checks: checks.map((c) => ({ name: c.name, pass: c.pass, detail: c.detail })) },
      null,
      2,
    ),
  )
  console.log(`\n判据：${checks.length - failed.length}/${checks.length} 通过`)
  for (const c of failed) console.log(`  ✗ ${c.name} — ${c.detail}`)
  const pass = failed.length === 0 && checks.length > 0
  console.log(`\n判定：${pass ? 'PASS' : 'FAIL'}`)
  await sleep(300)
  process.exit(pass ? 0 : 1)
}

await finish()
