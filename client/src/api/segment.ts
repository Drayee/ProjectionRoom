// 服务端切片服务的前端封装（端点、状态机与错误码见 docs/SEGMENT.md）。
//
// 三条纪律：
//   1. 上传必须用 XMLHttpRequest —— 只有它能在 FormData 上传过程中给出 upload.onprogress 百分比；
//   2. 错误体统一是 {error, code}，这里一律翻译成可读中文，并把 code / Retry-After 暴露给界面；
//   3. 基址是相对路径 /api/...：开发期走 vite.config.ts 的 /api 代理，部署期与页面同源。
//
// 注意：这个模块只负责「开播前的媒体准备」。直播链路（P2P DataChannel）完全不经过它，
// 服务器在直播期依旧不接触任何视频字节。

/** 作业公共前缀；四个端点都由它拼出来。 */
const JOBS_PATH = '/api/v1/segment/jobs'

/** 作业状态机：queued → running → done / failed。 */
export type SegmentJobState = 'queued' | 'running' | 'done' | 'failed'

/** 单次返回上限的产物会被切成多份 zip，manifest 里的一份就是下面这个形状。 */
export interface SegmentPart {
  /** 分批编号（从 1 开始）。服务端 manifest 一定带 n；缺失时由 url 推断。 */
  n?: number
  bytes: number
  sha256: string
  /** 分批下载地址，例如 /api/v1/segment/jobs/XXX/parts/1。 */
  url: string
}

export interface SegmentResult {
  bytes: number
  /** 分片数（打包与否都指分片，不是文件数）。 */
  segments: number
  /**
   * 产物**文件**数（init.mp4 + index.json + 若干个 pack-*.bin）。
   *
   * 打包后文件数远小于分片数（578 片可能只有 8 个文件），写目录的进度必须用它，
   * 否则进度条会一直卡在 2% 左右（分母是分片数、分子是文件数）。
   * 老服务端不返回该字段时退化为 null，由调用方回退到分片数。
   */
  files: number | null
  /** true：GET /result 直接是 zip；false：GET /result 是 manifest，要逐份下载。 */
  singleResponse: boolean
  parts?: SegmentPart[]
}

export interface SegmentJob {
  jobId: string
  state: SegmentJobState
  /** 0–1 的完成度。服务端刻意做成粗粒度（切片没有可靠的细粒度回调）。 */
  progress: number
  /** 排队位次，只在 queued 时非 0。 */
  queuePosition?: number
  /** 失败原因（服务端已写成可读中文）。 */
  error?: string
  result?: SegmentResult
}

/** 错误码 → 给用户看的摘要文案。 */
const CODE_TEXT: Record<string, string> = {
  BAD_REQUEST: '请求不合法：multipart 表单里缺少 file 字段',
  SEGMENT_JOB_NOT_FOUND: '作业不存在，或已经过了保留期被清理（产物默认保留 30 分钟）',
  SEGMENT_JOB_NOT_READY: '作业还没完成，或已经失败',
  SEGMENT_NO_PARTS: '该作业是单次打包返回，没有分批下载',
  SEGMENT_PART_NOT_FOUND: '这一份分批编号不存在',
  SEGMENT_SOURCE_TOO_LARGE: '源视频超过服务器单作业大小上限（默认 16 GiB）',
  SEGMENT_SOURCE_EMPTY: '上传的文件是 0 字节',
  SEGMENT_DURATION_TOO_LONG: '源视频超过服务器时长上限（默认 60 分钟）',
  SEGMENT_PROBE_FAILED: '服务器解不开这个文件：它不是视频，或者已经损坏',
  SEGMENT_QUEUE_FULL: '服务器排队已满，请稍后再试',
  SEGMENT_RATE_LIMITED: '提交太频繁，请稍后再试',
  SEGMENT_FFMPEG_MISSING: '服务器没有安装 ffmpeg，无法在服务端切片',
  INTERNAL: '服务端内部错误',
  NETWORK: '连不上服务器切片服务（/api 不可达）',
  ABORTED: '已取消',
  BAD_RESPONSE: '服务端返回了无法解析的内容',
  POLL_TIMEOUT: '等待切片完成超时',
}

