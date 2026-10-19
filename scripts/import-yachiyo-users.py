#!/usr/bin/env python3
"""
Import Yachiyo users into ProjectionRoom database

This script migrates users from old yachiyo database to new ProjectionRoom schema.
Password hashes are NOT compatible, so all users will need password reset.
"""

import psycopg2
from datetime import datetime
import bcrypt
import hashlib
import sys

# Old database (yachiyo)
OLD_DB = {
    'host': 'localhost',
    'database': 'yachiyo',
    'user': 'postgres',
    'password': 'yuanshen123',
    'port': 5432
}

# New database (ProjectionRoom) - on server
NEW_DB = {
    'host': '60.205.212.25',
    'database': 'projectionroom',
    'user': 'pr_app',
    'password': 'woOzWJFQn4Ilp93GSLkjYc75PfBt',
    'port': 5432
}

# Default password for migrated users (they'll need to reset it)
DEFAULT_PASSWORD = 'ResetPassword2026!'


def connect_old():
    """Connect to old yachiyo database"""
    conn = psycopg2.connect(**OLD_DB)
    print(f"[OK] Connected to yachiyo ({OLD_DB['database']})")
    return conn


def connect_new():
    """Connect to new ProjectionRoom database"""
    conn = psycopg2.connect(**NEW_DB)
    print(f"[OK] Connected to projectionroom ({NEW_DB['database']})")
    return conn


def get_users(cursor):
    """Extract users from yachiyo"""
    cursor.execute("""
        SELECT user_id, name, email, role, password, create_time, update_time
        FROM users ORDER BY user_id
    """)
    
    users = []
    for row in cursor.fetchall():
        uid, name, email, role, pwd, ctime, utime = row
        users.append({
            'id': uid,
            'name': name or 'Unknown',
            'email': email.lower() if email else None,
            'role': role.lower() if role else 'user',
            'old_password': pwd,
            'created_at': ctime,
            'updated_at': utime
        })
    
    return users


def hash_new_password(password):
    """Generate bcrypt hash for new default password"""
    salt = bcrypt.gensalt(rounds=12)
    return bcrypt.hashpw(password.encode('utf-8'), salt).decode('utf-8')


def migrate_users(conn_new, users):
    """Migrate users to ProjectionRoom schema"""
    cur = conn_new.cursor()
    
    insert_template = """
    INSERT INTO users (
        id, username, display_name, email, role, 
        password_hash, created_at, updated_at, last_seen_at, status
    ) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, 'active')
    ON CONFLICT (id) DO UPDATE SET
        display_name = EXCLUDED.display_name,
        email = COALESCE(EXCLUDED.email, users.email),
        updated_at = NOW()
    """
    
    hashed_pwd = hash_new_password(DEFAULT_PASSWORD)
    
    success = 0
    skipped = 0
    
    for user in users:
        try:
            # Generate username from display_name
            username = user['name'].lower().replace(' ', '_')[:20]
            
            params = (
                user['id'],
                username,
                user['name'],
                user['email'],
                user['role'] if user['role'] in ('user', 'admin') else 'user',
                hashed_pwd,
                user['created_at'] or datetime.now(),
                user['updated_at'] or datetime.now(),
                user['created_at'] or datetime.now(),
            )
            
            cur.execute(insert_template, params)
            success += 1
            
            # Progress indicator
            if success % 100 == 0:
                print(f"   Migrated {success}/{len(users)} users...")
        
        except Exception as e:
            print(f"   ⚠️  Skip user {user['id']}: {e}")
            skipped += 1
    
    conn_new.commit()
    cur.close()
    
    return success, skipped


