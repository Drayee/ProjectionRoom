# Yachiyo → ProjectionRoom 数据迁移指南

## 📋 概述

本文档记录了将旧项目 **yachiyo**（686 用户、18 帖子、MD5 密码）迁移到 **ProjectionRoom**（新版本 Docker + GitHub Actions）的完整流程。

### ✅ 已完成的改动

1. **用户名支持 Unicode**  
   - 新用户注册仍限制为 `^[A-Za-z0-9_]{3,20}$`（ASCII）
   - 旧用户登录允许 Unicode（含汉字/假名/西里尔字母）

2. **MD5 兼容层**  
   - `auth.IsLegacyMD5Hash()` 识别 32 位十六进制 MD5
   - `auth.VerifyLegacyMD5()` 校验 MD5 口令
   - Login 路径中自动升级为 bcrypt（首次登录后）

3. **字符数校验**  
   - store 层从 `len(u.Username)`（字节）改为 `len([]rune(u.Username))`（字符）
   - 中文用户名正确计数

4. **完整测试覆盖**  
   - `TestVerifyLegacyMD5()` - MD5 识别与校验
   - `TestLoginLegacyMD5Upgrade()` - 登录升级路径
   - `TestLoginDoesNotLeakAccountExistence()` - 时序纪律验证

---

## 🚀 部署流程

### Step 1: 服务器环境准备

```bash
# SSH 到服务器
ssh root@60.205.212.25

# 安装 Docker + Docker Compose
apt-get update && apt-get install -y docker.io docker-compose

# 启动 Docker 服务
systemctl start docker
systemctl enable docker

# 验证安装
docker --version
docker compose version
```

### Step 2: 准备 Git 代码

```bash
# 克隆仓库到服务器（或已通过 CI/CD 推送）
cd /opt/projectionroom
git pull origin master

# 或手动拉取最新版本
docker pull ghcr.io/drayee/projectionroom:latest
```

### Step 3: 导入数据（两种方式）

#### 方式 A: Python 脚本（推荐）

```bash
# 在服务器本地执行（如果 yachiyo DB 也在这台机器上）
python3 scripts/import_yachiyo.py \
    --host localhost \
    --import-users \
    --import-posts
```

#### 方式 B: 通过 pg_dump/pg_restore

```bash
# ① 本地导出 SQL dump
PGPASSWORD=yuanshen123 pg_dump -h localhost -U postgres -d yachiyo \
    --data-only --column-inserts \
    -t users -t posting > _migrate/yachiyo_data.sql

# ② 上传到服务器
scp _migrate/yachiyo_data.sql root@60.205.212.25:/tmp/

# ③ 在服务器上导入
psql -h localhost -U pr_app -d projectionroom -f /tmp/yachiyo_data.sql
```

### Step 4: 部署 Docker 容器

```bash
# ① 配置 .env 文件（生产环境请用真实值）
cat > /opt/projectionroom/.env <<EOF
PR_DB_DSN=host=localhost port=5432 user=pr_app password=your_strong_password dbname=projectionroom sslmode=disable TimeZone=Asia/Shanghai
PR_JWT_SECRET=$(openssl rand -hex 32)
PR_PUBLIC_BASE_URL=http://60.205.212.25
PR_SMTP_HOST=smtp.qq.com
PR_SMTP_USER=your-email@qq.com
PR_SMTP_PASS=your-smtp-auth-code
PR_ALLOWED_ORIGINS=http://localhost:8888,http://60.205.212.25:8888
PR_SERVE_STATIC=true
EOF

chmod 600 /opt/projectionroom/.env

# ② 构建镜像（本地）
cd "/d/IT/program/go-program/ProjectionRoom"
docker build -t projectionroom-app .

# ③ 标签并推送到 GHCR
docker tag projectionroom-app ghcr.io/drayee/projectionroom:$(git rev-parse --short HEAD)
docker login ghcr.io
docker push ghcr.io/drayee/projectionroom:$(git rev-parse --short HEAD)

# ④ 或在服务器直接拉取
ssh root@60.205.212.25 "cd /opt/projectionroom && docker compose down && docker compose up -d"
```