/** 错误码 → 「接下来怎么办」的建议文案。 */
const CODE_HINT: Record<string, string> = {
  BAD_REQUEST: '重试一次即可；若反复出现，请把这条错误发给维护者。',
  SEGMENT_JOB_NOT_FOUND: '重新提交一次作业；产物只保留 30 分钟，下载要趁早。',
  SEGMENT_JOB_NOT_READY: '等作业完成后再下载，或直接重新提交一次。',
  SEGMENT_SOURCE_TOO_LARGE:
    '请用下面的「本地切片教程」在本机切（不占服务器配额），或让服务端调大 PR_SEGMENT_MAX_SOURCE_BYTES。',
  SEGMENT_SOURCE_EMPTY: '换一个文件重试。',
  SEGMENT_DURATION_TOO_LONG:
    '请用下面的「本地切片教程」在本机切，或让服务端调大 PR_SEGMENT_MAX_DURATION。',
  SEGMENT_PROBE_FAILED: '先用 ffprobe -v error <文件> 确认它到底是不是视频；不是就先转封装。',
  SEGMENT_QUEUE_FULL: '服务器同时只切 2 个作业、最多排 8 个；等一会儿再提交。',
  SEGMENT_RATE_LIMITED: '服务器默认 3 个作业/分钟；按提示的秒数等待后再提交。',
  SEGMENT_FFMPEG_MISSING: '改用下面的「本地切片教程」：本机装了 ffmpeg 就能自己切。',
  INTERNAL: '稍后重试；持续失败请把这段错误发给维护者。',
  NETWORK: '确认后端 Go 服务在 127.0.0.1:8080 上运行；下面的本地切片教程不受影响，随时可用。',
}

/** 服务器没装 ffmpeg 时作业会立刻 failed（202 + state=failed），错误体里没有专门的 code。 */
const FFMPEG_MISSING_RE =
  /(未安装|未找到|找不到|缺失|not found|missing)[^，。;；]{0,16}ffmpeg|ffmpeg[^，。;；]{0,16}(未安装|未找到|找不到|缺失|not found|missing)/i

/** 判断一段错误文案是不是"服务器缺 ffmpeg"：是的话界面要引导到本地切片教程。 */
export function isFfmpegMissing(message: string, code = ''): boolean {
  if (code === 'SEGMENT_FFMPEG_MISSING') return true
  return FFMPEG_MISSING_RE.test(message)
}

/** 取错误码对应的中文摘要；没有映射就返回空串。 */
export function segmentCodeText(code: string): string {
  return CODE_TEXT[code] ?? ''
}

export interface SegmentErrorInit {
  status: number
  code: string
  message: string
  hint: string
  retryAfterSec: number
}

/** 切片服务的统一错误：比 Error 多带了 code / status / Retry-After / 处理建议。 */
export class SegmentApiError extends Error {
  readonly status: number
  readonly code: string
  readonly hint: string
  /** 429 时服务端给的 Retry-After（整数秒）；没有就是 0。 */
  readonly retryAfterSec: number

  constructor(init: SegmentErrorInit) {
    super(init.message)
    this.name = 'SegmentApiError'
    this.status = init.status
    this.code = init.code
    this.hint = init.hint
    this.retryAfterSec = init.retryAfterSec
  }

  /** 是不是"服务器缺 ffmpeg"。 */
  get ffmpegMissing(): boolean {
    return isFfmpegMissing(this.message, this.code)
  }
}

function makeError(code: string, message?: string, hint?: string, extra: Partial<SegmentErrorInit> = {}) {
  return new SegmentApiError({
    status: 0,
    code,
    message: message ?? CODE_TEXT[code] ?? '切片服务出错',
    hint: hint ?? CODE_HINT[code] ?? '',
    retryAfterSec: 0,
    ...extra,
  })
}

/** 用户主动取消（上传中止 / 停止轮询）。 */
export function abortedError(): SegmentApiError {
  return makeError('ABORTED')
}

/** 把任意异常归一成 SegmentApiError，组件只需要处理一种错误类型。 */
export function toSegmentError(err: unknown): SegmentApiError {
  if (err instanceof SegmentApiError) return err
  if (typeof DOMException !== 'undefined' && err instanceof DOMException && err.name === 'AbortError') {
    return abortedError()
  }
  const message = err instanceof Error ? err.message : String(err)
  return new SegmentApiError({ status: 0, code: 'UNKNOWN', message, hint: '', retryAfterSec: 0 })
}

