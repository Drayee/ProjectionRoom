#!/usr/bin/env python3
"""
导入旧项目 yachiyo 的用户和帖子数据到 ProjectionRoom。

⚠️ 核心逻辑：
1. 密码是 MD5 → 直接写入 projectionroom.users.password_hash，Login 代码会识别并升级
2. 用户名可以是 Unicode → Login 查询直接用 name，不强制 ASCII 限制（仅注册才限制）
3. 帖子内容可能包含特殊字符 → 用 psycopg2 的参数化 INSERT

前提条件：
- 本地/远程 yachiyo DB 可访问
- 本地/远程 projectionroom DB 已创建且运行新代码版本（含 MD5 兼容层）
- 已安装 psycopg2: pip install psycopg2-binary

用法：
    # 本地 → 本地测试
    python scripts/import_yachiyo.py --mode local
    
    # 本地 → 服务器（生产环境）
    python scripts/import_yachiyo.py --host 60.205.212.25 \
        --yachiyo-db yachiyo --proj-db projectionroom --user pr_app
        
    # 只导入用户
    python scripts/import_yachiyo.py --import-users
    
    # 只导入帖子
    python scripts/import_yachiyo.py --import-posts
    
    # 全部导入
    python scripts/import_yachiyo.py --full
"""

import sys
from pathlib import Path
from datetime import datetime
from argparse import ArgumentParser

# --- 配置 ---
LOCAL_YACHIYO = {
    'host': 'localhost',
    'database': 'yachiyo',
    'user': 'postgres',
    'password': 'yuanshen123',
    'port': 5432,
}

SERVER_PROJ_ROOM = {
    'host': '60.205.212.25',
    'database': 'projectionroom',
    'user': 'pr_app',
    'password': 'woOzWJFQn4Ilp93GSLkjYc75PfBt',  # ⚠️ 生产环境请用环境变量
    'port': 5432,
}

# --- 依赖导入 ---
try:
    import psycopg2
except ImportError:
    print("❌ 需要安装 psycopg2")
    print("   pip install psycopg2-binary")
    sys.exit(1)

# === 核心函数 ===

def connect_db(config, name='db'):
    """连接数据库"""
    try:
        conn = psycopg2.connect(**config)
        print(f"[✅] 已连接到{name}: {config['database']}@{config['host']}")
        return conn
    except Exception as e:
        print(f"❌ [{name}] 连接失败: {e}")
        sys.exit(1)


def get_user_data(conn):
    """获取用户数据"""
    cur = conn.cursor()
    cur.execute("""
        SELECT user_id, name, email, role, password, create_time, update_time
        FROM users ORDER BY user_id
    """)
    
    users = []
    for row in cur.fetchall():
        uid, name, email, role, pwd, ctime, utime = row
        users.append({
            'id': uid,
            'username': name.lower().replace(' ', '_')[:20],  # 尝试生成 username（如果中文则保持原样）
            'display_name': name or 'Unknown',
            'email': email.lower() if email else None,
            'role': role.lower() if role in ('user', 'admin', 'normal', 'member') else 'user',
            'password_hash': pwd or '',  # ⚠️ MD5 hash - 不要在这里转换！
            'created_at': ctime,
            'updated_at': utime,
        })
    
    cur.close()
    return users


def get_post_data(conn):
    """获取帖子数据"""
    cur = conn.cursor()
    cur.execute("""
        SELECT id, user_id, title, content, type, is_approved, score, create_time
        FROM posting ORDER BY id
    """)
    
    posts = []
    for row in cur.fetchall():
        pid, uid, title, content, ptype, approved, score, ctime = row
        posts.append({
            'id': pid,
            'author_id': uid,
            'title': (title or '')[:60],  # 限制为 60 字符
            'content': content or '',
            'like_count': int(score) if score else 0,
            'comment_count': 0,
            'created_at': ctime,
            'updated_at': ctime,
        })
    
    cur.close()
    return posts


def import_users(users, proj_conn):
    """导入用户到 projectionroom"""
    cur = proj_conn.cursor()
    
    insert_template = """
    INSERT INTO users (
        id, username, display_name, email, role, 
        password_hash, created_at, updated_at, last_seen_at
    ) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s)
    ON CONFLICT (id) DO UPDATE SET
        display_name = EXCLUDED.display_name,
        email = COALESCE(EXCLUDED.email, users.email),
        updated_at = NOW(),
        password_hash = CASE 
            WHEN users.password_hash LIKE '$2%' THEN users.password_hash  -- bcrypt 已有，跳过
            ELSE EXCLUDED.password_hash  -- 保留 MD5，等登录时升级
        END
    """
    
    success = 0
    skipped = 0
    md5_count = 0
    
    for user in users:
        try:
            params = (
                user['id'],
                user['username'],
                user['display_name'],
                user['email'],
                user['role'] if user['role'] in ('user', 'admin') else 'user',
                user['password_hash'],
                user['created_at'] or datetime.now(),
                user['updated_at'] or datetime.now(),
                user['created_at'] or datetime.now(),  # last_seen_at 设为 create_time
            )
            
            cur.execute(insert_template, params)
            
            # 统计 MD5 数量
            if user['password_hash'] and len(user['password_hash']) == 32:
                md5_count += 1
            
            success += 1
            if success % 100 == 0:
                print(f"   导入 {success}/{len(users)} 用户...")
        
        except Exception as e:
            print(f"   ⚠️  跳过用户 {user['id']}: {e}")
            skipped += 1
    
    proj_conn.commit()
    cur.close()
    
    print(f"\n[✅] 用户导入完成:")
    print(f"   • 成功：{success}")
    print(f"   • 跳过/失败：{skipped}")
    print(f"   • 其中 MD5 哈希：{md5_count}")
    
    return success, skipped, md5_count


