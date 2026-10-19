#!/usr/bin/env python3
"""
yachiyo -> ProjectionRoom Data Migration Tool

Usage:
    python scripts/migrate-yachiyo.py --test-old
    python scripts/migrate-yachiyo.py --extract-users
    python scripts/migrate-yachiyo.py --extract-posts
    python scripts/migrate-yachiyo.py --generate-report --output-dir reports
"""

import psycopg2
from datetime import datetime
import json
from pathlib import Path
import argparse
import sys

OLD_DB = {'host': 'localhost', 'database': 'yachiyo', 'user': 'postgres', 
          'password': 'yuanshen123', 'port': 5432}
NEW_DB = {'host': 'localhost', 'database': 'projectionroom', 'user': 'pr_app',
          'password': 'woOzWJFQn4Ilp93GSLkjYc75PfBt', 'port': 5432}

def connect(config, name='db'):
    try:
        conn = psycopg2.connect(**config)
        print(f"[OK] Connected to {name}")
        return conn
    except Exception as e:
        print(f"[ERROR] Failed to connect {name}: {e}")
        sys.exit(1)

def test_old_db():
    print("=" * 80)
    print("TESTING OLD DATABASE CONNECTION (yachiyo)")
    print("=" * 80)
    
    conn = connect(OLD_DB, "yachiyo")
    cur = conn.cursor()
    
    cur.execute("""
        SELECT table_name, (SELECT COUNT(*) FROM information_schema.columns 
         WHERE table_name=t.table_name) as col_count
        FROM (SELECT DISTINCT table_name FROM information_schema.tables 
              WHERE table_schema='public') t
        ORDER BY table_name
    """)
    
    tables = [(row[0], row[1]) for row in cur.fetchall()]
    
    print(f"\nFound {len(tables)} tables:")
    for i, (table, cols) in enumerate(tables, 1):
        cur.execute(f"SELECT COUNT(*) FROM {table}")
        count = cur.fetchone()[0]
        print(f"  {i:2d}. {table:<30} [{cols} cols] -> {count:>8} rows")
    
    cur.close()
    conn.close()

def extract_users(conn):
    print("\nEXTRACTING USER DATA...")
    print("-" * 80)
    
    cur = conn.cursor()
    cur.execute("""
        SELECT user_id, name, email, role, password, create_time, update_time
        FROM users ORDER BY user_id
    """)
    
    users = []
    valid_pw = 0
    
    for row in cur.fetchall():
        uid, name, email, role, pwd, ctime, utime = row
        
        is_valid = len(pwd) == 60 and pwd.startswith('$2b$') if pwd else False
        if is_valid:
            valid_pw += 1
        
        # Parse timestamp
        if isinstance(ctime, str):
            try:
                ctime = datetime.fromisoformat(ctime.replace('Z', '+00:00'))
            except:
                ctime = None
        
        users.append({
            'id': uid, 'name': name, 'email': email, 'role': role or 'user',
            'password': pwd, 'valid_password': is_valid,
            'created_at': ctime, 'updated_at': utime
        })
    
    cur.close()
    
    print(f"Total users: {len(users)}")
    print(f"Valid bcrypt passwords: {valid_pw}/{len(users)}")
    
    # Sample
    print("\nSample data (first 3):")
    for u in users[:3]:
        print(f"  ID={u['id']}, Name={u['name']}, Email={u['email'] or 'N/A'}, Role={u['role']}")
        print(f"    Password valid: {u['valid_password']}")
    
    return users

def extract_posts(conn):
    print("\nEXTRACTING POST DATA...")
    print("-" * 80)
    
    cur = conn.cursor()
    cur.execute("""
        SELECT id, user_id, title, content, type, is_approved, score, create_time
        FROM posting ORDER BY id
    """)
    
    posts = []
    approved = 0
    
    for row in cur.fetchall():
        pid, uid, title, content, ptype, approved_flag, score, ctime = row
        
        if isinstance(ctime, str):
            try:
                ctime = datetime.fromisoformat(ctime.replace('Z', '+00:00'))
            except:
                ctime = None
        
        is_app = bool(approved_flag) if approved_flag is not None else False
        if is_app:
            approved += 1
        
        posts.append({
            'id': pid, 'author_id': uid, 'title': title, 'content': content,
            'post_type': ptype, 'is_approved': is_app, 'score': int(score) if score else 0,
            'created_at': ctime
        })
    
    cur.close()
    
    print(f"Total posts: {len(posts)}")
    print(f"Approved: {approved}/{len(posts)}")
    
    print("\nSample data (first 3):")
    for p in posts[:3]:
        print(f"  ID={p['id']}, Title='{p['title']}', Author={p['author_id']}")
        print(f"    Type={p['post_type']}, Score={p['score']}, Approved={p['is_approved']}")
    
    return posts

def generate_report(users, posts, output_dir):
    print("\nGENERATING MIGRATION REPORT...")
    print("-" * 80)
    
    report = {
        'generated_at': datetime.now().isoformat(),
        'summary': {
            'total_users': len(users),
            'valid_passwords': sum(1 for u in users if u['valid_password']),
            'total_posts': len(posts),
            'approved_posts': sum(1 for p in posts if p['is_approved']),
            'status': 'ready'
        },
        'issues': [],
        'recommendations': []
    }
    
    # Check issues
    for u in users:
        if not u['valid_password']:
            report['issues'].append(f"User {u['id']}: invalid password hash")
        if u['email'] and '@' not in u['email']:
            report['issues'].append(f"User {u['id']}: invalid email '{u['email']}'")
    
    for p in posts:
        if not p['title'] or len(p['title']) > 60:
            report['issues'].append(f"Post {p['id']}: invalid title length")
        if not p['content']:
            report['issues'].append(f"Post {p['id']}: empty content")
    
    if report['issues']:
        report['recommendations'] = [
            f"Fix {len(report['issues'])} issues before migration",
            "Run dry-run on staging database first"
        ]
    
    # Save
    path = Path(output_dir) / 'migration_report.json'
    path.parent.mkdir(parents=True, exist_ok=True)
    
    with open(path, 'w', encoding='utf-8') as f:
        json.dump(report, f, indent=2, ensure_ascii=False)
    
    print(f"Report saved: {path}")
    return report

def main():
    parser = argparse.ArgumentParser(description='yachiyo to ProjectionRoom migration tool')
    parser.add_argument('--test-old', action='store_true', help='Test old DB connection')
    parser.add_argument('--extract-users', action='store_true', help='Extract only users')
    parser.add_argument('--extract-posts', action='store_true', help='Extract only posts')
    parser.add_argument('--generate-report', action='store_true', help='Generate full report')
    parser.add_argument('--output-dir', default='reports', help='Output directory')
    
    args = parser.parse_args()
    
    if args.test_old:
        test_old_db()
        return
    
    conn = connect(OLD_DB, "yachiyo")
    
    users = []
    posts = []
    
    if args.extract_users or (not args.extract_posts and not args.generate_report):
        users = extract_users(conn)
    
    if args.extract_posts or (not args.extract_users and not args.generate_report):
        posts = extract_posts(conn)
    
    if args.generate_report or (not args.extract_users and not args.extract_posts):
        if not users:
            users = extract_users(conn)
        if not posts:
            posts = extract_posts(conn)
        
        generate_report(users, posts, args.output_dir)
    
    conn.close()
    print("\n[MIGRATION ANALYSIS COMPLETE]")

if __name__ == '__main__':
    main()
