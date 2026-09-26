#!/usr/bin/env bash
# ============================================================================
#  DocFlow — проверка сценариев приёмки (ТЗ §10).
#
#    ./smoke-test.sh                     проверить локальный стенд
#    ./smoke-test.sh https://docs.by     проверить боевой адрес
#
#  Проходит ровно те сценарии, которые перечислены в ТЗ как критерии приёмки,
#  и печатает результат по каждому. Код возврата 0 = всё прошло.
#
#  Запускать при заказчике: это и есть демонстрация приёмки, только
#  воспроизводимая, а не «сейчас покажу, обычно работает».
# ============================================================================
set -uo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -t 1 ]; then B=$'\033[1m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'; else B=; G=; Y=; R=; N=; fi
pass=0; fail=0; skip=0
ok()   { printf '%s %s\n' "${G}✓${N}" "$1"; pass=$((pass+1)); }
bad()  { printf '%s %s\n' "${R}✗${N}" "$1"; fail=$((fail+1)); }
skipf(){ printf '%s %s\n' "${Y}~${N}" "$1"; skip=$((skip+1)); }
say()  { printf '\n%s\n' "${B}$1${N}"; }

command -v curl >/dev/null 2>&1 || { echo "нужен curl"; exit 1; }
[ -f .env ] || { echo ".env не найден — запускайте из каталога развёртывания"; exit 1; }

get_env() { grep -E "^$1=" .env | head -1 | cut -d= -f2- | tr -d '\r' | sed 's/[[:space:]]*$//'; }

BASE="${1:-}"
if [ -z "$BASE" ]; then
  DOMAIN="$(get_env DOMAIN)"
  if [ -n "$DOMAIN" ]; then BASE="https://$DOMAIN"; else BASE="http://localhost:$(get_env HTTP_PORT || echo 80)"; fi
fi
BASE="${BASE%/}"

ADMIN_LOGIN="$(get_env BOOTSTRAP_ADMIN_LOGIN)"; ADMIN_LOGIN="${ADMIN_LOGIN:-admin}"
ADMIN_PASS="$(get_env BOOTSTRAP_ADMIN_PASSWORD)"
ONEC_TOKEN="$(get_env ONEC_INBOUND_TOKEN)"

printf '%s\n' "${B}DocFlow — проверка сценариев приёмки${N}"
printf 'Адрес: %s\n' "$BASE"

req() { # метод путь [данные] [заголовок]
  local method="$1" path="$2" data="${3:-}" extra="${4:-}"
  local args=(-s -o /tmp/df_body.$$ -w '%{http_code}' -X "$method" "$BASE$path")
  [ -n "$data" ]  && args+=(-H 'Content-Type: application/json' -d "$data")
  [ -n "$extra" ] && args+=(-H "$extra")
  curl "${args[@]}" 2>/dev/null || echo 000
}
body() { cat /tmp/df_body.$$ 2>/dev/null; }

# --- 1. Доступность и HTTPS -------------------------------------------------
say "1. Сервис доступен (ТЗ §10)"
code=$(req GET /healthz)
[ "$code" = "200" ] && ok "healthz отвечает 200" || bad "healthz вернул $code"

case "$BASE" in
  https://*)
    ok "работа по HTTPS"
    redirect=$(curl -s -o /dev/null -w '%{http_code}' "http://${BASE#https://}/" 2>/dev/null || echo 000)
    case "$redirect" in
      301|302|308) ok "http перенаправляется на https" ;;
      *) skipf "редирект http→https вернул $redirect (проверьте вручную)" ;;
    esac
    ;;
  *) bad "работа по HTTP: для сдачи заказчику нужен HTTPS (ТЗ §10)" ;;
esac

