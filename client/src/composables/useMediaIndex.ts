import { ref } from 'vue'
import { isPackedIndex, validateMediaIndex } from '../types/media'
import type { MediaIndex } from '../types/media'

export interface LoadedMedia {
  index: MediaIndex
  initFile: File
  segments: Map<number, File>
}

/** 取出编码串的"编码族"：av01.0.12M.10 → av01，avc1.64001f → avc1，opus → opus。 */
function codecFamily(codec: string): string {
  const dot = codec.indexOf('.')
  return (dot > 0 ? codec.slice(0, dot) : codec).trim()
}

/** 拆出 `type/subtype; codecs="a,b"` 的承载类型与编码串；拆不开返回 null。 */
function splitMimeType(mimeType: string): { base: string; codecs: string[]; quoted: boolean } | null {
  const match = /^\s*([^;]+?)\s*;\s*codecs\s*=\s*(?:"([^"]*)"|([^";]+))\s*$/i.exec(mimeType)
  if (!match) return null

  const raw = match[2] ?? match[3] ?? ''
  const codecs = raw
    .split(',')
    .map((codec) => codec.trim())
    .filter((codec) => codec !== '')
  if (codecs.length === 0) return null

  return { base: match[1].trim(), codecs, quoted: match[2] !== undefined }
}

function renderMimeType(base: string, codecs: string[], quoted: boolean): string {
  const list = codecs.join(',')
  return `${base}; codecs=${quoted ? `"${list}"` : list}`
}

/**
 * 按"从精确到宽松"的顺序给出候选 mimeType，第一个能被浏览器接受的才是真正要用的。
 *
 * 为什么需要：index.json 里的编码串是按**真实码流**算出来的（例如 av01.0.12M.10），
 * 而 `isTypeSupported` 在不同浏览器/版本上接受的粒度不一样：
 *   - 有的只认编码族（av01 / vp09 / avc1），带上 profile/level/位深就一律返回 false；
 *   - 有的要求必须带参数，只给编码族反而不认。
 * 只试精确串的话，第二种情况会直接黑屏；这里两种都试，全失败才报错。
 */
export function mimeTypeCandidates(mimeType: string): string[] {
  const parsed = splitMimeType(mimeType)
  if (!parsed) return [mimeType]

  const candidates: string[] = [mimeType]
  const push = (codecs: string[]) => {
    const rendered = renderMimeType(parsed.base, codecs, parsed.quoted)
    if (!candidates.includes(rendered)) {
      candidates.push(rendered)
    }
  }

  // 1) 全部降级成编码族：av01.0.12M.10,opus → av01,opus
  push(parsed.codecs.map(codecFamily))

  // 2) 只降级其中一条：音频串写错时不该把视频串一起丢掉（反之亦然）
  for (let i = 0; i < parsed.codecs.length; i += 1) {
    const codecs = parsed.codecs.slice()
    codecs[i] = codecFamily(codecs[i])
    push(codecs)
  }

  return candidates
}

/** 返回第一个浏览器能接受的候选串；全部失败返回 null。 */
function pickSupportedMimeType(mimeType: string): string | null {
  for (const candidate of mimeTypeCandidates(mimeType)) {
    if (MediaSource.isTypeSupported(candidate)) {
      return candidate
    }
  }
  return null
}

/** 只用于错误文案：把 mimeType 里的编码串列出来。 */
function describeCodecs(mimeType: string): string {
  return splitMimeType(mimeType)?.codecs.join(',') ?? mimeType
}

/**
 * 主播端的媒体准备：选择一个由 cmd/segmenter 产出的分片目录。
 *
 * 三个必须先拦住的问题（SPEC §4.3）：
 *   1. 目录里没有 index.json（没经过 segmenter 预处理）；
 *   2. 索引不自洽（序号断档、缺文件）；
 *   3. 浏览器不支持该编码 —— 必须在开播前用 isTypeSupported 判定，
 *      否则会在运行期变成一块黑屏。判定时先试索引里的精确串，再降级成编码族
 *      （av01.0.12M.10 → av01），两者都试过才敢说"不支持"。
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

      // 精确串 → 编码族串逐级降级，第一个能过的就是真正要 append 的 mimeType。
      const supportedMime = pickSupportedMimeType(parsed.mimeType)
      if (!supportedMime) {
        throw new Error(
          `这段视频是 ${describeCodecs(parsed.mimeType)} 编码，你的浏览器不支持；` +
            `可让主播用 -Transcode（或 segmenter -transcode）转成 H.264/AAC 后重开。`,
        )
      }
      if (supportedMime !== parsed.mimeType) {
        // 降级串才是浏览器认的。这里改写的是**内存里的索引**（JSON 文件不动），
        // 它会随 MediaIndex 一起广播给观众 —— 主播与观众必须用同一个串，
        // 否则观众端 addSourceBuffer 会用一个自己没验证过的串。
        parsed = { ...parsed, mimeType: supportedMime }
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
