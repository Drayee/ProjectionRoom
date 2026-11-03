/**
 * "用户级实时通道该不该继续重连"的判据（2026-11-02 线上事故的沉淀）。
 *
 * # 为什么要单独一个模块
 *
 * 这条判据错一格的后果两端都很响：
 *   - 判太松（把 403 当瞬时故障）→ 控制台被失败握手刷屏，且每个周期白烧一次取票。
 *     线上那次就是这样：几十条 `WebSocket connection to 'ws://…/ws/user?ticket=…'
 *     failed: …403`，每条前面还跟着一发 `api/auth/ws-ticket`。
 *   - 判太紧（把网络抖动当凭据失效）→ 一次断网就永久失去实时通道，用户不刷新页面
 *     就再也收不到提示与红点。
 *
 * 两端都不能靠"看代码觉得对"。抽成**零依赖纯函数**之后，它进得了 `client/test/` 的
 * 表驱动用例（那一族用 Node 原生类型擦除，要求被测模块自己不带 import ——
 * 这也是为什么判据不写在 `api/userChannel.ts` 里：那里 import 了 `./auth`）。
 *
 * # 判据内容
 *
 *   - `AUTH_NO_TOKEN`：本机没有 access token。不该重连（未登录时空转只会刷屏）。
 *   - 401：服务端明确拒了这份凭据。新 token 才有意义 → 挂起。
 *   - 403：这次的 403 来自 Origin 白名单没配这个域名（`/ws/user` 的握手 403）。
 *     配置不改、结果不变 → 挂起。
 *   - 其余（网络不可达、5xx、429、404）→ 会自己好，退避重试。
 */

/** 判据需要的最小信息：从 `AuthApiError` 上取的两个字段。 */
export interface ChannelErrorShape {
  /** HTTP 状态码；0 表示请求没到服务端。 */
  status: number
  /** 服务端错误码，或本地自造码（`AUTH_NO_TOKEN` 等）。 */
  code: string
}

/**
 * 这次取票失败是不是"凭据/配置问题"（应当挂起等新凭据，而不是退避重连）。
 *
 * 入参刻意只收 `{ status, code }` 而不是 `AuthApiError`：纯函数意味着它不依赖
 * `api/auth.ts`，于是单测可以直接跑；而 `AuthApiError` 天然满足这个形状。
 */
export function isCredentialFailure(err: ChannelErrorShape): boolean {
  if (err.code === 'AUTH_NO_TOKEN') return true
  return err.status === 401 || err.status === 403
}
