<script setup lang="ts">
// 服务端切片面板：把源视频上传给服务器切成分片，再下载 / 写进本地目录直接开播。
//
// 适用场景：主播本机没有 ffmpeg。整条链路只发生在"开播前的媒体准备"阶段，
// 直播期仍然是 P2P，服务器不接触任何视频字节。
//
// 降级策略（任何一步失败都不挡用户）：
//   1. /api 不可达 → 只提示"服务器切片不可用"，下面的本地切片教程照旧可用；
//   2. 服务器没装 ffmpeg（作业立刻 failed）→ 同一位置引导到本地切片教程；
//   3. 浏览器不支持 File System Access API → 退化为"下载 zip + 手动解压后选目录"。

import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  SegmentApiError,
  fetchSegmentPartZip,
  fetchSegmentResult,
  isFfmpegMissing,
  pollSegmentJob,
  probeSegmentService,
  segmentCodeText,
  segmentPartNumber,
  segmentPartUrl,
  segmentResultUrl,
  submitSegmentJob,
  toSegmentError,
  type SegmentJob,
  type SegmentPart,
  type SegmentResult,
} from '../api/segment'
import { extractZipToDirectory } from '../api/segmentZip'
import { pickDirectory, supportsFileSystemAccess } from '../api/fileSystemAccess'
import { useRoomStore } from '../stores/room'
import SliceToolPanel from './SliceToolPanel.vue'

const store = useRoomStore()

/** 服务端切片是否可用（进面板时探测一次）。 */
const reachable = ref<'checking' | 'ok' | 'unavailable'>('checking')

const fileInput = ref<HTMLInputElement | null>(null)
const tutorialEl = ref<HTMLDetailsElement | null>(null)
const file = ref<File | null>(null)

/** idle → uploading → queued → running → done / failed。 */
const phase = ref<'idle' | 'uploading' | 'queued' | 'running' | 'done' | 'failed'>('idle')
const uploadPercent = ref(0)
const job = ref<SegmentJob | null>(null)

const errorText = ref('')
const errorHint = ref('')
const errorCode = ref('')
const retryAfterSec = ref(0)
const ffmpegMissing = ref(false)
const notice = ref('')

const writing = ref(false)
const writeDone = ref(0)
const writeTotal = ref(0)
const writeCurrent = ref('')
const publishNotice = ref('')
/** 「重新切片」是破坏性动作：先内联确认一次，别让一次误点丢掉已经切好的产物。 */
const resliceConfirm = ref(false)

let controller: AbortController | null = null

const fsSupported = supportsFileSystemAccess()

const busy = computed(() => phase.value === 'uploading' || phase.value === 'queued' || phase.value === 'running')
const result = computed<SegmentResult | null>(() => job.value?.result ?? null)
const parts = computed<SegmentPart[]>(() =>
  result.value && !result.value.singleResponse ? (result.value.parts ?? []) : [],
)
const writePercent = computed(() =>
  writeTotal.value > 0 ? Math.min(100, Math.round((writeDone.value / writeTotal.value) * 100)) : 0,
)

const progressPercent = computed(() => {
  if (phase.value === 'uploading') return Math.round(uploadPercent.value)
  const raw = job.value?.progress ?? 0
  return Math.round(Math.min(1, Math.max(0, raw)) * 100)
})

const stateText = computed(() => {
  switch (phase.value) {
    case 'uploading':
      return `正在上传源视频（${Math.round(uploadPercent.value)}%）`
    case 'queued': {
      const position = job.value?.queuePosition ?? 0
      return position > 0 ? `服务器排队中：前面还有 ${position - 1} 个作业` : '服务器排队中'
    }
    case 'running':
      return '服务器正在切片'
    case 'done':
      return '切片完成'
    case 'failed':
      return '切片失败'
    default:
      return ''
  }
})

/** 主按钮文案：进行中态直接写在按钮上，不用用户去猜为什么点不动。 */
const startLabel = computed(() => {
  switch (phase.value) {
    case 'uploading':
      return '正在上传…'
    case 'queued':
      return '排队中…'
    case 'running':
      return '切片中…'
    case 'done':
      return '重新切片'
    default:
      return '上传并切片'
  }
})

/** 主按钮：已切完时先要一次确认，其余情况直接开跑。 */
function onPrimary() {
  if (phase.value === 'done') {
    resliceConfirm.value = true
    return
  }
  resliceConfirm.value = false
  void start()
}

function confirmReslice() {
  resliceConfirm.value = false
  void start()
}

