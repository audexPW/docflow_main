#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# DocFlow, патч 4: три независимые правки после того, как заработала сетка.
#
#  1. Пустые сетки. Детектор иногда размечает рамку бланка или поле для
#     печати: строки и колонки есть, текста нет. Такая «таблица» проходила
#     все проверки и уходила в 1С пустой.
#  2. Ряд номеров граф («2 3 4» под шапкой) попадал в таблицу как позиция,
#     когда OCR прочитал не все цифры ряда.
#  3. Стороны документа. Контрагентом ставился покупатель вместо продавца,
#     а в организацию попадал обрывок строки банка.
#
# Запуск: cd /opt/docflow-deploy && bash patch4-cleanup.sh
# ---------------------------------------------------------------------------
set -euo pipefail

ROOT="${DOCFLOW_DIR:-$(pwd)}"
STAMP="$(date +%Y-%m-%d_%H-%M-%S)"
cd "$ROOT"

say() { printf '\n\033[1;33m==> %s\033[0m\n' "$*"; }
ok()  { printf '    \033[0;32m%s\033[0m\n' "$*"; }
warn(){ printf '    \033[0;31m%s\033[0m\n' "$*"; }

[ -f docflow/internal/recognize/table.go ] || { warn "не тот каталог"; exit 1; }

backup() {
  if [ -f "$1" ]; then
    cp -a "$1" "$1.bak.$STAMP"
    ok "бэкап: $1.bak.$STAMP"
  fi
  return 0
}

# ---------------------------------------------------------------------------
say "1/3  Пустая сетка больше не считается таблицей"
# ---------------------------------------------------------------------------
backup docflow/internal/recognize/table.go
python3 - <<'DF_T1_EOF'
import sys

p = "docflow/internal/recognize/table.go"
s = open(p, encoding="utf-8").read()

if "сетка почти пустая" in s:
    print("    уже пропатчен")
    sys.exit(0)

old = '''	ft.Rows = dropDegenerateRows(ft.Rows)
	if len(ft.Rows) == 0 {
		gridDiag = "шапка найдена, но ниже неё нет строк с данными"
		return nil
	}
	gridDiag = ""
	return ft
}'''
new = '''	ft.Rows = dropDegenerateRows(ft.Rows)
	if len(ft.Rows) == 0 {
		gridDiag = "шапка найдена, но ниже неё нет строк с данными"
		return nil
	}

	// Сетка из пустых клеток. Детектор размечает как таблицу и рамку бланка,
	// и поле для печати, и блок подписей: строки с колонками там есть, а
	// текста в них нет. Раньше такая таблица проходила все проверки и уходила
	// в 1С пустой табличной частью — документ выглядел распознанным, а
	// позиций в нём не было ни одной.
	filled, numeric := 0, 0
	for _, r := range ft.Rows {
		for _, v := range r {
			if strings.TrimSpace(v) == "" {
				continue
			}
			filled++
			if reNumberish.MatchString(v) {
				numeric++
			}
		}
	}
	total := len(ft.Rows) * len(cols)
	if total > 0 && float64(filled)*100 < float64(total)*15 {
		gridDiag = fmt.Sprintf("сетка почти пустая: заполнено %d клеток из %d", filled, total)
		return nil
	}
	// В товарной таблице всегда есть числа — количество, цена или сумма.
	// Их полное отсутствие означает, что размечен блок текста, а не таблица.
	if numeric == 0 {
		gridDiag = "в строках сетки нет ни одного числа — это не товарная таблица"
		return nil
	}

	gridDiag = ""
	return ft
}'''

if old not in s:
    print("!! не нашёл хвост FreeTableFromGrid2")
    sys.exit(2)
s = s.replace(old, new)

head, rest = s.split("import (", 1)
block, tail = rest.split(")", 1)
if '"fmt"' not in block:
    block = '\n\t"fmt"' + block
    s = head + "import (" + block + ")" + tail
    print("    добавлен импорт fmt")

open(p, "w", encoding="utf-8").write(s)
print("    FreeTableFromGrid2: пустая сетка отбраковывается")
DF_T1_EOF

# ---------------------------------------------------------------------------
say "2/3  Ряд номеров граф не попадает в позиции"
# ---------------------------------------------------------------------------
backup docflow/internal/recognize/columns.go
python3 - <<'DF_T2_EOF'
import sys

p = "docflow/internal/recognize/columns.go"
s = open(p, encoding="utf-8").read()

