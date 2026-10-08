// 账号体系之外那几组 REST 端点的公共 HTTP 原语（公开房列表 / 房主元数据 / 管理端）。
//
// 为什么单独抽一层，而不是像一期那样直接把 fetch 散在界面里：
//   1. **错误体只解析一次**。二期新增的端点是混合形状 —— 新写的用扁平
//      `{"error":"只有房主可以修改房间信息","code":"FORBIDDEN"}`，而冻结契约与
//      一期账号接口写的是嵌套 `{"error":{"code":…,"message":…}}`。同一个解析器
//      同时认这两种形状，界面就永远只处理"一条文案 + 一个状态码"，不必各自猜形状。
//   2. **错误码必须能被判定**，不能靠正则去匹配文案。管理端要区分
//      403 FORBIDDEN（显示「无权限」）与 401（会话失效），分离出 `status` / `code`
//      之后这件事是一行判断，而不是一句脆弱的字符串匹配。
//   3. **认证一律走 auth.authedFetch**（401 自动刷新一次再重试一次）。所以这里
//      只接受一个已经带有 Authorization 的 fetch 实现，绝不自己持有 token ——
//      access token 的存放与生命周期唯一归 `stores/auth.ts`（一期定下的纪律）。
//
// 服务端文案**原样透出**：错误体里的 message 就是给用户看的最终表述，这里不改写、
// 不归纳；只有"根本没有服务端文案"时（网络不可达 / 响应不是 JSON）才用本地兜底文案。

/** 网络层兜底文案：这里是"请求根本没到服务端"，与"服务端拒绝了这次请求"必须分开。 */
export const NETWORK_MESSAGE = '无法连接服务器（网络不可达）：请检查网络或稍后重试'

/**
 * 带 access token 的 fetch（签名与 `auth.authedFetch` 一致，便于直接传引用）。
 *
 * 注意这里**没有**把它限制成某个具体来源：房主面板与管理端都直接传
 * `auth.authedFetch`，因此"401 自动刷新一次"这条语义只有一处实现。
 */
export type AuthedFetch = (
  input: string,
  init?: RequestInit,
  options?: { redirectOnAuthFailure?: boolean },
) => Promise<Response>

/**
 * 二期接口的统一错误。
 *
 * `status === 0` 表示请求没到服务端（网络不可达 / 连接被断 / 被浏览器拦下）；
 * `code` 是服务端错误码（本层自造的兜底码一律带 `HTTP_` 前缀，一眼能区分）。
 */
export class HttpError extends Error {
  readonly status: number
  readonly code: string
  /** true = 网络不可达（不是服务端拒绝）。 */
  readonly offline: boolean

  constructor(init: { status: number; code: string; message: string; offline?: boolean }) {
    super(init.message)
    this.name = 'HttpError'
    this.status = init.status
    this.code = init.code
    this.offline = init.offline ?? false
  }

  /** 已认证但权限不足（管理端最常见的失败：非 admin）。 */
  get forbidden(): boolean {
    return this.status === 403
  }

  /** 会话失效（401 且刷新也失败）。 */
  get unauthorized(): boolean {
    return this.status === 401
  }
}

/** 把任意异常归一成 HttpError，调用方只需要处理一种错误类型。 */
export function toHttpError(err: unknown): HttpError {
  if (err instanceof HttpError) return err
  const message = err instanceof Error ? err.message : String(err)
  // 走到这里说明异常不是本层产生的（多半是 fetch 自己抛的）：按网络不可达处理。
  return new HttpError({ status: 0, code: 'HTTP_NETWORK', message, offline: true })
}

function offlineError(): HttpError {
  return new HttpError({ status: 0, code: 'HTTP_NETWORK', message: NETWORK_MESSAGE, offline: true })
}

