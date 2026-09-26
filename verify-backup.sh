#!/usr/bin/env bash
# ============================================================================
#  DocFlow — проверка, что из резервной копии реально можно восстановиться.
#  Требование ТЗ §7: бэкапы «с проверкой, что из них реально можно
#  восстановиться». Скрипт эту проверку и выполняет — автоматически.
#
#    ./verify-backup.sh backups/20260719T101500Z
#
#  Что делает:
#    1. поднимает ОДНОРАЗОВЫЙ контейнер PostgreSQL (боевой не трогается);
#    2. восстанавливает в него db.dump;
#    3. распаковывает files.tar.gz во временный каталог;
#    4. запускает backupcheck: сверяет каждый документ из базы с файлом на
#       диске — файл есть, расшифровывается ключом FILES_ENCRYPTION_KEY,
#       sha256 совпадает с записанным в базе;
#    5. сносит за собой всё временное.
#
#  Боевой стек при этом продолжает работать и никак не затрагивается.
#  Код возврата 0 = копия восстановима. Годится для cron с оповещением.
# ============================================================================
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -t 1 ]; then B=$'\033[1m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'; else B=; G=; Y=; R=; N=; fi
say()  { printf '%s\n' "${B}==>${N} $*"; }
ok()   { printf '%s\n' "${G}✓${N} $*"; }
warn() { printf '%s\n' "${Y}!${N} $*"; }
die()  { printf '%s\n' "${R}✗ $*${N}" >&2; exit 1; }

BACKUP="${1:-}"
[ -n "$BACKUP" ] || die "укажите каталог копии: ./verify-backup.sh backups/<штамп>"
[ -f "$BACKUP/db.dump" ] || die "нет $BACKUP/db.dump"
[ -f "$BACKUP/files.tar.gz" ] || die "нет $BACKUP/files.tar.gz"
[ -f .env ] || die ".env не найден — из него берётся ключ шифрования."

command -v docker >/dev/null 2>&1 || die "docker не установлен."

get_env() { grep -E "^$1=" .env | head -1 | cut -d= -f2- | tr -d '\r' | sed 's/[[:space:]]*$//'; }
KEY="$(get_env FILES_ENCRYPTION_KEY)"
[ -n "$KEY" ] || die "FILES_ENCRYPTION_KEY пуст — расшифровать файлы нечем."

# --- контрольные суммы архива ----------------------------------------------
if grep -q '^sha256:' "$BACKUP/manifest.txt" 2>/dev/null; then
  say "Сверка контрольных сумм архива…"
  ( cd "$BACKUP" && sed -n '/^sha256:/,/^$/p' manifest.txt \
      | grep -E '^\s+[0-9a-f]{64}' | sed 's/^\s*//' | sha256sum -c - ) \
    || die "архив повреждён: sha256 не сходится"
  ok "суммы сходятся"
fi

SUF="$(date +%s)$$"
PG_NAME="docflow-verify-pg-$SUF"
NET_NAME="docflow-verify-net-$SUF"
TMPDIR="$(mktemp -d)"
BACKEND_IMAGE="$(docker compose config --images 2>/dev/null | grep -i 'docflow.*backend' | head -1 || true)"
[ -n "$BACKEND_IMAGE" ] || BACKEND_IMAGE="$(basename "$PWD" | tr '[:upper:]' '[:lower:]')-docflow-backend"

cleanup() {
  docker rm -f "$PG_NAME" >/dev/null 2>&1 || true
  docker network rm "$NET_NAME" >/dev/null 2>&1 || true
  rm -rf "$TMPDIR"
}
trap cleanup EXIT

# --- одноразовая база -------------------------------------------------------
say "Поднимаю одноразовый PostgreSQL (боевой не трогаю)…"
docker network create "$NET_NAME" >/dev/null
docker run -d --name "$PG_NAME" --network "$NET_NAME" \
  -e POSTGRES_USER=verify -e POSTGRES_PASSWORD=verify -e POSTGRES_DB=verify \
  postgres:16-alpine >/dev/null

for i in $(seq 1 60); do
  docker exec "$PG_NAME" pg_isready -U verify >/dev/null 2>&1 && break
  sleep 1
  [ "$i" = 60 ] && die "одноразовый PostgreSQL не поднялся"
done
ok "поднят"

say "Восстанавливаю дамп…"
docker exec -i "$PG_NAME" pg_restore -U verify -d verify --no-owner --no-privileges \
  < "$BACKUP/db.dump" >/dev/null 2>"$TMPDIR/restore.err" || {
    warn "pg_restore завершился с предупреждениями:"; sed 's/^/    /' "$TMPDIR/restore.err" | head -20; }
ok "дамп восстановлен"

# --- файлы ------------------------------------------------------------------
say "Распаковываю файлы документов…"
mkdir -p "$TMPDIR/restore"
tar -xzf "$BACKUP/files.tar.gz" -C "$TMPDIR/restore"
FILES_ROOT="$(find "$TMPDIR/restore" -maxdepth 2 -type d -name files | head -1)"
[ -n "$FILES_ROOT" ] || FILES_ROOT="$TMPDIR/restore"
ok "распаковано: $(find "$FILES_ROOT" -type f | wc -l) файл(ов)"

# --- собственно проверка ----------------------------------------------------
say "Проверяю целостность: база ↔ файлы ↔ ключ шифрования…"
docker run --rm --network "$NET_NAME" \
  -e DATABASE_URL="postgres://verify:verify@$PG_NAME:5432/verify?sslmode=disable" \
  -e FILES_DIR=/restore \
  -e FILES_ENCRYPTION_KEY="$KEY" \
  -v "$FILES_ROOT:/restore:ro" \
  --entrypoint /app/backupcheck \
  "$BACKEND_IMAGE" "${@:2}" \
  || die "копия НЕ восстановима — разбирайтесь до того, как она понадобится"

echo
ok "Копия $BACKUP восстановима."
