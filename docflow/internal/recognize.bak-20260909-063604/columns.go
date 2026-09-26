package recognize

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"docflow/internal/domain"
)

// Табличная часть по координатам слов — проекция колонок.
//
// Почему не по шапке и не моделью. Разбор по шапке (FreeTableFromGrid) требует
// узнать в одной строке хотя бы две подписи граф. В белорусских бланках шапка
// почти всегда в две-три строки: «Сумма» на одной, «без НДС, руб.» на другой, а
// ниже ещё ряд с номерами граф «1 2 3 4 5». Одной строки, где видно две
// знакомые подписи, там попросту нет — разбор отваливается, и таблица
// достаётся модели, которая сводит её к шести типовым колонкам. Отсюда и
// «Сумма с НДС» в графе «Сумма», и ставка 20 в графе «НДС».
//
// Здесь колонки берутся из геометрии страницы: по вертикальным полосам, где ни
// в одной строке таблицы нет ни одного слова. Это и есть границы граф, как их
// видит человек. Состав колонок при этом не навязан: сколько полос нашлось —
// столько граф и уйдёт в интерфейс и в 1С, с подписями из шапки оригинала.

var (
	// Ряд номеров граф под шапкой: «1 2 3 4 5». Данными не является.
	reColNumbering = regexp.MustCompile(`^\d{1,2}$`)
	// Мусор от surya: формулы приходят разметкой LaTeX внутри <math>.
	reMathTag   = regexp.MustCompile(`(?s)<math[^>]*>.*?</math>`)
	reTexMacro  = regexp.MustCompile(`\\[a-zA-Z]+\s*`)
	reTexBraces = regexp.MustCompile(`[{}$]`)
)

// StripMath убирает разметку формул из распознанного текста. Surya отдаёт
// «№ п/п» как <math>N_{\overline{2}}</math>, и такая подпись уезжает в
// заголовок колонки, а оттуда — в 1С.
// reNoSign — как OCR читает знак номера: «NΩ», «N°», «No», «Ne», «N2».
var reNoSign = regexp.MustCompile(`(?i)^(n[oeº°ΩΩ]|n°2|№)$`)

// normalizeNoSign приводит эти написания к «№». Заменяем только когда слово
// целиком совпадает: внутри текста «no» — обычное слово, и трогать его нельзя.
func normalizeNoSign(s string) string {
	if reNoSign.MatchString(s) {
		return "№"
	}
	return s
}

