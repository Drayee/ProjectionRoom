<script setup lang="ts">
// 一键切片脚本面板：在浏览器里拼一个 .ps1 / .sh，用户在**自己机器**上跑它完成切片。
//
// 脚本的主路线是「下载服务端发布的 segmenter 可执行文件（校验 sha256）→ 调它切片」，
// 所以这个面板会在挂载时拉一次 GET /api/downloads/segmenter，把清单烘焙进脚本。
// 拉不到**不影响生成**：脚本会自动退回内置的 HLS-fMP4 路径（同样带防呆断言），
// 即"服务端连不上、断网也能生成一份能用的脚本"这条老规矩继续成立。
//
// 组件只做四件事：
//   1. 收集参数并用 validateScriptParams 做即时校验（中文错误列表）；
//   2. 拉 segmenter 清单（可选）；
//   3. 给一个可折叠的只读预览（等宽、超高滚动）；
//   4. 落盘 / 复制：Windows 版必须 UTF-8 **带 BOM**，否则 PowerShell 按 ANSI 解码，
//      中文注释全乱码（已实测），这是本组件唯一"看起来多余但必须有"的细节。
//
// 为什么一直在强调"浏览器拿不到完整路径"：File 对象只有 name，
// 而脚本要在用户自己的磁盘上找到那个文件，所以路径只能靠拖拽 / 粘贴 / 按文件名搜索。

import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  DEFAULT_FFMPEG_URLS,
  baseNameOf,
  buildBashScript,
  buildPowerShellScript,
  stemOf,
  validateScriptParams,
  type ScriptParams,
  type ScriptPlatform,
  type SegmenterDownload,
} from '../api/segmentScript'
import { copyText } from '../utils/clipboard'

const platform = ref<ScriptPlatform>('windows')

const sourceName = ref('')
const sourceSize = ref(0)
const sourcePath = ref('')
const outputDir = ref('room-media')
const segmentSeconds = ref(2)
const packSize = ref(100)
const transcodeBitrate = ref('')
const ffmpegUrl = ref('')

/** 服务端发布的 segmenter 清单；空数组 = 脚本会走内置 HLS-fMP4 兜底路径。 */
const segmenterDownloads = ref<SegmenterDownload[]>([])

const fileInput = ref<HTMLInputElement | null>(null)
const previewEl = ref<HTMLDetailsElement | null>(null)
const copyState = ref<'idle' | 'ok' | 'failed'>('idle')
let copyTimer: number | undefined

/** 用户是否手改过文件名。没手改时，粘贴路径会自动带出文件名（三条路里的第 ③ 条要用它）。 */
const nameManual = ref(false)

/**
 * 拉 segmenter 清单。**失败必须安静**：这个面板的核心承诺是"服务端不可达也能生成脚本"，
 * 拿不到清单只会让脚本走兜底路径，不该在界面上报错。
 */
async function loadSegmenterDownloads() {
  try {
    const res = await fetch('/api/downloads/segmenter', { headers: { Accept: 'application/json' } })
    if (!res.ok) return
    const body = (await res.json()) as { platforms?: Partial<SegmenterDownload>[] }
    const origin = window.location.origin
    segmenterDownloads.value = (body.platforms ?? [])
      .filter((p) => typeof p.url === 'string' && p.url !== '')
      .map((p) => ({
        os: String(p.os ?? ''),
        arch: String(p.arch ?? ''),
        file: String(p.file ?? ''),
        // 服务端给的是 /downloads/xxx 这样的相对路径，烘焙进脚本前必须变成绝对地址。
        url: new URL(String(p.url), origin).toString(),
        sha256: String(p.sha256 ?? ''),
      }))
  } catch {
    // 服务端不可达 / 返回不是 JSON：保持空清单，脚本自动兜底。
  }
}

onMounted(loadSegmenterDownloads)

/** 当前平台在清单里的条目（只用于界面提示，脚本自己在运行时选）。 */
const currentEntry = computed(() => {
  const os = platform.value === 'windows' ? 'windows' : 'linux'
  return segmenterDownloads.value.find((d) => d.os === os) ?? null
})

function paramsFor(target: ScriptPlatform): ScriptParams {
  return {
    sourceName: sourceName.value.trim(),
    sourcePath: sourcePath.value.trim(),
    outputDir: outputDir.value.trim(),
    segmentSeconds: Number(segmentSeconds.value),
    packSize: Number(packSize.value),
    ffmpegUrl: ffmpegUrl.value.trim(),
    transcodeBitrate: transcodeBitrate.value.trim(),
    platform: target,
    segmenterDownloads: segmenterDownloads.value,
  }
}

