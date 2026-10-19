# GitHub Actions Token 设置指南

## 🎯 用途
用于 GitHub Actions 将 Docker 镜像推送到 GHCR（GitHub Container Registry）。

---

## 📋 操作步骤

### 1️⃣ 创建 Personal Access Token (PAT)

1. **访问**: https://github.com/settings/tokens/new  
   （或点击你的头像 → Settings → Developer settings → Personal access tokens → Tokens (classic)）

2. **填写表单**:
   - **Note**: `GHCR Push for ProjectionRoom`
   - **Expiration**: `No expiration` (建议至少 30 天)
   - **Select scopes**: 
     - ✅ `read:package`（关键：用于推送到 GHCR）

3. **点击 "Generate token"**
   - ⚠️ **立刻复制 Token**（只展示一次！保存到你认为安全的地方，如密码管理器）

---

### 2️⃣ 在仓库添加 Secret

1. **访问**: https://github.com/Drayee/ProjectionRoom/settings/secrets/actions
2. **点击 "New repository secret"**
3. **填写**:
   - **Name**: `GITHUB_REGISTRY_TOKEN`
   - **Secret**: `<刚才复制的 Token>`
4. **点击 "Add secret"**

---

### 3️⃣ 验证配置

```bash
# 提交并推送代码到主分支
git add .
git commit -m "chore: configure GHCR token for CI/CD"
git push origin main
```

---

## ✅ 完成检查

成功后会看到：
1. 新 Commit 旁边出现绿色对勾 ✓
2. URL: `https://github.com/Drayee/ProjectionRoom/actions`
3. 最新运行状态：`Success`
4. 日志中显示：
   ```
   Login to GitHub Container Registry...
   Build and push Docker image
   Push to ghcr.io/drayee/projectionroom:<sha>
   ```

---

## 🔒 安全建议

- 不要将 Token 直接写在代码里
- 定期轮换 Token（建议每 90 天）
- 如果怀疑泄露，立即删除并重做新 Token

---

## 🆘 故障排查

### 问题 1: Action 失败 "Unauthorized"

**原因**: Token 权限不足或已过期  
**解决**: 
1. 确认添加了 `read:package` 权限
2. 检查 Token 是否过期

### 问题 2: Action 卡住不启动

**原因**: 需要手动触发或分支规则未匹配  
**解决**:
1. 去 Actions 页面 → 选择该 Workflow → 右侧 "Run workflow"
2. 确保推送到的是 `main` 分支

### 问题 3: 镜像没出现在 GHCR 页面

**等待时间**: 构建 + 推送通常需要 5~10 分钟  
**检查方法**: 
1. https://github.com/orgs/Drayee/packages?ecosystem=docker&page=1&filter=version&q=&repo_name=projectionroom
2. 应该能看到最新版本

---

**版本**: v1.0  
**日期**: 2026-10-19
