#!/usr/bin/env sh
# ShitIDC PostgreSQL backup (第二十五阶段).
#
#   每日全量 pg_dump(custom 格式，自带压缩) + 保留策略。cron 示例:
#     30 3 * * * /opt/shitidc/scripts/backup.sh >> /var/log/shitidc-backup.log 2>&1
#
# 环境变量:
#   BACKUP_DIR        备份输出目录 (默认 ./backups)
#   BACKUP_KEEP_DAYS  保留天数     (默认 7)
#   数据库连接读 .env 的 POSTGRES_* 或标准 PGHOST/PGPORT/PGUSER/PGPASSWORD。
#
# 恢复: scripts/restore.sh backups/shitidc-XXXX.sql.dump
# 安全: 备份包含用户隐私与财务数据，请加密存放(或上传开启服务端加密的
# 对象存储)并与主机分机/分机房保存。每月至少一次恢复到测试环境验证
# (第六十三阶段: 备份成功不等于能恢复)。

set -eu

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
PROJECT_DIR=$(dirname "$SCRIPT_DIR")
BACKUP_DIR="${BACKUP_DIR:-$PROJECT_DIR/backups}"
KEEP_DAYS="${BACKUP_KEEP_DAYS:-7}"
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT="$BACKUP_DIR/shitidc-$STAMP.sql.dump"

if [ -f "$PROJECT_DIR/.env" ]; then
    # shellcheck disable=SC1091
    set -a
    . "$PROJECT_DIR/.env"
    set +a
fi

export PGHOST="${PGHOST:-localhost}"
export PGPORT="${PGPORT:-5432}"
export PGDATABASE="${PGDATABASE:-${POSTGRES_DB:-shitidc}}"
export PGUSER="${PGUSER:-${POSTGRES_USER:-shitidc}}"
[ -n "${PGPASSWORD:-}" ] || PGPASSWORD="${POSTGRES_PASSWORD:-}"
export PGPASSWORD

mkdir -p "$BACKUP_DIR"

echo "[$(date -u +%FT%TZ)] dumping $PGDATABASE@$PGHOST -> $OUT"
pg_dump --no-owner --no-privileges --format=custom --file="$OUT"

sha256sum "$OUT" > "$OUT.sha256"
echo "[$(date -u +%FT%TZ)] wrote $OUT ($(du -h "$OUT" | cut -f1))"

find "$BACKUP_DIR" -name 'shitidc-*.sql.dump*' -mtime +"$KEEP_DAYS" -delete
echo "[$(date -u +%FT%TZ)] retention applied (keep $KEEP_DAYS days)"
