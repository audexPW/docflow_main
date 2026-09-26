package recognize

import (
	"math"
	"strconv"
	"strings"
)

// repairRowOrder чинит строку, в которой значения сдвинуты относительно шапки.
//
// Модель возвращает columns и rows независимо: заголовки — в том порядке, в
// каком она их перечислила, ячейки — в порядке документа. Если порядки
// разошлись, позиционная укладка даёт «Сумма 23,79 / Кол-во 6,86 / Ставка
// 16.74» там, где в документе «Кол-во 16.74 / Цена 0.41009 / Сумма 6,86 /
// Ставка 20».
//
// Опознать перестановку по одному значению нельзя — 6,86 выглядит суммой не
// хуже, чем количеством. Зато её видно по строке целиком: в любой
// бухгалтерской позиции qty × price = amount, а ставка НДС — из короткого
// набора. Перебираем перестановки числовых ячеек по числовым ролям и берём ту,
// на которой строка сходится.
func repairRowOrder(row []string, roles []string) []string {
	if len(row) != len(roles) {
		return row
	}
	if rowConsistent(row, roles) {
		return row
	}

	var numIdx []int
	for i, r := range roles {
		switch r {
		case "qty", "price", "amount", "vat", "vat_rate":
			numIdx = append(numIdx, i)
		}
	}
	if len(numIdx) < 2 || len(numIdx) > 7 {
		return row
	}
	vals := make([]string, 0, len(numIdx))
	for _, i := range numIdx {
		if v := strings.TrimSpace(row[i]); v != "" {
			vals = append(vals, v)
		}
	}
	if len(vals) < 2 {
		return row
	}

	best := append([]string(nil), row...)
	found := false
	permute(vals, func(p []string) bool {
		cand := append([]string(nil), row...)
		for k, i := range numIdx {
			if k < len(p) {
				cand[i] = p[k]
			} else {
				cand[i] = ""
			}
		}
		if rowConsistent(cand, roles) {
			best, found = cand, true
			return true
		}
		return false
	})
	if !found {
		return row
	}
	return best
}

// rowConsistent проверяет строку на внутреннюю сходимость: количество на цену
// даёт сумму, ставка НДС правдоподобна. Строка без нужных граф считается
// сходящейся — чинить нечего.
func rowConsistent(row []string, roles []string) bool {
	get := func(role string) (float64, bool) {
		for i, r := range roles {
			if r == role {
				f, err := strconv.ParseFloat(normalizeNumber(row[i]), 64)
				if err != nil {
					return 0, false
				}
				return f, true
			}
		}
		return 0, false
	}

	if rate, ok := get("vat_rate"); ok {
		if rate != 0 && rate != 10 && rate != 20 && rate != 25 {
			return false
		}
	}

	qty, okQ := get("qty")
	price, okP := get("price")
	amount, okA := get("amount")
	if !okQ || !okP || !okA {
		return true
	}
	want := qty * price
	if want == 0 && amount == 0 {
		return true
	}
	tol := math.Max(0.02, math.Abs(want)*0.01)
	return math.Abs(want-amount) <= tol
}

// permute перебирает перестановки, останавливаясь, как только fn вернула true.
func permute(in []string, fn func([]string) bool) {
	a := append([]string(nil), in...)
	var rec func(k int) bool
	rec = func(k int) bool {
		if k == len(a) {
			return fn(a)
		}
		for i := k; i < len(a); i++ {
			a[k], a[i] = a[i], a[k]
			if rec(k + 1) {
				return true
			}
			a[k], a[i] = a[i], a[k]
		}
		return false
	}
	rec(0)
}
