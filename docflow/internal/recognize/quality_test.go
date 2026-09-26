package recognize

import (
	"strings"
	"testing"

	"docflow/internal/domain"
)

// Счёт-фактура БГУ № 5479: две позиции, ряд номеров граф под шапкой и
// подпись-разделитель «ул. Революционная, 11» между шапкой и позициями.
func bguGood() *domain.FreeTable {
	return &domain.FreeTable{
		Source: "grid",
		Columns: []string{
			"Наименование", "Ед. измер.", "Кол-во", "Тариф",
			"Стоимость всего без НДС, руб.", "Ст. НДС, %",
			"Сумма НДС, руб", "Стоимость всего с учетом НДС, руб",
		},
		Rows: [][]string{
			{"1", "2", "3", "4", "5", "6", "7", "8"},
			{"", "", "", "", "", "", "", "ул. Революционная, 11"},
			{"Возмещение затрат по электроэнергии (по отдельно установленному прибору электрической энергии)", "кВт", "73", "0.41009", "29.94", "20%", "5.99", "35.93"},
			{"Возмещение затрат по электроэнергии (места общего пользования)", "кВт", "1,52", "0,33842", "0,51", "20%", "0,10", "0,61"},
			{"Итого:", "", "", "", "30,45", "", "6,09", "36,54"},
		},
	}
}

// Тот же документ, разобранный вкривь: строки разъехались по графам.
func bguBad() *domain.FreeTable {
	return &domain.FreeTable{
		Source: "columns",
		Columns: []string{
			"Наименование", "Ед. измер.", "Кол-во", "Тариф",
			"Стоимость всего без НДС, руб.", "Ст. НДС, %",
			"Сумма НДС, руб", "Стоимость всего с учетом НДС, руб",
		},
		Rows: [][]string{
			{"Возмещение затрат по", "", "", "", "", "", "", ""},
			{"электроэнергии (по отдельно", "кВт", "0.41009", "29.94", "20", "5.99", "35.93", ""},
			{"установленному прибору", "", "73", "", "", "", "", ""},
			{"Возмещение затрат по", "кВт", "0.33842", "0.51", "20", "0.10", "0.61", ""},
			{"электроэнергии (места", "", "1.52", "", "", "", "", ""},
		},
	}
}

func TestCleanDropsNumberingAndSectionRows(t *testing.T) {
	ft := CleanFreeTable(bguGood())
	if ft == nil {
		t.Fatal("таблица вычищена в ноль")
	}
	if len(ft.Rows) != 2 {
		t.Fatalf("ожидались 2 позиции, получено %d: %v", len(ft.Rows), ft.Rows)
	}
	for _, r := range ft.Rows {
		if isNumberingRow(r) {
			t.Fatalf("ряд номеров граф остался в таблице: %v", r)
		}
		if strings.Contains(strings.Join(r, " "), "Революционная") {
			t.Fatalf("подпись-разделитель осталась позицией: %v", r)
		}
	}
	if len(ft.Totals) == 0 {
		t.Fatal("строка Итого не выделена")
	}
	if got := maxMoneyIn(ft.Totals); got != 36.54 {
		t.Fatalf("итог таблицы %v, ожидалось 36.54", got)
	}
	// Запятые в числах приведены к точке.
	if ft.Rows[1][2] != "1.52" {
		t.Fatalf("количество не нормализовано: %q", ft.Rows[1][2])
	}
}

func TestScorePrefersArithmeticallyConsistentTable(t *testing.T) {
	good, _ := ScoreFreeTable(CleanFreeTable(bguGood()))
	bad, _ := ScoreFreeTable(CleanFreeTable(bguBad()))
	if good <= bad {
		t.Fatalf("развалившийся разбор не должен выигрывать: good=%.2f bad=%.2f", good, bad)
	}
	if good < 0.7 {
		t.Fatalf("верный разбор оценён слишком низко: %.2f", good)
	}
}

