package recognize

import (
	"strings"
	"testing"
)

// wb — короткий конструктор слова: текст, левый край, верх, ширина.
func wb(text string, x, y, w float64) wordBox {
	return wordBox{Text: text, X0: x, Y0: y, X1: x + w, Y1: y + 10}
}

// Счёт № 112 ЗАО «Суперпрод»: восемь граф, шапка в две строки
// («Ставка» / «НДС %», «Сумма» / «с НДС»), две позиции и «Итого».
func invoice112() []wordBox {
	return []wordBox{
		wb("№", 650, 100, 8),
		wb("Наименование", 700, 100, 90),
		wb("товара", 795, 100, 45),
		wb("Ед.", 890, 100, 18),
		wb("изм.", 910, 100, 20),
		wb("Кол-во", 935, 100, 40),
		wb("Цена", 985, 100, 30),
		wb("Сумма", 1045, 100, 38),
		wb("Ставка", 1100, 100, 38),
		wb("НДС", 1160, 100, 26),
		wb("Сумма", 1215, 100, 38),
		wb("НДС", 1100, 112, 26),
		wb("%", 1128, 112, 8),
		wb("с", 1215, 112, 6),
		wb("НДС", 1226, 112, 26),
		wb("1", 652, 132, 6),
		wb("Энергопотребление", 700, 132, 130),
		wb("кв.ч", 890, 132, 24),
		wb("16.74", 935, 132, 32),
		wb("0.41009", 985, 132, 44),
		wb("6,86", 1050, 132, 28),
		wb("20", 1105, 132, 14),
		wb("1,37", 1160, 132, 26),
		wb("8,23", 1220, 132, 28),
		wb("2", 652, 148, 6),
		wb("Энергопотребление", 700, 148, 130),
		wb("кв.ч", 890, 148, 24),
		wb("58", 942, 148, 16),
		wb("0.41009", 985, 148, 44),
		wb("23,79", 1044, 148, 34),
		wb("20", 1105, 148, 14),
		wb("4,76", 1160, 148, 26),
		wb("28,55", 1214, 148, 34),
		wb("Итого:", 830, 166, 40),
		wb("30,65", 1044, 166, 34),
		wb("6,13", 1160, 166, 26),
		wb("36,78", 1214, 166, 34),
	}
}

func TestTableFromWordsKeepsOriginalColumns(t *testing.T) {
	ft := TableFromWords(invoice112())
	if ft == nil {
		t.Fatal("таблица не собралась")
	}
	if len(ft.Columns) != 9 {
		t.Fatalf("граф должно быть 9, получено %d: %q", len(ft.Columns), ft.Columns)
	}
	if len(ft.Rows) != 2 {
		t.Fatalf("позиций должно быть 2, получено %d: %q", len(ft.Rows), ft.Rows)
	}

	// Многострочная шапка склеена, а не потеряна.
	joined := strings.Join(ft.Columns, "|")
	for _, want := range []string{"Ставка НДС %", "Сумма с НДС", "Ед. изм."} {
		if !strings.Contains(joined, want) {
			t.Errorf("в шапке нет %q; шапка: %q", want, ft.Columns)
		}
	}

	// Главное: значения стоят в своих графах, а не сдвинуты влево.
	row := ft.Rows[0]
	want := []string{"1", "Энергопотребление", "кв.ч", "16.74", "0.41009", "6,86", "20", "1,37", "8,23"}
	for i, w := range want {
		if row[i] != w {
			t.Errorf("графа %d (%q): ожидалось %q, получено %q", i, ft.Columns[i], w, row[i])
		}
	}

	if len(ft.Totals) == 0 || ft.Totals[8] != "36,78" {
		t.Errorf("строка Итого разобрана неверно: %q", ft.Totals)
	}
}

func TestVATAmountWinsOverVATRate(t *testing.T) {
	ft := TableFromWords(invoice112())
	if ft == nil {
		t.Fatal("таблица не собралась")
	}
	lines := FreeTableToLines(ft)
	if len(lines) != 2 {
		t.Fatalf("строк для 1С: %d", len(lines))
	}
	if lines[0].Amount != "8.23" {
		t.Errorf("сумма строки: ожидалось 8.23, получено %q (роли: %v)", lines[0].Amount, ft.Roles)
	}
	if lines[0].VAT == "20" {
		t.Errorf("в НДС уехала ставка вместо суммы налога: %q", lines[0].VAT)
	}
	if lines[0].Qty != "16.74" {
		t.Errorf("количество: %q", lines[0].Qty)
	}
	if lines[0].Unit != "кв.ч" {
		t.Errorf("единица измерения: %q", lines[0].Unit)
	}
}

func TestStripMath(t *testing.T) {
	got := StripMath(`<math>N_{\overline{2}}</math>`)
	if !strings.Contains(got, "№") {
		t.Errorf("формула не свёрнута в №: %q", got)
	}
	if strings.Contains(got, "overline") || strings.Contains(got, "<math") {
		t.Errorf("остатки разметки: %q", got)
	}
}

