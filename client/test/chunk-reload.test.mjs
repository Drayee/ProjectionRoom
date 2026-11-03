/**
 * chunk 自动刷新决策器的表驱动用例。
 *
 * 判据只落在**公开行为**上：给"现在几点"与"本会话刷过几次"，得到"该不该刷新"。
 * 不测 `sessionStorage` 的键名、不测 `location.reload` 有没有被调用（那是接线，不是判据）。
 *
 * 为什么这条判据值得一个测试：这段代码的作用域很窄 —— 它只在"用户拿着旧页面、
 * 而新构建已经删掉了那个 chunk"时生效。真机很难稳定复现（要先构建 A、加载页面、
 * 再构建 B、再点路由），而它**出错的方式很响**：判成"总是刷新"就是一个刷新循环，
 * 用户永远进不去站点。所以把边界钉在单测里。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import {
  CHUNK_RELOAD_COOLDOWN_MS,
  CHUNK_RELOAD_MAX,
  decideChunkReload,
} from '../src/utils/chunkReload.ts'

test('本会话还没刷过：刷新（这就是"部署了新版、页面还是旧的"的主路径）', () => {
  assert.deepEqual(decideChunkReload(1_000_000, null), { reload: true, reason: 'first' })
  // 计数为 0 与"没有记录"等价：写记录失败时会落成 0。
  assert.deepEqual(decideChunkReload(1_000_000, { at: 0, count: 0 }), { reload: true, reason: 'first' })
})

test('冷却期内不刷新（防连点两个分区就刷两次）', () => {
  const now = 10_000_000
  const record = { at: now - (CHUNK_RELOAD_COOLDOWN_MS - 1), count: 1 }
  assert.deepEqual(decideChunkReload(now, record), { reload: false, reason: 'cooldown' })
})

test('冷却刚好到点：可以再刷（边界是 >= 而不是 >）', () => {
  const now = 10_000_000
  const record = { at: now - CHUNK_RELOAD_COOLDOWN_MS, count: 1 }
  assert.deepEqual(decideChunkReload(now, record), { reload: true, reason: 'first' })
})

test('次数用尽之后一律不刷（刷新后仍然坏时不能变成刷新循环）', () => {
  const now = 10_000_000
  // 冷却早就过了，但次数已经到上限 —— 次数闸必须独立生效。
  const record = { at: 0, count: CHUNK_RELOAD_MAX }
  assert.deepEqual(decideChunkReload(now, record), { reload: false, reason: 'too-many' })
})

test('系统时钟回拨（记录的时刻在未来）按冷却处理，不刷新', () => {
  const now = 1_000
  const record = { at: now + 60_000, count: 1 }
  assert.deepEqual(decideChunkReload(now, record), { reload: false, reason: 'cooldown' })
})

test('常量本身是保守值：不刷太多次，也不允许长到用户等不到', () => {
  // 上限必须是有限的小数字：大了就是刷新循环。
  assert.ok(CHUNK_RELOAD_MAX >= 1 && CHUNK_RELOAD_MAX <= 3, `CHUNK_RELOAD_MAX=${CHUNK_RELOAD_MAX} 应落在 [1,3]`)
  // 冷却必须小于一次人工刷新能忍的等待，又大于"连点两个导航"的间隔。
  assert.ok(
    CHUNK_RELOAD_COOLDOWN_MS >= 3_000 && CHUNK_RELOAD_COOLDOWN_MS <= 60_000,
    `CHUNK_RELOAD_COOLDOWN_MS=${CHUNK_RELOAD_COOLDOWN_MS} 应落在 [3s,60s]`,
  )
})
