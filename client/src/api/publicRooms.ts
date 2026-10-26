// 公开房列表端点（ACCOUNTS §6 / §10 的 GET /api/public-rooms，二期 T2）。
//
// 这个端点**免登录**，因此调用方仍然是 `auth.authedFetch`：它只是"有 token 就带上"，
// 没有 token 时行为与裸 fetch 完全一致（房主用它顺手看列表也不会退化成匿名请求）。
//
// 服务端返回的是一个**字段白名单**（没有密码、成员明细、邮箱、角色、状态）。
// 前端的类型只声明白名单里的那些字段：多声明一个字段就等于把"服务端将来漏给"当成承诺。

import { query, requestJSON, type AuthedFetch } from './http'

/** 列表里的一行。字段与 `publicRoomItem` 一一对应。 */
export interface PublicRoom {
  roomId: string
  /** 标题可以为空（房主没起名，或元数据缺行时的降级）：界面负责显示「未命名房间」。 */
  title: string
  /** 房主昵称；元数据缺行时是空串（界面不再猜，直接不显示这一项）。 */
  ownerName: string
  memberCount: number
  /** 主播在线（房间里真的有 host 成员）。 */
  hostOnline: boolean
  /** 主播已断线、房间处于离线宽限期：房间仍然可进（观众进去等主播回来）。 */
  hostOffline: boolean
  /** 有密码：列表不会给出密码本身，只给这一个标记。 */
  hasPassword: boolean
  /**
   * 运营标记（wayfinder #35 的 `rooms_meta.is_official`）。
   *
   * 普通列表也会带上它（服务端恒返回该键，`false` 也返回）：于是同一个房间里
   * 「是不是官方」只有一个来源，官方区（`?official=true`）与 `/rooms` 都读它，
   * 前端不必用"出现在官方列表里"反推。
   */
  isOfficial: boolean
}

export interface PublicRoomPage {
  items: PublicRoom[]
  total: number
  /** 服务端**生效值**回显（自己传的 9999 被夹到了多少由它回答）。 */
  limit: number
  offset: number
  /**
   * 服务端回显的 `official` 筛选值（wayfinder #35 起才有）。
   *
   * 它是**能力探测点**：老服务端不认这个参数、会当没传，于是回包里没有这个键，
   * `official` 为 false。官方页据此判断"服务端还没上线官方区"，而不是把整份
   * 公开房列表当成官方房渲染出来（那是最坏的一种撒谎：把普通房标成官方推荐）。
   */
  official: boolean
}

/** 单页条数。20 与计划 T5 的约定一致，也是服务端默认值。 */
export const PUBLIC_ROOMS_PAGE_SIZE = 20
function asString(raw: unknown): string {
  return typeof raw === 'string' ? raw : ''
}

function asBool(raw: unknown): boolean {
  return raw === true
}

function asNumber(raw: unknown): number {
  return typeof raw === 'number' && Number.isFinite(raw) ? raw : 0
}

/** 逐字段归一：缺字段/类型不对时给出安全默认值，界面因此不必处理 `undefined`。 */
function normalizeItem(raw: unknown): PublicRoom {
  const item = (raw ?? {}) as Record<string, unknown>
  return {
    roomId: asString(item.roomId),
    title: asString(item.title),
    ownerName: asString(item.ownerName),
    memberCount: asNumber(item.memberCount),
    hostOnline: asBool(item.hostOnline),
    hostOffline: asBool(item.hostOffline),
    hasPassword: asBool(item.hasPassword),
    isOfficial: asBool(item.isOfficial),
  }
}

/**
 * 拉一页公开房。
 *
 * 网络不可达时的兜底文案在 `http.ts` 里统一给出（`HTTP_NETWORK`），
 * 因此这里不需要自己再包一层 catch：界面拿到的 message 已经是最终文案。
 */
export async function fetchPublicRooms(
  authedFetch: AuthedFetch,
  options: { limit?: number; offset?: number; official?: boolean } = {},
): Promise<PublicRoomPage> {
  const path = `/api/public-rooms${query({
    limit: options.limit ?? PUBLIC_ROOMS_PAGE_SIZE,
    offset: options.offset ?? 0,
    // 只有调用方**显式**要官方区时才拼这个参数：普通列表不该因为传了 false
    // 而在 URL 里留下 `official=false`（服务端两种都认，但少一个参数少一个出错面）。
    official: options.official === undefined ? undefined : String(options.official),
  })}`
  const body = await requestJSON<Record<string, unknown>>(authedFetch, path)
  const rawItems = Array.isArray(body?.items) ? (body?.items as unknown[]) : []
  return {
    items: rawItems.map(normalizeItem),
    total: asNumber(body?.total),
    limit: asNumber(body?.limit) || (options.limit ?? PUBLIC_ROOMS_PAGE_SIZE),
    offset: asNumber(body?.offset) || (options.offset ?? 0),
    // 老服务端没有这个键 → false（官方页据此判"官方区还没上线"，见类型说明）。
    official: asBool(body?.official),
  }
}

/** 给界面用的空列表兜底文案（与 store 里那一份共用同一个常量，避免两处措辞漂移）。 */
export const PUBLIC_ROOMS_EMPTY_TEXT = '还没有公开的房间'