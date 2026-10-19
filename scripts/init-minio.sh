#!/bin/bash
# 初始化 MinIO：建桶 + 设置匿名只读（图片需要能被 <img src> 直接加载）
#
# 为什么匿名只读是安全的：桶里只有用户上传的图片（发帖配图、番剧封面），
# 这些内容本来就要公开展示。写入仍然需要凭据（只有应用服务端持有）。
set -e

source /opt/minio/.env

echo "=== 建桶 + 匿名只读策略 ==="
docker run --rm --network host \
  -e MC_USER="$MINIO_ROOT_USER" \
  -e MC_PASS="$MINIO_ROOT_PASSWORD" \
  --entrypoint /bin/sh \
  minio/minio:latest -c '
    mc alias set pr http://127.0.0.1:9000 "$MC_USER" "$MC_PASS" >/dev/null
    mc mb --ignore-existing pr/pr-files
    mc anonymous set download pr/pr-files
    echo "--- 验证 ---"
    mc anonymous get pr/pr-files
    mc ls pr/
  '
