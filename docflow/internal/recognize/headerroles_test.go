package recognize

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"docflow/internal/domain"
)

// Подписи граф — дословно как их отдал OCR на бланках заказчика.
func TestColumnRoleRealHeaders(t *testing.T) {
	cases := map[string]string{
		// разорванные переносом
		"Коли- чество": "qty",
		"Количеств":    "qty",
		"изме- рения":  "unit",
		"измере":       "unit",
		"мость":        "amount",
		// «с НДС» и «с учётом НДС» — итог по строке, а не налог
		"с НДС": "amount",
		"всего с учетом НДС, руб":  "amount",
		"Всего с НДС, BYN":         "amount",
		"Стоимость НДС, руб":       "amount",
		"Стоимость всего с учетом": "amount",
		// слова переставлены или OCR спутал буквы
		"НДС, руб. всего без":         "amount",
		"Стоимось прод. без НДС,руб.": "amount",
		"нлс. Сумма руб.":             "vat",
		// то, что было верным, верным и осталось
		"Сумма НДС, BYN":      "vat",
		"Сумма НДС":           "vat",
		"НДС":                 "vat",
		"Ставка НДС, %":       "vat_rate",
		"Цена, BYN":           "price",
		"Тариф":               "price",
		"Кол-во":              "qty",
		"Ед. изм.":            "unit",
		"Стоимость, BYN":      "amount",
		"Сумма б/НДС":         "vat",
		"Наименование товара": "name",
		"№ п/п":               "index",
		"Примечание":          "other",
		"показание счётчика":  "other",
		"с учётом мест общего пользования": "other",
	}
	for title, want := range cases {
		if got := columnRole(title); got != want {
			t.Errorf("%q: роль %q, ожидалась %q", title, got, want)
		}
	}
}

func TestColumnRoleSwitchOff(t *testing.T) {
	t.Setenv("TABLE_HEADER_FIX", "false")
	if got := columnRole("с НДС"); got != "vat" {
		t.Fatalf("TABLE_HEADER_FIX=false должен давать прежнюю роль, получено %q", got)
	}
}

// IMG_0141 (рынок): вторая строка шапки попала в данные, у денежных граф
// подписей нет.
func TestSecondHeaderRowBecomesTitles(t *testing.T) {
	cols := []string{"", "Наименование работ (услуг)", "Единица", "Количеств", "", "Ставка", "", ""}
	ft := CleanFreeTable(freeTableForTest(cols, [][]string{
		{"", "", "измерения", "", "Сумма", "НДС", "Сумма НДС", "Всего с НДС"},
		{"", "Вывоз ТКО место №118", "M2", "1.5", "0.73", "20", "0.15", "0.88"},
		{"", "Электроэнергия место №118", "КВТ.Ч", "80", "32.59", "20", "6.52", "39.11"},
	}))
	lines := FreeTableToLines(ft)
	if len(lines) != 2 {
		t.Fatalf("строка шапки осталась позицией: %v", ft.Rows)
	}
	want := []domain.LineItem{
		{Name: "Вывоз ТКО место №118", Amount: "0.88", AmountNoVAT: "0.73", VAT: "0.15"},
		{Name: "Электроэнергия место №118", Amount: "39.11", AmountNoVAT: "32.59", VAT: "6.52"},
	}
	for i, w := range want {
		l := lines[i]
		if l.Name != w.Name || l.Amount != w.Amount || l.AmountNoVAT != w.AmountNoVAT || l.VAT != w.VAT {
			t.Errorf("позиция %d: %+v, ожидалось %+v", i+1, l, w)
		}
	}
}

// Шапка разобрана криво, но цифры сходятся: роли ставятся по арифметике.
func TestRolesByArithmeticWhenHeaderIsGarbled(t *testing.T) {
	cols := []string{"товара (продукции, услуги)", "руб.", "", "ндс, %", "", "ндс, руб.", "", "с руб.", "бланк"}
	ft := CleanFreeTable(freeTableForTest(cols, [][]string{
		{"Арендная плата за сентябрь 2023 г.", "", "25.35", "", "20", "", "5.07", "", "30.42"},
	}))
	lines := FreeTableToLines(ft)
	if len(lines) != 1 || lines[0].Amount != "30.42" || lines[0].AmountNoVAT != "25.35" || lines[0].VAT != "5.07" {
		t.Fatalf("суммы разложены неверно: %+v (роли %q)", lines, ft.Roles)
	}
}

// Верные подписи арифметика не трогает, даже если в соседней графе стоит
// число, случайно дающее сумму.
func TestRolesByArithmeticKeepsGoodHeader(t *testing.T) {
	cols := []string{"Наименование", "Кол-во", "Цена", "Стоимость", "Ставка НДС, %", "Сумма НДС", "Всего с НДС"}
	ft := CleanFreeTable(freeTableForTest(cols, [][]string{
		{"Услуга А", "2", "5.00", "10.00", "20%", "2.00", "12.00"},
		{"Услуга Б", "1", "8.00", "8.00", "20%", "1.60", "9.60"},
	}))
	want := []string{"name", "qty", "price", "amount", "vat_rate", "vat", "amount"}
	for i, r := range want {
		if ft.Roles[i] != r {
			t.Fatalf("роли %q, ожидались %q", ft.Roles, want)
		}
	}
}

