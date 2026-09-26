package recognize

// Сборка позиций, напечатанных в несколько строк.
//
// В счетах-фактурах за коммуналку и в накладных одна позиция занимает на листе
// две-три строки: наименование переносится, адрес объекта стоит отдельной
// строкой над позицией, а суммы («Сумма НДС», «Стоимость с НДС») выровнены по
// нижней строке ячейки, тогда как количество и цена — по верхней. Разборщик
// видит это как несколько строк таблицы, и в 1С уезжают обрывки: позиция без
// итоговой суммы, «позиция» из одного адреса, сумма, у которой нет строки.
// Сумма позиций перестаёт сходиться с «Итого», и документ уходит на проверку.
//
// Здесь обрывки склеиваются обратно, но только когда это подтверждает сам
// документ:
//
//   - Строка с числами, у которой нет своего наименования (или оно начинается
//     со строчной буквы — это хвост переноса), приклеивается к предыдущей
//     позиции, если их числа стоят в разных графах и вместе дают арифметику
//     строки: без НДС + НДС = с НДС или количество × цена = сумма. Одного
//     «графы не пересекаются» мало — так склеились бы две настоящие позиции.
//   - Строка без чисел (перенос наименования, адрес объекта) приклеивается к
//     соседней позиции: к следующей, если позиций выше ещё нет или следующая
//     начинается с середины фразы; иначе — к предыдущей.
//
// TABLE_MULTILINE=false возвращает прежнее поведение.

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MultilineEnabled — склеивать ли позиции, перенесённые на несколько строк.
func MultilineEnabled() bool { return envBool("TABLE_MULTILINE", true) }

// Графы, в которых текст — не данные, а заехавший сосед (адрес объекта,
// кусок наименования). При склейке такой текст уходит в наименование.
var multilineNumericRoles = map[string]bool{
	"qty": true, "price": true, "amount": true, "vat": true, "vat_rate": true, "index": true,
}

func mlNameCol(roles []string) int {
	for i, r := range roles {
		if r == "name" {
			return i
		}
	}
	return 0
}

func mlIsNum(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && reNumericCell.MatchString(s)
}

// mlNumCols — графы строки (кроме наименования и порядкового номера), в которых
// стоит число.
func mlNumCols(row []string, roles []string, nameCol int) []int {
	var out []int
	for i, c := range row {
		if i == nameCol || roleAt(roles, i) == "index" {
			continue
		}
		if mlIsNum(c) {
			out = append(out, i)
		}
	}
	return out
}

func mlName(row []string, nameCol int) string {
	if nameCol < len(row) {
		return strings.TrimSpace(row[nameCol])
	}
	return ""
}

// mlContinues — текст начинается с середины фразы: со строчной буквы или со
// знака, которым фраза не открывается. Так выглядит хвост переноса.
func mlContinues(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	if unicode.IsLower(r) {
		return true
	}
	return strings.ContainsRune(")](,.;:-–—/\\", r)
}

type mlNum struct {
	v   float64
	pct bool
	row int // 0 — первая строка, 1 — вторая
}

func mlNums(row []string, cols []int, tag int) []mlNum {
	out := make([]mlNum, 0, len(cols))
	for _, i := range cols {
		c := strings.TrimSpace(row[i])
		pct := strings.HasSuffix(c, "%")
		v, err := strconv.ParseFloat(normalizeNumber(strings.TrimSuffix(c, "%")), 64)
		if err != nil {
			continue
		}
		out = append(out, mlNum{v: v, pct: pct, row: tag})
	}
	return out
}

// mlHasMoney — в строке есть сумма в деньгах (с копейками), а не только
// целые номера граф или проценты.
func mlHasMoney(row []string, roles []string, nameCol int) bool {
	for _, i := range mlNumCols(row, roles, nameCol) {
		if reCellMoney.MatchString(row[i]) && !strings.HasSuffix(strings.TrimSpace(row[i]), "%") {
			return true
		}
	}
	return false
}

