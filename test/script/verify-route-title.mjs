#!/usr/bin/env node
/**
 * 票 #79（懒加载 + 预热 + 标题 + 焦点播报）的**纯函数**判据。
 *
 * 为什么是 node 脚本而不是 vitest：本仓库前端没有测试框架（client/package.json 里
 * 只有 vue-tsc + vite），为了三个纯函数引入一整套 vitest 依赖不划算；
 * 这些模块是**纯的**（不 import Vue、不碰 DOM、不读时钟），Node 原生 import 就能覆盖，
 * 且与验收脚本同一条命令风格（先例：verify-relativetime.mjs / verify-schedule.mjs）。
 *
 * 真机部分（首包产物、真实换页的标题与播报）在 verify-routelazy.mjs 里 ——
 * 那些判据必须在**浏览器**里看 DOM 与产物文件。
 *
 * 覆盖：
 *   · 文档标题拼装（页面名 · 月喵 / 空页面名退回品牌名 / 首尾空白）；
 *   · meta.title 的两种形态（静态串 / 函数）与异常兜底；
 *   · 「算不算换页」的判据（首屏不算 / 路由名变算 / 参数变算 / 仅 query 变不算）；
 *   · 参数浅比较（数组参数、缺键）。
 *
 * 用法：node test/script/verify-route-title.mjs
 */
import {
  BRAND_NAME,
  formatDocumentTitle,
  isPageChange,
  resolveDocumentTitle,
  resolvePageName,
  sameParams,
} from '../../client/src/utils/routeMeta.ts'

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

const CTX = { params: {}, path: '/' }

console.log('== 文档标题拼装 ==')
check('品牌名常量', BRAND_NAME, '月喵')
check('页面名 + 品牌后缀', formatDocumentTitle('帖子'), '帖子 · 月喵')
check('空页面名退回纯品牌名（不出现「 · 月喵」半截串）', formatDocumentTitle(''), '月喵')
check('纯空白页面名同样退回品牌名', formatDocumentTitle('   '), '月喵')
check('首尾空白被裁掉', formatDocumentTitle('  好友  '), '好友 · 月喵')
check('页面名里的「·」不被当成后缀', formatDocumentTitle('管理端 · 运维'), '管理端 · 运维 · 月喵')

console.log('\n== meta.title 取值（两种形态）==')
check('静态串', resolvePageName({ title: '消息' }, CTX), '消息')
check('静态串被裁空白', resolvePageName({ title: '  消息 ' }, CTX), '消息')
check(
  '函数形态：按路由参数求值（房间码）',
  resolvePageName({ title: (r) => String(r.params.roomId ?? '').toUpperCase() }, {
    params: { roomId: 'abcd' },
    path: '/room/ABCD',
  }),
  'ABCD',
)
check('函数形态返回非字符串 → 空串', resolvePageName({ title: () => 42 }, CTX), '')
check('函数形态抛错 → 空串（标题坏了不许让导航失败）', resolvePageName({ title: () => { throw new Error('boom') } }, CTX), '')
check('缺 meta.title → 空串', resolvePageName({}, CTX), '')
check('meta 为 undefined → 空串', resolvePageName(undefined, CTX), '')
check('title 类型不对（数字）→ 空串', resolvePageName({ title: 7 }, CTX), '')

console.log('\n== 一步到位：路由 → 文档标题 ==')
check('静态串', resolveDocumentTitle({ title: '帖子' }, CTX), '帖子 · 月喵')
check('缺 title → 纯品牌名', resolveDocumentTitle({}, CTX), '月喵')
check(
  '函数形态',
  resolveDocumentTitle({ title: (r) => r.path.toUpperCase() }, { params: {}, path: '/posts' }),
  '/POSTS · 月喵',
)

console.log('\n== 「算不算换页」==')
const FROM = { name: 'posts', params: { id: '1' }, matched: [{}] }
const FIRST = { name: 'posts', params: {}, matched: [] }

// 首屏不算换页：打开页面时读屏本来就会念标题，重复播报等于念两遍。
check('首屏（from.matched 为空）不算换页', isPageChange({ name: 'posts', params: {} }, FIRST), false)
// 路由名不同 → 换页
check('路由名不同 → 换页', isPageChange({ name: 'friends', params: {} }, FROM), true)
// 路由名相同但参数不同 → 也是换页（另一篇帖子）
check('参数不同 → 换页', isPageChange({ name: 'posts', params: { id: '2' } }, FROM), true)
// 路由名与参数都相同、只有 query 变了 → 不是换页（同一屏的状态变化）
check('仅 query 变化 → 不算换页', isPageChange({ name: 'posts', params: { id: '1' } }, FROM), false)

console.log('\n== 参数浅比较 ==')
check('完全相同', sameParams({ a: '1', b: '2' }, { a: '1', b: '2' }), true)
check('值不同', sameParams({ a: '1' }, { a: '2' }), false)
check('键数不同', sameParams({ a: '1' }, { a: '1', b: '2' }), false)
check('空对空', sameParams({}, {}), true)
check('数组参数相同', sameParams({ a: ['x', 'y'] }, { a: ['x', 'y'] }), true)
check('数组参数不同', sameParams({ a: ['x', 'y'] }, { a: ['x', 'z'] }), true === false)
check('数组长度不同', sameParams({ a: ['x'] }, { a: ['x', 'y'] }), false)
check('数组对标量 → 不同', sameParams({ a: ['x'] }, { a: 'x' }), false)

console.log(`\n通过 ${passed} / 失败 ${failed}`)
console.log(failed === 0 ? '判定：PASS' : '判定：FAIL')
process.exit(failed === 0 ? 0 : 1)
