#!/usr/bin/env bash
# ============================================================================
#  DocFlow — деплой в одну команду.
#
#    ./deploy.sh            первый запуск: секреты + сборка + подъём
#    ./deploy.sh up         пересобрать и поднять (после git pull)
#    ./deploy.sh down       остановить всё (данные сохраняются в volume'ах)
#    ./deploy.sh logs       смотреть логи
#    ./deploy.sh ps         статус контейнеров
#    ./deploy.sh restart    перезапуск без пересборки
#    ./deploy.sh nuke       ОСТОРОЖНО: снести контейнеры + volume'ы (всё удалит)
#
#  При первом запуске сам создаёт .env, генерит JWT_SECRET,
#  FILES_ENCRYPTION_KEY и пароль администратора, показывает их в конце.
# ============================================================================
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- цвета (без зависимостей) ----------------------------------------------
if [ -t 1 ]; then B=$'\033[1m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'; else B=; G=; Y=; R=; N=; fi
say()  { printf '%s\n' "${B}==>${N} $*"; }
ok()   { printf '%s\n' "${G}✓${N} $*"; }
warn() { printf '%s\n' "${Y}!${N} $*"; }
die()  { printf '%s\n' "${R}✗ $*${N}" >&2; exit 1; }

# --- проверки окружения -----------------------------------------------------
command -v docker >/dev/null 2>&1 || die "docker не установлен. Поставьте Docker Engine и повторите."

if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  DC="docker-compose"
else
  die "docker compose не найден. Нужен Docker Compose v2 (docker compose) или v1 (docker-compose)."
fi

docker info >/dev/null 2>&1 || die "демон docker недоступен (нет прав или не запущен). Попробуйте под sudo или добавьте себя в группу docker."

# secret-хелпер: openssl, а если нет — /dev/urandom
gen_hex32() {
  if command -v openssl >/dev/null 2>&1; then openssl rand -hex 32
  else head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'; fi
}
gen_pass() {
  if command -v openssl >/dev/null 2>&1; then openssl rand -base64 18 | tr -d '/+=' | cut -c1-20
  else head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n' | cut -c1-20; fi
}

# upsert KEY=VALUE в .env (заменяет пустое или отсутствующее значение)
set_env() {
  local key="$1" val="$2"
  if grep -qE "^${key}=" .env; then
    # экранируем & и | для sed
    local esc; esc=$(printf '%s' "$val" | sed -e 's/[&|\\]/\\&/g')
    sed -i -E "s|^(${key}=).*|\1${esc}|" .env
  else
    printf '%s=%s\n' "$key" "$val" >> .env
  fi
}
get_env() { grep -E "^$1=" .env | head -1 | cut -d= -f2-; }

# --- подкоманды -------------------------------------------------------------
cmd="${1:-bootstrap}"
case "$cmd" in
  down)    say "Останавливаю стек…"; $DC down;               ok "Остановлено. Данные сохранены в volume'ах."; exit 0 ;;
  logs)    exec $DC logs -f --tail=200 ;;
  ps)      exec $DC ps ;;
  restart) say "Перезапуск…"; $DC restart;                   ok "Готово."; exit 0 ;;
  nuke)    warn "Это удалит контейнеры И volume'ы (БД, файлы, ключи в .env останутся)."
           read -r -p "Точно снести? введите yes: " a; [ "$a" = "yes" ] || die "Отменено."
           $DC down -v; ok "Снесено."; exit 0 ;;
  up|bootstrap) : ;;   # основной путь ниже
  *) die "Неизвестная команда: $cmd (up|down|logs|ps|restart|nuke)" ;;
esac

