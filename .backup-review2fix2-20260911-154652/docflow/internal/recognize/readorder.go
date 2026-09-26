package recognize

import (
	"math"
	"os"
	"sort"
	"strings"
)

// Порядок чтения и ложные колонки.
//
// Разбор второй, пункт первый. В многострочных абзацах актов строки
// переворачивались, а слова внутри строки перемешивались. Координаты от
// распознавалки при этом в порядке: строки идут сверху вниз, слова — в порядке
// чтения. Ломала сборка блоков.
//
// Причин две, и обе от одного: текст в актах выровнен по ширине, межсловные
// пробелы растянуты до сантиметра.
//
//  1. Сборщик колонок принимал растянутые пробелы за границы граф и находил
//     «таблицу» посреди абзаца. Всё, что оказывалось между шапкой ложной
//     таблицы и её данными, не попадало ни в одну зону — на актах 370/371
//     именно так пропадала строка «арендная плата составляет 28,25, в том числе
//     НДС 4,71», и сумма без налога не находилась.
//  2. Слова внутри строки сортировались по X. На растянутой строке surya отдаёт
//     координаты слов немонотонно, и порядок рассыпался.
//
// Лечение не в другой сортировке, а в решении, где колонки есть, а где текст
// сплошной. Настоящая колонка идёт через весь блок сверху донизу, ложная
// рассыпается на соседних строках. Строка движка, в которой много слов,
// служебные слова и которая тянется на полстраницы, — это абзац, а не ряд
// подписей граф.

// AttachEngineOrder проставляет словам номер строки движка и порядок в его
// выдаче. Слова одной строки surya отдаёт подряд и с одним и тем же
// вертикальным размером — по этому строка и узнаётся.
//
// Порядок проверяется по плоскому тексту: если собранные так строки в нём не
// находятся, выдаче не доверяем и всё остаётся как было (Line = 0).
func AttachEngineOrder(words []wordBox, flat string) []wordBox {
	if len(words) == 0 || os.Getenv("READ_ORDER") == "xy" {
		return words
	}
	out := make([]wordBox, len(words))
	copy(out, words)
	line := 0
	for i := range out {
		if i == 0 || !sameEngineLine(out[i-1], out[i]) {
			line++
		}
		out[i].Line = line
		out[i].Seq = (i + 1) * 100
	}
	if !engineOrderConfirmed(out, flat) {
		for i := range out {
			out[i].Line, out[i].Seq = 0, 0
		}
		return out
	}
	return out
}

func sameEngineLine(a, b wordBox) bool {
	const tol = 3.0
	return math.Abs(a.Y0-b.Y0) <= tol && math.Abs(a.Y1-b.Y1) <= tol
}

// engineOrderConfirmed — строки, собранные по выдаче движка, дословно есть в
// плоском тексте. Проверяем только строки от трёх слов: короткие совпадут и при
// перепутанном порядке.
func engineOrderConfirmed(words []wordBox, flat string) bool {
	if strings.TrimSpace(flat) == "" {
		return false
	}
	hay := normForSearch(flat)
	var cur []string
	curLine := -1
	checked, matched := 0, 0
	flush := func() {
		if len(cur) >= 3 {
			checked++
			if strings.Contains(hay, normForSearch(strings.Join(cur, " "))) {
				matched++
			}
		}
		cur = cur[:0]
	}
	for _, w := range words {
		if w.Line != curLine {
			flush()
			curLine = w.Line
		}
		cur = append(cur, strings.TrimSpace(w.Text))
	}
	flush()
	if checked == 0 {
		return false
	}
	return matched*10 >= checked*6
}

// rowHasEngineOrder — у всех слов строки известен порядок движка.
func rowHasEngineOrder(r tokenRow) bool {
	if len(r.Toks) < 2 {
		return false
	}
	for _, t := range r.Toks {
		if t.Line == 0 {
			return false
		}
	}
	return true
}

