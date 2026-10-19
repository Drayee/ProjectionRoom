# 🚀 快速手动部署方案（替代 GitHub Actions）

## ⏱️ 预计时间：5-10 分钟

---

## Step 1: 在服务器上安装 Docker + PostgreSQL

```powershell
# SSH 连接到服务器（使用密码 Qwertyui1234）
ssh root@60.205.212.25
# 密码：Qwertyui1234

# ① 确认 Docker 已安装（上一步已完成）
docker --version
docker compose version

# ② 检查 PostgreSQL
sudo -u postgres psql -c "SELECT current_database();"

# 创建新数据库（清空旧库）
sudo -u postgres dropdb --if-exists projectionroom
sudo -u postgres createdb projectionroom

# 设置密码（临时，后续会更新 .env）
psql -U postgres -d projectionroom -c "CREATE USER pr_app WITH PASSWORD 'woOzWJFQn4Ilp93GSLkjYc75PfBt';"
psql -U postgres -d projectionroom -c "GRANT ALL PRIVILEGES ON DATABASE projectionroom TO pr_app;"

# 退出 SSH
exit
```

---

## Step 2: 构建本地镜像并上传

### 方式 A: 用 Docker Compose 本地构建（推荐）

```powershell
cd "D:\IT\program\go-program\ProjectionRoom"

# ① 生成 JWT Secret
openssl rand -hex 32 > .secret_jwt

# ② 创建 .env 文件
cat > .env << EOF
PR_DB_DSN=host=localhost port=5432 user=pr_app password=woOzWJFQn4Ilp93GSLkjYc75PfBt dbname=projectionroom sslmode=disable TimeZone=Asia/Shanghai
PR_JWT_SECRET=$(type .secret_jwt)
PR_PUBLIC_BASE_URL=http://60.205.212.25
PR_SMTP_HOST=smtp.qq.com
PR_SMTP_USER=drayee@qq.com
PR_SMTP_PASS=<你的 QQ SMTP 授权码>
PR_ALLOWED_ORIGINS=http://localhost:8888,http://60.205.212.25:8888
PR_SERVE_STATIC=true
EOF

# ③ 构建 Docker 镜像
docker build -t projectionroom-manual .

# ④ 查看镜像
docker images | grep projectionroom
# 应该看到投影房间镜像
```

### 方式 B: 直接用二进制部署（跳过 Docker，最快速）

如果不想折腾 Docker，可以这样：

```bash
# SSH 到服务器
ssh root@60.205.212.25

# 拉取最新编译的二进制（假设已经有 CI 构建过）
wget -P /tmp/ https://github.com/Drayee/ProjectionRoom/releases/download/v0.1/projectionroom_linux_amd64.tar.gz

# 或者直接从本机拷贝
scp /opt/projectionroom/projectionroom root@60.205.212.25:/tmp/

# 启动服务
cd /opt/projectionroom
mv projectionroom.backup* projectionroom.old  # 备份旧版
cp /tmp/projectionroom .
chmod +x projectionroom

# 修改 .env 配置
nano /opt/projectionroom/.env

# 重启服务
systemctl restart projectionroom.service

# 查看日志
journalctl -u projectionroom.service -f
```

---

## Step 3: 导入数据

### 从本地 yachiyo 导出并导入投影房间

```powershell
# ① 导出 SQL dump（已存在 _migrate/dump/yachiyo_data.sql）

# ② 通过 SCP 上传到服务器
scp _migrate/dump/yachiyo_data.sql root@60.205.212.25:/tmp/yachiyo.sql

# ③ 在服务器上导入
ssh root@60.205.212.25

# 连接投影房间数据库（如果运行的是 PostgreSQL 16+）
psql -h localhost -U pr_app -W projectionroom -f /tmp/yachiyo.sql

# 验证用户数量
psql -h localhost -U pr_app -W projectionroom -c "SELECT COUNT(*) FROM users;"
# 应该输出 686

# 验证 MD5 哈希
psql -h localhost -U pr_app -W projectionroom -c "SELECT COUNT(*) FROM users WHERE LENGTH(password_hash)=32;"
# 应该接近 686（所有都是 MD5）

# 退出
exit
```

