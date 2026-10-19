/**
 * 私聊域门面（唯一被页面 import 的出口）。wayfinder #30。
 *
 * 形状（对着 #31 冻结的服务端契约定，页面只认这一份）：
 *   listConversations()            → ConversationEntry[]      （会话列表，按最近消息倒序）
 *   listHistory({peerId, before})  → HistoryPage              （历史一页，最新在前）
 *   send({recipientId, body})      → DirectMessage            （服务端权威行）
 *   markRead({peerId, upTo})       → number                   （本次真正标记的行数）
 *
 * **没有 `unreadTotal()`**：未读数的唯一来源是会话列表的合计（由页面写进
 * `stores/auth.ts` 的 `setUnread`）。多开一个"取未读"的入口就等于多一个写入点，
 * 而两个写入点会互相覆盖 —— 第一次真机验收就是这么挂的（mock 的假消息 + 真后端的
 * `/me` 回的 0，顶栏角标时有时无）。`/me` 的 `unread` 只在 real 域被接受，见
 * `stores/auth.ts` 的 `applyServerUnread`。
 *
 * 实现由 `api/mode.ts` 的 `VITE_MOCK_MESSAGES` 在**模块加载时**二选一：默认 mock
 * （`api/mock/messages.ts`），配成 `0`/`false`/`off`/`no` 时用真实实现
 * （`api/real/messages.ts`）。页面不需要知道自己在跟谁说话。
 *
 * 两个刻意的契约决定：
 *
 *  1. **`send` 返回权威行，不是 void**。与好友域的写操作相反（那里 `void` + 写完重拉），
 *     因为消息要立刻 append 到列表里，而"自己编一个临时 id"会与真实 id 撞上 ——
 *     撞上之后 `markRead({upTo})` 就会把别人刚发来的消息也标成已读。这是 #31 的取舍。
 *  2. **`listHistory` 不做 reverse**。服务端顺序是 id DESC（最新在前），页面渲染时自己
 *     倒一次。"往上翻"要翻的边界（`before` = 当前最旧那条的 id）因此只有一处定义。
 */
import { isMock } from './mode'
import * as mock from './mock/messages'
import * as real from './real/messages'

/**
 * 两份实现形状必须一致 —— 类型标注就是这条契约的编译期检查点。
 * 任何一边签名漂移，这里立刻报错，而不是等到页面在运行期炸。
 */
type MessagesApi = {
  listConversations(): Promise<import('../types/messages').ConversationEntry[]>
  listHistory(input: import('../types/messages').ListHistoryInput): Promise<import('../types/messages').HistoryPage>
  send(input: import('../types/messages').SendMessageInput): Promise<import('../types/messages').DirectMessage>
  markRead(input: import('../types/messages').MarkReadInput): Promise<number>
}

const impl: MessagesApi = isMock('messages') ? mock : real

export const listConversations = impl.listConversations
export const listHistory = impl.listHistory
export const send = impl.send
export const markRead = impl.markRead

/** 正文上限：与 `store.MaxMessageBodyLen` 同值（§7.2 ≤ 2000）。页面用它做本地预检。 */
export const MAX_MESSAGE_BODY = 2000

/** 类型再导出：页面从门面拿类型，不必知道 `types/messages.ts` 的位置。 */
export type {
  ConversationEntry,
  DirectMessage,
  HistoryPage,
  ListHistoryInput,
  MarkReadInput,
  PersonRef,
  SendMessageInput,
} from '../types/messages'
