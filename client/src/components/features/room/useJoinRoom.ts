/**
 * 展示层 composable：观众免登录进房（查房间 → 落凭据 → 跳转）。
 * 不含协议实现（REST 直连与凭据存储分别归服务端契约与 utils/joinSession）。
 *
 * 从旧 JoinRoomCard.vue 下沉。硬契约：
 *   - 进房**不需要登录**（产品决定，刻意不做账号拦截）；
 *   - 先 GET /api/rooms/:code 判存在与主播在否，再 rememberJoin + 跳 /room/<码>；
 *   - 房间密码输入**只出现在这里**（落地页路径），房间页零密码框（票 #12）。
 */
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { rememberJoin } from '../../../utils/joinSession'
import type { RoomInfoResponse } from '../../../types/protocol'

export function useJoinRoom(getName: () => string) {
  const router = useRouter()

  const code = ref('')
  const password = ref('')
  const busy = ref(false)
  const error = ref('')

  async function join(): Promise<boolean> {
    error.value = ''
    const c = code.value.trim().toUpperCase()
    const name = getName().trim()
    if (!c) {
      error.value = '请填写房间码'
      return false
    }
    if (!name) {
      error.value = '请先填写昵称'
      return false
    }
    busy.value = true
    try {
      const resp = await fetch(`/api/rooms/${encodeURIComponent(c)}`)
      const info = resp.ok ? ((await resp.json()) as RoomInfoResponse) : null
      if (!info?.exists) {
        error.value = `房间 ${c} 不存在`
        return false
      }
      if (!info.hasHost) {
        error.value = `房间 ${c} 还没有主播进房，请稍候`
        return false
      }
      rememberJoin(c, { password: password.value, role: 'viewer', displayName: name })
      await router.push(`/room/${c}`)
      return true
    } catch (err) {
      error.value = `加入房间失败：${(err as Error).message}`
      return false
    } finally {
      busy.value = false
    }
  }

  return { code, password, busy, error, join }
}
