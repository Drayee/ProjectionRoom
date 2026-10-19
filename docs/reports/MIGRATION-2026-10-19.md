# yachiyo → ProjectionRoom 迁移完成报告

日期：2026-10-19
执行人：AI（Little Code Sauce）+ YG

## 结果

| 项目 | 数值 |
| :--- | :--- |
| 迁移用户 | **686** |
| 迁移帖子 | **18** |
| 带邮箱用户 | 685 |
| 初始 MD5 哈希 | 686（登录后自动升级 bcrypt） |
| 容器状态 | running / healthy |
| healthz | 200（直连 8080 与 nginx 80 均通） |
| 登录验证 | sam/1234 → 200，哈希 32→60（$2a$12$） |

## 代码改动（4 处）

1. `internal/service/auth/password.go`
   新增 `IsLegacyMD5Hash` / `VerifyLegacyMD5`（迁移兼容路径，不是新的哈希方案）。

2. `internal/service/account.go` — `Login`
   MD5 命中后**异步**把口令升级为 bcrypt（不撤销会话，因为这不是改密）。
   ⚠ 关键细节：对 MD5 行，bcrypt 是解析阶段就返回的（远快于真实校验），
   所以先补一次 `verifyDummy` 对齐耗时 —— 否则"哪些账号来自旧项目"变成可枚举情报。

3. `internal/store/user.go`
   username 长度校验从 `len()`（字节）改为 `len([]rune())`（字符），否则中文名被误拒。

4. `Dockerfile`
   移除 `-tags wireinject`。该 tag 会选中 `cmd/wire.go`（空壳，`InitializeApp` 返回 nil）
   而非 `cmd/wire_gen.go`，编译期无警告、启动即 SIGSEGV。同时 Go 版本对齐 go.mod（1.26）。

## 部署拓扑

```
公网 → nginx :80 → 127.0.0.1:8080 → docker container (network_mode: host)
                                          ↓
                                   宿主机 PostgreSQL 16 :5432 (127.0.0.1)
```

- 容器用 **host 网络**：宿主机 PG 只监听 127.0.0.1，bridge 网络的容器连不上，
  而 `host.docker.internal` 在 Linux 需要额外 DNS 配置。host 模式下共享网络栈，DSN 不用改。
- `.env` 必须显式设 `PR_ADDR=127.0.0.1:8080`（默认是 8888，与 nginx 反代目标不符）。
- systemd 单元已 `disable`，回滚只需 `systemctl start projectionroom.service`（二进制仍在 /opt/projectionroom）。

## 已知遗留

1. `PR_FFMPEG` 为空 → 服务端切片作业会失败（不影响直播链路，peer 间仍直传）。
2. `client/dist/downloads` 不在镜像内 → `/api/downloads/segmenter` 返回空清单。
3. `PR_ALLOWED_ORIGINS` 仍是 8888，需改成 `http://60.205.212.25`（否则经域名的 /ws 会被 403）。
4. 685 个用户的 MD5 会随各自下次登录自然升级为 bcrypt。
