package recognize

import "testing"

// Таблица из текста с восстановленной раскладкой должна доезжать колонками, а
// не превращаться в набор строк: именно из-за этого в 1С уезжала «каша».
const layoutInvoice = `Счёт на оплату № 145 от 12.03.2026
Поставщик: ООО "Ромашка", УНП 191234567
№ | Наименование товара | Кол-во | Ед. | Цена | Сумма
1 | Бумага офисная А4 | 10 | шт | 12.50 | 125.00
2 | Картридж HP CF217A | 2 | шт | 95.00 | 190.00
Итого: | | | | | 315.00
Руководитель | | Иванов И.И.`

func TestParseTableKeepsColumns(t *testing.T) {
	table := ParseTable(layoutInvoice)
	if table == nil {
		t.Fatal("таблица не распознана")
	}
	if len(table.Rows) != 2 {
		t.Fatalf("строк должно быть 2, получено %d: %+v", len(table.Rows), table.Rows)
	}

	lines := TableToLines(table)
	if len(lines) != 2 {
		t.Fatalf("позиции не собрались: %+v", lines)
	}
	if lines[0].Name != "Бумага офисная А4" {
		t.Errorf("наименование: %q", lines[0].Name)
	}
	if lines[0].Qty != "10" || lines[0].Price != "12.50" || lines[0].Amount != "125.00" {
		t.Errorf("колонки разъехались: %+v", lines[0])
	}
	if len(table.TotalsRow) == 0 {
		t.Error("строка «Итого» должна быть отделена от позиций")
	}
	// Подписи внизу документа — не позиции таблицы.
	for _, l := range lines {
		if l.Name == "Руководитель" {
			t.Error("подпись попала в табличную часть")
		}
	}
}

// Перенос длинного наименования на следующую строку — часть той же позиции.
func TestParseTableJoinsWrappedName(t *testing.T) {
	text := `Наименование | Кол-во | Цена | Сумма
Услуги по техническому | 1 | 240.00 | 240.00
обслуживанию оборудования`

	table := ParseTable(text)
	if table == nil || len(table.Rows) != 1 {
		t.Fatalf("ожидалась одна позиция, получено %+v", table)
	}
	lines := TableToLines(table)
	if lines[0].Name != "Услуги по техническому обслуживанию оборудования" {
		t.Errorf("перенос не приклеился: %q", lines[0].Name)
	}
}

// Раскладки нет (старый движок) — разбор не должен падать, работает запасной
// построчный путь в RecognizeDocument.
func TestRecognizeDocumentWithoutLayout(t *testing.T) {
	rec := RecognizeDocument("Счёт на оплату № 5 от 01.02.2026\nИтого к оплате: 1200,00", "")
	if rec.Fields["number"].Value != "5" {
		t.Errorf("номер: %q", rec.Fields["number"].Value)
	}
}
