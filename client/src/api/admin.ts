// 管理端 REST 端点（ACCOUNTS §8 / §10，二期 T4）。
//
// 一条纪律贯穿整个文件：**授权 100% 在服务端**。这里不做任何"我是 admin 所以能调"的
// 判断，前端缓存里的 role 只决定显示什么（一期不变量 I3）。因此：
//   - 所有请求都带 `redirectOnAuthFailure: false`（调用方传进来）：非 admin 的 403
//     必须在原地显示「无权限」，而把人拽去登录页会把这个结论说成"你的登录有问题"；
//   - 403 与 401 被分诊成两种界面结论，而不是同一条红色错误条。
//
// 响应形状（服务端 admin.go 已冻结）：
//   users   {items:[Profile], total, limit, offset}
//   rooms   {items:[adminRoomItem], total, limit, offset, search, public}
//   metrics 四个来源各自独立的键：connections / onlineUsers / rooms / writeQueue / logs
//   logs    {lines:[string], total, limit, offset}      —— 一律文本插值，绝不拼 HTML
//   audit   {items:[…], limit, offset}                  —— **没有 total**（见下）

import { requestJSON, query, type AuthedFetch } from './http'

// ---------- 用户 ----------

/** 服务端 `usecase.Profile` 的对外投影（**不含** passwordHash / password）。 */
export interface AdminUser {
  id: number
  username: string
  displayName: string
  email: string
  role: string
  status: string
  createdAt: string
  lastSeenAt: string
}

export interface AdminUserPage {
  items: AdminUser[]
  total: number
  limit: number
  offset: number
}

/** 管理端改用户：role / status 至少一个；reason 只进审计明细。 */
export interface AdminUserPatch {
  role?: string
  status?: string
  reason?: string
}

// ---------- 房间 ----------

/** 管理端房间行：含**未公开**的房间，并带上管理所需的字段（仍不含密码明文与成员明细）。 */
export interface AdminRoom {
  roomId: string
  /** 0 = 无房主（游客路径或测试直接建的房间）。 */
  ownerUserId: number
  title: string
  memberCount: number
  maxDepth: number
  isPublic: boolean
  hasPassword: boolean
  hostOnline: boolean
  hostOffline: boolean
  playingIndex: number
  /** true = 库里缺这一行元数据（要连 isPublic 一起看才能区分"未公开"与"缺行"）。 */
  metadataMissing: boolean
  /** 恒为 true（这一行至少来自内存）；保留给将来"只存在于库里的行"。 */
  inMemory: boolean
  closedAt: string
  lastSeenAt: string
}

export interface AdminRoomPage {
  items: AdminRoom[]
  total: number
  limit: number
  offset: number
}

// ---------- 指标 / 日志 / 审计 ----------

export interface AdminMetrics {
  connections?: number
  onlineUsers?: number
  rooms?: number
  writeQueue?: {
    pending?: number
    queued?: number
    dropped?: number
    succeeded?: number
    failed?: number
    lastLatencyMs?: number
  }
  logs?: { kept?: number; total?: number; dropped?: number }
}

export interface AdminLogPage {
  /** 服务端日志行（**已经是给人看的文本**），顺序：新 → 旧。 */
  lines: string[]
  total: number
  limit: number
  offset: number
}

/** 审计行：字段白名单，`detail` 是**文本**（jsonb 的原文），不是结构。 */
export interface AdminAuditRow {
  id: number
  actorId: number
  action: string
  targetType: string
  targetId: string
  detail: string
  ip: string
  createdAt: string
}

/** 审计**没有 total**：服务端刻意不为了翻页去做一次全表 COUNT。 */
export interface AdminAuditPage {
  items: AdminAuditRow[]
  limit: number
  offset: number
}

// ---------- 归一化（缺字段给安全默认值，界面不必处理 undefined）----------

function asString(raw: unknown): string {
  return typeof raw === 'string' ? raw : ''
}

function asNumber(raw: unknown): number {
  return typeof raw === 'number' && Number.isFinite(raw) ? raw : 0
}

function asBool(raw: unknown): boolean {
  return raw === true
}

function normalizeUser(raw: unknown): AdminUser {
  const u = (raw ?? {}) as Record<string, unknown>
  return {
    id: asNumber(u.id),
    username: asString(u.username),
    displayName: asString(u.displayName),
    email: asString(u.email),
    role: asString(u.role) || 'user',
    status: asString(u.status) || 'active',
    createdAt: asString(u.createdAt),
    lastSeenAt: asString(u.lastSeenAt),
  }
}

function normalizeRoom(raw: unknown): AdminRoom {
  const r = (raw ?? {}) as Record<string, unknown>
  return {
    roomId: asString(r.roomId),
    ownerUserId: asNumber(r.ownerUserId),
    title: asString(r.title),
    memberCount: asNumber(r.memberCount),
    maxDepth: asNumber(r.maxDepth),
    isPublic: asBool(r.isPublic),
    hasPassword: asBool(r.hasPassword),
    hostOnline: asBool(r.hostOnline),
    hostOffline: asBool(r.hostOffline),
    playingIndex: asNumber(r.playingIndex),
    metadataMissing: asBool(r.metadataMissing),
    inMemory: asBool(r.inMemory),
    closedAt: asString(r.closedAt),
    lastSeenAt: asString(r.lastSeenAt),
  }
}

