#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# DocFlow, патч 2: блок реквизитов над таблицей больше не считается шапкой.
#
# Симптом: строка 0 сетки помечена header=true и содержит «покупателя УНП …
# р/с … СберБанк», а настоящие подписи граф («Наименование», «Ед. измер.»,
# «Кол-во», «Тариф») стоят строкой ниже с header=false. Из-за этого сетка
# отдаёт колонки-мусор, проигрывает проекции по Y и молча отбрасывается.
#
# Правится в двух местах: surya-ocr не приклеивает над таблицей строку, не
# похожую на шапку; бэкенд не верит флагу header на строке из одной ячейки.
#
# Запуск: cd /opt/docflow-deploy && bash patch2-header-row.sh
# ---------------------------------------------------------------------------
set -euo pipefail

ROOT="${DOCFLOW_DIR:-$(pwd)}"
STAMP="$(date +%Y-%m-%d_%H-%M-%S)"
cd "$ROOT"

say() { printf '\n\033[1;33m==> %s\033[0m\n' "$*"; }
ok()  { printf '    \033[0;32m%s\033[0m\n' "$*"; }
warn(){ printf '    \033[0;31m%s\033[0m\n' "$*"; }

[ -f surya-ocr/server.py ] || { warn "нет surya-ocr/server.py — не тот каталог"; exit 1; }

backup() {
  if [ -f "$1" ]; then
    cp -a "$1" "$1.bak.$STAMP"
    ok "бэкап: $1.bak.$STAMP"
  fi
  return 0
}

# ---------------------------------------------------------------------------
say "1/3  surya-ocr: не приклеивать над таблицей строку, не похожую на шапку"
# ---------------------------------------------------------------------------
backup surya-ocr/server.py
python3 - <<'DF_SRV_EOF'
import sys

p = "surya-ocr/server.py"
s = open(p, encoding="utf-8").read()

if "не похожа на шапку" in s:
    print("    уже пропатчен")
    sys.exit(0)

old = """        for ln in sorted(above, key=lambda l: l["y0"])[-max_head:]:
            for col, texts in to_cols(ln).items():
                merged.setdefault(col, []).extend(texts)
        if merged:"""
new = """        for ln in sorted(above, key=lambda l: l["y0"])[-max_head:]:
            for col, texts in to_cols(ln).items():
                merged.setdefault(col, []).extend(texts)
        # Над таблицей не обязательно шапка. На счетах там стоит блок
        # реквизитов покупателя: длинный текст, который целиком ложится в
        # одну графу и растягивается детектором на всю ширину. Приклеив его
        # как шапку, мы задвигаем настоящие подписи граф в строки данных.
        # Признак настоящей шапки простой: подписи короткие и их несколько.
        filled = [c for c, t in merged.items() if " ".join(t).strip()]
        longest = max((len(" ".join(t).strip()) for t in merged.values()), default=0)
        if len(filled) < 2 or longest > 60:
            _dbg("HEADER: строка над таблицей не похожа на шапку, пропускаю:",
                 filled, longest, flush=True)
            merged = {}
        if merged:"""

if old not in s:
    print("!! не нашёл сборку merged в _attach_header_and_totals — правку не применил")
    sys.exit(2)

open(p, "w", encoding="utf-8").write(s.replace(old, new))
print("    _attach_header_and_totals: добавлена проверка шапки")
DF_SRV_EOF

# ---------------------------------------------------------------------------
say "2/3  Go: флаг header на строке из одной ячейки — не шапка"
# ---------------------------------------------------------------------------
backup docflow/internal/recognize/table.go
python3 - <<'DF_TBL_EOF'
import sys

p = "docflow/internal/recognize/table.go"
s = open(p, encoding="utf-8").read()

if "headerFlagged" in s:
    print("    уже пропатчен")
    sys.exit(0)

old = """	headerRow := -1
	for _, c := range best.Cells {
		if c.Row < 0 || c.Col < 0 || c.Row > maxRow || c.Col > maxCol {
			continue
		}"""
