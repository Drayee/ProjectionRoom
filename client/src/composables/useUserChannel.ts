/**
 * 用户级实时通道的挂载点（票 #67）。
 *
 * ## 为什么这个文件在 #67 才出现
 *
 * `api/userChannel.ts` 从 wayfinder #31 就写好了，但**全仓没有调用点** ——
 * 它一直在等第一个真正需要它的功能。票 #67 的"在线时飘一条自消失提示"
 * 就是那个功能：没有这条通道，提示只能靠轮询（决议 §6 明确不做）。
 *
 * ## 只处理关注事件，其余一律忽略
 *
 * 本票的范围是粉丝红点。通道还会推私聊（`dm`）、已读回执、在线态 ——
 * 那些各自有各自的票与各自的落点（消息页自己拉会话列表）。
 * **刻意在这里不处理它们**：把 `dm` 在这里顺手做成"刷新未读数"会让
 * 未读数的写入点从 1 个变成 2 个，而 #30 花了一整票把那个写入点收敛成一个。
 * 需要的时候由那个域自己的票来接。
 *
 * ## 失败是静默的
 *
 * 通道连不上（后端未上线、未登录、网络不通）时**什么都不做**：界面上
 * 红点仍然会在下次进好友页时由名单接口带回真值，提示则彻底没有 ——
 * 这正是这条通道出现之前的行为，界面已经完整可用。
 */
import { onBeforeUnmount, watch } from 'vue'
import { createUserChannel, type UserChannel } from '../api/userChannel'
import { listFollowList } from '../api/follows'
import { useAuthStore } from '../stores/auth'
import { useFollowsStore } from '../stores/follows'
import { useNoticesStore } from '../stores/notices'
import { followNoticeText } from '../utils/followNotice'
import { friendsPath } from '../utils/friendsRoute'

/**
 * 挂载用户级实时通道。
 *
 * 必须在组件 `setup` 里调用（它用 `watch` + `onBeforeUnmount`）。
 * 建议在 `App.vue` 里调一次：外壳不重建，通道也就不会随换页断开。
 */
export function useUserChannel(): void {
  const auth = useAuthStore()
  const follows = useFollowsStore()
  const notices = useNoticesStore()

  let channel: UserChannel | null = null

  /**
   * 登录后先取一次粉丝红点（顶栏角标的首屏来源）。
   *
   * 为什么必须在这里取：角标挂在顶栏，而顶栏**不认识**好友页 ——
   * 红点数的唯一来源是"拉粉丝名单时带回来的 `newCount`"，那只在
   * `/friends?tab=followers` 发生过。不在这里补一次，用户登录后
   * 不点进好友页就永远看不到角标（症状是"明明有人关注我，顶栏没反应"）。
   *
   * 为什么用名单接口而不是新开一个 count 端点：决议 #53 §9 明确
   * "列表与红点一个端点两用"，多开一个端点就是多一处可能与列表不一致的数字。
   * 代价是登录时多一次请求 —— 与 `/me` 那次并行，用户感知不到。
   *
   * 失败静默：红点是体验增量，取不到就不亮，不影响任何功能。
   */
  async function primeRedDot(): Promise<void> {
    try {
      const result = await listFollowList('followers')
      follows.setNewFollowers(result.newCount)
    } catch {
      // 后端未上线 / 网络不通 / 未登录：不亮就是了。
    }
  }

  function stop(): void {
    channel?.stop()
    channel = null
  }

  function start(): void {
    stop()
    channel = createUserChannel({
      onEvent(ev) {
        if (ev.kind !== 'friend') return
        // 两类关注事件 → 一句话 + 红点 +1。其余 friend 事件（拉黑/解除）
        // 一律静默：决议 §7 明确只弹「新粉丝」与「互关」。
        const text = followNoticeText(ev.type, ev.event.displayName ?? '')
        if (text === null) return
        // 红点 +1 与提示是两件独立的事：提示 4 秒后消失，红点留到进粉丝 tab。
        // 决议 §5 原文「toast 不改变红点生命周期」—— 这里只是**同时**发生。
        follows.bumpNewFollowers()
        notices.push(text, friendsPath({ tab: 'followers' }))
      },
      onState(state) {
        // 通道自己已经处理了三种"停止重连"（封禁 / 凭据失效挂起 / 正常关闭），
        // 这里不需要额外动作。保留这个回调是为了让"通道状态"将来能显示成
        // 界面上的实时徽标（那时 `suspended` 与 `banned` 要用不同的文案：
        // 前者是"重新登录后会自动恢复"，后者是"账号已被封禁"）。
        void state
      },
    })
    channel.start()
  }

  // 登录态翻转时重连/断开。`immediate` 让首屏已登录的情况也能连上
  //（bootstrap 恢复会话是异步的，所以首屏那一刻通常是 anonymous）。
  watch(
    () => auth.isLoggedIn,
    (loggedIn) => {
      if (loggedIn) {
        void primeRedDot()
        start()
      } else {
        follows.clearNewFollowers()
        stop()
      }
    },
    { immediate: true },
  )

  onBeforeUnmount(stop)
}
