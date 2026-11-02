/**
 * 计数缩写的**表驱动**用例（票 #63）。
 *
 * 为什么有这个文件：票面第 ② 条要求「计数 0 隐数字、1.2k/1.2w 缩写」，而真机脚本
 * `verify-authorrow.mjs` 只能钉住 mock 数据里真实出现的数量级（23 / 41 / 0 这类），
 * 钉不住 999 / 1200 / 12345 这三档的边界 —— 那三档恰好是判据的原文。
 *
 * 所以按仓内既有做法（见 relative-time.test.mjs 的说明）：**零新依赖**，
 * 用 Node 内置 test runner + Node 自带的 TypeScript 类型剥离：
 *
 *   node --test "client/test/*.test.mjs"
 *
 * 期望值全部是**字面量**，不用实现反推（同义反复是 TDD 的典型反模式）。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import { formatCount } from '../src/utils/count.ts'

test('0 与负数不显示数字（由组件隐藏整个计数位）', () => {
  assert.equal(formatCount(0), '')
  assert.equal(formatCount(-3), '')
})

test('三位数原样显示，不加任何单位', () => {
  assert.equal(formatCount(1), '1')
  assert.equal(formatCount(999), '999')
})

test('四位数用 k，保留一位小数（整数不带 .0）', () => {
  assert.equal(formatCount(1000), '1k')
  assert.equal(formatCount(1200), '1.2k')
  // 截断而不是四舍五入：9999 显示 9.9k，绝不显示「10k」——
  // 把没到的数说大，比说小更容易让人以为数据错了。
  assert.equal(formatCount(9999), '9.9k')
})

test('五位数及以上用 w（万），同样截断到一位小数', () => {
  assert.equal(formatCount(10000), '1w')
  assert.equal(formatCount(12345), '1.2w')
  assert.equal(formatCount(1000000), '100w')
})
