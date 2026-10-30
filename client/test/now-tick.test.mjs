/**
 * 全站单一刻度的行为用例（票 #60）。
 *
 * 判据来自票面："全站单一刻度 30s、document.hidden 暂停、回可见补算；组件禁自建定时器"。
 * 这里是**可注入环境**的纯逻辑测试（不依赖浏览器），浏览器侧的真机断言在
 * `test/script/verify-relativetime.mjs`。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import { createNowTick, TICK_INTERVAL_MS } from '../src/utils/nowTick.ts'

/**
 * 假环境：可控的时钟 + 可数的定时器 + 可切换的可见性。
 *
 * 为什么要能"数定时器"：判据"全站单一刻度"的本质是**同时最多一个定时器**，
 * 而"组件禁自建定时器"意味着页面上有多少个时间组件都不能改变这个数。
 */
function fakeEnv() {
  let t = 1_700_000_000_000
  let hidden = false
  let nextId = 1
  const timers = new Map()
  let visibilityCb = null

  return {
    env: {
      now: () => t,
      setInterval: (fn, ms) => {
        const id = nextId++
        timers.set(id, { fn, ms })
        return id
      },
      clearInterval: (handle) => {
        timers.delete(handle)
      },
      isHidden: () => hidden,
      subscribeVisibility: (cb) => {
        visibilityCb = cb
        return () => {
          visibilityCb = null
        }
      },
    },
    /** 推进时间并触发到期的定时器。 */
    advance(ms) {
      t += ms
      for (const timer of [...timers.values()]) timer.fn()
    },
    setHidden(next) {
      hidden = next
      if (visibilityCb) visibilityCb()
    },
    timerCount: () => timers.size,
    timerIntervals: () => [...timers.values()].map((x) => x.ms),
    /** 手动推进时间**但不**触发定时器（模拟"页面在后台没走表"）。 */
    moveClock(ms) {
      t += ms
    },
  }
}

test('刻度间隔是 30 秒', () => {
  assert.equal(TICK_INTERVAL_MS, 30_000)
})

test('多个订阅者只产生一个定时器（全站单一刻度）', () => {
  const fake = fakeEnv()
  const tick = createNowTick(fake.env)
  const seen = []
  tick.subscribe(() => seen.push(1))
  tick.subscribe(() => seen.push(1))
  tick.acquire()
  tick.acquire()

  assert.equal(fake.timerCount(), 1, '两个订阅者只应有一个定时器')
  assert.deepEqual(fake.timerIntervals(), [30_000])

  fake.advance(30_000)
  assert.equal(seen.length, 2, '一次刻度应通知两个订阅者')
})

test('刻度推进时把新时刻交给订阅者', () => {
  const fake = fakeEnv()
  const tick = createNowTick(fake.env)
  const seen = []
  tick.subscribe((now) => seen.push(now))
  tick.acquire()

  const before = fake.env.now()
  fake.advance(30_000)
  assert.equal(seen.length, 1)
  assert.equal(seen[0], before + 30_000)
})

test('document.hidden 时暂停：定时器被清掉，且不再推进', () => {
  const fake = fakeEnv()
  const tick = createNowTick(fake.env)
  const seen = []
  tick.subscribe((now) => seen.push(now))
  tick.acquire()

  fake.setHidden(true)
  assert.equal(fake.timerCount(), 0, '隐藏时应暂停定时器')

  const countBefore = seen.length
  fake.advance(60_000)
  assert.equal(seen.length, countBefore, '隐藏期间不应推进刻度')
})

test('回可见立即补算（不等下一个 30 秒）', () => {
  const fake = fakeEnv()
  const tick = createNowTick(fake.env)
  const seen = []
  tick.subscribe((now) => seen.push(now))
  tick.acquire()

  fake.setHidden(true)
  // 隐藏期间时间过去了 10 分钟，但表没走。
  fake.moveClock(10 * 60_000)

  fake.setHidden(false)
  assert.equal(seen.length, 1, '回可见应立刻补算一次')
  assert.equal(seen[0], fake.env.now(), '补算出来的应是当前时刻')
  assert.equal(fake.timerCount(), 1, '回可见应恢复定时器')
})

test('最后一个订阅者退订后定时器被清掉', () => {
  const fake = fakeEnv()
  const tick = createNowTick(fake.env)
  const offA = tick.subscribe(() => {})
  const offB = tick.subscribe(() => {})
  tick.acquire()
  tick.acquire()

  offA()
  tick.release()
  assert.equal(fake.timerCount(), 1, '还有一个订阅者时不该停表')

  offB()
  tick.release()
  assert.equal(fake.timerCount(), 0, '全部退订后必须停表')
})

test('退订后再订阅不重复建表（仍是单一刻度）', () => {
  const fake = fakeEnv()
  const tick = createNowTick(fake.env)
  const off = tick.subscribe(() => {})
  tick.acquire()
  off()
  tick.release()

  tick.subscribe(() => {})
  tick.acquire()
  assert.equal(fake.timerCount(), 1)
})
