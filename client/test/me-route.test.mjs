/**
 * 「我的管理台」URL 契约（票 #69）的表驱动测试。
 *
 * 判据只落在**公开行为**上：给一个 query 值 / 给一个 tab，得到什么 URL 与什么判定。
 * 不测内部实现（常量怎么存、函数怎么拆）。
 *
 * 为什么这一族要有测试：URL 是**跨票契约**（`dmRoute` / `friendsRoute` / `postsRoute`
 * 的同族兄弟）。`?tab=` 一旦改名，分享出去的 `/me?tab=settings` 就会静默退化成
 * 概览页 —— 而那种退化在界面上没有任何报错，只有表驱动测试能钉住它。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'

import { ME_DEFAULT_LIST, ME_DEFAULT_TAB, ME_LIST_QUERY, ME_TAB_QUERY, mePath, parseMeList, parseMeTab } from '../src/utils/meRoute.ts'

test('URL 键名与默认值是稳定的跨票契约', () => {
  assert.equal(ME_TAB_QUERY, 'tab')
  assert.equal(ME_LIST_QUERY, 'list')
  assert.equal(ME_DEFAULT_TAB, 'overview')
  assert.equal(ME_DEFAULT_LIST, 'mutual')
})

test('tab 解析：三个合法值原样返回，其余一律回落 overview', () => {
  assert.equal(parseMeTab('overview'), 'overview')
  assert.equal(parseMeTab('relations'), 'relations')
  assert.equal(parseMeTab('settings'), 'settings')
  // 未知值 / 缺省 / 非字符串：都落到默认 tab（**不是**报错）。
  for (const raw of ['', 'profile', 'SETTINGS', 'settings ', undefined, null, 7, {}, []]) {
    assert.equal(parseMeTab(raw), 'overview', '输入 ' + JSON.stringify(raw))
  }
})

test('mePath 默认 tab 不写进 URL', () => {
  assert.equal(mePath(), '/me')
  assert.equal(mePath({}), '/me')
  // `/me` 与 `/me?tab=overview` 是同一个地方，只能有一个"对"的地址。
  assert.equal(mePath({ tab: 'overview' }), '/me')
})

test('mePath 非默认 tab 写进 URL', () => {
  assert.equal(mePath({ tab: 'relations' }), '/me?tab=relations')
  assert.equal(mePath({ tab: 'settings' }), '/me?tab=settings')
})

test('mePath 与 parseMeTab 互为逆运算', () => {
  for (const tab of ['overview', 'relations', 'settings']) {
    const path = mePath({ tab })
    const query = path.includes('?') ? path.slice(path.indexOf('?') + 1) : ''
    const raw = query === '' ? undefined : query.slice(query.indexOf('=') + 1)
    assert.equal(parseMeTab(raw), tab, '往返失败：' + tab)
  }
})

test('list 解析：四个合法值原样返回，其余一律回落 mutual', () => {
  assert.equal(parseMeList('mutual'), 'mutual')
  assert.equal(parseMeList('following'), 'following')
  assert.equal(parseMeList('followers'), 'followers')
  assert.equal(parseMeList('blocked'), 'blocked')
  for (const raw of ['', 'friends', 'FOLLOWERS', undefined, null, 7, {}]) {
    assert.equal(parseMeList(raw), 'mutual', '输入 ' + JSON.stringify(raw))
  }
})

test('list 只在关系分区里写进 URL', () => {
  assert.equal(mePath({ tab: 'relations' }), '/me?tab=relations')
  assert.equal(mePath({ tab: 'relations', list: 'mutual' }), '/me?tab=relations')
  assert.equal(mePath({ tab: 'relations', list: 'blocked' }), '/me?tab=relations&list=blocked')
  // 非关系分区带着 list：忽略（不能让人以为"切个 tab 名单还在"）。
  assert.equal(mePath({ tab: 'settings', list: 'followers' }), '/me?tab=settings')
  assert.equal(mePath({ list: 'followers' }), '/me')
})
