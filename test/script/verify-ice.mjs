#!/usr/bin/env node
/**
 * ICE 打洞优化的端到端验收（TURN 已退役 → 本脚本的判据全部改成"TTL 刷新 + IPv6 直连"）。
 *
 * 为什么需要它：这三条优化都不是"配置对不对"的问题，而是**运行期行为**：
 *   - 列表会随服务端每轮探测变化，客户端必须按 TTL 重拉，且**只在列表真的变了**时才动连接；
 *   - ICE restart 只能由发起方做，做错就是两边互相打架；
 *   - "IPv6 直连有没有生效"必须能从选中候选对里看出来，否则这条优化无法验证。
 * 这三件事只存在于真实浏览器的运行期状态里，所以必须起真浏览器读 window.__pr。
 *
 * 用法（两个终端）：
 *   # 1) 自己的测试实例（不要碰正在跑的 8080）：TTL 配短，才能在几十秒内观察到刷新
 *   PR_ADDR=127.0.0.1:8099 PR_ICE_TTL=30s $env:TEMP\pr-ice\projectionroom.exe
 *   # 2) 前端（把 /api 与 /ws 代理到测试实例）
 *   PR_SERVER_URL=http://127.0.0.1:8099 npm --prefix client run dev
 *   # 3) 跑验收
 *   node test/script/verify-ice.mjs --server http://127.0.0.1:8099 --client http://127.0.0.1:5173
 *
 * 判据（末尾打印 判定：PASS/FAIL，任何一条 false 即 FAIL，skip 不影响判定）：
 *   ① /api/ice 与 POST /api/rooms 的响应里都没有 turn:（TURN 彻底退役）
 *   ② 两者都带 ttlSeconds / expiresAt / probe.scores
 *   ③ 服务端探测分数里有多条带 rttMs 的成功 STUN，且 qq.com 为 ok:false 或未被选中
 *   ④ 客户端在到期前重拉（refreshCount 增长，且两次刷新间隔 < ttlSeconds）
 *   ⑤ 本机有全局 IPv6 时，候选分类里 v6Global >= 1（没有则如实 skip，不伪造）
 *   ⑥ selectedPair 能被读到（本地/对端候选类型 + 地址族 + RTT）
 *   ⑦ 列表未变：refreshCount 增长而 changeCount / restartCount 不变（发起方侧）
 *   ⑧ 列表变化：changeCount +1 且发起方 restartCount +1；父节点侧只换配置、不重启
 *   ⑨ 过滤判据：fe80::/10 与 fc00::/7 判为不可跨网，2001::/3 判为可用，.local 保留
 *   ⑩ leaveRoom 之后刷新定时器停止（切房不会残留定时器打旧房间）
 *   ⑪ /api/ice 刷新失败只退避重试，连接与播放不受影响并自动恢复
 *   ⑫ ICE 配置 / IPv6 地址族 / 选中候选对能在现有 DiagnosticsDrawer 里看到
 *
 * ⑧ 的列表变化是**在 HTTP 层注入**的（包一层 fetch）：服务端每轮探测的结果不可控，
 * 而"变化 → setConfiguration → ICE restart"这条链路本身必须确定性可测。
 * 注入只加一条额外 STUN，走的仍然是客户端自己的定时刷新 → 归一化比较 → 变更处理。
 */
import { networkInterfaces } from 'node:os'
import { argOf, findChrome, openTarget, seedAndEnter, sleep, startChrome, waitFor } from './lib/browser.mjs'

const argv = process.argv.slice(2)
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:8099')
const CLIENT_URL = argOf(argv, 'client', 'http://127.0.0.1:5173')
const BASE_PORT = Number(argOf(argv, 'port', '9900'))
/** 注入用的额外 STUN：真实存在、无需解析即可被浏览器接受，只为让"列表变了"。 */
const INJECTED_STUN = argOf(argv, 'inject-stun', 'stun:stun1.l.google.com:19302')
/**
 * 判据⑦（列表未变 → 不重启）用的"钉住"列表。
 *
 * 为什么需要钉住：服务端默认每 60s 重新探测 STUN 并可能轮换下发内容（实测
 * miwifi → chat.bilibili），所以"两次刷新之间列表没变"不能用真实服务端赌 ——
 * 那会让 ⑦ 随探测周期间歇性假失败。用固定列表把窗口变成确定性的。
 * 只影响判据⑦的窗口，⑧ 之前会清掉。
 */