---

## Step 4: 验证登录

### 测试任意一个旧项目用户的登录

```powershell
# 等待 1 分钟后服务完全启动
curl http://60.205.212.25:8888/healthz
# 应返回 {"status":"ok"}

# 用旧项目口令测试登录（示例：用户 id=379 是 sam，密码是 "1234"）
curl -X POST http://60.205.212.25:8888/api/auth/login \
    -H "Content-Type: application/json" \
    -d '{"username":"sam","password":"1234"}' \
    -c /tmp/cookies.txt
    
# 预期：登录成功，响应包含 access token
```

---

## Step 5: 监控升级日志

```bash
# SSH 到服务器查看实时日志
ssh root@60.205.212.25

# systemd 服务日志（如果用 systemctl）
journalctl -u projectionroom.service -f --no-hostname | grep -iE "login|upgrade|bcrypt|legacy"

# 或直接看文件日志
tail -f /var/log/projectionroom.log | grep -iE "legacy.*upgraded"

# 首次登录后应该看到：
# [INFO] legacy password upgraded to bcrypt: user=XXX username=sam
```

---

## 常见问题解决

### Q1: Docker 镜像推不上去 GHCR

**原因**: GitHub Action 需要 `GITHUB_REGISTRY_TOKEN` Secret  
**解决**: 
1. 访问 https://github.com/Drayee/ProjectionRoom/settings/secrets/actions
2. 添加新 Secret: `GITHUB_REGISTRY_TOKEN` = 你的 PAT Token
3. 重新触发 Actions 或改用手动构建（见上面步骤 2）

### Q2: 导入时报 "relation users already exists"

**原因**: 数据库里已有旧表结构冲突  
**解决**:
```sql
-- 先清空表但保留结构
DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS posts CASCADE;

-- 重新运行迁移脚本
psql -h localhost -U pr_app -W projectionroom -f /tmp/yachiyo.sql
```

### Q3: 登录时 "invalid credentials"

**原因**: 代码版本未包含 MD5 兼容层  
**解决**: 
- 确保服务器上的二进制是从这次提交构建的（commit 28c9734）
- 或直接用 Python 脚本导入前先转换密码格式

### Q4: PostgreSQL 密码认证拒绝

**问题**: `pg_hba.conf` 限制 localhost  
**解决**:
```bash
# 编辑 pg_hba.conf
sudo nano /etc/postgresql/16/main/pg_hba.conf

# 在文件末尾加一行（信任模式，仅本地开发）
# host    all             all             127.0.0.1/32            trust
# 或
# local   all             all                                     trust

# 重启 PostgreSQL
sudo systemctl restart postgresql@16-main
```

---

## ✅ 验收清单

完成后逐项核对：

- [ ] Docker 容器正常启动 (`docker ps`) OR 服务正常运行 (systemctl status)
- [ ] `/healthz` 返回 `{"status":"ok"}`
- [ ] `users` 表记录数 = 686
- [ ] `posts` 表记录数 ≈ 18
- [ ] 至少 3 个用户能成功登录（用旧口令如 sam/1234）
- [ ] 日志中出现 `"legacy password upgraded to bcrypt"`
- [ ] 下次登录时密码哈希变为 bcrypt（长度 60）

---

## 📞 紧急联系

遇到问题？
1. 查看日志：`docker logs projectionroom-app` 或 `journalctl -u projectionroom.service -f`
2. 检查数据库：`psql -h localhost -U pr_app -W projectionroom`
3. 文档：本项目 `MIGRATION_GUIDE.md` 有更详细的故障排查

---

**版本**: v1.1  
**日期**: 2026-10-19
