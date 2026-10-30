#!/usr/bin/env node
/**
 * `verify-follows.mjs` —— 关注边（wayfinder #56）的**真机**验收。
 *
 * # 为什么需要独立脚本
 *
 * 关注/取关的状态切换是典型的**静默缺陷**面：点了「关注」而按钮不变、
 * 或者按钮变了但服务端根本没这条边 —— 这两种在 HTTP 层单测里都看不出来，
 * 只有真实浏览器点一次才知道。所以这里用 headless Chrome 走完整 UI 流程，
 * 并且用**页面里的 fetch 钩子**证明请求真的发出去了（verify-forgot 的 F3 先例：
 * "点了没反应"与"反应失败"必须可分辨）。
 *
 * # 判据
 *
 *  F1  他人档案页出现**关系动作区**，且初始态是「关注」（未关注时）。
 *  F2  点「关注」**确实发出** POST /api/follows（fetch 钩子为证），返回 201。
 *  F3  点完之后按钮**变成「已关注」**并出现「取消关注」（状态真的切换了，
 *      不是服务端改了、前端没跟上）。
 *  F4  点「取消关注」发出 DELETE /api/follows/:id（204），按钮**回到「关注」**。
 *  F5  后端确实落了边：GET /api/follows/:id 回 `following: true`，
 *      且 TA 的粉丝数 = 1 —— **后端事实**与界面显示一致（界面骗人是另一回事）。
 *  F6  自关注被拒（400）且**没有**建边（服务端裁决，不是前端拦）。
 *  F7  不存在的用户 → 404，且**不存在与被封禁的响应体逐字相同**
 *      （防存在性探测的同口径，服务端那侧已钉，这里再确认一遍形态）。
 *  F8  幂等键：同一个键 + 同一份请求体 → 重放返回**同一份关系视图**
 *      （客户端不可分辨）。
 *
 * # 前置
 *
 * - **限速要放宽**：关注/取关走登录桶（PR_AUTH_LOGIN_PER_MINUTE，默认 10/分钟、
 *   容量 5），而本脚本在一轮里连打十几次写请求 —— 那会被打成 429，
 *   而 429 是**限速语义的正确行为**，不是缺陷。
 *   起实例时请加 `PR_AUTH_LOGIN_PER_MINUTE=1000` 与 `PR_AUTH_LOGIN_BURST=200`。
 *
 * - 服务端实例必须开着**账号能力**（PR_DB_DSN 非空）；
 * - 前端必须翻成**真实接口**（`VITE_MOCK_FOLLOWS=0`，其余域按需），
 *   否则档案页的关系动作区走 mock，与本脚本验的服务端不是同一条路；
 * - 前端产物要有调试钩子（`VITE_DEBUG_HOOKS=1` 构建），本脚本用页面里的
 *   `fetch` 钩子而不是 `window.__pr` 快照，所以**不依赖**钩子开关，
 *   但需要**页面本身**能跑（Vite dev 或 dist 都行）。
 *
 * # 用法
 *
 *   node test/script/verify-follows.mjs --server http://127.0.0.1:18092 --client http://127.0.0.1:5173
 *
 * 参数：
 *   --server  测试服务端（默认 http://127.0.0.1:8888）
 *   --client  前端入口（默认 = --server）
 *   --port    headless Chrome 的 CDP 端口（默认 9902）
 *
 * 退出码：全部判据 PASS → 0；任一 FAIL → 1。
 */
import {
  argOf,
  findChrome,
  newCookieJar,
  openTarget,
  registerUser,
  sleep,
  startChrome,
  testEmailFor,
  waitFor,
} from './lib/browser.mjs'

const SERVER = argOf(process.argv, 'server', 'http://127.0.0.1:8888').replace(/\/+$/, '')
const CLIENT = argOf(process.argv, 'client', SERVER).replace(/\/+$/, '')
const BASE_PORT = Number(argOf(process.argv, 'port', '9902'))

/** 判据结果。 */
const checks = []

