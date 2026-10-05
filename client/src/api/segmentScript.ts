// 一键切片脚本生成器（纯函数，无副作用）。
//
// 为什么由浏览器生成而不是发一份固定脚本：
//   - 分片秒数、每包片数、输出目录、源文件名都要跟着当前这一场走；
//   - 用户机器上 ffmpeg 的有无、路径、下载源都得能在界面上改（默认值来自服务端配置）。
//
// 生成的脚本只依赖 ffmpeg/ffprobe；没有的话按用户的明确要求：
// 先给 5 秒警告，然后用 curl 自动下载一份到脚本旁边再用。
//
// 浏览器**拿不到**用户所选文件的完整本地路径（安全限制，File 对象只有 name），
// 所以路径按优先级取：命令行参数 / 把文件拖到脚本上 / 界面里粘贴的路径 /
// 在常见目录（桌面、下载、视频、文档、当前目录）按文件名找 / 交互式询问。

export type ScriptPlatform = 'windows' | 'unix'

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

/**
 * 生成 PowerShell 版一键脚本（Windows 首选）。
 *
 * 步骤：找输入 → 找 ffmpeg（没有就 5 秒后 curl 下载）→ ffprobe 探测 →
 * 直接对源文件做 DASH 切片（H.264/AAC 走 -c copy，否则转码）→
 * 按 PackSize 拼包 + 逐片 sha256 + 写 index.json（含 packs）→ 打印容量提示。
 */