// sortByEngineOrder: строки движка — слева направо по левому краю, слова
// внутри строки — в порядке выдачи.
func sortByEngineOrder(toks []token) {
	left := map[int]float64{}
	for _, t := range toks {
		if x, ok := left[t.Line]; !ok || t.X0 < x {
			left[t.Line] = t.X0
		}
	}
	sort.SliceStable(toks, func(a, b int) bool {
		ta, tb := toks[a], toks[b]
		if ta.Line == tb.Line {
			return ta.Seq < tb.Seq
		}
		if la, lb := left[ta.Line], left[tb.Line]; la != lb {
			return la < lb
		}
		return ta.Line < tb.Line
	})
}

// Служебные слова русского текста. В подписях граф их почти нет («Сумма без
// НДС», «Цена за ед.»), в абзаце договора — через слово.
var proseFunctionWords = map[string]bool{
	"в": true, "во": true, "на": true, "за": true, "по": true, "и": true, "с": true,
	"со": true, "о": true, "об": true, "от": true, "до": true, "из": true, "к": true,
	"у": true, "что": true, "а": true, "не": true, "для": true, "при": true,
	"который": true, "которых": true, "именуемое": true, "именуемый": true,
	"настоящего": true, "настоящий": true, "составляет": true, "является": true,
}

// isProseRow — строка страницы является сплошным текстом, а не рядом таблицы.
//
// Признаки абзаца: одна строка движка (или слова без просветов шире пары
// символов), от семи слов, тянется хотя бы на 45% ширины текста на странице и
// несёт служебные слова. Подписи граф так не выглядят даже тогда, когда surya
// склеила всю шапку в одну строку: в «Наименование товара Ед. изм. Кол-во Цена
// Сумма» служебных слов нет.
func isProseRow(r tokenRow, pageW float64) bool {
	if len(r.Toks) < 7 || pageW <= 0 {
		return false
	}
	units := map[int]int{}
	minX, maxX := math.Inf(1), math.Inf(-1)
	func_, words, roles := 0, 0, map[string]bool{}
	for _, t := range r.Toks {
		minX = math.Min(minX, t.X0)
		maxX = math.Max(maxX, t.X1)
		units[t.Line]++
		for _, f := range strings.Fields(t.Text) {
			w := strings.ToLower(strings.Trim(f, ".,;:«»\"()"))
			if w == "" {
				continue
			}
			words++
			if proseFunctionWords[w] {
				func_++
			}
		}
		if role := guessRole(t.Text); role != "" && role != "other" {
			roles[role] = true
		}
	}
	if (maxX-minX)/pageW < 0.45 {
		return false
	}
	// Слова известной строки движка: абзац — это одна-две строки движка на
	// ряд. Порядок неизвестен — проверяем, что в ряду нет просветов шире
	// трёх символов: у таблицы они есть всегда.
	if _, unknown := units[0]; unknown {
		if hasWideGap(r.Toks) {
			return false
		}
	} else if len(units) > 2 {
		return false
	}
	switch {
	case func_ >= 3:
		return true
	case func_ >= 2 && len(roles) < 2 && words >= 8:
		return true
	}
	return false
}

func hasWideGap(toks []token) bool {
	s := make([]token, len(toks))
	copy(s, toks)
	sort.Slice(s, func(i, j int) bool { return s[i].X0 < s[j].X0 })
	var widths []float64
	for _, t := range s {
		if n := len([]rune(t.Text)); n > 0 && t.X1 > t.X0 {
			widths = append(widths, (t.X1-t.X0)/float64(n))
		}
	}
	cw := median(widths)
	if cw <= 0 {
		cw = 10
	}
	right := s[0].X1
	for _, t := range s[1:] {
		if t.X0-right > cw*3 {
			return true
		}
		right = math.Max(right, t.X1)
	}
	return false
}

// pageTextWidth — ширина, занятая текстом на странице.
func pageTextWidth(rows []tokenRow) float64 {
	minX, maxX := math.Inf(1), math.Inf(-1)
	for _, r := range rows {
		for _, t := range r.Toks {
			minX = math.Min(minX, t.X0)
			maxX = math.Max(maxX, t.X1)
		}
	}
	if math.IsInf(minX, 1) {
		return 0
	}
	return maxX - minX
}

