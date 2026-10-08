import type { Role } from '../types/protocol'

export interface StoredJoin {
  password: string
  role: Role
  displayName: string
  /**
   * 主播复位令牌（仅主播有；`POST /api/rooms` 的创建响应一次性下发）。
   *
   * 为什么跟 join 凭据存在一起：它和密码是同一类东西 —— "证明我是这个房间的主播"，
   * 生命周期也一样（跟着标签页会话走）。落 sessionStorage 意味着刷新页面仍在，
   * 关掉标签页即失效（令牌本来就只在下发时可见一次，丢了只能重开房间）。
   */
  hostToken?: string
}

/** 凭据存 sessionStorage：刷新页面能留在房间里，密码又不会进浏览历史。 */
const joinKey = (roomId: string) => `pr:join:${roomId.toUpperCase()}`

export function rememberJoin(roomId: string, value: StoredJoin): void {
  sessionStorage.setItem(joinKey(roomId), JSON.stringify(value))
}

/**
 * 只更新/补写主播复位令牌，其余字段原样保留。
 *
 * 为什么要单独一条：令牌有两条到达路径 —— 创建房间（HomeView）与**重建房间**
 * （store 的 rebuildRoom，房间被宽限期回收后重新 POST /api/rooms 会下发新令牌）。
 * 两条路径都必须写进去，而它们各自只关心令牌这一个字段。
 */
export function rememberHostToken(roomId: string, hostToken: string): void {
  if (!hostToken) return
  const current = readJoin(roomId)
  rememberJoin(roomId, {
    password: current?.password ?? '',
    role: current?.role ?? 'host',
    displayName: current?.displayName ?? '主播',
    hostToken,
  })
}

export function readJoin(roomId: string): StoredJoin | null {
  const raw = sessionStorage.getItem(joinKey(roomId))
  if (!raw) return null
  try {
    return JSON.parse(raw) as StoredJoin
  } catch {
    return null
  }
}
