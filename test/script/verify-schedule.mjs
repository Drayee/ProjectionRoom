#!/usr/bin/env node
/**
 * 调度算术单测：直接 import client/src/utils/serveSchedule.ts（Node 24 的类型擦除）。
 *
 * 为什么是 node 脚本而不是 vitest：本仓库前端没有测试框架（client/package.json 里
 * 只有 vue-tsc + vite），为了三个纯函数引入一整套 vitest 依赖不划算；
 * 这三个函数是**纯的**，Node 原生 import 就能覆盖，且和验收脚本同一条命令风格。
 *
 * 覆盖：
 *   · 发送队列排序（T2-1）：紧迫度、更急在前、"已播过的排最后"、取不到播放头退化为升序、FIFO 稳定性；
 *   · 在途上限推导（T2-2）：未测量回落 4、上下限钳制、公式取值；
 *   · 请求超时（T3-1）：RTT 未测得用 800ms、否则 max(500, 3×RTT)。
 *
 * 用法：node test/script/verify-schedule.mjs
 */
import {
  MAX_FETCH_FAILOVER_HOPS,
  deriveInflightLimit,
  deriveRequestTimeoutMs,
  orderServeTasks,
  pickServeTaskIndex,
  servePriority,
} from '../../client/src/utils/serveSchedule.ts'

let failed = 0
let passed = 0

function check(name, actual, expected) {
  const a = JSON.stringify(actual)
  const e = JSON.stringify(expected)
  if (a === e) {
    passed += 1
    console.log(`  ok   ${name} → ${a}`)
    return
  }
  failed += 1
  console.log(`  FAIL ${name}\n       期望 ${e}\n       实际 ${a}`)
}

/** 只保留可比较的部分，断言输出好读。 */
const idx = (list) => list.map((t) => t.index)

console.log('== 发送队列紧迫度排序（T2-1）==')

// 1) 播放头在 10：未播到的按"离 10 的距离"升序，已播过（9、7）整组排在后面
{
  const tasks = [
    { index: 14, seq: 1 },
    { index: 12, seq: 2 },
    { index: 9, seq: 3 },
    { index: 11, seq: 4 },
    { index: 10, seq: 5 },
    { index: 7, seq: 6 },
  ]
  check('播放头 10：正向按距离升序，已播过排最后', idx(orderServeTasks(tasks, 10)), [10, 11, 12, 14, 9, 7])
}

// 2) 同一个分片被两个子节点同时请求（键完全相同）→ 按入队序先到先发（FIFO 稳定性）
{
  const tasks = [
    { index: 12, seq: 2, peerId: 'B' },
    { index: 12, seq: 1, peerId: 'A' },
  ]
  check('同键按入队序（FIFO）', orderServeTasks(tasks, 10).map((t) => t.peerId), ['A', 'B'])
  const noSeq = [
    { index: 12, peerId: 'B' },
    { index: 12, peerId: 'A' },
  ]
  check('缺 seq 时保持原顺序（稳定排序）', orderServeTasks(noSeq, 10).map((t) => t.peerId), ['B', 'A'])
}

// 3) 已经播过的（index < 播放头）整组排最后，组内"离播放头近的"优先
{
  const tasks = [
    { index: 3, seq: 1 },
    { index: 7, seq: 2 },
    { index: 11, seq: 3 },
    { index: 12, seq: 4 },
  ]
  check('已播过的排最后（组内近者优先）', idx(orderServeTasks(tasks, 10)), [11, 12, 7, 3])
  check('已播过的优先级键远大于正向', servePriority(9, 10) > servePriority(11, 10), true)
}

// 4) 取不到播放头（null）→ 退化为按序号升序
{
  const tasks = [
    { index: 14, seq: 1 },
    { index: 3, seq: 2 },
    { index: 9, seq: 3 },
    { index: 1, seq: 4 },
  ]
  check('播放头未知：退化为序号升序', idx(orderServeTasks(tasks, null)), [1, 3, 9, 14])
  check('播放头 null 与 NaN 同义', idx(orderServeTasks(tasks, Number.NaN)), [1, 3, 9, 14])
}

// 5) 播放头正好落在某片开头：它自己是最急的（距离 0），不是"已播过"
{
  const tasks = [
    { index: 9, seq: 1 },
    { index: 10, seq: 2 },
  ]
  check('播放头所在分片最急', idx(orderServeTasks(tasks, 10)), [10, 9])
}

