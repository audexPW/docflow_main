package recognize

// Чистка табличной части и выбор лучшего кандидата по арифметике документа.
//
// Раньше приоритет между разборщиками таблицы был зашит в код: сперва
// побеждала проекция по координатам слов, потом — сетка детектора. Оба
// правила одинаково слепые: источник выбирается заранее, без единого взгляда
// на то, что он выдал на этом конкретном бланке. Отсюда и «каша»: там, где
// выбранный разбор рассыпался, он всё равно оставался победителем.
//
// Здесь таблица сначала чистится (ряд номеров граф, подписи-разделители,
// переносы наименования, пустые графы, повторы), а потом получает оценку по
// арифметике самого документа: количество × цена = сумма, сумма без НДС + НДС
// = сумма с НДС, сумма строк = «Итого». Бланк проверяет сам себя, и берётся
// тот разбор, который сходится, — независимо от того, кто его собрал.

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"docflow/internal/domain"
)

var (
	// Ячейка целиком является числом (с разделителем разрядов и без).
	reNumericCell = regexp.MustCompile(`^-?\d{1,3}(?:[ \x{00A0}]?\d{3})*(?:[.,]\d{1,6})?\s*%?$`)
	// Мусор в графе наименования: туда заезжают реквизиты из шапки.
	reJunkName = regexp.MustCompile(`(?i)(р\s*/\s*с|расч[ёе]тн|унп|бик|окпо|адрес|тел\.|телефон|в\s+банке|дирекция|грузоотправ|грузополуч|поставщик|покупатель|плательщик|продавец|[A-Z]{2}\d{2}[A-Z]{4}\d{6})`)
	// Деньги внутри ячейки — для отличия подписи-разделителя от позиции.
	reCellMoney = regexp.MustCompile(`\d+[.,]\d{2}\b`)
)

// Замены символов, которые OCR стабильно путает в цифрах.
var ocrDigitFix = strings.NewReplacer(
	"О", "0", "о", "0", "O", "0", "o", "0",
	"З", "3", "з", "3",
	"Б", "6", "б", "6",
	"l", "1", "I", "1", "|", "1", "і", "1",
	"S", "5", "s", "5",
	"\u00A0", " ",
)

// cellNum разбирает содержимое ячейки как число.
func cellNum(s string) (float64, bool) {
	s = strings.TrimSpace(normalizeNumber(s))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// tidyNumber убирает разделители разрядов и приводит дробную часть к точке.
func tidyNumber(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\u00A0", "")
	s = strings.ReplaceAll(s, " ", "")
	pct := strings.HasSuffix(s, "%")
	s = strings.TrimSuffix(s, "%")
	// Разделителем дробной части считаем последний знак: «1.234,56» и
	// «1,234.56» — одно и то же число, записанное по-разному.
	lastDot := strings.LastIndexByte(s, '.')
	lastCom := strings.LastIndexByte(s, ',')
	cut := lastDot
	if lastCom > cut {
		cut = lastCom
	}
	if cut >= 0 {
		head := strings.NewReplacer(".", "", ",", "").Replace(s[:cut])
		s = head + "." + s[cut+1:]
	}
	if pct {
		s += "%"
	}
	return s
}

// repairNumericCell чинит числовую ячейку, испорченную распознаванием, и
// оставляет всё остальное как есть. Правка применяется только если после
// замен ячейка стала числом целиком: «кВт», «кв.ч» и наименования не трогаем.
func repairNumericCell(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return t
	}
	if reNumericCell.MatchString(t) {
		return tidyNumber(t)
	}
	if !strings.ContainsAny(t, "0123456789") {
		return t
	}
	if c := ocrDigitFix.Replace(t); reNumericCell.MatchString(c) {
		return tidyNumber(c)
	}
	return t
}

// cleanCell приводит ячейку к печатному виду: один пробел между словами,
// без переносов, с починенным числом.
func cleanCell(s string) string {
	s = strings.ReplaceAll(s, "\u00A0", " ")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, " |¦")
	return repairNumericCell(s)
}

// normalizeHeader — то же для подписи графы, но без починки чисел: заголовок
// числом быть не должен.
func normalizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\u00A0", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " |¦,;")
}

// isSectionRow — ряд-подпись поперёк таблицы: заполнена одна ячейка, в ней
// текст и нет денег. В счёте-фактуре так печатают адрес объекта
// («ул. Революционная, 11») между шапкой и позициями; в таблицу он попадал
// отдельной позицией и тянул за собой сдвиг всех колонок.
func isSectionRow(cells []string) bool {
	if len(cells) < 3 {
		return false
	}
	filled, idx := 0, -1
	for i, c := range cells {
		if strings.TrimSpace(c) != "" {
			filled++
			idx = i
		}
	}
	if filled != 1 || idx < 0 {
		return false
	}
	v := cells[idx]
	return hasLetters(v) && !reCellMoney.MatchString(v)
}

