#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# DocFlow, патч 3: сетка от table_rec больше не проигрывает проекции по Y.
#
# В логе было видно, что сетка собралась правильно:
#   сетка отклонена в пользу проекции  строк_в_сетке=2 колонок_в_сетке=8
#   шапка_сетки="Наименование | Ед. измер. | Кол-во | Тариф | Стоимость ..."
# и всё равно отбрасывалась. Виновато правило «побеждает та таблица, где
# больше строк и колонок»: проекция по Y всегда богаче, потому что рвёт
# наименование на строки и режет «Стоимость всего без НДС, руб.» на две
# графы. То есть правило систематически выбирало сломанный вариант.
#
# Теперь при TABLE_PRIORITY=grid сетка берётся всегда, если она собралась.
#
# Запуск: cd /opt/docflow-deploy && bash patch3-gridwins.sh
# ---------------------------------------------------------------------------
set -euo pipefail

ROOT="${DOCFLOW_DIR:-$(pwd)}"
STAMP="$(date +%Y-%m-%d_%H-%M-%S)"
cd "$ROOT"

say() { printf '\n\033[1;33m==> %s\033[0m\n' "$*"; }
ok()  { printf '    \033[0;32m%s\033[0m\n' "$*"; }
warn(){ printf '    \033[0;31m%s\033[0m\n' "$*"; }

[ -f docflow/internal/recognize/refine_split.go ] || { warn "нет refine_split.go — сначала первый патч"; exit 1; }

backup() {
  if [ -f "$1" ]; then
    cp -a "$1" "$1.bak.$STAMP"
    ok "бэкап: $1.bak.$STAMP"
  fi
  return 0
}

say "gridWins: сетка выигрывает всегда, когда собралась"
backup docflow/internal/recognize/refine_split.go
python3 - <<'DF_GW_EOF'
import sys

p = "docflow/internal/recognize/refine_split.go"
s = open(p, encoding="utf-8").read()

if "сравнение «кто богаче» убрано" in s:
    print("    уже пропатчен")
    sys.exit(0)

old = """	if !tableIsGeometric(current) {
		return true
	}
	// Обе таблицы геометрические. Сетку не берём, только если она заметно
	// беднее: меньше и строк, и колонок — тогда детектор нашёл не ту рамку.
	cur := current.RawCells
	if cur == nil {
		return true
	}
	if len(grid.Rows) < len(cur.Rows) && len(grid.Columns) < len(cur.Columns) {
		return false
	}
	return true
}"""
new = """	// Сравнение «кто богаче» убрано намеренно. Проекция по Y всегда даёт
	// больше строк и колонок, чем сетка: наименование, перенесённое внутри
	// ячейки, она считает несколькими позициями, а шапку «Стоимость всего
	// без НДС, руб.» режет по зазору на две графы. Побеждала, соответственно,
	// всегда она — то есть ровно тот разбор, от которого мы уходили.
	// Сетка знает границы граф от модели разметки, и если она собралась,
	// брать надо её.
	return len(grid.Columns) >= 2
}"""

if old not in s:
    print("!! не нашёл тело gridWins — правку не применил")
    sys.exit(2)

open(p, "w", encoding="utf-8").write(s.replace(old, new))
print("    gridWins упрощён: две колонки и хотя бы одна строка — берём сетку")
DF_GW_EOF

if command -v gofmt >/dev/null 2>&1; then gofmt -w docflow/internal/recognize/ && ok "gofmt пройден"; fi
if command -v go >/dev/null 2>&1; then ( cd docflow && go build ./... ) && ok "go build прошёл"; fi

cat <<'DF_NEXT_EOF'

Дальше:

  docker compose build docflow-backend
  COMPOSE_PROFILES=ai,ai-surya docker compose up -d
  docker compose logs -f docflow-backend | grep -E "grid diag: таблица собрана|источник_таблицы"

Ждём в логе:
  grid diag: таблица собрана  columns=8 rows=2
  контекст для модели ... источник_таблицы=grid

В карточке документа под таблицей подпись должна смениться с
«по колонкам документа» на разбор по сетке, колонок стать 8, а
наименование позиции — одной строкой, а не тремя.

Откат: TABLE_PRIORITY=columns в .env возвращает прежнее поведение
без пересборки (нужен только рестарт бэкенда).
DF_NEXT_EOF
