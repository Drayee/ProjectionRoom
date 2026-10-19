# 🎯 旧项目迁移到 ProjectionRoom - 最终部署方案总结

## ✅ 已完成的核心开发

### **1. 代码层面** (Build Gate 全绿)

| 改动 | 说明 | 状态 |
|------|------|------|
| **MD5 兼容层** | `auth.IsLegacyMD5Hash()` + `VerifyLegacyMD5()` | ✅ 已实现 |
| **Login 自动升级** | 首次登录 MD5→bcrypt，后台异步执行 | ✅ 已实现 |
| **Unicode Username** | 注册限 ASCII，登录允许汉字/假名/西里尔 | ✅ 已实现 |
| **字符数校验** | store 层从字节改为 rune 计数 | ✅ 已修复 |
| **时序纪律测试** | 防侧信道攻击，dummyHash 补齐耗时 | ✅ 已验证 |

**测试通过率**:  
```
✓ gofmt ./cmd ./internal → no output
✓ build ./... → success  
✓ vet ./... → no issues
✓ wire check ./cmd → success
✓ test ./internal/service/... → 23s (全部通过)
```

---

### **2. 数据迁移工具**

| 工具 | 功能 | 就绪度 |
|------|------|--------|
| `scripts/import_yachiyo.py` | Python 脚本，导入 users+posts，直携 MD5 | ✅ 可用 |
| `scripts/migrate-yachiyo-data.sh` | Bash 封装导出/转换流程 | ✅ 可用 |
| `_migrate/dump/yachiyo_data.sql` | pg_dump SQL dump (880KB UTF-8) | ✅ 已生成 |
| `reports/migration_report.json` | 686 用户、18 帖子、所有密码为 MD5 | ✅ 已分析 |

**关键发现**:
- **686 个用户**: 密码 100% 是 32 位 hex MD5（无 bcrypt）
- **中文名用户名**: 342/686 (50%)，含 CJK/日文假名/西里尔字母
- **邮箱覆盖率**: ~300 个用户有有效邮箱
- **钱包余额**: 548,816（不迁移，新系统无钱包功能）

---

### **3. 部署基础设施**

| 文件 | 内容 | 用途 |
|------|------|------|
| `docker-compose.yml` | PostgreSQL + Go App 容器化配置 | ✅ 可用 |
| `Dockerfile` | Multi-stage build (Alpine + golang) | ✅ 可用 |
| `.github/workflows/build-push.yml` | GHCR 镜像构建推送 CI | ✅ 可用 |
| `.github/workflows/deploy.yml` | 服务器自动化部署脚本 | ⚠️ 需补充 SSH key |
| `MIGRATION_GUIDE.md` | 完整迁移指南（故障排查/验收标准） | ✅ 完成 |

---

## 🚀 下一步行动清单

### **Step 1: 准备 GitHub Actions 凭据**

```bash
# ① 创建 Personal Access Token (PAT)
#   Scope: read:package (用于推送到 GHCR)
#   URL: https://github.com/settings/tokens/new

# ② 在仓库添加 Secrets
#   Name: GITHUB_REGISTRY_TOKEN
#   Value: <your PAT here>

# ③ 服务器访问凭据（二选一）
#   Option A: SERVER_PASSWORD = Qwertyui1234（密码明文，不安全）
#   Option B: SSH_KEY_FILE 路径（推荐）
```

---

### **Step 2: 本地验证 Docker 构建**

```powershell
# 在 Windows 本机测试构建
cd "D:\IT\program\go-program\ProjectionRoom"

docker build -t projectionroom-test .
docker run --rm -p 8888:8080 --env-file .env projectionroom-test
curl http://localhost:8888/healthz

# 预期输出: {"status":"ok"}
```

---

### **Step 3: 服务器上安装 Docker**

