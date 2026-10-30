/**
 * 相对时间的**表驱动**边界用例（票 #60）。
 *
 * 为什么是这个文件：本仓前端**没有**单测框架（见 spec #55 的 Testing Decisions：
 * "仓内前端零单测设施，不引入新框架"），而本票的验收清单明确要求"六档边界
 * （超 7 天回绝对日期）表驱动测资"。折中办法是**只用 Node 内置的 test runner**
 * （node:test，零新依赖）+ Node 自带的 TypeScript 类型剥离：
 *
 *   node --test "client/test/*.test.mjs"
 *
 * ⚠ 需要 Node ≥ 22.6（类型剥离在 22.6 才可用，23.6 起默认开启）。
 * 本机是 Node 24。CI 的 build-push.yml 目前用 Node 20 —— 那条线**跑不了**这个文件
 * （类型剥离不存在），所以它没有接进 CI；接线需要动 CI 的 Node 版本，属于另一票。
 *
 * **期望值全部是字面量**（不是用实现反推）：时间用**本地时间分量**构造
 * （new Date(2026, 9, 30, 12, 0, 0)），这样断言与运行机器的时区无关。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import { timeView } from '../src/utils/relativeTime.ts'

const pad = (n) => String(n).padStart(2, '0')

/**
 * 本地时间分量 → 期望的"精确绝对时间"字面量（`YYYY-MM-DD HH:mm`）。
 *
 * **不带秒**是决策票 #44 的原文（"title 与 aria-label 给本地时区的
 * YYYY-MM-DD HH:mm"）—— 悬停提示是给人**核对**用的，分钟粒度已经够，
 * 而秒会让它看起来像日志而不是界面文案。
 */
function fullOf(d) {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 本地时间分量 → 期望的"绝对日期"字面量（同年 M月D日 / 跨年 YYYY年M月D日）。 */
function dateOf(d) {
  const m = d.getMonth() + 1
  const day = d.getDate()
  return d.getFullYear() === NOW.getFullYear() ? `${m}月${day}日` : `${d.getFullYear()}年${m}月${day}日`
}

/** 基准时刻：2026-10-30 12:00:00 本地时间。 */
const NOW = new Date(2026, 9, 30, 12, 0, 0, 0)
const NOW_MS = NOW.getTime()

const MINUTE = 60 * 1000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/**
 * 六档边界表。
 *
 * 每一行都给**完整**的四项期望：相对文案、绝对日期、精确绝对时间、datetime 值。
 * 只断言其中一项会让"某一档写错了但另一项对"这种半对实现蒙混过关。
 */
const CASES = [
  { name: '5 秒前 = 刚刚', at: new Date(NOW.getTime() - 5 * 1000), relative: '刚刚' },
  { name: '59.999 秒前仍是刚刚', at: new Date(NOW.getTime() - 59_999), relative: '刚刚' },
  { name: '整 60 秒 = 1 分钟前', at: new Date(NOW.getTime() - MINUTE), relative: '1 分钟前' },
  { name: '59 分钟前', at: new Date(NOW.getTime() - 59 * MINUTE), relative: '59 分钟前' },
  { name: '整 60 分钟 = 1 小时前', at: new Date(NOW.getTime() - HOUR), relative: '1 小时前' },
  { name: '23 小时前', at: new Date(NOW.getTime() - 23 * HOUR), relative: '23 小时前' },
  { name: '整 24 小时 = 1 天前', at: new Date(NOW.getTime() - DAY), relative: '1 天前' },
  { name: '6 天前', at: new Date(NOW.getTime() - 6 * DAY), relative: '6 天前' },
  // 恰好 7 天：**已经**落绝对档（决策票 #44 的表是「差值 < 7 天 → N 天前」）。
  { name: '整 7 天回绝对日期（同年）', at: new Date(NOW.getTime() - 7 * DAY), relative: '10月23日' },
  { name: '7 天零 1 毫秒仍是天前档', at: new Date(NOW.getTime() - 7 * DAY + 1), relative: '6 天前' },
  { name: '超 7 天（同年）回绝对日期', at: new Date(NOW.getTime() - 8 * DAY), relative: '10月22日' },
  { name: '超 7 天（跨年）含年份', at: new Date(2025, 11, 20, 8, 30, 0), relative: '2025年12月20日' },
  { name: '未来时间按刚刚处理', at: new Date(NOW.getTime() + HOUR), relative: '刚刚' },
]

test('六档相对时间的边界（表驱动）', () => {
  for (const c of CASES) {
    const view = timeView(c.at.toISOString(), NOW_MS)
    assert.equal(view.relative, c.relative, `相对文案：${c.name}`)
    assert.equal(view.date, dateOf(c.at), `绝对日期：${c.name}`)
    assert.equal(view.full, fullOf(c.at), `精确绝对时间：${c.name}`)
    assert.equal(view.machine, c.at.toISOString(), `datetime 值：${c.name}`)
    assert.equal(view.invalid, false, `非法标记：${c.name}`)
  }
})

test('非法时间原样透出，且不给 datetime', () => {
  const view = timeView('昨天', NOW_MS)
  assert.equal(view.relative, '昨天')
  assert.equal(view.date, '昨天')
  assert.equal(view.full, '昨天')
  assert.equal(view.machine, null)
  assert.equal(view.invalid, true)
})

test('空串与 null 原样透出（不当成"刚刚"）', () => {
  for (const raw of ['', '   ']) {
    const view = timeView(raw, NOW_MS)
    assert.equal(view.relative, raw)
    assert.equal(view.invalid, true)
    assert.equal(view.machine, null)
  }
})

test('同一天内跨过零点仍走"天前"档（不因为跨年改写成绝对日期）', () => {
  // 基准：2026-01-02 00:30 本地；3 天前 = 2025-12-30 → 属"天前"档，不含年。
  const now = new Date(2026, 0, 2, 0, 30, 0, 0)
  const view = timeView(new Date(2025, 11, 30, 0, 30, 0, 0).toISOString(), now.getTime())
  assert.equal(view.relative, '3 天前')
})
