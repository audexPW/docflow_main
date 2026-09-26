package recognize

// Проверка реквизитов после правил и после модели.
//
// Две болезни, которые видно на любом бланке заказчика:
//
//  1. Модель придумывает налоговый номер. На счёте, где УНП плательщика вообще
//     не напечатан, в поле organization_unp приезжало правдоподобное
//     девятизначное число, и документ уходил в 1С уже с ошибкой — заметить её
//     потом некому.
//  2. Стороны меняются местами. Продавец и покупатель напечатаны подряд, и
//     модель берёт контрагентом ту сторону, что ближе к концу текста.
//
// Обе лечатся не промптом, а сверкой с самим текстом: номера, которых в
// документе нет, отбрасываются, а стороны привязываются к подписанным
// «УНП продавца» / «УНП покупателя».

import (
	"regexp"
	"strconv"
	"strings"

	"docflow/internal/domain"
)

var (
	// Подписанные налоговые номера сторон. Скобка в «УНП покупателя
	// (заказчика) 590888079» — обычное дело, поэтому окно до цифр широкое.
	reUNPSeller = regexp.MustCompile(`(?i)УНП\s*(?:продавца|поставщика|исполнителя|арендодателя|грузоотправителя|получателя\s+платежа)[^\d\n]{0,25}(\d{9})`)
	reUNPBuyer  = regexp.MustCompile(`(?i)УНП\s*(?:покупателя|заказчика|плательщика|грузополучателя|арендатора)[^\d\n]{0,25}(\d{9})`)

	// Хвосты, которые заезжают в наименование стороны следом за названием.
	rePartyCut = regexp.MustCompile(`(?i)(,?\s*(адрес|р\s*/\s*с|расч[ёе]тн|в\s+банке|банк[:\s]|дирекция|бик|окпо|унп|инн|тел\.|телефон|код\s+банка|e-?mail)).*$`)
)

// Реквизиты, которые обязаны находиться в тексте документа. Номер документа и
// дату сюда не берём: номер модель нередко чинит («No» → «№»), а дату
// переписывает в другой формат, и сверка по цифрам их зря отбросит.
var groundedIDKeys = []string{"unp", "inn", "kpp", "organization_unp"}
var groundedMoneyKeys = []string{"total", "vat_amount", "amount_no_vat"}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// groundedInText — цифры значения действительно встречаются в распознанном
// тексте подряд, с поправкой на разделители («36.54», «36 54», «36,54»).
func groundedInText(val, text string) bool {
	d := digitsOf(val)
	if len(d) < 4 {
		return true // слишком коротко, чтобы отличить совпадение от случайности
	}
	var b strings.Builder
	for i, r := range d {
		if i > 0 {
			b.WriteString(`[\s.,\-']{0,3}`)
		}
		b.WriteRune(r)
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return true
	}
	return re.MatchString(text)
}

// cleanPartyName отрезает от наименования стороны реквизиты и банк.
func cleanPartyName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = rePartyCut.ReplaceAllString(s, "")
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ",;:-"))
	if len([]rune(s)) < 3 {
		return ""
	}
	return s
}

// FixFields приводит набор реквизитов в порядок по тексту документа.
// Вызывается последним шагом, поэтому чинит и правила, и ответ модели.
func FixFields(fields map[string]domain.Field, ocrText string) {
	if len(fields) == 0 || strings.TrimSpace(ocrText) == "" {
		return
	}

	// 1. Выдуманные номера — вон. Проверка дешёвая и односторонняя: то, чего
	// в тексте нет, в документе появиться не могло.
	for _, k := range groundedIDKeys {
		if f, ok := fields[k]; ok && f.Value != "" && !groundedInText(f.Value, ocrText) {
			delete(fields, k)
		}
	}
	// Суммы не удаляем: модель иногда справедливо чинит цифру, которую OCR
	// прочёл буквой. Но уверенность роняем, чтобы поле попало на проверку.
	for _, k := range groundedMoneyKeys {
		if f, ok := fields[k]; ok && f.Value != "" && !groundedInText(f.Value, ocrText) {
			if f.Confidence > 0.45 {
				f.Confidence = 0.45
			}
			fields[k] = f
		}
	}

	// 2. Стороны — по подписанным номерам, а не по порядку строк.
	if m := reUNPSeller.FindStringSubmatch(ocrText); m != nil {
		fields["unp"] = domain.Field{Value: m[1], Confidence: 0.9, Source: "rule"}
	}
	if m := reUNPBuyer.FindStringSubmatch(ocrText); m != nil {
		fields["organization_unp"] = domain.Field{Value: m[1], Confidence: 0.9, Source: "rule"}
	}
	// Одна и та же сторона не может быть и продавцом, и покупателем.
	if a, ok := fields["unp"]; ok {
		if b, ok2 := fields["organization_unp"]; ok2 && a.Value == b.Value {
			if b.Confidence < a.Confidence {
				delete(fields, "organization_unp")
			} else {
				delete(fields, "unp")
			}
		}
	}

	// 3. Наименования сторон без банковских хвостов.
	for _, k := range []string{"counterparty", "organization"} {
		f, ok := fields[k]
		if !ok || f.Value == "" {
			continue
		}
		v := cleanPartyName(f.Value)
		if v == "" {
			delete(fields, k)
			continue
		}
		if v != f.Value {
			f.Value = v
			fields[k] = f
		}
	}
	// Контрагент и организация не могут совпадать: так бывает, когда модель
	// взяла одну и ту же строку дважды.
	if a, ok := fields["counterparty"]; ok {
		if b, ok2 := fields["organization"]; ok2 && strings.EqualFold(a.Value, b.Value) {
			delete(fields, "organization")
		}
	}
}

// FillTotalsFromTable добирает итог документа из строки «Итого» таблицы, когда
// в шапке и подвале его не нашли. Значение в таблице напечатано цифрами и
// проверено арифметикой строк — оно надёжнее догадки по всему тексту.
func FillTotalsFromTable(rec *domain.Recognition) {
	if rec == nil || rec.Table == nil || rec.Table.RawCells == nil {
		return
	}
	totals := rec.Table.RawCells.Totals
	if len(totals) == 0 {
		return
	}
	t := maxMoneyIn(totals)
	if t <= 0 {
		return
	}
	if rec.Fields == nil {
		rec.Fields = map[string]domain.Field{}
	}
	cur, ok := rec.Fields["total"]
	if ok && cur.Confidence >= ruleConfidence {
		if v, okv := cellNum(cur.Value); okv && v > 0 {
			return
		}
	}
	rec.Fields["total"] = domain.Field{
		Value:      strconv.FormatFloat(t, 'f', 2, 64),
		Confidence: 0.8,
		Source:     "table",
	}
}
