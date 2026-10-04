#!/usr/bin/env sh
# ShitIDC PostgreSQL restore (第二十五/六十三阶段).
#
#   scripts/restore.sh backups/shitidc-20260101T033000Z.sql.dump
#
# 默认恢复到 .env / PG* 指定的数据库。加 DRY=1 先做校验不落库:
#   DRY=1 scripts/restore.sh backups/shitidc-xxx.sql.dump
#
# 警告: 恢复会覆盖目标库中的同名对象，请在维护窗口操作。

set -eu

FILE="${1:-}"
if [ -z "$FILE" ] || [ ! -f "$FILE" ]; then
    echo "usage: $0 <backup.sql.dump>" >&2
    exit 1
fi

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
PROJECT_DIR=$(dirname "$SCRIPT_DIR")

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

echo "[$(date -u +%FT%TZ)] verifying checksum"
if [ -f "$FILE.sha256" ]; then
    sha256sum -c "$FILE.sha256"
fi

echo "[$(date -u +%FT%TZ)] target: $PGUSER@$PGHOST:$PGPORT/$PGDATABASE"
pg_restore --list "$FILE" > /dev/null
echo "[$(date -u +%FT%TZ)] archive is valid ($(pg_restore --list "$FILE" | grep -c 'TABLE DATA') tables with data)"

if [ "${DRY:-0}" = "1" ]; then
    echo "[$(date -u +%FT%TZ)] DRY=1, not restoring. Looks good."
    exit 0
fi

echo "[$(date -u +%FT%TZ)] restoring (existing objects will be replaced)"
pg_restore --clean --if-exists --no-owner --no-privileges --dbname="$PGDATABASE" "$FILE"
echo "[$(date -u +%FT%TZ)] restore finished. 核对: 用户数/订单数/余额/交易条数。"