def import_posts(posts, proj_conn):
    """导入帖子到 projectionroom"""
    cur = proj_conn.cursor()
    
    insert_template = """
    INSERT INTO posts (
        id, author_id, title, content, like_count, comment_count,
        created_at, updated_at
    ) VALUES (%s, %s, %s, %s, %s, %s, %s, %s)
    ON CONFLICT (id) DO UPDATE SET
        title = EXCLUDED.title,
        content = EXCLUDED.content,
        like_count = EXCLUDED.like_count,
        updated_at = NOW()
    """
    
    success = 0
    skipped = 0
    
    for post in posts:
        try:
            params = (
                post['id'],
                post['author_id'],
                post['title'],
                post['content'],
                post['like_count'],
                post['comment_count'],
                post['created_at'] or datetime.now(),
                post['created_at'] or datetime.now(),
            )
            
            cur.execute(insert_template, params)
            
            success += 1
            if success % 5 == 0:
                print(f"   导入 {success}/{len(posts)} 帖子...")
        
        except Exception as e:
            print(f"   ⚠️  跳过帖子 {post['id']}: {e}")
            skipped += 1
    
    proj_conn.commit()
    cur.close()
    
    print(f"\n[✅] 帖子导入完成:")
    print(f"   • 成功：{success}")
    print(f"   • 跳过/失败：{skipped}")
    
    return success, skipped


def main():
    parser = ArgumentParser(description='导入 yachiyo 数据到 ProjectionRoom')
    parser.add_argument('--mode', choices=['local', 'server'], default='local',
                        help='目标模式：本地测试或服务器部署')
    parser.add_argument('--import-users', action='store_true',
                        help='只导入用户数据')
    parser.add_argument('--import-posts', action='store_true',
                        help='只导入帖子数据')
    parser.add_argument('--full', action='store_true',
                        help='导入所有数据')
    parser.add_argument('--host', help='目标服务器 IP（可选，默认 use local）')
    
    args = parser.parse_args()
    
    print("=" * 80)
    print("📊 YACHIYO → PROJECTIONROOM 数据导入工具")
    print("=" * 80)
    
    if not (args.import_users or args.import_posts or args.full):
        print("\n❌ 错误：必须指定 --import-users、--import-posts 或 --full")
        parser.print_help()
        sys.exit(1)
    
    # 决定是否导入哪些数据
    should_import_users = args.import_users or args.full
    should_import_posts = args.import_posts or args.full
    
    # 连接源数据库
    print("\n[步骤 1] 连接 yachiyo 数据库...")
    src_conn = connect_db(LOCAL_YACHIVO)
    
    # 提取数据
    users = []
    posts = []
    
    if should_import_users:
        print("[步骤 2a] 提取用户数据...")
        users = get_user_data(src_conn)
        print(f"   共 {len(users)} 个用户")
    
    if should_import_posts:
        print("[步骤 2b] 提取帖子数据...")
        posts = get_post_data(src_conn)
        print(f"   共 {len(posts)} 篇帖子")
    
    src_conn.close()
    print("[OK] 源数据提取完成\n")
    
    # 连接目标数据库
    target_config = SERVER_PROJ_ROOM if args.mode == 'server' else LOCAL_YACHIYO.copy()
    target_config.update({'database': 'projectionroom'})
    if args.host:
        target_config['host'] = args.host
    
    print("[步骤 3] 连接 ProjectionRoom 数据库...")
    proj_conn = connect_db(target_config, "projectionroom")
    
    # 导入数据
    users_done = False
    posts_done = False
    
    if should_import_users:
        print("\n[步骤 4a] 导入用户...")
        _, _, md5_count = import_users(users, proj_conn)
        users_done = True
        
        print("\n⚠️  重要提示:")
        print(f"   • {md5_count} 个用户的密码仍以 MD5 格式存储")
        print("   • 首次登录成功后，系统会自动升级到 bcrypt")
        print("   • 如需强制重置密码，可使用 /api/auth/forgot 端点\n")
    
    if should_import_posts:
        print("\n[步骤 4b] 导入帖子...")
        import_posts(posts, proj_conn)
        posts_done = True
    
    proj_conn.close()
    
    # 总结
    print("\n" + "=" * 80)
    print("🎉 导入流程结束")
    print("=" * 80)
    print(f"• 用户：{'✅' if users_done else '⏭'}")
    print(f"• 帖子：{'✅' if posts_done else '⏭'}")
    print("\n下一步:")
    print("  1. 重启应用容器（如果需要）：docker compose restart app")
    print("  2. 验证登录：测试任意一个旧用户的密码是否能正常登录")
    print("  3. 检查日志：观察是否有 'legacy password upgraded to bcrypt' 记录")
    print("=" * 80)


if __name__ == '__main__':
    main()
