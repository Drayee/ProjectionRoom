import { ref, shallowRef } from 'vue'

/**
 * MediaSource 播放器（SPEC §4.2）。
 *
 * 三条硬约束决定了这里的写法：
 *   1. 首个 appendBuffer 必须是 init 段（ftyp+moov），之后每个 buffer 必须从一个完整 moof 开始；
 *   2. appendBuffer 在 updating 期间会抛错，因此所有写入必须串行排队；
 *   3. seek 需要先 remove 已缓冲区间再重灌，不能直接改 currentTime 了事。
 */
export function useChunkPlayer() {
  const attached = ref(false)
  const ready = ref(false)
  const error = ref('')
  const queued = ref(0)
  const initAppended = ref(false)
  const stalls = ref(0)

  const video = shallowRef<HTMLVideoElement | null>(null)

  let mediaSource: MediaSource | null = null
  let sourceBuffer: SourceBuffer | null = null
  let objectUrl = ''
  const queue: ArrayBuffer[] = []
  let pumpBound: (() => void) | null = null

  function pump() {
    queued.value = queue.length
    if (!sourceBuffer || sourceBuffer.updating || queue.length === 0) {
      return
    }
    const buf = queue.shift() as ArrayBuffer
    queued.value = queue.length
    try {
      sourceBuffer.appendBuffer(buf)
    } catch (err) {
      error.value = `appendBuffer 失败：${(err as Error).message}`
      queue.length = 0
      queued.value = 0
    }
  }

  async function attach(el: HTMLVideoElement, mimeType: string): Promise<void> {
    detach()
    video.value = el

    if (typeof MediaSource === 'undefined') {
      error.value = '当前浏览器不支持 MediaSource'
      return
    }
    if (!MediaSource.isTypeSupported(mimeType)) {
      error.value = `浏览器不支持该编码：${mimeType}`
      return
    }

    const ms = new MediaSource()
    mediaSource = ms
    objectUrl = URL.createObjectURL(ms)
    el.src = objectUrl

    await new Promise<void>((resolve, reject) => {
      const timer = window.setTimeout(() => reject(new Error('MediaSource 打开超时')), 5000)
      ms.addEventListener(
        'sourceopen',
        () => {
          window.clearTimeout(timer)
          resolve()
        },
        { once: true },
      )
    }).catch((err: Error) => {
      error.value = err.message
      throw err
    })

    if (ms.readyState !== 'open') {
      error.value = `MediaSource 状态异常：${ms.readyState}`
      return
    }

    sourceBuffer = ms.addSourceBuffer(mimeType)
    sourceBuffer.mode = 'segments'
    pumpBound = pump
    sourceBuffer.addEventListener('updateend', pumpBound)
    sourceBuffer.addEventListener('error', () => {
      error.value = 'SourceBuffer 追加失败（分片可能不是合法的 fMP4 片段）'
    })

    el.addEventListener('waiting', () => {
      stalls.value += 1
    })

    attached.value = true
    ready.value = true
  }

  /** 把一段分片排入写入队列。init 段与媒体分片走同一个队列，顺序由调用方保证。 */
  function append(type: number, payload: ArrayBuffer) {
    queue.push(payload)
    if (type === 0x01) {
      initAppended.value = true
    }
    pump()
  }

  function bufferedEnd(): number {
    const ranges = sourceBuffer?.buffered
    if (!ranges || ranges.length === 0) {
      return 0
    }
    return ranges.end(ranges.length - 1)
  }

  /** 当前位置前方的可播时长（秒），是抖动缓冲的健康度指标。 */
  function bufferedAhead(time: number): number {
    const ranges = sourceBuffer?.buffered
    if (!ranges) {
      return 0
    }
    for (let i = 0; i < ranges.length; i += 1) {
      if (time >= ranges.start(i) && time <= ranges.end(i)) {
        return ranges.end(i) - time
      }
    }
    return 0
  }

  function removeRange(start: number, end: number): Promise<void> {
    return new Promise((resolve) => {
      if (!sourceBuffer || sourceBuffer.updating || end <= start) {
        resolve()
        return
      }
      const sb = sourceBuffer
      const onEnd = () => {
        sb.removeEventListener('updateend', onEnd)
        resolve()
      }
      sb.addEventListener('updateend', onEnd)
      try {
        sb.remove(start, end)
      } catch {
        sb.removeEventListener('updateend', onEnd)
        resolve()
      }
    })
  }

  /**
   * 清空全部已缓冲区间（不动播放位置）。
   *
   * 注意不要用它来"定位"：清空后缓冲为空，此时给 currentTime 赋值会被浏览器丢弃
   *（HTML 规范：readyState 为 HAVE_NOTHING 时只记录默认起始位置）。
   * 正确的跳转顺序是 clearBuffered → 补齐目标分片 → seekTo（见 store 的 seekPipeline）。
   */
  async function clearBuffered(): Promise<void> {
    queue.length = 0
    queued.value = 0

    if (!sourceBuffer || !mediaSource || mediaSource.readyState !== 'open') {
      return
    }

    const ranges: Array<[number, number]> = []
    const buffered = sourceBuffer.buffered
    for (let i = 0; i < buffered.length; i += 1) {
      ranges.push([buffered.start(i), buffered.end(i)])
    }
    for (const [start, end] of ranges) {
      await removeRange(start, end)
    }
  }

  /** 设置播放位置。必须在目标位置已经有缓冲数据时调用，否则会被浏览器丢弃。 */
  function seekTo(time: number) {
    if (video.value) {
      video.value.currentTime = time
    }
  }

  /** 在已缓冲区间内跳到最接近 target 的关键帧位置（不清缓冲，代价最低）。 */
  function jumpWithinBuffer(target: number): boolean {
    const ranges = sourceBuffer?.buffered
    if (!ranges || !video.value) {
      return false
    }
    for (let i = 0; i < ranges.length; i += 1) {
      if (target >= ranges.start(i) && target <= ranges.end(i)) {
        video.value.currentTime = target
        return true
      }
    }
    return false
  }

  function detach() {
    if (sourceBuffer && pumpBound) {
      sourceBuffer.removeEventListener('updateend', pumpBound)
      pumpBound = null
    }
    queue.length = 0
    queued.value = 0
    attached.value = false
    ready.value = false
    initAppended.value = false
    sourceBuffer = null

    if (video.value) {
      try {
        video.value.pause()
      } catch {
        // 元素可能已经被卸载，忽略。
      }
      video.value.removeAttribute('src')
    }
    if (objectUrl) {
      URL.revokeObjectURL(objectUrl)
      objectUrl = ''
    }
    mediaSource = null
  }

  return {
    video,
    attached,
    ready,
    error,
    queued,
    initAppended,
    stalls,
    attach,
    append,
    bufferedEnd,
    bufferedAhead,
    clearBuffered,
    seekTo,
    jumpWithinBuffer,
    detach,
  }
}
