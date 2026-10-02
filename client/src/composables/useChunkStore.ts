import { ref } from 'vue'

/**
 * 观众端的分片仓库：只存已经收到的 ArrayBuffer。
 *
 * 主播端不使用它 —— 主播的"仓库"就是磁盘上的 File，
 * 按需 File.slice().arrayBuffer() 读取，避免把整部片子读进内存（SPEC §4.4）。
 */
export function useChunkStore() {
  const buffers = ref(new Map<number, ArrayBuffer>())
  const owned = ref(0)

  let initChunk: ArrayBuffer | null = null

  function putInit(data: ArrayBuffer) {
    initChunk = data
  }

  function getInit(): ArrayBuffer | null {
    return initChunk
  }

  function hasInit(): boolean {
    return initChunk !== null
  }

  /** 存入一个分片；已存在时返回 false。 */
  function put(index: number, data: ArrayBuffer): boolean {
    if (buffers.value.has(index)) {
      return false
    }
    buffers.value.set(index, data)
    owned.value = buffers.value.size
    return true
  }

  /** 已拥有的分片序号列表（用于生成 have 位图）。 */
  function indices(): number[] {
    return [...buffers.value.keys()]
  }

  function has(index: number): boolean {
    return buffers.value.has(index)
  }

  function get(index: number): ArrayBuffer | null {
    return buffers.value.get(index) ?? null
  }

  /** 丢弃已经播过的分片，避免长视频把内存吃光。 */
  function evictBefore(index: number) {
    for (const key of buffers.value.keys()) {
      if (key < index) {
        buffers.value.delete(key)
      }
    }
    owned.value = buffers.value.size
  }

  function reset() {
    buffers.value.clear()
    owned.value = 0
    initChunk = null
  }

  return { owned, hasInit, putInit, getInit, put, has, get, indices, evictBefore, reset, size: () => buffers.value.size }
}


