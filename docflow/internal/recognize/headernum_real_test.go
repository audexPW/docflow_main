package recognize

import (
	"testing"
)

// Номер документа на НАСТОЯЩИХ раскладках.
//
// Тест на одной идеальной строке не ловит ошибок раскладки. Здесь
// берутся слова с координатами реальных документов, и сценарий строится ПРАВКОЙ
// этих слов на их же местах: геометрия бланка остаётся настоящей.

type placed struct {
	text   string
	x0, x1 float64
}

// rowOf — слова строки, в которой стоит слово anchor (по пересечению по вертикали).
func rowBounds(ws []wordBox, anchor string) (y0, y1 float64, ok bool) {
	for _, w := range ws {
		if w.Text == anchor {
			return w.Y0, w.Y1, true
		}
	}
	return 0, 0, false
}

func sameRow(w wordBox, y0, y1 float64) bool {
	c := (w.Y0 + w.Y1) / 2
	return c >= y0 && c <= y1
}

// replaceRow убирает строку, где стоит anchor, и кладёт на её место новые слова.
func replaceRow(t *testing.T, ws []wordBox, anchor string, repl ...placed) []wordBox {
	t.Helper()
	y0, y1, ok := rowBounds(ws, anchor)
	if !ok {
		t.Fatalf("в раскладке нет слова %q", anchor)
	}
	out := make([]wordBox, 0, len(ws)+len(repl))
	for _, w := range ws {
		if !sameRow(w, y0, y1) {
			out = append(out, w)
		}
	}
	for _, p := range repl {
		out = append(out, wordBox{Text: p.text, X0: p.x0, X1: p.x1, Y0: y0, Y1: y1})
	}
	return out
}

// appendToRow дописывает слова в строку, где стоит anchor.
func appendToRow(t *testing.T, ws []wordBox, anchor string, add ...placed) []wordBox {
	t.Helper()
	y0, y1, ok := rowBounds(ws, anchor)
	if !ok {
		t.Fatalf("в раскладке нет слова %q", anchor)
	}
	out := append([]wordBox{}, ws...)
	for _, p := range add {
		out = append(out, wordBox{Text: p.text, X0: p.x0, X1: p.x1, Y0: y0, Y1: y1})
	}
	return out
}

func TestRealHeader126(t *testing.T) {
	base := func() []wordBox { return loadWordsTSV(t, "testdata/header-126-words.tsv") }

	t.Run("как есть: номер акта, а не договора", func(t *testing.T) {
		if got := HeaderNumberFromWords(base()); got != "У-000484" {
			t.Errorf("получено %q, ожидалось У-000484 — выиграл номер договора строкой ниже", got)
		}
	})

	t.Run("акт б/н: номер договора чужой", func(t *testing.T) {
		ws := replaceRow(t, base(), "сдачи-приемки",
			placed{"сдачи-приемки", 998, 1290}, placed{"выполненных", 1313, 1560},
			placed{"работ", 1583, 1695}, placed{"б/н", 1718, 1780})
		if got := HeaderNumberFromWords(ws); got != "" {
			t.Errorf("акт без номера получил %q — это номер договора, чужой номер хуже пустого поля", got)
		}
	})

	t.Run("акт без номера вовсе: номер договора чужой", func(t *testing.T) {
		ws := replaceRow(t, base(), "сдачи-приемки",
			placed{"выполненных", 1000, 1250}, placed{"работ", 1270, 1380},
			placed{"за", 1400, 1440}, placed{"сентябрь", 1460, 1640}, placed{"2023", 1660, 1740})
		if got := HeaderNumberFromWords(ws); got != "" {
			t.Errorf("акт без номера получил %q — это номер договора", got)
		}
	})

	t.Run("б/н в строке названия, помещение под ним", func(t *testing.T) {
		ws := appendToRow(t, base(), "AKT",
			placed{"№", 1350, 1370}, placed{"б/н", 1380, 1440},
			placed{"от", 1460, 1500}, placed{"30.09.2023", 1520, 1700})
		ws = replaceRow(t, ws, "сдачи-приемки",
			placed{"Помещение", 1000, 1200}, placed{"№", 1210, 1235}, placed{"5", 1290, 1320})
		if got := HeaderNumberFromWords(ws); got != "" {
			t.Errorf("получено %q — номер помещения под актом б/н", got)
		}
	})

	t.Run("перенос: «по договору» в строке названия, номер под ним", func(t *testing.T) {
		ws := appendToRow(t, base(), "AKT",
			placed{"выполненных", 1350, 1590}, placed{"работ", 1610, 1720},
			placed{"по", 1740, 1790}, placed{"договору", 1810, 1990})
		ws = replaceRow(t, ws, "сдачи-приемки",
			placed{"аренды", 1000, 1150}, placed{"№", 1170, 1195}, placed{"42", 1215, 1260},
			placed{"от", 1280, 1320}, placed{"01.03.2022", 1340, 1520})
		if got := HeaderNumberFromWords(ws); got != "" {
			t.Errorf("получено %q — номер договора с перенесённой строки", got)
		}
	})

	t.Run("перенос: «к» в конце строки названия", func(t *testing.T) {
		ws := appendToRow(t, base(), "AKT",
			placed{"выполненных", 1350, 1590}, placed{"работ", 1610, 1720}, placed{"к", 1740, 1760})
		ws = replaceRow(t, ws, "сдачи-приемки",
			placed{"№", 1000, 1025}, placed{"45", 1045, 1090},
			placed{"от", 1110, 1150}, placed{"01.03.2022", 1170, 1350})
		if got := HeaderNumberFromWords(ws); got != "" {
			t.Errorf("получено %q — номер чужого документа («к № 45»)", got)
		}
	})

	t.Run("латинская c в «cчету»", func(t *testing.T) {
		ws := appendToRow(t, base(), "AKT",
			placed{"выполненных", 1350, 1590}, placed{"работ", 1610, 1720}, placed{"по", 1740, 1780},
			placed{"cчету", 1800, 1920}, placed{"№", 1940, 1965}, placed{"45", 1985, 2030})
		ws = replaceRow(t, ws, "сдачи-приемки", placed{"аренды", 1000, 1150})
		if got := HeaderNumberFromWords(ws); got != "" {
			t.Errorf("получено %q — латинская буква в слове-владельце вернула чужой номер", got)
		}
	})
}

