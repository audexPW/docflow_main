package recognize

import (
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Плотные таблицы: сдвиг граф по вертикали.
//
// Разбор второй, пункт третий. Счёт-фактура на восемь граф и двадцать строк
// выглядел в кабинете разобранным, а на деле значения съехали на строку
// относительно наименований: электроэнергия получила гигакалории, теплоэнергия
// осталась пустой.
//
// По координатам слов видно, что лист повёрнут не целиком. Наименование и
// «Гкал» стоят ровно, а каждая следующая числовая графа ниже предыдущей на
// 8 px: на восьми графах набегает 40 px при шаге строк 53 px. Строки
// собирались по перекрытию боксов, и правые графы каждой позиции прилипали к
// следующей. Общий поворот страницы здесь не поможет — сдвиг копится от графы
// к графе, поэтому и считаем его от графы к графе.
//
// Как считаем. Соседние графы сдвинуты друг относительно друга на единицы
// пикселей, много меньше полушага строки, — значит, ближайшее по вертикали
// значение соседней графы и есть значение той же позиции. Медиана этих
// разниц даёт сдвиг пары граф, сумма по цепочке — сдвиг каждой графы от
// опорной. Дальше слова опускаются на своё место и строки собираются заново,
// уже по центрам, а не по краям боксов.

type driftItem struct {
	t    token
	band int
	c    float64
	kind int // 0 — данные, 1 — «Итого», 2 — ряд номеров граф
}

type item = driftItem

// itemsOf разворачивает строки обратно в элементы (для служебных рядов).
func itemsOf(rows []tokenRow, bands [][2]float64) []item {
	var out []item
	for _, r := range rows {
		kind := 0
		if reTotalsRow.MatchString(rowText(r)) {
			kind = 1
		} else if isColumnNumberingText(rowText(r)) {
			kind = 2
		}
		for _, t := range r.Toks {
			out = append(out, item{t: t, band: bandOf(t, bands), c: (t.Y0 + t.Y1) / 2, kind: kind})
		}
	}
	return out
}

// alignColumnDrift пересобирает строки полосы с поправкой на сдвиг граф.
// ok=false — сдвига нет (меньше четверти шага строк), строки не трогаются.
func alignColumnDrift(rows []tokenRow, bands [][2]float64) ([]tokenRow, bool) {
	if len(bands) < 3 || len(rows) < 3 || os.Getenv("TABLE_DRIFT_FIX") == "false" {
		return rows, false
	}
	perBand := make([][]item, len(bands))
	var all []item
	var heights []float64
	for _, r := range rows {
		kind := 0
		switch joined := rowText(r); {
		case reTotalsRow.MatchString(joined):
			kind = 1
		case isColumnNumberingText(joined):
			kind = 2
		}
		for _, t := range r.Toks {
			it := item{t: t, band: bandOf(t, bands), c: (t.Y0 + t.Y1) / 2, kind: kind}
			perBand[it.band] = append(perBand[it.band], it)
			all = append(all, it)
			heights = append(heights, t.Y1-t.Y0)
		}
	}
	for b := range perBand {
		sort.Slice(perBand[b], func(i, j int) bool { return perBand[b][i].c < perBand[b][j].c })
	}

	// Шаг строк — по графам, где на позицию приходится одно значение: у
	// них медиана расстояния между соседями и есть шаг.
	var pitches []float64
	for _, its := range perBand {
		if len(its) < 5 {
			continue
		}
		var d []float64
		for i := 1; i < len(its); i++ {
			if gap := its[i].c - its[i-1].c; gap > 1 {
				d = append(d, gap)
			}
		}
		if m := median(d); m > 0 {
			pitches = append(pitches, m)
		}
	}
	pitch := median(pitches)
	if pitch <= 0 {
		pitch = median(heights) * 1.5
	}
	if pitch <= 0 {
		return rows, false
	}

	// Сдвиг пары соседних граф. Доверяем только согласному: значения должны
	// найтись у большинства слов графы, а разброс разниц — быть мал. Иначе на
	// разреженной таблице с переносами наименований «сдвиг» находился там,
	// где его нет, и строки склеивались с «Итого» и рядом номеров граф.
	pairShift := func(from, to []item) (float64, bool) {
		var d []float64
		for _, it := range to {
			best, bestAbs := 0.0, math.Inf(1)
			for _, o := range from {
				if a := math.Abs(it.c - o.c); a < bestAbs {
					best, bestAbs = it.c-o.c, a
				}
			}
			if bestAbs < pitch*0.5 {
				d = append(d, best)
			}
		}
		need := len(to)
		if len(from) < need {
			need = len(from)
		}
		if len(d) < 3 || len(d)*2 < need {
			return 0, false
		}
		m := median(d)
		dev := make([]float64, len(d))
		for i, v := range d {
			dev[i] = math.Abs(v - m)
		}
		if median(dev) > pitch*0.12 {
			return 0, false
		}
		return m, true
	}

	// Графа наименования — та, где больше всего букв. Всё левее неё (номер
	// п/п, код) стоит на уровне наименования; всё правее — блок значений,
	// который и сползает.
	nameBand, bestLetters := -1, 0
	for b, its := range perBand {
		letters := 0
		for _, it := range its {
			for _, r := range it.t.Text {
				if r >= 'А' && r <= 'я' || r == 'ё' || r == 'Ё' || r >= 'A' && r <= 'z' {
					letters++
				}
			}
		}
		if letters > bestLetters {
			nameBand, bestLetters = b, letters
		}
	}
	if nameBand < 0 || nameBand >= len(bands)-2 {
		return rows, false
	}

	shift := make([]float64, len(bands))
	firstVal := -1
	for b := nameBand + 1; b < len(bands); b++ {
		if len(perBand[b]) > 0 {
			firstVal = b
			break
		}
	}
	if firstVal < 0 {
		return rows, false
	}
	prev := firstVal
	for b := firstVal + 1; b < len(bands); b++ {
		if len(perBand[b]) == 0 {
			shift[b] = shift[prev]
			continue
		}
		if sh, ok := pairShift(perBand[prev], perBand[b]); ok {
			shift[b] = shift[prev] + sh
		} else {
			shift[b] = shift[prev]
		}
		prev = b
	}
	lo, hi := 0.0, 0.0
	for _, sh := range shift {
		lo, hi = math.Min(lo, sh), math.Max(hi, sh)
	}
	if hi-lo < pitch*0.25 {
		return rows, false
	}

	thr := pitch * 0.4
	// group собирает элементы в строки по центрам, не смешивая виды строк.
	group := func(items []item) ([]tokenRow, []int, []float64) {
		sort.SliceStable(items, func(i, j int) bool { return items[i].c < items[j].c })
		var out []tokenRow
		var kinds []int
		var centers []float64
		var sum float64
		for _, it := range items {
			if n := len(out); n > 0 {
				mean := sum / float64(len(out[n-1].Toks))
				if it.c-mean <= thr && kinds[n-1] == it.kind {
					out[n-1].Toks = append(out[n-1].Toks, it.t)
					out[n-1].Top = math.Min(out[n-1].Top, it.t.Y0)
					out[n-1].Bot = math.Max(out[n-1].Bot, it.t.Y1)
					sum += it.c
					centers[n-1] = sum / float64(len(out[n-1].Toks))
					continue
				}
			}
			out = append(out, tokenRow{Toks: []token{it.t}, Top: it.t.Y0, Bot: it.t.Y1})
			kinds = append(kinds, it.kind)
			centers = append(centers, it.c)
			sum = it.c
		}
		return out, kinds, centers
	}

	var names, values []item
	for _, it := range all {
		if it.band <= nameBand {
			names = append(names, it)
			continue
		}
		sh := shift[it.band]
		it.t.Y0 -= sh
		it.t.Y1 -= sh
		it.c -= sh
		values = append(values, it)
	}
	// Строки наименований собираем по самой графе наименования. Код и номер
	// п/п левее неё стоят то на строке наименования, то посередине между
	// двумя его строками — их привязываем к ближайшей строке по центру.
	var core, side []item
	for _, it := range names {
		if it.band == nameBand && !reItemCode.MatchString(strings.TrimSpace(it.t.Text)) {
			core = append(core, it)
		} else {
			side = append(side, it)
		}
	}
	nameRows, nameKinds, nameC := group(core)
	for _, it := range side {
		best, bestD := -1, math.Inf(1)
		for i := range nameRows {
			if nameKinds[i] != it.kind {
				continue
			}
			if d := math.Abs(nameC[i] - it.c); d < bestD {
				best, bestD = i, d
			}
		}
		if best < 0 || bestD > pitch*0.6 {
			nameRows = append(nameRows, tokenRow{Toks: []token{it.t}, Top: it.t.Y0, Bot: it.t.Y1})
			nameKinds = append(nameKinds, it.kind)
			nameC = append(nameC, it.c)
			continue
		}
		nameRows[best].Toks = append(nameRows[best].Toks, it.t)
		nameRows[best].Top = math.Min(nameRows[best].Top, it.t.Y0)
		nameRows[best].Bot = math.Max(nameRows[best].Bot, it.t.Y1)
	}
	{
		idx := make([]int, len(nameRows))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return nameC[idx[a]] < nameC[idx[b]] })
		r2, k2, c2 := make([]tokenRow, len(idx)), make([]int, len(idx)), make([]float64, len(idx))
		for i, j := range idx {
			r2[i], k2[i], c2[i] = nameRows[j], nameKinds[j], nameC[j]
		}
		nameRows, nameKinds, nameC = r2, k2, c2
	}
	valRows, valKinds, _ := group(values)

	// Позиции таблицы: ряды значений по порядку сверху вниз. Наименования к
	// ним привязываем тоже по порядку, а не по высоте: в распечатках 1С блок
	// значений стоит на полстроки-строку ниже своего наименования, и любая
	// привязка «по ближайшему» отдаёт позиции чужие значения. Перенос
	// наименования на вторую строку склеиваем с соседней строкой по самому
	// узкому просвету — пока строк наименований больше, чем рядов значений.
	type nline struct {
		row tokenRow
		c   float64
	}
	var nl []nline
	var extra []tokenRow
	for i, r := range nameRows {
		if nameKinds[i] != 0 || reTotalsRow.MatchString(rowText(r)) {
			extra = append(extra, r)
			continue
		}
		nl = append(nl, nline{row: r, c: nameC[i]})
	}
	var vr []tokenRow
	for i, r := range valRows {
		// Ряд значений без единого числа — перенос подписей шапки
		// («Сумма НДС», «Всего с НДС»), позицией он не является.
		if valKinds[i] != 0 || !valueRowHasNumbers(r) {
			extra = append(extra, r)
			continue
		}
		vr = append(vr, r)
	}
	// Под последним наименованием стоит «Итого»: подпись в графе
	// наименования, суммы — в блоке значений, и самый нижний ряд значений
	// — это итог, а не позиция.
	totalsLabels := 0
	for _, r := range extra {
		if reTotalsRow.MatchString(rowText(r)) && !valueRowHasNumbers(r) {
			totalsLabels++
		}
	}
	for totalsLabels > 0 && len(vr) > len(nl) {
		extra = append(extra, vr[len(vr)-1])
		vr = vr[:len(vr)-1]
		totalsLabels--
	}
	// Плотная таблица — от шести позиций. На коротких таблицах сдвиг блока
	// значений не успевает набежать на строку, а привязка по порядку там
	// рискованнее, чем прежняя сборка.
	if len(vr) < 6 {
		return rows, false
	}
	// Привязка по порядку с подбором общего смещения. Блок значений в
	// распечатках 1С стоит то вровень с наименованием, то на полстроки ниже,
	// поэтому смещение перебираем, а ряды сопоставляем монотонно (порядок
	// сверху вниз не нарушается): наименование без значений допустимо — это
	// заголовок группы («Теплоэнергия») или перенос, значения без
	// наименования — почти никогда.
	vc := make([]float64, len(vr))
	for i, r := range vr {
		sum := 0.0
		for _, t := range r.Toks {
			sum += (t.Y0 + t.Y1) / 2
		}
		vc[i] = sum / float64(len(r.Toks))
	}
	match := func(off float64) (float64, []int) {
		n, m := len(vr), len(nl)
		const inf = 1e18
		dp := make([][]float64, n+1)
		from := make([][]byte, n+1)
		for i := range dp {
			dp[i] = make([]float64, m+1)
			from[i] = make([]byte, m+1)
			for j := range dp[i] {
				dp[i][j] = inf
			}
		}
		dp[0][0] = 0
		for i := 0; i <= n; i++ {
			for j := 0; j <= m; j++ {
				cur := dp[i][j]
				if cur >= inf {
					continue
				}
				if j < m && cur+0.35 < dp[i][j+1] { // наименование без значений
					dp[i][j+1], from[i][j+1] = cur+0.35, 'n'
				}
				if i < n && cur+1.0 < dp[i+1][j] { // значения без наименования
					dp[i+1][j], from[i+1][j] = cur+1.0, 'v'
				}
				if i < n && j < m {
					d := math.Abs(vc[i]-nl[j].c-off) / pitch
					if d < 0.6 && cur+d < dp[i+1][j+1] {
						dp[i+1][j+1], from[i+1][j+1] = cur+d, 'm'
					}
				}
			}
		}
		pair := make([]int, n)
		for i := range pair {
			pair[i] = -1
		}
		for i, j := n, m; i > 0 || j > 0; {
			switch from[i][j] {
			case 'm':
				pair[i-1] = j - 1
				i, j = i-1, j-1
			case 'v':
				i--
			default:
				j--
			}
		}
		return dp[n][m], pair
	}
	bestCost, bestOff := math.Inf(1), 0.0
	var pair []int
	for k := -12; k <= 12; k++ {
		off := float64(k) * pitch * 0.05
		cost, p := match(off)
		// При равной цене предпочитаем меньшее смещение.
		if cost < bestCost-1e-9 || (math.Abs(cost-bestCost) < 1e-9 && math.Abs(off) < math.Abs(bestOff)) {
			bestCost, bestOff, pair = cost, off, p
		}
	}

	var out []tokenRow
	usedName := make([]bool, len(nl))
	rowOfName := make([]int, len(nl))
	for j := range rowOfName {
		rowOfName[j] = -1
	}
	for i, r := range vr {
		row := tokenRow{Toks: append([]token{}, r.Toks...), Top: r.Top, Bot: r.Bot}
		if j := pair[i]; j >= 0 {
			row.Toks = append(append([]token{}, nl[j].row.Toks...), row.Toks...)
			row.Top = math.Min(row.Top, nl[j].row.Top)
			row.Bot = math.Max(row.Bot, nl[j].row.Bot)
			usedName[j] = true
			rowOfName[j] = len(out)
			// Порядок строк задаёт наименование, а не сползший блок.
			row.Top = nl[j].row.Top
		} else {
			row.Top = vc[i] - bestOff
		}
		out = append(out, row)
	}
	// Наименования без значений: перенос предыдущей позиции, если стоит
	// вплотную под ней, иначе отдельная строка (заголовок группы).
	for j := range nl {
		if usedName[j] {
			continue
		}
		if j > 0 && rowOfName[j-1] >= 0 && nl[j].c-nl[j-1].c < pitch*0.8 {
			k := rowOfName[j-1]
			out[k].Toks = append(out[k].Toks, nl[j].row.Toks...)
			out[k].Bot = math.Max(out[k].Bot, nl[j].row.Bot)
			rowOfName[j] = k
			continue
		}
		out = append(out, nl[j].row)
	}
	// «Итого» и прочие служебные ряды — отдельно, по месту на листе.
	extraItems := itemsOf(extra, bands)
	for i := range extraItems {
		extraItems[i].kind = 0
	}
	ek, _, _ := group(extraItems)
	for i := range ek {
		// Итоговые суммы сползли вместе с блоком значений — возвращаем их
		// на уровень подписи, чтобы «Итого» не встало выше последней позиции.
		ek[i].Top -= bestOff
	}
	out = append(out, ek...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Top < out[j].Top })
	for i := range out {
		orderDriftRow(&out[i], bands, nameBand)
	}
	return out, true
}