export function buildPowerShellScript(p: ScriptParams): string {
  const out = p.outputDir.replace(/\\+$/, '')
  const ffmpegUrl = p.ffmpegUrl.trim() || DEFAULT_FFMPEG_URLS.windows
  const forced = p.transcodeBitrate.trim()

  return `# ============================================================================
#  ProjectionRoom 一键切片脚本（由浏览器生成）
#  把视频切成放映室可用的分片目录：init.mp4 + pack-*.bin + index.json
#
#  用法（三种任选）：
#    1) 双击/右键"使用 PowerShell 运行"
#    2) 把视频文件直接拖到这个脚本上（最省事，路径自动带进来）
#    3) 命令行： .\\<脚本名>.ps1 -Source "D:\\video\\movie.mp4"
#
#  需要 ffmpeg。**没装也没关系**：脚本会提示 5 秒后用 curl 自动下载一份
#  放在脚本旁边（按 Ctrl+C 可取消）。下载源可用 -FfmpegUrl 覆盖。
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
  [string]$FfmpegDir = "",
  [string]$FfmpegUrl = "${ffmpegUrl}",
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
$probe = & $ffprobe -v error -show_entries format=duration,bit_rate -show_entries stream=codec_name,codec_type -of json -- "$Source" | ConvertFrom-Json
$duration = [double]$probe.format.duration
$vCodec = ($probe.streams | Where-Object { $_.codec_type -eq 'video' } | Select-Object -First 1).codec_name
$aCodec = ($probe.streams | Where-Object { $_.codec_type -eq 'audio' } | Select-Object -First 1).codec_name
$audio = if ([string]::IsNullOrEmpty($aCodec)) { '无' } else { $aCodec }
Say ("源: {0:N1}s ({1:N1} 分钟)  视频={2} 音频={3}" -f $duration, ($duration / 60), $vCodec, $audio)
if ($duration -gt 3600) { Warn "超过 60 分钟：本地切片没问题，但服务端切片会拒绝这么长的视频。" }

# ---------- 4. 切片（DASH：init + 每片一个 m4s + MPD 时间轴）----------
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$work = Join-Path $env:TEMP ("pr-slice-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Force -Path $work | Out-Null
$mpd = Join-Path $work 'index.mpd'

# 播放器要的是浏览器能解的编码：H.264/AAC 直接 copy，其它一律转码。
$playable = ($vCodec -eq 'h264') -and ([string]::IsNullOrEmpty($aCodec) -or $aCodec -eq 'aac')
$force = -not [string]::IsNullOrWhiteSpace($TranscodeBitrate)
if (-not $playable -or $force) {
  Say "转码为 H.264/AAC（源编码浏览器可能不支持）..."
  $rate = if ($force) { $TranscodeBitrate } else { '1800k' }
  & $ffmpeg -y -v warning -i "$Source" -c:v libx264 -preset veryfast -crf 23 -maxrate $rate -bufsize ($rate -replace 'k$','k') -c:a aac -b:a 128k -movflags +faststart $work/source.mp4
  if ($LASTEXITCODE -ne 0) { Die '转码失败' }
  $Source = Join-Path $work 'source.mp4'
}

Say "切片中（每片约 ${p.segmentSeconds}s）..."
# 分片名必须是**绝对路径**：DASH 复用器把相对路径解析到"当前目录"而不是 mpd 目录
#（踩过：分片全掉进仓库根目录，脚本在 work 目录里一个都找不到）。
# 单引号拼接是为了不让 PowerShell 把 $Number / $ 当变量插值。
& $ffmpeg -y -v warning -i "$Source" -map 0:v -map 0:a? -c copy \`
  -f dash -seg_duration ${p.segmentSeconds} -use_timeline 1 -use_template 1 \`
  -init_seg_name ($work + '\\init.mp4') -media_seg_name ($work + '\\seg$Number%05d$.m4s') "$mpd"
if ($LASTEXITCODE -ne 0) { Die '切片失败（看上面的 ffmpeg 输出）' }

$segs = Get-ChildItem -LiteralPath $work -Filter 'seg*.m4s' | Sort-Object Name
if ($segs.Count -eq 0) { Die '没有产出任何分片' }

# MPD 的 SegmentTimeline 给出每片时长：<S t="0" d="60000" r="12"/>
# 注意两处都踩过坑：1) SegmentTemplate 在 AdaptationSet 下（不是 Period）；
# 2) MPD 带默认命名空间，XPath 必须挂 namespace manager，否则一条都选不中；
# 3) timescale 也在 SegmentTemplate 上，取不到会让时长整体缩水（60000/1e6 = 0.06s）。
$xml = [xml](Get-Content -LiteralPath $mpd -Raw)
$ns = New-Object System.Xml.XmlNamespaceManager($xml.NameTable)
$ns.AddNamespace('d', $xml.DocumentElement.NamespaceURI)
$tpl = $xml.SelectSingleNode('//d:SegmentTemplate', $ns)
$timescale = if ($tpl -and $tpl.timescale) { [double]$tpl.timescale } else { 1000000 }
$durations = New-Object System.Collections.Generic.List[double]
foreach ($s in $xml.SelectNodes('//d:SegmentTimeline/d:S', $ns)) {
  $d = [double]$s.d / $timescale
  $r = if ($s.r) { [int]$s.r } else { 0 }
  for ($i = 0; $i -le $r; $i++) { $durations.Add($d) }
}
if ($durations.Count -ne $segs.Count) {
  Warn "时间轴条目数($($durations.Count))与分片数($($segs.Count))不一致，按平均时长回填。"
  $durations.Clear()
  for ($i = 0; $i -lt $segs.Count; $i++) { $durations.Add(($duration / $segs.Count)) }
}

# ---------- 5. 拼包 + 逐片 sha256 + 写 index.json ----------
$initSrc = Join-Path $work 'init.mp4'
$initDst = Join-Path $Out 'init.mp4'
Copy-Item -LiteralPath $initSrc -Destination $initDst -Force

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

$mime = if ([string]::IsNullOrEmpty($aCodec) -and $playable) { 'video/mp4; codecs="avc1.64001f"' } else { 'video/mp4; codecs="avc1.64001f,mp4a.40.2"' }
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

# ---------- 6. 结果 ----------
Say ""
Say "完成。产物目录： $Out"
Say ("  分片 {0} 片 / {1} 个文件 / {2}" -f $segments.Count, ($packs.Count + 2), (Human $totalBytes))
Say ("  码率 {0:N2} Mbps" -f ($bitrate / 1000000))
$host0 = 12
$k0 = [Math]::Min(8, [Math]::Floor(($host0 * 1000000 * 0.8) / [Math]::Max($bitrate, 1)))
Say ""
Say "容量提示（假设主播上行 12 Mbps）: K0 = $k0"
Say "接下来：在主播页点「选择分片目录」选中这个目录即可开播。"
`
}