# --- первичная генерация .env ----------------------------------------------
if [ ! -f .env ]; then
  say "Создаю .env из .env.example и генерирую секреты…"
  cp .env.example .env

  set_env JWT_SECRET            "$(gen_hex32)"
  set_env FILES_ENCRYPTION_KEY  "$(gen_hex32)"
  # Токен, которым 1С аутентифицируется, возвращая статус обработки.
  set_env ONEC_INBOUND_TOKEN    "$(gen_hex32)"

  ADMIN_PASS="$(gen_pass)"
  set_env BOOTSTRAP_ADMIN_PASSWORD "$ADMIN_PASS"

  # Пароль БД тоже генерируем: дефолтный из примера — это открытая дверь,
  # и в production сервис с ним просто не поднимется (config.Validate).
  PGUSER="$(get_env POSTGRES_USER)"; PGUSER="${PGUSER:-docflow}"
  PGDB="$(get_env POSTGRES_DB)";     PGDB="${PGDB:-docflow}"
  PGPASS="$(gen_pass)"
  set_env POSTGRES_USER "$PGUSER"
  set_env POSTGRES_DB   "$PGDB"
  set_env POSTGRES_PASSWORD "$PGPASS"
  set_env DATABASE_URL "postgres://${PGUSER}:${PGPASS}@postgres:5432/${PGDB}?sslmode=disable"

  ok ".env создан. Секреты сгенерированы."
  FRESH=1

  # --- домен и HTTPS --------------------------------------------------------
  # Без домена сертификат Let's Encrypt получить нельзя, поэтому спрашиваем
  # сразу: это единственное, что нельзя сгенерировать за заказчика.
  echo
  say "Настройка адреса сайта"
  printf '  Укажите домен, который уже указывает на этот сервер (A-запись).\n'
  printf '  Тогда HTTPS настроится автоматически: Caddy сам получит и будет\n'
  printf '  продлевать сертификат Let'"'"'s Encrypt.\n'
  printf '  Оставьте пустым для стенда без домена (будет только HTTP).\n\n'
  # Неинтерактивная установка: DOMAIN=docs.klient.by ./deploy.sh
  if [ -n "${DOMAIN:-}" ]; then
    DOMAIN_IN="$DOMAIN"
    printf '  Домен взят из переменной окружения: %s\n' "$DOMAIN_IN"
  elif [ -t 0 ]; then
    read -r -p "  Домен (например docs.klient.by): " DOMAIN_IN || DOMAIN_IN=""
  else
    DOMAIN_IN=""
  fi
  DOMAIN_IN="$(printf '%s' "$DOMAIN_IN" | tr -d ' \t\r')"

  if [ -n "$DOMAIN_IN" ]; then
    if [ -n "${ACME_EMAIL:-}" ]; then
      ACME_IN="$ACME_EMAIL"
    elif [ -t 0 ]; then
      read -r -p "  E-mail для уведомлений Let's Encrypt (можно пропустить): " ACME_IN || ACME_IN=""
    else
      ACME_IN=""
    fi
    set_env DOMAIN "$DOMAIN_IN"
    set_env ACME_EMAIL "$(printf '%s' "$ACME_IN" | tr -d ' \t\r')"
    set_env APP_ENV production
    set_env CORS_ORIGINS "https://${DOMAIN_IN}"
    set_env PUBLIC_BASE_URL "https://${DOMAIN_IN}"
    ok "HTTPS будет включён для ${DOMAIN_IN}"
  else
    set_env DOMAIN ""
    set_env APP_ENV development
    set_env CORS_ORIGINS "*"
    warn "Домен не задан: поднимаю по HTTP и в режиме development."
    warn "Для сдачи заказчику домен обязателен — иначе не будет HTTPS (ТЗ §10)."
  fi
else
  warn ".env уже существует — секреты не трогаю."
  FRESH=0
fi

# --- какие профиля поднимать ------------------------------------------------
# tls   — Caddy с автоматическим сертификатом (когда задан DOMAIN)
# plain — публикация порта 80 напрямую (стенд без домена)
# ai    — локальные paddle-ocr и llm, если включены в .env
DOMAIN_SET="$(get_env DOMAIN)"
EXTRA_PROFILES="$(get_env COMPOSE_PROFILES)"

if [ -n "$DOMAIN_SET" ]; then
  PROFILES="tls"
else
  PROFILES="plain"
fi
case "$EXTRA_PROFILES" in
  *ai*) PROFILES="${PROFILES},ai" ;;
