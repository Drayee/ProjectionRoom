#!/bin/bash
# 给 nginx 加一条 /files/ 反代到 MinIO，使图片能经 80 端口访问
#
# 为什么经 nginx 而不是直连 9000：
#  - 9000 只监听回环（安全），公网够不着；
#  - 复用已有的 80 入口，浏览器不需要额外端口，也不会撞上跨域；
#  - 可以在这里加缓存头，让图片被浏览器/中间层缓存。
set -e

CONF=/etc/nginx/sites-available/projectionroom

# 幂等：已加过就跳过
if grep -q 'location /files/' "$CONF" 2>/dev/null; then
  echo "already configured"
  exit 0
fi

# 在最后一个 } 前插入 location /files/
python3 - << 'PYEOF'
import re
path = '/etc/nginx/sites-available/projectionroom'
s = open(path, encoding='utf-8').read()

block = '''
    # —— 对象存储（MinIO）图片 ——
    # 浏览器拿到的 URL 形如 http://<host>/files/pr-files/2026/10/<rand>.jpg
    # 这里剥掉 /files 前缀后转发给 MinIO 的 S3 API（端口 9000）。
    #
    # 为什么要 proxy_hide_header + add_header：
    # MinIO 会带自己的 Server / x-amz-* 头，暴露内部实现细节；
    # 而 Cache-Control 由应用侧写入（immutable），这里不覆盖。
    location /files/ {
        proxy_pass http://127.0.0.1:9000/;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;

        proxy_hide_header x-amz-request-id;
        proxy_hide_header x-amz-id-2;
        proxy_hide_header x-minio-deployment-id;
        proxy_hide_header x-minio-origin-endpoint;

        # 图片是不可变的（key 含随机串），可以长缓存
        proxy_cache_valid 200 7d;
        client_max_body_size 12m;
        proxy_read_timeout 60s;
    }
'''

# 插到最后一个 '}' 之前（即 server 块的收尾）
idx = s.rstrip().rfind('}')
s = s[:idx] + block + s[idx:]
open(path, 'w', encoding='utf-8').write(s)
print('inserted')
PYEOF

nginx -t && systemctl reload nginx && echo "nginx reloaded"
