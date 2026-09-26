package recognize

import (
	"testing"

	"docflow/internal/domain"
)

const invoiceWithHeader = `
ООО "Ромашка"
УНП 191234567
Счёт на оплату № 145 от 12.03.2026

№ п/п  Наименование товара  Кол-во  Ед.  Цена  Сумма
1  Бумага офисная А4 500л  10  шт  12,50  125,00
2  Картридж HP CF217A  2  шт  95,00  190,00
3  Ручка шариковая синяя  50  шт  1,20  60,00

Итого: 375,00
В том числе НДС 20%: 62,50
`

func TestExtractLinesWithHeader(t *testing.T) {
	lines := ExtractLines(invoiceWithHeader)
	if len(lines) != 3 {
		t.Fatalf("ожидалось 3 строки, получено %d: %+v", len(lines), lines)
	}

	first := lines[0]
	if first.Name != "Бумага офисная А4 500л" {
		t.Errorf("наименование: %q", first.Name)
	}
	if first.Qty != "10" || first.Unit != "шт" || first.Price != "12.50" || first.Amount != "125.00" {
		t.Errorf("колонки разъехались: %+v", first)
	}
	if lines[2].Amount != "60.00" {
		t.Errorf("последняя строка: %+v", lines[2])
	}
}

func TestExtractLinesStopsAtTotals(t *testing.T) {
	lines := ExtractLines(invoiceWithHeader)
	for _, l := range lines {
		if l.Name == "Итого:" || l.Name == "В том числе НДС 20%:" {
			t.Fatalf("строка итогов попала в табличную часть: %+v", l)
		}
	}
}

func TestExtractLinesGluesThousands(t *testing.T) {
	text := `
Наименование  Кол-во  Ед.  Цена  Сумма
Ноутбук Lenovo V15  2  шт  1 250,00  2 500,00
Итого 2 500,00
`
	lines := ExtractLines(text)
	if len(lines) != 1 {
		t.Fatalf("ожидалась 1 строка, получено %d: %+v", len(lines), lines)
	}
	if lines[0].Price != "1250.00" || lines[0].Amount != "2500.00" {
		t.Errorf("разряды не склеились: %+v", lines[0])
	}
}

func TestExtractLinesNoTable(t *testing.T) {
	text := `
Акт оказанных услуг № 7 от 01.02.2026
Исполнитель оказал, а Заказчик принял услуги по настройке оборудования.
Итого к оплате: 1200,00
`
	if lines := ExtractLines(text); lines != nil {
		t.Errorf("таблицы нет, но что-то разобрано: %+v", lines)
	}
}

func TestExtractLinesPositionalFallback(t *testing.T) {
	// Шапка есть, но колонок в строках меньше, чем объявлено, — работает
	// позиционная эвристика по хвостовым числам.
	text := `
Наименование  Кол-во  Ед.  Цена  Сумма  НДС
Услуги хостинга за март  120,00
Итого 120,00
`
	lines := ExtractLines(text)
	if len(lines) != 1 {
		t.Fatalf("ожидалась 1 строка, получено %d: %+v", len(lines), lines)
	}
	if lines[0].Name != "Услуги хостинга за март" || lines[0].Amount != "120.00" {
		t.Errorf("позиционный разбор: %+v", lines[0])
	}
}

func TestLinesTotalMismatch(t *testing.T) {
	rec := domain.Recognition{
		Fields: map[string]domain.Field{"total": {Value: "375.00"}},
		Lines: []domain.LineItem{
			{Name: "a", Amount: "125.00"},
			{Name: "b", Amount: "190.00"},
			{Name: "c", Amount: "60.00"},
		},
	}
	if LinesTotalMismatch(rec, LinesTolerance) {
		t.Error("сумма строк сходится с итогом, расхождения быть не должно")
	}

	rec.Fields["total"] = domain.Field{Value: "9000.00"}
	if !LinesTotalMismatch(rec, LinesTolerance) {
		t.Error("расхождение с итогом не поймано")
	}
}

func TestLinesTotalMismatchIncompleteLines(t *testing.T) {
	rec := domain.Recognition{
		Fields: map[string]domain.Field{"total": {Value: "100.00"}},
		Lines: []domain.LineItem{
			{Name: "a", Amount: "100.00"},
			{Name: "b"}, // сумма не распозналась
		},
	}
	if !LinesTotalMismatch(rec, LinesTolerance) {
		t.Error("строка без суммы должна считаться неподтверждённой")
	}
}

func TestRequiredFieldsFollowLocale(t *testing.T) {
	t.Cleanup(func() { SetLocale("ru") })

	SetLocale("by")
	if got := RequiredFields("invoice"); !contains(got, "unp") || contains(got, "inn") {
		t.Errorf("для Беларуси ожидался unp, получено %v", got)
	}

	SetLocale("ru")
	if got := RequiredFields("invoice"); !contains(got, "inn") || contains(got, "unp") {
		t.Errorf("для России ожидался inn, получено %v", got)
	}
}

// Промпт модели обязан спрашивать тот же налоговый идентификатор, который
// система считает обязательным, иначе документ навсегда зависнет в needs_input.
func TestExtractPromptMatchesLocale(t *testing.T) {
	t.Cleanup(func() { SetLocale("ru") })

	SetLocale("by")
	prompt := buildExtractPrompt("текст документа", "invoice", "")
	if !containsSubstr(prompt, `"unp"`) {
		t.Error("промпт для Беларуси не спрашивает УНП")
	}
	if containsSubstr(prompt, `"inn"`) {
		t.Error("промпт для Беларуси спрашивает ИНН")
	}
	if !containsSubstr(prompt, `"lines"`) {
		t.Error("промпт не спрашивает табличную часть")
	}
}

func TestNeedsModelOnMissingFields(t *testing.T) {
	t.Cleanup(func() { SetLocale("ru") })
	SetLocale("by")

	// Тип опознан уверенно, но УНП не распознан — модель обязана подключиться.
	rec := domain.Recognition{
		DocType: "invoice",
		Fields: map[string]domain.Field{
			"number": {Value: "145", Confidence: 0.7},
			"date":   {Value: "12.03.2026", Confidence: 0.7},
			"total":  {Value: "375.00", Confidence: 0.7},
		},
		Lines: []domain.LineItem{{Name: "a", Amount: "375.00"}},
	}
	if !needsModel(rec, 1.0) {
		t.Error("не хватает обязательного поля, а модель не вызывается")
	}

	rec.Fields["unp"] = domain.Field{Value: "191234567", Confidence: 0.7}
	if needsModel(rec, 1.0) {
		t.Error("всё распознано — модель дёргать незачем")
	}
}

func TestNeedsModelOnMissingTable(t *testing.T) {
	t.Cleanup(func() { SetLocale("ru") })
	SetLocale("by")

	rec := domain.Recognition{
		DocType: "waybill",
		Fields: map[string]domain.Field{
			"number": {Value: "1", Confidence: 0.7},
			"date":   {Value: "01.01.2026", Confidence: 0.7},
			"unp":    {Value: "191234567", Confidence: 0.7},
		},
	}
	if !needsModel(rec, 1.0) {
		t.Error("накладная без табличной части должна уходить на разбор модели")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
