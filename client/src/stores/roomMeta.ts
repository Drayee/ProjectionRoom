import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { fetchRoomMeta, patchRoomMeta, ROOM_TITLE_MAX, type RoomMetaPatch } from '../api/rooms'
import { toHttpError, type AuthedFetch } from '../api/http'

/**
 * 房主的房间标题 / 公开性（`/api/rooms/:id/meta` 的读与写）。
 *
 * 为什么单独一个 store，而不是把它塞进 `stores/room.ts`：
 *   `stores/room.ts` 是**房间实时链路**（信令 / WebRTC / 分片 / 播放同步）的唯一 owner，
 *   它有 2600 行与一堆定时器，任何改动都有把播放同步弄坏的风险。标题与公开性是"房间的
 *   静态元数据"，与那台机器没有任何共享状态 —— 唯一的交接是"房间码"，而房间码是参数。
 *   拆开之后这次改动对播放路径是**零字节**的接触。
 *
 * 权限由**服务端**判定，本 store 只负责把裁决翻译成界面能分诊的状态：
 *   - 404（元数据不存在 / 不是房主，服务端用**同一个码**避免存在性探测）→ `notOwner`，
 *     控件收起；
 *   - 503 / 网络失败 → 只写 `error`，**控件留在原位**（否则"后端少配环境变量"会表现成
 *     "房主看不到控件"，排障上是个死胡同）。
 * 本地缓存里的 role 从来不参与这里的判断（一期不变量 I3）。
 */