// ---------- 响应归一化 ----------

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text) as unknown
  } catch {
    return null
  }
}

/** 服务端的错误文案带 "segment: " 前缀，对用户没有意义，去掉。 */
function cleanServerText(text: string): string {
  return text.trim().replace(/^segment:\s*/i, '')
}

function partNumberFromUrl(url: string): number {
  const matched = /\/parts\/(\d+)(?:[?#]|$)/.exec(url)
  const n = matched ? Number.parseInt(matched[1], 10) : 0
  return Number.isFinite(n) && n > 0 ? n : 0
}

function normalizePart(raw: unknown): SegmentPart | null {
  if (!raw || typeof raw !== 'object') return null
  const part = raw as Partial<SegmentPart>
  const url = typeof part.url === 'string' ? part.url : ''
  if (url === '') return null
  return {
    n: typeof part.n === 'number' && part.n > 0 ? part.n : partNumberFromUrl(url),
    bytes: typeof part.bytes === 'number' ? part.bytes : 0,
    sha256: typeof part.sha256 === 'string' ? part.sha256 : '',
    url,
  }
}

function normalizeResult(raw: unknown): SegmentResult | undefined {
  if (!raw || typeof raw !== 'object') return undefined
  const result = raw as Partial<SegmentResult>
  if (typeof result.bytes !== 'number' || typeof result.segments !== 'number') return undefined

  const parts = Array.isArray(result.parts)
    ? result.parts.map(normalizePart).filter((part): part is SegmentPart => part !== null)
    : []

  return {
    bytes: result.bytes,
    segments: result.segments,
    files: typeof result.files === 'number' && result.files > 0 ? result.files : null,
    // 服务端一定给 singleResponse；万一没给，就按"有没有 parts"推断。
    singleResponse:
      typeof result.singleResponse === 'boolean' ? result.singleResponse : parts.length === 0,
    parts: parts.length > 0 ? parts : undefined,
  }
}

function normalizeJob(raw: unknown): SegmentJob {
  const body = (raw ?? {}) as Partial<SegmentJob>
  return {
    jobId: typeof body.jobId === 'string' ? body.jobId : '',
    state: (body.state ?? 'queued') as SegmentJobState,
    progress: typeof body.progress === 'number' ? body.progress : 0,
    queuePosition: typeof body.queuePosition === 'number' ? body.queuePosition : undefined,
    error: typeof body.error === 'string' && body.error !== '' ? cleanServerText(body.error) : undefined,
    result: normalizeResult(body.result),
  }
}

function errorFromResponse(status: number, retryAfterHeader: string | null, text: string): SegmentApiError {
  const body = parseJson(text) as { error?: string; code?: string } | null
  const code = typeof body?.code === 'string' && body.code !== '' ? body.code : `HTTP_${status}`
  const serverText = typeof body?.error === 'string' ? cleanServerText(body.error) : ''
  const seconds = retryAfterHeader ? Number.parseInt(retryAfterHeader, 10) : Number.NaN

  return new SegmentApiError({
    status,
    code,
    message: serverText !== '' ? serverText : (CODE_TEXT[code] ?? `请求失败（HTTP ${status}）`),
    hint: CODE_HINT[code] ?? '',
    retryAfterSec: Number.isFinite(seconds) && seconds > 0 ? seconds : 0,
  })
}

// ---------- 网络原语 ----------

async function doFetch(url: string, init: RequestInit = {}): Promise<Response> {
  let res: Response
  try {
    res = await fetch(url, init)
  } catch {
    if (init.signal?.aborted) throw abortedError()
    throw makeError('NETWORK')
  }
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw errorFromResponse(res.status, res.headers.get('Retry-After'), text)
  }
  return res
}

async function readJsonOrThrow(res: Response): Promise<unknown> {
  const text = await res.text().catch(() => '')
  const body = parseJson(text)
  if (body === null) throw makeError('BAD_RESPONSE', `服务端返回了非 JSON 内容：${text.slice(0, 120)}`)
  return body
}

function filenameFromDisposition(disposition: string | null, fallback: string): string {
  if (!disposition) return fallback
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(disposition)
  if (encoded) {
    try {
      return decodeURIComponent(encoded[1])
    } catch {
      // 编码坏了就退回普通 filename。
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(disposition)
  return plain ? plain[1] : fallback
}

// ---------- 端点 1：提交作业（multipart 上传） ----------

export interface SegmentSubmitOptions {
  /** 上传进度回调，0–100。 */
  onUploadProgress?: (percent: number) => void
  /** 中止上传（AbortController.signal）。 */
  signal?: AbortSignal
}

/**
 * 提交切片作业：POST /api/v1/segment/jobs（multipart/form-data，字段名固定 file）。
 *
 * 成功是 202：通常 state=queued/running；但服务器没装 ffmpeg 时也会 202 + state=failed，
 * 所以调用方必须同时检查 state 与 error，不能只看 HTTP 状态码。
 */
export function submitSegmentJob(file: File, options: SegmentSubmitOptions = {}): Promise<SegmentJob> {
  return new Promise<SegmentJob>((resolve, reject) => {
    const signal = options.signal
    if (signal?.aborted) {
      reject(abortedError())
      return
    }

    const form = new FormData()
    // 字段名必须叫 file：internal/handler/segment.go 就是按这个名字找那一段的。
    form.append('file', file, file.name)

    const xhr = new XMLHttpRequest()
    xhr.open('POST', JOBS_PATH, true)
    // 成功与失败都是 JSON；拿到 HTML 说明请求没走到切片端点（例如代理没配对）。
    xhr.setRequestHeader('Accept', 'application/json')

    const onAbort = () => xhr.abort()
    signal?.addEventListener('abort', onAbort, { once: true })
    const cleanup = () => signal?.removeEventListener('abort', onAbort)

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && event.total > 0) {
        options.onUploadProgress?.(Math.min(100, (event.loaded / event.total) * 100))
      }
    }

    xhr.onabort = () => {
      cleanup()
      reject(abortedError())
    }
    xhr.onerror = () => {
      cleanup()
      reject(makeError('NETWORK'))
    }
    xhr.ontimeout = () => {
      cleanup()
      reject(makeError('NETWORK', '上传超时'))
    }
    xhr.onload = () => {
      cleanup()
      const body = parseJson(xhr.responseText)
      if (xhr.status === 202 && body && typeof (body as SegmentJob).jobId === 'string') {
        options.onUploadProgress?.(100)
        resolve(normalizeJob(body))
        return
      }
      reject(errorFromResponse(xhr.status, xhr.getResponseHeader('Retry-After'), xhr.responseText))
    }

    xhr.send(form)
  })
}

// ---------- 端点 2：查询作业状态 ----------

export interface SegmentFetchOptions {
  signal?: AbortSignal
}

/** 查询作业状态：GET /api/v1/segment/jobs/{id}。 */
export async function getSegmentJob(jobId: string, options: SegmentFetchOptions = {}): Promise<SegmentJob> {
  const res = await doFetch(`${JOBS_PATH}/${encodeURIComponent(jobId)}`, {
    signal: options.signal,
    headers: { Accept: 'application/json' },
  })
  return normalizeJob(await readJsonOrThrow(res))
}

/** 建议的轮询间隔：docs/SEGMENT.md §6 建议 1 秒一次。 */
export const SEGMENT_POLL_INTERVAL_MS = 1000
/** 服务端单作业上限是 60 分钟，这里给一点余量。 */
export const SEGMENT_POLL_TIMEOUT_MS = 65 * 60 * 1000

export interface SegmentPollOptions {
  /** 每次拿到新状态都会回调一次，便于界面实时刷新。 */
  onUpdate?: (job: SegmentJob) => void
  signal?: AbortSignal
  intervalMs?: number
  timeoutMs?: number
}

function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(abortedError())
      return
    }
    const onAbort = () => {
      window.clearTimeout(timer)
      reject(abortedError())
    }
    const timer = window.setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