/**
 * 解析错误响应，兼容两种形状：
 *   - 嵌套（冻结契约）：`{"error":{"code":"…","message":"…"}}`
 *   - 扁平（仓库既有 REST）：`{"error":"房间不存在","code":"ROOM_NOT_FOUND"}`
 * 两种都命中时以 message 优先；**都没有**才退回本地兜底文案 —— 绝不把原始 JSON
 * 整包当文案糊到界面上。
 */
function parseErrorBody(status: number, text: string): { code: string; message: string } {
  let body: unknown = null
  try {
    body = text === '' ? null : (JSON.parse(text) as unknown)
  } catch {
    body = null
  }

  const record = (body ?? {}) as Record<string, unknown>
  const nested =
    typeof record.error === 'object' && record.error !== null
      ? (record.error as Record<string, unknown>)
      : null

  const nestedMessage = nested && typeof nested.message === 'string' ? nested.message : ''
  const flatMessage = typeof record.error === 'string' ? record.error : ''
  const topMessage = typeof record.message === 'string' ? record.message : ''
  const message = nestedMessage || flatMessage || topMessage

  const nestedCode = nested && typeof nested.code === 'string' ? nested.code : ''
  const flatCode = typeof record.code === 'string' ? record.code : ''

  return {
    code: nestedCode || flatCode || `HTTP_${status}`,
    message: message.trim() !== '' ? message : `请求失败（HTTP ${status}）`,
  }
}

/** 拼查询串：空值（空串 / undefined / null）整条丢掉，避免出现 `?search=&public=` 这种自造参数。 */
export function query(params: Record<string, string | number | boolean | undefined | null>): string {
  const parts: string[] = []
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`)
  }
  return parts.length === 0 ? '' : `?${parts.join('&')}`
}

interface RequestOptions {
  method?: string
  body?: unknown
  /**
   * 会话失效时是否把人送去登录页（默认 true，与 authedFetch 的语义一致）。
   * 管理端传 false：非 admin 的 401/403 必须在**原地**说明白，把人拽去登录页
   * 只会让"我明明是登录的，为什么被踢出去"变成一个无法回答的问题。
   */
  redirectOnAuthFailure?: boolean
}

/**
 * 发一次带认证的 JSON 请求，返回解析后的响应体。
 *
 * `204`（强关 / 下架）没有响应体，返回 `null`；成功但响应不是 JSON 时也返回 `null`
 * 而不是抛错 —— 成功路径上"没有可读内容"不是失败。
 */
export async function requestJSON<T>(
  authedFetch: AuthedFetch,
  path: string,
  options: RequestOptions = {},
): Promise<T | null> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  const init: RequestInit = { method: options.method ?? 'GET', headers }
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(options.body)
  }

  let resp: Response
  try {
    resp = await authedFetch(path, init, {
      redirectOnAuthFailure: options.redirectOnAuthFailure !== false,
    })
  } catch (err) {
    // authedFetch 在"401 且刷新失败"时会抛它自己的 AuthApiError（并且可能已经跳了登录页）：
    // 这里归一成同一种错误类型，界面只需要认识 HttpError。
    throw toHttpError(err)
  }

  if (resp.status === 204) {
    return null
  }

  const text = await resp.text().catch(() => '')
  if (!resp.ok) {
    const parsed = parseErrorBody(resp.status, text)
    throw new HttpError({ status: resp.status, code: parsed.code, message: parsed.message })
  }
  if (text.trim() === '') {
    return null
  }
  try {
    return JSON.parse(text) as T
  } catch {
    // 服务端返回了非 JSON 的成功响应（例如反向代理插了一页 HTML）：
    // 这不是"网络不可达"，而是一次**服务端异常**，必须如实说出来。
    throw new HttpError({
      status: resp.status,
      code: 'HTTP_BAD_RESPONSE',
      message: `服务端返回了无法解析的内容（HTTP ${resp.status}）`,
    })
  }
}

/** 网络不可达时用于本地兜底（供需要区分"离线"与"被拒绝"的界面复用）。 */
export function isOffline(err: unknown): boolean {
  return err instanceof HttpError && err.offline === true
}
