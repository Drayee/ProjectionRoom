# 用本机非 docker 的 PostgreSQL 18 启动月喵后端。
#
# 为什么不直接 `go run ./cmd`：服务端只认**进程环境变量**（仓库里没有 .env 加载器，
# go.mod 里没有 godotenv），所以 .env.local 必须由谁来读并塞进环境。这个脚本就是那个入口。
#
# 用法：
#   powershell -File scripts/run-local.ps1              # 默认 127.0.0.1:8888
#   powershell -File scripts/run-local.ps1 -Port 8090   # 换端口（会自动补 Origin 白名单）
#
# 前置：先跑一次 scripts/setup-local-db.ps1 建库建角色并生成 .env.local。
param(
    [int]$Port = 0,
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
        # 默认值已含 5173 与 8080，这里只补新端口（重复项无害）。
        $env:PR_ALLOWED_ORIGINS = "$needed,127.0.0.1:5173,localhost:5173"
    }
    Write-Host "[run-local] 端口覆盖为 $Port，已把该来源加入 PR_ALLOWED_ORIGINS" -ForegroundColor Yellow
}

Write-Host "[run-local] 监听   : $($env:PR_ADDR)"
Write-Host "[run-local] 数据库 : $(([regex]::Match($env:PR_DB_DSN, 'host=\S+ port=\S+ user=\S+').Value)) dbname=$(([regex]::Match($env:PR_DB_DSN, 'dbname=\S+').Value -replace 'dbname=',''))"
Write-Host "[run-local] 账号能力: $(if ($env:PR_DB_DSN) { '已启用' } else { '关闭（游客模式）' })"
Write-Host "[run-local] 静态产物: client/dist（若缺请先 npm --prefix client run build）"
Write-Host ''

if (-not $SkipBuild) {
    Write-Host '[run-local] 编译中…'
    & go build -o (Join-Path $root 'bin/pr-local.exe') ./cmd
    if ($LASTEXITCODE -ne 0) { Write-Error '编译失败' }
}

Write-Host '[run-local] 启动（Ctrl+C 停止）'
& (Join-Path $root 'bin/pr-local.exe')