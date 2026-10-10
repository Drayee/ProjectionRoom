// 账号 / 会话 REST 的薄封装（端点与契约见 docs/ACCOUNTS.md §5 与 §10）。
//
// 三条纪律：
//   1. **这个模块不持有任何 token**。access token 的存放与生命周期唯一归
//      `stores/auth.ts`；这里只接受调用方传进来的 token，把它拼成 Authorization 头。
//      为什么要把这件事写死成纪律：规格 §5 的核心结论就是"access token 只准进内存 +
//      sessionStorage"，一旦在 api 层也留一份副本，迟早有人从这儿把它读到 localStorage 去。
//   2. refresh token 在 **HttpOnly Cookie**（`pr_refresh`，Path=/api/auth）里：
//      前端看不到、也不需要它。`refresh` / `logout` 一律靠浏览器自动带 Cookie，
//      所以 fetch 用 `credentials: 'same-origin'`（开发态的 Vite 5173 → Go 8080 是
//      same-site，Cookie 正常发送）。
//   3. **服务端文案原样透出**。错误体里的 message 是给用户看的最终文案，这里不改写、不归纳；
//      只有"根本没有服务端文案"的两种情形（连接不上、响应不是 JSON）才用本地兜底文案。
//      两者必须能被调用方区分（`offline` / `code`），否则"网络不可达"会被显示成"密码错误"。

const AUTH_BASE = '/api/auth'

/** 登录态里的用户信息（服务端契约：不含 passwordHash，前端也不假设它存在）。 */
export interface AuthUser {
  id: number
  username: string
  displayName: string
  /** 预留字段，本期不验证；服务端可能给 null，这里统一归一成空串。 */
  email: string
  /** user | admin；**只决定显示什么**，授权 100% 在服务端（规格不变量 I3）。 */
  role: string
  /** active | banned。 */
  status: string
  /**
   * 加入时间（RFC3339）。服务端 `usecase.Profile.createdAt` 一直在返回它，
   * 只是前端以前没接（用户详情页要显示"加入时间"才接上）。
   * 归一成字符串：缺失/类型不对一律空串，界面上显示"服务端未返回"而不是 `undefined`。
   */
  createdAt: string
}

/** register / login / refresh 三个端点的统一响应。 */
export interface AuthSession {
  user: AuthUser
  accessToken: string
  /** access token 有效期（秒）。 */
  expiresIn: number
}

export interface RegisterInput {
  username: string
  displayName: string
  password: string
}

export interface LoginInput {
  username: string
  password: string
}

export interface WsTicket {
  ticket: string
  expiresIn: number
}

/** 网络层兜底文案：这里是"请求根本没到服务端"，与"服务端拒绝了这次请求"必须分开。 */
export const AUTH_NETWORK_MESSAGE = '无法连接服务器（网络不可达）：请检查网络或稍后重试'

/** 账号接口的统一错误：比 Error 多带 status 与错误码，界面据此决定展示与恢复方式。 */
export class AuthApiError extends Error {
  /** HTTP 状态码；**0 表示请求没到服务端**（网络不可达 / 连接被断 / 被拦）。 */
  readonly status: number
  /** 服务端错误码（`AUTH_` 前缀）；本地兜底错误用 `AUTH_*` 自造码。 */
  readonly code: string
  /** true = 网络不可达（不是服务端拒绝）。判据④ 要的"区分网络问题与凭据问题"靠它。 */
  readonly offline: boolean

  constructor(init: { status: number; code: string; message: string; offline?: boolean }) {
    super(init.message)
    this.name = 'AuthApiError'
    this.status = init.status
    this.code = init.code
    this.offline = init.offline ?? false
  }
}

/** 把任意异常归一成 AuthApiError，调用方只需要处理一种错误类型。 */
export function toAuthError(err: unknown): AuthApiError {
  if (err instanceof AuthApiError) return err
  const message = err instanceof Error ? err.message : String(err)
  return new AuthApiError({ status: 0, code: 'AUTH_UNKNOWN', message, offline: true })
}

function offlineError(): AuthApiError {
  return new AuthApiError({ status: 0, code: 'AUTH_NETWORK', message: AUTH_NETWORK_MESSAGE, offline: true })
}

// ---------- 错误体解析 ----------

/**
 * 解析错误响应。
 *
 * 兼容两种形状（都不是猜：契约说嵌套，仓库既有的 REST 错误是扁平）：
 *   - 契约（本批账号接口）：`{"error":{"code":"AUTH_INVALID_CREDENTIALS","message":"…"}}`
 *   - 仓库既有 REST 形状：  `{"error":"房间不存在","code":"ROOM_NOT_FOUND"}`
 * 两种都命中时以 message 优先；**都没有**才退回本地兜底文案 —— 绝不把原始 JSON 整包
 * 当文案糊到界面上。
 */