func StripMath(s string) string {
	if !strings.ContainsAny(s, "<\\{$") {
		return s
	}
	s = reMathTag.ReplaceAllString(s, " № ")
	s = reTexMacro.ReplaceAllString(s, " ")
	s = reTexBraces.ReplaceAllString(s, " ")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// token — слово с координатами; единица, которой оперирует проекция.
type token struct {
	Text string
	X0   float64
	Y0   float64
	X1   float64
	Y1   float64
}

// tokenRow — строка страницы: слова, перекрывающиеся по вертикали.
type tokenRow struct {
	Toks []token
	Top  float64
	Bot  float64
}

// TableFromWords собирает табличную часть из слов с координатами.
// Возвращает nil, если полосу таблицы на странице найти не удалось — тогда
// работают прежние пути разбора.
func TableFromWords(words []wordBox) *domain.FreeTable {
	toks := make([]token, 0, len(words))
	for _, w := range words {
		t := normalizeNoSign(StripMath(strings.TrimSpace(w.Text)))
		if t == "" {
			continue
		}
		if w.X1 <= w.X0 || w.Y1 <= w.Y0 {
			continue
		}
		toks = append(toks, token{Text: t, X0: w.X0, Y0: w.Y0, X1: w.X1, Y1: w.Y1})
	}
	if len(toks) < 8 {
		return nil
	}

	rows := groupTokenRows(toks)
	if len(rows) < 3 {
		return nil
	}

	head, first, last := findTableBand(rows)
	if head < 0 {
		return nil
	}

	// Границы колонок считаем ТОЛЬКО по строкам данных.
	//
	// Раньше в расчёт шла вся полоса вместе с шапкой и «Итого», и это
	// склеивало графы: подпись «Наименование товара» тянется через зазор
	// между «Ед. изм.» и «Кол-во», а «Всего отпущено на сумму» в итоговой
	// строке перекрывает сразу половину граф. Достаточно одного такого слова,
	// чтобы две колонки слились навсегда — на счёте №112 из девяти граф
	// оставалось четыре. В строках данных значение всегда стоит внутри своей
	// клетки и чужих зазоров не пересекает.
	bands := columnBands(dataRows(rows[first : last+1]))
	if len(bands) < 2 {
		// Данных не хватило (одна строка, вся из одного слова) — считаем по
		// всей полосе, как раньше: хуже, но лучше, чем отказ от таблицы.
		bands = columnBands(rows[head : last+1])
	}
	if len(bands) < 2 {
		return nil
	}

	// Шапку набираем вверх от первой строки данных, а не вниз от найденного
	// заголовка. Поиск сверху цеплял строки документа, лежащие над таблицей:
	// «Дополнение: Договор аренды№49 от17.03.20г ... за октябрь 2023г» и
	// «руб.» разрезались по графам и приклеивались к настоящим подписям —
	// в интерфейсе выходило «2023г Кол-во» и «руб. Сумма с НДС».
	cols := headerTitles(headerRowsAbove(rows, head, first, bands), bands)
	ft := &domain.FreeTable{Columns: cols, Roles: rolesFor(cols), Source: "columns"}

	for i := first; i <= last; i++ {
		cells := rowCells(rows[i], bands)
		joined := strings.TrimSpace(strings.Join(cells, " "))
		if joined == "" {
			continue
		}
		if isNumberingRow(cells) {
			continue
		}
		if reTotalsRow.MatchString(joined) {
			if len(ft.Totals) == 0 {
				ft.Totals = cells
			}
			continue
		}
		// Перенос наименования: заполнена только текстовая графа, чисел нет.
		if len(ft.Rows) > 0 && isNameContinuation(cells, ft.Roles) {
			appendToFreeName(ft.Rows[len(ft.Rows)-1], cells, ft.Roles)
			continue
		}
		if !hasLetters(joined) && !reNumberish.MatchString(joined) {
			continue
		}
		ft.Rows = append(ft.Rows, cells)
	}

	ft.Rows = dropDegenerateRows(ft.Rows)
	if len(ft.Rows) == 0 {
		return nil
	}
	return ft
}

// groupTokenRows собирает слова в строки по вертикальному перекрытию. Порог по
// перекрытию, а не по расстоянию между верхами боксов: на скане с наклоном
// строка уезжает, и сравнение верхов рвёт её пополам.
func groupTokenRows(toks []token) []tokenRow {
	sorted := make([]token, len(toks))
	copy(sorted, toks)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Y0 == sorted[j].Y0 {
			return sorted[i].X0 < sorted[j].X0
		}
		return sorted[i].Y0 < sorted[j].Y0
	})

	var rows []tokenRow
	for _, t := range sorted {
		placed := false
		for i := range rows {
			h := math.Max(rows[i].Bot-rows[i].Top, 1)
			ov := math.Min(rows[i].Bot, t.Y1) - math.Max(rows[i].Top, t.Y0)
			if ov > h*0.35 {
				rows[i].Toks = append(rows[i].Toks, t)
				rows[i].Top = math.Min(rows[i].Top, t.Y0)
				rows[i].Bot = math.Max(rows[i].Bot, t.Y1)
				placed = true
				break
			}
		}
		if !placed {
			rows = append(rows, tokenRow{Toks: []token{t}, Top: t.Y0, Bot: t.Y1})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Top < rows[j].Top })
	for i := range rows {
		r := rows[i].Toks
		sort.SliceStable(r, func(a, b int) bool { return r[a].X0 < r[b].X0 })
	}
	return rows
}

// findTableBand находит шапку и границы полосы данных.
//
// Шапка ищется как окно из одной-трёх подряд идущих строк, в котором суммарно
// опознаётся не меньше двух подписей граф. Окно, а не строка: подпись «Сумма
// без НДС, руб.» переносится, и в отдельно взятой строке видно только «Сумма».
//
// Возвращает (индекс первой строки шапки, индекс первой строки данных,
// индекс последней строки данных). head < 0 — таблицы нет.
func findTableBand(rows []tokenRow) (head, first, last int) {
	head = -1
	for i := range rows {
		for w := 1; w <= 3 && i+w <= len(rows); w++ {
			known, cells := 0, 0
			seen := map[string]bool{}
			for _, r := range rows[i : i+w] {
				for _, t := range r.Toks {
					cells++
					role := guessRole(t.Text)
					if role != "" && role != "other" && !seen[role] {
						seen[role] = true
						known++
					}
				}
			}
			// Две разные знакомые подписи и не меньше трёх слов — это шапка,
			// а не случайное «Сумма НДС» в тексте договора.
			if known >= 2 && cells >= 3 {
				head = i
				first = i + w
				break
			}
		}
		if head >= 0 {
			break
		}
	}
	if head < 0 || first >= len(rows) {
		return -1, 0, 0
	}

	// Шапка почти всегда переносится: «Ставка» / «НДС %», «Сумма» / «с НДС».
	// Дотягиваем её вниз, пока строки не содержат ни одного числа — позиция
	// без единой цифры в бухгалтерской таблице не встречается.
	for first < len(rows)-1 && isHeaderContinuation(rows[first]) {
		first++
	}

	last = first - 1
	for i := first; i < len(rows); i++ {
		joined := rowText(rows[i])
		// «Итого» проверяем раньше стоп-слов: reTableStop содержит и «итого», и
		// «всего», и без этой перестановки строка итога обрывала полосу, так и
		// не попав в таблицу.
		if reTotalsRow.MatchString(joined) {
			last = i
			break
		}
		if reTableStop.MatchString(joined) {
			break
		}
		// Полоса кончилась: пошёл сплошной текст без чисел и без структуры.
		if len(rows[i].Toks) < 2 && !reNumberish.MatchString(joined) {
			// одиночная строка-хвост наименования — не обрываем
			if i > first && len([]rune(joined)) > 60 {
				break
			}
		}
		last = i
	}
	if last < first {
		return -1, 0, 0
	}
	return head, first, last
}