// bandIsProse — найденная полоса таблицы на деле сплошной текст: либо в шапке
// стоит абзац, либо абзацы составляют половину полосы.
func bandIsProse(rows []tokenRow, head, first, last int) bool {
	pageW := pageTextWidth(rows)
	for i := head; i < first && i < len(rows); i++ {
		if isProseRow(rows[i], pageW) {
			return true
		}
	}
	prose, total := 0, 0
	for i := first; i <= last && i < len(rows); i++ {
		total++
		if isProseRow(rows[i], pageW) {
			prose++
		}
	}
	return total > 0 && prose*2 >= total
}

// LayoutFromWords собирает текст с раскладкой из слов с координатами.
//
// Раскладку раньше отдавал сервис surya, и именно в ней абзац читался снизу
// вверх: сервис склеивал соседние строки абзаца в одну и резал её по
// растянутым пробелам. Здесь строка страницы собирается из строк движка, слова
// внутри них не переставляются, а граница ячейки « | » ставится только там, где
// просвет повторяется в соседней строке — настоящая графа идёт через блок, а не
// живёт в одной строке.
func LayoutFromWords(words []wordBox) string {
	toks := tokensFromWords(words)
	if len(toks) < 8 {
		return ""
	}
	rows := groupTokenRows(toks)
	var widths []float64
	for _, t := range toks {
		if n := len([]rune(t.Text)); n > 0 && t.X1 > t.X0 {
			widths = append(widths, (t.X1-t.X0)/float64(n))
		}
	}
	cw := median(widths)
	if cw <= 0 {
		cw = 10
	}
	gap := cw * 2.5

	type cut struct{ x float64 }
	type rowUnits struct {
		units [][]token
		cuts  []float64
	}
	built := make([]rowUnits, len(rows))
	for i, r := range rows {
		var units [][]token
		for _, t := range r.Toks {
			if len(units) == 0 {
				units = append(units, []token{t})
				continue
			}
			lastU := units[len(units)-1]
			prev := lastU[len(lastU)-1]
			sameLine := t.Line != 0 && t.Line == prev.Line
			if sameLine || t.X0-unitRight(lastU) <= gap {
				units[len(units)-1] = append(lastU, t)
				continue
			}
			units = append(units, []token{t})
		}
		ru := rowUnits{units: units}
		for k := 1; k < len(units); k++ {
			ru.cuts = append(ru.cuts, (unitRight(units[k-1])+units[k][0].X0)/2)
		}
		built[i] = ru
	}
	persistent := func(i int, x float64) bool {
		for _, j := range []int{i - 1, i + 1} {
			if j < 0 || j >= len(built) {
				continue
			}
			for _, c := range built[j].cuts {
				if math.Abs(c-x) <= cw*4 {
					return true
				}
			}
		}
		return false
	}

	var b strings.Builder
	for i, ru := range built {
		for k, u := range ru.units {
			if k > 0 {
				if persistent(i, ru.cuts[k-1]) {
					b.WriteString(" | ")
				} else {
					b.WriteString("  ")
				}
			}
			for n, t := range u {
				if n > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(t.Text)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func unitRight(u []token) float64 {
	r := math.Inf(-1)
	for _, t := range u {
		r = math.Max(r, t.X1)
	}
	return r
}

// tokensFromWords — общий перевод слов движка в токены разбора.
func tokensFromWords(words []wordBox) []token {
	toks := make([]token, 0, len(words))
	for _, w := range words {
		t := normalizeNoSign(StripMath(strings.TrimSpace(w.Text)))
		if t == "" || w.X1 <= w.X0 || w.Y1 <= w.Y0 {
			continue
		}
		toks = append(toks, token{Text: t, X0: w.X0, Y0: w.Y0, X1: w.X1, Y1: w.Y1, Line: w.Line, Seq: w.Seq})
	}
	return toks
}
