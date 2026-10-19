#!/bin/bash
# 在**服务器本地**把切片器二进制导入 MinIO 并生成清单。
#
# 为什么这个脚本在服务器上跑而不是在 CI runner 上：
#   MinIO 只监听 127.0.0.1（安全考虑，不对公网暴露），CI runner 连不上它。
#   所以 CI 只负责把二进制 SCP 过来，由这个脚本在服务器本地完成导入。
#
# 前提：/tmp/pr-segmenter/ 里已有 segmenter-* 文件（CI 传过来的）。
# 依赖：docker（用 minio/minio 镜像自带的 mc），python3。
#
# 幂等：同名文件覆盖，清单每次重新生成。

set -euo pipefail

SRC_DIR=/tmp/pr-segmenter
BUCKET=pr-files
MINIO_ENV=/opt/minio/.env

if [ ! -d "$SRC_DIR" ]; then
  echo "ERROR: $SRC_DIR 不存在（CI 没传二进制？）" >&2
  exit 1
fi
if ! ls "$SRC_DIR"/segmenter-* >/dev/null 2>&1; then
  echo "ERROR: $SRC_DIR 里没有 segmenter-* 文件" >&2
  exit 1
fi
if [ ! -f "$MINIO_ENV" ]; then
  echo "ERROR: $MINIO_ENV 不存在（MinIO 凭据缺失）" >&2
  exit 1
fi

# shellcheck disable=SC1090
set -a; . "$MINIO_ENV"; set +a

echo "=== 待发布文件 ==="
ls -lh "$SRC_DIR"/segmenter-*

echo "=== 生成清单 ==="
# 清单形状必须与 internal/storage/segmenter.go 的 SegmenterManifest 一致：
#   {"version":"1","platforms":[{"os","arch","file","bytes","sha256"}]}
# sha256 与大小在**本地**算（就是即将上传的那份文件），保证与下载内容一致。
python3 - "$SRC_DIR" << 'PYEOF'
import hashlib, json, os, sys

src = sys.argv[1]

# 文件名 -> (os, arch)，与 internal/handler/downloads.go 的
# segmenterDownloadTargets 逐条对齐。顺序也照抄（windows 打头）。
ORDER = [
    ("segmenter-windows-amd64.exe", "windows", "amd64"),
    ("segmenter-linux-amd64",       "linux",   "amd64"),
    ("segmenter-linux-arm64",       "linux",   "arm64"),
    ("segmenter-darwin-amd64",      "darwin",  "amd64"),
    ("segmenter-darwin-arm64",      "darwin",  "arm64"),
]

platforms = []
for fname, os_name, arch in ORDER:
    path = os.path.join(src, fname)
    if not os.path.isfile(path):
        # 缺文件不致命：清单里少一个平台，其它照常发布。
        # 这与 handler 侧"文件缺失只是该平台不可用"的语义一致。
        print(f"  WARN: 缺少 {fname}，跳过", file=sys.stderr)
        continue
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    platforms.append({
        "os": os_name,
        "arch": arch,
        "file": fname,
        "bytes": os.path.getsize(path),
        "sha256": h.hexdigest(),
    })
    print(f"  {fname}: {os.path.getsize(path)} bytes, sha256={h.hexdigest()[:16]}…")

out = os.path.join(src, "manifest.json")
with open(out, "w", encoding="utf-8") as fh:
    json.dump({"version": "1", "platforms": platforms}, fh, indent=2)
print(f"  清单已写入 {out}（{len(platforms)} 个平台）")
PYEOF

echo "=== 导入 MinIO ==="
# 用 minio 镜像自带的 mc（宿主机没装），--network host 才能连 127.0.0.1:9000
docker run --rm --network host \
  -v "$SRC_DIR":/src:ro \
  -e U="$MINIO_ROOT_USER" \
  -e P="$MINIO_ROOT_PASSWORD" \
  -e B="$BUCKET" \
  --entrypoint /bin/sh \
  minio/minio:latest -c '
    set -e
    mc alias set prio http://127.0.0.1:9000 "$U" "$P" >/dev/null
    mc mb --ignore-existing "prio/$B" >/dev/null 2>&1 || true

    # 先传二进制，再传清单 —— 顺序重要：清单里的 sha256 必须对应已经存在的文件，
    # 反过来会出现"清单已更新但文件还是旧的"的短暂窗口，客户端校验必然失败。
    for f in /src/segmenter-*; do
      mc cp --quiet "$f" "prio/$B/segmenter/$(basename "$f")"
      echo "  已上传 $(basename "$f")"
    done
    mc cp --quiet /src/manifest.json "prio/$B/segmenter/manifest.json"
    echo "  已上传 manifest.json"

    echo "--- 桶内现状 ---"
    mc ls "prio/$B/segmenter/"
  '

echo "=== 校验公开访问 ==="
# 从清单里取第一个文件做匿名读（验证桶策略）
FIRST=$(python3 -c "
import json
m = json.load(open('$SRC_DIR/manifest.json'))
print(m['platforms'][0]['file'] if m['platforms'] else '')
")
if [ -n "$FIRST" ]; then
  CODE=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1/files/$BUCKET/segmenter/$FIRST" || echo 000)
  echo "  匿名下载 $FIRST: HTTP $CODE"
  if [ "$CODE" != "200" ]; then
    echo "  WARN: 匿名下载失败，检查桶策略（mc anonymous set download）"
  fi
fi

echo "=== 完成 ==="
