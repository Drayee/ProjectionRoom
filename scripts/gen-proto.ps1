# 从 proto/projection_room.proto 生成 Go 与 TypeScript 代码。
#
# 生成的代码**入库**：构建与部署都不需要 protoc —— 只有改了 .proto 才需要跑这个脚本。
#
# 前置：
#   - protoc（本机已装）
#   - protoc-gen-go（在 GOPATH/bin）
#   - client/node_modules/.bin/protoc-gen-es（npm install 时随 @bufbuild/protoc-gen-es 安装）
#
# 用法：pwsh scripts/gen-proto.ps1
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$protoFile = Join-Path $root 'proto/projection_room.proto'
$goOut = Join-Path $root 'internal'
$tsOut = Join-Path $root 'client/src/gen'

Write-Host '生成 Go 代码 -> internal/pb'
New-Item -ItemType Directory -Force -Path (Join-Path $goOut 'pb') | Out-Null
protoc "--proto_path=$root/proto" "--go_out=$goOut" '--go_opt=module=ProjectionRoom' $protoFile
if ($LASTEXITCODE -ne 0) { throw "protoc (go) 失败: $LASTEXITCODE" }

Write-Host '生成 TypeScript 代码 -> client/src/gen'
New-Item -ItemType Directory -Force -Path $tsOut | Out-Null
$esPlugin = Join-Path $root 'client/node_modules/.bin/protoc-gen-es.cmd'
if (-not (Test-Path $esPlugin)) { throw '找不到 protoc-gen-es：请先在 client/ 执行 npm install' }
protoc "--proto_path=$root/proto" "--plugin=protoc-gen-es=$esPlugin" "--es_out=$tsOut" '--es_opt=target=ts,import_extension=none' $protoFile
if ($LASTEXITCODE -ne 0) { throw "protoc (es) 失败: $LASTEXITCODE" }

Write-Host '完成。生成的文件：'
Get-ChildItem -Recurse -File (Join-Path $goOut 'pb'), $tsOut | Select-Object -ExpandProperty FullName