esac
export COMPOSE_PROFILES="$PROFILES"
say "Профили compose: $COMPOSE_PROFILES"

# --- сборка и запуск --------------------------------------------------------
say "Собираю образы (первый раз это несколько минут)…"
$DC build

say "Поднимаю контейнеры…"
$DC up -d

# --- ожидание готовности бэкенда -------------------------------------------
HTTP_PORT="$(get_env HTTP_PORT)"; HTTP_PORT="${HTTP_PORT:-80}"
say "Жду, пока бэкенд ответит на /healthz…"
# Проверяем изнутри сети: снаружи при включённом HTTPS порт 80 отдаёт редирект,
# а сертификат может ещё выпускаться.
HEALTH_URL="http://localhost:${HTTP_PORT}/healthz"
ready=0
for i in $(seq 1 60); do
  if command -v curl >/dev/null 2>&1; then
    code=$(curl -s -o /dev/null -w '%{http_code}' "$HEALTH_URL" || true)
  else
    code=$(wget -qO- "$HEALTH_URL" >/dev/null 2>&1 && echo 200 || echo 000)
  fi
  if [ "$code" = "200" ]; then ready=1; break; fi
  sleep 2
done

echo
if [ "$ready" = "1" ]; then ok "Стек поднят и здоров."; else
  warn "Здоровье пока не подтвердилось за 2 минуты. Проверьте логи: ./deploy.sh logs"
fi

# --- итог -------------------------------------------------------------------
ADMIN_LOGIN="$(get_env BOOTSTRAP_ADMIN_LOGIN)"
ADMIN_PASS_SHOW="$(get_env BOOTSTRAP_ADMIN_PASSWORD)"
printf '\n%s\n' "${B}────────────────────────────────────────────────────────${N}"
printf '%s\n'   "${B} DocFlow развёрнут${N}"
printf '%s\n'   "${B}────────────────────────────────────────────────────────${N}"
if [ -n "$DOMAIN_SET" ]; then
  printf '  Веб-интерфейс : %s\n' "https://${DOMAIN_SET}/"
  printf '  Health-check  : %s\n' "https://${DOMAIN_SET}/healthz"
  printf '  Приём из 1С   : %s\n' "https://${DOMAIN_SET}/api/onec/status"
else
  printf '  Веб-интерфейс : %s\n' "http://<IP-сервера>:${HTTP_PORT}/"
  printf '  Health-check  : %s\n' "http://<IP-сервера>:${HTTP_PORT}/healthz"
fi
printf '  Админ логин   : %s\n' "$ADMIN_LOGIN"
if [ "${FRESH:-0}" = "1" ]; then
  printf '  Админ пароль  : %s%s%s\n' "$B" "$ADMIN_PASS_SHOW" "$N"
  printf '  %s(пароль лежит в .env — сохраните его в надёжном месте)%s\n' "$Y" "$N"
else
  printf '  Админ пароль  : см. BOOTSTRAP_ADMIN_PASSWORD в .env\n'
fi
printf '%s\n' "${B}────────────────────────────────────────────────────────${N}"
if [ "${FRESH:-0}" = "1" ]; then
  printf '  Токен для 1С  : %s\n' "$(get_env ONEC_INBOUND_TOKEN)"
  printf '  %s(передайте его 1С-специалисту — см. ONEC-INTEGRATION.md)%s\n' "$Y" "$N"
fi
printf '%s\n' "${B}────────────────────────────────────────────────────────${N}"

printf '\nДальше:\n'
printf '  ./deploy.sh logs      логи\n'
printf '  ./deploy.sh ps        статус\n'
printf '  ./smoke-test.sh       проверка сценариев приёмки\n'
printf '  ./backup.sh           резервная копия\n'
if [ -z "$DOMAIN_SET" ]; then
  printf '\n%sHTTPS не включён: домен не задан.%s Пропишите DOMAIN в .env и\n' "$Y" "$N"
  printf 'запустите ./deploy.sh up — сертификат выпустится автоматически.\n'
fi
printf '\nМобильное приложение: см. MOBILE.md.\n\n'