### Step 5: 验证登录

```bash
# 等待服务启动
sleep 10
curl http://localhost:8080/healthz

# 测试登录（使用旧项目口令）
# ⚠️ 预期结果：登录成功，日志中会出现 "legacy password upgraded to bcrypt"
# 示例（需替换为真实用户 ID）：
curl -c /tmp/cookies.txt -X POST http://localhost:8080/api/auth/login \
    -H "Content-Type: application/json" \
    -d '{"username":"sam","password":"1234"}'
```

---

## 🔍 故障排查

### 问题 1: 登录失败 "invalid credentials"

**症状**: 明明记得密码正确，但提示错误

**原因**: 
1. 密码不是 MD5 格式（可能是明文或其他哈希）
2. User 表未成功导入

**检查**:

```sql
-- 查看是否有导入用户
SELECT COUNT(*) FROM users;
-- 应该等于 686

-- 查看具体用户的密码哈希
SELECT id, username, LEFT(password_hash, 20), LENGTH(password_hash)
FROM users WHERE username = 'sam';
-- MD5 长度应为 32，bcrypt 为 60
```

**解决**:
- 如果长度为 32，确认是 MD5 → 代码会自动处理
- 如果为 0 或异常，重新运行导入脚本

### 问题 2: Upgrade 失败 "bcrypt hash too slow"

**症状**: 登录成功但日志显示 "upgrade failed"

**原因**: 
- 服务器内存不足导致 bcrypt cost 计算超时
- CPU 资源被其他进程占用

**解决**:

```bash
# 检查系统资源
free -h
top | grep node

# 临时调整 bcrypt cost（不推荐生产环境）
# PR_BCRYPT_COST=8 # 默认 10~14
```

### 问题 3: Docker 无法拉取镜像

**症状**: 

```
FATA[0000] error pulling image: Get https://ghcr.io/v2/: dial tcp...
```

**原因**: 
- 服务器防火墙阻止访问 GitHub Container Registry
- DNS 解析失败

**解决**:

```bash
# 方法 A: 使用阿里云镜像源
docker login registry.cn-beijing.aliyuncs.com
docker tag projectionroom-app registry.cn-beijing.aliyuncs.com/<namespace>/projectionroom
docker push registry.cn-beijing.aliyuncs.com/<namespace>/projectionroom

# 方法 B: 修改 `/etc/docker/daemon.json` 添加镜像加速器
{
  "registry-mirrors": ["https://docker.mirrors.example.com"]
}

systemctl restart docker
```

---

## 📊 数据质量分析总结

### yachiyo 数据库画像

| 指标 | 数量 | 说明 |
|------|------|------|
| 总用户数 | 686 | |
| 有效邮箱 | ~300 | |
| 已批准帖子 | 18 | 几乎全部批准 |
| 钱包余额 | 548,816 | 不迁移（新系统无钱包） |
| 密码哈希格式 | 100% MD5 (32 位 hex) | 无 bcrypt |
| 中文名用户名 | 342 (50%) | 含 CJK/日文假名/西里尔字母 |
| 非 ASCII 字符分布 | 683 个不同字符 | 主要是 CJK (983 次) |

### 映射方案

| yachiyo 字段 | projectionroom 目标 | 处理方式 |
|-------------|---------------------|----------|
| `user_id` | `users.id` | 直接映射，保持自增序列 |
| `name` | `username` + `display_name` | 尝试生成 ASCII username；如失败则 `username = display_name` |
| `email` | `users.email` | 小写转换，保留 NULL |
| `role` (`normal`/`member`) | `users.role` | 统一为 `'user'` |
| `password` (MD5) | `users.password_hash` | **直携 MD5** → Login 时自动升级 |
| `create_time` (TIME) | `created_at` | 转换为 `TIMESTAMPTZ` |
| `is_approved` | N/A | 忽略（投影室没有帖子审核概念） |
| `score` | `posts.like_count` | 作为点赞数估算 |

