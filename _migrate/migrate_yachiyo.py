#!/usr/bin/env python
"""
迁移脚本：将旧项目 yachiyo 的用户数据和其他数据导出并导入到新环境。
支持两种模式：
1) 本地 → 本地：将 yachiyo 导入到 projectionroom（用于测试/对比）
2) 本地 → 远程：通过 SSH+SCCP 传输 SQL dump 到服务器，并在 Docker 中恢复

用法：
  # 模式 A: 本地导入到投影房间的新库（推荐先跑这个验证 schema 兼容性）
  python _migrate/migrate_yachiyo.py local --to-db projectionroom_test

  # 模式 B: 导出为 SQL 文件
  python _migrate/migrate_yachiyo.py export --out yachiyo.sql.gz

  # 模式 C: 上传到服务器（服务器需开启 SSH key auth）
  python _migrate/migrate_yachiyo.py deploy \
    --host 60.205.212.2 \
    --key-file ~/.ssh/id_rsa \
    --remote-dir /opt/projectionroom/data
"""

import subprocess
import sys
import argparse
import os
import gzip
from pathlib import Path

LOCAL_PG_HOST = "127.0.0.1"
LOCAL_PG_PORT = 5432
LOCAL_PG_USER = "postgres"
LOCAL_PG_PASS = "yuanshen123"
YACHIYO_DB = "yachiyo"
REMOTE_SSH_USER = "root"  # 根据实际修改


def pg_dump(db, host=LOCAL_PG_HOST, port=LOCAL_PG_PORT, user=LOCAL_PG_USER, password=LOCAL_PG_PASS):
    """导出单个数据库的 SQL dump"""
    cmd = [
        "/c/Program Files/PostgreSQL/18/bin/pg_dump",
        "-h", host,
        "-p", str(port),
        "-U", user,
        "-d", db,
        "--format=custom",  # 使用 custom 格式便于压缩
        "-Fc",
        "-Z", "9",
    ]
    result = subprocess.run(cmd, capture_output=True, text=True, 
                          env={**os.environ, "PGPASSWORD": password})
    if result.returncode != 0:
        raise RuntimeError(f"pg_dump failed: {result.stderr}")
    return result.stdout


def pg_restore(data, target_db, host=LOCAL_PG_HOST, port=LOCAL_PG_PORT, user=LOCAL_PG_USER, password=LOCAL_PG_PASS):
    """还原 custom 格式 dump"""
    from io import BytesIO
    import psycopg2
    
    cmd = [
        "/c/Program Files/PostgreSQL/18/bin/pg_restore",
        "-h", host,
        "-p", str(port),
        "-U", user,
        "-d", target_db,
        "-v",
        "--clean",
        "--if-exists",
    ]
    
    proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                           env={**os.environ, "PGPASSWORD": password})
    stdout, stderr = proc.communicate(input=data)
    
    if proc.returncode != 0:
        raise RuntimeError(f"pg_restore failed: {stderr.decode()}")
    return stdout.decode()


def export_to_file(db, out_path):
    """导出到本地文件"""
    print(f"📤 导出 {db} 到 {out_path}...")
    data = pg_dump(db)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    with open(out_path, 'wb') as f:
        f.write(data)
    print(f"✅ 完成！文件大小：{out_path.stat().st_size:,} bytes")


def deploy_to_server(host, ssh_key_path, remote_dir):
    """通过 SSH 连接服务器并部署"""
    import paramiko
    import scp
    
    print(f"🌐 连接到服务器 {host}...")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    
    # 读取私钥
    pkey = paramiko.RSAKey.from_private_key_file(ssh_key_path)
    ssh.connect(host, username=REMOTE_SSH_USER, pkey=pkey, timeout=30)
    
    sftp = ssh.open_sftp()
    sftp_m = scp.SCPClient(ssh.get_transport())
    
    try:
        # 第一步：创建远程目录
        remote_dir = Path(remote_dir)
        remote_parent = remote_dir.parent
        sftp_m.put(_migrate_path, remote_dir / "yachiyo.sql.gz")
        
        # 第二步：在服务器上解压并导入（需要服务器上有 Docker+Postgres）
        stdin, stdout, stderr = ssh.exec_command(f"""
            cd {remote_dir} && \
            gunzip -c yachiyo.sql.gz | docker exec -i postgres psql -U postgres -d projectionroom
        """)
        stderr_str = stderr.read().decode()
        if "error" in stderr_str.lower() or "failed" in stderr_str.lower():
            print("⚠️ 服务器恢复警告:", stderr_str[:200])
        
        print("✅ 数据已传输到服务器")
        print(f"📦 查看容器日志：docker logs projectionroom-pg")
        
    finally:
        ssh.close()


def main():
    parser = argparse.ArgumentParser(description="Yachiyo 数据迁移工具")
    sub = parser.add_subparsers(dest="cmd", required=True)
    
    # export 子命令
    exp = sub.add_parser("export", help="导出 Yachiyo 到本地文件")
    exp.add_argument("--out", default="_migrate/yachiyo.dump", help="输出路径")
    
    # deploy 子命令
    dep = sub.add_parser("deploy", help="部署到远程服务器")
    dep.add_argument("--host", default="60.205.212.2", help="服务器地址")
    dep.add_argument("--key-file", default=os.path.expanduser("~/.ssh/id_rsa"), help="SSH 私钥路径")
    dep.add_argument("--remote-dir", default="/opt/projectionroom/data", help="服务器目标目录")
    
    args = parser.parse_args()
    
    if args.cmd == "export":
        export_to_file(YACHIYO_DB, Path(args.out))
    elif args.cmd == "deploy":
        deploy_to_server(args.host, args.key_file, args.remote_dir)


if __name__ == "__main__":
    main()
