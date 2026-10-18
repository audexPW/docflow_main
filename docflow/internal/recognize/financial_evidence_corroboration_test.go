package recognize

import (
	"fmt"
	"strings"
	"testing"

	"docflow/internal/domain"
)

// These tests are constructed from document invariants, without accessing
// Golden dataset IDs or expected-value files.
func recoveryMakeSplitTable(qty, price, base, vat, total string) *domain.Table {
	cols := []domain.TableColumn{
		{Index: 1, Title: "Наименование", Role: "name"},
		{Index: 2, Title: "Количество", Role: "qty"},
		{Index: 3, Title: "Цена", Role: "price"},
		{Index: 4, Title: "руб.коп.", Role: "other"},
		{Index: 5, Title: "%", Role: "vat_rate"},
		{Index: 6, Title: "руб.коп.", Role: "other"},
		{Index: 7, Title: "руб.коп.", Role: "other"},
	}
	return recoveryTestTable(cols, [][]string{
		{"", "", "", "", "", "", total},
		{"№ nn", qty, price, base, "20", vat, ""},
		{"Арендная плата", "", "", "", "", vat, total},
	})
}

func recoveryMakeSummaryTable(base, vat, total string) *domain.Table {
	cols := []domain.TableColumn{
		{Index: 1, Title: "Наименование", Role: "name"},
		{Index: 2, Title: "Цена", Role: "price"},
		{Index: 3, Title: "Сумма б/НДС", Role: "vat"}, // intentionally wrong OCR role
		{Index: 4, Title: "Ставка НДС", Role: "vat_rate"},
		{Index: 5, Title: "руб.коп.", Role: "other"},
		{Index: 6, Title: "руб.коп.", Role: "other"},
	}
	tbl := recoveryTestTable(cols, [][]string{
		{"Услуги хранения", base, base, "20", "", total},
	})
	tbl.TotalsRow = []domain.TableCell{
		{Column: 1, Role: "name", Value: "Услуги хранения"},
		{Column: 4, Role: "vat_rate", Value: "итого:"},
		{Column: 5, Role: "other", Value: vat},
		{Column: 6, Role: "other", Value: total},
	}
	return tbl
}

func TestFinancialRecoveryCorroboratedFooter(t *testing.T) {
	t.Run("damaged OCR prose gap with independently labeled table aggregate", func(t *testing.T) {
		rec := &domain.Recognition{
			Table:  recoveryMakeSummaryTable("200.00", "40.00", "240.00"),
			Fields: recoveryTestFields("165.00", "75.00", "240.00"),
		}
		ocr := "200.00 40.00 240.00 Всего на сумму: Двести сорок рублей 00 копеек " +
			strings.Repeat(" incoherent OCR artifacts ", 14) +
			"в т.ч. НДС: Сорок рублей 00 копеек"
		recoveryCheck(t, rec, ocr, "200.00", "40.00", "240.00")
	})

	t.Run("split OCR table row with quantity-times-price", func(t *testing.T) {
		rec := &domain.Recognition{
			Table:  recoveryMakeSplitTable("2.5", "40.00", "100.00", "20.00", "120.00"),
			Fields: recoveryTestFields("", "20.00", "20.00"),
		}
		ocr := "2.5 40.00 100.00 20.00 120.00 итого: Сто двадцать рублей 00 копеек " +
			"Всего на сумму: в т.ч. НДС: Двадцать рублей 00 копеек"
		recoveryCheck(t, rec, ocr, "100.00", "20.00", "120.00")
	})

	t.Run("unrelated page VAT cannot override even with table proof", func(t *testing.T) {
		rec := &domain.Recognition{
			Table:  recoveryMakeSummaryTable("200.00", "40.00", "240.00"),
			Fields: recoveryTestFields("200.00", "40.00", "240.00"),
		}
		ocr := "200.00 40.00 240.00 Всего на сумму: Двести сорок рублей 00 копеек\f" +
			"Приложение. Сумма НДС: Сорок рублей 00 копеек"
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatalf("unexpected cross-page change: %+v", rec.Fields)
		}
	})

	t.Run("different VAT on a separate section cannot override", func(t *testing.T) {
		rec := &domain.Recognition{
			Table:  recoveryMakeSummaryTable("200.00", "40.00", "240.00"),
			Fields: recoveryTestFields("200.00", "40.00", "240.00"),
		}
		ocr := "200.00 40.00 240.00 Всего на сумму: Двести сорок рублей 00 копеек " +
			strings.Repeat(" unrelated appendix ", 35) +
			"Сумма НДС: Один рубль 67 копеек"
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatalf("unrelated amount must not override: %+v", rec.Fields)
		}
	})

	t.Run("ambiguous VAT written twice abstains", func(t *testing.T) {
		rec := &domain.Recognition{
			Table:  recoveryMakeSummaryTable("200.00", "40.00", "240.00"),
			Fields: recoveryTestFields("165.00", "75.00", "240.00"),
		}
		ocr := "200.00 40.00 240.00 Всего на сумму: Двести сорок рублей 00 копеек " +
			strings.Repeat(" junk ", 25) + "в т.ч. НДС: Сорок рублей 00 копеек " +
			"Сумма НДС: Один рубль 67 копеек"
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatalf("conflicting labeled VAT must abstain: %+v", rec.Fields)
		}
	})

	t.Run("incomplete table without VAT-plus-total pairing abstains", func(t *testing.T) {
		cols := []domain.TableColumn{{Index: 1, Role: "name"}, {Index: 2, Role: "qty"}, {Index: 3, Role: "price"}, {Index: 4, Role: "other"}}
		tbl := recoveryTestTable(cols, [][]string{{"Работы", "2", "100.00", "200.00"}})
		rec := &domain.Recognition{Table: tbl, Fields: recoveryTestFields("", "75.00", "240.00")}
		ocr := "200.00 40.00 240.00 Всего на сумму: Двести сорок рублей 00 копеек " + strings.Repeat(" junk ", 30) + "в т.ч. НДС: Сорок рублей 00 копеек"
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatalf("weak table cannot validate distant footer: %+v", rec.Fields)
		}
	})

	t.Run("vat in product name cannot corroborate", func(t *testing.T) {
		cols := []domain.TableColumn{{Index: 1, Role: "name"}, {Index: 2, Role: "price"}, {Index: 3, Role: "other"}, {Index: 4, Role: "other"}}
		tbl := recoveryTestTable(cols, [][]string{{"Консультация по НДС", "200.00", "40.00", "240.00"}})
		rec := &domain.Recognition{Table: tbl, Fields: recoveryTestFields("", "75.00", "240.00")}
		ocr := "200.00 40.00 240.00 Всего на сумму: Двести сорок рублей 00 копеек " + strings.Repeat(" junk ", 30) + "в т.ч. НДС: Сорок рублей 00 копеек"
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatalf("unlabeled ordinary line cannot act as a summary: %+v", rec.Fields)
		}
	})

	t.Run("positive invariant across different numerical inputs", func(t *testing.T) {
		// Confirmation logic works without matching any historical amount.
		for _, tt := range []struct {
			base, vat, total     string
			totalWords, vatWords string
		}{
			{"50.00", "10.00", "60.00", "Шестьдесят рублей 00 копеек", "Десять рублей 00 копеек"},
			{"150.00", "30.00", "180.00", "Сто восемьдесят рублей 00 копеек", "Тридцать рублей 00 копеек"},
		} {
			t.Run(fmt.Sprintf("total_%s", tt.total), func(t *testing.T) {
				rec := &domain.Recognition{Table: recoveryMakeSummaryTable(tt.base, tt.vat, tt.total), Fields: recoveryTestFields("", "", tt.total)}
				ocr := tt.base + " " + tt.vat + " " + tt.total + " Всего: " + tt.totalWords + " " + strings.Repeat(" gap ", 30) + "в т.ч. НДС: " + tt.vatWords
				recoveryCheck(t, rec, ocr, tt.base, tt.vat, tt.total)
			})
		}
	})
}

