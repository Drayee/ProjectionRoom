// 一键切片脚本生成器（纯函数，无副作用）。
//
// 为什么由浏览器生成而不是发一份固定脚本：
//   - 分片秒数、每包片数、输出目录、源文件名都要跟着当前这一场走；
//   - 用户机器上 ffmpeg 的有无、路径、下载源都得能在界面上改（默认值来自服务端配置）。
//
// 主路线：脚本先**下载服务端发布的 segmenter 可执行文件**（用 sha256 校验）再调它切片。
//   为什么不再自己拼 ffmpeg 的 DASH：DASH 会把视频/音频拆成两条 AdaptationSet，
//   而分片名模板里没有 $RepresentationID$，于是两条流写同一批文件名互相覆盖 ——
//   产物每个分片只剩一条 traf、init.mp4 只有视频轨，index.json 却声明了双编码，
//   浏览器 append 时报"分片可能不是合法的 fMP4 片段"，文件体积也只有源文件的一小部分。
//   segmenter（cmd/segmenter）走的是"ffmpeg 重新封装为 fragmented MP4 → 按 moof 边界切分"，
//   单条 muxed 流，天然没有这个问题；它还与服务端切片共用 internal/service/segment，
//   产物格式不可能漂移。
//
// 兜底（-NoExe / 服务端没发布 / 下载失败）：退回内置的 HLS-fMP4 路径。
//   HLS 复用器只输出一条 muxed 流（每个分片的 moof 里 video/audio 两条 traf 都在），
//   因此也不会丢音轨。两条路径都带防呆断言：产物轨道数与声明编码不一致就**立刻失败**，
//   绝不静默产出坏切片。
//
// 生成的脚本只依赖 ffmpeg/ffprobe；没有的话按用户的明确要求：
// 先给 5 秒警告，然后用 curl 自动下载一份到脚本旁边再用。
//
// 浏览器**拿不到**用户所选文件的完整本地路径（安全限制，File 对象只有 name），
// 所以路径按优先级取：命令行参数 / 把文件拖到脚本上 / 界面里粘贴的路径 /
// 在常见目录（桌面、下载、视频、文档、当前目录）按文件名找 / 交互式询问。

export type ScriptPlatform = 'windows' | 'unix'

/**
 * 服务端发布的一个平台的 segmenter 可执行文件（GET /api/downloads/segmenter 的一项）。
 * 生成脚本时整份清单会被烘焙进脚本，运行时按当前机器挑一项。
 */
export interface SegmenterDownload {
  /** 目标操作系统：windows / linux / darwin。 */
  os: string
  /** 目标架构：amd64 / arm64。 */
  arch: string
  /** 文件名，例如 segmenter-windows-amd64.exe。 */
  file: string
  /** 可直接下载的**绝对**地址（由调用方用站点来源拼好）。 */
  url: string
  /** 小写十六进制 sha256；空串表示服务端没给，脚本会拒绝下载。 */
  sha256: string
}

export interface ScriptParams {
  /** 所选文件名（浏览器能拿到的只有这个）。 */
  sourceName: string
  /** 用户粘贴的完整本地路径，可空。 */
  sourcePath: string
  /** 输出目录（切片产物落这里）。 */
  outputDir: string
  /** 分片目标秒数。 */
  segmentSeconds: number
  /** 每包片数（1 = 不打包）。 */
  packSize: number
  /** ffmpeg 下载地址覆盖，可空 → 用脚本内置默认。 */
  ffmpegUrl: string
  /** 非空则强制转码到该码率（例如 "1200k"）。 */
  transcodeBitrate: string
  platform: ScriptPlatform
  /**
   * 服务端发布的 segmenter 清单（来自 GET /api/downloads/segmenter）。
   * 省略或为空 = 服务端没发布：脚本会直接走内置 HLS-fMP4 兜底路径。
   */
  segmenterDownloads?: SegmenterDownload[]
}

export const DEFAULT_FFMPEG_URLS: Record<ScriptPlatform, string> = {
  windows: 'https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip',
  unix: 'https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz',
}