// rowEqualsHeader — ряд дословно повторяет шапку (сетка иногда отдаёт её и
// как заголовок, и как первую строку данных).
func rowEqualsHeader(cells, cols []string) bool {
	same, filled := 0, 0
	for i, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		filled++
		if i < len(cols) && strings.EqualFold(c, strings.TrimSpace(cols[i])) {
			same++
		}
	}
	return filled >= 2 && same == filled
}

// CleanFreeTable возвращает вычищенную копию таблицы. Исходная структура не
// меняется — кандидатов сравнивают между собой, и порча одного из них по
// дороге сделала бы сравнение бессмысленным.
func CleanFreeTable(in *domain.FreeTable) *domain.FreeTable {
	if in == nil {
		return nil
	}
	width := len(in.Columns)
	for _, r := range in.Rows {
		if len(r) > width {
			width = len(r)
		}
	}
	if len(in.Totals) > width {
		width = len(in.Totals)
	}
	if width == 0 {
		return nil
	}

	cols := make([]string, width)
	for i := 0; i < width && i < len(in.Columns); i++ {
		cols[i] = normalizeHeader(in.Columns[i])
	}
	roles := rolesFor(cols)

	var (
		rows      [][]string
		totals    []string
		hasTotals bool
	)
	for _, raw := range in.Rows {
		cells := padRow(raw, width)
		for i := range cells {
			cells[i] = cleanCell(cells[i])
		}
		joined := strings.TrimSpace(strings.Join(cells, " "))
		if joined == "" {
			continue
		}
		if isNumberingRow(cells) || isColumnNumberingText(joined) {
			continue
		}
		if rowEqualsHeader(cells, cols) {
			continue
		}
		if reTotalsRow.MatchString(joined) {
			if !hasTotals {
				totals, hasTotals = cells, true
			}
			continue
		}
		if isSectionRow(cells) {
			// До первой позиции это подпись раздела — она не строка
			// документа. После — почти всегда хвост наименования.
			if len(rows) > 0 {
				appendToFreeName(rows[len(rows)-1], cells, roles)
			}
			continue
		}
		if len(rows) > 0 && isNameContinuation(cells, roles) {
			appendToFreeName(rows[len(rows)-1], cells, roles)
			continue
		}
		if !hasLetters(joined) && !reNumberish.MatchString(joined) {
			continue
		}
		rows = append(rows, cells)
	}
	if !hasTotals && len(in.Totals) > 0 {
		totals = padRow(in.Totals, width)
		for i := range totals {
			totals[i] = cleanCell(totals[i])
		}
		hasTotals = strings.TrimSpace(strings.Join(totals, "")) != ""
	}
	rows = dropDegenerateRows(rows)
	if len(rows) == 0 {
		return nil
	}

	// Пустые графы: подписи нет и во всех строках пусто. Они сдвигают
	// таблицу вправо и в интерфейсе выглядят разрывом.
	keep := make([]int, 0, width)
	for i := 0; i < width; i++ {
		used := strings.TrimSpace(cols[i]) != ""
		if !used {
			for _, r := range rows {
				if strings.TrimSpace(r[i]) != "" {
					used = true
					break
				}
			}
		}
		if !used && hasTotals && strings.TrimSpace(totals[i]) != "" {
			used = true
		}
		if used {
			keep = append(keep, i)
		}
	}
	if len(keep) < 2 {
		return nil
	}
	pick := func(src []string) []string {
		out := make([]string, 0, len(keep))
		for _, i := range keep {
			out = append(out, src[i])
		}
		return out
	}
	out := &domain.FreeTable{Columns: pick(cols), Source: in.Source}
	for _, r := range rows {
		out.Rows = append(out.Rows, pick(r))
	}
	if hasTotals {
		out.Totals = pick(totals)
	}
	out.Roles = rolesFor(out.Columns)
	return out
}

