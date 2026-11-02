import { test } from 'node:test'
import assert from 'node:assert/strict'
import { followNoticeText, FOLLOW_NOTICE_TYPES } from '../src/utils/followNotice.ts'

test('两类事件各有一句人话，且都带对方的名字', () => {
  const created = followNoticeText('follow-created', '放映员')
  assert.ok(created, 'follow-created 必须有文案')
  assert.match(created, /放映员/)
  assert.match(created, /关注/)

  const mutual = followNoticeText('mutual-follow', '阿梓')
  assert.ok(mutual, 'mutual-follow 必须有文案')
  assert.match(mutual, /阿梓/)
  // 决议 #42 的措辞：互关即好友，文案里要出现"好友"
  assert.match(mutual, /好友/)
})

test('两类事件的文案必须不同', () => {
  const a = followNoticeText('follow-created', '同名')
  const b = followNoticeText('mutual-follow', '同名')
  assert.notEqual(a, b, '同一件事的两种程度不能给同一句话')
})

test('不认识的类型一律返回 null（不是空串）', () => {
  // null 与 '' 的区别在这里是判据：调用方靠 null 判断"这条事件不该弹提示"，
  // 空串会让它渲染一个空的 toast。
  assert.equal(followNoticeText('friend-removed', '某人'), null)
  assert.equal(followNoticeText('blocked', '某人'), null)
  assert.equal(followNoticeText('', '某人'), null)
  assert.equal(followNoticeText('dm', '某人'), null)
})

test('名字为空时不给出半句话', () => {
  // 服务端理论上总会带名字，但少一个字段不该变成" 关注了你"这种半句话。
  assert.equal(followNoticeText('follow-created', ''), null)
  assert.equal(followNoticeText('follow-created', '   '), null)
})

test('名字两端空白被裁掉', () => {
  assert.equal(followNoticeText('follow-created', '  放映员 '), '放映员 关注了你')
})

test('导出的事件类型表就是两类（取关/拉黑一律静默）', () => {
  assert.deepEqual([...FOLLOW_NOTICE_TYPES].sort(), ['follow-created', 'mutual-follow'])
})