const PINNED_STUN = ['stun:stun.miwifi.com:3478', 'stun:stun.hitv.com:3478']

const checks = []
function record(id, label, pass, detail) {
  const verdict = pass === 'skip' ? 'SKIP' : pass ? 'PASS' : 'FAIL'
  checks.push({ id, label, pass })
  console.log(`判据 ${id} ${verdict}　${label}`)
  if (detail !== undefined) {
    console.log(`        ${detail}`)
  }
}

/**
 * 页面加载前挂钩 fetch：
 *   - `extraUrls`：给 /api/ice 的响应"加一条 STUN"（让列表真的变化）；
 *   - `pinUrls`：把响应里的 iceServers **整体替换**为固定列表，用于制造"列表稳定"
 *     的确定性窗口 —— 服务端每 60s 重新探测打分，即使 TTL 没到，它下发的列表也可能
 *     自己轮换（实测 miwifi → chat.bilibili），所以"列表未变"不能用真实服务端赌；
 *   - `failTimes`：让接下来 N 次 /api/ice 返回 500（验证刷新失败不影响连接）。
 * 非注入分支原样返回，不改变客户端的正常路径。
 */
const HOOK = `(() => {
  const original = window.fetch;
  window.__prIceInject = { extraUrls: [], pinUrls: null, failTimes: 0 };
  window.fetch = function (input, init) {
    const url = typeof input === 'string' ? input : (input && input.url) || '';
    if (url.includes('/api/ice') && window.__prIceInject.failTimes > 0) {
      window.__prIceInject.failTimes -= 1;
      return Promise.resolve(
        new Response('{"error":"injected failure"}', {
          status: 500,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    }
    const promise = original.apply(this, arguments);
    if (!url.includes('/api/ice')) return promise;
    return promise.then(async (resp) => {
      const pin = window.__prIceInject.pinUrls;
      const extra = window.__prIceInject.extraUrls || [];
      const hasPin = Array.isArray(pin) && pin.length > 0;
      if (!resp.ok || (!hasPin && extra.length === 0)) return resp;
      const body = await resp.clone().json();
      if (hasPin) {
        body.iceServers = pin.map((u) => ({ urls: u }));
      } else {
        body.iceServers = [...(body.iceServers || []), ...extra.map((u) => ({ urls: u }))];
      }
      return new Response(JSON.stringify(body), {
        status: resp.status,
        headers: { 'Content-Type': 'application/json' },
      });
    });
  };
})()`

/** 本机是否存在全局（非链路本地/非回环）IPv6：⑤ 的独立基线，不依赖浏览器。 */
function hostHasGlobalIPv6() {
  for (const addrs of Object.values(networkInterfaces())) {
    for (const addr of addrs ?? []) {
      if (addr.family !== 'IPv6') continue
      const value = addr.address.toLowerCase().split('%')[0]
      if (value === '::1' || value.startsWith('fe80:')) continue
      return value
    }
  }
  return ''
}

const flat = (list) => (list ?? []).join(', ')

async function fetchICE() {
  const resp = await fetch(`${SERVER_URL}/api/ice`)
  const text = await resp.text()
  return { status: resp.status, text, body: JSON.parse(text) }
}

/** 服务端第一轮探测是异步的：probedAt=0 时 scores 为空，必须等它跑完（最长 1.5s 一轮）。 */
async function waitForProbe() {
  return waitFor(
    async () => {
      const ice = await fetchICE()
      return ice.body?.probe?.probedAt > 0 && (ice.body?.probe?.scores?.length ?? 0) > 0 ? ice : null
    },
    { label: '服务端第一轮 STUN 探测', timeoutMs: 30000, intervalMs: 1000 },
  )
}

