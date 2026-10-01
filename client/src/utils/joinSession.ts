import type { Role } from '../types/protocol'

export interface StoredJoin {
  password: string
  role: Role
  displayName: string
}

/** 凭据存 sessionStorage：刷新页面能留在房间里，密码又不会进浏览历史。 */
const joinKey = (roomId: string) => `pr:join:${roomId.toUpperCase()}`

export function rememberJoin(roomId: string, value: StoredJoin): void {
  sessionStorage.setItem(joinKey(roomId), JSON.stringify(value))
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