// valueRowHasNumbers — в ряду есть число с дробной частью или хотя бы два
// целых: одиночная «0» бывает и в подписи («Количеств о»).
func valueRowHasNumbers(r tokenRow) bool {
	ints := 0
	for _, t := range r.Toks {
		txt := strings.TrimSpace(t.Text)
		if reDecimalToken.MatchString(txt) {
			return true
		}
		if reSmallInt.MatchString(txt) || regexp.MustCompile(`^\d+$`).MatchString(txt) {
			ints++
		}
	}
	return ints >= 2
}

// orderDriftRow упорядочивает слова пересобранной строки: по графам, внутри
// графы — по строкам листа сверху вниз (наименование в две-три строки), внутри
// строки листа — в порядке движка или по X.
func orderDriftRow(r *tokenRow, bands [][2]float64, nameBand int) {
	var hs []float64
	for _, t := range r.Toks {
		hs = append(hs, t.Y1-t.Y0)
	}
	medH := median(hs)
	if medH <= 0 {
		medH = 10
	}
	type key struct{ group, line int }
	keys := make([]key, len(r.Toks))
	var nameIdx []int
	for i, t := range r.Toks {
		b := bandOf(t, bands)
		switch {
		case b < nameBand:
			keys[i] = key{group: 0}
		case b == nameBand && reItemCode.MatchString(strings.TrimSpace(t.Text)):
			keys[i] = key{group: 0}
		case b == nameBand:
			keys[i] = key{group: 1}
			nameIdx = append(nameIdx, i)
		default:
			keys[i] = key{group: 2 + b}
		}
	}
	sort.Slice(nameIdx, func(a, c int) bool {
		ta, tc := r.Toks[nameIdx[a]], r.Toks[nameIdx[c]]
		return ta.Y0+ta.Y1 < tc.Y0+tc.Y1
	})
	line, prevC := 0, math.Inf(-1)
	for _, i := range nameIdx {
		c := (r.Toks[i].Y0 + r.Toks[i].Y1) / 2
		if !math.IsInf(prevC, -1) && c-prevC > medH*0.6 {
			line++
		}
		prevC = c
		keys[i].line = line
	}
	perm := make([]int, len(r.Toks))
	for i := range perm {
		perm[i] = i
	}
	sort.SliceStable(perm, func(a, c int) bool {
		ka, kc := keys[perm[a]], keys[perm[c]]
		if ka.group != kc.group {
			return ka.group < kc.group
		}
		if ka.line != kc.line {
			return ka.line < kc.line
		}
		ta, tc := r.Toks[perm[a]], r.Toks[perm[c]]
		if ta.Line != 0 && ta.Line == tc.Line {
			return ta.Seq < tc.Seq
		}
		return ta.X0 < tc.X0
	})
	sorted := make([]token, len(r.Toks))
	for i, p := range perm {
		sorted[i] = r.Toks[p]
	}
	r.Toks = sorted
}