/** 参数校验：返回错误列表（空表示可以生成）。 */
export function validateScriptParams(p: ScriptParams): string[] {
  const errors: string[] = []
  if (!p.sourceName.trim() && !p.sourcePath.trim()) {
    errors.push('请先选择视频文件，或粘贴它的完整路径')
  }
  if (!p.outputDir.trim()) {
    errors.push('输出目录不能为空')
  }
  if (!(p.segmentSeconds > 0)) {
    errors.push('分片秒数必须大于 0')
  }
  if (!(p.packSize >= 1)) {
    errors.push('每包片数必须 ≥ 1（1 = 不打包）')
  }
  if (p.transcodeBitrate && !/^\d+[kKmM]?$/.test(p.transcodeBitrate.trim())) {
    errors.push('转码码率格式应为 1200k / 3M 这样')
  }
  // 单引号会打断 PowerShell 字面量、双引号会打断 shell 变量，统一拒绝更安全。
  for (const [label, value] of [
    ['文件名', p.sourceName],
    ['源路径', p.sourcePath],
    ['输出目录', p.outputDir],
  ] as const) {
    if (/["'`$]/.test(value)) {
      errors.push(`${label}里不能包含引号或 $ 符号（脚本会当成变量）`)
    }
  }
  return errors
}

/** 从路径里取出文件名（两种分隔符都认）。 */
export function baseNameOf(path: string): string {
  const parts = path.split(/[\\/]/)
  return parts[parts.length - 1] ?? ''
}

/** 去掉扩展名。 */
export function stemOf(name: string): string {
  const dot = name.lastIndexOf('.')
  return dot > 0 ? name.slice(0, dot) : name
}

// ---------- 生成期的小工具 ----------

/** 只用清单里带 url 与平台键的条目；其余视为坏数据丢掉。 */
function usableDownloads(list: SegmenterDownload[] | undefined): SegmenterDownload[] {
  return (list ?? []).filter((d) => d && d.os && d.arch && d.url)
}

/**
 * 把值塞进单引号字面量前先消毒。
 * 单引号会打断 PowerShell/bash 的字面量（校验函数也拦了用户输入，这里是最后一道保险）。
 */
function quoteSafe(value: string): string {
  return String(value).replace(/['`$\r\n]/g, '')
}

function lowerHex(value: string): string {
  return quoteSafe(value).trim().toLowerCase()
}

/** Windows 版把 windows/amd64 这一项烘焙成参数默认值（Windows 上没有别的常见架构）。 */
function pickWindows(downloads: SegmenterDownload[]): SegmenterDownload | undefined {
  return downloads.find((d) => d.os === 'windows' && d.arch === 'amd64') ?? downloads.find((d) => d.os === 'windows')
}

/**
 * 生成 PowerShell 版一键脚本（Windows 首选）。
 *
 * 步骤：找输入 → 找 ffmpeg（没有就 5 秒后 curl 下载）→ ffprobe 探测 →
 * 下载 segmenter 并校验 sha256 → 调它切片（-NoExe/失败则退回内置 HLS-fMP4）→
 * 用 ffprobe 断言产物轨道数与 index.json 声明一致 → 打印容量提示。
 */
export function buildPowerShellScript(p: ScriptParams): string {
  const out = p.outputDir.replace(/\\+$/, '')
  const ffmpegUrl = p.ffmpegUrl.trim() || DEFAULT_FFMPEG_URLS.windows
  const forced = p.transcodeBitrate.trim()
  const downloads = usableDownloads(p.segmenterDownloads)
  const winEntry = pickWindows(downloads)

  // 烘焙进脚本的清单：键是 "<os>/<arch>"，运行时按当前机器挑一项。
  // 单引号字面量，避免 PowerShell 把 $ 当变量插值（URL 里不会有，但保持防御）。
  const tableLines = downloads.map(
    (d) =>
      `  '${quoteSafe(d.os)}/${quoteSafe(d.arch)}' = @{ Url = '${quoteSafe(d.url)}'; Sha256 = '${lowerHex(d.sha256)}' }`,
  )
  const tableBody = tableLines.length > 0 ? `@{\n${tableLines.join('\n')}\n}` : '@{}'

  return `
# ============================================================================
#  ProjectionRoom 一键切片脚本（由浏览器生成）
#  把视频切成放映室可用的分片目录：init.mp4 + pack-*.bin + index.json
#
#  用法（三种任选）：
#    1) 双击/右键"使用 PowerShell 运行"
#    2) 把视频文件直接拖到这个脚本上（最省事，路径自动带进来）
#    3) 命令行： .\\<脚本名>.ps1 -Source "D:\\video\\movie.mp4"
#
#  ★ 这个脚本会做的事（请先看一眼）：
#    1) 找输入视频；找 ffmpeg/ffprobe，没有就提示 5 秒后用 curl 自动下载一份；
#    2) **从服务器下载 segmenter 可执行文件到脚本旁边（只下载，不执行别的），
#       并校验它的 sha256**；校验不过会删掉文件并立刻退出；
#    3) 调用 segmenter 切片（H.264/AAC/AV1/VP9 + AAC/Opus 走无损直切，其它编码自动转码）；
#    4) 用 ffprobe 断言产物轨道数与 index.json 的声明一致，不一致就报错退出
#       —— 绝不静默产出"丢音轨"的坏切片。
#    它仍然需要 ffmpeg/ffprobe（segmenter 用它们做重新封装/转码）。
#
#  不想下载可执行文件（离线、或你只想用内置实现）：加 -NoExe，
#  脚本会退回内置的 HLS-fMP4 切片路径（同样带防呆断言）。
# ============================================================================
[CmdletBinding()]
param(
  # 注意：不能叫 $Input —— 那是 PowerShell 的自动变量（管道输入枚举器），
  # 同名的参数会被静默忽略，表现为"默认路径永远不生效、每次都来问一遍"。
  [Alias('Input')]
  [string]$Source = "${p.sourcePath.trim()}",
  [string]$Out = "${out}",
  [int]$SegmentSeconds = ${p.segmentSeconds},
  [int]$PackSize = ${p.packSize},
  [string]$TranscodeBitrate = "${forced}",
  # 显式要求转码（默认：浏览器能解的编码族直接 remux，省掉整片转码）
  [switch]$Transcode,
  [string]$FfmpegDir = "",
  [string]$FfmpegUrl = "${ffmpegUrl}",
  # 覆盖下面烘焙的 segmenter 下载地址 / sha256；两个要一起给（一般不用改）
  [string]$SegmenterUrl = "${quoteSafe(winEntry?.url ?? '')}",
  [string]$SegmenterSha256 = "${lowerHex(winEntry?.sha256 ?? '')}",
  # 跳过 segmenter 下载，直接用内置 HLS-fMP4 兜底路径
  [switch]$NoExe,
  [switch]$KeepFragments
)

$ErrorActionPreference = 'Stop'
$WantName = "${p.sourceName.trim()}"
if ([string]::IsNullOrWhiteSpace($WantName)) { $WantName = Split-Path -Leaf $Out }

function Say($msg)  { Write-Host $msg }
function Warn($msg) { Write-Host $msg -ForegroundColor Yellow }
function Die($msg)  { Write-Host $msg -ForegroundColor Red; exit 1 }
function Human([long]$bytes) {
  if ($bytes -ge 1GB) { return ('{0:N2} GiB' -f ($bytes / 1GB)) }
  if ($bytes -ge 1MB) { return ('{0:N1} MiB' -f ($bytes / 1MB)) }
  return ('{0:N0} KiB' -f ($bytes / 1KB))
}

# segmenter 二进制清单：由浏览器在生成时从 GET /api/downloads/segmenter 取回并烘焙进来。
# 键是 "<os>/<arch>"。运行时会按当前机器挑一项；表为空说明服务端没有发布，
# 脚本会直接走内置的 HLS-fMP4 兜底路径（不是错误，只是实现不同）。
$SegmenterTable = ${tableBody}

# ---------- 1. 找输入文件 ----------
if ([string]::IsNullOrWhiteSpace($Source) -or -not (Test-Path -LiteralPath $Source)) {
  $candidates = @()
  if ($WantName) {
    $dirs = @("$env:USERPROFILE\\Desktop", "$env:USERPROFILE\\Downloads", "$env:USERPROFILE\\Videos",
              "$env:USERPROFILE\\Documents", (Get-Location).Path, $PSScriptRoot)
    foreach ($d in $dirs) {
      if ($d -and (Test-Path -LiteralPath $d)) {
        $candidates += (Get-ChildItem -LiteralPath $d -Filter $WantName -File -ErrorAction SilentlyContinue)
      }
    }
  }
  if ($candidates.Count -gt 0) {
    $Source = $candidates[0].FullName
    Say "在常见目录里找到了： $Source"
  } else {
    Warn "没找到视频文件。把文件拖到这个脚本上，或粘贴完整路径（在资源管理器里 Shift+右键 → 复制文件地址）。"
    if ($WantName) { Warn "要找的文件名是： $WantName" }
    $Source = Read-Host "视频文件完整路径"
  }
}
if (-not (Test-Path -LiteralPath $Source)) { Die "找不到输入文件： $Source" }
$Source = (Resolve-Path -LiteralPath $Source).Path
$srcSize = (Get-Item -LiteralPath $Source).Length
Say "输入: $Source  ($(Human $srcSize))"

# ---------- 2. 找 ffmpeg；没有就提示 5 秒后用 curl 下载 ----------
function Find-Tool([string]$name, [string]$dir) {
  if ($dir) {
    $c = Join-Path $dir "$name.exe"
    if (Test-Path -LiteralPath $c) { return $c }
  }
  $cmd = Get-Command "$name.exe" -ErrorAction SilentlyContinue
  if (-not $cmd) { $cmd = Get-Command $name -ErrorAction SilentlyContinue }
  if ($cmd) { return $cmd.Source }
  foreach ($sub in @('ffmpeg\\bin', 'ffmpeg', 'bin')) {
    $c = Join-Path $PSScriptRoot "$sub\\$name.exe"
    if (Test-Path -LiteralPath $c) { return $c }
  }
  return $null
}

$ffmpeg = Find-Tool 'ffmpeg' $FfmpegDir
$ffprobe = Find-Tool 'ffprobe' $FfmpegDir
if (-not $ffmpeg -or -not $ffprobe) {
  Warn "没有找到 ffmpeg/ffprobe。"
  Warn "5 秒后将用 curl 自动下载一份到脚本旁边（约 100 MB）： $FfmpegUrl"
  Warn "不想下载就按 Ctrl+C 取消，然后自己装好 ffmpeg 再加进 PATH。"
  for ($i = 5; $i -ge 1; $i--) { Write-Host "  $i ..."; Start-Sleep -Seconds 1 }

  $pkgDir = Join-Path $PSScriptRoot '_ffmpeg'
  New-Item -ItemType Directory -Force -Path $pkgDir | Out-Null
  $zip = Join-Path $pkgDir 'ffmpeg.zip'
  try {
    curl.exe -L --fail --output $zip $FfmpegUrl
    if (-not (Test-Path -LiteralPath $zip)) { throw 'curl 没有产出文件' }
    Expand-Archive -LiteralPath $zip -DestinationPath $pkgDir -Force
    $exe = Get-ChildItem -LiteralPath $pkgDir -Recurse -Filter 'ffmpeg.exe' | Select-Object -First 1
    if (-not $exe) { throw '压缩包里没有 ffmpeg.exe' }
    $FfmpegDir = $exe.DirectoryName
    $ffmpeg = Find-Tool 'ffmpeg' $FfmpegDir
    $ffprobe = Find-Tool 'ffprobe' $FfmpegDir
    Say "已下载并解压到: $FfmpegDir"
  } catch {
    Die "自动下载失败：$($_.Exception.Message)\`n请手动安装 ffmpeg（https://www.gyan.dev/ffmpeg/builds/），或改用服务器切片。"
  }
}
Say "ffmpeg: $ffmpeg"

# ---------- 3. 探测源文件 ----------
$probe = & $ffprobe -v error -show_entries format=duration,bit_rate -show_entries stream=codec_name,codec_type,pix_fmt,profile,level -of json -- "$Source" | ConvertFrom-Json
$duration = [double]$probe.format.duration
$vStream = $probe.streams | Where-Object { $_.codec_type -eq 'video' } | Select-Object -First 1
$aStream = $probe.streams | Where-Object { $_.codec_type -eq 'audio' } | Select-Object -First 1
$vCodec = $vStream.codec_name
$aCodec = $aStream.codec_name
$vPixFmt = $vStream.pix_fmt
$audio = if ([string]::IsNullOrEmpty($aCodec)) { '无' } else { $aCodec }
$aLabel = if ([string]::IsNullOrEmpty($aCodec)) { '无音轨' } else { $aCodec }
Say ("源: {0:N1}s ({1:N1} 分钟)  视频={2} 音频={3}" -f $duration, ($duration / 60), $vCodec, $audio)
if ($duration -gt 3600) { Warn "超过 60 分钟：本地切片没问题，但服务端切片会拒绝这么长的视频。" }

# ---------- 4. 编码族判定：能解的直通（-c copy），解不了的转码 ----------
# 播放器要的是浏览器能解的**编码族**：能解就只 remux，不要整片转码。
# 142 分钟的 AV1 转码要数小时；直通只受磁盘带宽限制，通常几分钟。
$vFamily = switch ($vCodec) { 'h264' { 'avc1' } 'av1' { 'av01' } 'vp9' { 'vp09' } default { '' } }
$aFamily = switch ($aCodec) { 'aac' { 'mp4a.40.2' } 'opus' { 'opus' } default { '' } }
$audioOk = [string]::IsNullOrEmpty($aCodec) -or ($aFamily -ne '')
$passthrough = ($vFamily -ne '') -and $audioOk -and (-not $Transcode)
$force = -not [string]::IsNullOrWhiteSpace($TranscodeBitrate)
$rate = if ($force) { $TranscodeBitrate } else { '1800k' }
$is10bit = ($vPixFmt -like '*10*')
# AV1 的编码串按 ffprobe 报出的真实 profile/level 拼（level 就是 av1C 里的 seq_level_idx_0），
# 不要写死：写死一个对不上的 level，严格校验的浏览器会直接判 isTypeSupported 为假 → 黑屏。
# profile: Main=0 / High=1 / Professional=2；tier 只有 High 才写 H，ffprobe 不报 tier 时按 Main 处理。
$av1Profile = switch ([string]$vStream.profile) { 'High' { 1 } 'Professional' { 2 } default { 0 } }
$av1Level = '05'
if ("$($vStream.level)" -match '^\\d+$') { $av1Level = '{0:d2}' -f [int]$vStream.level }
$av1Depth = if ($is10bit) { '10' } else { '08' }
$vCodecString = switch ($vFamily) {
  'avc1' { 'avc1.64001f' }
  'av01' { 'av01.' + $av1Profile + '.' + $av1Level + 'M.' + $av1Depth }
  'vp09' { 'vp09.00.10.08' }
  default { '' }
}
# mimeType 必须按**真实编码**写：写错了 isTypeSupported 照样通过，但 append 的是别的编码 → 黑屏。
# segmenter 路径的 mimeType 由它自己写进 index.json；这里算出来给兜底的 HLS-fMP4 路径用。
$q = [char]34
$mime = if ([string]::IsNullOrEmpty($aCodec)) {
  'video/mp4; codecs=' + $q + $vCodecString + $q
} else {
  'video/mp4; codecs=' + $q + $vCodecString + ',' + $aFamily + $q
}
if (-not $passthrough) {
  if ($Transcode) { Say '按 -Transcode 显式要求转码为 H.264/AAC…' }
  else { Say "源编码（$vCodec/$aLabel）浏览器可能解不了，转码为 H.264/AAC…" }
} else {
  Say "直通（不转码）：$vCodec / $aLabel"
  if ($vFamily -eq 'av01' -or $vFamily -eq 'vp09') {
    Warn '注意：AV1/VP9 只在支持它的浏览器能播（Chrome 基本都行；Safari 与部分 Firefox 不行）。'
    Warn '      要最大兼容性就加 -Transcode（代价是整片转码，142 分钟要数小时）。'
  }
}

# ---------- 5. 取 segmenter（下载 + sha256 校验），拿不到就用内置 HLS-fMP4 兜底 ----------
$segExe = ''
if ($NoExe) {
  Warn '按 -NoExe：跳过 segmenter 下载，改用内置的 HLS-fMP4 兜底切片。'
} else {
  # Windows 上用 PROCESSOR_ARCHITECTURE 判架构；ARM64 上也能跑 x64（系统自带仿真），
  # 所以清单里没有 windows/arm64 时退回 amd64 那一项即可。
  $segArch = switch -Wildcard ([string]$env:PROCESSOR_ARCHITECTURE) { 'ARM64' { 'arm64' } default { 'amd64' } }
  $segKey = "windows/$segArch"
  $segUrl = ''
  $segSha = ''
  # 显式传了 -SegmenterUrl / -SegmenterSha256 就以它为准（$PSBoundParameters 能区分
  # "用户传了" 与 "用了 param 默认值"，否则 ARM64 会被 amd64 的默认值顶掉）。
  $explicit = $PSBoundParameters.ContainsKey('SegmenterUrl') -or $PSBoundParameters.ContainsKey('SegmenterSha256')
  if ($explicit) {
    $segUrl = [string]$SegmenterUrl
    $segSha = ([string]$SegmenterSha256).Trim().ToLower()
  } elseif ($SegmenterTable.ContainsKey($segKey)) {
    $entry = $SegmenterTable[$segKey]
    $segUrl = [string]$entry.Url
    $segSha = ([string]$entry.Sha256).Trim().ToLower()
  }

  if (-not $segUrl) {
    Warn "服务端没有发布 $segKey 的 segmenter 二进制（清单为空或缺这一项）。"
    Warn '改用内置的 HLS-fMP4 兜底切片路径（同样是单条 muxed 流，不会丢音轨）。'
  } elseif (-not $segSha) {
    Die "这个下载地址没有 sha256，无法校验二进制；拒绝运行来路不明的可执行文件。请用 -SegmenterSha256 <十六进制> 显式给出，或加 -NoExe 走内置 HLS-fMP4 兜底路径。"
  } else {
    $segExe = Join-Path $PSScriptRoot 'segmenter.exe'
    $tmpExe = "$segExe.download"
    Say "下载 segmenter： $segUrl"
    Say "  → $segExe"
    if (Test-Path -LiteralPath $tmpExe) { Remove-Item -LiteralPath $tmpExe -Force -ErrorAction SilentlyContinue }
    & curl.exe -L --fail --silent --show-error --output "$tmpExe" "$segUrl"
    $curlCode = $LASTEXITCODE
    if ($curlCode -ne 0 -or -not (Test-Path -LiteralPath $tmpExe)) {
      if (Test-Path -LiteralPath $tmpExe) { Remove-Item -LiteralPath $tmpExe -Force -ErrorAction SilentlyContinue }
      Warn "下载 segmenter 失败（curl 退出码 $curlCode）： $segUrl"
      Warn '改用内置的 HLS-fMP4 兜底切片路径。'
      $segExe = ''
    } else {
      $actual = (Get-FileHash -LiteralPath $tmpExe -Algorithm SHA256).Hash.ToLower()
      if ($actual -ne $segSha) {
        Remove-Item -LiteralPath $tmpExe -Force -ErrorAction SilentlyContinue
        Die "下载的二进制校验失败，可能被篡改或中转损坏（期望 sha256 $segSha，实际 $actual）。已删除下载文件，未执行它。请核对 -SegmenterUrl/-SegmenterSha256，或加 -NoExe 走内置 HLS-fMP4 兜底路径。"
      }
      Move-Item -LiteralPath $tmpExe -Destination $segExe -Force
      Say "  sha256 校验通过： $actual"
    }
  }
}

# ---------- 6. 产物自检（两条路径共用）----------
# 把"丢音轨"这类静默失败变成明确失败：index.json 声明了几条编码，init.mp4 就必须有几条轨。
# 覆盖过的真实故障：DASH 双流互相覆盖 → init.mp4 只有 trackID=1，浏览器 append 才报错，
# 用户拿着一个"看起来切完了"的目录查半天。
function Assert-Index([string]$dir) {
  $init = Join-Path $dir 'init.mp4'
  $idxPath = Join-Path $dir 'index.json'
  if (-not (Test-Path -LiteralPath $init)) { Die "产物缺少 init.mp4： $init" }
  if (-not (Test-Path -LiteralPath $idxPath)) { Die "产物缺少 index.json： $idxPath" }

  $mime = ''
  try { $mime = [string](([System.IO.File]::ReadAllText($idxPath) | ConvertFrom-Json).mimeType) } catch { }
  $declared = 1
  if ($mime -match 'codecs="([^"]*)"') { $declared = @($Matches[1] -split ',').Count }

  $tracks = @(& $ffprobe -v error -show_entries stream=codec_type -of csv=p=0 -- "$init" |
    Where-Object { $_ -and $_.Trim() -ne '' })
  if ($tracks.Count -ne $declared) {
    Die ("切片产出与声明编码不一致（疑似多流互相覆盖）：index.json 声明了 {0} 条编码（{1}），init.mp4 里只有 {2} 条轨（{3}）。请改用 cmd/segmenter 或报告此问题。" -f \`
      $declared, $mime, $tracks.Count, ($tracks -join '/'))
  }
  Say ("轨道自检通过：init.mp4 有 {0} 条轨（{1}），与 index.json 的声明一致" -f $tracks.Count, ($tracks -join '/'))
}

# ---------- 7. 切片 ----------
if ($segExe) {
  Say "切片中（segmenter：每片约 ${p.segmentSeconds}s，每 ${p.packSize} 片一个包）..."
  $segArgs = @('-in', "$Source", '-out', "$Out", '-frag-sec', "$SegmentSeconds", '-pack', "$PackSize")
  if ($passthrough) { $segArgs += '-fragment' } else { $segArgs += @('-transcode', "$rate") }
  & $segExe @segArgs
  if ($LASTEXITCODE -ne 0) { Die 'segmenter 切片失败（看上面它自己的输出）' }
} else {
  # ---------- 7b. 兜底：内置 HLS-fMP4 切片 ----------
  # 为什么不用 DASH：DASH 把视频/音频拆成两条 AdaptationSet，分片名模板里没有
  # $RepresentationID$ 时两条流写同一批文件名互相覆盖 —— 就是"丢音轨"的根因。
  # HLS-fMP4 只输出一条 muxed 流：每个分片的 moof 里 video/audio 两条 traf 都在。
  New-Item -ItemType Directory -Force -Path $Out | Out-Null
  $work = Join-Path $env:TEMP ("pr-slice-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
  New-Item -ItemType Directory -Force -Path $work | Out-Null
  $m3u8 = Join-Path $work 'index.m3u8'
  $initSrc = Join-Path $work 'init.mp4'
  $segPattern = Join-Path $work 'seg%05d.m4s'

  if (-not $passthrough) {
    & $ffmpeg -y -v warning -i "$Source" -c:v libx264 -preset veryfast -crf 23 -maxrate $rate -bufsize $rate -c:a aac -b:a 128k -movflags +faststart (Join-Path $work 'source.mp4')
    if ($LASTEXITCODE -ne 0) { Die '转码失败' }
    $Source = Join-Path $work 'source.mp4'
    $mime = 'video/mp4; codecs=' + $q + 'avc1.64001f,mp4a.40.2' + $q
  }

  Say "切片中（内置 HLS-fMP4：每片约 ${p.segmentSeconds}s）..."
  # 分片名与 init 必须是**绝对路径**：HLS 复用器按"当前目录"解析相对名
  #（踩过：分片全掉进当前目录），而 Windows 版 ffmpeg 不接受正斜杠路径。
  & $ffmpeg -y -v warning -i "$Source" -map 0:v -map 0:a? -c copy \`
    -f hls -hls_time ${p.segmentSeconds} -hls_playlist_type vod -hls_segment_type fmp4 \`
    -hls_fmp4_init_filename "$initSrc" -hls_segment_filename "$segPattern" "$m3u8"
  if ($LASTEXITCODE -ne 0) {
    if ($passthrough) {
      Die "直通切片失败：HLS-fMP4 无法把 $vCodec/$aLabel 原样复用（该编码可能不支持直通）。请加 -Transcode 重新生成（会转码为 H.264/AAC），或改用 cmd/segmenter。"
    }
    Die '切片失败（看上面的 ffmpeg 输出）'
  }

  $segs = @(Get-ChildItem -LiteralPath $work -Filter 'seg*.m4s' | Sort-Object Name)
  if ($segs.Count -eq 0) { Die '没有产出任何分片' }

  # 每片时长从 m3u8 的 #EXTINF 逐行取（顺序即分片顺序）。
  # 条目数与分片数必须严格相等：对不上说明切片不完整，**明确失败**，
  # 绝不按平均时长静默回填（回填会让进度条与 seek 一直偏）。
  $durations = New-Object System.Collections.Generic.List[double]
  foreach ($line in [System.IO.File]::ReadAllLines($m3u8)) {
    if ($line -match '^#EXTINF:([0-9]*\\.?[0-9]+)') {
      $durations.Add([double]::Parse($Matches[1], [System.Globalization.CultureInfo]::InvariantCulture))
    }
  }
  if ($durations.Count -ne $segs.Count) {
    Die "分片时长条目数($($durations.Count))与分片数($($segs.Count))不一致：切片不完整，已中止（不按平均时长回填）。临时目录： $work"
  }

  # 拼包 + 逐片 sha256 + 写 index.json（结构与服务端/segmenter 完全一致）。
  Copy-Item -LiteralPath $initSrc -Destination (Join-Path $Out 'init.mp4') -Force
  $segments = New-Object System.Collections.Generic.List[object]
  $packs = New-Object System.Collections.Generic.List[object]
  $pts = 0.0
  $totalBytes = [long]0
  $packIndex = 0
  $packStream = $null
  $packOffset = [long]0
  $packBytes = [long]0
  $packFirst = 0
  $packCount = 0

  function Close-Pack {
    if ($script:packStream) {
      $script:packStream.Close()
      $script:packs.Add([ordered]@{ file = ('pack-{0:d4}.bin' -f $script:packIndex); firstSegment = $script:packFirst; count = $script:packCount; bytes = $script:packBytes })
      $script:packStream = $null
    }
  }

  for ($i = 0; $i -lt $segs.Count; $i++) {
    $file = $segs[$i]
    $bytes = [System.IO.File]::ReadAllBytes($file.FullName)
    $needNew = ($null -eq $packStream) -or (${p.packSize} -gt 1 -and $packCount -ge ${p.packSize})
    if ($needNew) {
      Close-Pack
      $packIndex++
      $packFirst = $i + 1
      $packCount = 0
      $packOffset = 0
      $packBytes = 0
      if (${p.packSize} -le 1) {
        # 不打包：每片一个文件，包记为自己
        $target = Join-Path $Out ("c{0:d5}.m4s" -f ($i + 1))
        [System.IO.File]::WriteAllBytes($target, $bytes)
        $packs.Add([ordered]@{ file = ("c{0:d5}.m4s" -f ($i + 1)); firstSegment = ($i + 1); count = 1; bytes = $bytes.Length })
        $packStream = $null
      } else {
        $path = Join-Path $Out ('pack-{0:d4}.bin' -f $packIndex)
        $packStream = [System.IO.File]::Create($path)
      }
    }

    $sha = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLower()
    $fileName = if (${p.packSize} -le 1) { "c{0:d5}.m4s" -f ($i + 1) } else { 'pack-{0:d4}.bin' -f $packIndex }
    $segments.Add([ordered]@{
      index = $i + 1
      file = $fileName
      offset = $packOffset
      size = $bytes.Length
      duration = [Math]::Round($durations[$i], 6)
      startPts = [Math]::Round($pts, 6)
      keyframe = $true
      sha256 = $sha
    })
    if ($packStream) { $packStream.Write($bytes, 0, $bytes.Length) }
    $packOffset += $bytes.Length
    $packBytes += $bytes.Length
    $packCount++
    $pts += $durations[$i]
    $totalBytes += $bytes.Length
  }
  Close-Pack

  $bitrate = [long](($totalBytes * 8) / [Math]::Max($duration, 1))
  $index = [ordered]@{
    version = 1
    initFile = 'init.mp4'
    mimeType = $mime
    totalDuration = [Math]::Round($duration, 6)
    segmentSec = [Math]::Round($duration / [Math]::Max($segments.Count, 1), 6)
    bitrateBps = $bitrate
    totalBytes = $totalBytes
    segments = $segments
    packs = $packs
  }
  $indexPath = Join-Path $Out 'index.json'
  [System.IO.File]::WriteAllText($indexPath, ($index | ConvertTo-Json -Depth 8), (New-Object System.Text.UTF8Encoding($false)))

  if (-not $KeepFragments) { Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue }
}

# ---------- 8. 产物自检 + 结果 ----------
Assert-Index $Out

$idx = [System.IO.File]::ReadAllText((Join-Path $Out 'index.json')) | ConvertFrom-Json
$segCount = @($idx.segments).Count
$packCount = @($idx.packs).Count
$totalBytes = [long]$idx.totalBytes
$bitrate = [long]$idx.bitrateBps

Say ""
Say "完成。产物目录： $Out"
Say ("  编码声明: {0}" -f [string]$idx.mimeType)
Say ("  分片 {0} 片 / {1} 个文件 / {2}" -f $segCount, ($packCount + 2), (Human $totalBytes))
Say ("  码率 {0:N2} Mbps" -f ($bitrate / 1000000))
$host0 = 12
$k0 = [Math]::Min(8, [Math]::Floor(($host0 * 1000000 * 0.8) / [Math]::Max($bitrate, 1)))
Say ""
Say "容量提示（假设主播上行 12 Mbps）: K0 = $k0"
Say "接下来：在主播页点「选择分片目录」选中这个目录即可开播。"
`
}

/** 生成 bash 版（Linux/macOS）。行为与文案必须与 PowerShell 版逐一对应。 */
export function buildBashScript(p: ScriptParams): string {
  const out = p.outputDir.replace(/\/+$/, '')
  const ffmpegUrl = p.ffmpegUrl.trim() || DEFAULT_FFMPEG_URLS.unix
  const forced = p.transcodeBitrate.trim()
  const downloads = usableDownloads(p.segmenterDownloads)
  // 清单原样烘焙（含 windows）：Git Bash / MSYS 下 `uname -s` 是 MINGW*/MSYS*，
  // 那种环境跑的就是 Windows 版二进制，所以 bash 版也需要 windows 那几项；
  // 否则这些用户会被迫走内置兜底路径，而那条路依赖 python3。
  const urlCases = downloads
    .map((d) => `    '${quoteSafe(d.os)}/${quoteSafe(d.arch)}') printf '%s' '${quoteSafe(d.url)}' ;;`)
    .join('\n')
  const shaCases = downloads
    .map((d) => `    '${quoteSafe(d.os)}/${quoteSafe(d.arch)}') printf '%s' '${lowerHex(d.sha256)}' ;;`)
    .join('\n')

  return `#!/usr/bin/env bash
# ============================================================================
#  ProjectionRoom 一键切片脚本（由浏览器生成）
#  切成：init.mp4 + pack-*.bin + index.json
#  用法：  bash <脚本名>.sh                              # 用内置的路径/文件名
#          bash <脚本名>.sh /path/to/movie.mp4           # 指定源文件
#          bash <脚本名>.sh /path/to/movie.mp4 -Transcode # 强制转码为 H.264/AAC
#          bash <脚本名>.sh /path/to/movie.mp4 -NoExe     # 不下载 segmenter，走内置 HLS-fMP4
#          TRANSCODE=1 bash <脚本名>.sh                  # 同上（环境变量写法）
#  没有 ffmpeg 时：提示 5 秒后用 curl 自动下载静态版到脚本旁边（Ctrl+C 取消）。
#
#  ★ 这个脚本会做的事（请先看一眼）：
#    1) 找输入视频；找 ffmpeg/ffprobe，没有就提示 5 秒后用 curl 自动下载一份；
#    2) **从服务器下载 segmenter 可执行文件到脚本旁边，并校验它的 sha256**；
#       校验不过会删掉文件并立刻退出（绝不运行来路不明的可执行文件）；
#    3) 调用 segmenter 切片（H.264/AAC/AV1/VP9 + AAC/Opus 走无损直切，其它编码自动转码）；
#    4) 用 ffprobe 断言产物轨道数与 index.json 的声明一致，不一致就报错退出。
#    它仍然需要 ffmpeg/ffprobe（segmenter 用它们做重新封装/转码）。
#
#  默认策略与 Windows 版一致：浏览器能解的编码族只 remux（-c copy），不整片转码 ——
#  142 分钟的 AV1 转码要数小时，直通只受磁盘带宽限制。
#  下载不到 segmenter（离线 / -NoExe / 服务端没发布）时退回内置 HLS-fMP4 路径。
# ============================================================================
set -euo pipefail

TRANSCODE="\${TRANSCODE:-}"
NO_EXE="\${NO_EXE:-}"
SRC=""
SEGMENTER_URL="\${SEGMENTER_URL:-}"
SEGMENTER_SHA="\${SEGMENTER_SHA256:-}"
for _arg in "$@"; do
  case "$_arg" in
    -Transcode|--transcode) TRANSCODE=1 ;;
    -NoExe|--no-exe) NO_EXE=1 ;;
    -SegmenterUrl=*) SEGMENTER_URL="\${_arg#*=}" ;;
    -SegmenterSha256=*) SEGMENTER_SHA="\${_arg#*=}" ;;
    *) [ -n "$SRC" ] || SRC="$_arg" ;;
  esac
done
[ -n "$SRC" ] || SRC="${p.sourcePath.trim()}"
OUT="\${OUT:-${out}}"
SEGMENT_SECONDS="\${SEGMENT_SECONDS:-${p.segmentSeconds}}"
PACK_SIZE="\${PACK_SIZE:-${p.packSize}}"
TRANSCODE_BITRATE="\${TRANSCODE_BITRATE:-${forced}}"
FFMPEG_URL="\${FFMPEG_URL:-${ffmpegUrl}}"
WANT_NAME="${p.sourceName.trim()}"
HERE="$(cd "$(dirname "\${BASH_SOURCE[0]}")" && pwd)"

say()  { printf '%s\\n' "$*"; }
warn() { printf '%s\\n' "$*" >&2; }
die()  { printf '%s\\n' "$*" >&2; exit 1; }

file_size() {
  if stat -f%z "$1" >/dev/null 2>&1; then stat -f%z "$1"; else stat -c%s "$1"; fi
}
human() {
  awk -v b="$1" 'BEGIN{ if (b>=1073741824) printf "%.2f GiB", b/1073741824; else if (b>=1048576) printf "%.1f MiB", b/1048576; else printf "%.0f KiB", b/1024 }'
}
file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else printf ''
  fi
}
# 在 Git Bash / MSYS 下跑的常常是 Windows 版 ffmpeg 与 segmenter：它们不认
# /tmp/xxx 这类 POSIX 路径，必须用 cygpath -w 转成 C:\\... 形式。
# 纯 Linux/macOS 上没有 cygpath，原样返回（正因为如此，两条路径都不会走歪）。
native_path() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -w -- "$1"; else printf '%s' "$1"; fi
}

# segmenter 二进制清单（由浏览器在生成时从 GET /api/downloads/segmenter 取回并烘焙进来）。
# 运行时按 uname 选一项；两个函数都返回空串说明服务端没有发布该平台。
segmenter_url_for() {
  case "$1" in
${urlCases ? urlCases + '\n' : ''}    *) printf '' ;;
  esac
}
segmenter_sha_for() {
  case "$1" in
${shaCases ? shaCases + '\n' : ''}    *) printf '' ;;
  esac
}

# ---------- 1. 找输入文件 ----------
if [ -z "$SRC" ] || [ ! -f "$SRC" ]; then
  found=""
  if [ -n "$WANT_NAME" ]; then
    for d in "$HOME/Desktop" "$HOME/Downloads" "$HOME/Videos" "$HOME/Documents" "$PWD" "$HERE"; do
      [ -d "$d" ] && [ -f "$d/$WANT_NAME" ] && { found="$d/$WANT_NAME"; break; }
    done
  fi
  if [ -n "$found" ]; then
    SRC="$found"; say "在常见目录里找到了： $SRC"
  else
    warn "没找到视频文件。用法：bash $0 /path/to/movie.mp4"
    [ -n "$WANT_NAME" ] && warn "要找的文件名是： $WANT_NAME"
    read -r -p "视频文件完整路径: " SRC
  fi
fi
[ -f "$SRC" ] || die "找不到输入文件： $SRC"
SRC="$(cd "$(dirname "$SRC")" && pwd)/$(basename "$SRC")"
say "输入: $SRC  ($(human "$(file_size "$SRC")"))"

# ---------- 2. 找 ffmpeg；没有就提示 5 秒后用 curl 下载 ----------
find_tool() { command -v "$1" 2>/dev/null || true; }
FFMPEG="$(find_tool ffmpeg)"; FFPROBE="$(find_tool ffprobe)"
if [ -z "$FFMPEG" ] && [ -x "$HERE/bin/ffmpeg" ]; then FFMPEG="$HERE/bin/ffmpeg"; FFPROBE="$HERE/bin/ffprobe"; fi
if [ -z "$FFMPEG" ] || [ -z "$FFPROBE" ]; then
  warn "没有找到 ffmpeg/ffprobe。"
  warn "5 秒后用 curl 自动下载静态版到 $HERE/bin（Ctrl+C 取消）： $FFMPEG_URL"
  for i in 5 4 3 2 1; do printf '  %s ...\\n' "$i"; sleep 1; done
  mkdir -p "$HERE/bin"
  pkg="$HERE/bin/ffmpeg-pkg"
  case "$FFMPEG_URL" in
    *.zip) curl -L --fail -o "$pkg.zip" "$FFMPEG_URL" && (unzip -o -q "$pkg.zip" -d "$pkg" || die '解压失败（需要 unzip）') ;;
    *.tar.xz) curl -L --fail -o "$pkg.tar.xz" "$FFMPEG_URL" && tar -xJf "$pkg.tar.xz" -C "$HERE/bin" ;;
    *) curl -L --fail -o "$HERE/bin/ffmpeg" "$FFMPEG_URL" && chmod +x "$HERE/bin/ffmpeg" ;;
  esac
  FFMPEG="$(find "$HERE/bin" -name ffmpeg -type f | head -1)"
  FFPROBE="$(find "$HERE/bin" -name ffprobe -type f | head -1)"
  [ -n "$FFMPEG" ] || die "自动下载失败，请手动安装 ffmpeg 后重试。"
  chmod +x "$FFMPEG" "$FFPROBE" 2>/dev/null || true
fi
say "ffmpeg: $FFMPEG"

# ---------- 3. 探测 ----------
# 逐字段用 ffprobe 取（-of default=noprint_wrappers=1:nokey=1 只输出值）：比在 bash 里解 JSON 稳，
# 编码字段的顺序/存在性变化都不会把结果打偏。末尾的 || true 是为了无音轨的文件：
# ffprobe 对 "a:0" 会以非 0 退出，配合 set -o pipefail 会把整个脚本带崩。
probe_field() { "$FFPROBE" -v error -select_streams "$1" -show_entries "$2" -of default=noprint_wrappers=1:nokey=1 -- "$(native_path "$SRC")" 2>/dev/null | head -1 | tr -d '\\r' || true; }
duration="$("$FFPROBE" -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 -- "$(native_path "$SRC")" | head -1 | tr -d '\\r')"
vcodec="$(probe_field v:0 stream=codec_name)"
acodec="$(probe_field a:0 stream=codec_name)"
vpix="$(probe_field v:0 stream=pix_fmt)"
vprofile="$(probe_field v:0 stream=profile)"
vlevel="$(probe_field v:0 stream=level)"
minutes="$(awk -v d="$duration" 'BEGIN { printf "%.1f", d / 60 }')"
say "源: \${duration}s (\${minutes} 分钟)  视频=\${vcodec:-?} 音频=\${acodec:-无}"
if awk -v d="$duration" 'BEGIN { exit !(d > 3600) }'; then
  warn "超过 60 分钟：本地切片没问题，但服务端切片会拒绝这么长的视频。"
fi

# ---------- 4. 编码族判定：能解的直通（-c copy），解不了的转码 ----------
case "$vcodec" in
  h264) vfamily=avc1 ;;
  av1)  vfamily=av01 ;;
  vp9)  vfamily=vp09 ;;
  *)    vfamily='' ;;
esac
audio_ok=1
case "$acodec" in
  '')   afamily='' ;;
  aac)  afamily=mp4a.40.2 ;;
  opus) afamily=opus ;;
  *)    afamily=''; audio_ok=0 ;;
esac
passthrough=1
[ -n "$vfamily" ] || passthrough=0
[ "$audio_ok" = "1" ] || passthrough=0
[ -z "$TRANSCODE" ] || passthrough=0
rate="\${TRANSCODE_BITRATE:-1800k}"
# AV1 的编码串按 ffprobe 报出的真实 profile/level 拼（level 就是 av1C 里的 seq_level_idx_0）：
# 写死一个对不上的 level，严格校验的浏览器会直接判 isTypeSupported 为假 → 黑屏。
# profile: Main=0 / High=1 / Professional=2；tier 只有 High 才写 H，ffprobe 不报 tier 时按 Main 处理。
case "$vpix" in *10*) is10bit=1 ;; *) is10bit=0 ;; esac
case "$vprofile" in High) av1_profile=1 ;; Professional) av1_profile=2 ;; *) av1_profile=0 ;; esac
case "$vlevel" in ''|*[!0-9]*) av1_level=05 ;; *) av1_level="$(printf '%02d' "$vlevel")" ;; esac
if [ "$is10bit" = "1" ]; then av1_depth=10; else av1_depth=08; fi
case "$vfamily" in
  avc1) vcodec_string=avc1.64001f ;;
  av01) vcodec_string="av01.$av1_profile.$av1_level""M.$av1_depth" ;;
  vp09) vcodec_string=vp09.00.10.08 ;;
  *)    vcodec_string='' ;;
esac
# mimeType 只在"HLS-fMP4 兜底路径"里用；segmenter 路径的 mimeType 由它自己写进 index.json。
q='"'
if [ "$passthrough" != "1" ]; then
  if [ -n "$TRANSCODE" ]; then
    say '按 -Transcode 显式要求转码为 H.264/AAC…'
  else
    say "源编码（$vcodec/\${acodec:-无音轨}）浏览器可能解不了，转码为 H.264/AAC…"
  fi
else
  say "直通（不转码）：$vcodec / \${acodec:-无音轨}"
  case "$vfamily" in
    av01|vp09)
      warn '注意：AV1/VP9 只在支持它的浏览器能播（Chrome 基本都行；Safari 与部分 Firefox 不行）。'
      warn '      要最大兼容性就加 -Transcode（代价是整片转码，142 分钟要数小时）。'
      ;;
  esac
fi

# ---------- 5. 取 segmenter（下载 + sha256 校验），拿不到就用内置 HLS-fMP4 兜底 ----------
SEGMENTER_BIN=""
if [ -n "$NO_EXE" ]; then
  warn '按 -NoExe：跳过 segmenter 下载，改用内置的 HLS-fMP4 兜底切片。'
else
  os_name=""
  # MINGW*/MSYS*/CYGWIN* = Git Bash / MSYS2 / Cygwin：那种环境跑的是 Windows 版二进制。
  case "$(uname -s)" in Linux) os_name=linux ;; Darwin) os_name=darwin ;; MINGW*|MSYS*|CYGWIN*) os_name=windows ;; esac
  arch_name=""
  case "$(uname -m)" in x86_64|amd64) arch_name=amd64 ;; arm64|aarch64) arch_name=arm64 ;; esac
  seg_key="\${os_name}/\${arch_name}"
  seg_url=""
  seg_sha=""
  if [ -n "$SEGMENTER_URL" ] || [ -n "$SEGMENTER_SHA" ]; then
    seg_url="$SEGMENTER_URL"; seg_sha="$(printf '%s' "$SEGMENTER_SHA" | tr 'A-F' 'a-f')"
  elif [ -n "$os_name" ] && [ -n "$arch_name" ]; then
    seg_url="$(segmenter_url_for "$seg_key")"
    seg_sha="$(segmenter_sha_for "$seg_key")"
  fi

  if [ -z "$seg_url" ]; then
    warn "服务端没有发布 \${seg_key:-当前平台} 的 segmenter 二进制（清单为空或缺这一项）。"
    warn '改用内置的 HLS-fMP4 兜底切片路径（同样是单条 muxed 流，不会丢音轨）。'
  elif [ -z "$seg_sha" ]; then
    die "这个下载地址没有 sha256，无法校验二进制；拒绝运行来路不明的可执行文件。请用 -SegmenterSha256=<十六进制> 显式给出，或加 -NoExe 走内置 HLS-fMP4 兜底路径。"
  else
    tmp_bin="$HERE/segmenter.download"
    # MSYS 下可执行文件必须带 .exe，否则 CreateProcess 找不到它。
    case "$os_name" in windows) bin_path="$HERE/segmenter.exe" ;; *) bin_path="$HERE/segmenter" ;; esac
    rm -f "$tmp_bin"
    say "下载 segmenter： $seg_url"
    say "  → $bin_path"
    if ! curl -L --fail --silent --show-error -o "$tmp_bin" "$seg_url"; then
      rm -f "$tmp_bin"
      warn "下载 segmenter 失败： $seg_url"
      warn '改用内置的 HLS-fMP4 兜底切片路径。'
    else
      actual="$(file_sha256 "$tmp_bin")"
      if [ -z "$actual" ]; then
        rm -f "$tmp_bin"
        die '本机既没有 sha256sum 也没有 shasum，无法校验下载的二进制；请先装上其中任一个，或加 -NoExe 走内置 HLS-fMP4 兜底路径。'
      fi
      if [ "$actual" != "$seg_sha" ]; then
        rm -f "$tmp_bin"
        die "下载的二进制校验失败，可能被篡改或中转损坏（期望 sha256 $seg_sha，实际 $actual）。已删除下载文件，未执行它。"
      fi
      chmod +x "$tmp_bin"
      mv -f "$tmp_bin" "$bin_path"
      SEGMENTER_BIN="$bin_path"
      say "  sha256 校验通过： $actual"
    fi
  fi
fi

# ---------- 6. 产物自检（两条路径共用）----------
# 把"丢音轨"这类静默失败变成明确失败：index.json 声明了几条编码，init.mp4 就必须有几条轨。
assert_index() {
  _dir="$1"
  [ -f "$_dir/init.mp4" ] || die "产物缺少 init.mp4： $_dir/init.mp4"
  [ -f "$_dir/index.json" ] || die "产物缺少 index.json： $_dir/index.json"

  # 从 index.json 里抠出 codecs="..."。
  # 注意：JSON 里引号是**转义**的（mimeType 的值长这样：codecs=\"avc1…,mp4a…\"），
  # 所以必须先把反斜杠删掉再抠；否则一条都匹配不到，"声明的编码条数"会变成空串，
  # 下面的 -eq 会报 "integer expression expected"，把正确产物误判成坏切片（真踩过）。
  _mime_line="$(tr -d '\\n' < "$_dir/index.json")"
  _plain="$(printf '%s' "$_mime_line" | tr -d '\\\\')"
  _codecs="$(printf '%s' "$_plain" | grep -o 'codecs="[^"]*"' | head -1 || true)"
  _codecs="\${_codecs#codecs=}"
  _codecs="$(printf '%s' "$_codecs" | tr -d '"')"
  # 末尾补一个逗号：空串也保证 awk 至少看到一条记录，于是"没有编码串"退化成 1 条
  #（与生成时的单轨默认一致）；有 n 条编码时逗号数正好是 n-1。
  _declared="$(printf '%s,' "$_codecs" | awk -F, '{print NF-1}')"

  _tracks="$("$FFPROBE" -v error -show_entries stream=codec_type -of csv=p=0 -- "$(native_path "$_dir/init.mp4")" | sed '/^[[:space:]]*$/d' | wc -l | tr -d ' ')"
  [ "$_tracks" -eq "$_declared" ] || die "切片产出与声明编码不一致（疑似多流互相覆盖）：index.json 声明了 $_declared 条编码，init.mp4 里只有 $_tracks 条轨。请改用 cmd/segmenter 或报告此问题。"
  say "轨道自检通过：init.mp4 有 $_tracks 条轨，与 index.json 的声明一致"
}

# ---------- 7. 切片 ----------
if [ -n "$SEGMENTER_BIN" ]; then
  mkdir -p "$OUT"
  say "切片中（segmenter：每片约 \${SEGMENT_SECONDS}s，每 \${PACK_SIZE} 片一个包）..."
  if [ "$passthrough" = "1" ]; then
    "$SEGMENTER_BIN" -in "$(native_path "$SRC")" -out "$(native_path "$OUT")" \\
      -fragment -frag-sec "$SEGMENT_SECONDS" -pack "$PACK_SIZE" || die 'segmenter 切片失败（看上面它自己的输出）'
  else
    "$SEGMENTER_BIN" -in "$(native_path "$SRC")" -out "$(native_path "$OUT")" \\
      -transcode "$rate" -frag-sec "$SEGMENT_SECONDS" -pack "$PACK_SIZE" || die 'segmenter 切片失败（看上面它自己的输出）'
  fi
else
  # ---------- 7b. 兜底：内置 HLS-fMP4 切片 ----------
  # 为什么不用 DASH：DASH 把视频/音频拆成两条 AdaptationSet，分片名模板里没有
  # $RepresentationID$ 时两条流写同一批文件名互相覆盖 —— 就是"丢音轨"的根因。
  # HLS-fMP4 只输出一条 muxed 流：每个分片的 moof 里 video/audio 两条 traf 都在。
  mkdir -p "$OUT"
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  m3u8="$work/index.m3u8"

  if [ "$passthrough" != "1" ]; then
    "$FFMPEG" -y -v warning -i "$(native_path "$SRC")" -c:v libx264 -preset veryfast -crf 23 \\
      -maxrate "$rate" -bufsize "$rate" -c:a aac -b:a 128k -movflags +faststart \\
      "$(native_path "$work/source.mp4")" || die '转码失败'
    SRC="$work/source.mp4"
    mime_type="video/mp4; codecs=\${q}avc1.64001f,mp4a.40.2\${q}"
  else
    if [ -z "$acodec" ]; then
      mime_type="video/mp4; codecs=\${q}$vcodec_string\${q}"
    else
      mime_type="video/mp4; codecs=\${q}$vcodec_string,$afamily\${q}"
    fi
  fi

  say "切片中（内置 HLS-fMP4：每片约 \${SEGMENT_SECONDS}s）..."
  # 绝对路径：HLS 复用器按"当前目录"解析相对分片名（与 m3u8 所在目录无关）。
  "$FFMPEG" -y -v warning -i "$(native_path "$SRC")" -map 0:v -map 0:a? -c copy \\
    -f hls -hls_time "$SEGMENT_SECONDS" -hls_playlist_type vod -hls_segment_type fmp4 \\
    -hls_fmp4_init_filename "$(native_path "$work/init.mp4")" \\
    -hls_segment_filename "$(native_path "$work/seg%05d.m4s")" \\
    "$(native_path "$m3u8")" || {
      if [ "$passthrough" = "1" ]; then
        die "直通切片失败：HLS-fMP4 无法把 $vcodec/\${acodec:-无音轨} 原样复用（该编码可能不支持直通）。请加 -Transcode 重新生成（会转码为 H.264/AAC），或改用 cmd/segmenter。"
      fi
      die '切片失败（看上面的 ffmpeg 输出）'
    }

  cp "$work/init.mp4" "$OUT/init.mp4"
  seg_count="$(find "$work" -name 'seg*.m4s' | wc -l | tr -d ' ')"
  [ "$seg_count" -gt 0 ] || die '没有产出任何分片'

  # 每片时长从 m3u8 的 #EXTINF 逐行取（顺序即分片顺序）。条目数与分片数必须严格相等：
  # 对不上说明切片不完整，**明确失败**，绝不按平均时长静默回填。
  extinf="$(sed -n 's/^#EXTINF:\\([0-9][0-9.]*\\),.*$/\\1/p' "$m3u8")"
  extinf_count="$(printf '%s\\n' "$extinf" | sed '/^$/d' | wc -l | tr -d ' ')"
  [ "$extinf_count" -eq "$seg_count" ] || die "分片时长条目数($extinf_count)与分片数($seg_count)不一致：切片不完整，已中止（不按平均时长回填）。"

  # 拼包 + sha256 + index.json（结构与服务端/segmenter 完全一致）。
  python_ok=0
  command -v python3 >/dev/null 2>&1 && python_ok=1

  if [ "$python_ok" = "1" ]; then
    WORK="$(native_path "$work")" OUT_NATIVE="$(native_path "$OUT")" PACK_SIZE="$PACK_SIZE" EXTINF="$extinf" \\
    DURATION="$duration" MIME="$mime_type" \\
    python3 - <<'PYEOF'
import json, os, hashlib, glob, shutil, sys
work, out = os.environ['WORK'], os.environ['OUT_NATIVE']
pack_size = int(os.environ['PACK_SIZE'])
segs = sorted(glob.glob(os.path.join(work, 'seg*.m4s')))
# HLS 的 #EXTINF 就是复用器写下的真实分片时长，顺序即分片顺序。
durations = [float(x) for x in os.environ['EXTINF'].split()]
if len(durations) != len(segs):
    print(f"错误：分片时长条目数({len(durations)})与分片数({len(segs)})不一致：切片不完整，已中止（不按平均时长回填）。", file=sys.stderr)
    sys.exit(1)
shutil.copyfile(os.path.join(work, 'init.mp4'), os.path.join(out, 'init.mp4'))
segments, packs, pts, total = [], [], 0.0, 0
pack = None; pack_index = 0; pack_offset = 0; pack_bytes = 0
pack_first = 0; pack_count = 0
def close_pack():
    if pack is not None:
        packs.append({"file": f"pack-{pack_index:04d}.bin", "firstSegment": pack_first,
                      "count": pack_count, "bytes": pack_bytes})
for i, path in enumerate(segs, start=1):
    data = open(path, 'rb').read()
    if pack is None or (pack_size > 1 and pack_count >= pack_size):
        close_pack()
        pack = None
        pack_index += 1; pack_first = i; pack_count = 0; pack_offset = 0; pack_bytes = 0
        if pack_size <= 1:
            name = f"c{i:05d}.m4s"
            open(os.path.join(out, name), 'wb').write(data)
            packs.append({"file": name, "firstSegment": i, "count": 1, "bytes": len(data)})
        else:
            name = f"pack-{pack_index:04d}.bin"
            pack = open(os.path.join(out, name), 'wb')
    segments.append({"index": i, "file": name, "offset": pack_offset, "size": len(data),
                     "duration": round(durations[i-1], 6), "startPts": round(pts, 6),
                     "keyframe": True, "sha256": hashlib.sha256(data).hexdigest()})
    if pack is not None:
        pack.write(data)
    pack_offset += len(data); pack_bytes += len(data); pack_count += 1
    pts += durations[i-1]; total += len(data)
close_pack()
if pack is not None: pack.close()
duration = float(os.environ['DURATION'])
index = {"version": 1, "initFile": "init.mp4",
         "mimeType": os.environ['MIME'],
         "totalDuration": round(duration, 6),
         "segmentSec": round(duration / max(len(segments), 1), 6),
         "bitrateBps": int(total * 8 / max(duration, 1)),
         "totalBytes": total, "segments": segments, "packs": packs}
with open(os.path.join(out, 'index.json'), 'w', encoding='utf-8') as f:
    json.dump(index, f, ensure_ascii=False)
print(f"完成：{len(segments)} 片 / {len(packs) + 2} 个文件 / {total} 字节")
PYEOF
  else
    die "没有 python3，无法在内置 HLS-fMP4 兜底路径里写 index.json。请安装 python3，或让服务端发布 segmenter 二进制（去掉 -NoExe），或改用 Windows 版脚本。"
  fi
fi

# ---------- 8. 产物自检 + 结果 ----------
assert_index "$OUT"

say ""
say "产物目录： $OUT"
say "接下来：在主播页点「选择分片目录」选中它即可开播。"
`
}
