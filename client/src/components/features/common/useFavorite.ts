/**
 * 收藏的共用逻辑（票 #64 把"乐观更新 + 失败回滚 + 在途锁"从点赞那一份照搬过来）。
 *
 * ## 为什么与 `usePostLike` 是两个文件而不是一个
 *
 * 两者的**流程骨架**一样（乐观改本地 → 发请求 → 用权威值覆盖 / 失败回滚），
 * 但有三处实质差别，硬合并会逼出一个满是开关的公共函数：
 *
 *   1. **幂等性**：点赞是 toggle（服务端翻转），收藏是 set（PUT/DELETE）。
 *      回滚时点赞要还原"翻转前"，收藏要还原"目标状态"——同一个 `before` 变量
 *      装不下这两种语义。
 *   2. **失败文案**：点赞失败说"点赞没成功"，收藏失败说"收藏没成功"。
 *   3. **作用对象**：点赞只作用于 `Post`；收藏作用于三种类型（帖/番剧/放映室），
 *      只能按 `(type, targetId)` 定位，没有"那个响应式对象"可以就地改。
 *
 * 第 3 条是决定性的：`usePostLike` 直接改传进来的 `Post` 对象（"改它页面就更新"），
 * 而收藏没有这样一个对象可改 —— 它要通知**持有状态的调用方**把某个 id 的
 * `favorited` 改成什么。所以这里用回调式接口，而不是就地改对象。
 *
 * ## 失败为什么不走页面的 `useApiFailure`
 *
 * 与点赞同一条理由：收藏是**写**。一次收藏失败绝不能把整个列表换成"加载失败"。
 * 401 才跳登录（唯一必须打断的），其余变成一条轻提示。
 */
import { onBeforeUnmount, ref } from 'vue'
import * as favoritesApi from '../../../api/favorites'
import { useAuthStore } from '../../../stores/auth'
import { classify, ensureLoggedInForWrite } from './useApiFailure'
import type { FavoriteType } from '../../../types/favorites'

/** 轻提示停留时长（与 usePostLike 同值：全站轻提示应该同时消失）。 */
const TOAST_MS = 4000

/**
 * 在途锁的键。
 *
 * 与点赞的"按帖子 id"同构，但这里要带类型 —— 帖子 1 与番剧 1 是两个不同的对象，
 * 用纯 id 做键会让"收藏帖子 1 的同时收藏番剧 1"互相挡住。
 */
function busyKey(type: FavoriteType, targetId: string): string {
  return `${type}:${targetId}`
}

export function useFavorite() {
  const auth = useAuthStore()

  /** 正在等响应的对象（可能同时有多个：列表里连点几条不同的）。 */
  const busyKeys = ref<string[]>([])

  /** 轻提示文案（空串 = 不显示）。 */
  const toast = ref('')
  let toastTimer: ReturnType<typeof setTimeout> | undefined

  function isBusy(type: FavoriteType, targetId: string): boolean {
    return busyKeys.value.includes(busyKey(type, targetId))
  }

  function dismissToast(): void {
    if (toastTimer !== undefined) {
      clearTimeout(toastTimer)
      toastTimer = undefined
    }
    toast.value = ''
  }

  function say(message: string): void {
    dismissToast()
    toast.value = message
    toastTimer = setTimeout(() => {
      toast.value = ''
      toastTimer = undefined
    }, TOAST_MS)
  }

  /**
   * 设置收藏状态。
   *
   * `apply` 是调用方给的**写回函数**：乐观更新时先 `apply(next)`，失败时 `apply(before)`。
   * 为什么不让本函数直接改对象：收藏作用于三种类型，而持有状态的是页面（列表数组、
   * 详情页的 `post` ref）—— 只有它们知道"哪个位置的那一行"要改。
   *
   * `current` 是调用方当前认为的状态（用来算 `next` 与回滚值）。
   */
  async function setFavorite(
    type: FavoriteType,
    targetId: string,
    next: boolean,
    apply: (value: boolean) => void,
  ): Promise<void> {
    // 未登录：就地送去登录页（带 redirect），一次注定 401 的请求都不发。
    if (!ensureLoggedInForWrite()) return
    const key = busyKey(type, targetId)
    // 在途锁：这个对象已经在等响应，连点直接忽略。
    if (busyKeys.value.includes(key)) return

    const before = !next
    apply(next)
    busyKeys.value = [...busyKeys.value, key]

    try {
      const r = await favoritesApi.setFavorite(type, targetId, next)
      // 以服务端为准：本地那次翻转只是"先给你看"。
      apply(r.favorited)
    } catch (err) {
      apply(before)
      const f = classify(err)
      if (f.kind === 'unauthorized') {
        void auth.redirectToLogin()
        return
      }
      // 原始错误只进 console（决议 #44：页面给人话，日志给堆栈）。
      console.error('[favorite] 失败', err)
      say(f.kind === 'ratelimited' ? '操作太频繁，稍后再试' : '收藏没成功，稍后再试')
    } finally {
      busyKeys.value = busyKeys.value.filter((k) => k !== key)
    }
  }

  onBeforeUnmount(dismissToast)

  /**
   * 对外也暴露 `say`：页面有**自己的**轻提示需求，而同一时刻只该有一条提示 ——
   * 两个组件各起一个定时器会互相盖不住（与 usePostLike 同一条理由）。
   */
  return { setFavorite, isBusy, toast, dismissToast, say }
}
