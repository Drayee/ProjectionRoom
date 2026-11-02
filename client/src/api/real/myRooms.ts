/**
 * 「我的放映室」· 真实 HTTP 实现（票 #70；服务端由 `internal/handler/myrooms.go` 提供）。
 *
 * 端点：`GET /api/rooms/mine?limit&offset`（需登录，只回自己的房）
 *
 * 三条判据在这一层把住：
 *
 *  1. **整域路由没注册**（老实例 / 账号能力关闭）→ 404 → `CapabilityUnavailable`，
 *     页面整块换「放映室分区尚未上线」空态。它与"我确实没开过房"必须能分开：
 *     后者是合法状态，要显示引导。
 *  2. 前端契约是 `page`（1 起），服务端是 `offset` —— 换算只发生在这一层
 *     （与 `api/real/posts.ts` 同一条纪律）。
 *  3. 逐字段归一：缺字段/类型不对时给安全默认值，界面因此不必处理 `undefined`。
 */
import { useAuthStore } from '../../stores/auth'
import { query, requestJSON } from '../http'
import { toCapabilityUnavailable } from '../mode'
import type { MyRoomItem, MyRoomList } from '../../types/rooms'

/** 单页条数：与服务端 `myRoomsDefaultLimit`（20）一致。 */
const PAGE_SIZE = 20

/** 取认证 fetch：延迟到调用时，模块加载期 store 可能还没装配。 */
function fetcher() {
  return useAuthStore().authedFetch
}

/** 服务端房间行的最小形状（只声明本文件用到的键）。 */
interface ServerMyRoomItem {
  roomId?: unknown
  title?: unknown
  isPublic?: unknown
  isClosed?: unknown
  hasPassword?: unknown
  createdAt?: unknown
  lastSeenAt?: unknown
}

function asString(raw: unknown): string {
  return typeof raw === 'string' ? raw : ''
}

function toItem(raw: ServerMyRoomItem): MyRoomItem {
  return {
    roomId: asString(raw?.roomId),
    title: asString(raw?.title),
    // 布尔只认 `=== true`：服务端漏字段时退成 false（"不公开/未关闭"），
    // 而不是把 undefined 当真值 —— 那会让界面给一间已关闭的房画上"在用"。
    isPublic: raw?.isPublic === true,
    isClosed: raw?.isClosed === true,
    hasPassword: raw?.hasPassword === true,
    createdAt: asString(raw?.createdAt),
    lastSeenAt: asString(raw?.lastSeenAt),
  }
}

export async function listMyRooms(
  options: { page?: number; pageSize?: number } = {},
): Promise<MyRoomList> {
  try {
    const pageSize = options.pageSize && options.pageSize > 0 ? options.pageSize : PAGE_SIZE
    const page = options.page && options.page > 0 ? options.page : 1
    const offset = (page - 1) * pageSize
    const body = await requestJSON<{ items?: ServerMyRoomItem[]; hasMore?: boolean }>(
      fetcher(),
      `/api/rooms/mine${query({ limit: pageSize, offset })}`,
    )
    return {
      items: (body?.items ?? []).map(toItem),
      hasMore: body?.hasMore === true,
    }
  } catch (err) {
    throw toCapabilityUnavailable('rooms', err)
  }
}
