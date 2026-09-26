package recognize

import (
	"strings"
	"testing"
)

// Боксы, которые surya отдаёт на самом деле: у всех слов строки один и тот же
// прямоугольник, отличающийся только дрожанием в пиксель. Именно из-за него
// наименование в карточке выходило перемешанным.
func TestSplitLineBoxesKeepsWordOrder(t *testing.T) {
	in := []wordBox{
		{Text: "Пеня", X0: 291, Y0: 949, X1: 640, Y1: 966},
		{Text: "по", X0: 292, Y0: 949, X1: 640, Y1: 966},
		{Text: "коммунальным", X0: 292, Y0: 949, X1: 640, Y1: 966},
		{Text: "услугам", X0: 291, Y0: 949, X1: 640, Y1: 966},
		{Text: "с", X0: 292, Y0: 949, X1: 638, Y1: 966},
		{Text: "21.08.", X0: 292, Y0: 949, X1: 638, Y1: 966},
		{Text: "по", X0: 292, Y0: 949, X1: 812, Y1: 966},
	}
	out := SplitLineBoxes(in)
	if len(out) != len(in) {
		t.Fatalf("слов было %d, стало %d", len(in), len(out))
	}
	words := make([]string, 0, len(out))
	for i, w := range out {
		words = append(words, w.Text)
		if i > 0 && w.X0 < out[i-1].X0 {
			t.Errorf("слово %q встало левее предыдущего", w.Text)
		}
	}
	got := strings.Join(words, " ")
	want := "Пеня по коммунальным услугам с 21.08. по"
	if got != want {
		t.Errorf("порядок слов сломан:\n получено %q\n ожидалось %q", got, want)
	}
	if out[len(out)-1].X1 < 700 {
		t.Errorf("строка не растянута до правого края: %v", out[len(out)-1])
	}
}

// Настоящие координаты слов функция не трогает.
func TestSplitLineBoxesLeavesRealBoxes(t *testing.T) {
	in := []wordBox{
		{Text: "Кол-во", X0: 1015, Y0: 882, X1: 1109, Y1: 900},
		{Text: "Цена", X0: 1217, Y0: 866, X1: 1289, Y1: 882},
	}
	out := SplitLineBoxes(in)
	if len(out) != 2 {
		t.Fatalf("ожидалось 2 слова, получено %d", len(out))
	}
	for _, w := range out {
		if w.Text == "Цена" && (w.X0 != 1217 || w.X1 != 1289) {
			t.Errorf("координаты изменились: %+v", w)
		}
	}
}

// Обрывок наименования без чисел приклеивается к позиции ниже, а не остаётся
// отдельной строкой таблицы.
func TestMergeNameFragmentsForward(t *testing.T) {
	ft := freeTableForTest(
		[]string{"Наименование", "Кол-во", "Сумма"},
		[][]string{
			{"Пеня по коммунальным услугам с 21.08. по", "", ""},
			{"25.08.2023", "5", "0,35"},
		},
	)
	MergeNameFragments(ft)
	if len(ft.Rows) != 1 {
		t.Fatalf("ожидалась одна позиция, получено %d: %v", len(ft.Rows), ft.Rows)
	}
	want := "Пеня по коммунальным услугам с 21.08. по 25.08.2023"
	if ft.Rows[0][0] != want {
		t.Errorf("наименование склеено неверно:\n получено %q\n ожидалось %q", ft.Rows[0][0], want)
	}
}