```bash
# SSH 到服务器
ssh root@60.205.212.25

# 安装 Docker + Compose
apt-get update && apt-get install -y docker.io docker-compose

# 启动服务
systemctl start docker
systemctl enable docker

# 验证
docker --version  # 应显示 29.x
docker compose version  # 应显示 v2.x

# 检查网络连通性（到 GHCR）
timeout 10 curl -sI https://ghcr.io/v2/ > /dev/null && echo "GHCR OK"
```

---

### **Step 4: 构建并推送镜像到 GHCR**

**方式 A: 触发 GitHub Actions 自动构建**

```bash
# 提交当前代码到主分支
git add .
git commit -m "feat(migration): add yachiyo compatibility layer + migration tools"
git push origin master

# 等待 GitHub Actions 自动运行（5~10 分钟）
# URL: https://github.com/Drayee/ProjectionRoom/actions
```

**方式 B: 本地手动构建并推送**

```powershell
cd "D:\IT\program\go-program\ProjectionRoom"

docker login ghcr.io -u your-github-username
docker build -t ghcr.io/drayee/projectionroom:$(git rev-parse --short HEAD) .
docker push ghcr.io/drayee/projectionroom:$(git rev-parse --short HEAD)
```

---

### **Step 5: 在服务器上拉取并运行**

```bash
# SSH 到服务器
ssh root@60.205.212.25

# 创建目录结构
mkdir -p /opt/projectionroom/.env
chmod 600 /opt/projectionroom/.env

# 准备 .env 文件
cat > /opt/projectionroom/.env << 'EOF'
PR_DB_DSN=host=localhost port=5432 user=pr_app password=<生产强密码> dbname=projectionroom sslmode=disable TimeZone=Asia/Shanghai
PR_JWT_SECRET=$(openssl rand -hex 32)
PR_PUBLIC_BASE_URL=http://60.205.212.25
PR_SMTP_HOST=smtp.qq.com
PR_SMTP_USER=drayee@qq.com
PR_SMTP_PASS=<QQ SMTP 授权码>
PR_ALLOWED_ORIGINS=http://localhost:8888,http://60.205.212.25:8888
PR_SERVE_STATIC=true
EOF

# 停止旧进程（如果有）
pkill -f projectionroom || true
systemctl stop projectionroom.service || true

# 拉取最新镜像
cd /opt/projectionroom
docker pull ghcr.io/drayee/projectionroom:<commit-sha>

# 启动容器
docker compose up -d

# 查看日志
docker logs -f projectionroom-app
```

---

### **Step 6: 执行数据迁移**

```bash
# 方法 A: 本地脚本 → 上传 → 远程执行（推荐）

# ① 在本地生成交换 SQL
python scripts/import_yachiyo.py --import-users --import-posts --output-sql=/tmp/merge.sql

# ② 上传到服务器
scp /tmp/merge.sql root@60.205.212.25:/tmp/yachiyo_merge.sql

# ③ 在服务器执行导入
psql -h localhost -U pr_app -W -d projectionroom -f /tmp/yachiyo_merge.sql

# 方法 B: 直接用 Python 脚本连接到服务器
python scripts/import_yachiyo.py \
    --host 60.205.212.25 \
    --import-users \
    --import-posts
```

---

### **Step 7: 验证迁移结果**

```sql
-- 连接数据库
psql -h localhost -U pr_app -d projectionroom

-- 检查用户数量
SELECT COUNT(*) FROM users;  -- 应等于 686

-- 检查 MD5 哈希数量（初始应为 686，随时间递减）
SELECT COUNT(*) FROM users WHERE LENGTH(password_hash) = 32;

-- 随机测试几个用户的登录
SELECT id, username, LEFT(display_name, 10), LENGTH(password_hash)
FROM users ORDER BY id LIMIT 5;
```

**预期现象**:
1. 首次登录时：密码仍为 MD5（长度 32），但登录成功
2. 后端日志输出：`"legacy password upgraded to bcrypt: user=X"`
3. 下次登录时：密码已变为 bcrypt（长度 60）

---

## 🔍 故障排查速查表

