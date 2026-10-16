# 在本机**非 docker** 的 PostgreSQL 上建月喵所需的角色与两个库，并生成 .env.local。
#
# 形态对齐 CLAUDE.md §8 的部署事实：
#   - 角色 pr_app（最小权限：不是 superuser、不能建库）
#   - 库 projectionroom（生产）+ projectionroom_test（测试）
#   - .env.local（含凭据，已被 .gitignore 的 .env* 规则排除）
#
# 用法：
#   powershell -File scripts/setup-local-db.ps1
#   powershell -File scripts/setup-local-db.ps1 -SuperPassword 'xxx' -Force
#
# 需要 superuser 口令（建库建角色必需）。取值顺序：
#   1) -SuperPassword 参数    2) $env:PGPASSWORD    3) 交互式安全输入
#
# 幂等：角色与库已存在时只更新口令 / 跳过建库，可重复执行。
param(
    [string]$SuperUser = 'postgres',
    [int]$Port = 5432,
    [string]$HostName = '127.0.0.1',
    [string]$SuperPassword = '',
    [switch]$Force
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root '.env.local'

# 找 psql：优先 PATH，其次 PostgreSQL 常见安装目录（按版本从新到旧）。
function Find-Psql {
    $c = Get-Command psql -ErrorAction SilentlyContinue
    if ($c) { return $c.Source }
    $dirs = Get-ChildItem 'C:\Program Files\PostgreSQL' -Directory -ErrorAction SilentlyContinue |
            Sort-Object Name -Descending
    foreach ($d in $dirs) {
        $p = Join-Path $d.FullName 'bin\psql.exe'
        if (Test-Path $p) { return $p }
    }
    throw '找不到 psql。请安装 PostgreSQL 客户端，或把它的 bin 目录加进 PATH。'
}

$psql = Find-Psql
Write-Host "[setup] psql: $psql"

if (-not $SuperPassword) { $SuperPassword = $env:PGPASSWORD }
if (-not $SuperPassword) {
    $sec = Read-Host -Prompt "[setup] 请输入 PostgreSQL superuser（$SuperUser）口令" -AsSecureString
    $SuperPassword = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
        [Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
}
if (-not $SuperPassword) { throw 'superuser 口令为空，无法建库。' }

# 跑一条 SQL 并返回输出。$TuplesOnly 用于取值（-tAc），否则用 -c。
function Invoke-Sql {
    param(
        [string]$Db, [string]$Sql,
        [string]$AsUser = $SuperUser, [string]$Pw = $SuperPassword,
        [switch]$TuplesOnly, [switch]$Quiet
    )
    $a = @('-h', $HostName, '-p', $Port, '-U', $AsUser, '-d', $Db, '-v', 'ON_ERROR_STOP=1')
    $a += if ($TuplesOnly) { '-tAc' } else { '-c' }
    $a += $Sql
    $old = $env:PGPASSWORD
    $env:PGPASSWORD = $Pw
    try {
        $out = & $psql @a 2>&1
        if ($LASTEXITCODE -ne 0) { throw "SQL 失败: $Sql`n$out" }
        if (-not $Quiet) { return ($out -join "`n").Trim() }
    } finally { $env:PGPASSWORD = $old }
}

# 1) 连通性
Write-Host '[setup] 检查连接…'
Write-Host "[setup] $(Invoke-Sql -Db postgres -Sql 'select version()' -TuplesOnly)"

# 生成随机串的辅助函数。
#
# ⚠ **不要用 `-join ((...chars) | Get-Random -Count N)`**：`Get-Random -Count` 是
# **无放回抽样**，池子只有 62 个字符（0-9A-Za-z）时 `-Count 64` 最多返回 62 个，
# 而且会去重 —— 实测要 64 位只拿到 16 位，于是 PR_JWT_SECRET 不足 32 字节、
# 服务端**拒绝启动**（这个脚本的第一版就踩了）。
# 正确做法：每次独立取一个字符（有放回）。
function New-RandomString {
    param(
        [int]$Length,
        [string]$Alphabet = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz'
    )
    $sb = [Text.StringBuilder]::new()
    for ($i = 0; $i -lt $Length; $i++) {
        [void]$sb.Append($Alphabet[(Get-Random -Minimum 0 -Maximum $Alphabet.Length)])
    }
    return $sb.ToString()
}

# 2) 生成 pr_app 口令。
#    **只含 [0-9A-Za-z]** —— 这样它可以安全地直接嵌进 SQL 单引号字面量，
#    也能安全地放进 DSN（不需要 URL 编码，也不会被 psql 当成元字符）。
$prPw = New-RandomString -Length 32

# 3) 角色：存在则改口令，不存在则建。
#
# 这里刻意**不用** DO $$ ... $$ 块：psql 的变量插值（:'pr_pw'）在 dollar-quoted
# 字符串里被禁用，会原样送进服务端并报语法错。也刻意不用 \gexec —— 它要求
# 每条语句自成一行，而多行语句会被切坏（这个脚本的第一版就踩了这个坑）。
# 改为：先在 PowerShell 里判存在性，再发一条**单行** DDL。口令是自生成的字母数字，
# 直接内插是安全的。
$roleExists = Invoke-Sql -Db postgres -TuplesOnly `
    -Sql "select 1 from pg_roles where rolname = 'pr_app'"
if ($roleExists) {
    Invoke-Sql -Db postgres -Sql "alter role pr_app login password '$prPw'" -Quiet
    Write-Host '[setup] 角色 pr_app 已存在 → 已更新口令'
} else {
    Invoke-Sql -Db postgres -Sql "create role pr_app login password '$prPw'" -Quiet
    Write-Host '[setup] 已创建角色 pr_app（LOGIN，无 superuser / 无 createdb）'
}

# 4) 两个库（CREATE DATABASE 不能放在事务里，所以逐条判存在性再建）
foreach ($db in 'projectionroom', 'projectionroom_test') {
    $exists = Invoke-Sql -Db postgres -TuplesOnly `
        -Sql "select 1 from pg_database where datname = '$db'"
    if ($exists) {
        Write-Host "[setup] 库 $db 已存在 → 跳过"
    } else {
        Invoke-Sql -Db postgres -Sql "create database $db owner pr_app" -Quiet
        Write-Host "[setup] 已创建库 $db（owner=pr_app）"
    }
}

# 5) schema 归属：pr_app 要能建表跑迁移
foreach ($db in 'projectionroom', 'projectionroom_test') {
    Invoke-Sql -Db $db -Quiet -Sql (
        'alter schema public owner to pr_app;' +
        'revoke all on schema public from public;' +
        'grant all on schema public to pr_app;')
}
Write-Host '[setup] schema 归属已确认（pr_app 为 owner）'

# 6) 自检：pr_app 能连、能建表（迁移的前提）
$check = Invoke-Sql -Db 'projectionroom_test' -AsUser 'pr_app' -Pw $prPw -TuplesOnly `
    -Sql 'create table _probe(x int); drop table _probe; select current_user'
Write-Host "[setup] pr_app 自检通过: $check"

# 7) 写 .env.local（纯 ASCII、LF；已存在的先备份，除非 -Force）
if ((Test-Path $envFile) -and -not $Force) {
    $bak = "$envFile.bak-$(Get-Date -Format 'yyyyMMdd-HHmmss')"
    Copy-Item $envFile $bak
    Write-Host "[setup] 已备份原 .env.local -> $bak（-Force 可跳过备份）" -ForegroundColor Yellow
}
# JWT 密钥：64 位十六进制 = 32 字节，正好是服务端要求的下限（<32 字节 → 拒绝启动）。
# 同样必须用有放回的生成器：十六进制池只有 16 个字符，Get-Random -Count 64 会去重到 16 位。
$jwt = New-RandomString -Length 64 -Alphabet '0123456789abcdef'
$lines = @(
    "# Local dev instance env (non-docker PostgreSQL).",
    "# Generated by scripts/setup-local-db.ps1. NOT in version control (.gitignore: .env*).",
    "# Use: powershell -File scripts/run-local.ps1",
    "",
    "PR_ADDR=127.0.0.1:8080",
    "PR_DB_DSN=host=$HostName port=$Port user=pr_app password=$prPw dbname=projectionroom sslmode=disable TimeZone=Asia/Shanghai",
    "PR_JWT_SECRET=$jwt",
    "PR_SMTP_HOST="
)
[IO.File]::WriteAllText($envFile, ($lines -join "`n") + "`n", (New-Object Text.UTF8Encoding($false)))

Write-Host ''
Write-Host '[setup] 完成。下一步：' -ForegroundColor Green
Write-Host '  npm --prefix client run build           # 首次需要（单端口部署要 client/dist）'
Write-Host '  powershell -File scripts/run-local.ps1  # 起服务（默认 127.0.0.1:8080）'
Write-Host ''
Write-Host '  跑真库用例（internal/store 会 TRUNCATE 测试库）：'
Write-Host "    `$env:PR_TEST_DB_DSN='host=$HostName port=$Port user=pr_app password=<见 .env.local> dbname=projectionroom_test sslmode=disable'"
Write-Host '    go test ./internal/store/ -count=1'
