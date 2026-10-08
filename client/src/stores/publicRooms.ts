import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { fetchPublicRooms, PUBLIC_ROOMS_PAGE_SIZE, type PublicRoom } from '../api/publicRooms'
import { toHttpError } from '../api/http'
import { useAuthStore } from './auth'

/**
 * 公开房列表（`/rooms`）的状态机。
 *
 * 为什么单独一个 store 而不是把状态留在视图里：`/rooms` 是**免登录**页面，
 * 而顶栏、首页与它都要知道"一共有多少公开房"这件事；把分页游标与列表放进 Pinia
 * 之后，视图只负责渲染三种状态，翻页/重试都不会各自造一份游标
 *（两份游标漂移的症状是"加载更多之后重复出现同一批房间"，很难从界面上看出来）。
 *
 * 四态由三个布尔一起表达，刻意**互斥**：
 *   loading       首次加载（列表为空时的骨架）
 *   loadingMore   已有数据时加载下一页（此时列表**必须**保持可见）
 *   error         最近一次失败的服务端文案（刷新会清掉它）
 * 空态不是布尔，而是 `!loading && !error && items.length === 0` 这个组合：
 * 多存一个 `empty` 只会多一个可能与 items 不一致的真相来源。
 */
export const usePublicRoomsStore = defineStore('publicRooms', () => {
  const auth = useAuthStore()

  const items = ref<PublicRoom[]>([])
  const total = ref(0)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref('')

  /** 已经发起过的请求序号：迟到的响应（用户连点刷新）不许覆盖最新的那份结果。 */
  let requestSeq = 0

  const empty = computed(() => !loading.value && error.value === '' && items.value.length === 0)
  /** 还有没有下一页：以服务端的 total 为准，而不是"这一页够不够 20 条"。 */
  const hasMore = computed(() => items.value.length < total.value)

  function fail(err: unknown) {
    error.value = toHttpError(err).message
  }

  /** 首次加载 / 重试：清空列表重新取第一页。 */
  async function load(): Promise<void> {
    requestSeq += 1
    const seq = requestSeq
    loading.value = true
    error.value = ''
    try {
      const page = await fetchPublicRooms(auth.authedFetch, { limit: PUBLIC_ROOMS_PAGE_SIZE, offset: 0 })
      if (seq !== requestSeq) return
      items.value = page.items
      total.value = page.total
    } catch (err) {
      if (seq !== requestSeq) return
      // 失败时**保留上一次的列表**：服务端抖了一下就清空界面，比显示一份略旧的列表更糟。
      fail(err)
    } finally {
      if (seq === requestSeq) loading.value = false
    }
  }

  /**
   * 加载下一页（追加）。
   *
   * 游标用**已加载条数**而不是 `offset += limit`：服务端会夹住越界的 limit，
   * 按请求值累加迟早会漂移出重复行。用 items.length 则永远与已显示的内容对齐。
   */
  async function loadMore(): Promise<void> {
    if (loading.value || loadingMore.value || !hasMore.value) return
    loadingMore.value = true
    error.value = ''
    try {
      const page = await fetchPublicRooms(auth.authedFetch, {
        limit: PUBLIC_ROOMS_PAGE_SIZE,
        offset: items.value.length,
      })
      // 追加时去重：翻页期间有房间被创建/关闭时服务端顺序可能变化，重复行会被 vue :key 吃掉，
      // 但它会让"第几个"这类计数变得不可信。
      const seen = new Set(items.value.map((room) => room.roomId))
      items.value = [...items.value, ...page.items.filter((room) => !seen.has(room.roomId))]
      total.value = page.total
    } catch (err) {
      fail(err)
    } finally {
      loadingMore.value = false
    }
  }

  return { items, total, loading, loadingMore, error, empty, hasMore, load, loadMore }
})