# --- 2. Аутентификация и роли -----------------------------------------------
say "2. Вход и разделение доступа (ТЗ §7)"
code=$(req POST /api/auth/login "{\"login\":\"$ADMIN_LOGIN\",\"password\":\"$ADMIN_PASS\"}")
if [ "$code" = "200" ]; then
  TOKEN=$(body | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  [ -n "$TOKEN" ] && ok "вход администратора" || bad "вход прошёл, но токен не разобран"
else
  bad "вход администратора вернул $code"
  TOKEN=""
fi

code=$(req POST /api/auth/login "{\"login\":\"$ADMIN_LOGIN\",\"password\":\"заведомо-неверный\"}")
[ "$code" = "401" ] && ok "неверный пароль отклоняется" || bad "неверный пароль вернул $code (ожидался 401)"

code=$(req GET /api/documents)
[ "$code" = "401" ] && ok "без токена доступа нет" || bad "запрос без токена вернул $code (ожидался 401)"

if [ -n "$TOKEN" ]; then
  code=$(req GET /api/users "" "Authorization: Bearer $TOKEN")
  [ "$code" = "200" ] && ok "администратор видит список пользователей" || bad "список пользователей вернул $code"
fi

# --- 3. Защита от перебора --------------------------------------------------
say "3. Защита входа от перебора"
blocked=0
for _ in $(seq 1 12); do
  code=$(req POST /api/auth/login '{"login":"smoke-probe","password":"неверный"}')
  [ "$code" = "429" ] && { blocked=1; break; }
done
[ "$blocked" = "1" ] && ok "перебор блокируется (429)" || bad "после 12 попыток блокировки нет"

# --- 4. Смена пароля --------------------------------------------------------
say "4. Управление учётными записями (ТЗ §7)"
if [ -n "$TOKEN" ]; then
  code=$(req POST /api/auth/password '{"current_password":"неверный","new_password":"НовыйПароль2026"}' "Authorization: Bearer $TOKEN")
  [ "$code" = "401" ] && ok "смена пароля требует текущий пароль" || bad "смена пароля с неверным текущим вернула $code"

  code=$(req POST /api/auth/password "{\"current_password\":\"$ADMIN_PASS\",\"new_password\":\"коротк1\"}" "Authorization: Bearer $TOKEN")
  [ "$code" = "400" ] && ok "слабый пароль отклоняется" || bad "слабый пароль вернул $code"
else
  skipf "смена пароля не проверена: нет токена"
fi

# --- 5. Обратный канал из 1С ------------------------------------------------
say "5. Возврат статуса из 1С (ТЗ §2, §10)"
if [ -z "$ONEC_TOKEN" ]; then
  bad "ONEC_INBOUND_TOKEN не задан — 1С не сможет вернуть статус"
else
  code=$(req POST /api/onec/status '{"source_id":"00000000-0000-0000-0000-000000000000","status":"posted"}')
  [ "$code" = "401" ] && ok "без токена 1С не принимается" || bad "запрос без токена вернул $code (ожидался 401)"

  code=$(req POST /api/onec/status '{"source_id":"00000000-0000-0000-0000-000000000000","status":"posted"}' "X-DocFlow-Token: $ONEC_TOKEN")
  [ "$code" = "404" ] && ok "канал приёма работает (несуществующий документ → 404)" \
    || bad "приём статуса вернул $code (ожидался 404 для несуществующего документа)"

  code=$(req POST /api/onec/status '{"source_id":"00000000-0000-0000-0000-000000000000","status":"выдуманный"}' "X-DocFlow-Token: $ONEC_TOKEN")
  [ "$code" = "400" ] && ok "неизвестный статус отклоняется" || bad "неизвестный статус вернул $code"
fi

# --- 6. Конфигурация --------------------------------------------------------
say "6. Конфигурация боевого стенда"
[ "$(get_env APP_ENV)" = "production" ] && ok "APP_ENV=production" || bad "APP_ENV не production"
case "$(get_env CORS_ORIGINS)" in
  "*"|"") bad "CORS_ORIGINS открыт для всех" ;;
  *)      ok "CORS_ORIGINS ограничен" ;;
esac
case "$(get_env POSTGRES_PASSWORD)" in
  docflow|postgres|change-me|"") bad "пароль PostgreSQL из примера" ;;
  *) ok "пароль PostgreSQL уникальный" ;;
esac
[ -n "$(get_env FILES_ENCRYPTION_KEY)" ] && ok "ключ шифрования файлов задан" || bad "FILES_ENCRYPTION_KEY пуст"

# --- 7. Резервное копирование -----------------------------------------------
say "7. Резервное копирование (ТЗ §7)"
if [ -x ./backup.sh ] && [ -x ./verify-backup.sh ]; then
  ok "скрипты backup.sh и verify-backup.sh на месте"
  latest=$(ls -1d backups/*/ 2>/dev/null | tail -1)
  if [ -n "$latest" ]; then
    ok "последняя копия: $latest"
    skipf "восстановимость проверяется отдельно: ./verify-backup.sh $latest"
  else
    bad "ни одной резервной копии ещё не снято — запустите ./backup.sh"
  fi
else
  bad "скрипты резервного копирования отсутствуют"
fi

rm -f /tmp/df_body.$$

# --- итог -------------------------------------------------------------------
printf '\n%s\n' "${B}────────────────────────────────────────${N}"
printf ' пройдено: %s%d%s   не пройдено: %s%d%s   вручную: %d\n' "$G" "$pass" "$N" "$R" "$fail" "$N" "$skip"
printf '%s\n\n' "${B}────────────────────────────────────────${N}"

if [ "$fail" -gt 0 ]; then
  printf '%sЕсть непройденные проверки — сдавать рано.%s\n\n' "$R" "$N"
  exit 1
fi
printf '%sВсе автоматические проверки пройдены.%s\n' "$G" "$N"
printf 'Осталось показать заказчику вручную: загрузку документа с телефона,\n'
printf 'распознавание, дозаполнение полей и появление документа в 1С.\n\n'
