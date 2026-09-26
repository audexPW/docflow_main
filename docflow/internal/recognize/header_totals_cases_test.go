package recognize

import (
	"strings"
	"testing"
)

// Номер документа и строка итога: пограничные раскладки. Сценарии
// построены на реальных документах и правке их слов на их же местах.

// replaceLeftOf убирает слова строки anchor левее xmax и кладёт на их место новые.
func replaceLeftOf(t *testing.T, ws []wordBox, anchor string, xmax float64, repl ...placed) []wordBox {
	t.Helper()
	y0, y1, ok := rowBounds(ws, anchor)
	if !ok {
		t.Fatalf("в раскладке нет слова %q", anchor)
	}
	out := make([]wordBox, 0, len(ws)+len(repl))
	for _, w := range ws {
		if sameRow(w, y0, y1) && w.X0 < xmax {
			continue
		}
		out = append(out, w)
	}
	for _, p := range repl {
		out = append(out, wordBox{Text: p.text, X0: p.x0, X1: p.x1, Y0: y0, Y1: y1})
	}
	return out
}

// lineOf раскладывает слова строки подряд по горизонтали.
func lineOf(y, x float64, words ...string) []wordBox {
	var out []wordBox
	for _, w := range words {
		width := float64(len([]rune(w))) * 12
		out = append(out, wb(w, x, y, width))
		x += width + 10
	}
	return out
}

func withSupplier(ws []wordBox) []wordBox {
	return append(ws, lineOf(140, 300, "Исполнитель:", "ООО", "Ромашка", "г.", "Минск")...)
}

// Номер строго под названием берётся без проверок владельца, места и даты.
func TestHeaderVerticalPathChecksOwner(t *testing.T) {
	w126 := func() []wordBox { return loadWordsTSV(t, "testdata/header-126-words.tsv") }

	t.Run("«по договору» в строке названия, номер договора в полосе названия", func(t *testing.T) {
		ws := appendToRow(t, w126(), "AKT", placed{"выполненных", 1350, 1480}, placed{"работ", 1490, 1560},
			placed{"по", 1570, 1600}, placed{"договору", 1610, 1720})
		ws = replaceRow(t, ws, "сдачи-приемки", placed{"аренды", 1100, 1200}, placed{"№", 1250, 1280},
			placed{"42", 1295, 1330}, placed{"от", 1340, 1370}, placed{"01.03.2022", 1380, 1500})
		if got := HeaderNumberFromWords(ws); got != "" {
			t.Errorf("получено %q — номер договора с перенесённой строки", got)
		}
	})

	t.Run("«Помещение № 5» в полосе названия", func(t *testing.T) {
		ws := appendToRow(t, w126(), "AKT", placed{"от", 1350, 1380}, placed{"30.09.2023", 1390, 1500})
		ws = replaceRow(t, ws, "сдачи-приемки", placed{"Помещение", 1100, 1250}, placed{"№", 1260, 1285},
			placed{"5", 1300, 1320})
		if got := HeaderNumberFromWords(ws); got != "" {
			t.Errorf("получено %q — номер помещения", got)
		}
	})

	t.Run("IMG_0216: «пом. 8» из адреса под названием", func(t *testing.T) {
		if got := HeaderNumberFromWords(loadWordsTSV(t, "testdata/header-0216-words.tsv")); got == "8" {
			t.Errorf("получено %q — номер помещения из адреса", got)
		}
	})
}

// Строка со словом «Договор» слева на высоте номера обрывала поиск.
func TestHeaderContractWordLeftOfNumber(t *testing.T) {
	cases := map[string][]placed{
		"Договор № 5/20 от …":    {{"Договор", 460, 560}, {"№", 570, 590}, {"5/20", 600, 660}, {"от", 670, 700}, {"01.11.2020", 710, 900}},
		"Договор 5/20 от …":      {{"Договор", 460, 560}, {"5/20", 600, 660}, {"от", 670, 700}, {"01.11.2020", 710, 900}},
		"Основание: договор № 7": {{"Основание:", 460, 560}, {"договор", 570, 660}, {"№", 670, 690}, {"7", 700, 720}},
	}
	for name, left := range cases {
		ws := replaceLeftOf(t, loadWordsTSV(t, "testdata/header-218-words.tsv"), `"Минсктранс"`, 1000, left...)
		if got := HeaderNumberFromWords(ws); got != "118" {
			t.Errorf("%s: получено %q, ожидалось 118 — строка с договором оборвала поиск номера", name, got)
		}
	}
}

// «АКТ № б/н» — документ без номера: число в полосе названия ниже (сумма) номером
// не становится. Стоп по «б/н» держит именно это, остальные проверки число пропускают.
func TestHeaderOwnNoNumberStopsSearch(t *testing.T) {
	ws := appendToRow(t, loadWordsTSV(t, "testdata/header-126-words.tsv"), "AKT",
		placed{"№", 1350, 1370}, placed{"б/н", 1380, 1440}, placed{"от", 1460, 1500}, placed{"30.09.2023", 1520, 1700})
	ws = replaceRow(t, ws, "сдачи-приемки", placed{"Сумма:", 1000, 1100}, placed{"300", 1290, 1330})
	if got := HeaderNumberFromWords(ws); got != "" {
		t.Errorf("получено %q — у акта «№ б/н» номера нет", got)
	}
}

// Под названием строка с другим названием, и номер ТОГО документа стоит в полосе
// нашего названия: «Договор № 77» под «СЧЕТ-ФАКТУРА». Номер договора не наш.
func TestHeaderOtherTitleNumberInOurBand(t *testing.T) {
	ws := replaceLeftOf(t, loadWordsTSV(t, "testdata/header-218-words.tsv"), `"Минсктранс"`, 2100,
		placed{"Договор", 1700, 1850}, placed{"№", 1900, 1925}, placed{"77", 2010, 2050})
	if got := HeaderNumberFromWords(ws); got != "" {
		t.Errorf("получено %q — номер договора из строки под названием", got)
	}
}

