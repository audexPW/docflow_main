package recognize

import (
	"strings"
	"testing"

	"docflow/internal/domain"
)

// Строка итога — сценарии разбора 14.09.
//
// Роли граф берутся из rolesFor, как в рабочем пути (роль «number» он не выдаёт
// никогда), а сквозные сценарии идут через TableFromWords на раскладке счёта № 112.

// invoice112With — счёт № 112, где слово «Итого:» заменено и снизу добавлены строки.
func invoice112With(totalWord string, extra ...wordBox) []wordBox {
	out := make([]wordBox, 0, 40)
	for _, w := range invoice112() {
		if w.Text == "Итого:" {
			w.Text = totalWord
		}
		out = append(out, w)
	}
	return append(out, extra...)
}

func rowsContain(rows [][]string, needle string) bool {
	for _, r := range rows {
		if strings.Contains(strings.Join(r, " "), needle) {
			return true
		}
	}
	return false
}

// «ИТ0ГО» в таблице, ниже «Всего к оплате с НДС: 36,78». Итог по слову уже найден,
// но изуродованная строка итога не должна остаться позицией — сумма удвоится.
func TestGarbledTotalWithWordTotalBelow(t *testing.T) {
	ft := TableFromWords(invoice112With("ИТ0ГО:",
		wb("Всего", 700, 184, 40), wb("к", 745, 184, 8), wb("оплате", 758, 184, 45),
		wb("с", 808, 184, 6), wb("НДС:", 820, 184, 30), wb("36,78", 1214, 184, 34)))
	if ft == nil {
		t.Fatal("таблица не собрана")
	}
	if len(ft.Rows) != 2 || rowsContain(ft.Rows, "36,78") {
		t.Errorf("строка итога осталась в позициях, сумма удвоится: %q", ft.Rows)
	}
}

// Сумма прописью без цифр под изуродованным итогом приклеивалась к нему как
// перенос наименования, и итог превращался в «позицию с настоящим названием».
func TestSpelledAmountNotGluedToGarbledTotal(t *testing.T) {
	ft := TableFromWords(invoice112With("ИТ0ГО:",
		wb("Тридцать", 700, 184, 60), wb("шесть", 765, 184, 40), wb("рублей", 810, 184, 18)))
	if ft == nil {
		t.Fatal("таблица не собрана")
	}
	if len(ft.Rows) != 2 || rowsContain(ft.Rows, "36,78") {
		t.Errorf("итог остался позицией: %q", ft.Rows)
	}
	if !strings.Contains(strings.Join(ft.Totals, " "), "36,78") {
		t.Errorf("итог не забран: %q", ft.Totals)
	}
}

// Одинаковые позиции без графы «№»: итог сверяется по ПОЛНОМУ набору строк, до
// схлопывания повторов, иначе 100 + 50 ≠ 250 и итог остаётся позицией.
func TestGarbledTotalBeforeDedup(t *testing.T) {
	ws := []wordBox{
		wb("Наименование", 700, 100, 90), wb("Сумма", 1045, 100, 38), wb("НДС", 1160, 100, 26), wb("Всего", 1215, 100, 38),
		wb("Аренда", 700, 132, 60), wb("100,00", 1040, 132, 42), wb("20,00", 1158, 132, 34), wb("120,00", 1212, 132, 42),
		wb("Аренда", 700, 148, 60), wb("100,00", 1040, 148, 42), wb("20,00", 1158, 148, 34), wb("120,00", 1212, 148, 42),
		wb("Охрана", 700, 164, 60), wb("50,00", 1044, 164, 34), wb("10,00", 1158, 164, 34), wb("60,00", 1214, 164, 34),
		wb("ИТ0ГО", 700, 180, 45), wb("250,00", 1040, 180, 42), wb("50,00", 1158, 180, 34), wb("300,00", 1212, 180, 42),
	}
	ft := TableFromWords(ws)
	if ft == nil {
		t.Fatal("таблица не собрана")
	}
	if rowsContain(ft.Rows, "300,00") {
		t.Errorf("итог остался позицией: %q", ft.Rows)
	}
}

// Графа «№ п/п» получает от rolesFor роль index — и не должна считаться деньгами:
// 3 = 1 + 2 давало бесплатное совпадение, и третья позиция уходила в итог.
func TestIndexColumnIsNotMoney(t *testing.T) {
	cols := []string{"№", "Наименование", "Кол-во", "Цена", "Сумма"}
	ft := &domain.FreeTable{
		Columns: cols,
		Roles:   rolesFor(cols),
		Rows: [][]string{
			{"1", "Газ", "1", "10,00", "10,00"},
			{"2", "Свет", "1", "17,50", "17,50"},
			{"3", "", "2", "13,75", "27,50"},
		},
	}
	takeGarbledTotalsRow(ft)
	if len(ft.Rows) != 3 {
		t.Errorf("позиция съедена как итог (роли %v): %q", ft.Roles, ft.Rows)
	}
}

// Однословное наименование — настоящее, даже короткое.
func TestOneWordItemIsRealName(t *testing.T) {
	cols := []string{"Наименование", "Сумма", "НДС", "Всего"}
	ft := &domain.FreeTable{
		Columns: cols,
		Roles:   rolesFor(cols),
		Rows: [][]string{
			{"Вода", "40,00", "8,00", "48,00"},
			{"Стоки", "25,00", "5,00", "30,00"},
			{"Теплоэнергия", "65,00", "13,00", "78,00"},
		},
	}
	takeGarbledTotalsRow(ft)
	if len(ft.Rows) != 3 {
		t.Errorf("позиция «Теплоэнергия» съедена как итог: %q", ft.Rows)
	}
}

// Изуродованный итог с хвостом «по счёту» — не наименование.
func TestGarbledTotalWithTail(t *testing.T) {
	cols := []string{"Наименование", "Сумма", "НДС", "Всего"}
	ft := &domain.FreeTable{
		Columns: cols,
		Roles:   rolesFor(cols),
		Rows: [][]string{
			{"Вода", "10,00", "2,00", "12,00"},
			{"Стоки", "20,65", "4,13", "24,78"},
			{"ИТ0Г0 по счёту", "30,65", "6,13", "36,78"},
		},
	}
	takeGarbledTotalsRow(ft)
	if len(ft.Rows) != 2 {
		t.Errorf("итог с хвостом остался позицией: %q", ft.Rows)
	}
}

// Итог по слову уже есть и с изуродованной строкой НЕ совпадает — строка
// остаётся позицией: данные не пропадают молча.
func TestOccupiedTotalsMismatchKeepsRow(t *testing.T) {
	cols := []string{"Наименование", "Сумма", "НДС", "Всего"}
	ft := &domain.FreeTable{
		Columns: cols,
		Roles:   rolesFor(cols),
		Totals:  []string{"Итого", "99,00", "19,80", "118,80"},
		Rows: [][]string{
			{"Вода", "10,00", "2,00", "12,00"},
			{"Стоки", "20,65", "4,13", "24,78"},
			{"ИТ0ГО", "30,65", "6,13", "36,78"},
		},
	}
	takeGarbledTotalsRow(ft)
	if len(ft.Rows) != 3 {
		t.Errorf("строка исчезла при несовпадающем итоге: %q", ft.Rows)
	}
}
