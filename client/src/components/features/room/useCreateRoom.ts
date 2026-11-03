/**
 * 展示层 composable：主播建房（POST /api/rooms → 落凭据 → 跳房）。
 *
 * 从旧 `CreateRoomCard.vue`（4454c63 删除）的 `createRoom()` 平移，形态对齐
 * `useJoinRoom.ts` —— 两者是同一条 REST 契约的两端（一个建房一个进房），
 * 逻辑写在 composable 里、组件只渲染，才不会出现"两个组件各写一份请求"。
 *
 * # 硬契约（逐字保留，改之前先看这几条）
 *
 *   1. **建房必须登录**：`ensureLoggedIn()` 未登录时**就地拦截**（带 `?redirect=`），
 *      绝不先发一个注定 401 的请求。服务端 `RequireAuth` 仍是兜底（铁律 I3：
 *      授权 100% 在服务端，这里只是体验前置）。
 *   2. 401 = "刷新一次再重试之后仍然 401"：说清原因 + `redirectToLogin()`，
 *      **不再自行重试**（服务端的 refresh 带重放检测，无脑重试会捅掉全部会话）。
 *   3. `hostToken` **只在创建响应里出现这一次**（服务端只存哈希）：必须随 join
 *      凭据一起落 sessionStorage，否则主播断线后宽限期内接不回主播位。
 *   4. 成功后跳 `/room/<码>` —— 凭据已经落好，房间页的 `useRoomEntry` 会自己取。
 *
 * # 为什么不在这里 `applyIceResponse`
 *
 * 旧实现从 room store 的 `applyIceResponse` 偷一份 ICE 列表进缓存。这里**刻意不做**：
 *
 *   - 房间页的 `enterRoom()` 已经会 `iceConfig.start()` 主动拉一次 `/api/ice`，
 *     而那条路径对"直接从分享链接进房"的人本来就成立（`stores/room.ts` 的注释
 *     记录了这条实测结论：不拉就是"进得去房间、一直缓冲 0 片"）；
 *   - 首页 chunk 现在不含 room store（实测 `applyIceResponse` 出现 0 次）。为了
 *     省一次几百字节的 `/api/ice` 往返而把 WebRTC/信令整包拉进首屏，是本末倒置。
 *
 * 唯一的代价是进房后多一次 `/api/ice` 往返，而它**与信令连接并行**，不挡进房。
 */
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../../../stores/auth'
import { rememberJoin } from '../../../utils/joinSession'
import type { CreateRoomResponse } from '../../../types/protocol'

/** 本机昵称的存储键（与 `useJoinRoom` 共用同一个值）。 */
const NAME_KEY = 'pr:name'

export function useCreateRoom(getName: () => string) {
  const router = useRouter()
  const auth = useAuthStore()

  const password = ref('')
  const busy = ref(false)
  const error = ref('')

  /**
   * 建房。返回是否成功（调用方一般只用来收尾，UI 状态由 `busy`/`error` 驱动）。
   */
  async function create(): Promise<boolean> {
    error.value = ''
    // ① 就地拦截：未登录先把人送去登录页并带上回跳路径，登录成功后回到首页。
    if (!auth.ensureLoggedIn()) {
      return false
    }

    const name = getName().trim()
    if (!name) {
      error.value = '请先填写昵称'
      return false
    }

    busy.value = true
    try {
      // ② 带 access token（服务端 POST /api/rooms 挂 RequireAuth）。
      // 401 时 authedFetch 会自动刷新一次再重试；刷新也失败才会走到下面那个 401 分支。
      const resp = await auth.authedFetch('/api/rooms', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: password.value }),
      })

      if (resp.status === 401) {
        error.value = '登录状态已失效，请重新登录后重试'
        void auth.redirectToLogin()
        return false
      }
      if (!resp.ok) {
        error.value = `创建房间失败（HTTP ${resp.status}）`
        return false
      }

      const data = (await resp.json()) as CreateRoomResponse
      // 昵称落本地：下次进房/建房时输入框里还是它（与 JoinRoomCard 同一个键）。
      try {
        localStorage.setItem(NAME_KEY, name)
      } catch {
        /* 隐私模式等场景静默：昵称是便利，不是数据保证 */
      }
      // ③ 凭据入 sessionStorage（含主播复位令牌），④ 然后跳房。
      rememberJoin(data.roomId, {
        password: password.value,
        role: 'host',
        displayName: name,
        // 老服务端没有这个字段 → undefined（不带照常能进房，只是宽限期内接不回主播位）。
        hostToken: data.hostToken,
      })
      await router.push(`/room/${data.roomId}`)
      return true
    } catch (err) {
      error.value = `创建房间失败：${(err as Error).message}`
      return false
    } finally {
      busy.value = false
    }
  }

  return { password, busy, error, create }
}
