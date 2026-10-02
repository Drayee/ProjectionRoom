// 极简 zip 读取器：只用浏览器平台能力（Blob.slice + DecompressionStream），不引第三方解压库。
//
// 为什么需要它：服务端产物（GET /result 或 GET /parts/{n}）都是 zip，而"写入本地分片目录并直接开播"
// 需要的是拆出来的 index.json / init.mp4 / c00001.m4s…。服务端的压缩方式是固定的
//（internal/service/segment/artifacts.go：index.json 用 deflate，其余用 store），
// 所以这里只需要支持 store 与 deflate-raw 两种。
//
// 内存策略：整包以 Blob 形式持有（大文件由浏览器自己落到 blob 存储，不必然占住 JS 堆），
// 读取时用 Blob.slice 按需取范围，store 条目直接把切片交给 FileSystemWritableFileStream。

const SIG_EOCD = 0x06054b50
const SIG_CENTRAL = 0x02014b50
const SIG_LOCAL = 0x04034b50
const EOCD_MIN_BYTES = 22
const MAX_COMMENT_BYTES = 0xffff
const CENTRAL_HEADER_BYTES = 46
const LOCAL_HEADER_BYTES = 30

/** zip 解析失败（不是 zip / 结构不支持）。 */
export class ZipError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ZipError'
  }
}

export interface ZipEntry {
  name: string
  blob: Blob
}

interface CentralEntry {
  name: string
  method: number
  compressedSize: number
  localOffset: number
}

/** 读 tail 里的 EOCD（中央目录结尾记录），拿到中央目录的位置与条目数。 */
async function readEocd(blob: Blob): Promise<{ centralOffset: number; centralSize: number; entries: number }> {
  const tailLength = Math.min(blob.size, EOCD_MIN_BYTES + MAX_COMMENT_BYTES)
  if (tailLength < EOCD_MIN_BYTES) {
    throw new ZipError('zip 太小，不是合法的压缩包')
  }
  const tail = new DataView(await blob.slice(blob.size - tailLength).arrayBuffer())

  for (let pos = tail.byteLength - EOCD_MIN_BYTES; pos >= 0; pos -= 1) {
    if (tail.getUint32(pos, true) !== SIG_EOCD) continue

    const entries = tail.getUint16(pos + 10, true)
    const centralSize = tail.getUint32(pos + 12, true)
    const centralOffset = tail.getUint32(pos + 16, true)

    // Zip64 只在单包超过 4GiB 或条目数超过 65535 时出现；服务端单次返回上限是 1GiB，
    // 60 分钟 / 2 秒最多 1800 个分片，都够不到。真遇到了就直接说清楚，别猜着解析。
    if (entries === 0xffff || centralSize === 0xffffffff || centralOffset === 0xffffffff) {
      throw new ZipError('这个 zip 用了 Zip64 扩展，浏览器内解压暂不支持；请改用「下载 zip」后手动解压')
    }
    return { centralOffset, centralSize, entries }
  }
  throw new ZipError('不是合法的 zip：找不到中央目录结尾记录')
}

function basename(name: string): string {
  const normalized = name.replace(/\\/g, '/')
  const slash = normalized.lastIndexOf('/')
  return slash >= 0 ? normalized.slice(slash + 1) : normalized
}

async function readCentralEntries(blob: Blob, centralOffset: number, centralSize: number): Promise<CentralEntry[]> {
  const raw = await blob.slice(centralOffset, centralOffset + centralSize).arrayBuffer()
  const view = new DataView(raw)
  const bytes = new Uint8Array(raw)
  const decoder = new TextDecoder('utf-8')
  const entries: CentralEntry[] = []

  let pos = 0
  while (pos + CENTRAL_HEADER_BYTES <= view.byteLength) {
    if (view.getUint32(pos, true) !== SIG_CENTRAL) break

    const method = view.getUint16(pos + 10, true)
    const compressedSize = view.getUint32(pos + 20, true)
    const nameLength = view.getUint16(pos + 28, true)
    const extraLength = view.getUint16(pos + 30, true)
    const commentLength = view.getUint16(pos + 32, true)
    const localOffset = view.getUint32(pos + 42, true)

    const nameStart = pos + CENTRAL_HEADER_BYTES
    const name = decoder.decode(bytes.subarray(nameStart, nameStart + nameLength))

    if (compressedSize === 0xffffffff || localOffset === 0xffffffff) {
      throw new ZipError('这个 zip 用了 Zip64 扩展，浏览器内解压暂不支持；请改用「下载 zip」后手动解压')
    }
    // 目录条目（以 / 结尾）不是产物，跳过。
    if (!name.endsWith('/')) {
      entries.push({ name: basename(name), method, compressedSize, localOffset })
    }
    pos = nameStart + nameLength + extraLength + commentLength
  }
  return entries
}