function urlsOf(body) {
  return (body?.iceServers ?? []).flatMap((s) => (typeof s.urls === 'string' ? [s.urls] : s.urls ?? []))
}

/**
 * ①②③ 都在**两条端点**上验：`/api/ice` 与 `POST /api/rooms` 必须同源同形状，
 * 否则"通过分享链接进房的人拿到的载荷"就没人验了（那正是历史上 TURN 不生效的原因）。
 */
function assertPayloads(iceResp, roomResp) {
  const sources = [
    { name: 'GET  /api/ice', body: iceResp.body, text: iceResp.text },
    { name: 'POST /api/rooms', body: roomResp.body, text: roomResp.text },
  ]
  const lines = (fn) => sources.map((s) => `${s.name} → ${fn(s)}`).join('\n        ')

  // ① TURN 彻底退役
  const turnHits = sources.filter((s) => /"turn:|"turns:/i.test(s.text))
  record(
    '①',
    '/api/ice 与 POST /api/rooms 都没有 turn:（TURN 已退役）',
    turnHits.length === 0,
    lines((s) => `${flat(urlsOf(s.body))}${/"turn:|"turns:/i.test(s.text) ? '  ← 命中 turn:' : ''}`),
  )

  // ② ttlSeconds / expiresAt / probe.scores
  const shapeOk = sources.every((s) => {
    const ttl = s.body?.ttlSeconds
    const expiresAt = s.body?.expiresAt
    const drift = typeof expiresAt === 'number' ? Math.abs(expiresAt - (Math.floor(Date.now() / 1000) + ttl)) : 999
    return typeof ttl === 'number' && ttl > 0 && typeof expiresAt === 'number' && drift <= 5 && (s.body?.probe?.scores?.length ?? 0) > 0
  })
  record(
    '②',
    '两条响应都带 ttlSeconds / expiresAt（= now+ttl）/ probe.scores',
    shapeOk,
    lines((s) => {
      const drift = Math.abs(s.body.expiresAt - (Math.floor(Date.now() / 1000) + s.body.ttlSeconds))
      return `ttlSeconds=${s.body.ttlSeconds}s expiresAt=now+${drift}s probe.scores=${s.body.probe.scores.length} 条 interval=${s.body.probe.intervalSeconds}s`
    }),
  )

  // ③ 探测分数：多条带 RTT 的成功 STUN + qq.com 落选
  const scores = iceResp.body?.probe?.scores ?? []
  const okRtt = scores.filter((s) => s.ok && s.rttMs > 0)
  const qq = scores.find((s) => String(s.url).includes('qq.com'))
  record(
    '③',
    '探测分数里有多条带 rttMs 的成功 STUN，且 qq.com 为 ok:false / 未选中',
    okRtt.length >= 2 && !!qq && (qq.ok === false || qq.selected === false),
    `成功且带 RTT 的 STUN ${okRtt.length} 条（${okRtt
      .map((s) => `${s.url} ${s.rttMs}ms${s.selected ? '*' : ''}`)
      .join(' · ')}）\n        qq.com: ok=${qq?.ok} selected=${qq?.selected} rttMs=${qq?.rttMs}` +
      `\n        probe.scores 全量=${scores.length} 条，下发=${flat(urlsOf(iceResp.body))}`,
  )
  return iceResp.body?.ttlSeconds
}

