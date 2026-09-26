#!/usr/bin/env bash
# ============================================================================
#  DocFlow — резервное копирование (ТЗ §7).
#
#    ./backup.sh                 создать копию в ./backups
#    ./backup.sh /mnt/nas/docflow  создать копию в указанном каталоге
#
#  В копию попадают:
#    db.dump      — дамп PostgreSQL (custom format, pg_dump -Fc)
#    files.tar.gz — каталог файлов документов (файлы уже зашифрованы AES-256-GCM)
#    manifest.txt — версии, состав, sha256 обеих частей
#
#  В копию НЕ попадает .env с ключом шифрования: тогда одна украденная копия
#  давала бы и шифротекст, и ключ. Ключ хранится отдельно — см. README-DEPLOY,
#  раздел «Передача секретов». Без FILES_ENCRYPTION_KEY документы из этой
#  копии не восстановить.
#
#  Проверить восстановимость: ./verify-backup.sh backups/<каталог>
# ============================================================================
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

DEST_ROOT="${1:-./backups}"
KEEP_DAYS="${BACKUP_KEEP_DAYS:-30}"

if [ -t 1 ]; then B=$'\033[1m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'; else B=; G=; Y=; R=; N=; fi
say()  { printf '%s\n' "${B}==>${N} $*"; }
ok()   { printf '%s\n' "${G}✓${N} $*"; }
warn() { printf '%s\n' "${Y}!${N} $*"; }
die()  { printf '%s\n' "${R}✗ $*${N}" >&2; exit 1; }

[ -f .env ] || die ".env не найден — запускайте из каталога развёртывания."

if docker compose version >/dev/null 2>&1; then DC="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then DC="docker-compose"
else die "docker compose не найден."; fi

get_env() { grep -E "^$1=" .env | head -1 | cut -d= -f2- | tr -d '\r' | sed 's/[[:space:]]*$//'; }

PG_USER="$(get_env POSTGRES_USER)";  PG_USER="${PG_USER:-docflow}"
PG_DB="$(get_env POSTGRES_DB)";      PG_DB="${PG_DB:-docflow}"
FILES_DIR="$(get_env FILES_DIR)";    FILES_DIR="${FILES_DIR:-/var/lib/docflow/files}"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
DEST="$DEST_ROOT/$STAMP"
mkdir -p "$DEST"

say "Резервная копия → $DEST"

# --- база -------------------------------------------------------------------
say "Дамп PostgreSQL…"
$DC exec -T postgres pg_dump -U "$PG_USER" -d "$PG_DB" -Fc > "$DEST/db.dump" \
  || die "pg_dump не отработал (стек поднят? ./deploy.sh ps)"
[ -s "$DEST/db.dump" ] || die "дамп пустой"
ok "db.dump — $(du -h "$DEST/db.dump" | cut -f1)"

# --- файлы документов -------------------------------------------------------
# Читаем том через сам контейнер бэкенда: том docflow_files примонтирован туда,
# и на хосте его путь знать не нужно.
say "Архив файлов документов…"
$DC exec -T docflow-backend tar -cf - -C "$(dirname "$FILES_DIR")" "$(basename "$FILES_DIR")" \
  | gzip > "$DEST/files.tar.gz" \
  || die "не удалось прочитать каталог файлов"
ok "files.tar.gz — $(du -h "$DEST/files.tar.gz" | cut -f1)"

# --- манифест ---------------------------------------------------------------
{
  echo "DocFlow backup"
  echo "created_utc:  $STAMP"
  echo "host:         $(hostname)"
  echo "postgres_db:  $PG_DB"
  echo "files_dir:    $FILES_DIR"
  echo "compose_ps:"
  $DC ps --format '  {{.Service}} {{.Image}} {{.Status}}' 2>/dev/null || true
  echo "sha256:"
  ( cd "$DEST" && sha256sum db.dump files.tar.gz | sed 's/^/  /' )
  echo
  echo "ВНИМАНИЕ: ключ шифрования файлов (FILES_ENCRYPTION_KEY) в копию не"
  echo "включён. Без него документы из files.tar.gz не расшифровать."
} > "$DEST/manifest.txt"
ok "manifest.txt"

# --- ретенция ---------------------------------------------------------------
if [ "$KEEP_DAYS" -gt 0 ] 2>/dev/null; then
  removed=0
  while IFS= read -r old; do
    rm -rf "$old"; removed=$((removed+1))
  done < <(find "$DEST_ROOT" -mindepth 1 -maxdepth 1 -type d -mtime "+$KEEP_DAYS" 2>/dev/null)
  [ "$removed" -gt 0 ] && ok "удалено старых копий: $removed (старше $KEEP_DAYS дней)"
fi

echo
ok "Готово: $DEST"
warn "Копия не считается рабочей, пока не проверена:  ./verify-backup.sh \"$DEST\""