func TestFinancialRecoveryRejectsOneSubtotalAmongMultipleItems(t *testing.T) {
	cols := []domain.TableColumn{
		{Index: 1, Title: "Товар", Role: "name"},
		{Index: 2, Title: "Кол-во", Role: "qty"},
		{Index: 3, Title: "Цена", Role: "price"},
		{Index: 4, Title: "Стоимость", Role: "other"},
		{Index: 5, Title: "НДС", Role: "vat"},
		{Index: 6, Title: "Всего с НДС", Role: "amount"},
	}
	tab := recoveryTestTable(cols, [][]string{
		{"Консультация по НДС", "2.5", "40.00", "100.00", "20.00", "120.00"},
		{"Установка", "1", "50.00", "50.00", "10.00", "60.00"},
	})
	rec := &domain.Recognition{Table: tab, Fields: recoveryTestFields("150.00", "30.00", "180.00")}
	ocr := "100.00 20.00 120.00 50.00 10.00 60.00. Итого: Сто двадцать рублей 00 копеек " +
		strings.Repeat(" OCR content ", 30) + "в т.ч. НДС: Двадцать рублей 00 копеек"
	if RecoverFinancialTotalsFromEvidence(rec, ocr) {
		t.Fatalf("SUBTOTAL_OVERRIDE: changed one item into document total: %+v", rec.Fields)
	}
}

func TestFinancialRecoveryDamagedLayoutRegressions(t *testing.T) {
	t.Run("total_caption_embedded_under_tax_rate_heading", func(t *testing.T) {
		tbl := recoveryMakeSummaryTable("587.50", "117.50", "705.00")
		rec := &domain.Recognition{Table: tbl, Fields: recoveryTestFields("533.50", "171.50", "705.00")}
		ocr := "587.50 117.50 705.00 итого: Всего на сумму: Семьсот пять рублей 00 копеек " +
			strings.Repeat(" The state of the state of ", 10) + "в т.ч. НДС: Сто семнадцать рублей 50 копеек"
		recoveryCheck(t, rec, ocr, "587.50", "117.50", "705.00")
	})
	t.Run("item_spread_across_rows_and_empty_total_row", func(t *testing.T) {
		cols := []domain.TableColumn{
			{Index: 1, Role: "name"}, {Index: 2, Role: "unit"}, {Index: 3, Role: "qty"},
			{Index: 4, Role: "price"}, {Index: 5, Role: "other"}, {Index: 6, Role: "vat_rate"},
			{Index: 7, Role: "other"}, {Index: 8, Role: "other"},
		}
		tab := recoveryTestTable(cols, [][]string{
			{"", "", "", "", "", "", "", "439.92"},
			{"№ nn", "", "19.5", "18.80", "366.60", "20", "73.32", ""},
			{"", "Арендная плата за помещение", "", "", "", "", "73.32", "439.92"},
		})
		tab.TotalsRow = []domain.TableCell{{Column: 6, Role: "vat_rate", Value: "итого:"}}
		rec := &domain.Recognition{Table: tab, Fields: recoveryTestFields("", "73.32", "73.32")}
		ocr := "439.92 73.32 366.60 18.80 19.5. итого: Четыреста тридцать девять рублей 92 копейки " +
			"Всего на сумму: в т.ч. НДС: Семьдесят три рубля 32 копейки"
		recoveryCheck(t, rec, ocr, "366.60", "73.32", "439.92")
	})
}
