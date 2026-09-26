package recognize

import (
	"strings"
	"testing"
)

// Перекос впечатанных данных.
//
// Лист ровный, а данные съехали: наклон по всей странице ноль, в табличной зоне
// 0.024-0.031. Строка уходит вниз на две-три высоты слова, пока идёт слева
// направо, и сборка строк по перекрытию боксов рвёт её на куски: значения
// позиции разъезжаются, а в саму позицию встают обломки шапки.
//
// Тест проверяемый: без поправки эта позиция собирается как
// « | | ШТ | 1,000 | | BYN | НДС, % | BYN | » — то есть без единой суммы.
func TestSkewedPositionKeepsItsAmounts(t *testing.T) {
	ft := TableFromWords(loadWordsTSV(t, "testdata/skew-126-words.tsv"))
	if ft == nil {
		t.Fatal("таблица не собрана")
	}
	if len(ft.Rows) == 0 {
		t.Fatal("позиций нет — значения ушли в шапку")
	}
	joined := strings.Join(ft.Rows[0], " | ")
	// цена, стоимость, НДС и всего с НДС — на документе это одна позиция
	for _, want := range []string{"8,33", "1,67", "10,00"} {
		if !strings.Contains(joined, want) {
			t.Errorf("в позиции нет суммы %s (строка: %q) — значения разъехались по соседним строкам", want, joined)
		}
	}
	// в графы значений не должны попадать обломки подписей граф
	for _, junk := range []string{"BYN", "НДС, %"} {
		if strings.Contains(joined, junk) {
			t.Errorf("в позицию затесался обломок шапки %q: %q", junk, joined)
		}
	}
}

// Строка «Итого», не опознанная по слову.
//
// Распознавание отдаёт «ОТОТИ» вместо «ИТОГО», строка итога остаётся в позициях,
// и сумма позиций становится вдвое больше настоящей: 95,48 вместо 47,74.
// Внешне таблица выглядит разобранной — в 1С уходит удвоенный документ.
func TestGarbledTotalsRowGoesToTotals(t *testing.T) {
	ft := TableFromWords(loadWordsTSV(t, "testdata/market-invoice-words.tsv"))
	if ft == nil {
		t.Fatal("таблица не собрана")
	}
	if len(ft.Totals) == 0 {
		t.Fatal("строка итога не выделена — останется в позициях и удвоит сумму")
	}
	if !strings.Contains(strings.Join(ft.Totals, " "), "47,74") {
		t.Errorf("в итоге нет суммы документа 47,74: %q", strings.Join(ft.Totals, " "))
	}
	for i, r := range ft.Rows {
		if strings.Contains(strings.Join(r, " "), "47,74") {
			t.Errorf("строка %d осталась в позициях с итогом документа: %q", i, strings.Join(r, " | "))
		}
	}
}