const params = computed(() => paramsFor(platform.value))
/** 即时校验：空列表 = 可以生成。文案全部来自 validateScriptParams（中文）。 */
const errors = computed(() => validateScriptParams(params.value))
const ready = computed(() => errors.value.length === 0)
const scriptText = computed(() => buildSliceScriptSafe())
const lineCount = computed(() => scriptText.value.split('\n').length)
const defaultFfmpegUrl = computed(() => DEFAULT_FFMPEG_URLS[platform.value])

/**
 * 参数不合法（例如分片秒数是空的）时也照样生成一份给预览看 —— 生成器是纯函数，
 * 传 NaN 只会让脚本体里出现 NaN 字面量，不会抛错。真正导出前用 ready 挡住。
 */
function buildSliceScriptSafe(): string {
  try {
    return platform.value === 'windows' ? buildPowerShellScript(params.value) : buildBashScript(params.value)
  } catch (err) {
    return `# 脚本生成失败：${(err as Error).message}`
  }
}

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

/** 选本地文件：只能拿到 name / size，**拿不到完整路径**（浏览器安全限制）。 */
function onFileChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const picked = input.files?.[0] ?? null
  // 清空 input.value：同一个文件再选一次也要能触发 change。
  input.value = ''
  if (!picked) return
  sourceName.value = picked.name
  sourceSize.value = picked.size
  nameManual.value = false
  if (copyState.value !== 'idle') copyState.value = 'idle'
}

/** 粘贴完整路径时自动带出文件名（用户手改过文件名就不再覆盖）。 */
function onPathInput(event: Event) {
  const value = (event.target as HTMLInputElement).value
  sourcePath.value = value
  if (nameManual.value && sourceName.value.trim()) return
  const name = baseNameOf(value.trim())
  if (name) sourceName.value = name
}

function onNameInput(event: Event) {
  sourceName.value = (event.target as HTMLInputElement).value
  nameManual.value = true
}

