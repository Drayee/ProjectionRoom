/**
 * 展示层 composable：进房凭据装配与房间会话建立 / 拆除。
 * 不含协议实现（信令/WebRTC 在 stores/room.ts，逻辑层）。
 *
 * 从旧 RoomView.vue 下沉。**硬契约逐字保留**（票 #12 §3.4）：
 *   readJoin() → newClientId() → store.enterRoom()；卸载时 store.leaveRoom()。
 * 这是全应用唯一把凭据变成房间会话的地方，也是唯一的拆除点。
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { newClientId, useRoomStore, type JoinCredentials } from '../../../stores/room'
import { readJoin } from '../../../utils/joinSession'
export function useRoomEntry(roomId: () => string) {
  const store = useRoomStore()
  const missingCredential = ref(false)

  function buildCredentials(): JoinCredentials | null {
    const code = roomId().toUpperCase()
    const stored = readJoin(code)
    if (!stored) return null
    const creds: JoinCredentials = {
      roomId: code,
      password: stored.password,
      role: stored.role,
      displayName: stored.displayName,
      clientId: newClientId(),
    }
    // hostToken 只在主播重建时才有；缺失就不传（游客/观众路径）
    if (stored.hostToken) creds.hostToken = stored.hostToken
    return creds
  }

  function enter(): void {
    const creds = buildCredentials()
    if (!creds) {
      missingCredential.value = true
      return
    }
    missingCredential.value = false
    void store.enterRoom(creds)
  }

  onMounted(() => {
    // 文档标题**不在这里写**（票 #79 的单点纪律）：
    // 页面各自写 document.title 会让多标签页下出现多种口径（Vite 默认标题、
    // 房间码标题、路由页面名各写各的），而用户无从判断哪条才是当前页。
    // 房间标题现在是路由表里 `meta.title` 的函数形态（取 params.roomId），
    // 由 utils/routeMeta.ts 统一拼成「<房间码> · 月喵」。
    onBeforeUnmount(() => {
      // 唯一拆除点：不清则信令 / WebRTC / 定时器都不停
      store.leaveRoom()
    })
    enter()
  })

  return { store, missingCredential, enter }
}
