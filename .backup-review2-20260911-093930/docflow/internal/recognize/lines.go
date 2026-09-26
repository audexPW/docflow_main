package recognize

import (
	"regexp"
	"strconv"
	"strings"

	"docflow/internal/domain"
)

// Разбор табличной части (номенклатуры) из плоского OCR-текста.
//
// OCR отдаёт документ построчно, без сетки таблицы, поэтому колонки
// восстанавливаются в два прохода:
//
//  1. по строке-шапке («Наименование | Кол-во | Ед | Цена | Сумма | НДС»)
//     определяется порядок колонок, и значения строк раскладываются по нему —
//     это основной и самый надёжный путь;
//  2. если шапку разобрать не удалось или в строке другое число значений —
//     работает позиционная эвристика по хвостовым числам.
//
// Результат заведомо приблизительный: строки помечаются как требующие
// проверки через LinesTotalMismatch (сверка суммы строк с итогом документа),
// и при расхождении документ уходит оператору, а не в 1С автоматом.

type colKind int

const (
	colUnknown colKind = iota
	colName
	colQty
	colUnit
	colPrice
	colAmount
	colVAT
	colSkip // «№ п/п», «код» и прочие служебные колонки
)

var (
	reRowNum       = regexp.MustCompile(`^\d{1,3}[.)]?$`)
	reNumeric      = regexp.MustCompile(`^-?\d+(?:[.,]\d+)?%?$`)
	reIntOnly      = regexp.MustCompile(`^\d+$`)
	reThousandTail = regexp.MustCompile(`^\d{3}(?:[.,]\d{1,3})?$`)
	reTableStop    = regexp.MustCompile(`(?i)^\s*(итого|всего|сумма к оплате|всего к оплате|всего наименований|в том числе ндс|ндс итого|руководител|главный бухгалтер|отпустил|получил|принял|м\.?\s?п\.?|подпис)`)
	reLetters      = regexp.MustCompile(`\p{L}`)
	knownUnitsSet  = map[string]bool{
		"шт": true, "шт.": true, "штук": true, "ед": true, "ед.": true,
		"кг": true, "г": true, "гр": true, "т": true, "л": true, "мл": true,
		"м": true, "м2": true, "м3": true, "м.п.": true, "пог.м": true,
		"уп": true, "уп.": true, "упак": true, "упак.": true, "пач": true,
		"компл": true, "компл.": true, "набор": true, "пар": true,
		"час": true, "ч": true, "мес": true, "усл": true, "услуга": true,
		"рул": true, "лист": true, "km": true, "pcs": true,
	}
)