/** 下载文件名里的 stem：去掉非法字符，避免在 Windows 上落盘失败。 */
function safeStem(): string {
  const raw = stemOf(sourceName.value.trim() || baseNameOf(sourcePath.value.trim()) || 'video')
  const cleaned = raw.replace(/[\\/:*?"<>|\s]+/g, '_').slice(0, 40)
  return cleaned || 'video'
}

function saveText(text: string, filename: string, withBom: boolean) {
  // withBom = 只在 Windows 版为 true：PowerShell 5.1 不带 BOM 时按 ANSI 读，中文全乱码。
  const blob = new Blob([withBom ? '\ufeff' + text : text], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.rel = 'noopener'
  document.body.appendChild(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 2000)
}

function downloadPs1() {
  if (!ready.value) return
  // CRLF：Windows 的记事本 / 旧版 PowerShell ISE 对纯 LF 的 .ps1 会显示成一行。
  const text = buildPowerShellScript(params.value).replace(/\r?\n/g, '\r\n')
  saveText(text, `room-slice-${safeStem()}.ps1`, true)
}

function downloadSh() {
  if (!ready.value) return
  saveText(buildBashScript(params.value), `room-slice-${safeStem()}.sh`, false)
}

function flashCopy(state: 'ok' | 'failed') {
  copyState.value = state
  if (copyTimer !== undefined) window.clearTimeout(copyTimer)
  copyTimer = window.setTimeout(() => (copyState.value = 'idle'), 2000)
}

async function copyScript() {
  if (!ready.value) return
  const done = await copyText(scriptText.value)
  flashCopy(done ? 'ok' : 'failed')
  // 复制失败（权限被拒/非安全上下文）时把预览展开，用户还能手动全选。
  if (!done && previewEl.value) previewEl.value.open = true
}

onBeforeUnmount(() => {
  if (copyTimer !== undefined) window.clearTimeout(copyTimer)
})
</script>

<template>
  <section class="slice-script" data-testid="slice-script-panel">
    <header>
      <h4>一键切片脚本（浏览器生成，本机跑 ffmpeg）</h4>
      <span class="badge" :class="{ ok: ready, danger: !ready }" data-testid="slice-script-status">
        {{ ready ? '参数就绪，可生成' : `还差 ${errors.length} 项` }}
      </span>
    </header>

    <p class="muted small">
      脚本由这个页面直接拼出来，你在自己机器上运行它。它会先从服务端
      （<code class="mono">GET /api/downloads/segmenter</code> 发布的位置）下载 <b>segmenter</b>
      可执行文件并<b>校验 sha256</b>（校验不过立刻退出，不会运行来路不明的程序），再调它切片；
      下载不到（离线 / <code class="mono">-NoExe</code>）就自动退回内置的 HLS-fMP4 路径。
      服务端连不上也照样能生成脚本。切完用上面的「选择分片目录」选中即可开播。
    </p>

    <div class="platforms" role="radiogroup" aria-label="脚本平台">
      <label class="radio">
        <input v-model="platform" type="radio" value="windows" />
        <span>Windows（.ps1）</span>
      </label>
      <label class="radio">
        <input v-model="platform" type="radio" value="unix" />
        <span>Linux / macOS（.sh）</span>
      </label>
    </div>

    <div class="fields">
      <div class="field">
        <span class="label-text">源视频</span>
        <div class="row">
          <input
            ref="fileInput"
            class="hidden-input"
            type="file"
            accept="video/*"
            @change="onFileChosen"
          />
          <button type="button" @click="chooseFile">选择本地文件</button>
          <span v-if="sourceName" class="muted small mono">
            {{ sourceName }}<template v-if="sourceSize > 0"> · {{ humanBytes(sourceSize) }}</template>
          </span>
          <span v-else class="muted small">选一个视频，脚本会按它的文件名去找路径</span>
        </div>
        <p class="muted tiny">
          浏览器的安全限制：选文件只能拿到<b>文件名</b>，拿不到完整路径。
          所以下面三条路任选一条都能跑通。
        </p>
      </div>

      <div class="field">
        <label class="label-text" for="slice-source-path">源文件完整路径（可选，粘贴）</label>
        <input
          id="slice-source-path"
          :value="sourcePath"
          type="text"
          spellcheck="false"
          placeholder="D:\video\movie.mp4 或 /home/me/movie.mp4"
          @input="onPathInput"
        />
        <p class="muted tiny">
          取路径的办法：在资源管理器里 <b>Shift + 右键</b> → 「复制文件地址」；macOS 用
          <b>Option + 右键</b> → 「拷贝 … 为路径名称」。
        </p>
      </div>

      <div class="field">
        <label class="label-text" for="slice-out-dir">输出目录（切片产物落这里）</label>
        <input
          id="slice-out-dir"
          v-model="outputDir"
          type="text"
          spellcheck="false"
          placeholder="room-media"
        />
        <p class="muted tiny">相对路径按脚本所在目录算；用绝对的也可以。</p>
      </div>

      <div class="grid-4">
        <label class="field">
          <span class="label-text">分片秒数</span>
          <input v-model.number="segmentSeconds" type="number" min="0.5" step="0.5" />
          <span class="muted tiny">默认 2 秒，与验证过的素材一致</span>
        </label>

        <label class="field">
          <span class="label-text">每包片数</span>
          <input v-model.number="packSize" type="number" min="1" step="1" />
          <span class="muted tiny">默认 100；1 = 每片一个文件</span>
        </label>

        <label class="field">
          <span class="label-text">转码码率（可空）</span>
          <input v-model="transcodeBitrate" type="text" spellcheck="false" placeholder="例如 1200k" />
          <span class="muted tiny">留空 = H.264/AAC 无损直切，其它编码自动转 1800k</span>
        </label>

        <label class="field">
          <span class="label-text">ffmpeg 下载源（可空）</span>
          <input
            v-model="ffmpegUrl"
            type="text"
            spellcheck="false"
            :placeholder="defaultFfmpegUrl"
          />
          <span class="muted tiny">留空就用内置默认（上面这个）</span>
        </label>
      </div>
    </div>

    <!-- 三条路：浏览器给不了完整路径，这三种做法是等价且都验证过的 -->
    <div class="paths">
      <p class="label-text">怎么让脚本找到视频（三条路任选）</p>
      <ol>
        <li>
          <b>把视频文件直接拖到生成的脚本上</b>（Windows 最省事）：拖进去的路径会自动成为脚本的
          <code class="mono">-Source</code> 参数。
          如果拖拽变成了"用编辑器打开"，就右键脚本 →「使用 PowerShell 运行」，再把路径粘进去；
          提示"脚本被禁用"时用
          <code class="mono">powershell -ExecutionPolicy Bypass -File .\room-slice-xxx.ps1</code>。
        </li>
        <li>
          <b>在表单里粘贴路径</b>（上面的「源文件完整路径」）：资源管理器
          <b>Shift + 右键</b> →「复制文件地址」，粘进去、生成脚本即可，脚本不会再问你。
        </li>
        <li>
          <b>什么都不填也行</b>：脚本会自己在<b>桌面 / 下载 / 视频 / 文档 / 当前目录</b>里按文件名找，
          找不到才会交互式问你要路径。
        </li>
      </ol>
    </div>

    <div class="todo-list">
      <p class="label-text">脚本会按顺序做的事</p>
      <ul>
        <li>找输入视频：上面的三条路依次尝试（命令行参数 → 拖拽 → 粘贴/按文件名搜 → 询问）。</li>
        <li>找 ffmpeg / ffprobe；没有就提示 5 秒后用 curl 自动下一份放到脚本旁边（Ctrl+C 可取消）。</li>
        <li>
          <b>从服务器下载 segmenter 可执行文件</b>到脚本旁边，并<b>校验 sha256</b>（校验不过就删掉文件、
          立刻退出，绝不运行来路不明的程序）；下载不到就改用内置的 HLS-fMP4 兜底路径。
          <template v-if="currentEntry">
            当前平台将下载 <code class="mono">{{ currentEntry.file }}</code>。
          </template>
          <template v-else>
            现在没拉到二进制清单（服务端未发布或不可达），脚本会走兜底路径。
          </template>
        </li>
        <li>ffprobe 探测时长与编码；不是 H.264/AAC/AV1/VP9（或你填了转码码率）就先转码。</li>
        <li>切片：每片约 {{ segmentSeconds || 2 }} 秒，产出 init.mp4 + 分片（由 segmenter 完成，不再是 DASH）。</li>
        <li>
          用 ffprobe <b>断言 init.mp4 的轨道数与 index.json 声明的编码一致</b>，不一致就报错退出
          —— 不会再静默产出"丢音轨"、浏览器无法播放的坏切片。
        </li>
        <li>
          产物由 segmenter 拼包 + 逐片 sha256 + 写 index.json（每 {{ packSize || 1 }} 片一个包）。
        </li>
      </ul>
    </div>

    <ul class="errors" v-if="!ready" data-testid="slice-script-errors">
      <li v-for="message in errors" :key="message">{{ message }}</li>
    </ul>

    <div class="actions">
      <button type="button" class="primary" :disabled="!ready" data-testid="download-ps1" @click="downloadPs1">
        下载 .ps1（Windows）
      </button>
      <button type="button" :disabled="!ready" data-testid="download-sh" @click="downloadSh">
        下载 .sh（Linux / macOS）
      </button>
      <button type="button" :disabled="!ready" data-testid="copy-script" @click="copyScript">
        复制到剪贴板
      </button>
      <span class="muted small" v-if="copyState === 'ok'">已复制。</span>
      <span class="muted small warn-text" v-else-if="copyState === 'failed'">
        剪贴板不可用：已展开下面的预览，请手动全选复制。
      </span>
      <span class="muted small" v-if="!ready">参数补齐后才能下载 / 复制。</span>
    </div>

    <details class="preview" ref="previewEl" data-testid="slice-script-preview">
      <summary>
        脚本预览（{{ platform === 'windows' ? '.ps1' : '.sh' }} · {{ lineCount }} 行 · 只读）
      </summary>
      <pre class="mono">{{ scriptText }}</pre>
    </details>
  </section>
</template>

<style scoped>
.slice-script {
  border-top: 1px dashed var(--border);
  margin-top: 10px;
  padding-top: 10px;
  min-width: 0;
}

header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
}

h4 {
  margin: 0;
  font-size: 12px;
  color: var(--text);
}

.platforms {
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
  margin: 8px 0;
}

label.radio {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  font-size: 12px;
  color: var(--text);
  cursor: pointer;
}

label.radio input {
  width: auto;
  margin: 0;
}

.fields {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
}

.label-text {
  font-size: 12px;
  color: var(--text-dim);
  margin: 0;
}

.row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.hidden-input {
  display: none;
}

.grid-4 {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 10px;
}

.small {
  font-size: 12px;
}

.tiny {
  font-size: 11px;
  line-height: 1.6;
  margin: 0;
}

.tiny b {
  color: var(--text);
}

p.muted.small {
  margin: 6px 0;
  line-height: 1.6;
}

.paths,
.todo-list {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 10px;
  margin: 10px 0;
  background: var(--panel);
}

.paths ol,
.todo-list ul {
  margin: 6px 0 0;
  padding-left: 20px;
  font-size: 12px;
  line-height: 1.75;
  color: var(--text-dim);
}

.paths b,
.todo-list b {
  color: var(--text);
}

code {
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0 4px;
  font-size: 11px;
}

.errors {
  margin: 8px 0;
  padding-left: 20px;
  border-left: 2px solid var(--danger);
  color: var(--danger);
  font-size: 12px;
  line-height: 1.7;
  list-style: none;
}

.errors li::before {
  content: '· ';
}

.actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin: 10px 0;
}

.warn-text {
  color: var(--accent-2);
}

.preview {
  margin-top: 6px;
}

.preview summary {
  cursor: pointer;
  font-size: 12px;
  color: var(--accent);
}

.preview pre {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 10px;
  margin: 6px 0 0;
  font-size: 11.5px;
  line-height: 1.55;
  max-height: 340px;
  overflow: auto;
  white-space: pre;
}
</style>