/**
 * 轮询到终态：done 或 failed 都原样返回（由调用方决定怎么展示）。
 * 期间遇到 404（TTL 到期被清理）之类的错误会抛 SegmentApiError。
 */
export async function pollSegmentJob(jobId: string, options: SegmentPollOptions = {}): Promise<SegmentJob> {
  const interval = options.intervalMs ?? SEGMENT_POLL_INTERVAL_MS
  const deadline = Date.now() + (options.timeoutMs ?? SEGMENT_POLL_TIMEOUT_MS)

  for (;;) {
    await delay(interval, options.signal)
    const job = await getSegmentJob(jobId, { signal: options.signal })
    options.onUpdate?.(job)
    if (job.state === 'done' || job.state === 'failed') return job
    if (Date.now() > deadline) throw makeError('POLL_TIMEOUT')
  }
}

// ---------- 端点 3 / 4：取产物 ----------

/** GET /api/v1/segment/jobs/{id}/result 的直链（交给浏览器流式落盘，不经过 JS 内存）。 */
export function segmentResultUrl(jobId: string): string {
  return `${JOBS_PATH}/${encodeURIComponent(jobId)}/result`
}

/** GET /api/v1/segment/jobs/{id}/parts/{n} 的直链。 */
export function segmentPartUrl(jobId: string, n: number): string {
  return `${JOBS_PATH}/${encodeURIComponent(jobId)}/parts/${n}`
}

