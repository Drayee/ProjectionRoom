/**
 * 「按 userId 打开会话」的跳转约定（wayfinder #28 定义，`/messages` 票 #30 消费）。
 *
 * 为什么是查询参数而不是 `/messages/:userId` 路径参数：`docs/ACCOUNTS.md` §7.2 的历史接口
 * 就是 `GET /api/messages?peer=<userId>`。前端沿用同一个键名，将来"从 URL 恢复会话"与
 * "发一次历史请求"用的是同一个值，不必再定义第二套命名（两套命名一定会漂移）。
 *
 * **这是跨票契约**：好友页的「发消息」按它生成链接，消息页解析 `peer`。任一侧改名都会
 * 直接表现成"点了发消息却打不开对的人"，所以两边都从这里取常量。
 */
export const DM_PEER_QUERY = 'peer'

/** 打开与某个用户的会话。 */
export function conversationPath(userId: number): string {
  return `/messages?${DM_PEER_QUERY}=${encodeURIComponent(String(userId))}`
}

/**
 * 从 `/messages?peer=…` 解析对方 id。
 *
 * 非法值一律 `null`（不是 0、不是 NaN）：调用方据此渲染"没选会话"，
 * 而不是拿 `NaN` 去发一次 `?peer=NaN` 的请求。
 */
export function parseConversationPeer(raw: unknown): number | null {
  if (typeof raw === 'number') return Number.isInteger(raw) && raw > 0 ? raw : null
  if (typeof raw !== 'string' || raw.trim() === '') return null
  const value = Number(raw)
  return Number.isInteger(value) && value > 0 ? value : null
}