func TestNumberingRowDropped(t *testing.T) {
	if !isNumberingRow([]string{"1", "2", "3", "4", "5"}) {
		t.Error("ряд номеров граф не распознан")
	}
	if isNumberingRow([]string{"1", "Энергопотребление", "16.74"}) {
		t.Error("позиция принята за ряд номеров граф")
	}
}

func TestGridRepeatRowDropped(t *testing.T) {
	// Скриншот с «Актом пользования имуществом»: детектор растащил текст
	// из-под таблицы по всем графам ряда, и он попал в позиции восемь раз.
	same := "Всего к оплате с НДС: Шестнадцать рублей 22 копейки"
	if !gridRowIsRepeat([]string{"", same, same, same}) {
		t.Error("ряд-повтор не распознан")
	}
	if gridRowIsRepeat([]string{"1", "Аренда", "13.52"}) {
		t.Error("настоящая позиция принята за повтор")
	}
}

// Реальный случай со стенда: surya отдала левые подписи шапки одним широким
// боксом («NΩ Наименование товара Ед. изм. Кол-во Цена»), а под таблицей идёт
// «Всего отпущено на сумму». И то и другое тянется через зазоры между узкими
// графами. Пока границы считались по всей полосе, из девяти граф оставалось
// четыре — ровно то, что было видно в логе стенда.
func invoice112WideHeader() []wordBox {
	return []wordBox{
		wb("№ Наименование товара Ед. изм. Кол-во Цена", 650, 100, 380),
		wb("Сумма", 1045, 100, 38),
		wb("Ставка", 1100, 100, 38),
		wb("НДС", 1160, 100, 26),
		wb("Сумма", 1215, 100, 38),
		wb("НДС", 1100, 112, 26),
		wb("%", 1128, 112, 8),
		wb("с", 1215, 112, 6),
		wb("НДС", 1226, 112, 26),
		wb("1", 652, 132, 6),
		wb("Энергопотребление", 700, 132, 130),
		wb("кв.ч", 890, 132, 24),
		wb("16.74", 935, 132, 32),
		wb("0.41009", 985, 132, 44),
		wb("6,86", 1050, 132, 28),
		wb("20", 1105, 132, 14),
		wb("1,37", 1160, 132, 26),
		wb("8,23", 1220, 132, 28),
		wb("2", 652, 148, 6),
		wb("Энергопотребление", 700, 148, 130),
		wb("кв.ч", 890, 148, 24),
		wb("58", 942, 148, 16),
		wb("0.41009", 985, 148, 44),
		wb("23,79", 1044, 148, 34),
		wb("20", 1105, 148, 14),
		wb("4,76", 1160, 148, 26),
		wb("28,55", 1214, 148, 34),
		wb("Итого:", 830, 166, 40),
		wb("30,65", 1044, 166, 34),
		wb("6,13", 1160, 166, 26),
		wb("36,78", 1214, 166, 34),
	}
}

func TestWideHeaderDoesNotMergeColumns(t *testing.T) {
	ft := TableFromWords(invoice112WideHeader())
	if ft == nil {
		t.Fatal("таблица не собралась")
	}
	if len(ft.Columns) != 9 {
		t.Fatalf("граф должно быть 9, получено %d: %q", len(ft.Columns), ft.Columns)
	}
	row := ft.Rows[0]
	want := []string{"1", "Энергопотребление", "кв.ч", "16.74", "0.41009", "6,86", "20", "1,37", "8,23"}
	for i, w := range want {
		if row[i] != w {
			t.Errorf("графа %d: ожидалось %q, получено %q", i, w, row[i])
		}
	}
	lines := FreeTableToLines(ft)
	if len(lines) == 0 || lines[0].Price != "0.41009" {
		t.Errorf("цена уехала: %+v", lines)
	}
}

// Случай со стенда: над таблицей идут строки самого документа — «Дополнение:
// Договор аренды№49 от17.03.20г коммунальные услуги за октябрь 2023г» и
// «руб.». Поиск шапки сверху вниз затягивал их в заголовки, они резались по
// графам, и в интерфейсе выходило «2023г Кол-во» и «руб. Сумма с НДС».
func invoice112WithTextAbove() []wordBox {
	out := []wordBox{
		wb("Дополнение:", 180, 60, 80),
		wb("Договор", 265, 60, 55),
		wb("аренды№49", 325, 60, 75),
		wb("от17.03.20г", 405, 60, 78),
		wb("коммунальные", 488, 60, 95),
		wb("услуги", 588, 60, 45),
		wb("за", 638, 60, 15),
		wb("октябрь", 658, 60, 55),
		wb("2023г", 718, 60, 38),
		wb("руб.", 1240, 80, 26),
	}
	return append(out, invoice112()...)
}

