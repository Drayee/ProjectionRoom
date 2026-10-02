import { ref } from 'vue'
import { validateMediaIndex } from '../types/media'
import type { MediaIndex } from '../types/media'

export interface LoadedMedia {
  index: MediaIndex
  initFile: File
  segments: Map<number, File>
}

/**
 * 主播端的媒体准备：选择一个由 cmd/segmenter 产出的分片目录。
 *
 * 三个必须先拦住的问题（SPEC §4.3）：
 *   1. 目录里没有 index.json（没经过 segmenter 预处理）；
 *   2. 索引不自洽（序号断档、缺文件）；
 *   3. 浏览器不支持该编码 —— 必须在开播前用 isTypeSupported 判定，
 *      否则会在运行期变成一块黑屏。
 */
export function useMediaIndex() {
  const index = ref<MediaIndex | null>(null)
  const loading = ref(false)

  let initFile: File | null = null
  const segments = new Map<number, File>()

  async function loadDirectory(files: FileList | File[]): Promise<LoadedMedia> {
    loading.value = true
    try {
      const list = Array.from(files)
      if (list.length === 0) {
        throw new Error('没有选择任何文件')
      }

      const indexFile = list.find((file) => file.name === 'index.json')
      if (!indexFile) {
        throw new Error('目录里没有 index.json：请先用 cmd/segmenter 预处理视频')
      }

      let parsed: MediaIndex
      try {
        parsed = JSON.parse(await indexFile.text()) as MediaIndex
      } catch {
        throw new Error('index.json 不是合法 JSON')
      }

      const invalid = validateMediaIndex(parsed)
      if (invalid) {
        throw new Error(`索引不合法：${invalid}`)
      }

      if (typeof MediaSource === 'undefined') {
        throw new Error('当前浏览器不支持 MediaSource，无法播放分片流')
      }
      if (!MediaSource.isTypeSupported(parsed.mimeType)) {
        throw new Error(`浏览器不支持该编码：${parsed.mimeType}。可用 segmenter -transcode 转成 H.264/AAC`)
      }

      const byName = new Map(list.map((file) => [file.name, file]))
      const init = byName.get(parsed.initFile)
      if (!init) {
        throw new Error(`缺少初始化段 ${parsed.initFile}`)
      }

      const next = new Map<number, File>()
      for (const seg of parsed.segments) {
        const file = byName.get(seg.file)
        if (!file) {
          throw new Error(`缺少分片文件 ${seg.file}`)
        }
        next.set(seg.index, file)
      }

      index.value = parsed
      initFile = init
      segments.clear()
      for (const [key, value] of next) {
        segments.set(key, value)
      }

      return { index: parsed, initFile: init, segments }
    } finally {
      loading.value = false
    }
  }

  /** 读取某个分片的内容（0 表示 init 段）。主播按需读取，不把整部片子读进内存。 */
  async function readChunk(segmentIndex: number): Promise<Uint8Array<ArrayBuffer> | null> {
    const file = segmentIndex === 0 ? initFile : segments.get(segmentIndex)
    if (!file) {
      return null
    }
    return new Uint8Array(await file.arrayBuffer())
  }

  function reset() {
    index.value = null
    initFile = null
    segments.clear()
  }

  return { index, loading, loadDirectory, readChunk, reset, segmentCount: () => segments.size }
}
