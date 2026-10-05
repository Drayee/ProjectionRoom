# 交叉编译 cmd/segmenter，产出浏览器「一键切片脚本」可下载的二进制。
#
# 产物固定落在 client/dist/downloads/（该目录被 .gitignore 覆盖，不入库），
# 由 internal/handler/downloads.go 的只读清单端点 /api/downloads/segmenter 列出，
# 真正的下载由静态托管 /downloads/<file> 送出 —— 两者指向同一个目录。
#
# 用法: powershell -File scripts/build-segmenter.ps1 [-OutDir <目录>]
#   -OutDir 可选，默认 <仓库根>/client/dist/downloads；相对路径按仓库根解析。
#
# 注意：本文件必须保存为「带 UTF-8 BOM」的 UTF-8。
# 本机是 Windows PowerShell 5.1，没有 BOM 时它按 ANSI(GBK) 解码源文件，
# 中文注释会被解成乱码并让解析器报错（scripts/gen-proto.ps1 因此保持全 ASCII）。
#
# 交叉编译统一关掉 cgo（CGO_ENABLED=0）、去掉构建路径与符号表（-trimpath -ldflags "-s -w"）。

param(
    [string]$OutDir
)

$ErrorActionPreference = 'Stop'

# 仓库根 = 脚本所在目录的上一级（与 scripts/gen-proto.ps1 的算法一致）。
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if ([string]::IsNullOrWhiteSpace($OutDir)) {
    $OutDir = Join-Path $root 'client/dist/downloads'
}
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$OutDir = (Resolve-Path -LiteralPath $OutDir).Path

# 固定的 5 个发布目标，文件名与 internal/handler/downloads.go 里的平台表逐字一致。
$targets = @(
    [pscustomobject]@{ OS = 'windows'; Arch = 'amd64'; File = 'segmenter-windows-amd64.exe' },
    [pscustomobject]@{ OS = 'linux';   Arch = 'amd64'; File = 'segmenter-linux-amd64' },
    [pscustomobject]@{ OS = 'linux';   Arch = 'arm64'; File = 'segmenter-linux-arm64' },
    [pscustomobject]@{ OS = 'darwin';  Arch = 'amd64'; File = 'segmenter-darwin-amd64' },
    [pscustomobject]@{ OS = 'darwin';  Arch = 'arm64'; File = 'segmenter-darwin-arm64' }
)

# 备份调用者的环境变量：交叉编译之后原样恢复，不把 GOOS/GOARCH 泄漏到调用者的 shell。
$savedGoOS = $env:GOOS
$savedGoArch = $env:GOARCH
$savedCgoEnabled = $env:CGO_ENABLED

function Restore-EnvVar {
    param([string]$Name, [string]$Value)
    if ([string]::IsNullOrEmpty($Value)) {
        # 原本就没有这个变量 → 删掉，而不是留一个空值。
        Remove-Item -Path ('Env:' + $Name) -ErrorAction SilentlyContinue
    } else {
        Set-Item -Path ('Env:' + $Name) -Value $Value
    }
}

try {
    Write-Host "交叉编译 cmd/segmenter → $OutDir"

    foreach ($t in $targets) {
        $env:GOOS = $t.OS
        $env:GOARCH = $t.Arch
        $env:CGO_ENABLED = '0'

        $out = Join-Path $OutDir $t.File
        Write-Host ("  构建 {0}/{1} → {2}" -f $t.OS, $t.Arch, $t.File)

        go build -trimpath -ldflags '-s -w' -o $out ./cmd/segmenter
        if ($LASTEXITCODE -ne 0) {
            throw ("go build 失败: {0}/{1}（退出码 {2}）" -f $t.OS, $t.Arch, $LASTEXITCODE)
        }
        if (-not (Test-Path -LiteralPath $out -PathType Leaf)) {
            throw ("go build 退出码为 0，但没有产出文件: {0}" -f $out)
        }
    }
} finally {
    # 无论成功还是失败都恢复现场。
    Restore-EnvVar -Name 'GOOS' -Value $savedGoOS
    Restore-EnvVar -Name 'GOARCH' -Value $savedGoArch
    Restore-EnvVar -Name 'CGO_ENABLED' -Value $savedCgoEnabled
}

# 逐行打印「文件名  字节数  sha256（小写十六进制）」，便于人工核对。
Write-Host ''
Write-Host '产物清单（文件名  字节数  sha256）:'
foreach ($t in $targets) {
    $out = Join-Path $OutDir $t.File
    $item = Get-Item -LiteralPath $out
    $hash = (Get-FileHash -LiteralPath $out -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-Output ("{0}  {1}  {2}" -f $t.File, $item.Length, $hash)
}
