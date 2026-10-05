// 与 Go 端 internal/model/index.go 一一对应的分片索引模型（SPEC §4.3）。
// 这份数据由 cmd/segmenter 生成，主播发布后经服务端校验并广播给全房。

export interface MediaSegment {
  /** 从 1 开始；0 保留给 init 段。 */
  index: number
  /**
   * 包含该分片的文件名：未打包时是分片自己的名字（"c00001.m4s"），
   * 打包时是所属包（"pack-0001.bin"）。
   */
  file: string
  /** 该分片在 file 里的字节偏移（未打包时是它在原始视频里的偏移）。 */
  offset: number
  /** 分片字节数（始终是"这一片"的大小，与是否打包无关）。 */
  size: number
  duration: number
  startPts: number
  keyframe: boolean
  sha256: string
}

/** 一个分片包：把连续 count 个分片合成一个 .bin（见 docs/SEGMENT.md §6）。 */
export interface MediaPack {
  /** 包文件名，例如 "pack-0001.bin"。 */
  file: string
  /** 包里第一个分片的序号（从 1 开始）。 */
  firstSegment: number
  /** 包里的分片个数。 */
  count: number
  /** 包的字节数。 */
  bytes: number
}

export interface MediaIndex {
  version: number
  initFile: string
  mimeType: string
  totalDuration: number
  segmentSec: number
  bitrateBps: number
  totalBytes: number
  segments: MediaSegment[]
  /**
   * 可选：分片打包清单。省略或为空表示"一片一个文件"的经典布局。
   * 非空时 segments[i].file/offset 指向所属包与包内偏移，客户端按
   * `file.slice(offset, offset + size)` 读取单个分片（不把整包读进内存）。
   */
  packs?: MediaPack[]
}

/** 索引是否使用打包布局。 */
export function isPackedIndex(index: MediaIndex): boolean {
  return Array.isArray(index.packs) && index.packs.length > 0
}

/**
 * 校验打包布局的自洽性：包按序覆盖全部分片（不重不漏）、分片落在自己所属的包里、
 * 包内偏移递增且不重叠、且不越过 pack.bytes。
 *
 * 判据与 Go 侧 model.Index.validatePacks 一致 —— 偏移写错时 MSE 只会抛一个
 * 看不懂的 appendBuffer 错误，所以必须在开播前拦住。
 */
function validatePacks(index: MediaIndex, packs: MediaPack[]): string | null {
  const seen = new Set<string>()
  let next = 1

  for (let i = 0; i < packs.length; i += 1) {
    const pack = packs[i]
    if (!pack || typeof pack.file !== 'string' || pack.file === '') return `第 ${i + 1} 个 pack 缺少文件名`
    if (seen.has(pack.file)) return `pack 文件名重复：${pack.file}`
    seen.add(pack.file)

    if (!Number.isInteger(pack.firstSegment) || pack.firstSegment !== next) {
      return `第 ${i + 1} 个 pack 的 firstSegment 应为 ${next}，实际 ${pack.firstSegment}`
    }
    if (!Number.isInteger(pack.count) || pack.count < 1) return `pack ${pack.file} 的分片个数非法`
    if (!(pack.bytes > 0)) return `pack ${pack.file} 的字节数非法`
    if (pack.firstSegment + pack.count - 1 > index.segments.length) {
      return `pack ${pack.file} 覆盖的分片超出索引范围`
    }

    // covered 是包内已被覆盖到的字节位置：下一片必须从它之后开始。
    let covered = 0
    for (let k = pack.firstSegment; k < pack.firstSegment + pack.count; k += 1) {
      const seg = index.segments[k - 1]
      if (seg.file !== pack.file) return `分片 ${k} 的 file 应为 ${pack.file}，实际 ${seg.file}`
      if (!(seg.offset >= covered)) return `分片 ${k} 在 ${pack.file} 内的偏移与前一片重叠`
      if (seg.offset + seg.size > pack.bytes) return `分片 ${k} 越过 ${pack.file} 的边界`
      covered = seg.offset + seg.size
    }

    next = pack.firstSegment + pack.count
  }

  if (next !== index.segments.length + 1) {
    return `packs 只覆盖了 ${next - 1} 片，索引里有 ${index.segments.length} 片`
  }
  return null
}

/** 客户端校验：与服务端 media.Index.Validate 保持同一套判据，避免半路才发现问题。 */
export function validateMediaIndex(index: MediaIndex): string | null {
  if (!index || typeof index !== 'object') return '索引为空'
  if (!index.version) return '索引版本非法'
  if (!index.initFile) return '索引缺少 initFile'
  if (!index.mimeType) return '索引缺少 mimeType'
  if (!(index.totalDuration > 0)) return '索引缺少有效总时长'
  if (!(index.bitrateBps > 0)) return '索引缺少有效码率（容量模型依赖它）'
  if (!Array.isArray(index.segments) || index.segments.length === 0) return '索引不包含任何分片'

  for (let i = 0; i < index.segments.length; i += 1) {
    const seg = index.segments[i]
    if (seg.index !== i + 1) return `第 ${i} 个分片序号应为 ${i + 1}，实际 ${seg.index}`
    if (!(seg.size > 0)) return `分片 ${seg.index} 大小非法`
    if (!seg.file) return `分片 ${seg.index} 缺少文件名`
  }

  // 两种形态都接受：有 packs 就必须自洽，没有 packs 就是逐片一个文件的经典布局。
  if (isPackedIndex(index)) {
    const packed = validatePacks(index, index.packs as MediaPack[])
    if (packed) return packed
  }

  return null
}

/** 返回覆盖播放位置 time（秒）的分片序号（从 1 开始）。 */
export function segmentIndexAt(index: MediaIndex, time: number): number {
  const segments = index.segments
  if (segments.length === 0) return 0

  let low = 0
  let high = segments.length - 1
  let result = segments[segments.length - 1].index
  while (low <= high) {
    const mid = (low + high) >> 1
    const seg = segments[mid]
    if (time < seg.startPts + seg.duration) {
      result = seg.index
      high = mid - 1
    } else {
      low = mid + 1
    }
  }
  return result
}

/** 返回分片在时间轴上的区间，用于 seek 时清理缓冲。 */
export function segmentRange(index: MediaIndex, segmentIndex: number): { start: number; end: number } | null {
  const seg = index.segments.find((s) => s.index === segmentIndex)
  if (!seg) return null
  return { start: seg.startPts, end: seg.startPts + seg.duration }
}
