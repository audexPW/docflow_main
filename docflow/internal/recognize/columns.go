package recognize

import (
	"math"
	"regexp"
	"sort"
	"strconv"
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
	Line int // строка движка, 0 — неизвестно
	Seq  int // порядок в выдаче движка
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
// ── строка итога, не опознанная по слову ────────────────────────
//
// Распознавание уродует слово «ИТОГО» («ОТОТИ», «ИТ0ГО», «MTOFO»), и строка
// итога уезжает в позиции. Внешне таблица выглядит разобранной, а сумма
// позиций оказывается ровно вдвое больше настоящей (IMG_0141: 95,48 вместо
// 47,74) — в 1С уйдёт удвоенный документ.
//
// Слово тут ненадёжно, а арифметика надёжна: строка, значения которой по
// нескольким колонкам равны суммам предыдущих строк, и есть итог, как бы её
// ни распознали.

func cellNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// пробелы-разделители разрядов бывают обычные, неразрывные и узкие
	s = strings.NewReplacer(" ", "", "\u00a0", "", "\u202f", "", "\u2009", "", ",", ".").Replace(s)
	if !reSimpleNumber.MatchString(s) {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

var reSimpleNumber = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// takeGarbledTotalsRow — если ПОСЛЕДНЯЯ строка таблицы на деле является строкой
// итога, забрать её из позиций в Totals.
//
// Зачем: распознавание уродует слово «ИТОГО» («ОТОТИ», «ИТ0ГО»), строка итога
// остаётся в позициях, и сумма позиций оказывается вдвое больше настоящей —
// в 1С уходит удвоенный документ.
//
// Почему так осторожно:
//   - Проверяется ТОЛЬКО последняя строка. Итог печатается в конце таблицы, а
//     обычная позиция в середине, случайно равная сумме предыдущих, раньше
//     молча выбрасывалась.
//   - Считаются ТОЛЬКО денежные графы. Графа «№ п/п» давала бесплатное
//     совпадение на третьей строке (3 = 1 + 2), а графа количества — на
//     «2 = 1 + 1», и порог «две графы» фактически равнялся одной.
//   - Графы НДС и «всего» — линейные функции базовой суммы: если сошлась база,
//     они сойдутся всегда. Поэтому нужна не просто пара совпадений, а
//     совпадение по графе с ролью amount (базовая сумма) плюс ещё одна.
//   - Данные не пропадают: если Totals уже заполнен, строка остаётся позицией.
func takeGarbledTotalsRow(ft *domain.FreeTable) bool {
	if len(ft.Rows) < 3 {
		return false
	}
	// Totals считается занятым, только если там есть ЧИСЛА. Часто туда попадает
	// строка «Всего … сорок семь белорусских рублей 74 копейки» — сумма прописью
	// без единой цифры, а настоящий числовой итог остаётся в позициях.
	//
	// Занятый Totals больше не повод выйти: при «ИТ0ГО … 36,78» в
	// таблице и «Всего к оплате с НДС: 36,78» ниже изуродованная строка оставалась
	// позицией, и документ удваивался. Теперь её забираем, если её суммы
	// совпадают с уже найденным итогом; не совпадают — строка остаётся позицией.
	occupied := rowHasMoney(ft.Totals)
	last := ft.Rows[len(ft.Rows)-1]
	prev := ft.Rows[:len(ft.Rows)-1]

	// У строки итога наименования нет: там либо пусто, либо огрызок слова
	// «ИТОГО», изуродованный распознаванием («ОТОТИ», «ИТ0ГО»). Если в графе
	// наименования стоит название услуги или товара — это ПОЗИЦИЯ, и сумма,
	// случайно совпавшая с предыдущими, ничего не доказывает: графы НДС и
	// «всего» линейны от базы и совпадают все разом.
	if rowHasRealName(ft.Roles, last) {
		return false
	}

	baseHit, hits := false, 0
	for c := range last {
		if !isMoneyColumn(ft.Roles, c) {
			continue
		}
		// роль не определена — судим по содержимому: графы № и количества
		// складываются «бесплатно» (3 = 1 + 2), а деньги пишутся с копейками
		if roleUnknown(ft.Roles, c) && !reMoneyWritten.MatchString(strings.TrimSpace(last[c])) {
			continue
		}
		v, ok := cellNumber(last[c])
		if !ok || v == 0 {
			continue
		}
		sum, cnt := 0.0, 0
		for _, r := range prev {
			if c >= len(r) {
				continue
			}
			if pv, ok := cellNumber(r[c]); ok {
				sum += pv
				cnt++
			}
		}
		// сумма должна складываться почти из всех предыдущих строк, а не из
		// случайного подмножества. Одну строку прощаем: в начале таблицы часто
		// стоит обломок шапки без чисел.
		if cnt < 2 || cnt < len(prev)-1 {
			continue
		}
		// допуск — копейки округления, а не доля суммы: на крупных документах
		// относительный допуск превращается в тысячи рублей
		if math.Abs(sum-v) <= 0.02+0.01*float64(len(prev)) {
			hits++
			if roleAt(ft.Roles, c) == "amount" {
				baseHit = true
			}
		}
	}
	// роли не определены — тогда одного совпадения мало, требуем два
	if (baseHit && hits >= 2) || (!hasAmountRole(ft.Roles) && hits >= 2) {
		// прописную строку не теряем: она остаётся, если числовой итог короче
		if rowHasMoney(last) && (!occupied || sharesMoney(ft.Totals, last)) {
			ft.Totals = last
			ft.Rows = prev
			return true
		}
	}
	return false
}

// rowHasMoney — есть ли в строке денежная сумма. Судим по НАПИСАНИЮ, а не по
// значению: «60,00» после разбора становится целым числом 60 и по значению
// неотличимо от ставки НДС. Деньги в этих документах всегда пишутся с копейками.
// За копейками не может идти ещё точка или цифра: «14.09.2026» — дата, а не 14,09
// (дата в строке «Всего по счёту от …» делала Totals «занятым»).
var reMoneyWritten = regexp.MustCompile(`\d+[.,]\d{2}([^\d.,]|$)`)

func rowHasMoney(cells []string) bool {
	for _, c := range cells {
		if reMoneyWritten.MatchString(strings.TrimSpace(c)) {
			return true
		}
		// суммы от сотни печатают и без копеек
		if v, ok := cellNumber(c); ok && v >= 100 {
			return true
		}
	}
	return false
}

func roleAt(roles []string, c int) string {
	if c < len(roles) {
		return roles[c]
	}
	return ""
}

func hasAmountRole(roles []string) bool {
	for _, r := range roles {
		if r == "amount" {
			return true
		}
	}
	return false
}

// isMoneyColumn — графа, где суммирование по строкам осмысленно. Номер строки,
// количество, единица измерения и ставка НДС сюда не входят: они складываются
// «бесплатно» и дают ложные совпадения. Цена за единицу тоже не суммируется.
func isMoneyColumn(roles []string, c int) bool {
	switch roleAt(roles, c) {
	case "amount", "vat", "amount_no_vat", "total":
		return true
	// «index» — роль графы «№ п/п» из rolesFor; «number» оставлено для старых вызовов
	case "name", "qty", "unit", "price", "vat_rate", "number", "index":
		return false
	}
	// роль не определена — судим по содержимому на месте вызова
	return true
}

func roleUnknown(roles []string, c int) bool {
	r := roleAt(roles, c)
	return r == "" || r == "other"
}

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
		toks = append(toks, token{Text: t, X0: w.X0, Y0: w.Y0, X1: w.X1, Y1: w.Y1, Line: w.Line, Seq: w.Seq})
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
	// Считаем по строкам данных И по подписям шапки.
	//
	// Шапку раньше исключали намеренно: движок отдавал всю левую часть шапки
	// одним широким боксом, тот перекрывал зазор между графами, и графы
	// склеивались. Теперь у каждого слова свои координаты, и подписи шапки
	// стали лучшим свидетельством о границах граф — особенно там, где строк
	// данных одна-две. Именно из-за их отсутствия в счёте-фактуре из восьми
	// граф получалось пять, а «Цена», «Кол-во» и «производителя» сваливались
	// в один заголовок.
	//
	// Строки шапки с цифрами не берём: так в расчёт не попадёт строка
	// документа над таблицей («Договор аренды№49 от17.03.20г»).
	dataOnly := dataRows(rows[first : last+1])
	headRows := headerBandRows(rows, head, first)

	bands := columnBands(dataOnly)
	// Шапка имеет право только ДОБАВИТЬ границы, но не убрать.
	//
	// Обычно подписи граф — лучшее свидетельство о границах: в счёте-фактуре
	// строк данных всего одна-две, и по ним из восьми граф выходило пять.
	// Но шапку OCR иногда читает мусором на всю ширину листа, и тогда она
	// склеивает всё в одну графу. Поэтому берём вариант с шапкой, только
	// если граф от этого стало не меньше.
	if withHead := columnBands(append(append([]tokenRow{}, dataOnly...), headRows...)); len(withHead) >= len(bands) {
		bands = withHead
	}
	// И ещё раз: подписи внутри уже найденной графы, разнесённые заметным
	// зазором, делят её. Слить графы этот шаг не может по построению.
	bands = splitBandsByHeader(bands, headRows)
	// Наименование, разрезанное растянутым пробелом на две графы, — одна графа.
	bands = mergeTextBands(bands, dataOnly)
	// Плотная широкая таблица: правые графы сползают вниз от графы к графе,
	// и значения позиции прилипают к следующей строке. Пересобираем строки
	// полосы с поправкой на этот сдвиг (table_skew.go).
	if aligned, ok := alignColumnDrift(rows[first:last+1], bands); ok {
		fixed := make([]tokenRow, 0, len(rows))
		fixed = append(fixed, rows[:first]...)
		fixed = append(fixed, aligned...)
		fixed = append(fixed, rows[last+1:]...)
		last = first + len(aligned) - 1
		rows = fixed
		dataOnly = dataRows(rows[first : last+1])
	}
	bands = splitBandsByNumbers(bands, dataOnly)
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
		if isNumberingRow(cells) || isColumnNumberingText(joined) {
			continue
		}
		if reTotalsRow.MatchString(joined) {
			if len(ft.Totals) == 0 {
				ft.Totals = cells
			}
			continue
		}
		// Сумма прописью без цифр («Тридцать шесть рублей») — подвал, а не перенос
		// наименования. Приклеенная к изуродованному «ИТ0ГО», она делала из строки
		// итога позицию с «настоящим названием», и документ удваивался.
		if !reNumberish.MatchString(joined) && isSpelledAmount(joined) {
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

	MergeNameFragments(ft)
	// Итог сверяется по ПОЛНОМУ набору строк, до схлопывания повторов: законные
	// одинаковые позиции («Аренда» дважды) схлопывались, сумма переставала
	// сходиться, и итог оставался позицией. Не сошлось — вторая
	// попытка после схлопывания, для повторов, наплоденных распознаванием.
	taken := takeGarbledTotalsRow(ft)
	ft.Rows = dropDegenerateRows(ft.Rows)
	if !taken {
		takeGarbledTotalsRow(ft)
	}
	if len(ft.Rows) == 0 {
		return nil
	}
	return ft
}

// groupTokenRows собирает слова в строки по вертикальному перекрытию. Порог по
// перекрытию, а не по расстоянию между верхами боксов: на скане с наклоном
// строка уезжает, и сравнение верхов рвёт её пополам.
// ── поправка на поворот листа ───────────────────────────────────
//
// Часть сканов снята с рук и повёрнута на 2-3 градуса. Строка при этом уходит
// вниз пропорционально сдвигу вправо: на листе шириной 2300 точек это 60-100
// точек, то есть три-четыре высоты слова. Группировка строк по перекрытию
// боксов такую строку неизбежно рвёт, куски прилипают к соседним строкам, и
// дальше рушится всё: шапка смешивается с первой позицией, значения уезжают
// «лесенкой» (IMG_0126, 0201, 0216).
//
// Это НЕ тот сдвиг, что лечит table_skew.go: там ступенька от графы к графе,
// накопительная и не связанная с координатой X. Здесь — обычный поворот, и
// правится он до группировки, одним наклоном всей страницы.
//
// Угол берём медианой по парам слов, стоящих заведомо на одной строке:
// разнесены по горизонтали не меньше чем на 150 точек, а по вертикали не
// больше чем на высоту слова. Медиана устойчива к случайным парам.

func estimateSkewBand(toks []token, lo, hi float64) float64 {
	var slopes []float64
	var in []token
	for _, t := range toks {
		if t.Y0 >= lo && t.Y0 <= hi {
			in = append(in, t)
		}
	}
	for i := 0; i < len(in); i++ {
		h := in[i].Y1 - in[i].Y0
		if h <= 0 {
			continue
		}
		for j := i + 1; j < len(in); j++ {
			// Ограничение. Направление наклона оценка различает:
			// лист с k < 0 выпрямляется (TestDeskewNoDoubleShift, k = -0.03). Но
			// замер на синтетике показал, что k = -0.045 не выпрямляется; причина
			// не установлена. Симметричный перебор (dx<0 → развернуть пару)
			// пробовали: роняет замер с 28 из 36 до 26 и ломает тест перекоса —
			// пары «назад» связывают слова из разных строк. Чинить вместе с
			// нормальной кластеризацией строк.
			dx := in[j].X0 - in[i].X0
			dy := in[j].Y0 - in[i].Y0
			if dx < 150 || dx > 2000 {
				continue
			}
			if math.Abs(dy) > h*1.2 {
				continue
			}
			slopes = append(slopes, dy/dx)
		}
	}
	if len(slopes) < 12 {
		return 0
	}
	sort.Float64s(slopes)
	k := slopes[len(slopes)/2]
	if math.Abs(k) < 0.020 || math.Abs(k) > 0.05 {
		return 0
	}
	return k
}

// deskewTokens выпрямляет слова ПОЛОСАМИ по высоте листа: перекос бывает не у
// всей страницы, а только там, где данные впечатаны поверх ровного бланка
// (IMG_0126: по всему листу наклон ноль, в табличной зоне 0.031).
func deskewTokens(toks []token) []token {
	if len(toks) == 0 {
		return toks
	}
	minY, maxY := toks[0].Y0, toks[0].Y0
	for _, t := range toks {
		if t.Y0 < minY {
			minY = t.Y0
		}
		if t.Y0 > maxY {
			maxY = t.Y0
		}
	}
	const band = 160.0
	out := make([]token, len(toks))
	copy(out, toks)
	for lo := minY; lo <= maxY; lo += band {
		hi := lo + band
		k := estimateSkewBand(toks, lo-band/2, hi+band/2)
		if k == 0 {
			continue
		}
		// принадлежность полосе берём по ИСХОДНОЙ координате: если читать
		// уже сдвинутый out, слово, опустившееся в следующую полосу, получит
		// вторую поправку
		for i := range out {
			if toks[i].Y0 >= lo && toks[i].Y0 < hi {
				d := k * toks[i].X0
				out[i].Y0 -= d
				out[i].Y1 -= d
			}
		}
	}
	return out
}

// groupTokenRows собирает слова в строки. Поправку на перекос применяем не
// вслепую: строим строки и так, и так, и берём вариант, где строк МЕНЬШЕ.
// Правильная геометрия склеивает обрывки, неправильная плодит их — на плотном
// счёте IMG_0217 поправка добавляла две лишние строки и ломала уже собранную
// таблицу, хотя другим документам помогала.
func groupTokenRows(toks []token) []tokenRow {
	plain := groupTokenRowsRaw(toks)
	fixed := groupTokenRowsRaw(deskewTokens(toks))
	if rowsDensity(fixed) > rowsDensity(plain)*1.02 {
		return fixed
	}
	return plain
}

// rowsDensity — насколько плотно набиты строки. Слова, разорванные по строкам
// из-за перекоса, дают много коротких строк; собранные — меньше и длиннее.
// Считаем среднее число слов в строке, игнорируя строки из одного слова:
// в шапке и подписях они законны и картину искажают.
func rowsDensity(rows []tokenRow) float64 {
	words, cnt := 0, 0
	for _, r := range rows {
		if len(r.Toks) < 2 {
			continue
		}
		words += len(r.Toks)
		cnt++
	}
	if cnt == 0 {
		return 0
	}
	return float64(words) / float64(cnt)
}

func groupTokenRowsRaw(toks []token) []tokenRow {
	sorted := make([]token, len(toks))
	copy(sorted, toks)
	sort.SliceStable(sorted, func(i, j int) bool {
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
		orderRowTokens(&rows[i])
	}
	return rows
}

// orderRowTokens расставляет слова строки в порядке чтения.
func orderRowTokens(row *tokenRow) {
	if rowHasEngineOrder(*row) {
		// Порядок чтения известен от движка: слова одной его строки
		// идут в выданном порядке, разные строки — слева направо.
		// Сортировка по X здесь врёт: на тексте, выровненном по ширине,
		// координаты слов внутри строки у surya не монотонны, и строка
		// «больница», именуемое в дальнейшем Арендодатель…» уходила в
		// модель как «больница», в лице именуемое в дальнейшем главного…».
		sortByEngineOrder(row.Toks)
		return
	}
	if rowBoxesDegenerate(*row) {
		// Боксы слов совпадают с боксом всей строки (движок отдаёт
		// координаты строки, а не слова). Сортировка по X0 тогда
		// перемешивает слова по шумовой разнице в 1-3 пикселя.
		// Порядок выдачи OCR — уже порядок чтения, оставляем его.
		//
		// Страховка на случай, когда SplitLineBoxes не распознала
		// вырожденные боксы: там строки склеиваются по совпадению
		// прямоугольника с допуском в три пикселя, здесь — по доле
		// ширины строки. Разные признаки одной и той же беды.
		return
	}
	r := row.Toks
	sort.SliceStable(r, func(a, b int) bool { return r[a].X0 < r[b].X0 })
}

// degenerateBoxShare — доля слов, чей бокс шире 60% всей строки, начиная с
// которой координаты считаются строчными, а не пословными.
const degenerateBoxShare = 0.6

// rowBoxesDegenerate — у слов строки нет собственных координат: бокс каждого
// слова примерно равен боксу всей строки.
func rowBoxesDegenerate(r tokenRow) bool {
	if len(r.Toks) < 3 {
		return false
	}
	minX, maxX := math.Inf(1), math.Inf(-1)
	for _, t := range r.Toks {
		minX = math.Min(minX, t.X0)
		maxX = math.Max(maxX, t.X1)
	}
	span := maxX - minX
	if span <= 0 {
		return true
	}
	wide := 0
	for _, t := range r.Toks {
		if (t.X1-t.X0)/span > degenerateBoxShare {
			wide++
		}
	}
	return wide*2 > len(r.Toks)
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
	// Кандидатов на шапку перебираем все и берём лучшего, а не первого
	// попавшегося.
	//
	// Первый попавшийся — это как раз то, что уводило разбор в блок
	// реквизитов: «Арендодатель / Его адрес / УНП / Расчетный счет» тоже
	// разложен в две колонки, а слова «работах» и «услугах» в названии акта
	// опознаются как подпись графы наименования. Полоса начиналась там, и в
	// таблицу приезжали адреса и расчётные счета, а настоящая товарная часть
	// оставалась ниже, за пределами полосы.
	//
	// Настоящую шапку от такого блока отличает две вещи: в ней больше
	// узнанных подписей граф, и под ней стоят суммы. По этому и выбираем;
	// при равном счёте побеждает верхний кандидат, как было раньше.
	head = -1
	bestScore := -1
	pageW := pageTextWidth(rows)
	for i := range rows {
		// Абзац шапкой таблицы не бывает. «3. Арендная плата за арендованное
		// имущество…» набирает две «подписи граф» из обычных слов, и без этой
		// проверки полоса таблицы начиналась посреди текста акта.
		if isProseRow(rows[i], pageW) || (envBool("TABLE_PROSE_CHECK", true) && reRowMoney.MatchString(rowText(rows[i]))) {
			continue
		}
		for w := 1; w <= 3 && i+w <= len(rows); w++ {
			// Сумма денег в подписи графы не печатается: «в размере 5,07» —
			// это строка текста, в которой встретилось слово «НДС».
			if w > 1 && (isProseRow(rows[i+w-1], pageW) || (envBool("TABLE_PROSE_CHECK", true) && reRowMoney.MatchString(rowText(rows[i+w-1])))) {
				break
			}
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
			if known < 2 || cells < 3 {
				continue
			}
			// Считаем не количество денежных строк, а сам факт их наличия:
			// количество тянуло выбор вниз по листу, к строке после «Итого»,
			// под которой денежных строк оказывалось больше всего.
			score := known * 10
			if moneyRowsBelow(rows, i+w) > 0 {
				score += 5
			}
			if score > bestScore {
				bestScore = score
				head = i
				first = i + w
			}
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
	// Полоса, которая наполовину состоит из абзацев, — не таблица, а текст,
	// выровненный по ширине: просветы между словами там растянуты и выглядят
	// как границы граф только в пределах одной строки.
	if bandIsProse(rows, head, first, last) {
		return -1, 0, 0
	}
	return head, first, last
}

// findTableBandAny — полоса таблицы: сначала по шапке, как 9 сентября, а если
// шапку опознать не удалось (подписи граф в две строки, скан под углом, строка
// с УНП, которая влезает в заголовок) — по самим строкам: три-четыре числа,
// стоящие по колонкам, это ряд таблицы и без шапки. Границы такой полосы
// уточняются: сверху — по строкам подписей граф, снизу — по строке «Итого»,
// чтобы не затянуть сверху текст договора, а снизу сумму прописью
// (tablerows.go).
//
// Там, где шапка нашлась, полоса остаётся прежней: меняется только её верх,
// если в шапку попала строка с числами (та самая строка с УНП).
func findTableBandAny(rows []tokenRow) (head, first, last int) {
	head, first, last = findTableBand(rows)
	if head < 0 {
		nf, nl := numericRowsBand(rows)
		if nf < 0 {
			return -1, 0, 0
		}
		head, first, last = tableBounds(rows, nf, nl)
		if last < first || bandIsProse(rows, head, first, last) {
			return -1, 0, 0
		}
	}
	// Строка с УНП или иная строка с числами подписями граф не бывает.
	for head < first && (len(numericCells(rows[head])) >= 2 || !looksLikeHeaderRow(rows[head])) {
		head++
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
	// Порог зазора — полтора символа.
	//
	// Раньше здесь стояло 0.35 символа, и это было подогнано под сломанные
	// координаты: движок отдавал один прямоугольник на всю ячейку, внутри
	// ячейки зазоров не было вовсе, и любой порог работал. Теперь координаты
	// у каждого слова свои (linebox.go), между словами внутри графы стоит
	// пробел шириной примерно в символ — и порог меньше символа резал
	// наименование по словам: «Пеня | по | коммунальным | услугам».
	//
	// Полтора символа проходят между словами и не проходят между графами:
	// в бланках зазор между графами — минимум три-четыре символа.
	gapMin := math.Max(charW*1.5, 4)

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

// headerBandRows отбирает строки шапки, годные для расчёта границ граф: те,
// что лежат между найденным заголовком и первой строкой данных и не содержат
// ни одной цифры. Цифра в строке над таблицей означает, что это не подпись
// графы, а строка документа — номер договора, дата, период.
func headerBandRows(rows []tokenRow, head, first int) []tokenRow {
	if head < 0 {
		head = 0
	}
	if first > len(rows) {
		first = len(rows)
	}
	if head >= first {
		return nil
	}
	out := make([]tokenRow, 0, first-head)
	for _, r := range rows[head:first] {
		if len(r.Toks) == 0 {
			continue
		}
		numeric := false
		for _, t := range r.Toks {
			if reNumberish.MatchString(t.Text) {
				numeric = true
				break
			}
		}
		if numeric {
			continue
		}
		out = append(out, r)
	}
	return out
}

// splitBandsByHeader делит уже найденную графу, если внутри неё стоят две и
// более отдельные подписи шапки. Границы только добавляются: слить две графы
// этот шаг не может, поэтому он безопасен даже на мусорной шапке.
func splitBandsByHeader(bands [][2]float64, headRows []tokenRow) [][2]float64 {
	if len(bands) == 0 || len(headRows) == 0 {
		return bands
	}
	type seg struct{ a, b float64 }
	var segs []seg
	var widths []float64
	for _, r := range headRows {
		for _, t := range r.Toks {
			segs = append(segs, seg{t.X0, t.X1})
			if n := len([]rune(t.Text)); n > 0 {
				widths = append(widths, (t.X1-t.X0)/float64(n))
			}
		}
	}
	if len(segs) < 2 {
		return bands
	}
	charW := median(widths)
	if charW <= 0 {
		charW = 5
	}
	gapMin := math.Max(charW*1.5, 4)

	sort.Slice(segs, func(i, j int) bool { return segs[i].a < segs[j].a })
	var clusters []seg
	cur := segs[0]
	for _, s := range segs[1:] {
		if s.a <= cur.b+gapMin {
			if s.b > cur.b {
				cur.b = s.b
			}
			continue
		}
		clusters = append(clusters, cur)
		cur = s
	}
	clusters = append(clusters, cur)

	out := make([][2]float64, 0, len(bands))
	for _, b := range bands {
		var inside []seg
		for _, c := range clusters {
			mid := (c.a + c.b) / 2
			if mid >= b[0] && mid < b[1] {
				inside = append(inside, c)
			}
		}
		if len(inside) < 2 {
			out = append(out, b)
			continue
		}
		lo := b[0]
		for i := 0; i+1 < len(inside); i++ {
			cut := (inside[i].b + inside[i+1].a) / 2
			// Слишком узкий обрезок — не графа, а дрожание координат.
			if cut-lo < charW || b[1]-cut < charW {
				continue
			}
			out = append(out, [2]float64{lo, cut})
			lo = cut
		}
		out = append(out, [2]float64{lo, b[1]})
	}
	return out
}

// reRowMoney — денежное значение в строке: две цифры после запятой.
var reRowMoney = regexp.MustCompile(`\d[\d\s]*[.,]\d{2}`)

// moneyRowsBelow считает, сколько строк под кандидатом в шапку содержат
// суммы. Считаем до первой стоп-строки («Итого», подписи, «Всего отпущено»):
// дальше идёт подвал, и его денежные строки к таблице отношения не имеют.
func moneyRowsBelow(rows []tokenRow, from int) int {
	n := 0
	for i := from; i < len(rows) && i < from+40; i++ {
		joined := rowText(rows[i])
		if joined == "" {
			continue
		}
		if reTotalsRow.MatchString(joined) {
			break
		}
		if reTableStop.MatchString(joined) {
			break
		}
		if reRowMoney.MatchString(joined) {
			n++
		}
	}
	return n
}

// rowHasRealName — стоит ли в графе наименования настоящее название, а не
// огрызок итога. Судим по словам (totals_words.go): настоящее — хоть одно слово
// из трёх и более букв, которое не изуродованное «ИТОГО» и не хвост итоговой
// строки («по счёту», «НДС»). Прежний признак «два слова или длиннее 12 букв»
// оставлял позицией «ИТ0Г0 по счёту» и съедал «Теплоэнергию».
func rowHasRealName(roles []string, cells []string) bool {
	for c, cell := range cells {
		if roleAt(roles, c) != "name" {
			continue
		}
		t := strings.TrimSpace(cell)
		if t == "" {
			continue
		}
		if reTotalsRow.MatchString(t) {
			return false
		}
		for _, w := range strings.Fields(t) {
			if isNameWord(w) {
				return true
			}
		}
	}
	return false
}
