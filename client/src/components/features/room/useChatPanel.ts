/**
 * 展示层 composable：聊天面板的输入与自动滚底。
 * 从旧 ChatPanel.vue 下沉（store.sendChat 是唯一写动作）。
 */
import { nextTick, ref, watch } from 'vue'
import { useRoomStore } from '../../../stores/room'

export function useChatPanel() {
  const store = useRoomStore()
  const draft = ref('')
  const listRef = ref<HTMLElement | null>(null)

  async function submit() {
    const text = draft.value.trim()
    if (!text) return
    store.sendChat(text)
    draft.value = ''
    await nextTick()
    if (listRef.value) listRef.value.scrollTop = listRef.value.scrollHeight
  }

  // 新消息到达（含别人的）也滚底：停在历史里会错过当下讨论
  watch(
    () => store.chat.length,
    async () => {
      await nextTick()
      if (listRef.value) listRef.value.scrollTop = listRef.value.scrollHeight
    },
  )

  return { store, draft, listRef, submit }
}
