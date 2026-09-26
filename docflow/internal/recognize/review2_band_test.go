package recognize

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
)

// engineLines расставляет словам номера строк движка так же, как это делает
// surya: у слов одной строки одинаковый вертикальный размер.
func engineLines(words []wordBox) []wordBox {
	out := append([]wordBox{}, words...)
	sort.SliceStable(out, func(i, j int) bool {
		if math.Abs(out[i].Y0-out[j].Y0) > 1 || math.Abs(out[i].Y1-out[j].Y1) > 1 {
			return out[i].Y0 < out[j].Y0
		}
		return out[i].X0 < out[j].X0
	})
	line := 0
	for i := range out {
		if i == 0 || math.Abs(out[i].Y0-out[i-1].Y0) > 1 || math.Abs(out[i].Y1-out[i-1].Y1) > 1 {
			line++
		}
		out[i].Line, out[i].Seq = line, (i+1)*100
	}
	return out
}

// Счёт ЖКХ по общежитию, как его описал проверяющий: текст договора сверху,
// строка с УНП прямо над таблицей, подписи граф в две строки, скан под углом
// (колонки гуляют по горизонтали), «Итого», ниже — сумма прописью.
func jkhLikeInvoice() []wordBox {
	var w []wordBox
	y := 100.0
	add := func(texts []string, xs []float64) {
		for i, t := range texts {
			w = append(w, wordBox{Text: t, X0: xs[i], Y0: y, X1: xs[i] + float64(len([]rune(t)))*12, Y1: y + 30})
		}
		y += 55
	}
	add([]string{"Исполнитель", "оказал,", "а", "Заказчик", "принял", "услуги", "по", "договору", "№", "33-тм", "от", "22.06.20"},
		[]float64{100, 250, 380, 420, 570, 700, 820, 870, 1010, 1050, 1160, 1210})
	add([]string{"УНП", "101452539", "Адрес", "220002,", "г.Минск,", "ул.Сторожевская,", "8"},
		[]float64{100, 180, 400, 500, 650, 800, 1100})
	add([]string{"Номер", "Наименование", "работ", "(услуг)", "Единица", "Количество", "Ставка"},
		[]float64{100, 250, 500, 620, 900, 1100, 1400})
	add([]string{"п/п", "измерения", "НДС", "Сумма", "Сумма НДС", "Всего с НДС"},
		[]float64{100, 900, 1400, 1600, 1800, 2000})
	skew := 0.0
	for i, name := range []string{"Дезинфекция", "Вывоз ТКО", "Уборка территории", "Электроэнергия"} {
		add([]string{fmt.Sprint(i + 1), name, "М2", "1.5", "20", "0,02", "0,00", "0,02"},
			[]float64{100, 250, 900 + skew, 1100 + skew, 1400 + skew, 1600 + skew, 1800 + skew, 2000 + skew})
		skew += 9
	}
	add([]string{"ИТОГО", "20", "39,78", "7,96", "47,74"}, []float64{600, 1400, 1600, 1800, 2000})
	add([]string{"Всего", "Сорок", "семь", "белорусских", "рублей", "74", "копейки"},
		[]float64{100, 250, 400, 520, 800, 950, 1020})
	return w
}

func TestTableFoundWithoutCleanHeader(t *testing.T) {
	words := engineLines(jkhLikeInvoice())
	rows := groupTokenRows(tokensFromWords(words))
	head, first, last := findTableBandAny(rows)
	if head < 0 {
		t.Fatal("таблица не найдена: по строкам с числами её видно и без чистой шапки")
	}
	if !strings.Contains(rowText(rows[head]), "Наименование") {
		t.Fatalf("полоса начинается не с подписей граф: %q", rowText(rows[head]))
	}
	if strings.Contains(rowText(rows[head]), "УНП") {
		t.Fatal("строка с УНП попала в шапку таблицы")
	}
	if !strings.HasPrefix(strings.TrimSpace(rowText(rows[last])), "ИТОГО") {
		t.Fatalf("полоса кончается не строкой «Итого»: %q", rowText(rows[last]))
	}
	if first != head+2 {
		t.Fatalf("подписи граф в две строки не учтены: head=%d first=%d", head, first)
	}
}

func TestLayoutCutsTableRowsWithoutHeader(t *testing.T) {
	words := engineLines(jkhLikeInvoice())
	lay := LayoutFromWords(words, "")
	lines := strings.Split(strings.TrimRight(lay, "\n"), "\n")
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "Исполнитель") || strings.HasPrefix(l, "УНП") || strings.HasPrefix(l, "Всего Сорок"):
			if strings.Contains(l, "|") {
				t.Fatalf("текст вне таблицы разрезан на ячейки: %q", l)
			}
		case strings.Contains(l, "Дезинфекция") || strings.HasPrefix(l, "ИТОГО"):
			if strings.Count(l, "|") < 3 {
				t.Fatalf("ряд таблицы не разрезан на ячейки: %q", l)
			}
		}
	}
	if ft := FreeTableFromGrid(lay); ft == nil || len(ft.Rows) < 4 {
		t.Fatalf("таблица по раскладке не собралась: %v", ft)
	}
}
