package recognize

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"docflow/internal/domain"
)

// Уверенность, посчитанная по свидетельствам, а не проставленная по источнику.
//
// Раньше число в графе «уверенность» было ярлыком происхождения, переодетым в
// проценты: от модели всегда 85, от правила 90, 70 или 30, из таблицы 80. На
// сотне документов разных значений было ровно пять. Главбух видел 85 и
// проходил поле глазами — а там номер, взятый из графы «Количество». Система
// выглядела уверенной ровно там, где ошиблась.
//
// Здесь уверенность считается: сходятся ли независимые источники значения,
// есть ли значение в самом документе, стоит ли оно в своей зоне листа, бьётся
// ли арифметика документа и подходит ли формат.

// Реквизиты, которые в документе печатаются ВЫШЕ таблицы. Значение такого поля,
// найденное только среди позиций, — почти наверняка ошибка.
var headerScopedFields = map[string]bool{
	"number":           true,
	"date":             true,
	"counterparty":     true,
	"organization":     true,
	"unp":              true,
	"inn":              true,
	"kpp":              true,
	"organization_unp": true,
	"organization_inn": true,
	"contract_number":  true,
	"contract_date":    true,
}

var (
	reConfDate   = regexp.MustCompile(`^\d{2}\.\d{2}\.\d{4}$`)
	reConfDigits = regexp.MustCompile(`^\d+$`)
	reConfMoney  = regexp.MustCompile(`^-?\d+([.,]\d{1,4})?$`)
)

// ConfidenceInput — всё, на что опирается расчёт.
type ConfidenceInput struct {
	Full   string                  // весь распознанный текст
	Zones  Zones                   // шапка / позиции / итоги / подвал
	Rules  map[string]domain.Field // что нашли правила до модели
	Header string                  // номер, найденный по месту на листе
}

// ApplyConfidence пересчитывает уверенность всех полей документа.
func ApplyConfidence(rec *domain.Recognition, in ConfidenceInput) {
	if rec == nil || len(rec.Fields) == 0 {
		return
	}
	full := normForSearch(in.Full)
	head := normForSearch(in.Zones.HeaderText(in.Full))
	body := normForSearch(in.Zones.Body)

	arith := arithmeticAgrees(rec.Fields)
	linesOK := linesSumAgrees(*rec)

	for key, f := range rec.Fields {
		if f.Source == "manual" {
			f.Confidence = 1
			rec.Fields[key] = f
			continue
		}
		// Значения из карточки компании и справочника контрагентов в
		// тексте дословно не стоят («ООО «КофеВенд»» против «Общество с
		// ограниченной ответственностью…»), но проверены по УНП — их
		// уверенность задана при подстановке.
		if f.Source == "company" || f.Source == "directory" {
			continue
		}
		rec.Fields[key] = domain.Field{
			Value:      f.Value,
			Source:     f.Source,
			Confidence: scoreField(key, f, in, full, head, body, arith, linesOK),
		}
	}
}

func scoreField(key string, f domain.Field, in ConfidenceInput, full, head, body string, arith, linesOK int) float64 {
	v := strings.TrimSpace(f.Value)
	if v == "" {
		return 0
	}
	score := 0.5

	// 1. Значение вообще есть в документе. Модель иногда выдаёт правдоподобный
	// номер, которого на листе нет вовсе — так в карточке появлялся УНП
	// плательщика 200020001, которого в документе не было.
	needle := normForSearch(v)
	inDoc := needle != "" && strings.Contains(full, needle)
	switch {
	case strings.HasPrefix(key, "account_"):
		// Счета учёта не извлекаются из текста, а подбираются по типу
		// документа и содержанию строк. Их проверяем планом счетов.
		if AccountsEnabled() && KnownAccount(v) {
			score = 0.75
		} else {
			score = 0.4
		}
	case inDoc:
		score += 0.2
	case derivedMatches(key, f.Value, in):
		// Значение получено приведением к формату: «28 августа 2023 г.» →
		// 28.08.2023, «белорусских рублей» → BYN. Дословно его в тексте нет,
		// но источник в документе есть — это не выдумка.
		score += 0.15
	default:
		score -= 0.3
	}

	// 2. Зона листа. Реквизит шапки, найденный только среди позиций таблицы,
	// — это и есть номер, взятый из графы «Количество».
	if headerScopedFields[key] && in.Zones.OK {
		inHead := needle != "" && strings.Contains(head, needle)
		inBody := needle != "" && body != "" && strings.Contains(body, needle)
		switch {
		case inHead:
			score += 0.15
		case inBody:
			score -= 0.3
		}
	}

	// 3. Согласие независимых источников: правила и модель нашли одно и то же.
	if alt, ok := in.Rules[key]; ok && strings.TrimSpace(alt.Value) != "" {
		if normForSearch(alt.Value) == needle {
			if f.Source != "rule" {
				score += 0.2
			}
		} else {
			score -= 0.1
		}
	}

	// 4. Формат.
	switch formatValid(key, v) {
	case 1:
		score += 0.1
	case -1:
		score -= 0.3
	}

	// 5. Арифметика документа: без НДС + НДС = с НДС.
	switch key {
	case "total", "amount_no_vat", "vat_amount":
		if arith > 0 {
			score += 0.15
		} else if arith < 0 {
			score -= 0.15
		}
	}
	// Сумма позиций сходится с итогом — сильное свидетельство и за итог,
	// и за то, что таблица разобрана верно.
	if key == "total" {
		if linesOK > 0 {
			score += 0.15
		} else if linesOK < 0 {
			score -= 0.1
		}
	}

	// 6. Номер документа, найденный по месту на листе, подтверждает сам себя.
	if key == "number" && in.Header != "" {
		if normForSearch(in.Header) == needle {
			score += 0.2
		} else {
			score -= 0.15
		}
	}

	return clampConf(score)
}

