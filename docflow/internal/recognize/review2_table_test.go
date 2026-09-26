package recognize

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// loadWordsTSV читает выгрузку 05-ocr-words.tsv из трассировки.
func loadWordsTSV(t *testing.T, p string) []wordBox {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer f.Close()
	var out []wordBox
	sc := bufio.NewScanner(f)
	for first := true; sc.Scan(); first = false {
		if first {
			continue
		}
		parts := strings.SplitN(sc.Text(), "\t", 5)
		if len(parts) < 5 {
			continue
		}
		num := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
		out = append(out, wordBox{Text: parts[4], X0: num(parts[0]), Y0: num(parts[1]), X1: num(parts[2]), Y1: num(parts[3])})
	}
	return out
}

func findRow(ft [][]string, needle string) []string {
	for _, r := range ft {
		if strings.Contains(strings.Join(r, " "), needle) {
			return r
		}
	}
	return nil
}

// Счёт-фактура на восемь граф, где блок значений сползает вниз от графы к
// графе. Раньше выходило 34 строки каши: значения прилипали к следующей
// позиции, два числа склеивались в одно, наименования резались.
func TestDenseTableDriftAligned(t *testing.T) {
	ft := TableFromWords(loadWordsTSV(t, "testdata/dense-invoice-words.tsv"))
	if ft == nil {
		t.Fatal("таблица не собрана")
	}
	if len(ft.Rows) > 20 {
		t.Fatalf("строк %d — позиции по-прежнему рвутся надвое", len(ft.Rows))
	}
	cases := map[string][]string{
		"Электроэнергия МОП":             {"21.9504", "4.3901", "26.3405"},
		"Пожарный надзор (сигнализация)": {"0.24590", "1.7277", "2.0732"},
		"Вывоз и утилизация мусора":      {"1.3116", "2.6232"},
		"Текущий ремонт зданий":          {"0.1719", "0.3438"},
	}
	for name, want := range cases {
		row := findRow(ft.Rows, name)
		if row == nil {
			t.Fatalf("нет позиции %q целиком", name)
		}
		joined := strings.Join(row, " | ")
		for _, w := range want {
			found := false
			for _, c := range row {
				if strings.TrimSpace(c) == w {
					found = true
				}
			}
			if !found {
				t.Fatalf("%q: нет отдельной ячейки %s в строке %s", name, w, joined)
			}
		}
	}
	if len(ft.Totals) == 0 || !strings.Contains(strings.ToLower(strings.Join(ft.Totals, " ")), "итого") {
		t.Fatalf("строка «Итого» не отделена: %v", ft.Totals)
	}
}

// Счёт рынка: значения напечатаны на полстроки ниже наименований.
func TestMarketTableValuesBoundToNames(t *testing.T) {
	ft := TableFromWords(loadWordsTSV(t, "testdata/market-invoice-words.tsv"))
	if ft == nil {
		t.Fatal("таблица не собрана")
	}
	want := map[string]string{
		"Дезинфекция":          "0,02",
		"Вывоз ТКО":            "0,88",
		"Электроэнергия место": "39,11",
		"Стоки":                "1,31",
	}
	for name, v := range want {
		row := findRow(ft.Rows, name)
		if row == nil || !strings.Contains(strings.Join(row, " "), v) {
			t.Fatalf("%s: ожидалось значение %s, строка %v", name, v, row)
		}
	}
}

func TestNormalizeNumberNoGlue(t *testing.T) {
	if got := normalizeNumber("0.24590 1.7277"); got != "" {
		t.Fatalf("два числа склеены в %q", got)
	}
	if got := normalizeNumber("1 234,56"); got != "1234.56" {
		t.Fatalf("разряды: %q", got)
	}
}

func TestColumnNumberingRow(t *testing.T) {
	if !isColumnNumberingText("2 за июль 2023г. 3 4 5 6 7 8") {
		t.Fatal("ряд номеров граф с подмешанным текстом не опознан")
	}
	if isColumnNumberingText("1 Услуга связи 2 3 45,00") {
		t.Fatal("позиция принята за ряд номеров")
	}
}

