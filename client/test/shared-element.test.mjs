/**
 * `utils/sharedElement.ts` 的纯逻辑单测（票 #78）。
 *
 * 为什么这些必须单测：morph 的**降级**分支在真机上表现为"什么都没发生"，
 * 它和"这段代码根本没跑"长得一模一样。真机脚本只能证明"该飞的时候飞了"，
 * 证明不了"该不飞的时候确实没飞，而且理由是想要的那一个"。
 *
 * 跑法：`cd client && node --test test/shared-element.test.mjs`
 */
import test from 'node:test'
import assert from 'node:assert/strict'
import {
  MORPH_DURATION_MS,
  SHARED_ATTR,
  parseSharedId,
  planSharedMorph,
} from '../src/utils/sharedElement.ts'

/** 造一个 `forward` 决策（其余字段取决策器的真实形状）。 */
function forward() {
  return { kind: 'forward', shiftPx: 12, durationMs: 240, reason: '进详情' }
}

/** 造一个矩形。 */
const rect = (x, y, width, height) => ({ x, y, width, height })

test('SHARED_ATTR 与 MORPH_DURATION_MS 是契约常量', () => {
  assert.equal(SHARED_ATTR, 'data-shared')
  // 决议 #45 §3：morph 440ms（= --dur-morph），比页面 240ms 长，两者并行。
  assert.equal(MORPH_DURATION_MS, 440)
  assert.ok(MORPH_DURATION_MS > 240, 'morph 必须比页面过渡长')
})

test('parseSharedId 解析三处现役取值', () => {
  assert.deepEqual(parseSharedId('post-cover:12'), { kind: 'post-cover', key: '12' })
  assert.deepEqual(parseSharedId('avatar:1'), { kind: 'avatar', key: '1' })
  assert.deepEqual(parseSharedId('room:MOON04'), { kind: 'room', key: 'MOON04' })
})

test('parseSharedId 只按第一个冒号切，key 里允许再有冒号', () => {
  assert.deepEqual(parseSharedId('avatar:1:2'), { kind: 'avatar', key: '1:2' })
})

test('parseSharedId 会 trim 两侧', () => {
  assert.deepEqual(parseSharedId('  avatar:1  '), { kind: 'avatar', key: '1' })
  assert.deepEqual(parseSharedId('avatar : 1'), { kind: 'avatar', key: '1' })
})

test('parseSharedId 对一切"没声明"返回 null，不猜默认 kind', () => {
  const bad = [
    undefined,
    null,
    '',
    '   ',
    'avatar',
    'avatar:',
    ':1',
    ' : ',
    'avatar:   ',
  ]
  for (const raw of bad) {
    assert.equal(parseSharedId(raw), null, `应当拒绝 ${JSON.stringify(raw)}`)
  }
})

test('forward + 两端都在 + 有尺寸 → 飞 440ms', () => {
  const plan = planSharedMorph(forward(), {
    from: rect(20, 300, 120, 80),
    to: rect(340, 120, 480, 270),
  })
  assert.equal(plan.kind, 'morph')
  assert.equal(plan.durationMs, 440)
})

test('只有 forward 触发：back / lateral / none / exempt / instant 一律降级', () => {
  for (const kind of ['back', 'lateral', 'none', 'exempt', 'instant']) {
    const plan = planSharedMorph(
      { kind, shiftPx: 0, durationMs: kind === 'back' ? 240 : 0, reason: kind },
      { from: rect(0, 0, 10, 10), to: rect(0, 0, 100, 100) },
    )
    assert.equal(plan.kind, 'degrade', `${kind} 不该 morph`)
    assert.match(plan.reason, /只有前进触发/)
  }
})

test('放映室进出（exempt）不做 morph —— 卡→房的共享因此自动禁用', () => {
  // 这条是「房页豁免优先」的执行点：卡上照样写了 data-shared="room:xxx"，
  // 但决策是 exempt，于是不飞。接线不需要额外判断，也就不会出现"两处各判一遍"。
  const plan = planSharedMorph(
    { kind: 'exempt', shiftPx: 0, durationMs: 0, reason: '进放映室' },
    { from: rect(0, 0, 300, 200), to: rect(0, 0, 600, 400) },
  )
  assert.equal(plan.kind, 'degrade')
})

test('源元素缺失 → 降级', () => {
  const plan = planSharedMorph(forward(), { from: null, to: rect(0, 0, 10, 10) })
  assert.equal(plan.kind, 'degrade')
  assert.match(plan.reason, /离开页/)
})

test('目标元素缺失 → 降级（目标还没渲染出来的主路径）', () => {
  const plan = planSharedMorph(forward(), { from: rect(0, 0, 10, 10), to: null })
  assert.equal(plan.kind, 'degrade')
  assert.match(plan.reason, /进入页/)
})

test('零尺寸任一端都降级（隐藏元素 / 未布局）', () => {
  const zeroW = planSharedMorph(forward(), { from: rect(0, 0, 0, 80), to: rect(0, 0, 10, 10) })
  const zeroH = planSharedMorph(forward(), { from: rect(0, 0, 120, 80), to: rect(0, 0, 480, 0) })
  assert.equal(zeroW.kind, 'degrade')
  assert.equal(zeroH.kind, 'degrade')
  assert.match(zeroW.reason, /尺寸为 0/)
})

test('源图片未解码 → 降级（飞过去的会是空白框）', () => {
  const plan = planSharedMorph(forward(), {
    from: rect(0, 0, 120, 80),
    to: rect(0, 0, 480, 270),
    imageReady: false,
  })
  assert.equal(plan.kind, 'degrade')
  assert.match(plan.reason, /解码/)
})

test('imageReady 缺省视为已就绪（字母头像、纯色块不需要解码）', () => {
  const plan = planSharedMorph(forward(), {
    from: rect(0, 0, 32, 32),
    to: rect(0, 0, 96, 96),
  })
  assert.equal(plan.kind, 'morph')
})

test('判序：方向优先于"元素缺失"，reason 给出的是真原因', () => {
  // 后退时目标缺失是常态（列表在重新拉数据）。如果判序写反，
  // reason 会变成"进入页没有元素"，排查时会以为接线漏了。
  const plan = planSharedMorph(
    { kind: 'back', shiftPx: 12, durationMs: 240, reason: '回列表' },
    { from: null, to: null },
  )
  assert.equal(plan.kind, 'degrade')
  assert.match(plan.reason, /只有前进触发/)
})

test('morph 时长与页面时长解耦（页面 240，morph 440，并行）', () => {
  const plan = planSharedMorph(forward(), {
    from: rect(0, 0, 100, 100),
    to: rect(0, 0, 400, 400),
  })
  assert.equal(plan.kind, 'morph')
  assert.match(plan.reason, /440ms/)
  assert.match(plan.reason, /240ms/)
})