func clampConf(v float64) float64 {
	if v < 0.05 {
		v = 0.05
	}
	if v > 0.98 {
		v = 0.98
	}
	return math.Round(v*100) / 100
}

// formatValid: 1 — формат верный, -1 — заведомо неверный, 0 — не проверяем.
func formatValid(key, v string) int {
	switch key {
	case "date", "contract_date":
		if !reConfDate.MatchString(v) {
			return -1
		}
		y, err := strconv.Atoi(v[6:])
		if err != nil || y < 1995 || y > 2100 {
			return -1
		}
		return 1
	case "unp", "organization_unp":
		if reConfDigits.MatchString(v) && len(v) == 9 {
			return 1
		}
		return -1
	case "inn", "organization_inn":
		if reConfDigits.MatchString(v) && (len(v) == 10 || len(v) == 12) {
			return 1
		}
		return -1
	case "kpp":
		if reConfDigits.MatchString(v) && len(v) == 9 {
			return 1
		}
		return -1
	case "currency":
		switch strings.ToUpper(v) {
		case "BYN", "RUB", "USD", "EUR":
			return 1
		}
		return -1
	case "total", "amount_no_vat", "vat_amount":
		if reConfMoney.MatchString(strings.ReplaceAll(v, " ", "")) {
			return 1
		}
		return -1
	case "number":
		if looksLikeAccountNumber(v) || len([]rune(v)) > 25 {
			return -1
		}
		return 0
	case "counterparty", "organization":
		if len([]rune(v)) < 3 {
			return -1
		}
		return 0
	}
	return 0
}

// arithmeticAgrees: 1 — без НДС + НДС = с НДС, -1 — не сходится, 0 — проверить
// нечем (какого-то из трёх значений нет).
func arithmeticAgrees(fields map[string]domain.Field) int {
	no, ok1 := confMoney(fields, "amount_no_vat")
	vat, ok2 := confMoney(fields, "vat_amount")
	tot, ok3 := confMoney(fields, "total")
	if !ok1 || !ok2 || !ok3 {
		return 0
	}
	if math.Abs(no+vat-tot) <= 0.02 {
		return 1
	}
	return -1
}

// linesSumAgrees: 1 — сумма строк сходится с итогом, -1 — расходится,
// 0 — строк нет или итога нет.
func linesSumAgrees(rec domain.Recognition) int {
	tot, ok := confMoney(rec.Fields, "total")
	if !ok || tot == 0 || len(rec.Lines) == 0 {
		return 0
	}
	sum, n := 0.0, 0
	for _, l := range rec.Lines {
		if v, err := parseMoney(l.Amount); err == nil {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	if math.Abs(sum-tot) <= math.Max(0.02, tot*LinesTolerance) {
		return 1
	}
	return -1
}

func confMoney(fields map[string]domain.Field, key string) (float64, bool) {
	f, ok := fields[key]
	if !ok {
		return 0, false
	}
	v, err := parseMoney(f.Value)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseMoney(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\u00a0", "")
	s = strings.ReplaceAll(s, ",", ".")
	if s == "" {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseFloat(s, 64)
}

// normForSearch приводит значение к виду, в котором его можно искать в тексте:
// без пробелов, кавычек и регистра. Иначе «ОАО "КофеВенд"» не находится в
// тексте, где стоит «ОАО «КофеВенд»».
func normForSearch(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '\t', '\n', '\r', '"', '«', '»', '\'', '`', '\u00a0':
			continue
		case ',':
			b.WriteRune('.')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// derivedMatches — значение не лежит в тексте дословно, но получается из него
// тем же разборщиком, который его и породил.
func derivedMatches(key, value string, in ConfidenceInput) bool {
	text := in.Zones.HeaderText(in.Full)
	switch key {
	case "date", "contract_date":
		return dateFromWords(text) == value
	case "currency":
		return strings.EqualFold(extractCurrency(in.Full), value)
	case "vat_amount", "total":
		return ParseMoneyWords(in.Zones.AmountsText(in.Full)) == value ||
			VATFromWords(in.Zones.AmountsText(in.Full)) == value
	}
	return false
}