new = """	headerRow := -1
	headerFlagged := make(map[int]bool)
	for _, c := range best.Cells {
		if c.Row < 0 || c.Col < 0 || c.Row > maxRow || c.Col > maxCol {
			continue
		}"""
if old not in s:
    print("!! не нашёл начало разбора ячеек"); sys.exit(2)
s = s.replace(old, new)

old = """		if c.Header && (headerRow < 0 || c.Row < headerRow) {
			headerRow = c.Row
		}
	}
"""
new = """		if c.Header {
			headerFlagged[c.Row] = true
		}
	}

	// Флагу header верим не вслепую. Детектор помечает шапкой и блок над
	// таблицей — реквизиты покупателя, р/с, банк: длинный текст, который
	// целиком лежит в одной графе, а остальные семь пустые. Взяв такую
	// строку за шапку, мы уводим настоящие подписи граф в данные, и графы
	// приезжают в 1С без названий. Шапка — это несколько коротких подписей,
	// поэтому строку с одной заполненной ячейкой пропускаем и смотрим ниже.
	for r := 0; r <= maxRow; r++ {
		if !headerFlagged[r] {
			continue
		}
		filled := 0
		for _, v := range grid[r] {
			if strings.TrimSpace(v) != "" {
				filled++
			}
		}
		if filled >= 2 {
			headerRow = r
			break
		}
	}
"""
if old not in s:
    print("!! не нашёл установку headerRow по флагу"); sys.exit(2)
s = s.replace(old, new)

open(p, "w", encoding="utf-8").write(s)
print("    FreeTableFromGrid2: шапка по флагу проверяется на заполненность")
DF_TBL_EOF

# ---------------------------------------------------------------------------
say "3/3  Go: в лог видно, когда сетка отклонена в пользу проекции"
# ---------------------------------------------------------------------------
backup docflow/internal/recognize/pipeline.go
python3 - <<'DF_PIPE_EOF'
import sys

p = "docflow/internal/recognize/pipeline.go"
s = open(p, encoding="utf-8").read()

if "сетка отклонена" in s:
    print("    уже пропатчен")
    sys.exit(0)

old = """	} else if ft == nil {
		p.log.Warn("grid diag: сетка не использована", "причина", GridDiag())
	}"""
new = """	} else if ft == nil {
		p.log.Warn("grid diag: сетка не использована", "причина", GridDiag())
	} else {
		// Сетка собралась, но проиграла проекции по координатам. Раньше эта
		// ветка молчала, и в логе было не отличить «сетки нет» от «сетка есть,
		// но её не взяли».
		p.log.Info("grid diag: сетка отклонена в пользу проекции",
			"строк_в_сетке", len(ft.Rows), "колонок_в_сетке", len(ft.Columns),
			"шапка_сетки", strings.Join(ft.Columns, " | "))
	}"""
if old not in s:
    print("!! не нашёл ветку GridDiag"); sys.exit(2)

open(p, "w", encoding="utf-8").write(s.replace(old, new))
print("    добавлен лог отклонённой сетки")
DF_PIPE_EOF

if command -v gofmt >/dev/null 2>&1; then gofmt -w docflow/internal/recognize/ && ok "gofmt пройден"; fi
if command -v go >/dev/null 2>&1; then ( cd docflow && go build ./... ) && ok "go build прошёл"; fi

cat <<'DF_NEXT_EOF'

Дальше:

  docker compose build docflow-backend surya-ocr
  COMPOSE_PROFILES=ai,ai-surya docker compose up -d
  docker compose logs -f docflow-backend | grep -E "grid diag|раздельный разбор"

Теперь в логе должно быть:
  grid diag: ячейка  row=0 ... text="Наименование"      <- шапка та самая
  grid diag: таблица собрана  columns=8 rows=N
  контекст для модели  ... источник_таблицы=grid         <- сетка победила

Если снова "сетка отклонена в пользу проекции" — сравни числа строк и
колонок в логе с бумагой и покажи их.
DF_NEXT_EOF