// ScoreFreeTable оценивает разбор от 0 до 1 и возвращает короткую расшифровку
// для журнала. Оценка опирается на внутреннюю арифметику документа, а не на
// то, каким способом таблица собрана.
func ScoreFreeTable(ft *domain.FreeTable) (float64, string) {
	if ft == nil || len(ft.Rows) == 0 || len(ft.Columns) < 2 {
		return 0, "пусто"
	}
	nRows, nCols := len(ft.Rows), len(ft.Columns)

	filled := 0
	for _, r := range ft.Rows {
		for _, c := range r {
			if strings.TrimSpace(c) != "" {
				filled++
			}
		}
	}
	fill := float64(filled) / float64(nRows*nCols)

	score := 0.15
	score += 0.10 * math.Min(1, fill/0.5)

	roles := ft.Roles
	if len(roles) != nCols {
		roles = rolesFor(ft.Columns)
	}
	var hasName, hasAmount, hasQtyPrice bool
	for _, r := range roles {
		switch r {
		case "name":
			hasName = true
		case "amount":
			hasAmount = true
		case "qty", "price", "unit":
			hasQtyPrice = true
		}
	}
	if hasName {
		score += 0.10
	}
	if hasAmount {
		score += 0.10
	}
	if hasQtyPrice {
		score += 0.05
	}

	lines := FreeTableToLines(ft)

	// Наименования: в графе должен стоять товар или услуга, а не реквизит.
	good := 0
	for _, l := range lines {
		n := strings.TrimSpace(l.Name)
		if len([]rune(n)) >= 3 && hasLetters(n) && !reJunkName.MatchString(n) {
			good++
		}
	}
	if len(lines) > 0 {
		score += 0.15 * float64(good) / float64(len(lines))
	}

	// Арифметика строки: количество × цена = сумма.
	mulOK, mulN := 0, 0
	vatOK, vatN := 0, 0
	for _, l := range lines {
		q, ok1 := cellNum(l.Qty)
		p, ok2 := cellNum(l.Price)
		base := l.AmountNoVAT
		if base == "" {
			base = l.Amount
		}
		if a, ok3 := cellNum(base); ok1 && ok2 && ok3 && a > 0 && q > 0 && p > 0 {
			mulN++
			if math.Abs(q*p-a) <= math.Max(0.05, a*0.02) {
				mulOK++
			}
		}
		if l.AmountNoVAT != "" && l.VAT != "" && l.Amount != "" {
			vatN++
			if amountsReconcile(l.AmountNoVAT, l.VAT, l.Amount) {
				vatOK++
			}
		}
	}
	if mulN > 0 {
		score += 0.20 * float64(mulOK) / float64(mulN)
	}
	if vatN > 0 {
		score += 0.10 * float64(vatOK) / float64(vatN)
	}

	// Сумма строк против «Итого» самой таблицы.
	totalsHit := false
	if t := maxMoneyIn(ft.Totals); t > 0 {
		var withVAT, noVAT float64
		for _, l := range lines {
			if v, ok := cellNum(l.Amount); ok {
				withVAT += v
			}
			if v, ok := cellNum(l.AmountNoVAT); ok {
				noVAT += v
			}
		}
		tol := math.Max(0.05, t*0.02)
		if math.Abs(withVAT-t) <= tol || math.Abs(noVAT-t) <= tol {
			score += 0.15
			totalsHit = true
		}
	}

	// Штрафы за характерные развалы разбора.
	seen := map[string]int{}
	for _, r := range ft.Rows {
		seen[strings.ToLower(strings.Join(r, "\x00"))]++
	}
	dups := 0
	for _, n := range seen {
		if n > 1 {
			dups += n - 1
		}
	}
	if dups > 0 {
		score -= 0.20 * float64(dups) / float64(nRows)
	}
	if nCols > 16 {
		score -= 0.05
	}
	if len(lines) == 0 {
		score -= 0.15
	}

	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	diag := fmt.Sprintf("строк=%d граф=%d заполн=%.0f%% имена=%d/%d кол×цена=%d/%d ндс=%d/%d итог=%v",
		nRows, nCols, fill*100, good, len(lines), mulOK, mulN, vatOK, vatN, totalsHit)
	return score, diag
}

// maxMoneyIn — наибольшее число в ряду. В строке «Итого» их обычно три
// (без НДС, НДС, с НДС), и к оплате идёт наибольшее.
func maxMoneyIn(cells []string) float64 {
	best := 0.0
	for _, c := range cells {
		if v, ok := cellNum(c); ok && v > best {
			best = v
		}
	}
	return best
}

// Небольшая надбавка за источник — только чтобы разрешить ничью. На разницу
// в качестве она не влияет: 0.03 против шага в 0.10–0.20 у арифметики.
var tableSourceBonus = map[string]float64{
	"grid":    0.03,
	"columns": 0.02,
	"layout":  0.01,
	"model":   0.00,
}

// PickBestFreeTable чистит всех кандидатов, оценивает и возвращает лучшего
// вместе со строкой для журнала. TABLE_PRIORITY=grid|columns|layout|model
// по-прежнему работает: названный источник получает решающую надбавку.
func PickBestFreeTable(cands []*domain.FreeTable) (*domain.FreeTable, string) {
	forced := strings.ToLower(strings.TrimSpace(os.Getenv("TABLE_PRIORITY")))
	var (
		best      *domain.FreeTable
		bestScore = -1.0
		parts     []string
	)
	for _, c := range cands {
		cl := CleanFreeTable(c)
		if cl == nil {
			continue
		}
		s, why := ScoreFreeTable(cl)
		s += tableSourceBonus[cl.Source]
		if forced != "" && forced == strings.ToLower(cl.Source) {
			s += 0.5
		}
		parts = append(parts, fmt.Sprintf("%s=%.2f [%s]", cl.Source, s, why))
		if s > bestScore {
			bestScore, best = s, cl
		}
	}
	if len(parts) == 0 {
		return nil, "кандидатов нет"
	}
	return best, strings.Join(parts, "; ")
}