async function main() {
  // ---------- 1) 服务端载荷 ----------
  const ice = await waitForProbe()
  console.log(`/api/ice -> HTTP ${ice.status}`)

  const roomResp = await fetch(`${SERVER_URL}/api/rooms`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  })
  const roomText = await roomResp.text()
  const roomBody = JSON.parse(roomText)
  console.log(`/api/rooms -> HTTP ${roomResp.status} roomId=${roomBody.roomId}`)

  const ttl = assertPayloads(ice, { body: roomBody, text: roomText })

  const ttlMs = (ttl > 0 ? ttl : 300) * 1000
  console.log(`\n本次 TTL=${ttl}s（刷新间隔必须早于它；客户端余量 = max(60s, 0.2×ttl)）\n`)

  // ---------- 2) 真浏览器：主播 + 观众 ----------
  const chrome = await startChrome(findChrome(), BASE_PORT, 'ice-host')
  const host = await openTarget(BASE_PORT, 'ice-host')
  await host.send('Page.addScriptToEvaluateOnNewDocument', { source: HOOK })

  const viewerBrowser = await startChrome(findChrome(), BASE_PORT + 1, 'ice-viewer')
  const viewer = await openTarget(BASE_PORT + 1, 'ice-viewer')
  await viewer.send('Page.addScriptToEvaluateOnNewDocument', { source: HOOK })

  await seedAndEnter(host, CLIENT_URL, roomBody.roomId, 'host', '主播')
  const hostFirst = await host.snapshot()
  console.log(
    `主播进房：iceServers=${flat(hostFirst.ice.serverUrls)} refreshCount=${hostFirst.ice.refreshCount}` +
      ` lastRefreshReason=${hostFirst.ice.lastRefreshReason} ttlSeconds=${hostFirst.ice.ttlSeconds}`,
  )

  // ④ 到期前重拉：以"本地读到的 refreshCount"为起点，等它增长，并量出间隔。
  const hostStart = Date.now()
  const hostNext = await waitFor(
    async () => {
      const s = await host.snapshot()
      return s.ice.refreshCount > hostFirst.ice.refreshCount ? s : null
    },
    { label: '主播侧 TTL 刷新', timeoutMs: ttlMs + 10000, intervalMs: 500 },
  ).catch(() => null)
  const hostElapsed = Date.now() - hostStart
  record(
    '④',
    '客户端在到期前重拉（refreshCount 增长且间隔 < TTL）',
    !!hostNext && hostElapsed < ttlMs,
    hostNext
      ? `refreshCount ${hostFirst.ice.refreshCount} → ${hostNext.ice.refreshCount}，间隔 ${hostElapsed}ms < TTL ${ttlMs}ms` +
        ` · 上次原因=${hostNext.ice.lastRefreshReason} 余量=${hostNext.ice.refreshMarginSec}s`
      : `超时没有观察到重拉（refreshCount 停在 ${hostFirst.ice.refreshCount}）`,
  )

  await seedAndEnter(viewer, CLIENT_URL, roomBody.roomId, 'viewer', '观众')
  const viewerFirst = await viewer.snapshot()
  console.log(
    `\n观众进房：primary=${viewerFirst.topology.primaryId || '（未分配）'} refreshCount=${viewerFirst.ice.refreshCount}` +
      ` initiatedPeers=${viewerFirst.ice.initiatedPeers}`,
  )

  // ⑥ P2P 真正建起来才会出现"选中候选对"。分两步等：先通道，再统计（统计 5s 一轮）。
  const bound = await waitFor(
    async () => {
      const s = await viewer.snapshot()
      return s.p2p.openChannels >= 1 ? s : null
    },
    { label: '观众 ↔ 主播 DataChannel 建立', timeoutMs: 40000, intervalMs: 500 },
  ).catch(() => null)
  const pairReady = bound
    ? await waitFor(
        async () => {
          const s = await viewer.snapshot()
          return s.selectedPair ? s : null
        },
        { label: '读到选中候选对（getStats 每 5s 一轮）', timeoutMs: 15000, intervalMs: 500 },
      ).catch(() => null)
    : null
  const viewerBound = pairReady ?? (await viewer.snapshot())
  const pair = viewerBound.selectedPair
  record(
    '⑥',
    'selectedPair 可读（本地/对端类型 + 地址族 + RTT）',
    !!pair,
    pair
      ? `openChannels=${viewerBound.p2p.openChannels} · ${pair.family}/${pair.remoteFamily}` +
        ` · ${pair.localType} ${pair.localAddress || '（mDNS 隐藏）'}` +
        ` ↔ ${pair.remoteType} ${pair.remoteAddress || '（mDNS 隐藏）'} · ${pair.protocol} · rtt ${pair.rttMs}ms` +
        (viewerBound.ipv6.mdnsHidden ? '\n        注：host 候选地址被 Chrome 的 mDNS 混淆隐藏，因此 family 只能是 unknown；' +
          '判定"有没有可跨网的 IPv6"要看 ⑤ 的 srflx/采样证据' : '')
      : `未读到候选对（openChannels=${viewerBound.p2p.openChannels}，通道 ${bound ? '已建立' : '未建立'}）`,
  )

  // ⑤ 全局 IPv6：本机有没有由 node 独立判定，客户端分类由 ipv6.families 给出。
  //    等采样跑完（8s 一轮）：真实连接可能靠 mDNS host 候选秒连，Chrome 会提前结束收集。
  const samplerDone = await waitFor(
    async () => {
      const s = await viewer.snapshot()
      return s.ipv6.sampler.runs >= 1 ? s : null
    },
    { label: '客户端本地候选采样完成', timeoutMs: 25000, intervalMs: 500 },
  ).catch(() => null)
  const hostV6 = hostHasGlobalIPv6()
  const hostDiag = await host.snapshot()
  const viewerDiag = samplerDone ?? (await viewer.snapshot())
  const v6Global = Math.max(hostDiag.ipv6.families.v6Global, viewerDiag.ipv6.families.v6Global)
  const v6Detail =
    `本机全局 IPv6（node 判定）=${hostV6 || '无'}\n` +
    `        客户端分类 主播 v6Global=${hostDiag.ipv6.families.v6Global}` +
    ` 观众 v6Global=${viewerDiag.ipv6.families.v6Global}（链路本地 ${viewerDiag.ipv6.families.v6LinkLocal}` +
    ` · 已挡下不可跨网候选 ${viewerDiag.ipv6.filtered} 条 · 采样 ${viewerDiag.ipv6.sampler.runs} 次：` +
    `全局 ${viewerDiag.ipv6.sampler.families.v6Global} / IPv4 ${viewerDiag.ipv6.sampler.families.v4}）\n` +
    `        真实连接候选：${flat(viewerDiag.ipv6.samples)}\n` +
    `        采样候选：${flat(viewerDiag.ipv6.sampler.samples)}\n` +
    `        选中候选对族：${pair ? `${pair.family} → ${pair.remoteFamily}` : '（无）'}`
  record(
    '⑤',
    '本机有全局 IPv6 时候选分类里能看到 v6Global >= 1',
    hostV6 ? v6Global >= 1 : 'skip',
    v6Detail + (hostV6 ? '' : '　（本机没有全局 IPv6：本判据 skip，不伪造）'),
  )

  // ⑫ 这些状态必须能在**现有** DiagnosticsDrawer 里看到（不能只存在于 window.__pr）。
  const diagText = JSON.parse(
    await viewer.evaluate(`JSON.stringify((() => {
      const drawer = document.querySelector('[data-testid="diagnostics-drawer"]')
      if (drawer) drawer.open = true
      const pick = (id) => {
        const el = document.querySelector('[data-testid="' + id + '"]')
        return el ? el.textContent.trim().replace(/\\s+/g, ' ') : ''
      }
      return { ice: pick('diag-ice'), ipv6: pick('diag-ipv6'), pair: pick('diag-selected-pair') }
    })())`),
  )
  record(
    '⑫',
    'DiagnosticsDrawer 里能看到 ICE 配置 / IPv6 直连 / 选中候选对',
    diagText.ice.length > 0 && diagText.ipv6.length > 0 && diagText.pair.length > 0 && (!pair || !diagText.pair.includes('尚未')),
    `ICE 配置: ${diagText.ice}\n        IPv6 直连: ${diagText.ipv6}\n        选中候选对: ${diagText.pair}`,
  )

  // ---------- 3) 列表未变 → 不重启 ----------
  // 先把列表钉成固定两条（见 PINNED_STUN）：钉住动作本身会引发一次"真变化 + 重启"，
  // 那是预期的，所以第一轮刷新只用来消化它，之后取基线再量第二轮 —— 只有第二轮才
  // 是"列表相同"的确定性窗口。
  const pinScript = `window.__prIceInject.pinUrls = ${JSON.stringify(PINNED_STUN)}; 'ok'`
  const viewerPrePin = await viewer.snapshot()
  await viewer.evaluate(pinScript)
  await host.evaluate(pinScript)
  await waitFor(
    async () => {
      const s = await viewer.snapshot()
      return s.ice.refreshCount > viewerPrePin.ice.refreshCount ? s : null
    },
    { label: '钉住列表后的第一次刷新（消化钉住引发的变化）', timeoutMs: ttlMs + 15000, intervalMs: 500 },
  ).catch(() => null)

  const viewerBefore = await viewer.snapshot()
  const unchangedStart = Date.now()
  const viewerRefreshed = await waitFor(
    async () => {
      const s = await viewer.snapshot()
      return s.ice.refreshCount > viewerBefore.ice.refreshCount ? s : null
    },
    { label: '观众侧 TTL 刷新（列表已钉住，必然未变）', timeoutMs: ttlMs + 15000, intervalMs: 500 },
  ).catch(() => null)
  const unchangedElapsed = Date.now() - unchangedStart
  record(
    '⑦',
    '列表未变（已钉住）：refreshCount 增长而 changeCount / restartCount 不变',
    !!viewerRefreshed &&
      viewerRefreshed.ice.changeCount === viewerBefore.ice.changeCount &&
      viewerRefreshed.ice.restartCount === viewerBefore.ice.restartCount,
    viewerRefreshed
      ? `refreshCount ${viewerBefore.ice.refreshCount} → ${viewerRefreshed.ice.refreshCount}（+${
          viewerRefreshed.ice.refreshCount - viewerBefore.ice.refreshCount
        }，${unchangedElapsed}ms）· changeCount ${viewerBefore.ice.changeCount} → ${viewerRefreshed.ice.changeCount}` +
        ` · restartCount ${viewerBefore.ice.restartCount} → ${viewerRefreshed.ice.restartCount}` +
        ` · 原因=${viewerRefreshed.ice.lastRefreshReason}` +
        ` · 钉住列表=${PINNED_STUN.join(', ')}`
      : `超时没有观察到刷新（refreshCount 停在 ${viewerBefore.ice.refreshCount}）`,
  )

  // 解除钉住：⑧ 要靠 extraUrls 制造"真变化"，钉住会让它失效。
  const unpinScript = `window.__prIceInject.pinUrls = null; 'ok'`
  await viewer.evaluate(unpinScript)
  await host.evaluate(unpinScript)

  // ---------- 4) 列表变化 → 发起方重启，父节点只换配置 ----------
  await viewer.evaluate(`window.__prIceInject.extraUrls = [${JSON.stringify(INJECTED_STUN)}]; 'ok'`)
  await host.evaluate(`window.__prIceInject.extraUrls = [${JSON.stringify(INJECTED_STUN)}]; 'ok'`)
  console.log(`\n已在 HTTP 层注入额外 STUN：${INJECTED_STUN}（等客户端下一次刷新）`)

  const changedViewer = await waitFor(
    async () => {
      const s = await viewer.snapshot()
      return s.ice.changeCount > viewerBefore.ice.changeCount ? s : null
    },
    { label: '观众侧观察到列表变化', timeoutMs: ttlMs + 15000, intervalMs: 500 },
  ).catch(() => null)
  const afterRestart = await waitFor(
    async () => {
      const s = await viewer.snapshot()
      return s.ice.restartCount > viewerBefore.ice.restartCount ? s : null
    },
    { label: '发起方 ICE restart', timeoutMs: 10000, intervalMs: 300 },
  ).catch(() => null)
  const viewerFinal = afterRestart ?? (await viewer.snapshot())
  record(
    '⑧',
    '列表变化：changeCount +1 且发起方 restartCount +1',
    !!changedViewer && viewerFinal.ice.restartCount > viewerBefore.ice.restartCount,
    `changeCount ${viewerBefore.ice.changeCount} → ${viewerFinal.ice.changeCount}` +
      ` · restartCount ${viewerBefore.ice.restartCount} → ${viewerFinal.ice.restartCount}` +
      ` · 发起方连接 ${viewerFinal.ice.initiatedPeers} 条 · 日志=${flat(viewerFinal.ice.restartLog)}\n` +
      `        iceServers=${flat(viewerFinal.ice.serverUrls)}`,
  )

  // 父节点侧：它必须应用新列表（changeCount 同样增长），但**不主动 restart**。
  const hostChanged = await waitFor(
    async () => {
      const s = await host.snapshot()
      return s.ice.changeCount > hostDiag.ice.changeCount ? s : null
    },
    { label: '父节点侧观察到列表变化', timeoutMs: ttlMs + 15000, intervalMs: 500 },
  ).catch(() => null)
  const hostAfterChange = hostChanged ?? (await host.snapshot())
  record(
    '⑧b',
    '父节点侧只换配置、不主动 ICE restart',
    !!hostChanged && hostAfterChange.ice.restartCount === 0,
    `changeCount ${hostDiag.ice.changeCount} → ${hostAfterChange.ice.changeCount}` +
      ` · restartCount=${hostAfterChange.ice.restartCount}（应为 0）· 发起方连接 ${hostAfterChange.ice.initiatedPeers} 条`,
  )

  // restart 之后连接必须还活着（否则"优化"变成了"打断"）。
  const alive = await waitFor(
    async () => {
      const s = await viewer.snapshot()
      return s.p2p.openChannels >= 1 ? s : null
    },
    { label: 'ICE restart 后通道仍可用', timeoutMs: 15000, intervalMs: 500 },
  ).catch(() => null)
  record(
    '⑧c',
    'ICE restart 后 DataChannel 仍然可用（restart 不影响正在跑的播放）',
    !!alive,
    alive
      ? `openChannels=${alive.p2p.openChannels} peerCount=${alive.p2p.peerCount} ` +
        `selectedPair=${alive.selectedPair ? `${alive.selectedPair.family} rtt ${alive.selectedPair.rttMs}ms` : '（无）'}`
      : '通道没有恢复',
  )

  // ---------- 5) 过滤判据（纯函数，确定性） ----------
  const classify = async (address) =>
    JSON.parse(
      await viewer.evaluate(
        `JSON.stringify({ f: window.__pr.iceClassify(${JSON.stringify(address)}), ok: window.__pr.iceCrossNetworkUsable(${JSON.stringify(address)}) })`,
      ),
    )
  const samples = {
    'fe80::1': 'v6-link-local',
    'fd00::1': 'v6-ula',
    '2001:da8:6004:6003::3:4d41': 'v6-global',
    '240e:398:7019:2::9a90': 'v6-global',
    '192.168.1.5': 'v4',
    '127.0.0.1': 'v4',
    '2f5a1c7d-1111.local': 'unknown',
    '': 'unknown',
  }
  const classified = {}
  let filterOk = true
  for (const [address, want] of Object.entries(samples)) {
    const got = await classify(address)
    classified[address || '(空)'] = `${got.f}${got.ok ? '+发' : '+不发'}`
    if (got.f !== want) filterOk = false
  }
  // 不可跨网必须"不发"，可跨网/判不了必须"发"（判不了就丢掉会打死同局域网直连）。
  filterOk =
    filterOk &&
    (await classify('fe80::1')).ok === false &&
    (await classify('fd00::1')).ok === false &&
    (await classify('2001:da8:6004:6003::3:4d41')).ok === true &&
    (await classify('2f5a1c7d-1111.local')).ok === true
  record(
    '⑨',
    '过滤判据：链路本地/ULA 不发，全局 IPv6 与不可判定（mDNS）保留',
    filterOk,
    Object.entries(classified)
      .map(([a, v]) => `${a}=${v}`)
      .join(' · '),
  )

  // ---------- 6) 刷新失败不能影响正在跑的连接 ----------
  const beforeFail = await viewer.snapshot()
  await viewer.evaluate(`window.__prIceInject.failTimes = 2; 'ok'`)
  const failureSeen = await waitFor(
    async () => {
      const s = await viewer.snapshot()
      return s.ice.failedRefreshCount > beforeFail.ice.failedRefreshCount ? s : null
    },
    { label: '观察刷新失败（注入 500）', timeoutMs: 20000, intervalMs: 500 },
  ).catch(() => null)
  const failWindow = failureSeen ?? (await viewer.snapshot())
  await viewer.evaluate(`window.__prIceInject.failTimes = 0; 'ok'`)
  const recovered = failureSeen
    ? await waitFor(
        async () => {
          const s = await viewer.snapshot()
          return s.ice.refreshCount > failWindow.ice.refreshCount ? s : null
        },
        { label: '退避后恢复刷新', timeoutMs: 20000, intervalMs: 500 },
      ).catch(() => null)
    : null
  record(
    '⑪',
    '刷新失败只退避重试：连接与播放不受影响，随后自动恢复',
    !!failureSeen && !!recovered && failWindow.p2p.openChannels >= 1,
    `failedRefreshCount ${beforeFail.ice.failedRefreshCount} → ${failWindow.ice.failedRefreshCount}` +
      ` · 失败期间 openChannels=${failWindow.p2p.openChannels} peerCount=${failWindow.p2p.peerCount}` +
      ` · lastRefreshReason=${failWindow.ice.lastRefreshReason} lastError=${failWindow.ice.lastError}` +
      (recovered ? ` · 恢复后 refreshCount=${recovered.ice.refreshCount}（原因=${recovered.ice.lastRefreshReason}）` : ' · 未观察到恢复'),
  )

  // ---------- 7) 离开房间 → 刷新定时器必须停 ----------
  // 走的是真实路径：点房间页的"离开房间"按钮（RoomView 的 leave() → store.leaveRoom()），
  // 不刷新页面，因此同一个 JS 上下文里的计数不会重置 —— 泄漏的定时器一定看得见。
  const beforeLeave = await viewer.snapshot()
  await viewer.evaluate(`(() => {
    const btn = [...document.querySelectorAll('button')].find((b) => b.textContent.trim() === '离开房间')
    if (!btn) throw new Error('找不到「离开房间」按钮')
    btn.click()
    return 'ok'
  })()`)
  await sleep(14000)
  const afterLeave = await viewer.snapshot()
  // 若定时器没被清掉，最迟一个看门狗周期（5s）+ 到期点（本次约 10s）之内必然再拉一次。
  record(
    '⑩',
    'leaveRoom 后刷新定时器停止（不再打旧房间的 /api/ice）',
    afterLeave.ice.refreshCount === beforeLeave.ice.refreshCount,
    `离开前 refreshCount=${beforeLeave.ice.refreshCount}，等待 14s 后=${afterLeave.ice.refreshCount}` +
      `（TTL=${beforeLeave.ice.ttlSeconds}s，客户端排定的刷新间隔=${beforeLeave.ice.nextRefreshAt - beforeLeave.ice.lastRefreshAt}ms，` +
      `本轮实测 ${unchangedElapsed}ms）· joined=${afterLeave.joined}`,
  )

  // ---------- 汇总 ----------
  const failed = checks.filter((c) => c.pass === false)
  const skipped = checks.filter((c) => c.pass === 'skip')
  console.log(
    `\n判定依据：${checks.length} 条判据，通过 ${checks.length - failed.length - skipped.length}，失败 ${failed.length}，跳过 ${skipped.length}`,
  )
  if (failed.length > 0) {
    console.log(`失败判据：${failed.map((c) => `${c.id} ${c.label}`).join(' | ')}`)
  }
  console.log(`判定：${failed.length === 0 ? 'PASS' : 'FAIL'}`)
  process.exitCode = failed.length === 0 ? 0 : 1

  host.close()
  viewer.close()
  chrome.close()
  viewerBrowser.close()
}

try {
  await main()
} catch (err) {
  console.error(`ICE 验收失败：${err.message}`)
  process.exitCode = 1
} finally {
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
