/**
 * 好友页 URL 契约（票 #58）的表驱动测试。
 *
 * 判据只落在**公开行为**上：给一个 query 串 / 给一个状态，得到什么 URL 与什么判定。
 * 不测内部实现（键名常量怎么存、函数怎么拆）。
 *
 * 为什么这一族要有测试：URL 是**跨票契约**（`utils/dmRoute.ts` 的同族兄弟）。
 * `?tab=` 与 `?q=` 一旦改名，分享出去的链接就会静默退化成"打开好友页看到默认 tab"，
 * 而那种退化在界面上没有任何报错 —— 只有表驱动测试能钉住它。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'

import {
  FRIENDS_DEBOUNCE_MS,
  FRIENDS_MIN_QUERY,
  FRIENDS_Q_QUERY,
  FRIENDS_TAB_QUERY,
  friendsPath,
  parseFriendsQuery,
  parseFriendsTab,
  shouldSearch,
} from '../src/utils/friendsRoute.ts'

test('URL 键名是稳定的跨票契约', () => {
  assert.equal(FRIENDS_TAB_QUERY, 'tab')
  assert.equal(FRIENDS_Q_QUERY, 'q')
})

test('tab 解析：四个合法值原样返回，其余一律回落 mutual', () => {
  assert.equal(parseFriendsTab('mutual'), 'mutual')
  assert.equal(parseFriendsTab('following'), 'following')
  assert.equal(parseFriendsTab('followers'), 'followers')
  assert.equal(parseFriendsTab('blocked'), 'blocked')
  // 未知值 / 缺省 / 非字符串：都落到默认 tab（**不是**报错）。
  for (const raw of ['', 'friends', 'FRIENDS', 'follow', undefined, null, 7, {}]) {
    assert.equal(parseFriendsTab(raw), 'mutual', '输入 ' + JSON.stringify(raw))
  }
})

test('搜索词解析：去首尾空白，非字符串回落空串', () => {
  assert.equal(parseFriendsQuery('  放映员 '), '放映员')
  assert.equal(parseFriendsQuery(''), '')
  assert.equal(parseFriendsQuery('   '), '')
  for (const raw of [undefined, null, 7, {}, []]) {
    assert.equal(parseFriendsQuery(raw), '')
  }
})

test('friendsPath 只写非默认项，且参数顺序稳定', () => {
  assert.equal(friendsPath({}), '/friends')
  assert.equal(friendsPath({ tab: 'mutual' }), '/friends')
  assert.equal(friendsPath({ tab: 'followers' }), '/friends?tab=followers')
  assert.equal(friendsPath({ q: '猫' }), '/friends?q=%E7%8C%AB')
  assert.equal(friendsPath({ tab: 'blocked', q: '猫' }), '/friends?tab=blocked&q=%E7%8C%AB')
  // 空白搜索词不算"有搜索词"。
  assert.equal(friendsPath({ tab: 'following', q: '   ' }), '/friends?tab=following')
})

test('shouldSearch：≥2 个字符才发请求', () => {
  assert.equal(FRIENDS_MIN_QUERY, 2)
  assert.equal(shouldSearch(''), false)
  assert.equal(shouldSearch('  '), false)
  assert.equal(shouldSearch('猫'), false)
  assert.equal(shouldSearch(' 猫 '), false)
  assert.equal(shouldSearch('月夜'), true)
  assert.equal(shouldSearch(' 月夜 '), true)
})

test('防抖 300ms（票 #50 的数值，不是随手取的）', () => {
  assert.equal(FRIENDS_DEBOUNCE_MS, 300)
})
