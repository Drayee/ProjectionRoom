#!/bin/bash
# 迁移脚本：将 yachiyo 旧项目的用户与帖子数据导入 ProjectionRoom 新库
#
# 前提：
# 1. 服务器已安装 Docker + Docker Compose
# 2. 投影室容器正在运行（或准备好启动）
# 3. 本地数据库 yachiyo 可访问
#
# 用法：
#   ./scripts/migrate-yachiyo-data.sh --import-users
#   ./scripts/migrate-yachiyo-data.sh --import-posts
#   ./scripts/migrate-yachiyo-data.sh --full      # 全部导入

set -e

# --- 配置 ---
YACHIYO_HOST="localhost"
YACHIYO_PORT="5432"
YACHIVO_USER="postgres"
YACHIYO_PASS="yuanshen123"
YACHIYO_DB="yachiyo"

PROJ_ROOM_HOST="60.205.212.25"  # 或 "localhost" if running local test
PROJ_ROOM_PORT="5432"
PROJ_ROOM_USER="pr_app"
PROJ_ROOM_PASS="woOzWJFQn4Ilp93GSLkjYc75PfBt"
PROJ_ROOM_DB="projectionroom"

IMPORT_USERS=false
IMPORT_POSTS=false

# --- 帮助 ---
usage() {
    echo "Usage: $0 [options]"
    echo "Options:"
    echo "  --import-users    仅导入用户数据"
    echo "  --import-posts    仅导入帖子数据"
    echo "  --full            导入所有数据"
    echo "  --help            显示此帮助"
    exit 0
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --import-users)
            IMPORT_USERS=true
            shift
            ;;
        --import-posts)
            IMPORT_POSTS=true
            shift
            ;;
        --full)
            IMPORT_USERS=true
            IMPORT_POSTS=true
            shift
            ;;
        --help)
            usage
            ;;
        *)
            echo "Unknown option: $1"
            usage
            ;;
    esac
done

# 默认：只导入不指定则什么都不做
if ! $IMPORT_USERS && ! $IMPORT_POSTS; then
    echo "Error: No data to import. Use --import-users, --import-posts or --full"
    exit 1
fi

# --- 导出 yachiyo 数据 ---
echo "=========================================="
echo "步骤 1: 从 yachiyo 导出数据"
echo "=========================================="

if $IMPORT_USERS; then
    echo "导出 users 表..."
    PGPASSWORD="$YACHIYO_PASS" /c/Program\ Files/PostgreSQL/18/bin/pg_dump \
        -h "$YACHIYO_HOST" -p "$YACHIYO_PORT" -U "$YACHIYO_USER" \
        -d "$YACHIYO_DB" \
        --data-only \
        --column-inserts \
        -t users \
        -f /tmp/yachiyo_users.sql
fi

if $IMPORT_POSTS; then
    echo "导出 posting 表..."
    PGPASSWORD="$YACHIYO_PASS" /c/Program\ Files/PostgreSQL/18/bin/pg_dump \
        -h "$YACHIYO_HOST" -p "$YACHIYO_PORT" -U "$YACHIYO_USER" \
        -d "$YACHIYO_DB" \
        --data-only \
        --column-inserts \
        -t posting \
        -f /tmp/yachiyo_posts.sql
fi

# --- 转换 SQL ---
# ⚠️ 核心问题：密码是 MD5，不是 bcrypt → 直接写入会破坏 bcrypt 校验逻辑
# ✅ 解决方案：修改 INSERT 语句，让哈希为 MD5，Login 代码会自动识别并升级

if $IMPORT_USERS; then
    echo ""
    echo "步骤 2: 转换 SQL 格式（适配 projectionroom schema）"
    echo "============================================================"
    
    # users 表映射：
    # yachiyo: user_id, name, email, role, password (MD5), create_time, update_time
    # projectionroom: id, username, display_name, email, role, password_hash, created_at, updated_at
    
    python3 << 'PYTHON_SCRIPT'
import sys

def main():
    input_file = "/tmp/yachiyo_users.sql"
    output_file = "/tmp/yachiyo_users_transformed.sql"
    
    with open(input_file, 'r', encoding='utf-8') as f_in:
        lines = f_in.readlines()
    
    with open(output_file, 'w', encoding='utf-8') as f_out:
        for line in lines:
            # 跳过 COMMENT 行和 SET 语句
            if 'COMMENT ON' in line or 'SET statement_timeout' in line:
                continue
            
            # 处理 INSERT INTO public.users ...
            if 'INSERT INTO public.users' in line:
                # 重写列名以匹配 new schema
                f_out.write('INSERT INTO users (id, username, display_name, email, role, password_hash, created_at, updated_at) VALUES ')
                
                # 替换 VALUES 部分
                vals_part = line.split('VALUES ')[1].rstrip().strip(');').strip(';()')
                f_out.write(f'({vals_part});\n')
            
            # 保留其他行的原始内容（如注释、分隔符）
            else:
                # 清理行首尾的多余空格
                clean_line = line.strip()
                if clean_line and not clean_line.startswith('COMMENT'):
                    f_out.write(line)
        
        f_out.write("\n-- Migration complete. Users are now stored with legacy MD5 hashes.\n")
        f_out.write("-- The login path will automatically upgrade them to bcrypt on next successful login.\n")
    
    print(f"✓ 转换完成：{output_file}")

if __name__ == '__main__':
    main()
PYTHON_SCRIPT

fi

# --- 导入到 projectionroom ---
echo ""
echo "步骤 3: 导入到 ProjectionRoom 数据库"
echo "============================================================"

if $IMPORT_USERS; then
    echo "导入 users 数据..."
    
    # 方式 A: 如果 projectionroom DB 已经在同一台服务器上运行（本机测试）
    # PGPASSWORD="$PROJ_ROOM_PASS" psql -h "$PROJ_ROOM_HOST" -p "$PROJ_ROOM_PORT" -U "$PROJ_ROOM_USER" -d "$PROJ_ROOM_DB" -f /tmp/yachiyo_users_transformed.sql
    
    # 方式 B: 通过 SSH 远程执行（生产环境）
    echo "⚠️  准备通过 SSH 执行导入（需要配置 SSH key 或使用 sshpass）..."
    echo ""
    echo "命令如下："
    echo "sshpass -p '$ROOT_PASSWORD' ssh root@$PROJ_ROOM_HOST \"psql -h localhost -U pr_app -d projectionroom -W$PROJ_ROOM_PASS -f <(cat /tmp/yachiyo_users_transformed.sql)\""
    
    echo ""
    echo "或者，你可以手动下载文件并通过 pgAdmin/DBeaver 导入："/tmp/yachiyo_users_transformed.sql
fi

if $IMPORT_POSTS; then
    echo "导入 posting 数据..."
    # TODO: similar transformation for posts table
    echo "TODO: Add post migration logic"
fi

echo ""
echo "============================================================"
echo "迁移流程结束"
echo "============================================================"
echo ""
echo "下一步操作："
echo "1. 检查导入结果：psql -h localhost -U pr_app -d projectionroom -c 'SELECT COUNT(*) FROM users'"
echo "2. 验证登录：使用旧项目口令测试登录（应自动升级为 bcrypt）"
echo "3. 监控日志：查看是否有迁移升级成功的日志记录"
echo ""
