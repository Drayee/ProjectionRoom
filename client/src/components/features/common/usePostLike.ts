/**
 * 点赞的共用逻辑（票 #63 把详情页原有的那一份迁出来）。
 *
 * ## 为什么必须共用
 *
 * 点赞是**全站唯一一处**同时具备这三件事的交互：乐观更新、失败回滚、在途锁。
 * 原先只有详情页有它（列表页的卡片整块是链接，根本没有点赞入口）。本票把点赞搬到
 * 列表卡片的动作条上之后，如果列表再抄一份，就会出现两份"乐观更新"：
 * 下一次修回滚边界时只改一处，另一处继续把计数停在谎言上 —— 而这种缺陷只在
 * 服务端开始返回 429 时才暴露。
 *
 * ## 三件事各自为什么必要
 *
 * 1. **乐观更新**：点赞是最频繁的互动，等一次往返会让按钮"慢半拍"。
 * 2. **失败回滚**：不回滚就等于把一次 429 变成永久错误的计数，而下次刷新又对不上 ——
 *    用户看到的是"数字自己乱跳"。回滚值优先用服务端响应里的**权威计数**，
 *    只有拿不到响应时才退回本地快照。
 * 3. **单条在途锁**：连点三次不能发三次请求（服务端计数会抖三下、限流也会被触发）。
 *    锁是**按帖子**的，不是全局 —— 列表里同时点两条不同的帖子应当都生效。
 *
 * ## 失败为什么**不**走页面的 `useApiFailure`
 *
 * 那是**读**的分诊：它把失败态交给页面渲染成整块空态/错误态。点赞是**写**，
 * 一次点赞失败绝不能把整个列表换成"加载失败"—— 用户会以为帖子没了。
 * 所以这里自己消化：401 才跳登录（那是唯一必须打断的），其余变成一条轻提示。
 */
import { onBeforeUnmount, ref } from 'vue'
import * as postsApi from '../../../api/posts'
import { useAuthStore } from '../../../stores/auth'
import type { Post } from '../../../types/posts'
import { classify, ensureLoggedInForWrite } from './useApiFailure'

/** 轻提示停留时长：够读完一句人话，又不至于一直挡在内容上。 */
const TOAST_MS = 4000

export function usePostLike() {
  const auth = useAuthStore()

  /** 正在等响应的帖子 id（列表里可能有多条同时在途，所以是集合不是单值）。 */
  const busyIds = ref<number[]>([])

  /** 轻提示文案（空串 = 不显示）。 */
  const toast = ref('')
  let toastTimer: ReturnType<typeof setTimeout> | undefined

  function isBusy(id: number): boolean {
    return busyIds.value.includes(id)
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
   * 点赞/取消点赞。
   *
   * 直接在传入的 `Post` 对象上改 —— 那是列表里那个响应式对象本身，改它页面就更新，
   * 不需要回调把新数组传回去。代价是调用方必须传**真对象**（不能传副本）。
   */
  async function toggle(post: Post): Promise<void> {
    // 未登录：就地送去登录页（带 redirect），一次注定 401 的请求都不发。
    if (!ensureLoggedInForWrite()) return
    // 在途锁：这一条已经在等响应，连点直接忽略（真机脚本 P2b 判的就是这条）。
    if (isBusy(post.id)) return

    const before = { liked: post.liked, likeCount: post.likeCount }
    // 显式取 1 / -1：三元里混 boolean 会让 TS 推断成 boolean | number。
    const delta = before.liked ? -1 : 1
    post.liked = !before.liked
    post.likeCount = Math.max(0, before.likeCount + delta)
    busyIds.value = [...busyIds.value, post.id]

    try {
      const r = await postsApi.toggleLike(post.id)
      // 以服务端为准：本地那次 ±1 只是"先给你看"，权威计数在响应里。
      post.liked = r.liked
      post.likeCount = r.likeCount
    } catch (err) {
      post.liked = before.liked
      post.likeCount = before.likeCount
      const f = classify(err)
      if (f.kind === 'unauthorized') {
        // token 本地看着有效、服务端已判失效：这是唯一必须打断的失败。
        void auth.redirectToLogin()
        return
      }
      // 原始错误只进 console（决议 #44：页面给人话，日志给堆栈）。
      console.error('[like] 失败', err)
      say(f.kind === 'ratelimited' ? '操作太频繁，稍后再试' : '点赞没成功，稍后再试')
    } finally {
      busyIds.value = busyIds.value.filter((id) => id !== post.id)
    }
  }

  onBeforeUnmount(dismissToast)

  /**
   * 对外也暴露 `say`：页面有**自己的**轻提示需求（例如"收藏还没接线"），
   * 而同一时刻只该有一条提示 —— 两个组件各起一个定时器会互相盖不住。
   */
  return { toggle, isBusy, toast, dismissToast, say }
}