func TestRealHeader218(t *testing.T) {
	base := func() []wordBox { return loadWordsTSV(t, "testdata/header-218-words.tsv") }

	t.Run("как есть", func(t *testing.T) {
		if got := HeaderNumberFromWords(base()); got != "118" {
			t.Errorf("получено %q, ожидалось 118", got)
		}
	})

	// Номер под названием в той же строке, что и банк слева. Ветка «заголовок в
	// две строки» шла раньше проверки по вертикали и брала «№ 527» из банка.
	moved := func(t *testing.T, left ...placed) []wordBox {
		ws := replaceRow(t, base(), `"Минсктранс"`, placed{`"Минсктранс"`, 460, 626})
		return replaceRow(t, ws, `"Троллейбусный`, append(left, placed{"118", 2020, 2072})...)
	}

	t.Run("банк слева на строке номера", func(t *testing.T) {
		ws := moved(t, placed{"Банк:", 664, 740}, placed{"ЦБУ", 755, 810}, placed{"№", 825, 850},
			placed{"527", 865, 915}, placed{"ОАО", 930, 980}, placed{"«Беларусбанк»", 995, 1190})
		if got := HeaderNumberFromWords(ws); got != "118" {
			t.Errorf("получено %q, ожидалось 118 — номер взят из реквизитов банка", got)
		}
	})

	t.Run("адрес слева на строке номера", func(t *testing.T) {
		ws := moved(t, placed{"Адрес:", 664, 760}, placed{"220030", 775, 870},
			placed{"г.", 885, 905}, placed{"Минск", 920, 1010})
		if got := HeaderNumberFromWords(ws); got != "118" {
			t.Errorf("получено %q, ожидалось 118 — номер взят из адреса", got)
		}
	})
}

// Договор: слово «акт» в теле договора не делает его актом.
func TestContractBodyActWord(t *testing.T) {
	ws := []wordBox{
		wb("ДОГОВОР", 900, 100, 80), wb("№", 990, 100, 10), wb("25", 1005, 100, 20),
		wb("г.", 300, 130, 15), wb("Минск", 330, 130, 50), wb("01.03.2023", 1500, 130, 80),
		wb("1.", 300, 160, 15), wb("Предмет", 330, 160, 70), wb("договора", 420, 160, 70),
		wb("Акт", 900, 190, 30), wb("сдачи-приемки", 940, 190, 120), wb("подписывается", 1070, 190, 120), wb("сторонами", 1200, 190, 90),
		wb("5", 905, 220, 10), wb("дней", 920, 220, 40),
	}
	if got := HeaderNumberFromWords(ws); got != "25" {
		t.Errorf("получено %q, ожидалось 25 — номер взят из текста договора", got)
	}
}

// Одинокое «АКТ», под ним адрес: индекс — не номер.
func TestLoneActPostalCode(t *testing.T) {
	ws := []wordBox{
		wb("АКТ", 900, 100, 40),
		wb("220030", 890, 130, 60), wb("г.", 960, 130, 15), wb("Минск", 985, 130, 50), wb("ул.", 1045, 130, 25), wb("Пинская", 1080, 130, 70),
		wb("Исполнитель", 100, 200, 120), wb("ООО", 240, 200, 40),
	}
	if got := HeaderNumberFromWords(ws); got != "" {
		t.Errorf("получено %q — почтовый индекс под названием", got)
	}
}

// «ООО» под названием распознано цифрами — не номер.
func TestZerosFromCompanyForm(t *testing.T) {
	ws := []wordBox{
		wb("АКТ", 900, 100, 40),
		wb("000", 890, 130, 60), wb("\"Ромашка\"", 960, 130, 140),
		wb("Исполнитель", 100, 200, 120), wb("ООО", 240, 200, 40),
	}
	if got := HeaderNumberFromWords(ws); got != "" {
		t.Errorf("получено %q — это «ООО», прочитанное цифрами", got)
	}
}

// Документы, где номер сверен со сканом (14.09): страховка от повтора ошибок
// поиска номера на этих раскладках.
func TestRealHeaderCheckedByScan(t *testing.T) {
	cases := []struct{ file, want, why string }{
		// на бумаге «1333337», распознавание прочитало с лишней тройкой — место номера верное
		{"testdata/header-0001-words.tsv", "13333337", "номер под штрихкодом, один в строке; «000» из «ООО» — не номер"},
		{"testdata/header-0153-words.tsv", "27-09/23", "номер акта с разными разделителями — не дата, и не номер договора ниже"},
		{"testdata/header-0161-words.tsv", "810", "знак «№» в конце строки названия, номер унесён на строку ниже"},
		{"testdata/header-0217-words.tsv", "", "на бумаге «счёт-фактура № б/н»"},
	}
	for _, c := range cases {
		if got := HeaderNumberFromWords(loadWordsTSV(t, c.file)); got != c.want {
			t.Errorf("%s: получено %q, на скане %q — %s", c.file, got, c.want, c.why)
		}
	}
}