onMounted(async () => {
  reachable.value = (await probeSegmentService()) ? 'ok' : 'unavailable'
})

/** 服务端后来才起来时，用户可以手动重试探测。 */
async function recheck() {
  reachable.value = 'checking'
  reachable.value = (await probeSegmentService()) ? 'ok' : 'unavailable'
}

onBeforeUnmount(() => {
  controller?.abort()
})

function humanBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value.toFixed(unit === 0 || value >= 100 ? 0 : 1)} ${units[unit]}`
}

function chooseFile() {
  fileInput.value?.click()
}

function onFileChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const picked = input.files?.[0] ?? null
  // 清空 input.value：同一个文件再选一次也要能触发 change。
  input.value = ''
  if (!picked) return

  resetRun()
  file.value = picked
  if (picked.size === 0) {
    phase.value = 'failed'
    errorText.value = '这个文件是 0 字节，请换一个真实的视频文件。'
  }
}

function resetRun() {
  job.value = null
  uploadPercent.value = 0
  errorText.value = ''
  errorHint.value = ''
  errorCode.value = ''
  retryAfterSec.value = 0
  ffmpegMissing.value = false
  notice.value = ''
  publishNotice.value = ''
  resliceConfirm.value = false
  writeDone.value = 0
  writeTotal.value = 0
  writeCurrent.value = ''
}

function applyJob(next: SegmentJob) {
  job.value = next
  if (next.state === 'failed') {
    const message = next.error ?? '服务器切片失败'
    phase.value = 'failed'
    errorText.value = message
    errorHint.value = isFfmpegMissing(message)
      ? '改用下面的「本地切片教程」：本机装了 ffmpeg 就能自己切。'
      : ''
    errorCode.value = ''
    ffmpegMissing.value = isFfmpegMissing(message)
    return
  }
  phase.value = next.state
  if (next.state === 'done') {
    errorText.value = ''
    errorHint.value = ''
    errorCode.value = ''
    ffmpegMissing.value = false
  }
}

function applyError(err: SegmentApiError) {
  phase.value = 'failed'
  errorText.value = err.message
  errorHint.value = err.hint
  errorCode.value = err.code
  retryAfterSec.value = err.retryAfterSec
  ffmpegMissing.value = err.ffmpegMissing
}

/** 服务端的时长上限（与 internal/config 默认值一致）：本地先判，省得白传几个 GB。 */
const MAX_UPLOAD_DURATION_SEC = 60 * 60

/**
 * 本地读时长：临时 `<video>` 读 metadata，不占内存、不上传。
 * 容器不被浏览器识别（或超时）时返回 null，交给服务端去判，不挡用户。
 */
function probeLocalMedia(file: File): Promise<{ durationSec: number | null }> {
  return new Promise((resolve) => {
    const url = URL.createObjectURL(file)
    const el = document.createElement('video')
    el.preload = 'metadata'

    let settled = false
    const done = (durationSec: number | null) => {
      if (settled) return
      settled = true
      URL.revokeObjectURL(url)
      el.removeAttribute('src')
      resolve({ durationSec })
    }

    el.onloadedmetadata = () => done(Number.isFinite(el.duration) ? el.duration : null)
    el.onerror = () => done(null)
    // 有些容器浏览器根本解不了（例如 mkv 里的 AV1），metadata 永远不来，别把按钮卡死。
    window.setTimeout(() => done(null), 4000)
    el.src = url
  })
}

async function start() {
  const picked = file.value
  if (!picked || busy.value || writing.value) return
  if (picked.size === 0) {
    phase.value = 'failed'
    errorText.value = '这个文件是 0 字节，请换一个真实的视频文件。'
    return
  }

  resetRun()
  phase.value = 'uploading'
  controller = new AbortController()

  try {
    // 上传前先本地读时长：服务端的时长上限只有在文件传完之后才判得了，
    // 而为了被拒先传 4GB 是纯粹的浪费（142 分钟的素材就是这么被拒的）。
    const local = await probeLocalMedia(picked)
    if (local.durationSec !== null && local.durationSec > MAX_UPLOAD_DURATION_SEC) {
      phase.value = 'failed'
      errorText.value =
        `这个视频约 ${Math.round(local.durationSec / 60)} 分钟，超过服务端 ${Math.round(MAX_UPLOAD_DURATION_SEC / 60)} 分钟的上限，` +
        `没必要白传一遍。请自行按下面的「本地切片教程」切好，或换一段更短的视频。`
      return
    }

    const submitted = await submitSegmentJob(picked, {
      onUploadProgress: (percent) => {
        uploadPercent.value = percent
      },
      signal: controller.signal,
    })
    // 202 也可能是"服务器没装 ffmpeg"导致的立刻 failed，所以这里必须看 state。
    applyJob(submitted)
    if (submitted.state === 'done' || submitted.state === 'failed') return

    const final = await pollSegmentJob(submitted.jobId, {
      onUpdate: (update) => applyJob(update),
      signal: controller.signal,
    })
    applyJob(final)
  } catch (err) {
    const apiError = toSegmentError(err)
    if (apiError.code === 'ABORTED') {
      phase.value = 'idle'
      notice.value =
        '已取消。上传被中断时服务器不会留下作业；如果已经在切，服务器会把这一轮切完，产物 30 分钟后自动清理。'
      return
    }
    applyError(apiError)
  } finally {
    controller = null
  }
}

/** 上传中 = 取消上传；排队/切片中 = 停止轮询（服务端作业不受影响）。 */
function cancel() {
  controller?.abort()
}

/** 取消写入：中止后续取产物与写盘；已经落进目录的文件不会回滚（重新写入会覆盖）。 */
function cancelWrite() {
  controller?.abort()
}

function triggerDownload(url: string, filename: string) {
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.rel = 'noopener'
  document.body.appendChild(link)
  link.click()
  link.remove()
}

/** ≤1GiB：直接让浏览器流式下载 zip，字节不经过 JS 内存。 */
function downloadZip() {
  const current = job.value
  if (!current) return
  triggerDownload(segmentResultUrl(current.jobId), `room-media-${current.jobId}.zip`)
}

/** >1GiB：逐份下载（每份都是 zip，解压到同一个目录即可，文件互不重复）。 */
function downloadPart(part: SegmentPart) {
  const current = job.value
  if (!current) return
  const n = segmentPartNumber(part)
  const url = part.url !== '' ? part.url : segmentPartUrl(current.jobId, n)
  triggerDownload(url, `room-media-${current.jobId}-part${String(n).padStart(3, '0')}.zip`)
}

/**
 * 依次下载全部分份：浏览器对"一次点击触发多个下载"会弹权限提示，
 * 这里用 500ms 间隔错开，并且始终把单份按钮留在界面上以便重试。
 */
async function downloadAllParts() {
  const list = parts.value
  for (const part of list) {
    downloadPart(part)
    await new Promise((resolve) => window.setTimeout(resolve, 500))
  }
}

function openTutorial() {
  const el = tutorialEl.value
  if (!el) return
  el.open = true
  el.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
}

/**
 * 写入本地目录并直接开播。
 *
 * 与"下载 zip 手动解压"相比，这里省掉了用户再选一次目录：
 * 产物写完就地读回 File，直接交给既有的开播路径（store.publishMediaFiles）。
 */
async function writeAndPublish() {
  const current = job.value
  const artifact = result.value
  if (!current || !artifact || writing.value) return

  publishNotice.value = ''
  if (!fsSupported) {
    publishNotice.value =
      '当前浏览器不支持 File System Access API，请用上面的「下载」拿到 zip，手动解压后再用「选择分片目录」开播。'
    return
  }

  let dir: FileSystemDirectoryHandle
  try {
    dir = await pickDirectory()
  } catch (err) {
    // 用户取消选目录不是错误。
    if (err instanceof DOMException && err.name === 'AbortError') return
    publishNotice.value = `无法写入目录：${(err as Error).message}`
    return
  }

  writing.value = true
  writeDone.value = 0
  // 进度分母必须是**文件**数：打包后 578 片可能只有 8 个文件（pack-*.bin），
  // 拿分片数当分母会让进度条一直停在 2% 左右。
  // 老服务端不返回 files 时回退到"分片数 + 2"，只是进度偏保守，不会算错。
  const expectedFiles = artifact.files ?? artifact.segments + 2
  writeTotal.value = expectedFiles
  writeCurrent.value = ''
  controller = new AbortController()

  try {
    const names: string[] = []

    if (artifact.singleResponse) {
      const fetched = await fetchSegmentResult(current.jobId, { signal: controller.signal })
      if (fetched.kind !== 'zip') {
        throw new Error('服务端返回的是 manifest 而不是 zip（产物可能刚刚超过单次返回上限），请改用逐份下载')
      }
      names.push(
        ...(await extractZipToDirectory(fetched.blob, dir, (progress) => {
          writeDone.value = progress.done
          writeTotal.value = progress.total
          writeCurrent.value = progress.name
        })),
      )
    } else {
      const list = artifact.parts ?? []
      if (list.length === 0) throw new Error('manifest 里没有任何分批，无法写入目录')
      let done = 0
      for (const part of list) {
        const n = segmentPartNumber(part)
        if (n <= 0) throw new Error('manifest 里某一批缺少编号，无法下载')
        const download = await fetchSegmentPartZip(current.jobId, n, { signal: controller.signal })
        const written = await extractZipToDirectory(download.blob, dir, (progress) => {
          writeCurrent.value = progress.name
          writeDone.value = done + progress.done
          writeTotal.value = Math.max(expectedFiles, progress.total)
        })
        names.push(...written)
        done += written.length
      }
    }

    if (names.length === 0) throw new Error('产物里没有任何文件')

    // 从目录里读回 File：File 是懒加载的磁盘引用，不会把整部片子再塞进内存。
    const files: File[] = []
    for (const name of names) {
      const handle = await dir.getFileHandle(name)
      files.push(await handle.getFile())
    }

    await store.publishMediaFiles(files)
    if (store.mediaError) {
      publishNotice.value = `已经写入本地目录，但开播失败：${store.mediaError}`
    } else {
      publishNotice.value = `已写入 ${names.length} 个文件并发布到房间，可以直接播放了。`
    }
  } catch (err) {
    const apiError = toSegmentError(err)
    if (apiError.code === 'ABORTED') {
      publishNotice.value = '已取消写入（目录里可能留下部分文件，重新写入会覆盖）。'
      return
    }
    errorText.value = apiError.message
    errorHint.value = apiError.hint
    errorCode.value = apiError.code
  } finally {
    writing.value = false
    controller = null
  }
}
</script>

<template>
  <div class="segment">
    <header>
      <h3>服务端切片</h3>
      <span
        class="badge"
        :class="{ ok: reachable === 'ok', danger: reachable === 'unavailable' }"
      >
        {{
          reachable === 'checking'
            ? '检测中…'
            : reachable === 'ok'
              ? '服务端可用'
              : '服务器切片不可用'
        }}
      </span>
    </header>

    <p class="muted small" v-if="reachable === 'unavailable'">
      连不上 /api/v1/segment/jobs：后端 Go 服务没起来，或者 Vite 没代理 /api。上传与切片暂时不可用，
      但下面的「切片工具下载」与「本地切片教程」只依赖静态托管的二进制与本机 ffmpeg，照旧可用。
    </p>
    <div class="row" v-if="reachable === 'unavailable'">
      <button @click="recheck">重新检测</button>
    </div>

    <template v-else>
      <div class="row">
        <input
          ref="fileInput"
          class="hidden-input"
          type="file"
          accept="video/*"
          @change="onFileChosen"
        />
        <button :disabled="busy || writing" @click="chooseFile">选择视频文件</button>
        <span v-if="file" class="muted small mono">{{ file.name }} · {{ humanBytes(file.size) }}</span>
        <span v-else class="muted small">选一个源视频（服务器上限：16GiB / 60 分钟）</span>
      </div>

      <div class="row">
        <button
          class="primary"
          :disabled="!file || busy || writing"
          :aria-busy="busy"
          @click="onPrimary"
        >
          {{ startLabel }}
        </button>
        <button v-if="busy" type="button" @click="cancel">
          {{ phase === 'uploading' ? '取消上传' : '停止轮询' }}
        </button>
        <span class="muted small" v-if="busy">服务器同时只切 2 个、最多排 8 个，人均 3 个作业/分钟。</span>
        <span class="muted small" v-else-if="phase === 'done'">
          重新切片会重新上传源视频、丢掉当前这份产物（服务器产物本来也只保留 30 分钟）。
        </span>
      </div>

      <div class="row confirm-row" v-if="resliceConfirm">
        <span class="warn-text small">确定要重新切片吗？当前进度与产物会被丢弃。</span>
        <button class="primary" type="button" @click="confirmReslice">确认重新切片</button>
        <button type="button" @click="resliceConfirm = false">取消</button>
      </div>

      <div class="progress-block" v-if="busy || phase === 'done'">
        <div class="progress-line">
          <span>{{ stateText }}</span>
          <span class="mono">{{ progressPercent }}%</span>
        </div>
        <div class="bar"><div class="fill" :style="{ width: progressPercent + '%' }"></div></div>
      </div>

      <p class="muted small" v-if="notice">{{ notice }}</p>

      <div class="error-block" v-if="errorText">
        <p class="error-text">{{ errorText }}</p>
        <p class="muted small" v-if="errorCode">
          {{ errorCode }}<span v-if="segmentCodeText(errorCode)"> · {{ segmentCodeText(errorCode) }}</span>
        </p>
        <p class="muted small" v-if="retryAfterSec > 0">
          服务端要求 {{ retryAfterSec }} 秒后再试。
        </p>
        <p class="muted small" v-if="errorHint">{{ errorHint }}</p>
        <button v-if="ffmpegMissing" @click="openTutorial">查看本地切片教程</button>
      </div>

      <div class="done-block" v-if="phase === 'done' && result">
        <p class="ok-text">
          切片完成：{{ result.segments }} 段 · {{ humanBytes(result.bytes) }}
          <span v-if="!result.singleResponse">· {{ parts.length }} 份</span>
        </p>

        <div class="row" v-if="result.singleResponse">
          <button class="primary" @click="downloadZip">下载 zip</button>
          <span class="muted small">解压到任意目录后，用上面的「选择分片目录」即可开播；或直接写入目录（见下）。</span>
        </div>

        <template v-else>
          <p class="muted small">
            产物超过服务器单次返回上限，需要逐份下载：每份都是 zip，<b>解压到同一个目录</b>即可
            （各份覆盖的文件互不重复）。
          </p>
          <ul class="parts">
            <li v-for="part in parts" :key="part.url">
              <button class="link" @click="downloadPart(part)">
                第 {{ segmentPartNumber(part) }} 份
              </button>
              <span class="muted small mono">{{ humanBytes(part.bytes) }}</span>
              <span class="muted small mono" :title="part.sha256">sha256 {{ part.sha256.slice(0, 12) }}…</span>
            </li>
          </ul>
          <div class="row">
            <button @click="downloadAllParts">依次下载全部分份</button>
          </div>
        </template>

        <div class="row">
          <button
            class="primary"
            :disabled="writing || !fsSupported || !store.joined"
            @click="writeAndPublish"
          >
            {{ writing ? '正在写入…' : '写入本地目录并开播' }}
          </button>
          <span v-if="!fsSupported" class="muted small">
            当前浏览器不支持 File System Access API：请下载 zip、手动解压，再用「选择分片目录」开播。
          </span>
          <span v-else-if="!store.joined" class="muted small">房间还没连上，连上后再写入即可直接开播。</span>
          <span v-else class="muted small">会弹出一个目录选择框，产物全部写进去后立刻发布到房间。</span>
        </div>

        <div class="progress-block" v-if="writing">
          <div class="progress-line">
            <span>正在写入 {{ writeCurrent }}</span>
            <span class="mono">{{ writeDone }} / {{ writeTotal }}</span>
          </div>
          <div class="bar"><div class="fill" :style="{ width: writePercent + '%' }"></div></div>
          <div class="row">
            <button type="button" @click="cancelWrite">取消写入</button>
            <span class="muted small">
              取消后已经写进目录的文件会留下（重新写入会覆盖），也不会发布到房间。
            </span>
          </div>
        </div>

        <p class="ok-text small" v-if="publishNotice">{{ publishNotice }}</p>
      </div>
    </template>

    <!-- 切片工具下载：与「本地切片教程」并列的推荐路径。
         只读 /api/downloads/segmenter（拿不到就显示中文提示），不依赖切片接口（它在 v-else 之外）。 -->
    <SliceToolPanel />

    <details class="tutorial" ref="tutorialEl">
      <summary>本机没有 ffmpeg？本地切片教程（手写命令，不占用服务器）</summary>

      <p class="muted small">
        本机装了 ffmpeg 时优先本地切：更快，也不占服务器磁盘与 CPU。产物格式与服务端完全一致，
        主播端"选择分片目录"的行为没有任何变化。
      </p>

      <h4>1. 先确认本机有没有 ffmpeg</h4>
      <pre class="mono">ffmpeg -version</pre>

      <h4>2. 一步 ffmpeg 生成 fragmented MP4（H.264/AAC 时无损、秒级）</h4>
      <pre class="mono">ffmpeg -i input.mp4 -c copy ^
  -movflags +frag_keyframe+empty_moov+default_base_moof ^
  -frag_duration 2000000 ^
  out_frag.mp4</pre>
      <p class="muted small">
        分片目标 2 秒（-frag_duration 单位是微秒，要 4 秒就写 4000000）。源视频不是 H.264/AAC
        （HEVC / AV1 / VP9 / 其它音频）时，MSE 放不了，必须先转码：
      </p>
      <pre class="mono">ffmpeg -i input.mp4 -c:v libx264 -preset veryfast -crf 23 -c:a aac -b:a 128k ^
  -movflags +frag_keyframe+empty_moov+default_base_moof ^
  -frag_duration 2000000 ^
  out_frag.mp4</pre>
      <p class="muted small">
        以上命令在 Windows 的 cmd / PowerShell 里用 ^ 续行；macOS / Linux 终端里换成 \。
      </p>

      <h4>3. 用 cmd/segmenter 切成分片目录（三种用法）</h4>
      <pre class="mono">REM 1) 已经是 fragmented MP4：直接按 moof 边界切（这一步不需要 ffmpeg）
go run ./cmd/segmenter -in out_frag.mp4 -out ./room-media

REM 2) 普通 MP4：先无损重新封装成 fMP4，再切
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -fragment

REM 3) 低上行预设：转码降码率后再切（长视频耗时数分钟）
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -transcode 1200k -uplink-mbps 3</pre>
      <p class="muted small">
        常用参数：-frag-sec 分片秒数（默认 2）、-ffmpeg 指定 ffmpeg 路径（目录或可执行文件）。
        产物是 room-media/ 下的 index.json、init.mp4、c00001.m4s…，正好是上面「选择分片目录」要的目录。
      </p>

      <h4>4. 常见报错</h4>
      <ul class="muted small">
        <li>提示"不是 fragmented MP4" → 输入是普通 MP4，加 -fragment，或先跑第 2 步。</li>
        <li>提示"暂不支持 hvc1/hev1/vp09/av01" → 视频编码不是 H.264，用 ffmpeg -c:v libx264 -c:a aac 转码后再切。</li>
        <li>提示"未找到 ffmpeg" → 安装 ffmpeg 并加入 PATH，或用 -ffmpeg 指定路径。</li>
        <li>提示"ffmpeg 执行失败: Invalid data found" → 文件损坏或根本不是视频，先用 ffprobe -v error ［文件］ 确认。</li>
      </ul>
    </details>
  </div>
</template>

<style scoped>
.segment {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 12px;
  margin-bottom: 10px;
  background: var(--panel-2);
}

header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 8px;
}

h3 {
  margin: 0;
  font-size: 13px;
}

h4 {
  margin: 12px 0 6px;
  font-size: 12px;
  color: var(--text-dim);
}

.row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}

.hidden-input {
  display: none;
}

.small {
  font-size: 12px;
}

.muted.small,
p.muted {
  margin: 6px 0;
}

p.muted.small {
  line-height: 1.6;
}

.progress-block {
  margin: 8px 0;
}

.confirm-row {
  border-left: 2px solid var(--accent-2);
  padding-left: 10px;
  margin: 8px 0;
}

.warn-text {
  color: var(--accent-2);
}

.progress-line {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 4px;
}

.bar {
  height: 6px;
  border-radius: 999px;
  background: var(--panel);
  border: 1px solid var(--border);
  overflow: hidden;
}

.fill {
  height: 100%;
  background: var(--accent);
  transition: width 0.2s linear;
}

.error-block {
  border-left: 2px solid var(--danger);
  padding-left: 10px;
  margin: 8px 0;
}

.error-text {
  margin: 0;
  color: var(--danger);
  font-size: 12px;
  line-height: 1.6;
  word-break: break-word;
}

.ok-text {
  margin: 0 0 8px;
  color: var(--ok);
  font-size: 12px;
  line-height: 1.6;
  word-break: break-word;
}

.done-block {
  border-top: 1px dashed var(--border);
  padding-top: 8px;
}

.parts {
  list-style: none;
  margin: 0 0 8px;
  padding: 0;
  max-height: 180px;
  overflow-y: auto;
}

.parts li {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 3px 0;
}

button.link {
  padding: 2px 8px;
  font-size: 12px;
}

.tutorial {
  border-top: 1px dashed var(--border);
  margin-top: 6px;
  padding-top: 8px;
}

.tutorial summary {
  cursor: pointer;
  font-size: 12px;
  color: var(--accent);
}

pre {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 10px;
  font-size: 12px;
  line-height: 1.55;
  overflow-x: auto;
  white-space: pre;
  margin: 0 0 6px;
}

.tutorial ul {
  margin: 0;
  padding-left: 18px;
  line-height: 1.7;
}
</style>
