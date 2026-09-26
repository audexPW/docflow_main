package recognize

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Признак таблицы по самим строкам.
//
// Замечание проверяющего от 11.09.2026: на счетах ЖКХ по общежитию и 370
// таблицу не узнало. У ЖКХ шапка в две строки, скан под углом, и строка с УНП
// над таблицей влезает в заголовок — по одной чистой строке шапки таблицу не
// поймать. Надёжнее признак по самим строкам: если в строке три-четыре числа,
// стоящих по колонкам, это строка таблицы, даже без шапки.
//
// Границы задаются отдельно: таблица начинается с шапки и заканчивается
// строкой «Итого». Иначе снизу затягивает строку суммы прописью, а сверху —
// текст договора.

var reNumericCellRow = regexp.MustCompile(`^[-(]?\d[\d \x{00A0}]*(?:[.,]\d+)?\)?%?$`)

// numericCells — числовые ячейки строки, по левому краю. Номер позиции («1»),
// стоящий у самого левого края, не считается: он есть и в тексте договора.
func numericCells(r tokenRow) []float64 {
	var xs []float64
	for _, t := range r.Toks {
		for _, w := range strings.Fields(t.Text) {
			w = strings.Trim(w, "|")
			if reNumericCellRow.MatchString(w) {
				xs = append(xs, t.X0)
				break
			}
		}
	}
	sort.Float64s(xs)
	return xs
}

// numericRowsBand ищет полосу таблицы по строкам с числами, стоящими по
// колонкам. Возвращает первый и последний ряд данных; -1, если полосы нет.
func numericRowsBand(rows []tokenRow) (first, last int) {
	if len(rows) < 2 {
		return -1, -1
	}
	cells := make([][]float64, len(rows))
	for i, r := range rows {
		cells[i] = numericCells(r)
	}
	var widths []float64
	for _, r := range rows {
		for _, t := range r.Toks {
			if n := len([]rune(t.Text)); n > 0 && t.X1 > t.X0 {
				widths = append(widths, (t.X1-t.X0)/float64(n))
			}
		}
	}
	cw := median(widths)
	if cw <= 0 {
		cw = 10
	}
	// Скан под углом: колонка гуляет по горизонтали от строки к строке,
	// поэтому совпадением считаем попадание в пределах нескольких символов.
	tol := cw * 4

	aligned := func(a, b []float64) int {
		hit := 0
		for _, x := range a {
			for _, y := range b {
				if math.Abs(x-y) <= tol {
					hit++
					break
				}
			}
		}
		return hit
	}

	bestFirst, bestLast, bestLen := -1, -1, 0
	for i := 0; i < len(rows); i++ {
		if len(cells[i]) < 3 {
			continue
		}
		j := i
		for j+1 < len(rows) {
			next := j + 1
			// Строка без чисел внутри полосы допустима: перенос наименования
			// на вторую строку. Две подряд — полоса кончилась.
			if len(cells[next]) < 3 {
				if next+1 < len(rows) && len(cells[next+1]) >= 3 && aligned(cells[i], cells[next+1]) >= 3 {
					j = next + 1
					continue
				}
				break
			}
			if aligned(cells[j], cells[next]) < 3 {
				break
			}
			j = next
		}
		if n := j - i + 1; n >= 2 && n > bestLen {
			bestFirst, bestLast, bestLen = i, j, n
		}
	}
	return bestFirst, bestLast
}

var reTotalsWord = regexp.MustCompile(`(?i)^\s*(итого|всего|итог)\b`)

// tableBounds уточняет границы полосы: вверх — до шапки (строки без чисел
// сразу над данными, не больше двух), вниз — по строку «Итого» включительно.
// Строка суммы прописью и текст договора в полосу не попадают.
func tableBounds(rows []tokenRow, first, last int) (head, from, to int) {
	head = first
	for k := 0; k < 2 && head > 0; k++ {
		prev := rows[head-1]
		txt := rowText(prev)
		if len(numericCells(prev)) >= 3 || reTotalsWord.MatchString(txt) {
			break
		}
		if !looksLikeHeaderRow(prev) {
			break
		}
		head--
	}
	to = last
	for i := last + 1; i < len(rows) && i <= last+2; i++ {
		txt := rowText(rows[i])
		if reTotalsWord.MatchString(txt) {
			// «Итого» — последний ряд таблицы. Ниже начинается сумма
			// прописью и подписи, они в таблицу не входят.
			to = i
			break
		}
		if len(numericCells(rows[i])) >= 3 {
			to = i
			continue
		}
		break
	}
	return head, first, to
}

// looksLikeHeaderRow — строка похожа на подписи граф: несколько коротких
// текстовых блоков, ни одного длинного предложения и почти без цифр.
func looksLikeHeaderRow(r tokenRow) bool {
	words, digits := 0, 0
	for _, t := range r.Toks {
		for _, w := range strings.Fields(t.Text) {
			words++
			for _, c := range w {
				if c >= '0' && c <= '9' {
					digits++
				}
			}
		}
	}
	if words == 0 || words > 24 {
		return false
	}
	return digits*4 <= words
}
