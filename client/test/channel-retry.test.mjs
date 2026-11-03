/**
 * 用户通道"取票失败该不该继续重连"的判据（2026-11-02 线上事故的回归哨兵）。
 *
 * # 事故现场
 *
 * 生产换域名后 `PR_ALLOWED_ORIGINS` 没跟着改，于是 `/ws/user` 的握手一律 403。
 * 当时的实现把所有取票失败都当瞬时故障退避重连，结果是控制台里几十条：
 *
 *   WebSocket connection to 'ws://…/ws/user?ticket=…' failed: … 403
 *   api/auth/ws-ticket:1  Failed to load resource: … 401
 *
 * 每一条 403 前面都先打一发取票请求 —— 因为"重连必须重新取票"。判据错一格的
 * 后果因此是双份的：既刷屏，又白烧取票配额。
 *
 * # 这里判什么
 *
 * 只判一个**零依赖纯函数**：给 `{ status, code }`，得到"该挂起等新凭据"还是
 * "该退避重试"。不判"重连了几次"（那要假 WebSocket 与假计时器，属接线）。
 *
 * ⚠ 本文件是 `.mjs`：Node 的类型擦除只对 `.ts` 生效，这里**不能**写 TypeScript 语法
 *（`(status: number)` 会让整个文件 SyntaxError）。要标注就写成 JSDoc。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import { isCredentialFailure } from '../src/utils/channelRetry.ts'

test('401 = 凭据被拒：挂起，别再用同一份失效凭据重连', () => {
  assert.equal(isCredentialFailure({ status: 401, code: 'AUTH_UNAUTHORIZED' }), true)
  // 具体错误码不影响判据（服务端换了码也不该退化成无限重连）。
  assert.equal(isCredentialFailure({ status: 401, code: 'ANYTHING' }), true)
})

test('403 = Origin 白名单拒绝：挂起（配置不改，重连一万次还是 403）', () => {
  // 这条就是事故当天的形态：/ws/user 握手 403。
  assert.equal(isCredentialFailure({ status: 403, code: 'BAD_REQUEST' }), true)
})

test('没有 access token（本机自造码 AUTH_NO_TOKEN）：挂起，别在未登录时空转', () => {
  assert.equal(isCredentialFailure({ status: 0, code: 'AUTH_NO_TOKEN' }), true)
})

test('网络不可达（status 0）= 瞬时故障：必须继续退避重试', () => {
  // 否则一次抖动就永久失去实时通道，而用户不会知道该刷新。
  assert.equal(isCredentialFailure({ status: 0, code: 'AUTH_NETWORK' }), false)
})

test('5xx 与 429 = 服务端临时状态：继续重试', () => {
  assert.equal(isCredentialFailure({ status: 500, code: 'AUTH_UNKNOWN' }), false)
  assert.equal(isCredentialFailure({ status: 503, code: 'AUTH_UNAVAILABLE' }), false)
  assert.equal(isCredentialFailure({ status: 429, code: 'RATE_LIMITED' }), false)
})

test('404（旧实例没有 ws-ticket 端点）继续重试：那份产物升级后就该自己好', () => {
  assert.equal(isCredentialFailure({ status: 404, code: 'NOT_FOUND' }), false)
})

test('400 之类的其余 4xx 也继续重试：只有明确的凭据/来源拒绝才停下', () => {
  assert.equal(isCredentialFailure({ status: 400, code: 'BAD_REQUEST' }), false)
})