// Акт пользования имуществом: абзац, выровненный по ширине, не таблица, и
// строка с суммой не пропадает между зонами.
func TestJustifiedActIsNotTable(t *testing.T) {
	words := loadWordsTSV(t, "testdata/act371-words.tsv")
	flat, _ := os.ReadFile("testdata/act371-flat.txt")
	z := BuildZones(words, string(flat))
	if z.OK {
		t.Fatal("в тексте акта найдена таблица")
	}
	if !strings.Contains(z.Head, "5,07") || !strings.Contains(z.Head, "30,42") {
		t.Fatal("строка с арендной платой и НДС пропала из текста для модели")
	}
	if TableFromWords(words) != nil {
		t.Fatal("из абзаца собрана таблица")
	}
}

func TestEngineOrderKeepsJustifiedLine(t *testing.T) {
	// Слова одной строки движка с немонотонными X, как у surya на тексте,
	// выровненном по ширине.
	words := []wordBox{
		{Text: "больница»,", X0: 863, Y0: 893, X1: 1074, Y1: 983},
		{Text: "именуемое", X0: 1095, Y0: 893, X1: 1286, Y1: 983},
		{Text: "в", X0: 865, Y0: 893, X1: 935, Y1: 983},
		{Text: "дальнейшем", X0: 1349, Y0: 893, X1: 1561, Y1: 983},
		{Text: "Арендодатель,", X0: 1582, Y0: 893, X1: 1858, Y1: 983},
		{Text: "в", X0: 1307, Y0: 893, X1: 1328, Y1: 983},
		{Text: "лице", X0: 1006, Y0: 893, X1: 1289, Y1: 983},
	}
	flat := "больница», именуемое в дальнейшем Арендодатель, в лице"
	rows := groupTokenRows(tokensFromWords(AttachEngineOrder(words, flat)))
	if len(rows) != 1 || rowText(rows[0]) != flat {
		t.Fatalf("порядок слов нарушен: %q", rowText(rows[0]))
	}
}

// Счёт рынка: шапка в две строки, пустые ячейки, значения на полстроки ниже
// наименований. Правило «граница ячейки повторяется в соседней строке» здесь
// не применяется — полоса таблицы берётся из раскладки движка, как 9 сентября.
// Было восемь колонок, после первого патча оставалось две.
func TestLayoutKeepsEngineColumnsInsideTable(t *testing.T) {
	words := loadWordsTSV(t, "testdata/market-invoice-words.tsv")
	// Строки движка: у слов одной строки surya одинаковый вертикальный размер.
	line := 0
	for i := range words {
		if i == 0 || words[i].Y0 != words[i-1].Y0 || words[i].Y1 != words[i-1].Y1 {
			line++
		}
		words[i].Line, words[i].Seq = line, (i+1)*100
	}
	engine, err := os.ReadFile("testdata/market-invoice-layout-engine.txt")
	if err != nil {
		t.Fatal(err)
	}
	lay := LayoutFromWords(words, string(engine))
	want := FreeTableFromGrid(string(engine))
	got := FreeTableFromGrid(lay)
	if want == nil || got == nil {
		t.Fatalf("таблица по раскладке не собрана: 9-го %v, сейчас %v", want != nil, got != nil)
	}
	if len(got.Columns) != len(want.Columns) || len(got.Rows) != len(want.Rows) {
		t.Fatalf("колонки таблицы изменились: 9-го %dx%d, сейчас %dx%d",
			len(want.Columns), len(want.Rows), len(got.Columns), len(got.Rows))
	}
	for _, l := range strings.Split(lay, "\n") {
		if strings.Contains(l, "Заказчик к качеству") && strings.Contains(l, "|") {
			t.Fatalf("строка текста вне таблицы разрезана на ячейки: %q", l)
		}
	}
	if !strings.Contains(lay, "Вывоз ТКО место №118 |") {
		t.Fatal("полоса таблицы не из раскладки движка")
	}
}
