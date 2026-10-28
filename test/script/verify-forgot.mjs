#!/usr/bin/env node
/**
 * `verify-forgot.mjs` —— 「找回密码（邮箱验证码）」的**真机**验收。
 *
 * # 为什么需要独立一个脚本，而不是塞进 verify-auth
 *
 * 这条链路的判据**必须**在真实浏览器里跑：本轮修掉的两个缺陷都是**静默**的
 *（点了按钮没反应 / 改密成功但表单还停在第二阶段），它们在服务端单测与 HTTP 层
 * 验收里完全看不出来 —— 请求压根没发出去，或者发了但界面状态不对，两种都没有
 * 可观测的错误。因此这里用 headless Chrome 走完整 UI 流程。
 *
 * 但它**不能**并进 `verify-auth`：那个脚本的前提是"实例配齐 SMTP 或按 503 口径探测"，
 * 而这里需要**拿到验证码明文**才能走通第二阶段 —— 只有 `PR_OTP_DEV_ECHO=1`
 * 的实例给得起这个值。两者对实例的要求互相矛盾，分成两个脚本才不会互相打挂。
 *
 * # 判据
 *
 *  F1  `GET /login?tab=forgot` 渲染出的是**找回密码**表单（第 1 段有邮箱框与
 *      「发送验证码」按钮）。
 *      —— 这条钉住的是真机踩过的坑：`useAuthForm` 曾经只吃 `initialTab` 参数，
 *         丢了"从 `?tab=` 读页签"那段逻辑，于是深链打开后停在**登录**页签，
 *         找回密码的表单根本没渲染。症状是"点了链接却看到登录框"。
 *  F2  第 1 段能打开**人机验证子窗体**，通过后进入第 2 段并拿到验证码
 *     （dev echo 模式下界面直接显示它）。
 *  F3  第 2 段填完码与新密码后点「设置新密码」**真的发出了请求**并且成功（204）。
 *      —— 这条钉住本轮那个"没有任何反馈"的缺陷：`submit()` 先置 `busy = true`
 *         再调 `submitForgotReset()`，而后者开头又 `if (!canSubmit.value) return`，
 *         `canSubmit` 第一条是 `if (busy.value) return false` —— 自己把自己挡回去，
 *         静默 return。fetch 钩子在这里是**唯一**能证明"请求发出去了"的手段。
 *  F4  成功之后界面**回到登录页签**、找回密码的输入被清空、并且保住那句提示。
 *      —— `/login` 与 `/login?tab=forgot` 命中同一个组件实例，不重置就会停在
 *         第 2 段并把旧验证码留在屏幕上。
 *  F5  改密在后端**确实生效**：新口令能登录（200），旧口令失效（401）。
 *      —— F3/F4 只证明界面走通了；这条证明它改的是正确的东西。
 *
 * # 前置
 *
 * 服务端实例必须 `PR_OTP_DEV_ECHO=1`（否则 F2 拿不到码，脚本会明确报"缺 dev echo"
 * 而不是假绿）。注册走**两段式** API（`register/start` → `register/email` →
 * `register/verify`），因为建号即视为邮箱已验证，找回密码需要的是一个**存在**的账号
 *（是否已验证不影响 —— 那正是本次改造的重点，见 F6）。
 *
 * # 用法
 *
 *   node test/script/verify-forgot.mjs --server http://127.0.0.1:18091
 *
 * 参数：
 *   --server   测试服务端（**默认 http://127.0.0.1:8888**）
 *   --port     headless Chrome 的 CDP 端口（默认 9900）
 *   CHROME_PATH 环境变量可指定浏览器路径
 */
import {
  argOf,
  findChrome,
  openTarget,
  sleep,
  startChrome,
  testEmailFor,
  waitFor,
} from './lib/browser.mjs'

const SERVER = argOf(process.argv, 'server', 'http://127.0.0.1:8888')
const BASE_PORT = Number(argOf(process.argv, 'port', '9900'))
const OLD_PASSWORD = 'abcd1234'
const NEW_PASSWORD = 'fgtnewpwd1234'

/** 判据结果。 */
const checks = []
const evidence = {}

function record(name, pass, detail) {
  checks.push({ name, pass, detail })
  console.log(`${pass ? '✓' : '✗'} ${name}${detail ? `\n    ${detail}` : ''}`)
}