// ExtractLines пытается собрать табличную часть документа. Возвращает nil,
// если таблица не обнаружена — это нормальная ситуация (чек, договор, акт без
// расшифровки), а не ошибка.
func ExtractLines(ocrText string) []domain.LineItem {
	rows := strings.Split(ocrText, "\n")

	headerIdx, cols := findHeader(rows)
	if headerIdx < 0 {
		return nil
	}

	var out []domain.LineItem
	for i := headerIdx + 1; i < len(rows); i++ {
		raw := strings.TrimSpace(rows[i])
		if raw == "" {
			continue
		}
		if reTableStop.MatchString(raw) {
			break
		}
		if item, ok := parseRow(raw, cols); ok {
			out = append(out, item)
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// findHeader ищет строку-шапку таблицы и раскладывает её на колонки.
// Шапкой считается строка, в которой опознано хотя бы два разных типа колонок
// и присутствует колонка наименования либо суммы.
func findHeader(rows []string) (int, []colKind) {
	for i, row := range rows {
		cols := headerColumns(row)
		if len(cols) < 2 {
			continue
		}
		kinds := map[colKind]bool{}
		for _, c := range cols {
			if c != colUnknown && c != colSkip {
				kinds[c] = true
			}
		}
		if len(kinds) < 2 {
			continue
		}
		if kinds[colName] || kinds[colAmount] {
			return i, cols
		}
	}
	return -1, nil
}

// headerColumns классифицирует слова строки-шапки и схлопывает подряд идущие
// слова одного типа («Кол» + «во» → одна колонка количества).
func headerColumns(row string) []colKind {
	words := strings.Fields(strings.ToLower(row))
	if len(words) == 0 {
		return nil
	}

	var cols []colKind
	for _, w := range words {
		k := classifyHeaderWord(w)
		if k == colUnknown {
			// Незнакомое слово внутри шапки — скорее продолжение предыдущей
			// колонки («товаров (работ, услуг)»), чем новая колонка.
			if len(cols) == 0 {
				return nil
			}
			continue
		}
		if len(cols) > 0 && cols[len(cols)-1] == k {
			continue
		}
		cols = append(cols, k)
	}
	return cols
}

func classifyHeaderWord(w string) colKind {
	w = strings.Trim(w, ".,:;()[]|")
	switch {
	case w == "":
		return colUnknown
	case strings.HasPrefix(w, "наимен"), w == "товар", w == "товары", w == "услуга",
		w == "услуги", strings.HasPrefix(w, "описан"), strings.HasPrefix(w, "номенклат"):
		return colName
	case w == "кол", w == "к-во", w == "кол-во", strings.HasPrefix(w, "количест"), w == "qty":
		return colQty
	case w == "ед", w == "изм", w == "ед.изм", strings.HasPrefix(w, "единиц"):
		return colUnit
	case strings.HasPrefix(w, "цен"), w == "price", w == "тариф":
		return colPrice
	case strings.HasPrefix(w, "сумм"), w == "стоимость", w == "amount", w == "всего":
		return colAmount
	case w == "ндс", w == "vat", w == "налог":
		return colVAT
	case w == "№", w == "n", w == "no", strings.HasPrefix(w, "п/п"), w == "код",
		strings.HasPrefix(w, "артикул"), w == "sku":
		return colSkip
	}
	return colUnknown
}

// parseRow раскладывает строку таблицы: сначала выделяет наименование
// (текстовый префикс), затем распределяет оставшиеся значения по колонкам.
func parseRow(raw string, cols []colKind) (domain.LineItem, bool) {
	tokens := strings.Fields(raw)
	if len(tokens) < 2 {
		return domain.LineItem{}, false
	}
	// Ведущий порядковый номер строки к данным не относится.
	if reRowNum.MatchString(tokens[0]) && len(tokens) > 2 {
		tokens = tokens[1:]
	}
	// «1 250,00» может быть как одним числом с разделителем разрядов, так и
	// двумя значениями (количество 1, цена 250,00). Разрешает эту неоднозначность
	// шапка: пробуем оба варианта разбиения и берём тот, у которого число
	// значений совпадает с числом колонок.
	merged := mergeThousands(tokens)
	for _, variant := range [][]string{merged, tokens} {
		name, values := splitNameValues(variant)
		if name == "" || len(values) == 0 {
			continue
		}
		item := domain.LineItem{Name: name}
		if assignByHeader(&item, values, cols) {
			return item, true
		}
	}

	// Шапка не помогла — позиционный разбор по склеенному варианту.
	name, values := splitNameValues(merged)
	if name == "" || len(values) == 0 {
		return domain.LineItem{}, false
	}
	item := domain.LineItem{Name: name}
	if !assignPositional(&item, values) || (item.Amount == "" && item.Price == "") {
		return domain.LineItem{}, false
	}
	return item, true
}

// splitNameValues отделяет наименование (текстовый префикс) от хвоста значений.
// Числа внутри названия («Кабель 3х2.5») остаются в имени, пока после них ещё
// идут буквенные слова.
func splitNameValues(tokens []string) (string, []string) {
	split := len(tokens)
	for i := len(tokens) - 1; i >= 0; i-- {
		t := tokens[i]
		if reNumeric.MatchString(t) || isUnit(t) {
			split = i
			continue
		}
		break
	}

	name := strings.TrimSpace(strings.Join(tokens[:split], " "))
	if !hasLetters(name) || len([]rune(name)) < 2 {
		return "", nil
	}
	return name, tokens[split:]
}

// assignByHeader раскладывает значения по колонкам, объявленным в шапке.
// Работает только при точном совпадении количества значений и колонок —
// иначе разъехавшаяся строка молча получит неверные цену и сумму.
func assignByHeader(item *domain.LineItem, values []string, cols []colKind) bool {
	value := make([]colKind, 0, len(cols))
	for _, c := range cols {
		if c == colName {
			continue
		}
		value = append(value, c)
	}
	if len(value) == 0 || len(value) != len(values) {
		return false
	}

	for i, kind := range value {
		v := values[i]
		switch kind {
		case colQty:
			item.Qty = normalizeNumber(v)
		case colUnit:
			item.Unit = v
		case colPrice:
			item.Price = normalizeNumber(v)
		case colAmount:
			item.Amount = normalizeNumber(v)
		case colVAT:
			item.VAT = normalizeNumber(v)
		}
	}
	return item.Amount != "" || item.Price != ""
}

// assignPositional — запасной разбор по хвостовым числам, когда шапка не
// помогла. Опирается на самый частый порядок: … количество, цена, сумма.
func assignPositional(item *domain.LineItem, values []string) bool {
	var nums []string
	for _, v := range values {
		if isUnit(v) && item.Unit == "" {
			item.Unit = v
			continue
		}
		if reNumeric.MatchString(v) {
			nums = append(nums, normalizeNumber(v))
		}
	}

	switch len(nums) {
	case 0:
		return false
	case 1:
		item.Amount = nums[0]
	case 2:
		item.Price, item.Amount = nums[0], nums[1]
	default:
		item.Qty = nums[0]
		item.Price = nums[len(nums)-2]
		item.Amount = nums[len(nums)-1]
	}
	return true
}

// mergeThousands склеивает число, разбитое пробелами по разрядам. Работает
// только над чисто числовыми токенами, поэтому «А4 500л» не пострадает.
func mergeThousands(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		for reIntOnly.MatchString(t) && i+1 < len(tokens) && reThousandTail.MatchString(tokens[i+1]) {
			t += tokens[i+1]
			i++
		}
		out = append(out, t)
	}
	return out
}

func isUnit(t string) bool {
	return knownUnitsSet[strings.ToLower(strings.Trim(t, ".,"))]
}

func hasLetters(s string) bool { return reLetters.MatchString(s) }

func normalizeNumber(s string) string {
	s = strings.TrimSuffix(s, "%")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, ",", ".")
	return s
}

// LinesTotalMismatch сверяет сумму строк табличной части с итогом документа.
// Возвращает true, если и то и другое известно, но расходится больше чем на
// tolerance (доля от итога). Это дешёвый бухгалтерский контроль: расхождение
// почти всегда означает, что таблица разобрана неверно, и такой документ
// нельзя отправлять в 1С без оператора.
func LinesTotalMismatch(rec domain.Recognition, tolerance float64) bool {
	if len(rec.Lines) == 0 {
		return false
	}
	totalField, ok := rec.Fields["total"]
	if !ok {
		return false
	}
	total, err := strconv.ParseFloat(normalizeNumber(totalField.Value), 64)
	if err != nil || total <= 0 {
		return false
	}

	var sum float64
	var counted int
	for _, l := range rec.Lines {
		if l.Amount == "" {
			continue
		}
		v, err := strconv.ParseFloat(l.Amount, 64)
		if err != nil {
			continue
		}
		sum += v
		counted++
	}
	if counted == 0 || counted != len(rec.Lines) {
		// Часть строк без суммы — сверять нечего, но и доверять нельзя.
		return true
	}

	diff := sum - total
	if diff < 0 {
		diff = -diff
	}
	return diff > total*tolerance
}
