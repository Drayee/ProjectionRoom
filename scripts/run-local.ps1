# 用本机非 docker 的 PostgreSQL 18 启动月喵后端。
#
# 为什么不直接 `go run ./cmd`：服务端只认**进程环境变量**（仓库里没有 .env 加载器，
# go.mod 里没有 godotenv），所以 .env.local 必须由谁来读并塞进环境。这个脚本就是那个入口。
#
# 用法：
#   powershell -File scripts/run-local.ps1              # 默认 127.0.0.1:8888
#   powershell -File scripts/run-local.ps1 -Port 8090   # 换端口（会自动补 Origin 白名单）
#   powershell -File scripts/run-local.ps1 -StaticDir client/dist-real -SkipBuild
#                                                       # 服务另一份产物（例如 VITE_MOCK_POSTS=0 的 real bundle）
#
# 前置：先跑一次 scripts/setup-local-db.ps1 建库建角色并生成 .env.local。
param(
    [int]$Port = 0,
    [string]$StaticDir = '',
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root '.env.local'

if (-not (Test-Path $envFile)) {
    Write-Error "找不到 $envFile —— 请先执行: powershell -File scripts/setup-local-db.ps1"
}

# 读 .env.local：忽略注释与空行，KEY=VALUE 逐条塞进当前进程环境。
foreach ($line in Get-Content $envFile) {
    $t = $line.Trim()
    if ($t -eq '' -or $t.StartsWith('#')) { continue }
    $i = $t.IndexOf('=')
    if ($i -lt 1) { continue }
    $k = $t.Substring(0, $i).Trim()
    $v = $t.Substring($i + 1).Trim()
    Set-Item -Path "Env:$k" -Value $v
}

# 端口覆盖：换端口时必须同时补 Origin 白名单，否则 /ws 一律 403
# （S-5 之后白名单是唯一判据，同源不隐式放行）。
if ($Port -gt 0) {
    $env:PR_ADDR = "127.0.0.1:$Port"
    $needed = "127.0.0.1:$Port,localhost:$Port"
    if ($env:PR_ALLOWED_ORIGINS) {
        $env:PR_ALLOWED_ORIGINS = "$needed,$($env:PR_ALLOWED_ORIGINS)"
    } else {
        # 默认值已含 5173 与 8888，这里只补新端口（重复项无害）。
        $env:PR_ALLOWED_ORIGINS = "$needed,127.0.0.1:5173,localhost:5173"
    }
    Write-Host "[run-local] 端口覆盖为 $Port，已把该来源加入 PR_ALLOWED_ORIGINS" -ForegroundColor Yellow
}

Write-Host "[run-local] 监听   : $($env:PR_ADDR)"
Write-Host "[run-local] 数据库 : $(([regex]::Match($env:PR_DB_DSN, 'host=\S+ port=\S+ user=\S+').Value)) dbname=$(([regex]::Match($env:PR_DB_DSN, 'dbname=\S+').Value -replace 'dbname=',''))"
Write-Host "[run-local] 账号能力: $(if ($env:PR_DB_DSN) { '已启用' } else { '未配置（启动会失败）' })"
Write-Host "[run-local] 静态产物: $staticDir"

# 启动完整性提示：缺任何一项必需配置都会在启动期被拒绝（不是运行期 503）。
# 这里先做一次预检，把"等它编译完再报错"变成"现在就说清缺什么"。
$missing = @()
if (-not $env:PR_DB_DSN) { $missing += 'PR_DB_DSN' }
if (-not $env:PR_JWT_SECRET -or $env:PR_JWT_SECRET.Length -lt 32) { $missing += 'PR_JWT_SECRET(>=32字节)' }
if (-not $env:PR_PUBLIC_BASE_URL) { $missing += 'PR_PUBLIC_BASE_URL' }
$smtpReady = $env:PR_SMTP_HOST -and $env:PR_SMTP_USER -and $env:PR_SMTP_PASS
$devEcho = $env:PR_OTP_DEV_ECHO -in @('1', 'true', 'True')
if (-not $smtpReady -and -not $devEcho) { $missing += 'SMTP 三件套 或 PR_OTP_DEV_ECHO=1' }

# 静态产物：-StaticDir 可指定另一份（默认 client/dist）。
# 预检只校验**指定的那一份**——切到 dist-real 做验收时不该被 dist 缺不缺失影响。
$staticDir = if ($StaticDir) { $StaticDir } else { 'client/dist' }
if (-not (Test-Path (Join-Path $root "$staticDir/index.html"))) { $missing += "$staticDir（先构建那一份产物）" }

if ($missing.Count -gt 0) {
    Write-Host ''
    Write-Host '[run-local] 必需配置不完整，服务会拒绝启动。缺项：' -ForegroundColor Red
    $missing | ForEach-Object { Write-Host "  - $_" -ForegroundColor Red }
    Write-Host '[run-local] 把这些补进 .env.local 后重试。' -ForegroundColor Yellow
    Write-Host ''
}

Write-Host ''

if (-not $SkipBuild) {
    Write-Host '[run-local] 编译中…'
    & go build -o (Join-Path $root 'bin/pr-local.exe') ./cmd
    if ($LASTEXITCODE -ne 0) { Write-Error '编译失败' }
}

if ($StaticDir) { $env:PR_STATIC_DIR = $StaticDir }

Write-Host '[run-local] 启动（Ctrl+C 停止）'
& (Join-Path $root 'bin/pr-local.exe')