// 6) pickServeTaskIndex 必须与排序结果一致，并且**不改动**队列
{
  const tasks = [
    { index: 20, seq: 1 },
    { index: 8, seq: 2 },
    { index: 9, seq: 3 },
  ]
  const snapshot = JSON.stringify(tasks)
  const pick = pickServeTaskIndex(tasks, 9)
  check('选优下标 == 排序后的第一条', tasks[pick].index, orderServeTasks(tasks, 9)[0].index)
  check('选优不改动队列', JSON.stringify(tasks), snapshot)
  check('空队列返回 -1', pickServeTaskIndex([], 9), -1)
}

// 7) 真机场景：起播后播放头在 40，只缺 41（紧要）与 55（远）两片 —— 41 必须先发
{
  const tasks = [
    { index: 55, seq: 1 },
    { index: 41, seq: 2 },
  ]
  check('紧迫分片先发（不再按到达顺序）', idx(orderServeTasks(tasks, 40)), [41, 55])
}

console.log('\n== 在途上限（T2-2）==')
check('未测量（速率 0）→ 回落 4', deriveInflightLimit(0, 552_000), 4)
check('未测量（分片大小 0）→ 回落 4', deriveInflightLimit(5_000_000, 0), 4)
check('速率很低 → 钳到下限 2', deriveInflightLimit(200_000, 552_000), 2)
check('速率极高 → 钳到上限 8', deriveInflightLimit(100_000_000, 552_000), 8)
// ceil(4e6 × 0.4 / 552000) = ceil(2.898…) = 3
check('公式取值（4 Mbps / 552 KiB 片）', deriveInflightLimit(4_000_000, 552_000), 3)
// ceil(8e6 × 0.4 / 1_000_000) = ceil(3.2) = 4 —— 恰好等于改动前的固定值
check('公式取值（8 Mbps / 1 MB 片）= 改动前的 4', deriveInflightLimit(8_000_000, 1_000_000), 4)

console.log('\n== 取数请求超时（T3-1）==')
check('RTT 未测得 → 800ms 兜底', deriveRequestTimeoutMs(0), 800)
check('RTT 40ms → 500ms 下限生效（3×40=120）', deriveRequestTimeoutMs(40), 500)
check('RTT 200ms → 600ms', deriveRequestTimeoutMs(200), 600)
check('RTT 900ms（跨运营商）→ 2700ms', deriveRequestTimeoutMs(900), 2700)
check('改动前固定值 3000ms 的对照：RTT 10ms 时只等 500ms', deriveRequestTimeoutMs(10) < 3000, true)
// 超时不只看 RTT：RTT 是控制报文往返，不含分片传输时间。跨运营商慢边上
// 「3×RTT」可能小于「传完这一片要多久」，只按 RTT 推导会必然误判超时（代价是重复请求、白烧上行）。
check('慢边：RTT 40ms 但预计传 900ms → 取 2×900=1800ms', deriveRequestTimeoutMs(40, { expectedDeliveryMs: 900 }), 1800)
check('慢边 + 高 RTT：取两者较大值（3×300=900 与 2×900=1800 取 1800）', deriveRequestTimeoutMs(300, { expectedDeliveryMs: 900 }), 1800)
check('预计传输极小则仍由 RTT 决定', deriveRequestTimeoutMs(900, { expectedDeliveryMs: 50 }), 2700)
check('上限 3s：预计传 60s 也不会无限等（坏父不能靠慢把等待拉长）', deriveRequestTimeoutMs(40, { expectedDeliveryMs: 60000 }), 3000)
check('上限就是改动前的固定值：3×高 RTT 也被截到 3000ms（新公式不会比以前等更久）', deriveRequestTimeoutMs(5000), 3000)
check('两个输入都没有 → 800ms 兜底', deriveRequestTimeoutMs(0, { expectedDeliveryMs: 0 }), 800)
check('单次取数最多换父跳数', MAX_FETCH_FAILOVER_HOPS, 2)

console.log(`\n通过 ${passed} / 失败 ${failed}`)
console.log(failed === 0 ? '判定：PASS' : '判定：FAIL')
process.exit(failed === 0 ? 0 : 1)