func TestPickBestIgnoresSourceOrder(t *testing.T) {
	// Проекция подана первой и с более высоким приоритетом источника — но
	// сходится по арифметике сетка, её и надо взять.
	best, diag := PickBestFreeTable([]*domain.FreeTable{bguBad(), bguGood()})
	if best == nil {
		t.Fatal("кандидат не выбран")
	}
	if best.Source != "grid" {
		t.Fatalf("выбран %q вместо grid; %s", best.Source, diag)
	}
	if len(best.Rows) != 2 {
		t.Fatalf("в выбранной таблице %d строк: %v", len(best.Rows), best.Rows)
	}
}

func TestRepairNumericCellLeavesTextAlone(t *testing.T) {
	cases := map[string]string{
		"З0,45":                       "30.45",
		"1 234,56":                    "1234.56",
		"0.41009":                     "0.41009",
		"20%":                         "20%",
		"кВт":                         "кВт",
		"кв.ч":                        "кв.ч",
		"17.03.20":                    "17.03.20",
		"BY23PJCB3012080588100000933": "BY23PJCB3012080588100000933",
		"Возмещение затрат":           "Возмещение затрат",
	}
	for in, want := range cases {
		if got := repairNumericCell(in); got != want {
			t.Errorf("repairNumericCell(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

const bguText = `СЧЕТ-ФАКТУРА № 5479
17 Ноября 2023 г.
Продавец: Белорусский государственный университет.
Адрес г.Минск, пр.Независимости,4.
УНП продавца 100235722.
Банковские реквизиты: р/с № BY05BLBB36420100235722001002.
в Дирекция ОАО Белинвестбанк по г. Минску и Минской обл.,BLBBBY2X
Покупатель ООО "КофеВенд".
Адрес 220002, г. Минск, ул. Сторожевска, 8, пом. 8.
УНП покупателя (заказчика) 590888079.
К оплате 36.54 (Тридцать шесть белорусских рублей 54 копейки)`

func TestFixFieldsDropsInventedTaxIDAndOrdersParties(t *testing.T) {
	fields := map[string]domain.Field{
		// Модель придумала УНП плательщика — в тексте его нет.
		"organization_unp": {Value: "200020001", Confidence: 0.85, Source: "model"},
		// И перепутала стороны местами.
		"unp":          {Value: "590888079", Confidence: 0.85, Source: "model"},
		"counterparty": {Value: `ООО "КофеВенд", УНП 590888079`, Confidence: 0.85, Source: "model"},
		"organization": {Value: "Белорусский государственный университет. Адрес г.Минск, пр.Независимости,4", Confidence: 0.85, Source: "model"},
	}
	FixFields(fields, bguText)

	if got := fields["unp"].Value; got != "100235722" {
		t.Fatalf("УНП продавца = %q, ожидалось 100235722", got)
	}
	if got := fields["organization_unp"].Value; got != "590888079" {
		t.Fatalf("УНП покупателя = %q, ожидалось 590888079", got)
	}
	if got := fields["counterparty"].Value; strings.Contains(got, "УНП") {
		t.Fatalf("в наименовании контрагента остались реквизиты: %q", got)
	}
	if got := fields["organization"].Value; strings.Contains(strings.ToLower(got), "адрес") {
		t.Fatalf("в наименовании организации остался адрес: %q", got)
	}
}

func TestFixFieldsKeepsGroundedValues(t *testing.T) {
	fields := map[string]domain.Field{
		"unp":   {Value: "100235722", Confidence: 0.85, Source: "model"},
		"total": {Value: "36.54", Confidence: 0.85, Source: "model"},
	}
	FixFields(fields, bguText)
	if fields["unp"].Value != "100235722" {
		t.Fatal("верный УНП отброшен")
	}
	if f := fields["total"]; f.Value != "36.54" || f.Confidence < 0.8 {
		t.Fatalf("верная сумма испорчена: %+v", f)
	}
}

func TestFillTotalsFromTable(t *testing.T) {
	rec := domain.Recognition{
		Fields: map[string]domain.Field{},
		Table:  FreeTableToTable(CleanFreeTable(bguGood())),
	}
	FillTotalsFromTable(&rec)
	if got := rec.Fields["total"].Value; got != "36.54" {
		t.Fatalf("итог из таблицы = %q, ожидалось 36.54", got)
	}
}
