// 与 Go 端 internal/media/index.go 一一对应的分片索引模型（SPEC §4.3）。
// 这份数据由 cmd/segmenter 生成，主播发布后经服务端校验并广播给全房。

export interface MediaSegment {
  /** 从 1 开始；0 保留给 init 段。 */
  index: number
  file: string
  offset: number
  size: number
  duration: number
  startPts: number
  keyframe: boolean
  sha256: string
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