/** POST JSON，返回 {status, json, text}。 */
async function postJson(path, body, headers = {}) {
  const resp = await fetch(`${SERVER}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...headers },
    body: JSON.stringify(body),
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

/**
 * 建一个可找回密码的账号（两段式注册）。
 *
 * 用它而不是 `registerUser`（lib 里那个走单段 `/api/auth/register`）：
 * 那个端点建出来的账号邮箱是**未验证**的，虽然找回密码现在也能走通（F6 验的就是它），
 * 但两段式是当前的主路径，而且它顺带把 dev echo 的拿码方式验一遍。
 */
async function registerTwoStage(username) {
  const email = testEmailFor(username)
  const rs = await postJson('/api/auth/register/start', { username, displayName: username, password: OLD_PASSWORD }, {
    'Idempotency-Key': `vf-start-${username}`,
  })
  if (rs.status !== 200 || !rs.json?.ticket) {
    throw new Error(`register/start 失败：HTTP ${rs.status} ${rs.text.slice(0, 200)}`)
  }
  const ticket = rs.json.ticket
  const re = await postJson('/api/auth/register/email', { ticket, email }, { 'Idempotency-Key': `vf-email-${username}` })
  const devCode = re.json?.devCode ?? ''
  if (re.status !== 200 && re.status !== 204) {
    throw new Error(`register/email 失败：HTTP ${re.status} ${re.text.slice(0, 200)}`)
  }
  if (!devCode) {
    throw new Error(
      `register/email 没回 devCode —— 实例必须开 PR_OTP_DEV_ECHO=1 才能跑本脚本（拿不到验证码就无法走完找回密码）`,
    )
  }
  const rv = await postJson('/api/auth/register/verify', { ticket, code: devCode }, { 'Idempotency-Key': `vf-verify-${username}` })
  if (rv.status !== 201) {
    throw new Error(`register/verify 失败：HTTP ${rv.status} ${rv.text.slice(0, 200)}`)
  }
  return { email, username }
}

/**
 * 解人机验证题面（按顺序点图形）。
 *
 * 它是**前端自答**：题面与校验都在组件里（`CaptchaChallenge.vue`），
 * 目标图形的中文名就在 `.order-item .sr` 里。所以这里不是"破解"，
 * 而是像人一样照题面点 —— 验收要的是"这条路走得通"，不是"验证强度"
 *（它的定位本来就是摩擦，不是安全边界）。
 */
async function solveCaptcha(page) {
  const names = await page.evaluate(`(() => {
    const d = document.querySelector('[role="dialog"]')
    if (!d) return null
    return [...d.querySelectorAll('.order-item .sr')].map((s) => s.textContent.trim())
  })()`)
  if (!names || names.length === 0) return false
  for (const name of names) {
    const r = await page.evaluate(`(() => {
      const cell = [...document.querySelectorAll('[role="dialog"] .cell')]
        .find((c) => (c.getAttribute('aria-label') || '').includes(${JSON.stringify(name)}))
      if (!cell) return 'NO-CELL'
      cell.click()
      return 'ok'
    })()`)
    if (r === 'NO-CELL') return false
    await sleep(120)
  }
  return true
}

/** 在页面里读找回密码表单的可观测状态。 */
async function readForm(page) {
  return page.evaluate(`(() => {
    const q = (s) => document.querySelector(s)
    const visible = (b) => b && b.offsetParent !== null
    const byText = (t) => [...document.querySelectorAll('button')].find((x) => visible(x) && x.textContent.includes(t))
    const onTab = (t) => {
      const b = [...document.querySelectorAll('.tabs [role="tab"]')].find((x) => x.textContent.trim() === t)
      return b ? b.className.includes('on') : false
    }
    return {
      url: location.pathname + location.search,
      tabLogin: onTab('登录'),
      tabForgot: onTab('找回密码'),
      email: q('#auth-email') ? q('#auth-email').value : null,
      otp: q('#auth-otp') ? q('#auth-otp').value : null,
      newpwd: q('#auth-new-password') ? q('#auth-new-password').value : null,
      alert: q('.alert') ? q('.alert').textContent.trim() : null,
      okMsg: q('.ok') ? q('.ok').textContent.trim() : null,
      devHint: q('.dev-hint') ? q('.dev-hint').textContent.replace(/\\s+/g, ' ').trim() : null,
      dialog: !!document.querySelector('[role="dialog"]'),
      sendBtn: byText('发送验证码') ? { present: true, disabled: byText('发送验证码').disabled } : { present: false },
      resetBtn: byText('设置新密码') ? { present: true, disabled: byText('设置新密码').disabled } : { present: false },
    }
  })()`)
}

const setInput = (page, sel, value) =>
  page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(sel)})
    if (!el) return 'MISSING'
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set
    setter.call(el, ${JSON.stringify(value)})
    el.dispatchEvent(new Event('input', { bubbles: true }))
    return 'ok'
  })()`)

const clickBtn = (page, text) =>
  page.evaluate(`(() => {
    const b = [...document.querySelectorAll('button')]
      .find((x) => x.offsetParent && x.textContent.includes(${JSON.stringify(text)}))
    if (!b) return 'NO-BUTTON'
    if (b.disabled) return 'DISABLED'
    b.click()
    return 'clicked'
  })()`)

async function main() {
  const stamp = Date.now().toString(36)
  const account = await registerTwoStage(`vf${stamp}`)
  console.log(`  · 账号 ${account.username} <${account.email}>（两段式注册，邮箱已验证）`)

  const chrome = findChrome()
  const browser = await startChrome(chrome, BASE_PORT, 'vf-forgot')
  const page = await openTarget(BASE_PORT, 'vf-forgot')
  try {
    // ---------- F1 深链渲染找回密码 ----------
    await page.navigate(`${SERVER}/login?tab=forgot`)
    await sleep(700)
    const first = await readForm(page)
    record(
      'F1 深链 /login?tab=forgot 渲染的是找回密码表单第 1 段（不是登录页签）',
      first.tabForgot && first.email !== null && first.sendBtn.present,
      `页签[找回密码]=${first.tabForgot} 页签[登录]=${first.tabLogin}；邮箱框=${first.email !== null ? '在' : '无'}；「发送验证码」按钮=${JSON.stringify(first.sendBtn)}；url=${first.url}`,
    )
    if (!(first.tabForgot && first.sendBtn.present)) return

    // ---------- F2 人机验证 → 第 2 段 ----------
    await setInput(page, '#auth-email', account.email)
    const clicked = await clickBtn(page, '发送验证码')
    record('F2-a 第 1 段的「发送验证码」可点（邮箱合法时不禁用）', clicked === 'clicked', `${clicked}`)
    await sleep(500)

    const dialogUp = await page.evaluate(`!!document.querySelector('[role="dialog"]')`)
    record('F2-b 点了之后弹出人机验证**页内子窗体**', dialogUp === true, `dialog=${dialogUp}`)
    const solved = await solveCaptcha(page)
    record('F2-c 按题面点完图形（子窗体自行 emit pass）', solved, `solveCaptcha=${solved}`)

    const toSecond = await waitFor(async () => (await readForm(page)).otp !== null, {
      label: '进入第 2 段（出现验证码输入框）',
      timeoutMs: 10000,
    })
      .then(() => true)
      .catch(() => false)
    const second = await readForm(page)
    const shownCode = second.devHint ? second.devHint.match(/\d{6}/)?.[0] ?? '' : ''
    record(
      'F2-d 验证通过后进入第 2 段，且界面显示验证码（dev echo）',
      toSecond && Boolean(shownCode),
      `第 2 段=${toSecond}；dev-hint=${JSON.stringify(second.devHint)}；解析到的码=${shownCode || '无'}；提示=${JSON.stringify(second.okMsg)}`,
    )
    if (!shownCode) return

    // ---------- F3 点「设置新密码」真的发出请求 ----------
    // fetch 钩子：这条判据的**全部价值**在于区分"没反应"与"反应失败"。
    await page.evaluate(`(() => {
      window.__vfReq = []
      const orig = window.fetch
      window.fetch = async (...args) => {
        const url = typeof args[0] === 'string' ? args[0] : args[0].url
        try {
          const r = await orig(...args)
          window.__vfReq.push({ url, status: r.status })
          return r
        } catch (e) {
          window.__vfReq.push({ url, error: String(e) })
          throw e
        }
      }
      window.__vfErr = []
      addEventListener('error', (e) => window.__vfErr.push(String(e.message)))
      addEventListener('unhandledrejection', (e) => window.__vfErr.push('unhandled: ' + String(e.reason)))
      return 'hooked'
    })()`)

    await setInput(page, '#auth-otp', shownCode)
    await setInput(page, '#auth-new-password', NEW_PASSWORD)
    const beforeClick = await readForm(page)
    record(
      'F3-a 填完码与强口令后「设置新密码」不再禁用',
      beforeClick.resetBtn.present && beforeClick.resetBtn.disabled === false,
      `按钮状态=${JSON.stringify(beforeClick.resetBtn)}`,
    )

    const clickRes = await clickBtn(page, '设置新密码')
    await sleep(2500)
    const reqs = await page.evaluate(`JSON.stringify(window.__vfReq ?? [])`)
    const errs = await page.evaluate(`JSON.stringify(window.__vfErr ?? [])`)
    const sent = JSON.parse(reqs)
    const resetHit = sent.find((r) => r.url.includes('/api/auth/password/otp/reset'))
    record(
      'F3-b 点「设置新密码」**确实发出了** /api/auth/password/otp/reset 请求（不是静默 return）',
      Boolean(resetHit),
      `点击结果=${clickRes}；请求列表=${reqs}；页面错误=${errs}`,
    )
    record(
      'F3-c 该请求返回 204（改密成功，无响应体）',
      resetHit?.status === 204,
      resetHit ? `status=${resetHit.status}` : '没有这条请求',
    )

    // ---------- F4 成功后界面回到登录页签并清空 ----------
    const done = await readForm(page)
    record(
      'F4 改密成功后界面切回**登录页签**、找回密码输入被清空、保住提示文案',
      done.tabLogin === true && done.otp === null && done.newpwd === null && (done.okMsg ?? '').includes('重新登录'),
      `页签[登录]=${done.tabLogin}；otp 残留=${JSON.stringify(done.otp)}；密码残留=${JSON.stringify(done.newpwd)}；提示=${JSON.stringify(done.okMsg)}；devHint 残留=${JSON.stringify(done.devHint)}`,
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
  }

  // ---------- F5 后端确实改了口令（不依赖界面）----------
  const loginNew = await postJson('/api/auth/login', { username: account.username, password: NEW_PASSWORD })
  const loginOld = await postJson('/api/auth/login', { username: account.username, password: OLD_PASSWORD })
  record(
    'F5 改密在后端生效：新口令 200，旧口令 401',
    loginNew.status === 200 && loginOld.status === 401,
    `new=${loginNew.status} old=${loginOld.status}`,
  )

  // ---------- F6 未验证邮箱**也**能走完找回（本次改造的核心）----------
  //
  // 这条才是这轮改动真正解锁的场景：单段 `/api/auth/register` 建出来的账号，
  // `email_verified_at` 是 NULL（注册时只发一封"验证你的邮箱"的信，没点就不算证明）。
  // 旧的链接通道在这里必然失败（恒 200 但**不发信**），而验证码必须能走通到改密成功 ——
  // 因为码只发到邮箱的真实持有者，"是否已被证明归属"对这条路不构成前提。
  //
  // 用一个**新账号**而不是复用上面那个：发码有 60 秒冷却，同账号连发会被静默挡掉，
  // 那会让这条判据看起来像"发不出码"而不是它真正在验的东西。
  const unvStamp = `${stamp}u`
  const unvEmail = testEmailFor(`vf${unvStamp}`)
  const unvReg = await postJson('/api/auth/register', {
    username: `vf${unvStamp}`,
    displayName: `vf${unvStamp}`,
    password: OLD_PASSWORD,
    email: unvEmail,
  })
  if (unvReg.status !== 201) {
    record('F6 未验证邮箱能走完找回密码', false, `单段注册失败：HTTP ${unvReg.status} ${unvReg.text.slice(0, 160)}`)
    return
  }
  // 先证明这个邮箱**确实**处于未验证态，否则 F6 验的是"已验证邮箱"，与 F3 重复。
  const unvMe = await fetch(`${SERVER}/api/auth/me`, {
    headers: { Authorization: `Bearer ${unvReg.json?.accessToken ?? ''}` },
  }).then(async (r) => ({ status: r.status, body: await r.json().catch(() => null) }))
  const unverifiedConfirmed = unvMe.body?.user?.emailVerified === false
  const unvOtp = await postJson('/api/auth/password/otp', { email: unvEmail })
  const unvCode = unvOtp.json?.devCode ?? ''
  const unvReset = unvCode
    ? await postJson('/api/auth/password/otp/reset', {
        email: unvEmail,
        code: unvCode,
        password: NEW_PASSWORD,
      })
    : { status: 0, text: '没拿到验证码' }
  record(
    'F6 未验证邮箱能走完找回密码（发码 + 改密 204）—— 本次改造的核心',
    unverifiedConfirmed && unvOtp.status === 200 && Boolean(unvCode) && unvReset.status === 204,
    `emailVerified=${JSON.stringify(unvMe.body?.user?.emailVerified)}（期望 false）；otp=${unvOtp.status}；devCode=${unvCode ? '已回显' : '缺失'}；reset=${unvReset.status}`,
  )
}

await main().catch((err) => {
  record('流程完整执行', false, `中断：${err.message}`)
})

console.log('\n=== 找回密码（邮箱验证码）真机验收 ===')
console.log(JSON.stringify({ server: SERVER, evidence: {}, checks: checks.map((c) => ({ name: c.name, pass: c.pass, detail: c.detail })) }, null, 2))
const failed = checks.filter((c) => !c.pass)
console.log(`\n判据：${checks.length - failed.length}/${checks.length} 通过`)
for (const c of failed) console.log(`  ✗ ${c.name} — ${c.detail}`)
console.log(`\n判定：${failed.length === 0 && checks.length > 0 ? 'PASS' : 'FAIL'}`)
await sleep(300)
process.exit(failed.length === 0 && checks.length > 0 ? 0 : 1)
