/**
 * 粉丝红点的全局状态（票 #67；决议 #53）。
 *
 * ## 为什么需要一个 store 而不是让顶栏自己拉
 *
 * 顶栏（`ui/AppHeader.vue`）在每个页面都在场，而"有没有新粉丝"这个数字
 * 有两个显示位置：顶栏「好友」分区的角标、好友页「粉丝」tab 上的角标。
 * 让顶栏自己去请求，就会出现"每次换页都打一次 `/api/follows?tab=followers`"——
 * 那是把顶栏变成了一个轮询器，而决议 §6 明确不做轮询。
 *
 * 所以这里只持有**一个数字**，写入点有两个：
 *
 *   1. 好友页拉名单时（响应里带 `newCount`）；
 *   2. 收到 `follow-created` 实时事件时（+1，乐观）。
 *
 * ## 它与 `auth.unread` 的关系：**没有关系**
 *
 * 决议 §3 写得最重的一条：消息未读只管私信，粉丝红点是另一条数。
 * 两者混用一个 ref 的症状是"读了私信、粉丝红点也灭了"，或者反过来
 * "有新粉丝、顶栏消息角标亮起来"。所以这里**刻意**是一个独立的 store，
 * 而不是往 `stores/auth.ts` 里加一个字段 —— 后者的名字叫"auth"，
 * 加进去之后下一个改它的人会以为它属于会话。
 *
 * ## 为什么不持久化
 *
 * 数字是**派生**的（服务端每次按水位线算）。存进 localStorage 会让
 * "上次的数字"与"这次的事实"打架，而它本来每次进好友页都会重新算。
 * 刷新后短暂为 0（顶栏不亮）是可接受的：那期间并没有"新发生的事"。
 */
import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

export const useFollowsStore = defineStore('follows', () => {
  /**
   * 未回关的新粉丝数（服务端 `new_count`）。
   *
   * 0 = 没有红点。这里没有"未知"态（不像 `auth.unread` 有 `unreadKnown`）：
   * 粉丝红点只在**已经拉到过数据**之后才有意义，而那时数字就是已知的；
   * 没拉到过时它是 0，顶栏不亮，与"真的没有新粉丝"在界面上表现一致。
   * 这是有意的取舍：红点不像未读数那样有"服务端没告诉我"这个第三种情况。
   */
  const newFollowers = ref(0)

  /** 角标文案：0 时为空串（`v-if` 直接判它）。超过 99 显示 `99+`。 */
  const badge = computed(() => {
    const n = newFollowers.value
    if (!Number.isFinite(n) || n <= 0) return ''
    return n > 99 ? '99+' : String(n)
  })

  /** 用服务端算好的真值覆盖（好友页拉名单时调用）。 */
  function setNewFollowers(count: number | null): void {
    if (count === null || !Number.isFinite(count) || count <= 0) {
      newFollowers.value = 0
      return
    }
    newFollowers.value = Math.floor(count)
  }

  /**
   * 收到「有人关注了你」时 +1（乐观）。
   *
   * 为什么不等下一次拉名单：这条通道的价值就是"在线时能立刻知道"。
   * 为什么只 +1 而不是重新请求：重新请求会让每一条关注事件都变成一次
   * 网络往返，而如果那一刻刚好网络不好，红点就不亮了 —— 而事件本身
   * 已经明确告诉我们"多了一个人"。
   *
   * 漂移是允许的：下次进好友页时 `setNewFollowers` 会用服务端的真值覆盖。
   */
  function bumpNewFollowers(): void {
    newFollowers.value += 1
  }

  /** 清掉红点（进粉丝 tab / 已看过）。 */
  function clearNewFollowers(): void {
    newFollowers.value = 0
  }

  return {
    newFollowers,
    badge,
    setNewFollowers,
    bumpNewFollowers,
    clearNewFollowers,
  }
})