// mlOnlyColumnNumbers — все числа строки целые и не больше числа граф: так
// выглядит ряд номеров граф, разнесённый по строкам шапки.
func mlOnlyColumnNumbers(row []string, roles []string, nameCol int) bool {
	cols := mlNumCols(row, roles, nameCol)
	if len(cols) == 0 {
		return false
	}
	for _, i := range cols {
		v, err := strconv.Atoi(strings.TrimSpace(row[i]))
		if err != nil || v < 1 || v > len(row)+2 {
			return false
		}
	}
	return true
}

// mlCrossArithmetic — среди чисел двух строк есть тройка, которая сходится
// как арифметика позиции, и в тройке участвуют обе строки. Роли граф здесь
// не используются намеренно: шапка бланка часто разобрана криво, а сложение
// копеек от этого не зависит.
func mlCrossArithmetic(a, b []mlNum) bool {
	all := append(append([]mlNum{}, a...), b...)
	n := len(all)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if j == i {
				continue
			}
			for k := 0; k < n; k++ {
				if k == i || k == j {
					continue
				}
				x, y, z := all[i], all[j], all[k]
				if x.row == y.row && y.row == z.row {
					continue
				}
				if z.pct || z.v <= 0 || x.v <= 0 || y.v <= 0 {
					continue
				}
				switch {
				case !x.pct && !y.pct && i < j:
					// без НДС + НДС = с НДС: копейки сходятся точно.
					if math.Abs(x.v+y.v-z.v) < 0.005 {
						return true
					}
					// количество × цена = сумма, с округлением до копейки.
					if x.v != 1 && y.v != 1 && math.Abs(x.v*y.v-z.v) <= math.Max(0.006, z.v*0.005) {
						return true
					}
				case y.pct && !x.pct:
					// сумма × ставка = НДС.
					if math.Abs(x.v*y.v/100-z.v) <= math.Max(0.006, z.v*0.005) {
						return true
					}
				}
			}
		}
	}
	return false
}

// mlComplements — вторая строка дописывает числа первой: графы не
// пересекаются, наименования у второй нет или это хвост переноса, и вместе
// они дают арифметику позиции.
func mlComplements(prev, cur []string, roles []string, nameCol int) bool {
	if n := mlName(cur, nameCol); n != "" && !mlContinues(n) {
		return false
	}
	pc := mlNumCols(prev, roles, nameCol)
	cc := mlNumCols(cur, roles, nameCol)
	if len(pc) == 0 || len(cc) == 0 {
		return false
	}
	used := map[int]bool{}
	for _, i := range pc {
		used[i] = true
	}
	for _, i := range cc {
		if used[i] {
			return false
		}
	}
	return mlCrossArithmetic(mlNums(prev, pc, 0), mlNums(cur, cc, 1))
}

// mlMerge дописывает строку src в позицию dst. before — src стоит на листе
// выше dst (адрес объекта, начало наименования).
func mlMerge(dst, src []string, roles []string, nameCol int, before bool) {
	var extra []string
	for i := range src {
		if i >= len(dst) {
			break
		}
		s := strings.TrimSpace(src[i])
		if s == "" || i == nameCol {
			continue
		}
		d := strings.TrimSpace(dst[i])
		sNum, dNum := mlIsNum(s), mlIsNum(d)
		switch {
		case d == "" && sNum:
			dst[i] = s
		case d == "" && multilineNumericRoles[roleAt(roles, i)]:
			extra = append(extra, s)
		case d == "":
			dst[i] = s
		case sNum && dNum:
			// Две цифры в одной графе склейка не допускает (mlComplements),
			// сюда попадаем только при ошибке — оставляем значение позиции.
		case sNum:
			// В графе стоял заехавший текст, а число — вот оно.
			extra = append(extra, d)
			dst[i] = s
		case dNum || multilineNumericRoles[roleAt(roles, i)]:
			extra = append(extra, s)
		case before:
			dst[i] = s + " " + d
		default:
			dst[i] = d + " " + s
		}
	}
	if nameCol >= len(dst) {
		return
	}
	name := strings.TrimSpace(dst[nameCol])
	if sn := mlName(src, nameCol); sn != "" {
		if before {
			name = strings.TrimSpace(sn + " " + name)
		} else {
			name = strings.TrimSpace(name + " " + sn)
		}
	}
	// Текст, заехавший в денежные графы (обычно адрес объекта), дописываем в
	// конец наименования: терять его нельзя, а в графе суммы ему не место.
	for i, c := range dst {
		if i != nameCol && multilineNumericRoles[roleAt(roles, i)] && strings.TrimSpace(c) != "" && !mlIsNum(c) {
			extra = append(extra, strings.TrimSpace(c))
			dst[i] = ""
		}
	}
	if len(extra) > 0 {
		name = strings.TrimSpace(name + " " + strings.Join(extra, " "))
	}
	dst[nameCol] = name
}