/** 生成 bash 版（Linux/macOS）。 */
export function buildBashScript(p: ScriptParams): string {
  const out = p.outputDir.replace(/\/+$/, '')
  const ffmpegUrl = p.ffmpegUrl.trim() || DEFAULT_FFMPEG_URLS.unix
  const forced = p.transcodeBitrate.trim()

  return `#!/usr/bin/env bash
# ============================================================================
#  ProjectionRoom 一键切片脚本（由浏览器生成）
#  切成：init.mp4 + pack-*.bin + index.json
#  用法：  bash <脚本名>.sh                       # 用内置的路径/文件名
#          bash <脚本名>.sh /path/to/movie.mp4    # 指定源文件
#  没有 ffmpeg 时：提示 5 秒后用 curl 自动下载静态版到脚本旁边（Ctrl+C 取消）。
# ============================================================================
set -euo pipefail

SRC="\${1:-${p.sourcePath.trim()}}"
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
probe="$("$FFPROBE" -v error -show_entries format=duration -show_entries stream=codec_name,codec_type -of json -- "$SRC")"
duration="$(printf '%s' "$probe" | sed -n 's/.*"duration": *"\\([0-9.]*\\)".*/\\1/p' | head -1)"
vcodec="$(printf '%s' "$probe" | tr -d ' \\n' | sed -n 's/.*"codec_name":"\\([a-z0-9_]*\\)","codec_type":"video".*/\\1/p' | head -1)"
acodec="$(printf '%s' "$probe" | tr -d ' \\n' | sed -n 's/.*"codec_name":"\\([a-z0-9_]*\\)","codec_type":"audio".*/\\1/p' | head -1)"
say "源: \${duration}s  视频=\${vcodec:-?} 音频=\${acodec:-无}"

# ---------- 4. 切片 ----------
mkdir -p "$OUT"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mpd="$work/index.mpd"

playable=0
[ "$vcodec" = "h264" ] && { [ -z "$acodec" ] || [ "$acodec" = "aac" ]; } && playable=1
if [ "$playable" = "0" ] || [ -n "$TRANSCODE_BITRATE" ]; then
  say "转码为 H.264/AAC ..."
  rate="\${TRANSCODE_BITRATE:-1800k}"
  "$FFMPEG" -y -v warning -i "$SRC" -c:v libx264 -preset veryfast -crf 23 -maxrate "$rate" -bufsize "$rate" -c:a aac -b:a 128k -movflags +faststart "$work/source.mp4"
  SRC="$work/source.mp4"
fi

say "切片中（每片约 \${SEGMENT_SECONDS}s）..."
# 绝对路径：DASH 复用器按"当前目录"解析相对分片名（与 mpd 所在目录无关）。
"$FFMPEG" -y -v warning -i "$SRC" -map 0:v -map 0:a? -c copy \\
  -f dash -seg_duration "$SEGMENT_SECONDS" -use_timeline 1 -use_template 1 \\
  -init_seg_name "$work/init.mp4" -media_seg_name "$work/seg\\$Number%05d\\$.m4s" "$mpd"

cp "$work/init.mp4" "$OUT/init.mp4"
seg_count="$(find "$work" -name 'seg*.m4s' | wc -l | tr -d ' ')"
[ "$seg_count" -gt 0 ] || die '没有产出任何分片'

# MPD 时间轴：<S t="0" d="2000000" r="12"/>
timeline="$(tr '>' '>\\n' < "$mpd" | sed -n 's/.*<S .*d="\\([0-9]*\\)".*r="\\([0-9]*\\)".*/\\1 \\2/p')"
timescale="$(tr '>' '>\\n' < "$mpd" | sed -n 's/.*timescale="\\([0-9]*\\)".*/\\1/p' | head -1)"
[ -n "$timescale" ] || timescale=1000000

# ---------- 5. 拼包 + sha256 + index.json ----------
python_ok=0
command -v python3 >/dev/null 2>&1 && python_ok=1

if [ "$python_ok" = "1" ]; then
  WORK="$work" OUT="$OUT" PACK_SIZE="$PACK_SIZE" TIMESCALE="$timescale" TIMELINE="$timeline" \\
  INIT_SRC="$work/init.mp4" SEG_COUNT="$seg_count" DURATION="$duration" \\
  python3 - <<'PYEOF'
import json, os, hashlib, glob, shutil
work, out = os.environ['WORK'], os.environ['OUT']
pack_size = int(os.environ['PACK_SIZE'])
timescale = float(os.environ['TIMESCALE'])
segs = sorted(glob.glob(os.path.join(work, 'seg*.m4s')))
# 展开 SegmentTimeline 的 (d, r)：r 是"再重复 r 次"
durations = []
for line in os.environ['TIMELINE'].splitlines():
    parts = line.split()
    if len(parts) == 2:
        d, r = float(parts[0]) / timescale, int(parts[1])
        durations.extend([d] * (r + 1))
if len(durations) != len(segs):
    durations = [float(os.environ['DURATION']) / max(len(segs), 1)] * len(segs)
shutil.copyfile(os.path.join(work, 'init.mp4'), os.path.join(out, 'init.mp4'))
segments, packs, pts, total = [], [], 0.0, 0
pack = None; pack_index = 0; pack_offset = 0; pack_bytes = 0
def close_pack():
    if pack is not None:
        packs.append({"file": f"pack-{pack_index:04d}.bin", "firstSegment": pack_first,
                      "count": pack_count, "bytes": pack_bytes})
for i, path in enumerate(segs, start=1):
    data = open(path, 'rb').read()
    if pack is None or (pack_size > 1 and pack_count >= pack_size):
        close_pack()
        pack_index += 1; pack_first = i; pack_count = 0; pack_offset = 0; pack_bytes = 0
        if pack_size <= 1:
            name = f"c{i:05d}.m4s"
            open(os.path.join(out, name), 'wb').write(data)
            packs.append({"file": name, "firstSegment": i, "count": 1, "bytes": len(data)})
            pack = None
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
         "mimeType": 'video/mp4; codecs="avc1.64001f,mp4a.40.2"',
         "totalDuration": round(duration, 6),
         "segmentSec": round(duration / max(len(segments), 1), 6),
         "bitrateBps": int(total * 8 / max(duration, 1)),
         "totalBytes": total, "segments": segments, "packs": packs}
with open(os.path.join(out, 'index.json'), 'w', encoding='utf-8') as f:
    json.dump(index, f, ensure_ascii=False)
print(f"完成：{len(segments)} 片 / {len(packs) + 2} 个文件 / {total} 字节")
PYEOF
else
  warn "没有 python3：请手动用 cmd/segmenter 或 Windows 版脚本生成 index.json（分片文件已在 \\$OUT）"
fi

say ""
say "产物目录： $OUT"
say "接下来：在主播页点「选择分片目录」选中它即可开播。"
`
}

/** 按平台生成。 */
export function buildSliceScript(p: ScriptParams): string {
  return p.platform === 'windows' ? buildPowerShellScript(p) : buildBashScript(p)
}
