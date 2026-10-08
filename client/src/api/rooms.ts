// 房主自有房间的元数据端点（ACCOUNTS §6 的 `/api/rooms/:id/meta`，二期 T3）。
//
// 这里做两件事：把「标题 / 公开性」读到本地、把它们写回服务端。房间的实时链路（信令、
// WebRTC、分片）与它无关 —— 改标题不会踢人、不会重建房间，也不会让观众掉线。
//
// 读写两个端点的**字段命名必须一致**（`roomId/title/isPublic/hasPassword`）：
// 读回来的对象可以直接喂给写操作之后的本地同步逻辑，不需要"两个形状之间搬字段"这层
// 容易漂移的适配（这层适配一旦漂移，症状是"刷新后开关莫名其妙变回默认"）。
//
// GET /api/rooms/:id/meta（需登录，**仅房主本人**）
//   200 {roomId,title,isPublic,hasPassword}
//   401 未登录；404 元数据不存在**或调用者不是房主**（同一个码 —— 避免存在性探测）
// PATCH /api/rooms/:id/meta（需登录，**仅房主本人**）
//   200 {roomId,title,isPublic}
//   400 两项都没传 / 不是合法 JSON / 标题超过 60 个字符
//   401 未登录；403 房间存在但不是你的；404 房间不存在或已被销毁；503 账号能力未启用

import { requestJSON, type AuthedFetch } from './http'

/** 标题的长度上限（**以服务端为准**，前端只做提交前的提示）。 */
export const ROOM_TITLE_MAX = 60

/** PATCH 的请求体：只带要改的字段。两项都不带会被服务端判 400。 */
export interface RoomMetaPatch {
  /**
   * 新标题。
   *
   * **允许空串**：服务端 schema 里 `title` 的默认值就是空串，所以"清空标题"是一个
   * 合法动作（列表里会显示成「未命名房间」）。读接口上线之后，空串不再有歧义 ——
   * 界面已经能从服务端读回真实值，不需要再靠"拒绝空串"来防止误清空。
   */
  title?: string
  isPublic?: boolean
}

/** GET / PATCH 共用的元数据形状（服务端两个端点字段一致）。 */
export interface RoomMeta {
  roomId: string
  title: string
  isPublic: boolean
}

/** GET 的响应：比 PATCH 多一个 hasPassword（服务端已有的事实，顺手给出，不必再查）。 */
export interface RoomMetaRead extends RoomMeta {
  hasPassword: boolean
}

function asString(raw: unknown): string {
  return typeof raw === 'string' ? raw : ''
}

function normalizeMeta(raw: unknown, fallbackRoomId: string): RoomMetaRead {
  const body = (raw ?? {}) as Record<string, unknown>
  return {
    // 房间码以**服务端回显**为准，缺失时退回请求里那个（大写归一）。
    roomId: asString(body.roomId) || fallbackRoomId.trim().toUpperCase(),
    title: asString(body.title),
    isPublic: body.isPublic === true,
    hasPassword: body.hasPassword === true,
  }
}

function metaPath(roomId: string): string {
  return `/api/rooms/${encodeURIComponent(roomId.trim().toUpperCase())}/meta`
}

/**
 * 读房间元数据（房主专用）。
 *
 * 失败**会抛**：404 是有意义的结论（不是房主 / 元数据不存在），调用方必须能把它与
 * "网络抖动"区分开 —— 两者的界面后果完全不同（收起控件 vs 原地提示）。
 */
export async function fetchRoomMeta(authedFetch: AuthedFetch, roomId: string): Promise<RoomMetaRead> {
  const body = await requestJSON<Record<string, unknown>>(authedFetch, metaPath(roomId))
  return normalizeMeta(body, roomId)
}

/**
 * 改房间的标题 / 公开性。
 *
 * 响应形状是 `{roomId,title,isPublic}`（**不是** 204）：界面因此可以拿回显的终态
 * 直接同步本地状态，不需要改完再拉一次。
 */
export async function patchRoomMeta(
  authedFetch: AuthedFetch,
  roomId: string,
  patch: RoomMetaPatch,
): Promise<RoomMeta> {
  const body = await requestJSON<Record<string, unknown>>(authedFetch, metaPath(roomId), {
    method: 'PATCH',
    body: patch,
  })
  return normalizeMeta(body, roomId)
}
