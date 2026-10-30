#!/usr/bin/env node
/**
 * 相对时间六档单测：直接 import client/src/utils/relativeTime.ts（Node 24 的类型擦除）。
 *
 * 为什么是 node 脚本而不是 vitest：本仓库前端没有测试框架（client/package.json 里
 * 只有 vue-tsc + vite），为了一个纯函数引入一整套 vitest 依赖不划算；
 * 这个模块是**纯的**（不 import Vue、不碰 DOM、不读时钟 —— 时间由入参显式给出），
 * Node 原生 import 就能覆盖，且和验收脚本同一条命令风格。
 *
 * 为什么表驱动：六档判据的价值全在**边界**上（59.999 秒 vs 60 秒、6 天 23:59:59 vs
 * 恰好 7 天、跨年那天）。逐条 if 写断言很容易漏掉边界两侧，而边界恰恰是最容易在重构里
 * 被悄悄改掉的地方 —— 所以每一档的临界值都写进同一张表，边界两侧成对出现。
 *
 * 覆盖：
 *   · 六档判据的每一条，以及每档的临界值两侧；
 *   · 非法输入（'not-a-date'、空串）与未来时间（delta < 0）→ 原样透出；
 *   · 绝对日期档的同年 / 跨年两种形态；
 *   · formatAbsoluteTime 的补零形态与非法输入。
 *
 * 用法：node test/script/verify-relativetime.mjs
 */
process.env.TZ = 'Asia/Shanghai'

import { RELATIVE_TIME_TICK_MS, formatAbsoluteTime, formatRelativeTime, parseTimestamp } from '../../client/src/utils/relativeTime.ts'

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

const NOW = Date.parse('2026-10-30T12:00:00+08:00')
const DAY = 86_400_000
const HOUR = 3_600_000
const MINUTE = 60_000

/** 相对 NOW 偏移 ms 后的 ISO 串（用 toISOString 保证是合法 ISO）；负偏移 = 过去，正偏移 = 未来。 */
function at(offsetMs) {
  return new Date(NOW + offsetMs).toISOString()
}

// 表驱动：每行 = [用例名, 输入 iso, 参照 now, 期望文案]
const RELATIVE_CASES = [
  // 第 3 档：1 分钟以内 → 「刚刚」
  ['0 秒 → 刚刚', at(0), NOW, '刚刚'],
  ['59.999 秒 → 刚刚（1 分钟的前一刻）', at(-59_999), NOW, '刚刚'],

  // 第 4 档：1 分钟到 1 小时 → 「N分钟前」
  ['60 秒 → 1分钟前（1 分钟的这一刻）', at(-MINUTE), NOW, '1分钟前'],
  ['59 分 59 秒 → 59分钟前', at(-(59 * MINUTE + 59_000)), NOW, '59分钟前'],

  // 第 5 档：1 小时到 1 天 → 「N小时前」
  ['1 小时 → 1小时前（1 小时的这一刻）', at(-HOUR), NOW, '1小时前'],
  ['23:59:59 → 23小时前', at(-(23 * HOUR + 59 * MINUTE + 59_000)), NOW, '23小时前'],

  // 第 6 档：1 天到 7 天 → 「N天前」
  ['24 小时 → 1天前（1 天的这一刻）', at(-DAY), NOW, '1天前'],
  ['6 天 23:59:59 → 6天前', at(-(6 * DAY + 23 * HOUR + 59 * MINUTE + 59_000)), NOW, '6天前'],
  ['7 天整减 1 毫秒 → 6天前（相对档只覆盖 < 7 天）', at(-(7 * DAY - 1)), NOW, '6天前'],

  // 第 7 档：≥ 7 天 → 绝对日期
  ['恰好 7 天 → 绝对日期（同年形态）', at(-7 * DAY), NOW, '10月23日'],
  ['7 天整加 1 毫秒 → 仍是绝对日期', at(-(7 * DAY + 1)), NOW, '10月23日'],
  ['跨年：2025-12-25 相对 2026-01-02 → 2025年12月25日', '2025-12-25T12:00:00+08:00', Date.parse('2026-01-02T12:00:00+08:00'), '2025年12月25日'],
  ['同年但相隔很久：2026-01-02 相对 2026-10-30 → 1月2日', '2026-01-02T12:00:00+08:00', NOW, '1月2日'],

  // 第 1、2 档：非法输入与未来时间 → 原样透出（不编一个假时间占位）
  ['非法串原样透出', 'not-a-date', NOW, 'not-a-date'],
  ['空串原样透出', '', NOW, ''],
  ['未来时间原样透出（delta < 0）', '2026-10-30T05:00:00.000Z', NOW, '2026-10-30T05:00:00.000Z'],
  ['未来时间原样透出（字符串形式逐字返回）', '2026-10-30T13:00:00+08:00', NOW, '2026-10-30T13:00:00+08:00'],
]

// 时区守卫：绝对日期档依赖**本地时区**（getFullYear 等），本脚本把 TZ 钉在 +08:00
// （无夏令时，跨月跨年的边界稳定）。若运行环境不让改 TZ，这里必须显性红 ——
// 静默给出环境相关的结果会让"通过"变成一句没有意义的断言。
{
  const offsetMin = new Date(NOW).getTimezoneOffset()
  check('时区守卫：本地时区偏移为 -480 分钟（+08:00）', offsetMin, -480)
}

console.log('== 六档相对时间判据 ==')
for (const [name, iso, now, expected] of RELATIVE_CASES) {
  check(name, formatRelativeTime(iso, now), expected)
}

console.log('\n== parseTimestamp ==')
check('合法 ISO → 毫秒时间戳', parseTimestamp('2026-10-30T12:00:00+08:00'), NOW)
check('非法串 → null', parseTimestamp('not-a-date'), null)
check('空串 → null', parseTimestamp(''), null)
check('纯空白 → null', parseTimestamp('   '), null)

console.log('\n== formatAbsoluteTime（补零形态，用于 title / aria-label）==')
check("'2026-01-02T03:04:00' 本地 → '2026-01-02 03:04'", formatAbsoluteTime('2026-01-02T03:04:00'), '2026-01-02 03:04')
check('带 +08:00 偏移 → 本地时刻', formatAbsoluteTime('2026-10-30T12:00:00+08:00'), '2026-10-30 12:00')
check('非法串 → 空串', formatAbsoluteTime('not-a-date'), '')
check('空串 → 空串', formatAbsoluteTime(''), '')

console.log('\n== 刷新节拍 ==')
check('RELATIVE_TIME_TICK_MS = 30 秒', RELATIVE_TIME_TICK_MS, 30_000)

console.log(`\n通过 ${passed} / 失败 ${failed}`)
console.log(failed === 0 ? '判定：PASS' : '判定：FAIL')
process.exit(failed === 0 ? 0 : 1)