// mergeMultilineRows склеивает позиции, разнесённые разборщиком на несколько
// строк. Строки таблицы уже вычищены (шапка, ряд номеров граф, итог сняты).
func mergeMultilineRows(rows [][]string, roles []string) [][]string {
	if len(rows) < 2 {
		return rows
	}
	nameCol := mlNameCol(roles)
	numeric := make([]bool, len(rows))
	for i, r := range rows {
		numeric[i] = len(mlNumCols(r, roles, nameCol)) > 0
	}
	// Позиции начинаются с первой строки с деньгами. Выше — остатки шапки:
	// номера граф, разнесённые по строкам («2 3 4», «5», «7»), случайно дают
	// «2 + 3 = 5», и склеивать их нельзя.
	posStart := -1
	for i, r := range rows {
		if mlHasMoney(r, roles, nameCol) {
			posStart = i
			break
		}
	}
	if posStart < 0 {
		return rows
	}
	// Над первой позицией строка, где из чисел только номера граф («5», «6»),
	// а наименование есть, — начало первой позиции, а не данные.
	for i := 0; i < posStart; i++ {
		if numeric[i] && mlName(rows[i], nameCol) != "" && mlOnlyColumnNumbers(rows[i], roles, nameCol) {
			numeric[i] = false
		}
	}
	nextNumeric := func(i int) int {
		for j := i + 1; j < len(rows); j++ {
			if numeric[j] && j >= posStart {
				return j
			}
		}
		return -1
	}

	out := make([][]string, 0, len(rows))
	// lastPos — индекс в out последней позиции.
	lastPos := -1
	var pending [][]string
	for i, row := range rows {
		cur := append([]string(nil), row...)
		if numeric[i] && i < posStart {
			out = append(out, cur)
			continue
		}
		if numeric[i] {
			if len(pending) == 0 && lastPos >= 0 && lastPos == len(out)-1 &&
				mlHasMoney(cur, roles, nameCol) && mlHasMoney(out[lastPos], roles, nameCol) &&
				mlComplements(out[lastPos], cur, roles, nameCol) {
				mlMerge(out[lastPos], cur, roles, nameCol, false)
				continue
			}
			for k := len(pending) - 1; k >= 0; k-- {
				mlMerge(cur, pending[k], roles, nameCol, true)
			}
			pending = nil
			out = append(out, cur)
			lastPos = len(out) - 1
			continue
		}

		// Строка без чисел.
		name := mlName(cur, nameCol)
		next := nextNumeric(i)
		switch {
		case name == "" && lastPos < 0 && isSectionRow(cur):
			// Подпись раздела над первой позицией (адрес объекта) —
			// не строка документа, как и раньше.
			continue
		case name == "" && lastPos < 0:
			// Над первой позицией строка без наименования — остаток шапки.
			out = append(out, cur)
		case lastPos < 0 && next < 0:
			out = append(out, cur)
		case lastPos < 0:
			pending = append(pending, cur)
		case next < 0 || mlContinues(name):
			mlMerge(out[lastPos], cur, roles, nameCol, false)
		case mlContinues(mlName(rows[next], nameCol)):
			pending = append(pending, cur)
		case mlName(rows[next], nameCol) == "" && next == i+1 && !mlComplements(out[lastPos], rows[next], roles, nameCol):
			// Следующая позиция без наименования, и эта строка стоит прямо
			// над ней: наименование позиции и есть эта строка.
			pending = append(pending, cur)
		default:
			mlMerge(out[lastPos], cur, roles, nameCol, false)
		}
	}
	if len(pending) > 0 {
		// Позиция, к которой они вели, так и не нашлась.
		out = append(out, pending...)
	}
	return out
}
