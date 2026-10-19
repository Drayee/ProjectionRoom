# 对象存储（MinIO）：图片等静态附件的落点

> 2026-10-19 起，服务器上跑着一个 MinIO 实例，用于托管用户上传的图片。
> 本文记录部署形态、配置、运维与排障。

## 1. 为什么需要它

改造前，全系统**没有任何文件写入路径**：

| 内容 | 改造前 | 改造后 |
| :--- | :--- | :--- |
| 帖子配图 | 只能贴 http(s) 外链（图床挂则图丢） | `POST /api/uploads` 上传到 MinIO，返回稳定 URL |
| 番剧封面 | 同上 | 同上 |
| 用户头像 | 前端按名字哈希生成，不存文件 | 不变（仍不存文件） |
| 直播视频 | 不进服务器（peer 间 DataChannel） | **不变**（不变量 I1） |

⚠ **不要**把视频分片塞进 MinIO。I1 说的是直播链路上服务器不经手视频字节；
`internal/service/segment` 的一次性切片是例外（开播前的预处理），但它有 TTL
且产物由用户下载后即删，与"长期对象存储"是两回事。

## 2. 部署形态

```
浏览器
  │  GET  http://60.205.212.25/files/pr-files/2026/10/<rand>.png
  ▼
nginx :80
  │  location /files/ → proxy_pass http://127.0.0.1:9000/
  ▼
MinIO 容器 (pr-minio, network_mode: host, 127.0.0.1:9000)
  │  桶 pr-files，匿名只读
  ▼
/opt/minio/data  （宿主机磁盘，容器外持久化）

应用容器 (projectionroom-app)
  │  POST /api/uploads → S3 API 写入 127.0.0.1:9000
  └─ 凭据来自 /opt/projectionroom/.env
```

### 关键设计

**为什么 MinIO 也只监听回环（127.0.0.1:9000）**
对象存储不该直接对公网开。所有读取经 nginx 的 `/files/` 反代，
这样浏览器不需要额外端口、不会撞上跨域，也便于在 nginx 层加缓存与限速。

**为什么匿名只读**
`<img src>` 无法携带凭据。桶里只有用户上传的图片，本来就要公开展示。
策略只给 `s3:GetObject`，**不给 ListBucket** —— 列目录没有业务需要，
给了只会方便别人枚举全部文件。

**为什么 key 是随机串而不是原文件名**
原文件名可能含 `..`（目录穿越）、路径分隔符、中文，且会泄露用户设备命名习惯。
key 形如 `2026/10/<16字节hex>.png`：按年月分目录便于定位，随机部分保证不可猜。

## 3. 配置

### 应用侧（`/opt/projectionroom/.env`）

```bash
PR_STORAGE_ENDPOINT=127.0.0.1:9000          # 不带 scheme
PR_STORAGE_ACCESS_KEY=<MinIO root user>
PR_STORAGE_SECRET_KEY=<MinIO root password>
PR_STORAGE_BUCKET=pr-files
PR_STORAGE_USE_SSL=0
PR_STORAGE_PUBLIC_BASE_URL=http://60.205.212.25/files
```

`PR_STORAGE_ENDPOINT` 为空 = 对象存储未启用：`/api/uploads` **不注册**（404），
帖子/番剧仍只接受 http(s) 外链。这与账号层"`PR_DB_DSN` 为空即不注册 `/api/auth/*`"
是同一条约定。

⚠ `PR_STORAGE_PUBLIC_BASE_URL` 与 `PR_STORAGE_ENDPOINT` 是**两件事**：
前者是浏览器看到的地址（经 nginx），后者是服务端连的地址（回环）。
混用会得到"上传成功但图片显示不出来"——这是最难查的一类故障。

### MinIO 侧（`/opt/minio/.env`，root:600）

```bash
MINIO_ROOT_USER=prminio<随机>
MINIO_ROOT_PASSWORD=<32位随机hex>
```

这两个值同时被 `docker-compose.yml` 的 minio 服务消费，以及应用侧的
`PR_STORAGE_ACCESS_KEY/_SECRET_KEY` 引用。轮换时两处都要改。

## 4. 运维命令

```bash
# 启动/重启整个栈（凭据从 /opt/minio/.env 读）
cd /opt/projectionroom
set -a; source /opt/minio/.env; set +a
export IMAGE_REF=ghcr.io/drayee/projectionroom:latest
docker compose up -d

# 状态
docker compose ps
docker stats --no-stream pr-minio

# 建桶 + 设匿名只读（幂等，重复执行安全）
bash /opt/minio/init.sh

# 列桶内容（用镜像自带的 mc）
docker run --rm --network host \
  -e U="$MINIO_ROOT_USER" -e P="$MINIO_ROOT_PASSWORD" \
  --entrypoint /bin/sh minio/minio:latest -c \
  'mc alias set pr http://127.0.0.1:9000 "$U" "$P" >/dev/null && mc ls -r pr/pr-files | head'

# 磁盘占用
du -sh /opt/minio/data
```

## 5. 验收

```bash
# 1. 上传（需要登录 token）
TOKEN=$(curl -s -X POST http://60.205.212.25/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"sam","password":"1234"}' | jq -r .accessToken)

curl -s -X POST http://60.205.212.25/api/uploads \
  -H "Authorization: Bearer $TOKEN" \
  -F 'file=@test.png;type=image/png'
# → {"url":"http://60.205.212.25/files/pr-files/2026/10/<rand>.png"}

# 2. 公网可读（这是 <img src> 走的路径）
curl -s -o /dev/null -w '%{http_code} %{content_type}\n' <上面返回的 URL>
# → 200 image/png

# 3. 未登录上传必须 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://60.205.212.25/api/uploads \
  -F 'file=@test.png;type=image/png'
# → 401

# 4. 非图片类型必须 415
curl -s -X POST http://60.205.212.25/api/uploads \
  -H "Authorization: Bearer $TOKEN" \
  -F 'file=@evil.svg;type=image/svg+xml'
# → 415（SVG 刻意不支持：它能内嵌 <script>，同源托管等于开 XSS 入口）
```

## 6. 已知限制

1. **只支持位图**：JPEG / PNG / GIF / WebP。**刻意不支持 SVG**（见上）。
   类型判断依据是客户端声明的 `Content-Type`（白名单初筛），没有做内容嗅探。
   真正的保障是"只存位图 + 匿名只读 + 不走同源执行"这三条。
2. **单文件上限 10 MiB**。这是配图不是原图存档，10 MiB 已能容纳手机直出的
   4000×3000 JPEG。
3. **没有清理策略**。删帖不会删图（`Remove` 方法已备好，但未接在删除路径上）。
   长期会累积孤儿对象，需要时按 `2026/10/` 前缀批量清理。
4. **单机单副本**。`/opt/minio/data` 在宿主机磁盘上，没有冗余。
   服务器磁盘 32 GB 可用，图片场景足够；真要冗余需要外部 S3 或 RAID。
5. **内存 256 MiB 上限**。实测空载约 71 MiB，导入大图或并发上传时会上涨。
   超限由内核 OOM 杀掉容器，`restart: unless-stopped` 会让它自己起来。

## 7. 变更记录

| 日期 | 变更 |
| :--- | :--- |
| 2026-10-19 | 首次部署：MinIO 容器 + 桶 `pr-files` + 匿名只读 + nginx `/files/` 反代 + `POST /api/uploads` |
