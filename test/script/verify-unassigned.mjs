#!/usr/bin/env node
/**
 * 「未安置」状态的验收（T4 补 T1 的缺口）。
 *
 * 背景（先说清服务器现在到底给不给这个状态）：
 *   internal/usecase/room.go 的准入闸门是 `len(members) + plan.GateSlots`，
 *   而 GateSlots 只认**实测**容量。树在深度上限处铺满后 GateSlots=0，
 *   后来者直接拿到 ROOM_FULL（见 internal/usecase/manager_test.go 里记录的那次行为变更）。
 *   也就是说"进得来但没有父节点"在**当前服务端**上几乎不会自然发生 ——
 *   客户端这段文案是**防御性**的，用于竞态（指标变化触发重排后放不下）与旧服务端。
 *
 * 因此这里分两段验证：
 *   A. 真机：深度上限压到 1 + 主播上报极小上行 → 后续观众应当被明确拒绝（ROOM_FULL），
 *      而不是"进来了却没上游"。把实际现象如实记下来。
 *   B. 注入：给一个已安置的观众注入"空分配"（primaryId/backupIds 全空）→
 *      诊断抽屉必须显示"未安置：当前房间已满 / 没有可用父节点"，恢复分配后回到"主父 …"。
 *      注入的是**分配结果**这一输入，界面文案与判定链路完全是产品代码。
 *
 * 用法：PR_MAX_DEPTH=1 起一份临时服务端，然后
 *   node test/script/verify-unassigned.mjs --media <分片目录> --client http://127.0.0.1:5173 --server http://127.0.0.1:18080
 */
import {
  argOf,
  createRoom,
  findChrome,
  loadMedia,
  openTarget,
  seedAndEnter,
  sleep,
  startChrome,
  waitFor,
} from './lib/browser.mjs'

const argv = process.argv.slice(2)
const CLIENT_URL = argOf(argv, 'client', 'http://127.0.0.1:5173')
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:18080')
const MEDIA_DIR = argOf(argv, 'media', 'test/resource/short_video/cut')
const BASE_PORT = Number(argOf(argv, 'port', '9440'))
/**
 * 主播上报的上行：必须让 `K0 = floor(上行 × 0.8 ÷ 素材码率)` **恰好为 1**，
 * 这样"第一个观众放得下、第二个放不下"是确定性的，不用赌"指标上报前的窗口"。
 *
 * 默认 8 Mbps 对应默认素材 `test/resource/short_video/cut`（≈4.55 Mbps）：
 * floor(8 × 0.8 ÷ 4.55) = 1 ✓。**换素材请相应调整**，否则第一个观众会被直接拒掉
 * （那本身是正确行为，但脚本的段 B 需要一个"已进房的观众"来注入空分配）。
 */
const HOST_UPLINK = Number(argOf(argv, 'host-uplink', '8000000'))

const checks = []
function record(name, pass, detail) {
  checks.push({ name, pass })
  console.log(`${pass ? '  ok  ' : '  FAIL'} ${name}\n        ${detail}`)
}

async function drawerText(cdp) {
  return JSON.parse(
    await cdp.evaluate(`JSON.stringify((() => {
      const drawer = document.querySelector('[data-testid="diagnostics-drawer"]')
      if (drawer) drawer.open = true
      const pick = (id) => {
        const el = document.querySelector('[data-testid="' + id + '"]')
        return el ? el.textContent.trim().replace(/\\s+/g, ' ') : ''
      }
      return { upstream: pick('diag-upstream'), schedule: pick('diag-health-schedule') }
    })())`),
  )
}