func TestTextAboveTableStaysOutOfHeader(t *testing.T) {
	ft := TableFromWords(invoice112WithTextAbove())
	if ft == nil {
		t.Fatal("таблица не собралась")
	}
	joined := strings.Join(ft.Columns, " ")
	for _, bad := range []string{"Дополнение", "аренды", "2023г", "руб."} {
		if strings.Contains(joined, bad) {
			t.Errorf("в шапку затянуло текст документа %q; шапка: %q", bad, ft.Columns)
		}
	}
	// И при этом настоящие подписи граф остались на месте: отбраковка
	// лишних строк не должна выносить шапку целиком.
	for _, want := range []string{"Наименование", "Сумма с НДС", "Ставка НДС %"} {
		if !strings.Contains(joined, want) {
			t.Errorf("из шапки пропало %q; шапка: %q", want, ft.Columns)
		}
	}
	empty := 0
	for _, c := range ft.Columns {
		if strings.TrimSpace(c) == "" {
			empty++
		}
	}
	if empty > 1 {
		t.Errorf("граф без названия: %d из %d — %q", empty, len(ft.Columns), ft.Columns)
	}
	if len(ft.Rows) != 2 {
		t.Fatalf("позиций должно быть 2, получено %d: %q", len(ft.Rows), ft.Rows)
	}
	lines := FreeTableToLines(ft)
	if lines[0].Price != "0.41009" || lines[0].Amount != "8.23" {
		t.Errorf("значения уехали: %+v", lines[0])
	}
}

// Подпись «Ставка НДС %» в бланке стоит в две строки, и нижняя её половина
// укладывается в одну графу. Фильтр шапки отбраковывал такие строки заодно с
// «руб.» над таблицей — заголовок графы со ставкой оставался пустым, хотя
// значения 20 стояли на месте.
func invoice112NarrowSubHeader() []wordBox {
	return []wordBox{
		wb("руб.", 1240, 80, 26), // над шапкой — в заголовки идти не должно
		wb("NΩ", 650, 100, 14),   // так OCR читает знак номера
		wb("Наименование", 700, 100, 90),
		wb("товара", 795, 100, 45),
		wb("Ед.", 890, 100, 18),
		wb("изм.", 910, 100, 20),
		wb("Кол-во", 935, 100, 40),
		wb("Цена", 985, 100, 30),
		wb("Сумма", 1045, 100, 38),
		wb("Ставка", 1100, 100, 38),
		wb("НДС", 1160, 100, 26),
		wb("Сумма с НДС", 1215, 100, 60),
		wb("НДС", 1100, 112, 26), // обрывок подписи: одна графа
		wb("%", 1128, 112, 8),
		wb("1", 652, 132, 6),
		wb("Энергопотребление", 700, 132, 130),
		wb("кв.ч", 890, 132, 24),
		wb("16.74", 935, 132, 32),
		wb("0.41009", 985, 132, 44),
		wb("6,86", 1050, 132, 28),
		wb("20", 1105, 132, 14),
		wb("1,37", 1160, 132, 26),
		wb("8,23", 1220, 132, 28),
		wb("2", 652, 148, 6),
		wb("Энергопотребление", 700, 148, 130),
		wb("кв.ч", 890, 148, 24),
		wb("58", 942, 148, 16),
		wb("0.41009", 985, 148, 44),
		wb("23,79", 1044, 148, 34),
		wb("20", 1105, 148, 14),
		wb("4,76", 1160, 148, 26),
		wb("28,55", 1214, 148, 34),
	}
}

func TestNarrowSubHeaderKeptAndNoSignNormalized(t *testing.T) {
	ft := TableFromWords(invoice112NarrowSubHeader())
	if ft == nil {
		t.Fatal("таблица не собралась")
	}
	joined := strings.Join(ft.Columns, " | ")
	if !strings.Contains(joined, "Ставка НДС %") {
		t.Errorf("обрывок подписи потерян; шапка: %q", ft.Columns)
	}
	if strings.Contains(joined, "руб.") {
		t.Errorf("«руб.» над таблицей затянуло в шапку: %q", ft.Columns)
	}
	if !strings.Contains(joined, "№") || strings.Contains(joined, "NΩ") {
		t.Errorf("знак номера не нормализован: %q", ft.Columns)
	}
	for _, c := range ft.Columns {
		if strings.TrimSpace(c) == "" {
			t.Errorf("графа без названия: %q", ft.Columns)
			break
		}
	}
}

func TestNoSignNormalization(t *testing.T) {
	for _, in := range []string{"NΩ", "No", "N°", "Ne", "№"} {
		if got := normalizeNoSign(in); got != "№" {
			t.Errorf("%q → %q, ожидалось №", in, got)
		}
	}
	// Внутри обычного текста трогать нельзя.
	if got := normalizeNoSign("Ноябрь"); got != "Ноябрь" {
		t.Errorf("испорчено слово: %q", got)
	}
}
