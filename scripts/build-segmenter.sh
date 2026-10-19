#!/bin/bash
# 交叉编译 cmd/segmenter，产出浏览器「一键切片脚本」可下载的二进制。
#
# 这是 scripts/build-segmenter.ps1 的 **bash 等效版本**，供 Linux CI 使用。
# 两者必须产出同样的文件名与同样的平台列表 —— 清单端点
# （internal/handler/downloads.go 的 segmenterDownloadTargets）按固定顺序
# 列出这些文件，少一个就会让对应平台的用户下载到 404。
#
# 为什么 CI 里要构建而不是提交产物：
#   39 MB 的二进制进 git 会让每次 clone 都变慢，且与源码版本容易不同步
#   （改了 cmd/segmenter 却忘了重新构建 —— 用户下载到的是旧版）。
#   在 CI 里现构，"镜像里的二进制"与"这次提交的源码"必然一致。
#
# 用法: ./scripts/build-segmenter.sh [OutDir]
#   默认输出到 <仓库根>/client/dist/downloads

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-$ROOT/client/dist/downloads}"

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

echo "构建切片器 -> $OUT_DIR"

# 与 build-segmenter.ps1 完全一致的五个目标与命名
build() {
  local goos="$1" goarch="$2" out="$3"
  echo "  $goos/$goarch -> $out"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w" -o "$OUT_DIR/$out" ./cmd/segmenter
}

build linux   amd64 segmenter-linux-amd64
build linux   arm64 segmenter-linux-arm64
build darwin  amd64 segmenter-darwin-amd64
build darwin  arm64 segmenter-darwin-arm64
build windows amd64 segmenter-windows-amd64.exe

echo "完成："
ls -lh "$OUT_DIR"/segmenter-*