async function main() {
  const chromePath = findChrome()
  const browsers = []
  const nodes = []
  for (let i = 0; i < 3; i += 1) {
    browsers.push(await startChrome(chromePath, BASE_PORT + i, `u${i}`))
    nodes.push(await openTarget(BASE_PORT + i, `u${i}`))
  }
  const [host, v1, v2] = nodes

  const roomId = await createRoom(SERVER_URL)
  console.log(`房间 ${roomId}（服务端深度上限应为 1）`)
  await seedAndEnter(host, CLIENT_URL, roomId, 'host', '主播')
  const mediaServerRef = { server: null }
  const media = await loadMedia(host, MEDIA_DIR, mediaServerRef)
  console.log(`媒体：${media.segmentCount} 段 / ${media.mimeType}`)
  // 先上报极小上行：K0=1 → 只放得下一个直连子节点；配合深度上限 1，第二个观众无处可放。
  await host.evaluate(`window.__pr.reportMetrics(${HOST_UPLINK}, 15)`)
  await host.evaluate('window.__pr.store.play()')
  await sleep(2000)

  // 甲：预期能进房（K0=1）。但不赌——进不去就如实记下原因，段 B 相应降级为跳过。
  let v1Join = { joined: true, err: '' }
  try {
    await seedAndEnter(v1, CLIENT_URL, roomId, 'viewer', '观众甲')
  } catch (err) {
    // 用既有判据把它记下来（见下），这里只捕获，不让整个脚本崩在 setup 上。
    v1Join = { joined: false, err: err.message }
  }
  console.log(`\n[A] 观众甲进房结果：${v1Join.joined ? 'joined' : `join-failed: ${v1Join.err}`}`)
  await sleep(3000)
  const v2Join = await seedAndEnter(v2, CLIENT_URL, roomId, 'viewer', '观众乙')
    .then(() => 'joined')
    .catch((err) => `join-failed: ${err.message}`)
  console.log(`[A] 观众乙进房结果：${v2Join}`)
  await sleep(5000)

  const s1 = v1Join.joined ? await v1.snapshot() : null
  const d1 = s1 ? await drawerText(v1) : { upstream: '（观众甲未进房，无上游链路可读）', schedule: '' }
  console.log(`观众甲: 深度=${s1?.topology.depth ?? '-'} 主父=${s1?.topology.primaryId ? '有' : '（无）'} 候选父=${s1?.health.parents.length ?? '-'}`)
  console.log(`观众甲 上游链路: ${d1.upstream}`)

  let v2State = '（未进房）'
  let v2Drawer = { upstream: '', schedule: '' }
  let v2Snap = null
  try {
    v2Snap = await v2.snapshot()
    v2Drawer = await drawerText(v2)
    v2State =
      `joined=${v2Snap.joined} 候选父=${v2Snap.health.parents.length} ` +
      `unassigned=${v2Snap.health.unassigned} 错误="${v2Snap.errors.last}"`
    console.log(`观众乙: ${v2State}`)
    console.log(`观众乙 上游链路: ${v2Drawer.upstream}`)
  } catch {
    // 进房失败时页面可能没有调试钩子
  }

  // 甲：K0=1 时应能进房并有主父；若它被拒，说明上行/码率算下来 K0=0 —— 那也是正确行为，
  // 只是段 B 需要一个"已进房的观众"，所以这里把它记成一条明确判据而不是崩在 setup。
  record(
    '甲（K0=1 时的第一个观众）：进房且被安置、照常在播',
    v1Join.joined && s1 !== null && s1.health.parents.length > 0 && d1.upstream.includes('主父'),
    v1Join.joined
      ? `候选父=${s1?.health.parents.length} 播放头=${s1?.video.currentTime.toFixed(2)}s paused=${s1?.video.paused} · ${d1.upstream}`
      : `被拒绝（说明本报上行 ÷ 素材码率算出 K0=0，请按素材调 --host-uplink）：${v1Join.err}`,
  )
  record(
    '[A] 甲没有被服务端判成未安置（不是"进来了却没上游"）',
    v1Join.joined ? s1 !== null && s1.health.unassigned === false : true,
    v1Join.joined
      ? `unassigned=${s1?.health.unassigned} 文案="${s1?.health.unassignedText}"`
      : '（甲未进房，此条不适用）',
  )
  // 如实记录服务端的实际行为：进得来但没父（旧行为）／直接被拒（当前行为）
  const rejected =
    v2Snap !== null &&
    v2Snap.joined === false &&
    (v2Snap.errors.last.includes('房间已满') || v2Join.startsWith('join-failed'))
  const admittedNoParent = v2Snap !== null && v2Snap.joined === true && v2Snap.health.unassigned === true
  record(
    '[A] 第 3 个节点：要么被明确拒绝（ROOM_FULL），要么进房并显示未安置 —— 不允许"静默无上游"',
    rejected || admittedNoParent,
    rejected
      ? `服务端按准入闸门明确拒绝：错误="${v2Snap.errors.last}"（GateSlots 只认实测容量，见 room.go:217）`
      : admittedNoParent
        ? `进房且显示未安置：${v2Drawer.upstream}`
        : `joined=${v2Snap?.joined} 错误="${v2Snap?.errors.last}" 候选父=${v2Snap?.health.parents.length}`,
  )

  // ---------- B. 注入"空分配"，验证未安置文案这条分支 ----------
  console.log('\n[B] 给甲注入空分配（primaryId/backupIds 全空），验证未安置文案')
  if (!v1Join.joined) {
    console.log('\n  skip  [B] 观众甲没进房（原因见上），注入验证需要已进房的观众 —— 本条不适用')
  } else {
    await v1.evaluate(`(() => {
    const store = window.__pr.store
    window.__prSavedAssignment = JSON.parse(JSON.stringify(store.topologyAssignment))
    store.topologyAssignment = { mode: 'chain', depth: 0, primaryId: '', backupIds: [], children: [], distributorId: '', reason: '验收注入：服务端未分配父节点' }
    return 'ok'
  })()`)
    await sleep(600)
    const injected = await v1.snapshot()
    const injectedDrawer = await drawerText(v1)
    console.log(`  候选父=${injected.health.parents.length} unassigned=${injected.health.unassigned}`)
    console.log(`  上游链路: ${injectedDrawer.upstream}`)
    record(
      '空分配下诊断抽屉显示"未安置"且指出原因（不是静默空白）',
      injected.health.unassigned === true &&
        injected.health.parents.length === 0 &&
        injectedDrawer.upstream.includes('未安置') &&
        (injectedDrawer.upstream.includes('没有可用父节点') || injectedDrawer.upstream.includes('房间已满')),
      `${injectedDrawer.upstream} · 快照 text="${injected.health.unassignedText}"`,
    )
  }

  // 关键：主播不受这条文案影响（它是源，没有上游）
  const hostSnap = await host.snapshot()
  const hostDrawer = await drawerText(host)
  record(
    '主播不会被误判成未安置',
    hostSnap.health.unassigned === false && !hostDrawer.upstream.includes('未安置'),
    `主播 上游链路: ${hostDrawer.upstream}`,
  )

  // 恢复真实分配 → 文案回到"主父 …"
  if (v1Join.joined) {
    await v1.evaluate(`(() => {
    window.__pr.store.topologyAssignment = window.__prSavedAssignment
    return 'ok'
  })()`)
    await sleep(600)
    const restored = await drawerText(v1)
    const restoredSnap = await v1.snapshot()
    console.log(`  恢复后 上游链路: ${restored.upstream}`)
    record(
      '恢复真实分配后文案回到"主父 …"（未安置提示可撤销）',
      restoredSnap.health.parents.length > 0 &&
        restored.upstream.includes('主父') &&
        !restored.upstream.includes('未安置'),
      restored.upstream,
    )
  }

  const failed = checks.filter((c) => !c.pass).length
  console.log(`\n=== 判定：${failed === 0 ? 'PASS' : `FAIL（${failed} 项）`} ===`)
  process.exitCode = failed === 0 ? 0 : 1

  mediaServerRef.server?.close()
  for (const b of browsers) b.close()
}

try {
  await main()
} catch (err) {
  console.error(`验收失败：${err.message}`)
  process.exitCode = 1
} finally {
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
