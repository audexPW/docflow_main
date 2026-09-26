#!/usr/bin/env bash
# =============================================================================
# DocFlow — перевод языковой модели (llm) на видеокарту
#
#   cd /opt/docflow-deploy
#   bash gpu-llm.sh            # проверить, переключить, замерить, при сбое откатить
#   bash gpu-llm.sh --rollback # вернуть модель на процессор
#   bash gpu-llm.sh --surya    # только проверить, может ли surya-ocr работать на карте
#
# Что делает:
#   1. Проверяет драйвер NVIDIA на хосте и доступ Docker к карте.
#   2. Замеряет скорость модели на процессоре (короткий запрос).
#   3. Меняет в docker-compose.yml образ llm на сборку с CUDA, добавляет
#      выгрузку всех слоёв на карту (-ngl) и выдачу GPU контейнеру.
#   4. Пересоздаёт только контейнер llm, ждёт healthy, проверяет, что слои
#      действительно на карте, и замеряет скорость ещё раз.
#   5. Если любой шаг не прошёл — возвращает прежний docker-compose.yml и
#      контейнер на процессоре.
#
# Бэкенд, база и surya-ocr не трогаются. Пока модель перезапускается (1–2 мин),
# распознавание документов не работает — запускать, когда никто не грузит.
# =============================================================================
set -uo pipefail
cd /opt/docflow-deploy 2>/dev/null || { echo "нет каталога /opt/docflow-deploy"; exit 1; }
say() { printf '\n\033[1m%s\033[0m\n' "$*"; }
err() { printf '\033[31m%s\033[0m\n' "$*" >&2; }
ok()  { printf '\033[32m%s\033[0m\n' "$*"; }

LLM=docflow-deploy-llm-1
BACKEND=docflow-deploy-docflow-backend-1
CUDA_IMG=ghcr.io/ggml-org/llama.cpp:server-cuda

bench() { # печатает «токенов/с генерации; время запроса»
  docker run --rm --network container:$BACKEND curlimages/curl -s -m 300 \
    -H 'Content-Type: application/json' \
    -d '{"model":"local","temperature":0,"max_tokens":120,"messages":[{"role":"user","content":"Перечисли десять реквизитов счёта-фактуры, по одному в строке."}]}' \
    -w '\nTIME=%{time_total}\n' http://llm:8081/v1/chat/completions 2>/dev/null |
  python3 -c '
import sys,json,re
raw=sys.stdin.read(); t=re.search(r"TIME=([\d.]+)",raw)
body=raw[:raw.rfind("TIME=")]
try:
  d=json.loads(body); tm=d.get("timings",{})
  print("генерация %.1f ток/с, разбор запроса %.1f ток/с, запрос %.1f с" % (tm.get("predicted_per_second",0), tm.get("prompt_per_second",0), float(t.group(1))))
except Exception:
  print("нет ответа от модели"); sys.exit(1)'
}

wait_healthy() {
  for i in $(seq 1 60); do
    st=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $LLM 2>/dev/null)
    [[ "$st" == healthy ]] && return 0
    [[ "$(docker inspect --format '{{.State.Status}}' $LLM 2>/dev/null)" == exited ]] && return 1
    sleep 5
  done
  return 1
}

if [[ "${1:-}" == --surya ]]; then
  say "surya-ocr: какая сборка torch и видит ли она карту"
  docker exec docflow-deploy-surya-ocr-1 python -c 'import torch;print("torch",torch.__version__,"cuda",torch.version.cuda,"видит карту:",torch.cuda.is_available())' 2>&1
  echo; head -5 surya-ocr/Dockerfile 2>/dev/null
  echo "Если cuda None — образ собран под процессор, для карты его нужно пересобрать с torch под CUDA."
  exit 0
fi

if [[ "${1:-}" == --rollback ]]; then
  last=$(ls -1 docker-compose.yml.bak-gpu-* 2>/dev/null | head -n1)
  [[ -n "$last" ]] || { err "резервной копии docker-compose.yml.bak-gpu-* нет"; exit 1; }
  cp -p "$last" docker-compose.yml && docker compose up -d --no-deps llm && ok "llm возвращена на процессор ($last)"
  exit 0
fi

say "1. Драйвер NVIDIA на хосте"
if ! nvidia-smi --query-gpu=name,driver_version,memory.total --format=csv,noheader; then
  err "nvidia-smi не работает: драйвер не установлен или не загружен. Ничего не изменено."
  exit 1
fi

