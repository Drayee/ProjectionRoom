# 生产部署：单端口 80（前端 + API + WebSocket 同源）

> 本文记录 **2026-10-19 起的生产形态**：对外只有 `http://60.205.212.25` 一个入口，
> 前端页面、REST API、WebSocket 全部走 80，由 nginx 反代到应用。

## 1. 拓扑

```
浏览器
  │  http://60.205.212.25  (80)
  ▼
nginx  (0.0.0.0:80, 唯一对外入口)
  │  proxy_pass
  ▼
127.0.0.1:8080
  │
  ▼
docker container「projectionroom-app」  (network_mode: host)
  │  PR_DB_DSN host=127.0.0.1
  ▼
宿主机 PostgreSQL 16  (127.0.0.1:5432)
```

**只有一个对外端口：80。** 8080 只监听回环，公网不可达。

## 2. 三层配置必须一致

改端口时这三处必须同步，漏一处就会出现"页面能开但连不上"：

| 层 | 配置项 | 生产值 |
| :--- | :--- | :--- |
| nginx | `proxy_pass` | `http://127.0.0.1:8080` |
| 应用 | `PR_ADDR` | `127.0.0.1:8080` |
| 应用 | `PR_ALLOWED_ORIGINS` | `http://60.205.212.25,http://60.205.212.25:80` |
| 应用 | `PR_PUBLIC_BASE_URL` | `http://60.205.212.25` |

### 为什么 `PR_ALLOWED_ORIGINS` 要写两种形态

浏览器的 `Origin` 对 http 默认端口 80 **通常会省略**（地址栏输 `60.205.212.25` 得到
`http://60.205.212.25`），但手写带端口的 URL 时会带上（`http://60.205.212.25:80`）。
两种都写才能覆盖全部用户。

⚠ 漏配的表现是**页面正常、房间进不去**：`/ws` 一律 403，
日志里是 `来源被拒 ... 不在 PR_ALLOWED_ORIGINS 白名单里`。
排查时先看这一行，而不是怀疑信令逻辑。

### 换域名 / 加域名：五项清单

**2026-11-02 线上事故**：新域名 `yachiyocat.top` 上线后只改了 nginx，`PR_ALLOWED_ORIGINS`
还停在旧的 IP 形态。结果是页面一切正常（静态资源不判来源），但
`/ws?roomId=…` 与 `/ws/user?ticket=…` **全部 403** —— 观众进不了房，登录用户这条
实时通道无限重连，浏览器控制台里只有几十行 handshake 403。

从那次起，**服务端在启动期就会拦下这种配置**（`config.requireComplete` 会校验
`PR_PUBLIC_BASE_URL` 的 host 是否被 `PR_ALLOWED_ORIGINS` 覆盖，不覆盖就拒绝启动并
打印"该把哪一项补进去"）。所以现在的症状不再是"线上悄悄 403"，而是**部署当天起不来**
—— 这正是想要的。

换域名时按顺序做这五件事（少任何一项都不算完成）：

| # | 位置 | 改什么 | 漏了会怎样 |
| :--- | :--- | :--- | :--- |
| 1 | DNS | 域名解析到 `60.205.212.25` | 页面打不开 |
| 2 | nginx | `server_name` 加上新域名（或保持 `_` 默认） | 证书/虚拟主机不匹配 |
| 3 | `/opt/projectionroom/.env` | `PR_PUBLIC_BASE_URL=http://<新域名>` | 邮件里的验证链接指向旧地址 |
| 4 | `/opt/projectionroom/.env` | `PR_ALLOWED_ORIGINS` 里加 `http://<新域名>` **与** `http://<新域名>:80` 两种形态（**旧的 IP 不要删**，老书签还在用） | `/ws` 与 `/ws/user` 一律 403 |
| 5 | 重启容器 | `docker compose up -d`（改了 `.env` 必须重建容器，`restart` 不会重读） | 新配置不生效 |

改完用原始 Upgrade 握手自查（**必须带 `Origin`**，不带 Origin 的裸 curl 走的是
"非浏览器客户端"放行分支，永远不会 403，看起来很健康）：

```bash
NEW=http://yachiyocat.top
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H "Origin: $NEW" "$NEW/ws?roomId=TEST01&clientId=c1"      # 期望 101
```

同一个握手**换成旧域名/白名单外的 Origin** 必须仍是 403（这条是 S-5 的判据，不是故障）。

## 3. 前端不需要单独的端口

前端是**构建产物**（`client/dist`），由 Go 进程在同端口托管（`PR_SERVE_STATIC=true`）。
所以"前后端统一到 80"不需要额外服务，也不需要把 Vite dev server 搬到生产 ——
生产环境根本没有 Node 进程。

## 4. 验收清单

```bash
# 页面
curl -s -o /dev/null -w '%{http_code}\n' http://60.205.212.25/          # 200 + HTML

# 健康检查
curl -s -o /dev/null -w '%{http_code}\n' http://60.205.212.25/healthz   # 200

# API
curl -s -o /dev/null -w '%{http_code}\n' http://60.205.212.25/api/ice   # 200
curl -s -o /dev/null -w '%{http_code}\n' http://60.205.212.25/api/public-rooms  # 200

# 登录
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://60.205.212.25/api/auth/login \
  -H 'Content-Type: application/json' -d '{"username":"sam","password":"1234"}'    # 200

# WebSocket 握手（必须用原始 Upgrade 头，不能用普通 HTTP 客户端）
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H 'Origin: http://60.205.212.25' \
  'http://60.205.212.25/ws?roomId=TEST01&clientId=c1'                     # 101
```

白名单外的 Origin 必须得到 403（这是 S-5 的判据，不是故障）：

```bash
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H 'Origin: http://evil.com' \
  'http://60.205.212.25/ws?roomId=TEST01&clientId=c1'                     # 403
```

## 5. 变更记录

| 日期 | 变更 |
| :--- | :--- |
| 2026-10-19 | 统一到 80：修正 `PR_ALLOWED_ORIGINS`（原为 `:8888`，导致 80 访问时 `/ws` 403）；nginx 反代目标保持 8080 不变 |
| 2026-11-02 | **第二次踩同一个坑**（换成 `yachiyocat.top` 时又漏配白名单，`/ws` 与 `/ws/user` 全 403）。这次不只补文档：`config.requireComplete` 现在校验「`PR_PUBLIC_BASE_URL` 的 host 必须在 `PR_ALLOWED_ORIGINS` 里」，漏配**拒绝启动**；新增本文的「换域名五项清单」；前端在 chunk 404 时自动刷新一次 |
