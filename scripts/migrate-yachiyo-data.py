#!/usr/bin/env python3
"""
yachiyo → ProjectionRoom 数据迁移工具

功能:
1. 从旧数据库 yachiyo 导出数据
2. 映射并导入到新 schema (ProjectionRoom)
3. 生成迁移报告与验证 SQL

使用方法:
    python scripts/migrate-yachiyo-data.py --local-only
    # 或
    
    python scripts/migrate-yachiyo-data.py --server 
    # 通过 SSH 远程执行

注意事项:
- users: password_hash 需要 bcrypt 格式验证
- posts: is_approved → content 字段标记
- wallet: 不迁移 (ProjectionRoom 无钱包系统)
"""

import psycopg2
from psycopg2 import sql, extras
import json
import hashlib
import datetime
from pathlib import Path
import argparse
import sys

# ==================== 配置 ====================

# 旧数据库 (本地非 Docker)
OLD_DB_CONFIG = {
    'host': 'localhost',
    'database': 'yachiyo',
    'user': 'postgres',
    'password': 'yuanshen123',
    'port': 5432
}

# 新数据库 (服务器 PostgreSQL)
NEW_DB_CONFIG = {
    'host': 'localhost',
    'database': 'projectionroom',
    'user': 'pr_app',
    'password': 'woOzWJFQn4Ilp93GSLkjYc75PfBt',
    'port': 5432
}

# 映射规则
MAPPING_RULES = {
    'users': {
        'source_table': 'users',
        'target_table': 'users',
        'field_map': {
            'user_id': 'id',
            'name': 'display_name',  # username 未明确，从 name 派生
            'email': 'email',
            'role': 'role',
            'password': 'password_hash',
            'create_time': 'created_at'
        },
        'constraints': [
            'username = lower(username)',
            'role IN (\'user\', \'admin\')',
            'status IN (\'active\', \'banned\')'
        ],
        'notes': [
            '⚠️ username 需要从 name 派生或设为 NULL 待补全',
            '⚠️ create_time 是 TIME 类型需转换为 timestamptz'
        ]
    },
    'posts': {
        'source_table': 'posting',
        'target_table': 'posts',
        'field_map': {
            'id': 'id',
            'user_id': 'author_id',
            'title': 'title',
            'content': 'content',
            'score': 'like_count'  # 粗略映射
        },
        'constraints': [
            'title NOT NULL AND length(title) <= 60',
            'content NOT NULL'
        ],
        'notes': ['is_approved → 可能需要在 content 中标记']
    }
}

# ==================== 核心函数 ====================

def connect_db(config, db_name='connect'):
    """连接数据库"""
    try:
        conn = psycopg2.connect(**config)
        print(f"✅ [{db_name}] 成功连接到数据库")
        return conn
    except Exception as e:
        print(f"❌ [{db_name}] 连接失败：{e}")
        sys.exit(1)

def test_old_connection():
    """测试旧数据库连接"""
    print("=" * 80)
    print("测试旧数据库连接 (yachiyo)")
    print("=" * 80)
    
    conn = connect_db(OLD_DB_CONFIG, "yachiyo")
    cursor = conn.cursor()
    
    # 获取表列表
    cursor.execute("""
        SELECT table_name 
        FROM information_schema.tables 
        WHERE table_schema = 'public' 
        ORDER BY table_name
    """)
    tables = [row[0] for row in cursor.fetchall()]
    
    print(f"\n[INFO] Found {len(tables)} tables:")
    for i, table in enumerate(tables, 1):
        # Get row count
        cursor.execute(f"SELECT COUNT(*) FROM {table}")
        count = cursor.fetchone()[0]
        print(f"  {i:2d}. {table:<30} -> {count:>8} rows")
    
    cursor.close()
    conn.close()
    return len(tables)

