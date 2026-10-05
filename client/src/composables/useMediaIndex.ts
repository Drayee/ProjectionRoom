import { ref } from 'vue'
import { isPackedIndex, validateMediaIndex } from '../types/media'
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
 *
 * 目录有两种合法形态，都必须读得进来：
 *   - 逐片一个文件（`-pack 1`）：c00001.m4s…，segments[i].file 就是分片文件；
 *   - 打包（默认）：pack-0001.bin…，segments[i].file/offset/size 指出分片在包内的切片。
 */
export function useMediaIndex() {
  const index = ref<MediaIndex | null>(null)
  const loading = ref(false)

  let initFile: File | null = null
  const segments = new Map<number, File>()
  /**
   * 分片 → 它在所属 .bin 里的切片位置。只在打包布局下填充：
   * 未打包时一个分片就是一个文件，读整份即可（此时 segments[i].offset 是分片在
   * 原始视频里的偏移，对读取没有意义，绝不能拿它去 slice）。
   */
  let slices = new Map<number, { offset: number; size: number }>()

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

      const packed = isPackedIndex(parsed)
      if (packed) {
        // 先把所有的包文件确认一遍：缺一个包就等于缺它包含的那 100 片，
        // 必须在开播前说清楚，而不是等到观众拉到那一片才 NOT_FOUND。
        for (const pack of parsed.packs ?? []) {
          if (!byName.has(pack.file)) {
            throw new Error(`缺少分片包 ${pack.file}`)
          }
        }
      }

      const next = new Map<number, File>()
      const nextSlices = new Map<number, { offset: number; size: number }>()
      for (const seg of parsed.segments) {
        const file = byName.get(seg.file)
        if (!file) {
          throw new Error(packed ? `缺少分片包 ${seg.file}` : `缺少分片文件 ${seg.file}`)
        }
        next.set(seg.index, file)
        if (packed) {
          nextSlices.set(seg.index, { offset: seg.offset, size: seg.size })
        }
      }

      index.value = parsed
      initFile = init
      segments.clear()
      for (const [key, value] of next) {
        segments.set(key, value)
      }
      slices = nextSlices

      return { index: parsed, initFile: init, segments }
    } finally {
      loading.value = false
    }
  }

  /** 读取某个分片的内容（0 表示 init 段）。主播按需读取，不把整部片子读进内存。 */
  async function readChunk(segmentIndex: number): Promise<Uint8Array<ArrayBuffer> | null> {
    if (segmentIndex === 0) {
      if (!initFile) {
        return null
      }
      return new Uint8Array(await initFile.arrayBuffer())
    }

    const file = segments.get(segmentIndex)
    if (!file) {
      return null
    }

    const slice = slices.get(segmentIndex)
    if (!slice) {
      // 未打包：分片就是一个独立文件。
      return new Uint8Array(await file.arrayBuffer())
    }
    // 打包：只把这一片读出来。File.slice 直接透传到磁盘，不会把整个 .bin 读进内存
    // —— 一个 100 片的包可能有几百 MB。
    return new Uint8Array(await file.slice(slice.offset, slice.offset + slice.size).arrayBuffer())
  }

  function reset() {
    index.value = null
    initFile = null
    segments.clear()
    slices = new Map()
  }

  return { index, loading, loadDirectory, readChunk, reset, segmentCount: () => segments.size }
}