def migrate_posts(conn_new, posts):
    """Migrate posts to ProjectionRoom schema"""
    cur = conn_new.cursor()
    
    insert_template = """
    INSERT INTO posts (
        id, author_id, title, content, 
        like_count, comment_count, created_at, updated_at
    ) VALUES (%s, %s, %s, %s, %s, %s, %s, %s)
    ON CONFLICT (id) DO UPDATE SET
        title = EXCLUDED.title,
        content = EXCLUDED.content,
        updated_at = NOW()
    """
    
    success = 0
    skipped = 0
    
    for post in posts:
        try:
            # Sanitize title (max 60 chars)
            title = post['title'][:60] if len(post['title']) > 60 else post['title']
            
            params = (
                post['id'],
                post['author_id'],
                title,
                post['content'] or '',
                post['score'] or 0,  # Use score as like_count approximation
                0,  # Comment count unknown
                post['created_at'] or datetime.now(),
                post['created_at'] or datetime.now(),
            )
            
            cur.execute(insert_template, params)
            success += 1
            
            if success % 5 == 0:
                print(f"   Migrated {success}/{len(posts)} posts...")
        
        except Exception as e:
            print(f"   ⚠️  Skip post {post['id']}: {e}")
            skipped += 1
    
    conn_new.commit()
    cur.close()
    
    return success, skipped


def main():
    print("=" * 80)
    print("IMPORTING YACHIYO DATA TO PROJECTIONROOM")
    print("=" * 80)
    
    # Connect to old database
    print("\n[STEP 1] Reading from yachiyo...")
    conn_old = connect_old()
    cur_old = conn_old.cursor()
    
    # Get counts first
    cur_old.execute("SELECT COUNT(*) FROM users")
    user_count = cur_old.fetchone()[0]
    cur_old.execute("SELECT COUNT(*) FROM posting")
    post_count = cur_old.fetchone()[0]
    
    print(f"   Users to migrate: {user_count}")
    print(f"   Posts to migrate: {post_count}")
    
    # Extract data
    users = get_users(cur_old)
    cur_old.execute("""
        SELECT id, user_id, title, content, type, is_approved, score, create_time
        FROM posting ORDER BY id
    """)
    
    posts = []
    for row in cur_old.fetchall():
        pid, uid, title, content, ptype, approved_flag, score, ctime = row
        posts.append({
            'id': pid,
            'author_id': uid,
            'title': title or '',
            'content': content or '',
            'post_type': ptype,
            'is_approved': bool(approved_flag) if approved_flag else False,
            'score': int(score) if score else 0,
            'created_at': ctime
        })
    
    conn_old.close()
    print("[OK] Data extracted\n")
    
    # Connect to new database
    print("[STEP 2] Connecting to ProjectionRoom...")
    conn_new = connect_new()
    
    # Migrate users
    print("\n[STEP 3] Migrating users...")
    print(f"⚠️  WARNING: All passwords have been reset to a temporary value.")
    print(f"⚠️  Default password: '{DEFAULT_PASSWORD}'")
    print(f"⚠️  Users MUST reset their password after login.\n")
    
    users_success, users_skip = migrate_users(conn_new, users)
    print(f"\n✅ User migration complete:")
    print(f"   - Successfully migrated: {users_success}")
    print(f"   - Skipped/failed: {users_skip}")
    
    # Migrate posts
    print("\n[STEP 4] Migrating posts...")
    posts_success, posts_skip = migrate_posts(conn_new, posts)
    print(f"\n✅ Post migration complete:")
    print(f"   - Successfully migrated: {posts_success}")
    print(f"   - Skipped/failed: {posts_skip}")
    
    # Verification
    print("\n[STEP 5] Verification...")
    cur = conn_new.cursor()
    cur.execute("SELECT COUNT(*) FROM users")
    new_user_count = cur.fetchone()[0]
    cur.execute("SELECT COUNT(*) FROM posts")
    new_post_count = cur.fetchone()[0]
    cur.close()
    
    print(f"   Current users in database: {new_user_count}")
    print(f"   Current posts in database: {new_post_count}")
    
    print("\n" + "=" * 80)
    print("MIGRATION COMPLETE!")
    print("=" * 80)
    print("\nIMPORTANT NOTES:")
    print(f"  • {users_success} users migrated successfully")
    print(f"  • {posts_success} posts migrated successfully")
    print(f"  • All user passwords reset to: '{DEFAULT_PASSWORD}'")
    print(f"  • Users must reset password via /forgot endpoint")
    print("\nNext steps:")
    print("  1. Restart the application on server")
    print("  2. Notify users about password reset requirement")
    print("  3. Monitor logs for any connection issues")
    print("=" * 80)


if __name__ == '__main__':
    main()