// «б/н» чужого документа после собственного номера стирал свой номер.
func TestHeaderForeignNoNumberAfterOwnNumber(t *testing.T) {
	cases := map[string]struct {
		ws   []wordBox
		want string
	}{
		"АКТ № 15 … к ТТН б/н":           {withSupplier(lineOf(100, 300, "АКТ", "№", "15", "от", "01.09.2023", "к", "ТТН", "б/н")), "15"},
		"Счет-фактура № 12 … к акту б/н": {withSupplier(lineOf(100, 300, "Счет-фактура", "№", "12", "от", "01.09.2023", "к", "акту", "б/н")), "12"},
		"АКТ № 15 … по договору б/н":     {withSupplier(lineOf(100, 300, "АКТ", "№", "15", "от", "01.09.2023", "по", "договору", "б/н")), "15"},
		"IMG_0209 + «Договор б/н»":       {appendToRow(t, loadWordsTSV(t, "testdata/header-0209-words.tsv"), "№371", placed{"Договор", 1900, 2000}, placed{"б/н", 2010, 2050}), "371"},
		"IMG_0209 + «ТТН б/н»":           {appendToRow(t, loadWordsTSV(t, "testdata/header-0209-words.tsv"), "№371", placed{"ТТН", 1900, 1950}, placed{"б/н", 1960, 2000}), "371"},
		"собственный б/н без номера":     {withSupplier(lineOf(100, 300, "АКТ", "б/н", "от", "01.09.2023", "Договор", "№", "42")), ""},
	}
	for name, c := range cases {
		if got := HeaderNumberFromWords(c.ws); got != c.want {
			t.Errorf("%s: получено %q, ожидалось %q", name, got, c.want)
		}
	}
}

// Название с предлогом («по ТТН №», «к акту №») — чужой документ.
func TestHeaderTitleWithPrepositionIsForeign(t *testing.T) {
	cases := map[string]struct {
		ws   []wordBox
		want string
	}{
		"акт по ТТН № 1234567":     {withSupplier(lineOf(100, 300, "АКТ", "приемки", "товара", "по", "ТТН", "№", "1234567", "от", "01.02.2023")), ""},
		"акт к акту № 45":          {withSupplier(lineOf(100, 300, "Акт", "выполненных", "работ", "к", "акту", "№", "45", "от", "01.02.2023")), ""},
		"свой номер раньше ссылки": {withSupplier(lineOf(100, 300, "АКТ", "№", "15", "от", "01.09.2023", "к", "ТТН", "№", "1234567")), "15"},
	}
	for name, c := range cases {
		if got := HeaderNumberFromWords(c.ws); got != c.want {
			t.Errorf("%s: получено %q, ожидалось %q", name, got, c.want)
		}
	}
}

// Знак «№» в конце строки названия, ниже дата словами — день не номер.
func TestHeaderDanglingSignSkipsDateDay(t *testing.T) {
	ws := replaceRow(t, loadWordsTSV(t, "testdata/header-0161-words.tsv"), "810",
		placed{"от", 1660, 1690}, placed{"15", 1700, 1730}, placed{"сентября", 1740, 1860},
		placed{"2023", 1870, 1930}, placed{"г.", 1940, 1960})
	if got := HeaderNumberFromWords(ws); got != "" {
		t.Errorf("получено %q — день из даты «от 15 сентября»", got)
	}
}

// Изуродованное «ИТОГО»/«ВСЕГО» не из пяти букв, со знаком или латиницей.
func totalsTable(last string) []wordBox {
	hdr := []wordBox{wb("Наименование", 700, 100, 84), wb("Сумма", 1045, 100, 35), wb("НДС", 1160, 100, 21), wb("Всего", 1215, 100, 35)}
	row := func(y float64, name, a, b, c string) []wordBox {
		return []wordBox{wb(name, 700, y, float64(len([]rune(name)))*7), wb(a, 1040, y, 40), wb(b, 1158, y, 34), wb(c, 1212, y, 40)}
	}
	out := append(hdr, row(132, "Вода", "10,00", "2,00", "12,00")...)
	out = append(out, row(148, "Стоки", "20,65", "4,13", "24,78")...)
	return append(out, row(164, last, "30,65", "6,13", "36,78")...)
}

func TestGarbledTotalWordVariants(t *testing.T) {
	for _, w := range []string{"ИТ0ГО", "ИТГО", "ИТОГ0О", "ИТ0Г", "|ИТ0ГО", "||ИТ0ГО", "ИТ0ГО:36,78", "ИТ0ГО:10,00", "Bcero", "BCEГО", "ВСЕГ0", "ВСЕО"} {
		ft := TableFromWords(totalsTable(w))
		if ft == nil {
			t.Fatalf("%s: таблица не собрана", w)
		}
		if len(ft.Rows) != 2 || !strings.Contains(strings.Join(ft.Totals, " "), "36,78") {
			t.Errorf("%s: итог остался позицией (строк %d, итог %q)", w, len(ft.Rows), ft.Totals)
		}
	}
	// настоящие короткие наименования с совпавшей суммой остаются позициями
	for _, w := range []string{"Пилот", "Отвод", "Вход"} {
		ft := TableFromWords(totalsTable(w))
		if ft == nil || len(ft.Rows) != 3 {
			n := 0
			if ft != nil {
				n = len(ft.Rows)
			}
			t.Errorf("%s: позиция съедена как итог (строк %d)", w, n)
		}
	}
}