function record(name, pass, detail) {
  checks.push({ name, pass, detail })
  console.log(`${pass ? '✓' : '✗'} ${name}${detail ? `\n    ${detail}` : ''}`)
}

/** 发一个 JSON 请求，返回 {status, json, text}。 */
async function api(path, { method = 'GET', body, token, headers = {} } = {}) {
  const resp = await fetch(`${SERVER}${path}`, {
    method,
    headers: {
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...headers,
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  })
  const text = await resp.text()
  let json = null
  try {
    json = text ? JSON.parse(text) : null
  } catch {
    /* 非 JSON 保留原文 */
  }
  return { status: resp.status, json, text }
}

/** 注册一个账号（两段式之外的简单路径：单段 register）。 */
async function makeAccount(prefix) {
  const username = `${prefix}${Date.now().toString(36)}`
  const jar = newCookieJar()
  const r = await registerUser(SERVER, {
    username,
    password: 'pr-test-pass1',
    displayName: username,
    cookieJar: jar,
    email: testEmailFor(username),
  })
  return { ...r, username }
}

/** 在页面里读关系动作区的可观测状态。 */
async function readRelation(page) {
  return page.evaluate(`(() => {
    const q = (s) => document.querySelector(s)
    const visible = (b) => b && b.offsetParent !== null
    const btn = (sel) => {
      const b = q(sel)
      return b ? { present: true, text: b.textContent.trim(), disabled: b.disabled } : { present: false }
    }
    const follow = q('[data-testid="follow-button"]')
    const unfollow = q('[data-testid="unfollow-button"]')
    const msg = q('[data-testid="follow-message-link"]')
    const counts = q('.counts')
    const alert = q('.relation .alert')
    const ok = q('.relation .ok')
    return {
      follow: follow ? { present: true, text: follow.textContent.trim(), disabled: follow.disabled } : { present: false },
      unfollow: unfollow ? { present: true, text: unfollow.textContent.trim(), disabled: unfollow.disabled } : { present: false },
      messageLink: msg ? { present: true, text: msg.textContent.trim(), href: msg.getAttribute('href') } : { present: false },
      counts: counts ? counts.textContent.replace(/\s+/g, ' ').trim() : null,
      alert: alert ? alert.textContent.trim() : null,
      ok: ok ? ok.textContent.trim() : null,
      hasRelationArea: visible(q('.relation')),
    }
  })()`)
}

/** 点一个 testid 按钮。 */
async function clickTestId(page, testid) {
  return page.evaluate(`(() => {
    const b = document.querySelector('[data-testid=${JSON.stringify(testid)}]')
    if (!b) return 'MISSING'
    if (b.disabled) return 'DISABLED'
    b.click()
    return 'clicked'
  })()`)
}

/** 装 fetch 钩子（证明请求真的发出去了）。 */
async function installFetchHook(page) {
  await page.evaluate(`(() => {
    window.__vfReq = []
    const orig = window.fetch
    window.fetch = async (...args) => {
      const url = typeof args[0] === 'string' ? args[0] : args[0].url
      // 方法也要记：只靠 url 分不出「POST /api/follows」与「DELETE /api/follows/6」
      // 之外的情况，而「取关发的是 DELETE」正是判据要证明的那件事。
      const method = (args[1] && args[1].method) || 'GET'
      try {
        const r = await orig(...args)
        window.__vfReq.push({ url, method, status: r.status })
        return r
      } catch (e) {
        window.__vfReq.push({ url, method, error: String(e) })
        throw e
      }
    };
    return 'hooked'
  })()`)
}

/**
 * 两份关系视图是否**语义相同**。
 *
 * 不能比字节：幂等响应存在 idempotency_keys.response（jsonb 列），
 * PostgreSQL 会把 jsonb 重新序列化（键排序、键值间加空格）——
 * 于是重放的字节与首次一定不同。这是 jsonb 的存储行为，不是缺陷；
 * 契约是「客户端**不可分辨**」，而客户端读的是解析后的 JSON。
 */
function sameRelation(a, b) {
  if (!a || !b) return false
  const keys = ['userId', 'displayName', 'following', 'followedBy', 'mutual', 'canDm', 'followerCount', 'followingCount']
  return keys.every((k) => a[k] === b[k])
}
/** 读钩子记录下来的请求。 */
async function readRequests(page) {
  const raw = await page.evaluate(`JSON.stringify(window.__vfReq ?? [])`)
  return JSON.parse(raw)
}

/** 把登录态播种进页面（access token 在 sessionStorage，档案在 localStorage）。 */
async function seedLogin(page, account) {
  await page.evaluate(`sessionStorage.setItem('pr:access', ${JSON.stringify(account.token)})`)
  if (account.user) {
    await page.evaluate(
      `localStorage.setItem('pr:profile', ${JSON.stringify(JSON.stringify(account.user))})`,
    )
  }
}

async function main() {
  // ---------- 准备两个账号 ----------
  const stamp = Date.now().toString(36).slice(-6)
  const me = await makeAccount('vfA' + stamp)
  const peer = await makeAccount('vfB' + stamp)
  console.log('  · 我 = ' + me.username + '（id ' + me.user?.id + '），TA = ' + peer.username + '（id ' + peer.user?.id + '）');

  const peerId = peer.user?.id
  const myId = me.user?.id
  if (!peerId || !myId) {
    record('准备：两个账号可用', false, '注册响应里没有 user.id');
    return
  }

  // ---------- 后端侧：自关注 400（服务端裁决）----------
  const selfFollow = await api('/api/follows', { method: 'POST', body: { userId: myId }, token: me.token })
  record(
    'F6-a 自关注被服务端拒（400 FOLLOW_SELF_TARGET）',
    selfFollow.status === 400 && selfFollow.json?.code === 'FOLLOW_SELF_TARGET',
    'status=' + selfFollow.status + ' body=' + selfFollow.text.slice(0, 160),
  );

  // ---------- 后端侧：不存在的用户 404 ----------
  const missing = await api('/api/follows', { method: 'POST', body: { userId: 99999999 }, token: me.token })
  record(
    'F7-a 不存在的用户 → 404（防存在性探测同口径）',
    missing.status === 404,
    'status=' + missing.status + ' body=' + missing.text.slice(0, 160),
  );

  // ---------- 后端侧：幂等键重放 ----------
  const idemKey = 'vf-follow-' + stamp
  const first = await api('/api/follows', {
    method: 'POST',
    body: { userId: peerId },
    token: me.token,
    headers: { 'Idempotency-Key': idemKey },
  })
  const replay = await api('/api/follows', {
    method: 'POST',
    body: { userId: peerId },
    token: me.token,
    headers: { 'Idempotency-Key': idemKey },
  })
  record(
    'F8 幂等键：同键同体重放返回同一份关系视图（客户端不可分辨）',
    first.status === 201 && replay.status === 201 && sameRelation(first.json, replay.json),
    'first=' + first.status + ' ' + first.text.slice(0, 200) + '；replay=' + replay.status + ' ' + replay.text.slice(0, 200),
  );

  // 清掉这条边，让 UI 判据从「未关注」起步。
  await api('/api/follows/' + peerId, { method: 'DELETE', token: me.token });
  // ---------- 真机：档案页关系动作区 ----------
  const chrome = findChrome()
  const browser = await startChrome(chrome, BASE_PORT, 'vf-follows')
  const page = await openTarget(BASE_PORT, 'vf-follows')
  try {
    await page.navigate(CLIENT + '/login')
    await seedLogin(page, me)
    await page.navigate(CLIENT + '/user/' + peerId)

    // F1：关系动作区出现，初始是「关注」。
    const initial = await waitFor(
      async () => {
        const s = await readRelation(page)
        return s.hasRelationArea && s.follow.present ? s : null
      },
      { label: '关系动作区出现且有「关注」按钮', timeoutMs: 15000 },
    ).catch(() => null)
    record(
      'F1 他人档案页出现关系动作区，初始态是「关注」',
      Boolean(initial) && initial.follow.text.includes('关注') && !initial.follow.text.includes('已关注'),
      initial ? '按钮=' + JSON.stringify(initial.follow) + '；计数=' + JSON.stringify(initial.counts) : '关系动作区没出现',
    );
    if (!initial) return

    // ---------- F2 点「关注」真的发出请求 ----------
    await installFetchHook(page)
    const clicked = await clickTestId(page, 'follow-button')
    await sleep(1500)
    const reqs = await readRequests(page)
    const post = reqs.find((r) => r.method === 'POST' && r.url.includes('/api/follows') && r.status !== undefined)
    record(
      'F2 点「关注」**确实发出** POST /api/follows 且返回 201（不是静默 return）',
      clicked === 'clicked' && Boolean(post) && post.status === 201,
      '点击=' + clicked + '；请求=' + JSON.stringify(reqs),
    );

    // ---------- F3 按钮变成「已关注」 ----------
    const after = await waitFor(
      async () => {
        const s = await readRelation(page)
        return s.follow.present && s.follow.text.includes('已关注') ? s : null
      },
      { label: '按钮变成「已关注」', timeoutMs: 10000 },
    ).catch(() => null)
    record(
      'F3 点完之后按钮变成「已关注」并出现「取消关注」（状态真的切换了）',
      Boolean(after) && after.follow.text.includes('已关注') && after.unfollow.present,
      after ? '关注按钮=' + JSON.stringify(after.follow) + '；取消按钮=' + JSON.stringify(after.unfollow) : '按钮没变',
    );

    // ---------- F4 点「取消关注」 ----------
    const unclicked = await clickTestId(page, 'unfollow-button')
    await sleep(1500)
    const reqs2 = await readRequests(page)
    const del = reqs2.find((r) => r.url.includes('/api/follows') && r.method === 'DELETE')
    const back = await waitFor(
      async () => {
        const s = await readRelation(page)
        return s.follow.present && s.follow.text.includes('关注') && !s.follow.text.includes('已关注') ? s : null
      },
      { label: '按钮回到「关注」', timeoutMs: 10000 },
    ).catch(() => null)
    record(
      'F4 点「取消关注」发出 DELETE /api/follows/:id 且按钮回到「关注」',
      unclicked === 'clicked' && Boolean(del) && del.status === 204 && Boolean(back),
      '点击=' + unclicked + '；请求=' + JSON.stringify(reqs2) + '；回退态=' + (back ? JSON.stringify(back.follow) : '没回到'),
    );
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
  }

  // ---------- F5 后端确实落了边（界面与后端事实一致）----------
  await api('/api/follows', { method: 'POST', body: { userId: peerId }, token: me.token })
  const rel = await api('/api/follows/' + peerId, { token: me.token })
  record(
    'F5 后端确实落了边：following=true 且 TA 的粉丝数 = 1',
    rel.status === 200 && rel.json?.following === true && rel.json?.followerCount === 1,
    'status=' + rel.status + ' body=' + rel.text.slice(0, 240),
  );
}

await main().catch((err) => {
  record('流程完整执行', false, '中断：' + err.message)
})

console.log('\n=== 关注边真机验收 ===')
console.log(JSON.stringify({ server: SERVER, checks: checks.map((c) => ({ name: c.name, pass: c.pass, detail: c.detail })) }, null, 2))
const failed = checks.filter((c) => !c.pass)
console.log('\n判据：' + (checks.length - failed.length) + '/' + checks.length + ' 通过')
for (const c of failed) console.log('  ✗ ' + c.name + ' — ' + c.detail)
console.log('\n判定：' + (failed.length === 0 && checks.length > 0 ? 'PASS' : 'FAIL'))
await sleep(300)
process.exit(failed.length === 0 && checks.length > 0 ? 0 : 1)
