/**
 * 全站单一刻度的 Vue 包装（票 #60）。
 *
 * 组件侧**只**用这里订阅刻度，**不许自建定时器**（票面验收：组件禁自建定时器）。
 * 生命周期绑定由 Vue 负责：组件挂载 → 订阅并计数 +1；卸载 → 退订并计数 −1。
 *
 * 用法：
 * ```ts
 * const now = useNowTick()
 * // 模板里：{{ relativeOf(post.createdAt, now) }}
 * ```
 *
 * 为什么返回值是**时刻**而不是文案：文案是 `utils/relativeTime.ts` 的纯函数职责，
 * 刻度只管"现在几点"。两件事分开，各有一个真源。
 */
import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'
import { globalNowTick } from '../utils/nowTick'

/**
 * 订阅全站刻度，返回一个随刻度推进的 `now`（毫秒）。
 *
 * SSR / 非浏览器环境也能用（刻度退化成纯定时器，见 `utils/nowTick.ts`）。
 */
export function useNowTick(): Ref<number> {
  const tick = globalNowTick()
  const now = ref(tick.now())

  let stop: (() => void) | null = null

  onMounted(() => {
    stop = tick.subscribe((value) => {
      now.value = value
    })
    tick.acquire()
  })

  onBeforeUnmount(() => {
    stop?.()
    stop = null
    tick.release()
  })

  return now
}