// isHeaderContinuation — перенос подписей граф на следующую строку. Отличаем
// от позиции по отсутствию чисел: «НДС %» и «с НДС» цифр не несут, а любая
// позиция таблицы несёт хотя бы количество или сумму.
func isHeaderContinuation(r tokenRow) bool {
	if len(r.Toks) == 0 {
		return false
	}
	for _, t := range r.Toks {
		if reNumberish.MatchString(t.Text) {
			return false
		}
		// Длинный текст — это уже не подпись графы, а строка документа.
		if len([]rune(t.Text)) > 24 {
			return false
		}
	}
	return true
}

func rowText(r tokenRow) string {
	parts := make([]string, 0, len(r.Toks))
	for _, t := range r.Toks {
		parts = append(parts, t.Text)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// columnBands находит границы граф: проецируем боксы всех слов полосы на ось X
// и режем по зазорам, где нет ни одного слова. Ширина зазора — от средней
// ширины символа в полосе, чтобы пробел между словами внутри графы не сошёл за
// границу колонки.
func columnBands(rows []tokenRow) [][2]float64 {
	type seg struct{ a, b float64 }
	var segs []seg
	var widths []float64
	minX, maxX := math.Inf(1), math.Inf(-1)
	for _, r := range rows {
		for _, t := range r.Toks {
			segs = append(segs, seg{t.X0, t.X1})
			if n := len([]rune(t.Text)); n > 0 {
				widths = append(widths, (t.X1-t.X0)/float64(n))
			}
			minX = math.Min(minX, t.X0)
			maxX = math.Max(maxX, t.X1)
		}
	}
	if len(segs) < 4 || math.IsInf(minX, 1) {
		return nil
	}
	charW := median(widths)
	if charW <= 0 {
		charW = 5
	}
	// Порог зазора намеренно маленький. Границей графы становится не всякий
	// широкий разрыв, а только вертикальная полоса, свободная ВО ВСЕХ строках
	// полосы сразу: пробел между словами внутри наименования в одной строке
	// перекрыт длинным наименованием в другой, и полосой не станет. Большой
	// порог, наоборот, склеивает соседние узкие графы — «Ед. изм.» с «Кол-во».
	gapMin := math.Max(charW*0.35, 2)

	sort.Slice(segs, func(i, j int) bool { return segs[i].a < segs[j].a })
	var merged []seg
	cur := segs[0]
	for _, s := range segs[1:] {
		if s.a <= cur.b+gapMin {
			if s.b > cur.b {
				cur.b = s.b
			}
			continue
		}
		merged = append(merged, cur)
		cur = s
	}
	merged = append(merged, cur)

	bands := make([][2]float64, 0, len(merged))
	for i, m := range merged {
		lo := m.a
		hi := m.b
		// Границу между графами ставим по середине зазора: слово, чуть
		// вылезшее за свою графу, останется в ней.
		if i > 0 {
			lo = (merged[i-1].b + m.a) / 2
		} else {
			lo = m.a - charW
		}
		if i < len(merged)-1 {
			hi = (m.b + merged[i+1].a) / 2
		} else {
			hi = m.b + charW
		}
		bands = append(bands, [2]float64{lo, hi})
	}
	return bands
}

// dataRows отбрасывает из полосы итоговую строку и строки-переносы
// наименования: границы граф считаются по настоящим позициям, где каждое
// значение стоит в своей клетке.
func dataRows(rows []tokenRow) []tokenRow {
	out := make([]tokenRow, 0, len(rows))
	for _, r := range rows {
		joined := rowText(r)
		if joined == "" || reTotalsRow.MatchString(joined) {
			continue
		}
		// Строка без единого числа — это перенос наименования, а не позиция.
		if !reNumberish.MatchString(joined) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// bandOf возвращает индекс графы, в которую попадает центр слова.
func bandOf(t token, bands [][2]float64) int {
	c := (t.X0 + t.X1) / 2
	for i, b := range bands {
		if c >= b[0] && c < b[1] {
			return i
		}
	}
	if c < bands[0][0] {
		return 0
	}
	return len(bands) - 1
}

func rowCells(r tokenRow, bands [][2]float64) []string {
	buf := make([][]string, len(bands))
	for _, t := range r.Toks {
		// Бокс, растянутый через несколько граф, режем по словам. Surya
		// нередко отдаёт всю левую часть шапки одной строкой — «№
		// Наименование товара Ед. изм. Кол-во Цена». Целиком такой бокс
		// уходил в графу наименования, и роли пяти колонок определялись по
		// одной подписи: «Кол-во» в ней срабатывало первым, поэтому графа
		// наименования уезжала в количество.
		for i, part := range splitAcrossBands(t, bands) {
			if part != "" {
				buf[i] = append(buf[i], part)
			}
		}
	}
	out := make([]string, len(bands))
	for i, parts := range buf {
		out[i] = strings.TrimSpace(strings.Join(parts, " "))
	}
	return out
}

// splitAcrossBands раскладывает слова одного бокса по графам. Координата слова
// оценивается пропорционально его длине в символах: точных границ внутри бокса
// движок не даёт, но графы шире одного символа, и такой оценки достаточно.
// Бокс, целиком лежащий в одной графе, возвращается без изменений.
func splitAcrossBands(t token, bands [][2]float64) []string {
	out := make([]string, len(bands))
	words := strings.Fields(t.Text)
	if len(words) < 2 {
		out[bandOf(t, bands)] = t.Text
		return out
	}
	// Бокс не выходит за свою графу — резать нечего.
	i0, i1 := bandIndexAt(t.X0, bands), bandIndexAt(t.X1, bands)
	if i0 == i1 {
		out[i0] = t.Text
		return out
	}

	width := t.X1 - t.X0
	total := 0
	for _, w := range words {
		total += len([]rune(w))
	}
	total += len(words) - 1 // пробелы
	if total <= 0 {
		out[bandOf(t, bands)] = t.Text
		return out
	}
	per := width / float64(total)

	cur := t.X0
	parts := make([][]string, len(bands))
	for _, w := range words {
		n := float64(len([]rune(w)))
		center := cur + n*per/2
		idx := bandIndexAt(center, bands)
		parts[idx] = append(parts[idx], w)
		cur += (n + 1) * per
	}
	for i := range parts {
		out[i] = strings.Join(parts[i], " ")
	}
	return out
}

// bandIndexAt — номер графы, в которую попадает координата x.
func bandIndexAt(x float64, bands [][2]float64) int {
	for i, b := range bands {
		if x >= b[0] && x < b[1] {
			return i
		}
	}
	if x < bands[0][0] {
		return 0
	}
	return len(bands) - 1
}

// headerTitles склеивает подписи граф из всех строк шапки. Многострочная шапка
// («Сумма» / «без НДС, руб.») собирается в один заголовок в порядке сверху вниз.
func headerTitles(headRows []tokenRow, bands [][2]float64) []string {
	parts := make([][]string, len(bands))
	for _, r := range headRows {
		cells := rowCells(r, bands)
		if isNumberingRow(cells) {
			continue // ряд «1 2 3 4 5» под шапкой — не подпись
		}
		for i, v := range cells {
			if v != "" {
				parts[i] = append(parts[i], v)
			}
		}
	}
	out := make([]string, len(bands))
	for i := range parts {
		out[i] = strings.TrimSpace(strings.Join(parts[i], " "))
	}
	return out
}

// headerRowsAbove отбирает строки шапки из полосы над данными.
//
// Именно отбирает, а не обходит с обрывом: обход вверх до первой неподходящей
// строки терял всю шапку целиком, если ближайшая к данным строка почему-то не
// проходила отбор. Здесь берётся тот же диапазон, что и раньше, и из него
// выбрасываются строки, похожие на текст документа. Если выбросить пришлось
// всё — возвращаем диапазон как есть: лишние слова в заголовке неприятны, но
// заголовки без названий хуже.
func headerRowsAbove(rows []tokenRow, head, first int, bands [][2]float64) []tokenRow {
	lo := head
	if lo < 0 {
		lo = 0
	}
	if first > len(rows) {
		first = len(rows)
	}
	if lo >= first {
		return nil
	}
	span := rows[lo:first]
	picked := make([]tokenRow, 0, len(span))
	started := false
	for _, r := range span {
		wide, narrow := headerRowKind(r, bands)
		if wide {
			picked = append(picked, r)
			started = true
			continue
		}
		// Строка, укладывающаяся в одну графу, идёт в шапку только после того,
		// как началась сама шапка. Так «НДС %» под «Ставка» и «с НДС» под
		// «Сумма» остаются подписями, а «руб.» над правым краем таблицы —
		// нет: он стоит выше первой строки заголовков.
		if narrow && started {
			picked = append(picked, r)
		}
	}
	if len(picked) == 0 {
		return span
	}
	// Больше трёх рядов подписей не бывает; берём ближайшие к данным.
	if len(picked) > 3 {
		picked = picked[len(picked)-3:]
	}
	return picked
}

// headerRowKind классифицирует строку над данными.
//
// wide — полноценный ряд подписей: раскладывается минимум по двум графам.
// narrow — обрывок подписи в одной графе («НДС %», «с НДС»): в шапку идёт
// только как продолжение уже начатой шапки.
//
// В обоих случаях в строке не должно быть цифр: в подписи графы их не бывает,
// а в строке документа над таблицей они есть почти всегда — «аренды№49»,
// «от17.03.20г», «2023г». Отбор по цифрам, а не по длине текста: surya
// склеивает всю левую часть шапки в один широкий бокс, и ограничение по длине
// выбросило бы настоящий заголовок вместе с мусором.
func headerRowKind(r tokenRow, bands [][2]float64) (wide, narrow bool) {
	if len(r.Toks) == 0 {
		return false, false
	}
	for _, t := range r.Toks {
		if reNumberish.MatchString(t.Text) {
			return false, false
		}
	}
	filled := 0
	for _, c := range rowCells(r, bands) {
		if strings.TrimSpace(c) != "" {
			filled++
		}
	}
	switch {
	case filled >= 2:
		return true, false
	case filled == 1:
		return false, true
	}
	return false, false
}

// isNumberingRow распознаёт ряд с номерами граф: все непустые ячейки — числа
// подряд, начиная с 1. В бланках он стоит под шапкой и данными не является.
func isNumberingRow(cells []string) bool {
	n, prev := 0, 0
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !reColNumbering.MatchString(c) {
			return false
		}
		v := 0
		for _, r := range c {
			v = v*10 + int(r-'0')
		}
		if v <= prev {
			return false
		}
		prev = v
		n++
	}
	// Порог два, а не три. Ряд «1 2 3 4 5 6 7 8» под шапкой OCR прочитал не
	// все цифры ряда — в логах он приходил как «2 3 4» и даже как «2 3», и
	// тогда проверка не срабатывала, а ряд вставал в таблицу первой позицией
	// с количеством 2 и ценой 3. Остальные условия жёсткие: только цифры,
	// строго по возрастанию, наибольшее значение не больше числа граф —
	// настоящая позиция так выглядеть не может, в ней есть наименование.
	return n >= 2 && prev <= len(cells)+2
}

// isNameContinuation — строка, где заполнена только текстовая графа: это хвост
// наименования предыдущей позиции, перенесённый на новую строку.
func isNameContinuation(cells []string, roles []string) bool {
	filled, nameOnly := 0, true
	for i, c := range cells {
		if strings.TrimSpace(c) == "" {
			continue
		}
		filled++
		if reNumberish.MatchString(c) {
			return false
		}
		if i < len(roles) && roles[i] != "name" && roles[i] != "other" && roles[i] != "" {
			nameOnly = false
		}
	}
	return filled > 0 && filled <= 2 && nameOnly
}

func appendToFreeName(row []string, cells []string, roles []string) {
	for i, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" || i >= len(row) {
			continue
		}
		if row[i] == "" {
			row[i] = c
			continue
		}
		row[i] = strings.TrimSpace(row[i] + " " + c)
	}
	_ = roles
}

// dropDegenerateRows выкидывает строки-повторы: когда детектор или проекция
// растиражировали один и тот же текст по нескольким рядам. В интерфейсе это
// выглядело как восемь одинаковых строк вместо двух настоящих позиций.
func dropDegenerateRows(rows [][]string) [][]string {
	seen := map[string]bool{}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		key := strings.Join(r, "\x00")
		if strings.TrimSpace(strings.ReplaceAll(key, "\x00", "")) == "" {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}
