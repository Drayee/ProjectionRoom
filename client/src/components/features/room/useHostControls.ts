/**
 * 展示层 composable：主播控制台的房间元数据编辑逻辑。
 * 从旧 HostPanel.vue 下沉（titleDirty 防覆盖、公开性失败回滚）。
 */
import { computed, ref, watch } from 'vue'
import { useAuthStore } from '../../../stores/auth'
import { useRoomStore } from '../../../stores/room'
import { useRoomMetaStore } from '../../../stores/roomMeta'

/**
 * 展示层 composable：主播控制台的房间元数据编辑逻辑。
 *
 * ⚠️ 所有 store 调用与响应式状态必须放在函数体内：模块顶层执行时机早于
 * `app.use(createPinia())`，顶层调用 useXxxStore() 会在 mount 前抛
 * `Cannot read properties of undefined (reading '_s')`，整站白屏。
 */
export function useHostControls() {
  const store = useRoomStore()
  const auth = useAuthStore()
  const meta = useRoomMetaStore()

  /** 非房主或本机不是主播时整块不渲染。 */
  const showMeta = computed(() => store.isHost && auth.isLoggedIn && meta.roomId !== '' && !meta.notOwner)
  const metaBusy = computed(() => meta.saving)

  /**
   * 标题输入框本地值。
   * 不直接 v-model 到 store：输入途中每敲一字都写进终态字段，
   * 读回/保存失败后就没法回退到服务端原值。输入是局部状态，
   * 读回与保存成功才由 store 同步回显（服务端会清洗标题）。
   */
  const titleDraft = ref('')
  let titleDirty = false

  const tooLong = computed(() => [...titleDraft.value].length > meta.titleLimit)

  watch(
    () => store.roomId,
    (roomId) => {
      meta.bind(roomId)
      titleDraft.value = ''
      titleDirty = false
      if (roomId !== '' && store.isHost && auth.isLoggedIn) {
        void meta.load(auth.authedFetch)
      }
    },
    { immediate: true },
  )

  // 服务端回显终态后同步进输入框（刚读回、保存成功）；用户正在敲则不覆盖。
  watch(
    () => meta.title,
    (value) => {
      if (!titleDirty) titleDraft.value = value
    },
    { immediate: true },
  )

  function onTitleInput() {
    titleDirty = true
  }

  /** 允许空串（服务端默认即空）：读回闭环后清空标题是明确动作。 */
  function saveTitle() {
    void meta.save(auth.authedFetch, { title: titleDraft.value }).then(() => {
      titleDirty = false
    })
  }

  let publicBefore = false

  /** 开关立刻动（用户这一下必须有反馈），失败再回滚。 */
  function onPublicToggle(event: Event) {
    const next = (event.target as HTMLInputElement).checked
    publicBefore = meta.isPublic
    meta.isPublic = next
    void meta.save(auth.authedFetch, { isPublic: next }).then((ok) => {
      if (!ok) meta.isPublic = publicBefore
    })
  }

  return { showMeta, metaBusy, titleDraft, titleDirty, tooLong, onTitleInput, saveTitle, onPublicToggle, meta }
}
