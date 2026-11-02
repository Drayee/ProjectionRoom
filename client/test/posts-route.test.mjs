/**
 * `utils/postsRoute.ts` 的表驱动单测（票 #66）。
 *
 * 跑法：`npm --prefix client run test`（Node 内置 test runner + TS 类型剥离，零新依赖）。
 *
 * 为什么这个文件值得存在：URL 契约是**跨票契约**（分享出去的链接会永久留在
 * 聊天记录里）。键名或默认值一改，旧链接就落到别的 pill —— 而那种回归在页面上
 * 没有任何报错，只能靠这条用例挡住。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import {
  POSTS_DEFAULT_SORT,
  POSTS_SORT_QUERY,
  parsePostsSort,
  postsPath,
  sortNeedsLogin,
} from '../src/utils/postsRoute.ts'

test('键名与默认值是跨票契约', () => {
  assert.equal(POSTS_SORT_QUERY, 'sort')
  assert.equal(POSTS_DEFAULT_SORT, 'latest')
})

test('parsePostsSort：三个合法值原样返回', () => {
  assert.equal(parsePostsSort('latest'), 'latest')
  assert.equal(parsePostsSort('hot'), 'hot')
  assert.equal(parsePostsSort('following'), 'following')
})

test('parsePostsSort：未知/空/非字符串一律回落默认（不报错）', () => {
  for (const raw of ['', '   ', 'new', 'LATEST', 'follow', undefined, null, 7, {}, []]) {
    assert.equal(parsePostsSort(raw), POSTS_DEFAULT_SORT, `raw=${String(raw)}`)
  }
})

test('parsePostsSort：首尾空白被吃掉', () => {
  assert.equal(parsePostsSort('  hot  '), 'hot')
})

test('postsPath：默认排序不写进 URL', () => {
  assert.equal(postsPath(), '/posts')
  assert.equal(postsPath({}), '/posts')
  assert.equal(postsPath({ sort: 'latest' }), '/posts')
})

test('postsPath：非默认排序写进 URL', () => {
  assert.equal(postsPath({ sort: 'hot' }), '/posts?sort=hot')
  assert.equal(postsPath({ sort: 'following' }), '/posts?sort=following')
})

test('sortNeedsLogin：只有 following 需要登录', () => {
  assert.equal(sortNeedsLogin('following'), true)
  assert.equal(sortNeedsLogin('latest'), false)
  assert.equal(sortNeedsLogin('hot'), false)
})
