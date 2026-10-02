# Generate Go and TypeScript code from proto/projection_room.proto.
#
# Generated code is committed: builds and deploys never need protoc -- only run
# this script after editing the .proto file.
#
# Requirements:
#   - protoc (installed locally)
#   - protoc-gen-go (in GOPATH/bin)
#   - client/node_modules/.bin/protoc-gen-es (installed with @bufbuild/protoc-gen-es)
#
# Usage: pwsh scripts/gen-proto.ps1
#
# NOTE: keep this file ASCII-only. Chinese characters in .ps1 comments have broken
# the PowerShell parser on this host before.
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$protoFile = Join-Path $root 'proto/projection_room.proto'
# go_out must be the module root: with '--go_opt=module=ProjectionRoom' protoc-gen-go
# emits paths like 'internal/pb/x.pb.go', already relative to the module.
$goOut = $root
$tsOut = Join-Path $root 'client/src/gen'

Write-Host 'gen go -> internal/pb'
New-Item -ItemType Directory -Force -Path (Join-Path $goOut 'pb') | Out-Null
protoc "--proto_path=$root/proto" "--go_out=$goOut" '--go_opt=module=ProjectionRoom' $protoFile
if ($LASTEXITCODE -ne 0) { throw "protoc (go) failed: $LASTEXITCODE" }

Write-Host 'gen ts -> client/src/gen'
New-Item -ItemType Directory -Force -Path $tsOut | Out-Null
$esPlugin = Join-Path $root 'client/node_modules/.bin/protoc-gen-es.cmd'
if (-not (Test-Path $esPlugin)) { throw 'protoc-gen-es not found: run npm install in client/ first' }
protoc "--proto_path=$root/proto" "--plugin=protoc-gen-es=$esPlugin" "--es_out=$tsOut" '--es_opt=target=ts,import_extension=none' $protoFile
if ($LASTEXITCODE -ne 0) { throw "protoc (es) failed: $LASTEXITCODE" }

Write-Host 'done. generated files:'
Get-ChildItem -Recurse -File (Join-Path $goOut 'internal/pb'), $tsOut | Select-Object -ExpandProperty FullName