var reDecimalToken = regexp.MustCompile(`^-?\d+[.,]\d+%?$`)

// splitBandsByNumbers делит графу, в которой на одной строке стоят два
// отдельных числа с дробной частью. Так выглядят две узкие графы, слипшиеся в
// одну: «0.24590 1.7277» — это сумма без НДС и сумма НДС, а не одно число.
// Делим, если так на двух и более строках и разрез в одном месте.
func splitBandsByNumbers(bands [][2]float64, rows []tokenRow) [][2]float64 {
	if len(bands) == 0 {
		return bands
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
	cuts := make([][]float64, len(bands))
	for _, r := range rows {
		inBand := make([][]token, len(bands))
		for _, t := range r.Toks {
			if reDecimalToken.MatchString(strings.TrimSpace(t.Text)) {
				b := bandOf(t, bands)
				inBand[b] = append(inBand[b], t)
			}
		}
		for b, ts := range inBand {
			if len(ts) < 2 {
				continue
			}
			sort.Slice(ts, func(i, j int) bool { return ts[i].X0 < ts[j].X0 })
			for i := 1; i < len(ts); i++ {
				if ts[i].X0-ts[i-1].X1 > cw {
					cuts[b] = append(cuts[b], (ts[i-1].X1+ts[i].X0)/2)
				}
			}
		}
	}
	out := make([][2]float64, 0, len(bands)+2)
	for b, band := range bands {
		cs := cuts[b]
		if len(cs) < 2 {
			out = append(out, band)
			continue
		}
		m := median(cs)
		agree := 0
		for _, c := range cs {
			if math.Abs(c-m) <= cw*3 {
				agree++
			}
		}
		if agree < 2 || m-band[0] < cw*2 || band[1]-m < cw*2 {
			out = append(out, band)
			continue
		}
		out = append(out, [2]float64{band[0], m}, [2]float64{m, band[1]})
	}
	return out
}

var (
	// Код позиции перед наименованием: «10.07.04.», «7.03.».
	reItemCode  = regexp.MustCompile(`^\d{1,3}(\.\d{1,3})+\.?$`)
	reSmallInt  = regexp.MustCompile(`^\d{1,2}$`)
	reAllDigits = regexp.MustCompile(`^\d+$`)
)

// isColumnNumberingText — строка с номерами граф «1 2 3 4 5 6 7 8», в которую
// OCR подмешал соседний текст («2 за июль 2023г. 3 4»). Прежняя проверка
// требовала, чтобы в каждой ячейке стояло только число, и такая строка
// проходила в данные: цифры 7 и 8 уезжали в суммы.
func isColumnNumberingText(joined string) bool {
	if reCellMoney.MatchString(joined) {
		return false
	}
	var nums []int
	letters := 0
	for _, f := range strings.Fields(joined) {
		f = strings.Trim(f, ".,;:|")
		if reSmallInt.MatchString(f) {
			v := 0
			for _, r := range f {
				v = v*10 + int(r-'0')
			}
			nums = append(nums, v)
			continue
		}
		for _, r := range f {
			if r >= 'А' && r <= 'я' || r >= 'A' && r <= 'z' {
				letters++
			}
		}
	}
	if len(nums) < 4 || letters > 20 {
		return false
	}
	asc := 0
	for i := 1; i < len(nums); i++ {
		if nums[i] == nums[i-1]+1 {
			asc++
		}
	}
	return asc*10 >= (len(nums)-1)*7
}

// mergeTextBands склеивает соседние графы, между которыми проходит одно и то же
// наименование. На широком счёте граница графы попадала в растянутый пробел
// между словами наименования, и в 1С уходило «Пожарный» вместо «Пожарный
// надзор (сигнализация)», а «Вывоз и» вместо «Вывоз и утилизация мусора».
// Признак — на большинстве строк текст левой графы вплотную (меньше двух
// символов) переходит в текст правой, и обе графы текстовые.
func mergeTextBands(bands [][2]float64, rows []tokenRow) [][2]float64 {
	if len(bands) < 3 {
		return bands
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
	out := append([][2]float64{}, bands...)
	for b := 0; b+1 < len(out); {
		both, joined, numeric := 0, 0, 0
		for _, r := range rows {
			var left, right *token
			for i := range r.Toks {
				t := &r.Toks[i]
				switch bandOf(*t, out) {
				case b:
					if left == nil || t.X1 > left.X1 {
						left = t
					}
				case b + 1:
					if right == nil || t.X0 < right.X0 {
						right = t
					}
				}
			}
			if left == nil || right == nil {
				continue
			}
			both++
			if reDecimalToken.MatchString(left.Text) || reDecimalToken.MatchString(right.Text) {
				numeric++
				continue
			}
			if hasLetters(left.Text) && hasLetters(right.Text) && right.X0-left.X1 < cw*2 {
				joined++
			}
		}
		if both >= 2 && numeric*10 < both*3 && joined*10 >= both*6 {
			out[b] = [2]float64{out[b][0], out[b+1][1]}
			out = append(out[:b+1], out[b+2:]...)
			continue
		}
		b++
	}
	return out
}
