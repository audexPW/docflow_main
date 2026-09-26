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
	return strings.Contains(t, "с ндс") || strings.Contains(t, "включая") ||
		strings.Contains(t, "с учетом ндс") || strings.Contains(t, "с учётом ндс")
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