function parseErrorBody(status: number, text: string): { code: string; message: string } {
  let body: unknown = null
  try {
    body = text === '' ? null : (JSON.parse(text) as unknown)
  } catch {
    body = null
  }

  const record = (body ?? {}) as Record<string, unknown>
  const nested = typeof record.error === 'object' && record.error !== null ? (record.error as Record<string, unknown>) : null

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

// ---------- 响应归一化 ----------

function asString(raw: unknown): string {
  return typeof raw === 'string' ? raw : ''
}

function normalizeUser(raw: unknown): AuthUser {
  const user = (raw ?? {}) as Partial<AuthUser>
  return {
    id: typeof user.id === 'number' ? user.id : 0,
    username: asString(user.username),
    displayName: asString(user.displayName),
    // email 允许为 null（预留字段）：归一成空串，界面就不用处理两种"空"。
    email: asString(user.email),
    role: asString(user.role) || 'user',
    status: asString(user.status) || 'active',
    // 服务端给的是 RFC3339；缺失时留空串（界面负责说"服务端未返回"，不在这里编时间）。
    createdAt: asString(user.createdAt),
  }
}

function normalizeSession(raw: unknown): AuthSession {
  const body = (raw ?? {}) as Partial<AuthSession>
  const accessToken = asString(body.accessToken)
  if (accessToken === '') {
    // 响应缺 token 是最危险的一类"半成功"：如果照收，界面会显示"已登录"而所有请求都 401。
    throw new AuthApiError({
      status: 0,
      code: 'AUTH_BAD_RESPONSE',
      message: '服务端响应缺少 accessToken（响应不完整）',
    })
  }
  return {
    user: normalizeUser(body.user),
    accessToken,
    expiresIn: typeof body.expiresIn === 'number' ? body.expiresIn : 0,
  }
}

// ---------- 网络原语 ----------

interface RequestOptions {
  method?: string
  body?: unknown
  /** 只在需要的端点上带（`me` / `ws-ticket`）。 */
  token?: string
}

async function request(path: string, options: RequestOptions = {}): Promise<unknown> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  const init: RequestInit = { method: options.method ?? 'GET', credentials: 'same-origin', headers }
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(options.body)
  }
  if (options.token) {
    headers.Authorization = `Bearer ${options.token}`
  }

  let resp: Response
  try {
    resp = await fetch(`${AUTH_BASE}/${path}`, init)
  } catch {
    // fetch 只在"请求没发出去/没有响应"时抛异常：这正是"网络不可达"，与 4xx/5xx 完全不同。
    throw offlineError()
  }

  if (!resp.ok) {
    const text = await resp.text().catch(() => '')
    const parsed = parseErrorBody(resp.status, text)
    throw new AuthApiError({ status: resp.status, code: parsed.code, message: parsed.message })
  }
  // 204（logout / logout-all）没有响应体，别去 JSON.parse 一个空串。
  if (resp.status === 204) {
    return null
  }
  const text = await resp.text().catch(() => '')
  if (text.trim() === '') {
    return null
  }
  try {
    return JSON.parse(text) as unknown
  } catch {
    throw new AuthApiError({
      status: resp.status,
      code: 'AUTH_BAD_RESPONSE',
      message: `服务端返回了无法解析的内容（HTTP ${resp.status}）`,
    })
  }
}

// ---------- 七个端点 ----------

/** POST /api/auth/register —— 注册并直接进入登录态（服务端同时下发 refresh Cookie）。 */
export async function register(input: RegisterInput): Promise<AuthSession> {
  return normalizeSession(await request('register', { method: 'POST', body: input }))
}

/** POST /api/auth/login */
export async function login(input: LoginInput): Promise<AuthSession> {
  return normalizeSession(await request('login', { method: 'POST', body: input }))
}

/**
 * POST /api/auth/refresh —— **无 body**，靠浏览器自动带上的 HttpOnly Cookie。
 *
 * 契约要点：服务端校验 Cookie 与库里的会话行 → 旧行置 revoked + 发新行（轮换）；
 * 收到已 revoked 的 token（重放）会让该用户**全部**会话被撤销 —— 所以这里的
 * "失败"不值得重试：重试只会把重放检测推得更远。
 */
export async function refresh(): Promise<AuthSession> {
  return normalizeSession(await request('refresh', { method: 'POST' }))
}

/** POST /api/auth/logout（204，无响应体）。撤销当前这条 refresh 会话。 */
export async function logout(): Promise<void> {
  await request('logout', { method: 'POST' })
}

/**
 * POST /api/auth/logout-all（204）—— 撤销该用户全部会话（服务端 `token_version+1`）。
 *
 * 本期没有界面入口（"退出所有设备"属设置页）；先按冻结契约把它封在这里，
 * 免得将来各处自己拼 URL、各自处理一次错误体。
 */
export async function logoutAll(): Promise<void> {
  await request('logout-all', { method: 'POST' })
}

/** GET /api/auth/me —— 校验 access token 并取回最新用户信息。 */
export async function me(token: string): Promise<AuthUser> {
  const body = (await request('me', { token })) as { user?: unknown } | null
  return normalizeUser(body?.user)
}

/**
 * POST /api/auth/ws-ticket —— 30 秒、单次使用的票据，用于拼进 `/ws?ticket=`。
 *
 * 目前无线索方（房间 WS 的票据绑定见 ACCOUNTS.md §6，客户端接线不在本次范围内）；
 * 同样先按契约封在这里，避免 URL 拼接散落各处。
 */
export async function wsTicket(token: string): Promise<WsTicket> {
  const body = (await request('ws-ticket', { method: 'POST', token })) as Partial<WsTicket> | null
  const ticket = asString(body?.ticket)
  if (ticket === '') {
    throw new AuthApiError({ status: 0, code: 'AUTH_BAD_RESPONSE', message: '服务端响应缺少 ticket' })
  }
  return { ticket, expiresIn: typeof body?.expiresIn === 'number' ? body.expiresIn : 0 }
}
