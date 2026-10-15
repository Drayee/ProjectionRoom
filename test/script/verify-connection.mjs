#!/usr/bin/env node
// 前后端连接验收：协议层探针（不依赖 Chrome）。
//
// 为什么单独有这么一个脚本：其余 verify-*.mjs 都要起 headless Chrome 才能跑，
// 而"页面连不上后端"这类问题的第一现场**往往在浏览器之前** —— Go 进程没起来、
// Vite 代理指错端口、/ws 被 Origin 白名单拒了。这个脚本用纯 HTTP/WebSocket 把
// 这三层逐条断言一遍，几十毫秒出结论，比先花十几秒起 Chrome 再看空页面高效得多。
//
// 它覆盖的四层：
//   ① 后端自身：/healthz、/api/ice、/api/public-rooms、静态页面、非法房间码
//   ② Vite 开发服务器：页面可开，且 /api 代理真的打到了 Go（而不是回退成 index.html）
//   ③ 信令链路：/ws 直连与经 Vite 代理都能升级
//   ④ Origin 白名单（S-5）：白名单内放行、白名单外 403、无 Origin 按设计放行
//
// 用法（两个实例都默认按本机开发端口）：
//   node test/script/verify-connection.mjs
//   $env:PR_BACKEND='http://127.0.0.1:8091'; node test/script/verify-connection.mjs
//
// 环境变量：
//   PR_BACKEND          后端地址，默认 http://127.0.0.1:8080
//   PR_VITE             Vite 地址，默认 http://127.0.0.1:5173（设为空串可跳过第②层）
//   PR_ALLOWED_ORIGIN   期望被放行的 Origin，默认 http://127.0.0.1:5173
//
// 退出码：全部通过为 0，有任一失败为 1（便于挂在门槛里）。

import http from 'node:http'
import { randomBytes } from 'node:crypto'

const BACKEND = process.env.PR_BACKEND || 'http://127.0.0.1:8080'
const VITE = process.env.PR_VITE === undefined ? 'http://127.0.0.1:5173' : process.env.PR_VITE
const ALLOWED_ORIGIN = process.env.PR_ALLOWED_ORIGIN || 'http://127.0.0.1:5173'

let pass = 0
let fail = 0

