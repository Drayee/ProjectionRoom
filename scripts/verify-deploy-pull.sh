#!/bin/bash
# 验证 deploy.yml 里"后台拉取 + 轮询"那段逻辑在真实 shell 下可用
# （重点验证：完成标记文件里确实是退出码，而不是空串）
set -euo pipefail

IMAGE_REF="$1"
PULL_LOG=/tmp/verify-pull.log
PULL_DONE=/tmp/verify-pull.done
rm -f "$PULL_LOG" "$PULL_DONE"

{
  printf '%s\n' '#!/bin/bash'
  printf '%s\n' '# $1=镜像引用 $2=日志路径 $3=完成标记路径'
  printf '%s\n' 'docker pull "$1" > "$2" 2>&1'
  printf '%s\n' 'rc=$?'
  printf '%s\n' 'printf "%s" "$rc" > "$3"'
} > /tmp/verify-pull.sh
chmod +x /tmp/verify-pull.sh

echo "--- 生成的脚本内容 ---"
cat /tmp/verify-pull.sh
echo "--- 第一行是否为行首 shebang ---"
head -c 12 /tmp/verify-pull.sh | od -c | head -1

nohup /tmp/verify-pull.sh "$IMAGE_REF" "$PULL_LOG" "$PULL_DONE" > /dev/null 2>&1 &

for i in $(seq 1 60); do
  if [ -f "$PULL_DONE" ]; then
    RC=$(cat "$PULL_DONE")
    echo "--- 完成：rc='$RC'（应为 0）---"
    tail -2 "$PULL_LOG"
    exit 0
  fi
  sleep 5
done
echo "--- 超时 ---"
tail -5 "$PULL_LOG"
exit 1