---

## 🛠️ 技术细节

### MD5 兼容层的时序安全保证

**不变量**: "账号不存在"与"口令错误"必须消耗等量的 bcrypt CPU

**实现**:

```go
ok := VerifyPassword(bcryptHash, plain)
if !ok && IsLegacyMD5Hash(hash) {
    // ⚠️ 这里补一次 dummyHash 校验，避免时序泄露
    verifyDummy(plain) 
    ok = VerifyLegacyMD5(md5Hash, plain)
}
```

**为什么关键**: 
- 若漏掉 `verifyDummy`，攻击者可以测量响应时间区分"旧用户"vs"新用户/不存在"
- MD5 对畸形 bcrypt 哈希是"解析阶段就返回"（~1ms），而 bcrypt 需 200~500ms
- `verifyDummy` 补齐这个差距，使两条路径耗时一致

### Unicode Username 的白名单策略

| 路径 | 白名单规则 | 理由 |
|------|-----------|------|
| **注册端点** | `^[A-Za-z0-9_]{3,20}$` | 防止同形字、Bidi 漏洞、未来扩展风险 |
| **登录查询** | Unicode 允许 | 迁移过来的旧用户需要能登录 |
| **Store 归一化** | `strings.ToLower(strTrim())` | 统一小写，不强制 ASCII |

**取舍说明**: 
- 允许中文用户名登录会增加"西里尔 а vs 拉丁 a"冲突的风险
- 但可以通过注册入口的成本（每 IP 限流 + bcrypt）和人工改名流程来控制
- 旧库恰好只有 2 个西里尔字母混合的用户，影响极小

---

## ✅ 验收标准

迁移完成后，验证以下各项：

1. **用户完整性**
   ```sql
   SELECT COUNT(*) FROM users; -- 应等于 686
   SELECT SUM(CASE WHEN LENGTH(password_hash)=32 THEN 1 ELSE 0 END) FROM users; -- MD5 数量
   ```

2. **帖子完整性**
   ```sql
   SELECT COUNT(*) FROM posts; -- 应接近 18（可能有重复/过滤）
   ```

3. **登录功能**
   - 随机选择 5 个用户测试登录
   - 日志中应出现 `"legacy password upgraded to bcrypt"`
   - 下次登录时应看到 `LENGTH(password_hash)=60`

4. **安全性**
   ```sql
   -- 检查是否所有用户都是 bcrypt（最终状态）
   SELECT COUNT(*) FROM users WHERE LENGTH(password_hash)=32; -- 应随时间递减至 0
   ```

5. **性能**
   - 并发登录测试（10+ 请求/秒）无阻塞
   - bcrypt cost 未触发闸门拒绝

---

## 🔄 后续建议

### 短期（1~2 周）

1. **强制改密通知**: 给所有邮件已验证用户发送"安全更新提醒"
2. **监控升级日志**: 观察 bcrypt 升级速率，确保不过载
3. **清理冗余表**: 删除 old project 特有的表（checkpoint, vector_store 等）

### 中期（1~3 个月）

1. **全面移除 MD5**: 设置最后期限（e.g., 6 个月后所有 MD5 强制重置）
2. **启用双因素认证**: 提高安全性
3. **完善后台管理**: 提供批量操作界面

### 长期（6 个月+）

1. **完全淘汰 yachiyo 库**: 归档后下线
2. **数据审计**: 清理长时间未活跃用户
3. **性能优化**: 基于真实负载调整架构参数

---

## 📞 联系支持

遇到任何问题：
1. 查阅文档：`docs/ACCOUNTS.md`, `CLAUDE.md`
2. 提交 Issue: <https://github.com/Drayee/ProjectionRoom/issues>
3. Email: drayee@qq.com

---

**版本**: v0.1  
**更新**: 2026-10-18  
**作者**: Project Room Team