function normalizeAudit(raw: unknown): AdminAuditRow {
  const a = (raw ?? {}) as Record<string, unknown>
  return {
    id: asNumber(a.id),
    actorId: asNumber(a.actorId),
    action: asString(a.action),
    targetType: asString(a.targetType),
    targetId: asString(a.targetId),
    detail: asString(a.detail),
    ip: asString(a.ip),
    createdAt: asString(a.createdAt),
  }
}

function itemsOf(body: Record<string, unknown> | null): unknown[] {
  return Array.isArray(body?.items) ? (body?.items as unknown[]) : []
}

// ---------- 端点 ----------

/** GET /api/admin/users?search=&limit=&offset= */
export async function listUsers(
  authedFetch: AuthedFetch,
  params: { search?: string; limit?: number; offset?: number },
): Promise<AdminUserPage> {
  const path = `/api/admin/users${query({
    search: params.search,
    limit: params.limit,
    offset: params.offset,
  })}`
  const body = await requestJSON<Record<string, unknown>>(authedFetch, path, {
    redirectOnAuthFailure: false,
  })
  return {
    items: itemsOf(body).map(normalizeUser),
    total: asNumber(body?.total),
    limit: asNumber(body?.limit) || (params.limit ?? 0),
    offset: asNumber(body?.offset) || (params.offset ?? 0),
  }
}

/** PATCH /api/admin/users/:id —— 改角色 / 封禁解封（危险操作，调用方必须二次确认）。 */
export async function patchUser(
  authedFetch: AuthedFetch,
  userId: number,
  patch: AdminUserPatch,
): Promise<void> {
  await requestJSON<Record<string, unknown>>(authedFetch, `/api/admin/users/${userId}`, {
    method: 'PATCH',
    body: patch,
    redirectOnAuthFailure: false,
  })
}

/**
 * GET /api/admin/rooms?search=&public=&limit=&offset=
 *
 * `public` 只接受 true / false / 留空（服务端对非法值返回 400，不静默当空处理）。
 */
export async function listRooms(
  authedFetch: AuthedFetch,
  params: { search?: string; public?: string; limit?: number; offset?: number },
): Promise<AdminRoomPage> {
  const path = `/api/admin/rooms${query({
    search: params.search,
    public: params.public,
    limit: params.limit,
    offset: params.offset,
  })}`
  const body = await requestJSON<Record<string, unknown>>(authedFetch, path, {
    redirectOnAuthFailure: false,
  })
  return {
    items: itemsOf(body).map(normalizeRoom),
    total: asNumber(body?.total),
    limit: asNumber(body?.limit) || (params.limit ?? 0),
    offset: asNumber(body?.offset) || (params.offset ?? 0),
  }
}

/** POST /api/admin/rooms/:id/close —— 强关（房内连接会被服务端断开）。 */
export async function closeRoom(authedFetch: AuthedFetch, roomId: string): Promise<void> {
  await requestJSON<Record<string, unknown>>(
    authedFetch,
    `/api/admin/rooms/${encodeURIComponent(roomId)}/close`,
    { method: 'POST', redirectOnAuthFailure: false },
  )
}

/** POST /api/admin/rooms/:id/unpublish —— 下架（幂等）。 */
export async function unpublishRoom(authedFetch: AuthedFetch, roomId: string): Promise<void> {
  await requestJSON<Record<string, unknown>>(
    authedFetch,
    `/api/admin/rooms/${encodeURIComponent(roomId)}/unpublish`,
    { method: 'POST', redirectOnAuthFailure: false },
  )
}

/** GET /api/admin/metrics */
export async function fetchMetrics(authedFetch: AuthedFetch): Promise<AdminMetrics> {
  const body = await requestJSON<AdminMetrics>(authedFetch, '/api/admin/metrics', {
    redirectOnAuthFailure: false,
  })
  return body ?? {}
}

/** GET /api/admin/logs?limit=&offset= */
export async function fetchLogs(
  authedFetch: AuthedFetch,
  params: { limit?: number; offset?: number },
): Promise<AdminLogPage> {
  const path = `/api/admin/logs${query({ limit: params.limit, offset: params.offset })}`
  const body = await requestJSON<Record<string, unknown>>(authedFetch, path, {
    redirectOnAuthFailure: false,
  })
  const rawLines = Array.isArray(body?.lines) ? (body?.lines as unknown[]) : []
  return {
    // 日志行**只保留字符串**：非字符串元素一律丢掉，而不是 String() 一遍 ——
    // 那会把一个结构化的对象变成 "[object Object]" 悄悄显示给管理员。
    lines: rawLines.filter((line): line is string => typeof line === 'string'),
    total: asNumber(body?.total),
    limit: asNumber(body?.limit) || (params.limit ?? 0),
    offset: asNumber(body?.offset) || (params.offset ?? 0),
  }
}

/** GET /api/admin/audit?limit=&offset=（**没有 total**，用"本页是否满页"判断还有没有更多） */
export async function fetchAudit(
  authedFetch: AuthedFetch,
  params: { limit?: number; offset?: number },
): Promise<AdminAuditPage> {
  const path = `/api/admin/audit${query({ limit: params.limit, offset: params.offset })}`
  const body = await requestJSON<Record<string, unknown>>(authedFetch, path, {
    redirectOnAuthFailure: false,
  })
  return {
    items: itemsOf(body).map(normalizeAudit),
    limit: asNumber(body?.limit) || (params.limit ?? 0),
    offset: asNumber(body?.offset) || (params.offset ?? 0),
  }
}
