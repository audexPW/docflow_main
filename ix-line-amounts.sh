#!/usr/bin/env bash
# DocFlow: «сумма не залетела в файл».
#
# В строке таблицы две денежные графы — «Сумма» (43.09, без НДС) и «Всего с НДС»
# (51.71). Обе получают роль amount, а FreeTableToLines оставляла из них только
# последнюю: в <Line> уезжал итог с налогом, сумма без НДС пропадала. Для 1С это
# не мелочь — <Amount>51.71</Amount> рядом с <VAT>8.62</VAT> приёмник разложит с
# двойным счётом налога.
#
# Патч перестаёт выбрасывать графы: строка отдаёт обе суммы — Amount (итог по
# строке) и AmountNoVAT (без налога). Какая графа какая, определяем по подписи
# («с НДС» / «без НДС»), а если подписи неоднозначны — по арифметике
# (сумма без НДС + НДС = итог).
set -euo pipefail

ROOT="${1:-$(pwd)}"
GO="$ROOT/docflow/internal"
[ -f "$GO/recognize/table.go" ] || { echo "Не найден $GO/recognize/table.go — запусти из каталога docflow-deploy"; exit 1; }

STAMP=$(date +%Y%m%d-%H%M%S)
for f in "$GO/domain/models.go" "$GO/recognize/table.go" "$GO/onec/file.go"; do
  cp "$f" "$f.bak-$STAMP"
done
echo "Бэкапы: *.bak-$STAMP"

cat > "$GO/recognize/amounts.go" <<'GOEOF'
package recognize

import (
	"math"
	"strconv"
	"strings"

	"docflow/internal/domain"
)

// Денежные графы строки: «Сумма», «Стоимость без НДС», «Всего с НДС» — все они
// получают роль amount. Раньше строка забирала из них последнюю, и сумма без
// налога терялась по дороге в 1С: в файле оставалось <Amount> с налогом рядом с
// <VAT>, то есть налог считался дважды.
//
// Теперь строка везёт обе величины. Итог определяем по подписи графы, а когда
// подпись ничего не говорит («Сумма» и «Всего») — по арифметике: сумма без
// налога плюс НДС даёт итог. Проверка по числам надёжнее подписи, потому что
// OCR подписи ломает чаще, чем цифры.

type amountCell struct {
	title string
	value string
}

// amountTolerance — допуск при сверке «без НДС + НДС = итог». Копейка на
// округление ставки, ещё копейка на округление в самом бланке.
const amountTolerance = 0.02

func titleWithVAT(t string) bool {
	t = strings.ToLower(t)
	return strings.Contains(t, "с ндс") || strings.Contains(t, "включая")
}

func titleWithoutVAT(t string) bool {
	return strings.Contains(strings.ToLower(t), "без ндс")
}

func amountFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(normalizeNumber(s), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// amountsReconcile — сходится ли «без НДС + НДС = итог».
func amountsReconcile(noVAT, vat, total string) bool {
	a, ok1 := amountFloat(noVAT)
	v, ok2 := amountFloat(vat)
	t, ok3 := amountFloat(total)
	if !ok1 || !ok2 || !ok3 {
		return false
	}
	return math.Abs(a+v-t) <= amountTolerance
}

// applyAmounts раскладывает денежные графы строки по Amount и AmountNoVAT.
// Порядок разбора: подпись «с НДС» → итог; подпись «без НДС» → сумма без
// налога; если подписи молчат — итогом считаем последнюю графу (в бланках она
// правее), а суммой без налога ту, что сходится с ней по НДС.
func applyAmounts(item *domain.LineItem, cells []amountCell) {
	if len(cells) == 0 {
		return
	}

	total := -1
	for i, c := range cells {
		if titleWithVAT(c.title) {
			total = i
			break
		}
	}
	if total < 0 {
		total = len(cells) - 1
	}
	item.Amount = cells[total].value

	noVAT := -1
	for i, c := range cells {
		if i != total && titleWithoutVAT(c.title) {
			noVAT = i
			break
		}
	}
	if noVAT < 0 {
		for i, c := range cells {
			if i != total && amountsReconcile(c.value, item.VAT, cells[total].value) {
				noVAT = i
				break
			}
		}
	}
	// Ни подписи, ни арифметики: берём первую оставшуюся графу. Потерять её в
	// файле хуже, чем отдать приёмнику лишнее число — 1С покажет его бухгалтеру.
	if noVAT < 0 {
		for i := range cells {
			if i != total {
				noVAT = i
				break
			}
		}
	}
	if noVAT >= 0 {
		item.AmountNoVAT = cells[noVAT].value
	}
}

// columnTitle — подпись графы по номеру (свободная таблица).
func columnTitle(cols []string, i int) string {
	if i >= 0 && i < len(cols) {
		return cols[i]
	}
	return ""
}

// tableColumnTitle — подпись графы по номеру колонки в domain.Table.
// Index в колонках начинается с единицы, как и Column в ячейках.
func tableColumnTitle(cols []domain.TableColumn, column int) string {
	for _, c := range cols {
		if c.Index == column {
			return c.Title
		}
	}
	return ""
}
GOEOF
echo "Добавлен recognize/amounts.go"

python3 - "$GO" <<'PY'
import sys, io, os

base = sys.argv[1]

def patch(path, pairs):
    p = os.path.join(base, path)
    src = io.open(p, encoding='utf-8').read()
    for old, new in pairs:
        if new.strip() and new.split('\n')[0].strip() in src and old not in src:
            print(f'{path}: уже пропатчен, пропускаю фрагмент')
            continue
        assert old in src, f'{path}: не найден фрагмент:\n{old[:120]}'
        src = src.replace(old, new, 1)
    io.open(p, 'w', encoding='utf-8').write(src)
    print(f'{path}: ок')

# --- 1. Строка таблицы везёт обе суммы -------------------------------------
patch('domain/models.go', [(
	'\tAmount string `json:"amount,omitempty"` // сумма по строке\n',
	'\tAmount string `json:"amount,omitempty"` // сумма по строке (итог, с НДС)\n'
	'\t// AmountNoVAT — та же строка без налога. В бланке это отдельная графа\n'
	'\t// («Сумма», «Стоимость без НДС»), и без неё приёмник в 1С вынужден\n'
	'\t// вычитать НДС сам, а при округлении расходится с документом.\n'
	'\tAmountNoVAT string `json:"amount_no_vat,omitempty"`\n',
)])

# --- 2. Разбор свободной таблицы -------------------------------------------
patch('recognize/table.go', [
    (
	'\t\tvar item domain.LineItem\n'
	'\t\tvar extraName []string\n'
	'\t\tvatIsAmount := false\n',
	'\t\tvar item domain.LineItem\n'
	'\t\tvar extraName []string\n'
	'\t\tvar amounts []amountCell\n'
	'\t\tvatIsAmount := false\n',
    ),
    (
	'\t\t\tcase "amount":\n'
	'\t\t\t\t// Из нескольких «сумм» в 1С уходит последняя — в бухгалтерских\n'
	'\t\t\t\t// формах это сумма с НДС, итог по строке.\n'
	'\t\t\t\titem.Amount = normalizeNumber(v)\n',
	'\t\t\tcase "amount":\n'
	'\t\t\t\t// Денежные графы копим все до единой. Раньше здесь оставалась\n'
	'\t\t\t\t// только последняя, и «Сумма» без НДС не доезжала до файла.\n'
	'\t\t\t\tamounts = append(amounts, amountCell{\n'
	'\t\t\t\t\ttitle: columnTitle(ft.Columns, i),\n'
	'\t\t\t\t\tvalue: normalizeNumber(v),\n'
	'\t\t\t\t})\n',
    ),
    (
	'\t\t}\n'
	'\t\tif item.Name == "" {\n'
	'\t\t\tcontinue\n'
	'\t\t}\n'
	'\t\tif len(extraName) > 0 {\n',
	'\t\t}\n'
	'\t\t// НДС по строке уже разобран, поэтому денежные графы раскладываем\n'
	'\t\t// после цикла: итог отличаем от суммы без налога по арифметике.\n'
	'\t\tapplyAmounts(&item, amounts)\n'
	'\t\tif item.Name == "" {\n'
	'\t\t\tcontinue\n'
	'\t\t}\n'
	'\t\tif len(extraName) > 0 {\n',
    ),
    (
	'\t\tvar item domain.LineItem\n'
	'\t\tfor _, c := range row.Cells {\n',
	'\t\tvar item domain.LineItem\n'
	'\t\tvar amounts []amountCell\n'
	'\t\tfor _, c := range row.Cells {\n',
    ),
    (
	'\t\t\tcase "amount":\n'
	'\t\t\t\titem.Amount = normalizeNumber(v)\n'
	'\t\t\tcase "vat":\n'
	'\t\t\t\titem.VAT = normalizeNumber(v)\n'
	'\t\t\t}\n'
	'\t\t}\n'
	'\t\tif item.Name == "" {\n'
	'\t\t\tcontinue\n'
	'\t\t}\n'
	'\t\tout = append(out, item)\n',
	'\t\t\tcase "amount":\n'
	'\t\t\t\tamounts = append(amounts, amountCell{\n'
	'\t\t\t\t\ttitle: tableColumnTitle(t.Columns, c.Column),\n'
	'\t\t\t\t\tvalue: normalizeNumber(v),\n'
	'\t\t\t\t})\n'
	'\t\t\tcase "vat":\n'
	'\t\t\t\titem.VAT = normalizeNumber(v)\n'
	'\t\t\t}\n'
	'\t\t}\n'
	'\t\tapplyAmounts(&item, amounts)\n'
	'\t\tif item.Name == "" {\n'
	'\t\t\tcontinue\n'
	'\t\t}\n'
	'\t\tout = append(out, item)\n',
    ),
])

# --- 3. Обе суммы в XML ------------------------------------------------------
patch('onec/file.go', [
    (
	'\tAmount  string `xml:"Amount,omitempty"`\n'
	'\tVAT     string `xml:"VAT,omitempty"`\n',
	'\tAmount  string `xml:"Amount,omitempty"`\n'
	'\t// AmountNoVAT — сумма строки без налога, как она напечатана в бланке.\n'
	'\t// Приёмник берёт её как есть, вместо того чтобы вычитать НДС из итога.\n'
	'\tAmountNoVAT string `xml:"AmountNoVAT,omitempty"`\n'
	'\tVAT     string `xml:"VAT,omitempty"`\n',
    ),
    (
	'\t\t\tName:    l.Name, Qty: l.Qty, Unit: l.Unit,\n'
	'\t\t\tPrice: l.Price, Amount: l.Amount, VAT: l.VAT,\n',
	'\t\t\tName:    l.Name, Qty: l.Qty, Unit: l.Unit,\n'
	'\t\t\tPrice: l.Price, Amount: l.Amount, AmountNoVAT: l.AmountNoVAT, VAT: l.VAT,\n',
    ),
])
PY

cd "$ROOT/docflow"
if command -v gofmt >/dev/null; then gofmt -w internal/recognize/amounts.go internal/recognize/table.go internal/onec/file.go internal/domain/models.go; fi
if command -v go >/dev/null; then
  echo "Проверяю сборку и тесты…"
  go build ./... && go test ./internal/recognize/... ./internal/onec/... || { echo "СБОРКА УПАЛА — откати из .bak-$STAMP"; exit 1; }
fi

cd "$ROOT"
echo "Пересобираю бэкенд…"
docker compose build docflow-backend
docker compose up -d docflow-backend
echo
echo "Готово. Перераспознай IMG_0003.jpg и проверь <Line>: должны быть и AmountNoVAT, и Amount."
