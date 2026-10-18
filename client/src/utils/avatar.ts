/**
 * 头像首字母 + 稳定取色。
 *
 * 从 `api/real/posts.ts` 的私有 `avatarOf` 提出来（wayfinder #28）：好友域要同一套派生，
 * 而"服务端契约不掺显示层"这条纪律意味着派生必须留在前端（#26 的结论）。
 *
 * **逐字等价迁移**：同一个 `id` 与 `displayName` 得到同一个 `{avatarChar, avatarColor}`
 * （`name.slice(0, 1)` + 五色板按 `Math.abs(id) % 5` 取）。改这里的色板或取模方式会
 * 让帖子域的头像颜色跟着变 —— 那是一次视觉回归，不是无害重构。
 */
export const AVATAR_PALETTE = ['#00a1d6', '#fb7299', '#8b7bf0', '#3ddc97', '#f5c76a'] as const

export function avatarOf(id: number, displayName: string): { avatarChar: string; avatarColor: string } {
  const name = displayName || '?'
  return {
    avatarChar: name.slice(0, 1),
    avatarColor: AVATAR_PALETTE[Math.abs(id) % AVATAR_PALETTE.length],
  }
}