export const useRoomMetaStore = defineStore('roomMeta', () => {
  /** 当前正在编辑的房间码（换房间时由 `bind` 复位，避免上一个房间的标题串场）。 */
  const roomId = ref('')
  const title = ref('')
  const isPublic = ref(false)

  /** 正在写（PATCH）。 */
  const saving = ref(false)
  /** 正在读（GET）：与 saving 分开，读的时候按钮文案不该说"保存中"。 */
  const loading = ref(false)
  /**
   * 已经成功从服务端读回过一次当前房间的元数据。
   *
   * 为什么需要这个标志：它区分"输入框是空**因为房间确实没标题**"与"输入框是空**因为还没读到**"。
   * 界面据此显示「未命名房间」提示，并据此决定空标题是否是一次有意义的动作。
   */
  const loaded = ref(false)

  const error = ref('')
  const done = ref('')

  /**
   * 服务端裁决"这不是你的房间 / 元数据不存在"。
   *
   * **只有写（PATCH）返回 403/404 时才置位** —— 读的 404 不足以判定（见 `readMissing`）。
   * 用**服务端的裁决**驱动控件的可见性，而不是本地缓存里的 role：缓存的 role 决定的是
   * "显示什么"，它可能过期（刚被降级/刚被转移所有权），拿它藏控件只是经验优化。
   */
  const notOwner = ref(false)

  /**
   * 读（GET）连续 404、重试也没拿到 —— 但**还不确定**是"不是房主"还是"元数据行还没落库"。
   *
   * 服务端对这两种情况用**同一个 404**（避免存在性探测），客户端无法区分；
   * 而 `rooms_meta` 是 `POST /api/rooms` 之后**异步**落库的（§9 事件类），
   * 所以"刚建完房就读"必然可能撞上这个窗口。此时：**不收起控件**、标题按空、
   * 开关按关，也**不显示错误条**（它不是错误，是时序）。真正的裁决留给写操作：
   * 只有 PATCH 也回 403/404，才判定"你不是房主"并收起。
   */
  const readMissing = ref(false)

  /** 请求序号：连续操作时迟到的响应不许覆盖最新一次的结果。 */
  let seq = 0

  const titleLimit = ROOM_TITLE_MAX

  /** 绑定到某个房间（组件挂载或房间码变化时调用）。 */
  function bind(nextRoomId: string): void {
    const id = nextRoomId.trim().toUpperCase()
    if (id === roomId.value) return
    roomId.value = id
    title.value = ''
    isPublic.value = false
    loaded.value = false
    error.value = ''
    done.value = ''
    notOwner.value = false
    readMissing.value = false
  }

  /**
   * 读回当前房间的标题与公开性（房主面板进房后调用一次）。
   *
   * 为什么必须读：服务端才是这两个字段的真相来源，客户端不缓存它们。不读的话
   * "刷新页面后输入框是空的、开关是关的"——用户会以为自己的设置丢了，而更糟的是
   * **下一次保存会把空标题写回服务端**（真的清掉标题）。
   *
   * **为什么要对 404 静默重试**（实测过的时序，不是保险起见）：房间的元数据行是
   * `POST /api/rooms` 之后**异步**落库的（投递即返回，见 ACCOUNTS §9 / I4），
   * 所以"刚建好房就读"会撞上一个短暂的窗口：房间在内存里存在，但库里还没有那一行。
   * 而服务端对 **"不是房主"** 与 **"元数据缺行"** 用的是**同一个 404**
   *（roommeta.go 的 get 明确写了三条来源共用 404，避免存在性探测）。
   *
   * 两者在客户端无法区分，所以 404 **既不能**当成"不是房主"立刻收起控件（症状：
   * "刚建完房刷新一下，标题设置没了"），**也不能**弹错误条（它不是错误）。
   * 处理方式：静默重试；仍然 404 就记 `readMissing`（标题空、开关关、控件都在），
   * 真正的裁决交给写操作 —— PATCH 回 403/404 才收起。
   */
  async function load(authedFetch: AuthedFetch): Promise<void> {
    const id = roomId.value
    if (id === '') return
    seq += 1
    const mine = seq
    loading.value = true
    error.value = ''
    try {
      const meta = await readWithRetry(authedFetch, id, mine)
      if (meta === null || mine !== seq || id !== roomId.value) return
      title.value = meta.title
      isPublic.value = meta.isPublic
      loaded.value = true
      readMissing.value = false
      notOwner.value = false
    } catch (err) {
      if (mine !== seq || id !== roomId.value) return
      const e = toHttpError(err)
      if (e.status === 404) {
        // 重试后仍 404：可能是元数据还没落库，也可能不是房主 —— 两种都**不收起控件**、
        // **不弹错误条**（标题维持空、开关维持关）。真正的裁决交给写操作。
        readMissing.value = true
        return
      }
      error.value = e.message
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** 404 的重试间隔（毫秒）：静默覆盖"元数据行还在异步落库"的那一小段窗口。 */
  const READ_RETRY_DELAYS_MS = [0, 400, 900, 1600]

  /**
   * 带 404 重试的读。返回 null 表示"这次读已作废"（有更新的一次读/写接管了）。
   *
   * 只在 404 上重试：401/403/5xx 都是明确结论，重试只会浪费一次往返。
   */
  async function readWithRetry(
    authedFetch: AuthedFetch,
    id: string,
    mine: number,
  ): Promise<{ title: string; isPublic: boolean } | null> {
    let lastErr: unknown = null
    for (const delay of READ_RETRY_DELAYS_MS) {
      if (delay > 0) await new Promise((resolve) => window.setTimeout(resolve, delay))
      if (mine !== seq || id !== roomId.value) return null
      try {
        return await fetchRoomMeta(authedFetch, id)
      } catch (err) {
        const e = toHttpError(err)
        if (e.status !== 404) throw err
        lastErr = err
      }
    }
    throw lastErr ?? new Error('读取房间元数据失败')
  }

  /**
   * 保存。只带**真的变了**的字段：
   * 服务端对"两项都没传"返回 400，而传了未变的值会让日志/审计里出现一次无意义的写入。
   *
   * **允许空串**：读回闭环之后（`load()` 已成功拿到服务端现值），空标题是一个明确动作，
   * 不再是"没读到"的歧义状态；服务端 schema 里 `title` 的默认值就是空串。
   */
  async function save(authedFetch: AuthedFetch, patch: RoomMetaPatch): Promise<boolean> {
    if (saving.value || roomId.value === '') return false
    seq += 1
    const mine = seq
    const id = roomId.value
    saving.value = true
    error.value = ''
    done.value = ''
    try {
      const meta = await patchRoomMeta(authedFetch, id, patch)
      if (mine !== seq || id !== roomId.value) return false
      // 回显的终态直接同步到本地：不做"乐观更新 + 事后回滚"，因为服务端会清洗标题
      //（去零宽/Bidi、去首尾空白），乐观写下去的值与真正生效的值可能不同。
      title.value = meta.title
      isPublic.value = meta.isPublic
      loaded.value = true
      readMissing.value = false
      notOwner.value = false
      done.value = '已保存'
      return true
    } catch (err) {
      if (mine !== seq || id !== roomId.value) return false
      const e = toHttpError(err)
      error.value = e.message
      // 写操作返回 404/403 才是**权威裁决**："房间元数据不存在 / 你不是房主" → 收起编辑区。
      // 读的 404 不作数（它可能只是异步落库还没到，见 load 的说明）。
      if (e.status === 404 || e.status === 403) {
        notOwner.value = true
      }
      return false
    } finally {
      if (mine === seq) saving.value = false
    }
  }

  const unavailableText = computed(() =>
    notOwner.value ? '当前账号不是这个房间的房主，房间信息由房主管理' : '',
  )

  /** 服务端确认过：这个房间现在**确实没有标题**（不是"还没读到"）。 */
  const unnamed = computed(() => loaded.value && title.value === '')

  return {
    roomId,
    title,
    isPublic,
    saving,
    loading,
    loaded,
    unnamed,
    readMissing,
    error,
    done,
    notOwner,
    unavailableText,
    titleLimit,
    bind,
    load,
    save,
  }
})