/** 取某个条目真正的数据起点：本地文件头的长度是可变的（文件名 + 扩展字段）。 */
async function localDataOffset(blob: Blob, entry: CentralEntry): Promise<number> {
  const header = new DataView(await blob.slice(entry.localOffset, entry.localOffset + LOCAL_HEADER_BYTES).arrayBuffer())
  if (header.byteLength < LOCAL_HEADER_BYTES || header.getUint32(0, true) !== SIG_LOCAL) {
    throw new ZipError(`zip 条目 ${entry.name} 的本地头损坏`)
  }
  const nameLength = header.getUint16(26, true)
  const extraLength = header.getUint16(28, true)
  return entry.localOffset + LOCAL_HEADER_BYTES + nameLength + extraLength
}

/** 用平台的 DecompressionStream 解 raw deflate（zip 的 method=8 就是裸 deflate 流）。 */
async function inflateRaw(data: Blob): Promise<Blob> {
  if (typeof DecompressionStream === 'undefined') {
    throw new ZipError('当前浏览器不支持 DecompressionStream，无法在浏览器内解压；请改用「下载 zip」后手动解压')
  }
  const stream = new DecompressionStream('deflate-raw')
  const output = new Response(stream.readable)
  const [, blob] = await Promise.all([data.stream().pipeTo(stream.writable), output.blob()])
  return blob
}

/**
 * 把 zip 拆成条目列表（保持中央目录里的顺序，也就是服务端的写入顺序：
 * index.json → init.mp4 → c00001.m4s → …）。
 */
export async function readZipEntries(blob: Blob): Promise<ZipEntry[]> {
  const eocd = await readEocd(blob)
  const central = await readCentralEntries(blob, eocd.centralOffset, eocd.centralSize)

  const entries: ZipEntry[] = []
  for (const entry of central) {
    const start = await localDataOffset(blob, entry)
    const raw = blob.slice(start, start + entry.compressedSize)
    if (entry.method === 0) {
      entries.push({ name: entry.name, blob: raw })
    } else if (entry.method === 8) {
      entries.push({ name: entry.name, blob: await inflateRaw(raw) })
    } else {
      throw new ZipError(`zip 条目 ${entry.name} 用了不支持的压缩方式 ${entry.method}（只支持 store/deflate）`)
    }
  }
  return entries
}

export interface ExtractProgress {
  name: string
  /** 已写出的条目数。 */
  done: number
  /** 这个 zip 里的条目总数。 */
  total: number
}

/**
 * 把 zip 解到一个本地目录里。
 *
 * 单个文件用 dirHandle.getFileHandle(name, {create:true}) + createWritable() 直写：
 * store 条目直接把 Blob 切片交给写入流，不做多余的拷贝。
 * 返回真正的文件名列表（不含目录），调用方可以据此再 getFileHandle().getFile() 拿到 File。
 */
export async function extractZipToDirectory(
  blob: Blob,
  dir: FileSystemDirectoryHandle,
  onProgress?: (progress: ExtractProgress) => void,
): Promise<string[]> {
  const entries = await readZipEntries(blob)
  const written: string[] = []

  for (let i = 0; i < entries.length; i += 1) {
    const entry = entries[i]
    const handle = await dir.getFileHandle(entry.name, { create: true })
    const writable = await handle.createWritable()
    try {
      await writable.write(entry.blob)
    } finally {
      await writable.close()
    }
    written.push(entry.name)
    onProgress?.({ name: entry.name, done: i + 1, total: entries.length })
  }
  return written
}