say "2. Доступ Docker к карте"
docker pull -q $CUDA_IMG >/dev/null || { err "не удалось скачать $CUDA_IMG. Ничего не изменено."; exit 1; }
if ! docker run --rm --gpus all --entrypoint nvidia-smi $CUDA_IMG -L; then
  err "Docker не выдаёт карту контейнерам: нужен nvidia-container-toolkit
  (apt install nvidia-container-toolkit && nvidia-ctk runtime configure --runtime=docker && systemctl restart docker).
Ничего не изменено."
  exit 1
fi

if grep -q "llama.cpp:server-cuda" docker-compose.yml; then
  ok "В docker-compose.yml уже стоит сборка с CUDA — переключение не требуется."
  docker logs $LLM 2>&1 | grep -iE "offloaded|CUDA0" | tail -3
  exit 0
fi

say "3. Скорость на процессоре (до)"
BEFORE=$(bench) || BEFORE="не замерено"
echo "$BEFORE"

say "4. Правка docker-compose.yml"
BK=docker-compose.yml.bak-gpu-$(date +%Y%m%d-%H%M%S)
cp -p docker-compose.yml "$BK"
python3 - <<'PYEOF' || { err "docker-compose.yml отличается от ожидаемого — ничего не изменено"; rm -f "$BK"; exit 1; }
p = 'docker-compose.yml'
s = open(p, encoding='utf-8').read()
edits = [
    ("    image: ghcr.io/ggml-org/llama.cpp:server\n",
     "    image: ghcr.io/ggml-org/llama.cpp:server-cuda\n"),
    ("      -np ${LLM_PARALLEL:-2}\n",
     "      -np ${LLM_PARALLEL:-2}\n      -ngl ${LLM_GPU_LAYERS:-99}\n"),
    ("    mem_limit: ${LLM_MEM_LIMIT:-6g}\n",
     "    mem_limit: ${LLM_MEM_LIMIT:-6g}\n"
     "    deploy:\n"
     "      resources:\n"
     "        reservations:\n"
     "          devices:\n"
     "            - driver: nvidia\n"
     "              count: all\n"
     "              capabilities: [gpu]\n"),
]
for old, new in edits:
    if s.count(old) != 1:
        raise SystemExit(1)
    s = s.replace(old, new)
open(p, 'w', encoding='utf-8').write(s)
print("образ llm: server-cuda, слои на карту: -ngl 99, GPU выдан контейнеру")
PYEOF

rollback() {
  err "$1"
  err "Откат: возвращаю прежний docker-compose.yml ($BK)"
  docker logs --tail 30 $LLM 2>&1 | sed 's/^/  llm| /'
  cp -p "$BK" docker-compose.yml
  docker compose up -d --no-deps llm >/dev/null 2>&1
  wait_healthy && ok "llm снова работает на процессоре" || err "llm после отката не поднялась — пришлите: docker logs --tail 80 $LLM"
  exit 1
}

docker compose config -q || rollback "docker compose не принял файл"

say "5. Перезапуск llm на карте"
docker compose up -d --no-deps llm || rollback "контейнер не создан"
wait_healthy || rollback "llm не стала healthy за 5 минут"

# Новые сборки llama.cpp при уровне журнала по умолчанию не пишут строку
# «offloaded N/N layers», поэтому выгрузку проверяем по памяти карты: модель
# 7B в Q4_K_M с контекстом занимает на карте больше 4 ГБ, а без выгрузки
# сервер карту почти не трогает.
docker logs $LLM 2>&1 | grep -iE "cuda|gpu|offload|vram|device" | tail -5 | sed 's/^/  llm| /'
USED=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits | head -1 | tr -d ' ')
nvidia-smi --query-gpu=memory.used,memory.total,utilization.gpu --format=csv,noheader
if [[ -z "$USED" || "$USED" -lt 3000 ]]; then
  rollback "на карте занято ${USED:-?} МиБ — модель на карту не выгрузилась"
fi
ok "на карте занято $USED МиБ — модель выгружена"

say "6. Скорость на карте (после)"
AFTER=$(bench) || rollback "модель на карте не отвечает на запрос"
echo "до:    $BEFORE"
echo "после: $AFTER"
G_BEFORE=$(echo "$BEFORE" | grep -oE 'генерация [0-9.]+' | grep -oE '[0-9.]+')
G_AFTER=$(echo "$AFTER" | grep -oE 'генерация [0-9.]+' | grep -oE '[0-9.]+')
if [[ -n "$G_BEFORE" && -n "$G_AFTER" ]] && python3 -c "import sys; sys.exit(0 if float('$G_AFTER') < float('$G_BEFORE')*1.5 else 1)"; then
  rollback "генерация не ускорилась ($G_BEFORE → $G_AFTER ток/с) — карта фактически не используется"
fi

ok "Готово. Модель работает на видеокарте. Откат: bash gpu-llm.sh --rollback"
echo "Резервная копия: $BK"