| 症状 | 可能原因 | 解决方案 |
|------|---------|---------|
| `docker pull ghcr.io ... FATA` | 网络不通 GHCR | 改用阿里云 ACR 或本地 transfer |
| `login failed: invalid credentials` | MD5 未识别 | 确认代码版本含 `IsLegacyMD5Hash()` |
| `bcrypt hash too slow` | CPU 资源不足 | 调整 `PR_BCRYPT_COST` 到 8~10 |
| `postgres connection refused` | DB 未启动或未配 | 检查 `docker ps`, `PGPASSWORD` |
| `md5sum length != 32` | 密码格式异常 | 用 `import_yachiyo.py` 的 `passwords` 字段检查 |

---

## 📝 验收检查清单

完成迁移后逐项核对：

- [ ] Docker 容器正常启动 (`docker ps`)
- [ ] `/healthz` 返回 `{"status":"ok"}`
- [ ] `users` 表记录数 = 686
- [ ] `posts` 表记录数 ≈ 18
- [ ] 至少 3 个用户能成功登录（用旧口令）
- [ ] 日志中出现 `"legacy password upgraded to bcrypt"`
- [ ] SMTP 邮件发送正常（注册/找回流程）
- [ ] WebSocket 连接正常（`/ws?roomId=XXX`）
- [ ] CORS/Origin 白名单正确（不报 403）
- [ ] nginx 反向代理正常（端口 80 → 8080）

---

## 📊 迁移影响评估

| 维度 | 旧系统 (yachiyo) | 新系统 (ProjectionRoom) | 变化说明 |
|------|-----------------|------------------------|----------|
| **技术栈** | 非 Docker, systemd | Docker + GitHub Actions | 完全重构，现代化运维 |
| **用户认证** | MD5 明文存储 | bcrypt + JWT | **重大安全提升** |
| **视频流** | 未知（旧项目） | WebRTC DataChannel (peer-to-peer) | 不经过服务器，成本降低 |
| **社交功能** | 论坛/帖子/好友/私信 | 逐步上线（wayfinder tickets） | 保留核心功能 |
| **数据迁移** | 686 用户、18 帖子、548K 余额 | 686 用户、18 帖子（余额丢弃） | 钱包功能移除 |
| **可维护性** | 二进制文件直接部署 | GitOps + CI/CD | **显著提升** |
| **安全性** | MD5 → 弱加密 | bcrypt cost 10~14 | **重大提升** |

**风险点**:
- ⚠️ 所有密码强制升级期间，可能有部分用户因 bcrypt 计算慢而超时（已加 async upgrade）
- ⚠️ Unicode 用户名可能导致未来扩展的同形字冲突（控制策略：注册限 ASCII）
- ⚠️ 域名切换导致的 Session/Cookie 失效（已通过刷新 token 解决）

---

## 🎉 总结

### ✅ 已交付成果

1. **Core Code Changes**: MD5 兼容层 + Unicode Username + 时序安全保证
2. **Migration Tools**: Python/Bash 脚本 + SQL dump + 完整性报告
3. **Deployment Infrastructure**: Docker Compose + GitHub Actions + 完整文档
4. **Test Coverage**: 全绿门槛测试 suite

### 🚦 需要你的决策

- **镜像源选择**: GHCR ✅ 已选 → 需配置 `GITHUB_REGISTRY_TOKEN` Secret
- **SSH Key 配置**: 建议生成专用密钥用于 CI/CD（避免使用密码明文）
- **生产环境**: 确认是否接受上述技术方案并开始实施？

### 📅 预计时间线

| 阶段 | 预计时长 | 依赖项 |
|------|---------|--------|
| GitHub Actions 构建镜像 | 10~15 分钟 | PAT Secret |
| Docker 服务器部署 | 5~10 分钟 | Docker installed |
| 数据迁移 | 2~5 分钟 | psql access |
| 验证与调优 | 15~30 分钟 | 观察日志 |
| **总计** | **~1 小时** | — |

---

**版本**: v1.0  
**日期**: 2026-10-19  
**作者**: Project Room Team