/** 取某一份的分批编号：manifest 里没给 n 就从 url 推断。 */
export function segmentPartNumber(part: SegmentPart): number {
  return part.n && part.n > 0 ? part.n : partNumberFromUrl(part.url)
}

/** /result 的两种形态：≤1GiB 是 zip，>1GiB 是 manifest。 */
export type SegmentArtifact =
  | { kind: 'zip'; blob: Blob; filename: string }
  | { kind: 'manifest'; manifest: SegmentResult }

/** 拉取产物：GET /api/v1/segment/jobs/{id}/result（按 Content-Type 区分 zip / manifest）。 */
export async function fetchSegmentResult(
  jobId: string,
  options: SegmentFetchOptions = {},
): Promise<SegmentArtifact> {
  const res = await doFetch(segmentResultUrl(jobId), {
    signal: options.signal,
    headers: { Accept: 'application/zip, application/json' },
  })
  const contentType = res.headers.get('Content-Type') ?? ''

  if (contentType.includes('json')) {
    const manifest = normalizeResult(await readJsonOrThrow(res))
    if (!manifest) throw makeError('BAD_RESPONSE', 'manifest 缺少必要字段')
    return { kind: 'manifest', manifest }
  }

  const blob = await res.blob()
  return {
    kind: 'zip',
    blob,
    filename: filenameFromDisposition(res.headers.get('Content-Disposition'), `room-media-${jobId}.zip`),
  }
}

export interface SegmentPartDownload {
  blob: Blob
  /** Content-Length（服务端分批下载一定带）。 */
  bytes: number
  /** X-Segment-Sha256：与 manifest 里的 sha256 逐字节一致，可用于校验。 */
  sha256: string
}

/** 拉取第 n 份 zip：GET /api/v1/segment/jobs/{id}/parts/{n}。 */
export async function fetchSegmentPartZip(
  jobId: string,
  n: number,
  options: SegmentFetchOptions = {},
): Promise<SegmentPartDownload> {
  const res = await doFetch(segmentPartUrl(jobId, n), { signal: options.signal })
  const length = Number.parseInt(res.headers.get('Content-Length') ?? '', 10)
  return {
    blob: await res.blob(),
    bytes: Number.isFinite(length) && length > 0 ? length : 0,
    sha256: res.headers.get('X-Segment-Sha256') ?? '',
  }
}

// ---------- 可用性探测 ----------

/**
 * 探测服务端切片服务是否可达。
 *
 * 用一个必然不存在的作业号去查：能拿到 404 + code=SEGMENT_JOB_NOT_FOUND，就说明
 * /api 已经被代理到 Go 服务、而且这些路由确实注册了。返回 false 时界面只提示
 * "服务器切片不可用"，本地切片教程照旧可用。
 */
export async function probeSegmentService(timeoutMs = 5000): Promise<boolean> {
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), timeoutMs)
  try {
    const res = await fetch(`${JOBS_PATH}/__probe__`, {
      signal: controller.signal,
      headers: { Accept: 'application/json' },
    })
    if (res.status !== 404) return false
    const body = (await res.json().catch(() => null)) as { code?: string } | null
    return body?.code === 'SEGMENT_JOB_NOT_FOUND'
  } catch {
    return false
  } finally {
    window.clearTimeout(timer)
  }
}
