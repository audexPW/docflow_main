#!/usr/bin/env bash
# ============================================================================
#  DocFlow — восстановление из резервной копии.
#
#    ./restore.sh backups/20260719T101500Z
#
#  ВНИМАНИЕ: операция разрушающая — текущие база и файлы документов будут
#  заменены содержимым копии. Перед восстановлением скрипт делает страховочную
#  копию текущего состояния (./backups/pre-restore-<штамп>).
#
#  Перед боевым восстановлением обязательно прогоните ./verify-backup.sh —
#  он проверяет ту же копию, ничего не ломая.
#
#  Ключ шифрования: FILES_ENCRYPTION_KEY в .env должен быть ТОТ ЖЕ, что был на
#  момент создания копии. С другим ключом база восстановится, а документы
#  превратятся в нечитаемый набор байт.
# ============================================================================
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -t 1 ]; then B=$'\033[1m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'; else B=; G=; Y=; R=; N=; fi
say()  { printf '%s\n' "${B}==>${N} $*"; }
ok()   { printf '%s\n' "${G}✓${N} $*"; }
warn() { printf '%s\n' "${Y}!${N} $*"; }
die()  { printf '%s\n' "${R}✗ $*${N}" >&2; exit 1; }

BACKUP="${1:-}"
[ -n "$BACKUP" ] || die "укажите каталог копии: ./restore.sh backups/<штамп>"
[ -f "$BACKUP/db.dump" ] || die "нет $BACKUP/db.dump"
[ -f "$BACKUP/files.tar.gz" ] || die "нет $BACKUP/files.tar.gz"
[ -f .env ] || die ".env не найден."

if docker compose version >/dev/null 2>&1; then DC="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then DC="docker-compose"
else die "docker compose не найден."; fi

get_env() { grep -E "^$1=" .env | head -1 | cut -d= -f2- | tr -d '\r' | sed 's/[[:space:]]*$//'; }
PG_USER="$(get_env POSTGRES_USER)"; PG_USER="${PG_USER:-docflow}"
PG_DB="$(get_env POSTGRES_DB)";     PG_DB="${PG_DB:-docflow}"
FILES_DIR="$(get_env FILES_DIR)";   FILES_DIR="${FILES_DIR:-/var/lib/docflow/files}"

warn "Текущие база и файлы документов будут ЗАМЕНЕНЫ содержимым $BACKUP."
read -r -p "Продолжить? введите restore: " a
[ "$a" = "restore" ] || die "Отменено."

# --- страховочная копия текущего состояния ----------------------------------
say "Делаю страховочную копию текущего состояния…"
./backup.sh ./backups >/dev/null 2>&1 && ok "страховочная копия создана в ./backups" \
  || warn "страховочную копию снять не удалось (стек не поднят?) — продолжаю"

# --- останавливаем всё, кроме базы ------------------------------------------
say "Останавливаю приложение (база остаётся поднятой)…"
$DC stop docflow-backend frontend >/dev/null 2>&1 || true
$DC up -d postgres >/dev/null
for i in $(seq 1 60); do
  $DC exec -T postgres pg_isready -U "$PG_USER" >/dev/null 2>&1 && break
  sleep 1
done

# --- база -------------------------------------------------------------------
say "Восстанавливаю базу…"
$DC exec -T postgres dropdb -U "$PG_USER" --if-exists --force "$PG_DB"
$DC exec -T postgres createdb -U "$PG_USER" "$PG_DB"
$DC exec -T postgres pg_restore -U "$PG_USER" -d "$PG_DB" --no-owner --no-privileges \
  < "$BACKUP/db.dump" || warn "pg_restore завершился с предупреждениями (обычно это нормально)"
ok "база восстановлена"

# --- файлы ------------------------------------------------------------------
say "Восстанавливаю файлы документов…"
$DC up -d docflow-backend >/dev/null
sleep 3
PARENT="$(dirname "$FILES_DIR")"
BASENAME="$(basename "$FILES_DIR")"
$DC exec -T docflow-backend sh -c "rm -rf '$FILES_DIR'.old && mv '$FILES_DIR' '$FILES_DIR'.old 2>/dev/null || true"
gunzip -c "$BACKUP/files.tar.gz" | $DC exec -T docflow-backend tar -xf - -C "$PARENT"
$DC exec -T docflow-backend sh -c "test -d '$FILES_DIR'" \
  || die "после распаковки каталог $FILES_DIR не появился — состояние в $FILES_DIR.old"
ok "файлы восстановлены (предыдущие остались в $FILES_DIR.old)"

# --- поднимаем всё ----------------------------------------------------------
say "Поднимаю стек…"
$DC up -d >/dev/null
ok "готово"

echo
warn "Проверьте вход в веб-интерфейс и открытие любого документа."
warn "Если всё хорошо — удалите $FILES_DIR.old внутри контейнера бэкенда."