// IMG_0214: наименования стоят во второй графе, первая — номера строк без
// подписи. Раньше первая позиция терялась (у неё номер пустой).
func TestNameColumnByContent(t *testing.T) {
	cols := []string{"", "таможенных операций в качестве таможенного представителя", "", "(руб.коп.)", "(руб.коп.)", "НДС", "(руб.коп.)", ""}
	ft := CleanFreeTable(freeTableForTest(cols, [][]string{
		{"", "Предоставление СД на таможню", "компл", "17.42", "17.42", "20%", "3.48", "20.90"},
		{"2", "Составление СД", "код", "12.83", "12.83", "20%", "2.57", "15.40"},
	}))
	sum, n := linesSum(t, ft)
	if n != 2 || sum != 36.30 {
		t.Fatalf("позиций %d, сумма %v; ожидалось 2 и 36.30: %v роли %q", n, sum, ft.Rows, ft.Roles)
	}
}

// IMG_0210: «23,54 Итого» — слово не в начале строки, итог уезжал в позиции
// и удваивал документ.
func TestTotalsWordInsideRow(t *testing.T) {
	cols := []string{"", "всего без НДС, руб.", "%", "руб.", "всего с учетом НДС, руб."}
	ft := CleanFreeTable(freeTableForTest(cols, [][]string{
		{"Аренда (поликлиника)", "8,45", "20", "1,69", "10,14"},
		{"Аренда (терапевтический корпус)", "15,09", "20", "3,02", "18,11"},
		{"", "23,54 Итого", "", "4,71", "28,25"},
	}))
	if sum, n := linesSum(t, ft); n != 2 || sum != 28.25 {
		t.Fatalf("итог остался позицией: позиций %d, сумма %v, строки %v", n, sum, ft.Rows)
	}
}

// IMG_0080: OCR потерял одну позицию. Сумма обрывка не должна заменять верную
// сумму без НДС, найденную в тексте.
func TestAmountNoVATNotFromUnconfirmedLines(t *testing.T) {
	rec := domain.Recognition{
		Fields: map[string]domain.Field{
			"total":      {Value: "68.44", Source: "model", Confidence: 0.9},
			"vat_amount": {Value: "9.02", Source: "rule", Confidence: 0.9},
		},
		Lines: []domain.LineItem{{Name: "Возмещение", Amount: "54.11", AmountNoVAT: "45.09", VAT: "9.02"}},
	}
	FillAmounts(&rec, "")
	if got := rec.Fields["amount_no_vat"].Value; got != "59.42" {
		t.Fatalf("сумма без НДС %q, ожидалось 59.42 (итог − НДС)", got)
	}
}

// Настоящие таблицы из трассировок: кандидаты разбора сохранены как есть
// (testdata/tables/cand-*.json, файл 08-table-candidates.json прогона).
// Проверяется, что шапка разобрана так, что каждая позиция получает свою
// сумму, суммы не уехали в соседние строки и графы, а их итог — итог
// документа.
func TestRealTablesStayAligned(t *testing.T) {
	cases := []struct {
		file    string
		lines   int
		total   float64
		amounts []string // суммы с НДС по порядку; пусто — не сверяем
	}{
		{"0144", 6, 136.37, []string{"0.04", "51.23", "26.83", "0.19", "0.50", "57.58"}},
		{"0171", 3, 43.33, []string{"41.24", "1.40", "0.69"}},
		{"0141-teplo", 2, 8.69, []string{"1.74", "6.95"}},
		{"0141-rynok", 7, 47.74, []string{"0.02", "0.88", "1.98", "39.11", "2.00", "2.44", "1.31"}},
		{"0143", 2, 25.91, []string{"0.54", "25.37"}},
		{"0140", 1, 0.42, []string{"0.42"}},
		{"0001", 12, 1155.38, nil},
		{"0066", 1, 30.42, []string{"30.42"}},
		{"0209", 1, 30.42, []string{"30.42"}},
		{"0210", 2, 28.25, []string{"10.14", "18.11"}},
		{"0214", 2, 36.30, []string{"20.90", "15.40"}},
		{"0208", 7, 46.90, nil},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			best, diag := PickBestFreeTable(loadTraceCandidates(t, "testdata/tables/cand-"+c.file+".json"))
			if best == nil {
				t.Fatal("таблица не выбрана")
			}
			lines := FreeTableToLines(best)
			sum := 0.0
			var got []string
			for i, l := range lines {
				v, err := parseMoney(l.Amount)
				if err != nil {
					t.Fatalf("позиция %d без суммы: %+v\n%s", i+1, l, diag)
				}
				if l.AmountNoVAT != "" && l.VAT != "" && !amountsReconcile(l.AmountNoVAT, l.VAT, l.Amount) {
					t.Errorf("позиция %d: без НДС %s + НДС %s ≠ %s — данные уехали в чужую графу", i+1, l.AmountNoVAT, l.VAT, l.Amount)
				}
				if strings.TrimSpace(l.Name) == "" {
					t.Errorf("позиция %d без наименования: %+v", i+1, l)
				}
				sum += v
				got = append(got, l.Amount)
			}
			if len(lines) != c.lines || math.Abs(sum-c.total) > 0.005 {
				t.Fatalf("позиций %d, сумма %.2f; ожидалось %d и %.2f\nсуммы %v\nроли %q\n%s", len(lines), sum, c.lines, c.total, got, best.Roles, diag)
			}
			if c.amounts != nil && strings.Join(got, " ") != strings.Join(c.amounts, " ") {
				t.Fatalf("суммы по позициям %v, ожидалось %v — строки перепутаны", got, c.amounts)
			}
		})
	}
}

func loadTraceCandidates(t *testing.T, path string) []*domain.FreeTable {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Cands []traceTableDump `json:"кандидаты"`
	}
	if err := json.Unmarshal(raw, &dump); err != nil {
		t.Fatal(err)
	}
	out := make([]*domain.FreeTable, 0, len(dump.Cands))
	for _, c := range dump.Cands {
		out = append(out, &domain.FreeTable{Columns: c.Колонки, Rows: c.Строки, Totals: c.Итого, Source: c.Источник})
	}
	return out
}
