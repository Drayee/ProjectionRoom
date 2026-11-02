/**
 * 关注事件的轻提示文案（票 #67；决议 #53 §5、§7）。
 *
 * ## 为什么文案要单独一个模块
 *
 * 它同时被两个地方需要：`App.vue` 的 toast 与真机脚本的判据。把字符串写在
 * 组件模板里，脚本就只能靠"页面上有没有出现『关注』两个字"去猜；写在这里，
 * 脚本可以 import 同一份常量（见 `test/script/verify-follow-red-dot.mjs`）。
 *
 * ## 为什么只认两类事件
 *
 * 决议 #53 §7 明确：**只弹「有人关注了你」与「你们互关了」**。
 * 取关、被取关、拉黑、解除拉黑一律静默 —— 那些不是"发生了一件值得打断你的事"，
 * 而是"某人的关系变了"。给它们也弹提示会让这条通道变成噪声，
 * 而噪声的代价是**真有用的那一条也被无视**。
 *
 * 返回 `null` 而不是空串：调用方靠 `null` 判断"这条事件根本不该弹提示"。
 * 空串会让它渲染一个空的 toast（一个只有背景没有字的黑框）。
 */

/** 会弹轻提示的事件类型（决议 #53 §7 的全部范围）。 */
export const FOLLOW_NOTICE_TYPES = ['follow-created', 'mutual-follow'] as const

export type FollowNoticeType = (typeof FOLLOW_NOTICE_TYPES)[number]

/**
 * 把一条关注事件翻译成一句人话。
 *
 * @param type 服务端 `FriendEvent` 帧的 `type` 字段
 * @param displayName 对方的显示名（服务端事件里带）
 * @returns 可展示的文案；不认识的事件类型、或名字为空时返回 `null`
 */
export function followNoticeText(type: string, displayName: string): string | null {
  const name = (displayName ?? '').trim()
  // 名字为空时宁可什么都不弹：`" 关注了你"` 这种半句话比不弹更让人困惑，
  // 而"这条事件缺了名字"本身就是服务端契约出了问题，不该由界面掩盖。
  if (name === '') return null

  if (type === 'follow-created') return `${name} 关注了你`
  if (type === 'mutual-follow') return `${name} 和你互相关注，成为好友了`
  return null
}
