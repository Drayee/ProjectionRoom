/**
 * 轻提示队列（票 #67；决议 #53 §5）。
 *
 * ## 为什么是全局 store 而不是某个页面里的 ref
 *
 * 提示挂在 `App.vue`（路由出口之外），因为**换页不该把它卸载掉** ——
 * 用户点了一条"有人关注了你"，正在跳去粉丝 tab，提示不该在跳转的一瞬间消失。
 *
 * ## 为什么是队列而不是"一个当前提示"
 *
 * 同时收到两条关注事件（比如互关的两个事件挨着到）时，只留最后一条会让
 * 前一条静默丢掉；而"丢了"这件事用户永远不知道。队列让它挨个显示。
 * 上限 `MAX_TOASTS` 条：再多就不是"轻提示"了，而是一面墙。
 *
 * ## 不做的事（决议 §5、§6）
 *
 *   - 不做铃铛 / 收件箱 / 通知面板 —— 提示是**转瞬**的，落点是粉丝 tab；
 *   - 不抢焦点（`role="status"` 而不是 `alert`，也不 `focus()`）；
 *   - 不改变红点生命周期 —— 提示出现与消失**都不**动 `followers.newFollowers`。
 *     红点只在"进粉丝 tab"时清（决议 §4），提示只是"顺便告诉你一声"。
 */
import { ref } from 'vue'
import { defineStore } from 'pinia'

/** 一条提示。`id` 用来做 `v-for` 的 key 与定时器的句柄。 */
export interface Toast {
  id: number
  /** 文案（由调用方翻译，store 不认识事件类型）。 */
  text: string
  /** 点击后去哪（`/friends?tab=followers`）。空串 = 不可点。 */
  to: string
}

/**
 * 提示存活时长（决议 §5 的 "3~5s"）。
 *
 * 取 4s：3s 对一条含人名的中文句子略短（读得快也要 2 秒），
 * 5s 又会让"下一条"等太久。
 */
export const TOAST_MS = 4000

/** 同时最多几条。超了就丢最老的 —— 新的那条更可能是用户还没看到的。 */
export const MAX_TOASTS = 3

export const useNoticesStore = defineStore('notices', () => {
  const toasts = ref<Toast[]>([])
  let nextId = 1
  /** id → 定时器。手动关闭时要清掉，否则定时器会在已经移除的条目上再跑一次。 */
  const timers = new Map<number, ReturnType<typeof setTimeout>>()

  function dismiss(id: number): void {
    const timer = timers.get(id)
    if (timer !== undefined) {
      clearTimeout(timer)
      timers.delete(id)
    }
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }

  /**
   * 推一条提示。
   *
   * 返回 id，调用方一般不需要它（关闭走 `dismiss` 或自动超时）。
   */
  function push(text: string, to = ''): number {
    const id = nextId++
    toasts.value = [...toasts.value, { id, text, to }]
    // 超上限就丢最老的（同时清掉它的定时器，否则那个定时器会在
    // 一个已经不存在的 id 上白跑一次）。
    while (toasts.value.length > MAX_TOASTS) {
      const oldest = toasts.value[0]
      if (!oldest) break
      const timer = timers.get(oldest.id)
      if (timer !== undefined) {
        clearTimeout(timer)
        timers.delete(oldest.id)
      }
      toasts.value = toasts.value.slice(1)
    }
    timers.set(
      id,
      setTimeout(() => {
        timers.delete(id)
        toasts.value = toasts.value.filter((t) => t.id !== id)
      }, TOAST_MS),
    )
    return id
  }

  /** 清空（登出时调：上一个人的提示不该留在下一个人屏幕上）。 */
  function clear(): void {
    for (const timer of timers.values()) clearTimeout(timer)
    timers.clear()
    toasts.value = []
  }

  return { toasts, push, dismiss, clear }
})