if "Порог два, а не три" in s:
    print("    уже пропатчен")
    sys.exit(0)

old = '''	return n >= 3 && prev <= len(cells)+2
}'''
new = '''	// Порог два, а не три. Ряд «1 2 3 4 5 6 7 8» под шапкой OCR прочитал не
	// все цифры ряда — в логах он приходил как «2 3 4» и даже как «2 3», и
	// тогда проверка не срабатывала, а ряд вставал в таблицу первой позицией
	// с количеством 2 и ценой 3. Остальные условия жёсткие: только цифры,
	// строго по возрастанию, наибольшее значение не больше числа граф —
	// настоящая позиция так выглядеть не может, в ней есть наименование.
	return n >= 2 && prev <= len(cells)+2
}'''

if old not in s:
    print("!! не нашёл хвост isNumberingRow")
    sys.exit(2)

open(p, "w", encoding="utf-8").write(s.replace(old, new))
print("    isNumberingRow: порог снижен до двух цифр")
DF_T2_EOF

# ---------------------------------------------------------------------------
say "3/3  Промпт реквизитов: продавец и покупатель не путаются"
# ---------------------------------------------------------------------------
backup docflow/internal/recognize/refine_split.go
python3 - <<'DF_T3_EOF'
import sys

p = "docflow/internal/recognize/refine_split.go"
s = open(p, encoding="utf-8").read()

if "Стороны определяй по подписям" in s:
    print("    уже пропатчен")
    sys.exit(0)

pairs = [
    ('"organization_unp": "налоговый номер получателя документа",',
     '"organization_unp": "налоговый номер получателя — тот, что подписан «УНП покупателя» или «УНП заказчика»",'),
    ('fieldSchema["unp"] = "УНП контрагента (Беларусь, ровно 9 цифр)"',
     'fieldSchema["unp"] = "УНП продавца — тот, что подписан «УНП продавца» или «УНП поставщика» (Беларусь, ровно 9 цифр)"'),
]
for old, new in pairs:
    if old in s:
        s = s.replace(old, new)

anchor = 'b.WriteString("Итоговую сумму бери из строки итога или из суммы прописью, ставку НДС не путай с суммой НДС.\\n")'
if anchor not in s:
    print("!! не нашёл строку про итоговую сумму в buildFieldsPrompt")
    sys.exit(2)

extra = anchor + '''
	// Стороны модель путала стабильно: в счёте-фактуре продавец напечатан
	// первым, покупатель ниже, и контрагентом бралась сторона, стоящая ближе
	// к концу текста. Плюс в организацию заезжала строка банка («в банке ОАО
	// ...», «Дирекция ...»): она идёт сразу после названия стороны и выглядит
	// как продолжение наименования.
	b.WriteString("Стороны определяй по подписям в документе, а не по порядку строк.\\n")
	b.WriteString("Контрагент — тот, кто выставил документ: продавец, поставщик, исполнитель, арендодатель.\\n")
	b.WriteString("Организация — тот, кому документ выставлен: покупатель, плательщик, заказчик, арендатор.\\n")
	b.WriteString("Каждой стороне бери её собственный налоговый номер: у продавца — УНП продавца, у покупателя — УНП покупателя.\\n")
	b.WriteString("В наименования сторон не бери названия банков и строки реквизитов: «в банке», «р/с», «Дирекция», BIC и номер счёта стороной документа не являются.\\n")'''

open(p, "w", encoding="utf-8").write(s.replace(anchor, extra))
print("    buildFieldsPrompt: добавлены правила сторон")
DF_T3_EOF

if command -v gofmt >/dev/null 2>&1; then gofmt -w docflow/internal/recognize/ && ok "gofmt пройден"; fi
if command -v go >/dev/null 2>&1; then ( cd docflow && go build ./... ) && ok "go build прошёл"; fi

cat <<'DF_NEXT_EOF'

Дальше:

  docker compose build docflow-backend
  docker compose up -d --force-recreate docflow-backend

Проверить через полчаса очереди:

  docker compose logs --since 30m docflow-backend | grep -o '"причина":"[^"]*"' | sort | uniq -c

Пустые сетки теперь видны отдельной причиной («сетка почти пустая» или
«нет ни одного числа») и уходят на разбор проекцией, а не пустышкой в 1С.

Стороны смотреть глазами в карточке: контрагент — продавец (тот, кто
выставил документ), организация — покупатель.
DF_NEXT_EOF