function report(name, ok, detail) {
  if (ok) pass++
  else fail++
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? ' :: ' + detail : ''}`)
}

/** 发一次请求，返回状态码与文本（网络不可达时 status=0，而不是抛异常）。 */
async function get(base, path, opts = {}) {
  const t0 = Date.now()
  try {
    const res = await fetch(base + path, { ...opts, signal: AbortSignal.timeout(8000) })
    const text = await res.text()
    return { status: res.status, text, ms: Date.now() - t0 }
  } catch (err) {
    return { status: 0, text: String(err), ms: Date.now() - t0 }
  }
}

/** 用 Node 内置 WebSocket 探一次升级（不支持自定义 Origin，见下面的 originCase）。 */
function wsUpgrade(url) {
  return new Promise((resolve) => {
    let opened = false
    let ws
    try {
      ws = new WebSocket(url)
    } catch (err) {
      resolve({ opened: false, code: -1, reason: 'ctor: ' + String(err) })
      return
    }
    const done = (code, reason) => {
      try {
        ws.close()
      } catch {}
      resolve({ opened, code, reason })
    }
    ws.onopen = () => {
      opened = true
    }
    ws.onerror = () => {
      /* 浏览器语义下错误后必跟 close，这里只等 close */
    }
    ws.onclose = (ev) => done(ev.code, ev.reason)
    // 连接保持住也算成功：信令是长连接，4s 内不该被服务端关掉。
    setTimeout(() => done(0, 'kept-open'), 4000)
  })
}

/**
 * 用**原始 HTTP Upgrade 握手**验证 Origin 白名单。
 *
 * 为什么不能用 Node 内置 WebSocket 的第二个参数传 { headers: { Origin } }：
 * 那个参数只接受子协议（string/array），传对象会被判非法子协议 → 握手失败 1006。
 * 于是"被白名单拒绝"与"选项不被支持"会得到同一个现象，结论完全不可信。
 * 只有自己发 Upgrade 请求、读状态码（101 放行 / 403 拒绝），两者才能分开。
 */
function originCase(host, port, origin, roomId, clientId) {
  return new Promise((resolve) => {
    const headers = {
      Connection: 'Upgrade',
      Upgrade: 'websocket',
      'Sec-WebSocket-Version': '13',
      // 必须是 16 字节随机数的 base64；截断或自造会被判协议违规 400，
      // 那会把"白名单问题"误报成"握手格式问题"。
      'Sec-WebSocket-Key': randomBytes(16).toString('base64'),
    }
    if (origin !== null) headers.Origin = origin

    const req = http.request(
      { host, port, path: `/ws?roomId=${roomId}&clientId=${clientId}`, headers },
      (res) => {
        let body = ''
        res.on('data', (c) => (body += c))
        res.on('end', () => resolve({ status: res.statusCode, upgraded: false, body: body.slice(0, 140) }))
      },
    )
    req.on('upgrade', (res) => {
      resolve({ status: res.statusCode, upgraded: true, body: '' })
      req.destroy()
    })
    req.on('error', (e) => resolve({ status: 0, upgraded: false, body: String(e) }))
    req.end()
  })
}

console.log(`== 后端 ${BACKEND}${VITE ? ` / Vite ${VITE}` : ' / (跳过 Vite)'} ==\n`)

// ---- ① 后端自身 ----
{
  const r = await get(BACKEND, '/healthz')
  report('后端 /healthz', r.status === 200 && r.text.includes('"ok"'), `${r.status} ${r.ms}ms ${r.text.slice(0, 60)}`)
}
{
  const r = await get(BACKEND, '/api/ice')
  report('后端 /api/ice 下发 ICE', r.status === 200 && r.text.includes('iceServers'), `${r.status} ${r.text.slice(0, 80)}`)
}
{
  const r = await get(BACKEND, '/api/public-rooms')
  report('后端 /api/public-rooms 免登录可读', r.status === 200 && r.text.includes('items'), `${r.status} ${r.text.slice(0, 80)}`)
}
{
  const r = await get(BACKEND, '/')
  report('后端静态托管（同端口出页面）', r.status === 200 && r.text.includes('<div id="app"'), `${r.status} ${r.text.length}B`)
}
{
  // 房间码规则 ^[A-Z0-9]{4,12}$：非法码不该被当成"房间不存在"之外的任何东西。
  const r = await get(BACKEND, '/api/rooms/ab')
  report('非法房间码被拒', r.status === 400 || r.status === 404, `${r.status} ${r.text.slice(0, 60)}`)
}
{
  // 认证路由是否注册取决于实例配没配 PR_DB_DSN：
  // 账号关闭 ⇒ 路由不注册（404）；账号开启 ⇒ 路由在但未带 token（401）。两者都算正常。
  const r = await get(BACKEND, '/api/auth/me')
  report('认证路由状态符合实例配置', r.status === 404 || r.status === 401, `${r.status}（404=账号关闭 / 401=账号开启）`)
}

// ---- ② Vite 开发服务器与代理 ----
if (VITE) {
  {
    const r = await get(VITE, '/')
    report('Vite 页面可开', r.status === 200 && r.text.includes('<div id="app"'), `${r.status} ${r.text.length}B`)
  }
  for (const [name, path, marker] of [
    ['Vite 代理 /api/ice', '/api/ice', 'iceServers'],
    ['Vite 代理 /api/public-rooms', '/api/public-rooms', 'items'],
  ]) {
    const r = await get(VITE, path)
    // 关键判据：代理失败时 Vite 会回退成 index.html（SPA 回退），
    // 那时状态码仍是 200，只有"响应体是不是 JSON"能识破。
    const isHtml = r.text.trimStart().startsWith('<')
    report(name, r.status === 200 && r.text.includes(marker) && !isHtml, `${r.status} ${r.ms}ms ${r.text.slice(0, 70)}`)
  }
}

// ---- ③ 信令链路 ----
{
  const r = await wsUpgrade(`ws://${new URL(BACKEND).host}/ws?roomId=CONNTEST1&clientId=c_probe_direct_01`)
  report('WS 直连后端升级成功', r.opened, `open=${r.opened} code=${r.code}`)
}
if (VITE) {
  const r = await wsUpgrade(`ws://${new URL(VITE).host}/ws?roomId=CONNTEST2&clientId=c_probe_vite_01`)
  report('WS 经 Vite 代理升级成功', r.opened, `open=${r.opened} code=${r.code}`)
}

// ---- ④ Origin 白名单（S-5：同源不隐式放行）----
{
  const u = new URL(BACKEND)
  const port = Number(u.port || 80)
  const cases = [
    [`白名单 Origin ${ALLOWED_ORIGIN} 放行`, ALLOWED_ORIGIN, 'ORIG1', true],
    ['非白名单 Origin 被拒', 'http://evil.example', 'ORIG2', false],
    ['端口不同的同主机 Origin 被拒', `http://${u.hostname}:9999`, 'ORIG3', false],
    ['无 Origin 的游客路径按设计放行', null, 'ORIG4', true],
  ]
  for (const [name, origin, room, want] of cases) {
    const r = await originCase(u.hostname, port, origin, room, 'c_' + room.toLowerCase())
    report(name, r.upgraded === want, `status=${r.status} upgraded=${r.upgraded} ${r.body}`)
  }
}

console.log(`\n结果：PASS=${pass} FAIL=${fail}`)
process.exit(fail === 0 ? 0 : 1)