def extract_users(conn):
    """提取用户数据"""
    print("\n[STEP 1] Extracting user data")
    print("=" * 80)
    
    cursor = conn.cursor()
    cursor.execute("""
        SELECT user_id, name, email, role, password, create_time, update_time
        FROM users
        ORDER BY user_id
    """)
    
    users = []
    for row in cursor.fetchall():
        user_id, name, email, role, password, create_time, update_time = row
        
        # 转换 create_time (假设是时间戳字符串或 date)
        if isinstance(create_time, str):
            try:
                create_time = datetime.datetime.fromisoformat(create_time.replace('Z', '+00:00'))
            except:
                create_time = None
        
        # 检查 password 是否为 bcrypt 格式
        valid_password = len(password) == 60 and password.startswith('$2b$')
        
        users.append({
            'id': user_id,
            'name': name,
            'email': email,
            'role': role,
            'password_hash': password,
            'valid_password': valid_password,
            'created_at': create_time,
            'updated_at': update_time
        })
    
    cursor.close()
    
    print(f"\n👥 共提取 {len(users)} 个用户")
    valid_pw = sum(1 for u in users if u['valid_password'])
    print(f"✅ 有效 bcrypt 密码：{valid_pw}/{len(users)}")
    
    # 显示前 3 个示例
    print("\n示例数据 (前 3 行):")
    for user in users[:3]:
        print(f"  ID={user['id']}, Name={user['name']}, Email={user['email'] or 'N/A'}, Role={user['role']}")
        print(f"    Password valid: {user['valid_password']}")
    
    return users

def extract_posts(conn):
    """提取帖子数据"""
    print("\n" + "=" * 80)
    print("步骤 2: 提取帖子数据")
    print("=" * 80)
    
    cursor = conn.cursor()
    cursor.execute("""
        SELECT id, user_id, title, content, type, is_approved, score, create_time
        FROM posting
        ORDER BY id
    """)
    
    posts = []
    for row in cursor.fetchall():
        post_id, user_id, title, content, post_type, is_approved, score, create_time = row
        
        # 转换时间
        if isinstance(create_time, str):
            try:
                create_time = datetime.datetime.fromisoformat(create_time.replace('Z', '+00:00'))
            except:
                create_time = None
        
        posts.append({
            'id': post_id,
            'author_id': user_id,
            'title': title,
            'content': content,
            'post_type': post_type,
            'is_approved': bool(is_approved) if is_approved is not None else False,
            'score': int(score) if score else 0,
            'created_at': create_time
        })
    
    cursor.close()
    
    print(f"\n📝 共提取 {len(posts)} 篇帖子")
    approved = sum(1 for p in posts if p['is_approved'])
    print(f"已批准：{approved}/{len(posts)}")
    
    # 显示前 3 个示例
    print("\n示例数据 (前 3 行):")
    for post in posts[:3]:
        print(f"  ID={post['id']}, Title='{post['title']}', Author={post['author_id']}")
        print(f"    Type={post['post_type']}, Score={post['score']}, Approved={post['is_approved']}")
    
    return posts

def generate_migration_report(users, posts, output_path):
    """生成迁移报告"""
    report = {
        'generated_at': datetime.datetime.now().isoformat(),
        'summary': {
            'total_users': len(users),
            'valid_passwords': sum(1 for u in users if u['valid_password']),
            'total_posts': len(posts),
            'approved_posts': sum(1 for p in posts if p['is_approved']),
            'migration_status': 'ready'
        },
        'issues': [],
        'recommendations': []
    }
    
    # 检查问题
    for user in users:
        if not user['valid_password']:
            report['issues'].append(f"User {user['id']} has invalid password hash format")
        if user['email'] is not None and '@' not in user['email']:
            report['issues'].append(f"User {user['id']} has invalid email: {user['email']}")
    
    for post in posts:
        if not post['title'] or len(post['title']) > 60:
            report['issues'].append(f"Post {post['id']} has invalid title length")
        if not post['content']:
            report['issues'].append(f"Post {post['id']} has empty content")
    
    # 生成建议
    if report['issues']:
        report['recommendations'].append(f"Fix {len(report['issues'])} issues before migration")
        report['recommendations'].append("Consider running a dry-run on staging database first")
    
    # 保存报告
    Path(output_path).parent.mkdir(parents=True, exist_ok=True)
    with open(output_path, 'w', encoding='utf-8') as f:
        json.dump(report, f, indent=2, ensure_ascii=False)
    
    print(f"\n📋 迁移报告已保存到：{output_path}")
    return report

