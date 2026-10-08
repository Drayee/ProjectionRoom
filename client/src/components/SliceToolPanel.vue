<script setup lang="ts">
// 切片工具下载面板：把服务端发布的 segmenter 可执行文件列出来，用户挑对平台的下载，
// 再照下面的命令行用法在自己机器上切一次。
//
// 为什么是「下载工具 + 命令行」而不是「在浏览器里拼一份本机要跑的脚本」：
//   切片产物要的是「单条复用 fMP4 + 按 moof 切分」，cmd/segmenter 已经把这套逻辑做完了；
//   再让浏览器拼一份脚本，等于把同一件事维护两遍（sha256 校验、编码判定、兜底路径都会各自漂移）。
//   所以这里只做两件事：给对平台的二进制 + 给命令行用法，产物格式不可能漂移。
//
// 面板只读 GET /api/downloads/segmenter：拿不到（服务端没构建工具 / 服务不可达）时
// 显示中文提示并保持页面可用，绝不抛错、不白屏。

import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { copyText } from '../utils/clipboard'
import { blockedLinkHint, resolveSafeLink } from '../utils/safeLink'

/** 清单里的一条平台记录，字段与服务端 SegmenterDownload 的 JSON tag 一一对应。 */
interface ToolEntry {
  os: string
  arch: string
  file: string
  /**
   * 过完白名单的下载地址（同源 + http/https）。
   * `null` = 服务端给的地址没通过白名单（例如 `javascript:`）：界面必须渲染成纯文本 + 提示，
   * 不能出现可点击的链接（F-1）。
   */
  url: string | null
  /** 未通过白名单的原因（通过时为空串），用于给出可读提示。 */
  blockedReason: string
  bytes: number
  sha256: string
}

/** 清单加载状态：loading 拉取中 / ok 有数据 / empty 拿到空清单 / failed 请求失败。 */
type LoadState = 'loading' | 'ok' | 'empty' | 'failed'

/** 服务端发布的可执行文件清单。 */
const entries = ref<ToolEntry[]>([])
const state = ref<LoadState>('loading')
/** 推断出的当前平台键（形如 "windows/amd64"）；空串 = 推断不出，界面只列清单、不高亮。 */
const detectedKey = ref('')

/** 展开显示完整 sha256 的那一行（值是平台键）；空串 = 全部只显示前 16 位。 */
const expandedSha = ref('')
/** 复制反馈：键是平台键，值是 'ok' / 'failed'。 */
const copyState = ref<Record<string, string>>({})

const OS_LABELS: Record<string, string> = { windows: 'Windows', linux: 'Linux', darwin: 'macOS' }
const ARCH_LABELS: Record<string, string> = { amd64: 'x64', arm64: 'arm64' }

/** 平台键：清单里的唯一标识，也用在前端做推荐匹配。 */
function keyOf(entry: ToolEntry): string {
  return `${entry.os}/${entry.arch}`
}

/** 展示名，例如 Windows x64 / macOS arm64（Apple 芯片）。 */
function platformLabel(entry: ToolEntry): string {
  const os = OS_LABELS[entry.os] ?? entry.os
  const arch = ARCH_LABELS[entry.arch] ?? entry.arch
  if (entry.os === 'darwin') return `${os} ${arch}（${entry.arch === 'arm64' ? 'Apple 芯片' : 'Intel'}）`
  return `${os} ${arch}`
}

function humanBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '未知大小'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value.toFixed(unit === 0 || value >= 100 ? 0 : 1)} ${units[unit]}`
}

/** sha256 前 16 位（清单里没有摘要时返回空串，界面改成提示）。 */
function shortSha(entry: ToolEntry): string {
  return entry.sha256.slice(0, 16)
}

// ---------- 平台识别 ----------

/** userAgentData 在部分浏览器里不存在，且 TS 的 DOM 类型不一定带它，统一按可选结构读。 */
interface UaDataLike {
  platform?: string
  getHighEntropyValues?: (hints: string[]) => Promise<{ architecture?: string; bitness?: string }>
}

function uaData(): UaDataLike | undefined {
  return (navigator as unknown as { userAgentData?: UaDataLike }).userAgentData
}

/**
 * 从 navigator.platform / userAgent 推操作系统。
 *
 * 只看这两处：它们在任何浏览器里都有，且 OS 比架构好判得多
 *（macOS 的 UA 永远写着 "Intel Mac OS X"，架构在 UA 里是假的，所以架构单走 UA-CH）。
 */
function detectOs(): string {
  const platform = String(uaData()?.platform ?? navigator.platform ?? '')
  const ua = navigator.userAgent
  if (/win/i.test(platform) || /windows/i.test(ua)) return 'windows'
  if (/mac/i.test(platform) || /mac os x/i.test(ua)) return 'darwin'
  if (/linux|android|cros/i.test(platform) || /linux|android/i.test(ua)) return 'linux'
  return ''
}

/**
 * 从 UA-CH 高熵值取真实架构：navigator.platform 在 Apple 芯片上仍报 MacIntel，
 * 只有 getHighEntropyValues(['architecture']) 会返回真正的 arm / x86。
 * 拿不到（Firefox / Safari / 权限被拒）就返回空串，绝不猜。
 */
async function detectArchFromUaData(): Promise<string> {
  const get = uaData()?.getHighEntropyValues
  if (typeof get !== 'function') return ''
  try {
    const high = await get.call(uaData(), ['architecture', 'bitness'])
    const arch = String(high?.architecture ?? '')
    if (/arm/i.test(arch)) return 'arm64'
    if (/x86/i.test(arch)) return 'amd64'
  } catch {
    // 权限被拒或浏览器不支持：安静退回"不推荐"，不打扰用户。
  }
  return ''
}

/** Windows 的 x86 fallback：UA 里带 Win64; x64 时必然是 x64 进程。 */
function detectArchFromUa(): string {
  const ua = navigator.userAgent
  if (/arm64|aarch64/i.test(ua)) return 'arm64'
  if (/win64|x64|amd64|wow64/i.test(ua)) return 'amd64'
  return ''
}

/**
 * 推断当前平台键。返回空串 = 不推荐（清单照常展示）。
 * macOS 不用 UA 猜架构：那里的 UA 被冻结成 Intel，猜了反而推荐错。
 */
async function detectPlatformKey(): Promise<string> {
  const os = detectOs()
  if (!os) return ''
  let arch = await detectArchFromUaData()
  if (!arch && os !== 'darwin') arch = detectArchFromUa()
  if (arch) return `${os}/${arch}`
  // 架构推断不出，但该 OS 在清单里只有一个二进制时，它不可能选错 → 照样推荐。
  const sameOs = entries.value.filter((entry) => entry.os === os)
  return sameOs.length === 1 ? keyOf(sameOs[0]) : ''
}

// ---------- 清单加载 ----------

/**
 * 拉清单。失败或空清单都只改状态、不抛错：
 * 这个面板的降级承诺是"服务端没构建工具时给一句中文提示，页面照常可用"。
 */
async function loadManifest() {
  state.value = 'loading'
  try {
    const res = await fetch('/api/downloads/segmenter', { headers: { Accept: 'application/json' } })
    if (!res.ok) {
      state.value = 'failed'
      return
    }
    const body = (await res.json()) as { platforms?: Partial<ToolEntry>[] }
    const origin = window.location.origin
    entries.value = (body.platforms ?? [])
      .filter((item) => typeof item.url === 'string' && item.url !== '' && typeof item.file === 'string')
      .map((item) => {
        // 服务端给的是 /downloads/<file> 这样的相对地址，按页面来源补成绝对地址再放进 href。
        // **必须过白名单**（F-1）：`:href` 直接吃服务端 JSON 时，一个 `javascript:` 就是本页 RCE。
        const safe = resolveSafeLink(item.url, origin)
        return {
          os: String(item.os ?? ''),
          arch: String(item.arch ?? ''),
          file: String(item.file ?? ''),
          url: safe.url,
          blockedReason: safe.reason,
          bytes: Number(item.bytes ?? 0),
          sha256: String(item.sha256 ?? '').toLowerCase(),
        }
      })
    state.value = entries.value.length > 0 ? 'ok' : 'empty'
  } catch {
    state.value = 'failed'
  }
}

onMounted(async () => {
  await loadManifest()
  detectedKey.value = await detectPlatformKey()
})

// ---------- 界面派生数据 ----------

const unavailable = computed(() => state.value === 'empty' || state.value === 'failed')
const stateText = computed(() => {
  if (state.value === 'loading') return '正在读取下载清单…'
  if (state.value === 'ok') return `可用 ${entries.value.length} 个平台`
  return '清单不可用（请确认服务端已构建切片工具）'
})

/** 高亮推荐：只有推断出的平台键与清单里的条目完全一致才推荐。 */
function isRecommended(entry: ToolEntry): boolean {
  return detectedKey.value !== '' && keyOf(entry) === detectedKey.value
}

/** 推荐条目（用于示例命令里替换出真实的文件名）。 */
const recommendedEntry = computed(() => entries.value.find(isRecommended) ?? null)

/** 用法文本（等宽展示 + 一键复制，内容与文案严格一致）。 */
const usageLines = computed(() => [
  'segmenter -in <视频文件> -out <输出目录>',
  '# 普通 MP4/MKV 会自动判定直通或转码；也可加 -fragment 强制无损重新封装',
  '# 需要转码（低上行/不被浏览器支持的编码）：加 -transcode 1200k',
])
const usageText = computed(() => usageLines.value.join('\n'))

/** Windows 上刚下载的 exe 名字与 `segmenter` 不同，把真实文件名点在提示里。 */
const windowsFileName = computed(
  () => entries.value.find((entry) => entry.os === 'windows' && entry.arch === 'amd64')?.file ?? '',
)

/**
 * 校验命令：按清单里实际出现的操作系统各给一行，文件名用该平台的真实文件名
 *（推荐架构优先，没有就用该 OS 的第一项）。
 */
const verifyLines = computed(() => {
  const order = ['windows', 'linux', 'darwin']
  const lines: { label: string; command: string }[] = []
  for (const os of order) {
    const sameOs = entries.value.filter((entry) => entry.os === os)
    if (sameOs.length === 0) continue
    const picked = sameOs.find((entry) => isRecommended(entry)) ?? sameOs[0]
    if (os === 'windows') {
      lines.push({ label: 'Windows', command: `Get-FileHash .\\${picked.file} -Algorithm SHA256` })
    } else {
      lines.push({
        label: os === 'darwin' ? 'macOS' : 'Linux',
        command: `shasum -a 256 ${picked.file}`,
      })
    }
  }
  return lines
})

/** 校验命令的展示文本：一行一个平台（等宽 pre 里保留换行）。 */
const verifyText = computed(() => verifyLines.value.map((line) => `${line.label}：${line.command}`).join('\n'))

// ---------- 复制 ----------

async function copyInto(key: string, text: string) {
  const done = await copyText(text)
  copyState.value = { ...copyState.value, [key]: done ? 'ok' : 'failed' }
  window.setTimeout(() => {
    if (copyState.value[key]) copyState.value = { ...copyState.value, [key]: '' }
  }, 2000)
}

function toggleSha(entry: ToolEntry) {
  const key = keyOf(entry)
  expandedSha.value = expandedSha.value === key ? '' : key
}
</script>

<template>
  <section class="slice-tool" data-testid="slice-tool-panel" :data-detected="detectedKey">
    <header>
      <h4>
        <BrandIcon name="download" decorative :size="14" />
        切片工具下载（拖到 exe 上或命令行都行）
      </h4>
      <span
        class="badge"
        :class="{ ok: state === 'ok', danger: unavailable }"
        data-testid="slice-tool-state"
      >
        {{ stateText }}
      </span>
    </header>

    <p class="muted small">
      切片产物要的是「<b>单条复用 fMP4 + 按 moof 切分</b>」——<b>segmenter</b> 已经做好，
      不用自己拼 <code class="mono">index.json</code>：把视频文件<b>直接拖到下载好的 exe 上</b>、
      或<b>双击 exe</b> 后按提示输入路径，都等价于下面的命令行。
      <RouterLink class="more-link" :to="{ name: 'help', hash: '#source' }">
        <BrandIcon name="tips" decorative :size="12" />
        片源准备说明
      </RouterLink>
    </p>

    <p class="muted small warn-block" v-if="unavailable" data-testid="slice-tool-unavailable">
      清单不可用（请确认服务端已构建切片工具）：服务端跑过构建后重新编译、再刷新本页；
      在拿到清单之前没有可下载的二进制。
    </p>

    <ul class="entries" v-if="state === 'ok'" data-testid="slice-tool-list">
      <li
        v-for="entry in entries"
        :key="keyOf(entry)"
        class="entry"
        :class="{ recommended: isRecommended(entry) }"
        data-testid="slice-tool-row"
        :data-key="keyOf(entry)"
        :data-recommended="isRecommended(entry) ? 'true' : 'false'"
      >
        <div class="line">
          <span class="name">{{ platformLabel(entry) }}</span>
          <span class="tag" v-if="isRecommended(entry)">推荐（与你当前平台匹配）</span>
          <span class="muted small mono">{{ humanBytes(entry.bytes) }}</span>
          <!-- 下载入口只留图标：可访问名（含平台名）走 aria-label。 -->
          <a
            v-if="entry.url"
            class="download"
            :href="entry.url"
            download
            data-testid="slice-tool-download"
            :data-key="keyOf(entry)"
            :aria-label="`下载 ${platformLabel(entry)} 的切片器`"
            :title="`下载 ${platformLabel(entry)} 的切片器`"
          >
            <BrandIcon name="download" decorative :size="15" />
          </a>
          <template v-else>
            <!-- 服务端给的地址没过白名单：渲染成纯文本 + 可读提示，绝不生成可点击链接。 -->
            <span class="muted small warn-text" data-testid="slice-tool-download-blocked" :data-key="keyOf(entry)">
              不可下载（{{ blockedLinkHint(entry.blockedReason) }}）
            </span>
          </template>
        </div>
        <div class="line sha-line">
          <span class="muted tiny">sha256</span>
          <code class="mono sha">{{ shortSha(entry) }}…</code>
          <button type="button" class="tiny-btn" data-testid="slice-tool-sha-toggle" @click="toggleSha(entry)">
            {{ expandedSha === keyOf(entry) ? '收起' : '显示完整' }}
          </button>
          <button
            type="button"
            class="tiny-btn icon-only"
            data-testid="slice-tool-sha-copy"
            aria-label="复制完整 sha256"
            title="复制完整 sha256"
            @click="copyInto('sha:' + keyOf(entry), entry.sha256)"
          >
            <BrandIcon name="copy" decorative :size="13" />
          </button>
          <span class="muted tiny mono" v-if="copyState['sha:' + keyOf(entry)] === 'ok'">已复制</span>
          <span class="muted tiny" v-else-if="copyState['sha:' + keyOf(entry)] === 'failed'">剪贴板不可用</span>
          <code class="mono sha full" v-if="expandedSha === keyOf(entry)" data-testid="slice-tool-sha-full">
            {{ entry.sha256 || '（服务端未给出摘要）' }}
          </code>
        </div>
      </li>
    </ul>

    <div class="block" v-if="state === 'ok'">
      <p class="label-text">校验下载的文件（对照上面的 sha256）</p>
      <pre class="mono" data-testid="slice-tool-verify">{{ verifyText }}</pre>
    </div>

    <div class="block">
      <p class="label-text">命令行用法</p>
      <pre class="mono" data-testid="slice-tool-usage">{{ usageText }}</pre>
      <div class="line">
        <button
          type="button"
          class="icon-only"
          data-testid="slice-tool-copy-usage"
          aria-label="复制命令行用法"
          title="复制命令行用法"
          @click="copyInto('usage', usageText)"
        >
          <BrandIcon :name="copyState.usage === 'failed' ? 'alert-triangle' : 'copy'" decorative :size="14" />
        </button>
        <span class="muted tiny" v-if="copyState.usage === 'ok'">已复制</span>
        <span class="muted tiny" v-else-if="copyState.usage === 'failed'">剪贴板不可用，请手动选中复制</span>
      </div>
      <ul class="notes">
        <li data-testid="slice-tool-usage-ways">
          <b>不想敲命令也行</b>：把视频文件<b>直接拖到下载好的 exe 上</b>（最省事），或者
          <b>双击 exe</b> 后按提示输入文件路径 —— 两者与上面的命令行完全等价。
        </li>
        <li data-testid="slice-tool-no-ffmpeg">
          <b>本机没有 ffmpeg 也没关系</b>：exe 会提示 5 秒后<b>自动下载</b>一份 ffmpeg 放到它旁边
          （这 5 秒里按 <code class="mono">Ctrl+C</code> 可取消）；已经有 ffmpeg 时用
          <code class="mono">-ffmpeg-dir &lt;目录或可执行文件&gt;</code> 指定即可，不会重复下载。
        </li>
        <li>
          <code class="mono">-fragment</code> = 无损重新封装后按 moof 切分（H.264/AAC 等浏览器能解的编码走这条）；
          编码不被浏览器支持、或上行偏低时，把 <code class="mono">-fragment</code> 换成
          <code class="mono">-transcode 1200k</code>。
        </li>
        <li>
          <code class="mono">-pack N</code> 控制每个 <code class="mono">.bin</code> 装几片（默认 100）；
          <code class="mono">-frag-sec</code> 控制每片秒数（默认 2）。
        </li>
        <li v-if="windowsFileName">
          Windows 上把命令里的 <code class="mono">segmenter</code> 换成刚下载的
          <code class="mono">.\{{ windowsFileName }}</code>，其余参数不变。
        </li>
        <li>切完回主播页点「选择分片目录」，选中那个输出目录即可开播。</li>
      </ul>
    </div>
  </section>
</template>

<style scoped>
.slice-tool {
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
  display: flex;
  align-items: center;
  gap: 6px;
}

.more-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
}

.small {
  font-size: 12px;
}

.tiny {
  font-size: 11px;
  line-height: 1.6;
  margin: 0;
}

p.muted.small {
  margin: 6px 0;
  line-height: 1.6;
}

.warn-block {
  border-left: 2px solid var(--accent-2);
  padding-left: 8px;
  color: var(--accent-2);
}

.entries {
  list-style: none;
  margin: 8px 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.entry {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 6px 8px;
  background: var(--panel);
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.entry.recommended {
  border-color: var(--accent);
  background: var(--panel-2);
}

.line {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.name {
  font-size: 12px;
  color: var(--text);
  font-weight: 600;
}

.tag {
  font-size: 11px;
  color: var(--accent);
  border: 1px solid var(--accent);
  border-radius: 4px;
  padding: 0 4px;
}

.download {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  color: var(--accent);
  text-decoration: none;
  border: 1px solid var(--accent);
  border-radius: 4px;
  padding: 5px 9px;
  line-height: 0;
}

.download:hover {
  background: var(--panel-2);
}

/* 纯图标按钮：只由图标承担点击区，可访问名走 aria-label / title。 */
button.icon-only,
.tiny-btn.icon-only {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 4px 7px;
  line-height: 0;
}

.sha-line {
  gap: 8px;
}

.sha {
  word-break: break-all;
}

.sha.full {
  flex-basis: 100%;
}

.tiny-btn {
  font-size: 11px;
  padding: 1px 8px;
}

.block {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 10px;
  margin: 10px 0;
  background: var(--panel);
}

.label-text {
  font-size: 12px;
  color: var(--text-dim);
  margin: 0 0 4px;
}

pre {
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 10px;
  margin: 6px 0;
  font-size: 11.5px;
  line-height: 1.55;
  overflow: auto;
  white-space: pre;
}

.notes {
  margin: 6px 0 0;
  padding-left: 20px;
  font-size: 12px;
  line-height: 1.75;
  color: var(--text-dim);
}

.notes b {
  color: var(--text);
}

code {
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0 4px;
  font-size: 11px;
}

a {
  color: var(--accent);
}
</style>