def generate_sql_statements(users, posts, output_file):
    """生成 INSERT 语句用于新数据库"""
    print("\n" + "=" * 80)
    print("步骤 3: 生成 INSERT 语句")
    print("=" * 80)
    
    lines = []
    lines.append("-- Generated by migrate-yachiyo-data.py")
    lines.append(f"-- Generated at: {datetime.datetime.now().isoformat()}")
    lines.append("")
    
    # Users
    lines.append("-- ============================================")
    lines.append("-- TABLE: users")
    lines.append("-- ============================================")
    lines.append("")
    
    insert_user_template = """
INSERT INTO users (id, username, display_name, email, role, password_hash, created_at, updated_at, last_seen_at)
VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s);
"""
    
    user_rows = []
    for u in users:
        # 注意：username 需要从 name 派生
        username = u['name'].lower() if u['name'] else f"user_{u['id']}"
        
        # 处理时间
        created_at = u['created_at'].isoformat() if u['created_at'] else 'NOW()'
        updated_at = u['updated_at'].isoformat() if u['updated_at'] else 'NOW()'
        last_seen = created_at  # 使用 create_time 作为 last_seen
        
        user_rows.append((
            u['id'],
            username,
            u['name'],
            u['email'],
            u['role'] or 'user',
            u['password_hash'],
            created_at,
            updated_at,
            last_seen
        ))
    
    print(f"准备插入 {len(user_rows)} 行用户数据...")
    
    # Posts  
    lines.append("")
    lines.append("-- ============================================")
    lines.append("-- TABLE: posts")
    lines.append("-- ============================================")
    lines.append("")
    
    insert_post_template = """
INSERT INTO posts (id, author_id, title, content, like_count, comment_count, created_at, updated_at)
VALUES (%s, %s, %s, %s, %s, %s, %s, %s);
"""
    
    post_rows = []
    for p in posts:
        post_rows.append((
            p['id'],
            p['author_id'],
            p['title'],
            p['content'],
            p['score'],
            0,  # comment_count 未知
            p['created_at'].isoformat() if p['created_at'] else 'NOW()',
            p['created_at'].isoformat() if p['created_at'] else 'NOW()'
        ))
    
    print(f"准备插入 {len(post_rows)} 行帖子数据...")
    
    # 写入 SQL 文件
    sql_file = Path(output_file)
    with open(sql_file, 'w', encoding='utf-8') as f:
        f.write('\n'.join(lines))
        f.write("# USER DATA START\n\n")
        
        # 生成参数化 INSERT 语句 (用 %s 占位符)
        for row in user_rows:
            f.write(insert_user_template.replace('%s', '%%s'))
            f.write(f"-- Values: {row}\n\n")
        
        f.write("# POST DATA START\n\n")
        
        for row in post_rows:
            f.write(insert_post_template.replace('%s', '%%s'))
            f.write(f"-- Values: {row}\n\n")
    
    print(f"\n✅ SQL 脚本已保存到：{sql_file}")
    return sql_file

def main():
    parser = argparse.ArgumentParser(description='yachiyo → ProjectionRoom 数据迁移工具')
    parser.add_argument('--test-old', action='store_true', help='仅测试旧数据库连接')
    parser.add_argument('--extract-users', action='store_true', help='仅提取用户数据')
    parser.add_argument('--extract-posts', action='store_true', help='仅提取帖子数据')
    parser.add_argument('--generate-report', action='store_true', help='生成迁移报告')
    parser.add_argument('--output-dir', default='reports', help='输出目录')
    
    args = parser.parse_args()
    
    print("=" * 80)
    print("[TOOL] yachiyo to ProjectionRoom Data Migration Tool")
    print("=" * 80)
    
    # 测试连接
    old_tables = test_old_connection()
    
    if args.test_old:
        print("\n✅ 测试完成!")
        return
    
    # 连接旧数据库
    conn = connect_db(OLD_DB_CONFIG, "yachiyo")
    
    # 提取数据
    if args.extract_users or not any([args.extract_posts, args.generate_report]):
        users = extract_users(conn)
    
    if args.extract_posts or not any([args.extract_users, args.generate_report]):
        posts = extract_posts(conn)
    
    # 生成报告和 SQL
    if args.generate_report or not any([args.extract_users, args.extract_posts]):
        report_path = f"{args.output_dir}/migration_report.json"
        sql_path = f"{args.output_dir}/migration.sql"
        
        if not 'users' in locals():
            users = extract_users(conn)
        if not 'posts' in locals():
            posts = extract_posts(conn)
        
        generate_migration_report(users, posts, report_path)
        generate_sql_statements(users, posts, sql_path)
    
    conn.close()
    print("\n✅ 迁移分析完成!")

if __name__ == '__main__':
    main()
