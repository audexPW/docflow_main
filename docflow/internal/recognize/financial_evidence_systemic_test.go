package recognize

import (
	"fmt"
	"testing"

	"docflow/internal/domain"
)

// Structural invariants are independent of Golden91: names, row order, and
// values vary. They must not change document-level sums just because an item
// description mentions VAT or a footer-like word.
func TestFinancialEvidenceStructuralInvariants(t *testing.T) {
	t.Run("label_is_whole_cell_not_keyword", func(t *testing.T) {
		cases := []struct {
			label   string
			summary bool
		}{
			{"Итого", true}, {"ВСЕГО:", true}, {"OTOTAL", true},
			{"К оплате", true}, {"Сумма НДС", true}, {"В т.ч. НДС 20%", true},
			{"Консультация по НДС", false}, {"Расчет НДС для клиента", false},
			{"Проверка декларации НДС", false}, {"Итоговая консультация", false},
			{"Услуги: в т.ч. НДС 20%", false}, {"Печать с НДС", false},
		}
		for _, tc := range cases {
			got := recoveryRowIsSummary([]groundedCell{{role: "name", value: tc.label}})
			if got != tc.summary {
				t.Errorf("label %q: summary=%v want %v", tc.label, got, tc.summary)
			}
		}
	})

	t.Run("two_real_items_varying_names_amounts_and_order", func(t *testing.T) {
		cols := []domain.TableColumn{
			{Index: 1, Title: "Наименование", Role: "name"},
			{Index: 2, Title: "Стоимость без НДС", Role: "price"},
			{Index: 3, Title: "Сумма НДС", Role: "vat"},
			{Index: 4, Title: "Всего с НДС", Role: "amount"},
		}
		names := []string{"Консультация по НДС", "Итоговая проверка", "Обучение по НДС", "Подготовка отчетности", "Всего лишь монтаж"}
		for i, name := range names {
			for _, swap := range []bool{false, true} {
				base1, vat1 := int64(10000+151*i), int64(2000+17*i)
				base2, vat2 := int64(5000+113*i), int64(1000+11*i)
				fmtMoney := func(n int64) string { return groundedFormatCents(n) }
				first := []string{"Установка оборудования", fmtMoney(base1), fmtMoney(vat1), fmtMoney(base1 + vat1)}
				second := []string{name, fmtMoney(base2), fmtMoney(vat2), fmtMoney(base2 + vat2)}
				data := [][]string{first, second}
				if swap {
					data[0], data[1] = data[1], data[0]
				}
				correctTotal := fmtMoney(base1 + vat1 + base2 + vat2)
				correctVAT := fmtMoney(vat1 + vat2)
				rec := &domain.Recognition{
					Table:  recoveryTestTable(cols, data),
					Fields: recoveryTestFields(fmtMoney(base1+base2), correctVAT, correctTotal),
				}
				ocr := fmt.Sprintf("%s %s %s %s %s %s %s %s Итого: %s", first[0], first[1], first[2], first[3], second[0], second[1], second[2], second[3], correctTotal)
				if RecoverFinancialTotalsFromEvidence(rec, ocr) {
					t.Errorf("unsafe single-item override for name=%q swap=%v", name, swap)
				}
				if rec.Fields["total"].Value != correctTotal || rec.Fields["vat_amount"].Value != correctVAT {
					t.Errorf("financial fields mutated for name=%q swap=%v: %+v", name, swap, rec.Fields)
				}
			}
		}
	})

	t.Run("second_item_lost_its_name_but_kept_money", func(t *testing.T) {
		cols := []domain.TableColumn{
			{Index: 1, Title: "Наименование", Role: "name"},
			{Index: 2, Title: "Стоимость без НДС", Role: "price"},
			{Index: 3, Title: "Сумма НДС", Role: "vat"},
			{Index: 4, Title: "Всего с НДС", Role: "amount"},
		}
		table := recoveryTestTable(cols, [][]string{
			{"Работы по установке", "100.00", "20.00", "120.00"},
			{"", "50.00", "10.00", "60.00"},
		})
		rec := &domain.Recognition{Table: table, Fields: recoveryTestFields("150.00", "30.00", "180.00")}
		ocr := "Работы по установке 100.00 20.00 120.00 50.00 10.00 60.00 Итого: 180.00"
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatal("missing OCR name made two items look like one")
		}
	})

	t.Run("VAT_annotation_in_product_title_is_not_a_summary", func(t *testing.T) {
		cols := []domain.TableColumn{
			{Index: 1, Title: "Наименование", Role: "name"},
			{Index: 2, Title: "Сумма", Role: "other"},
		}
		table := recoveryTestTable(cols, [][]string{
			{"Услуга установки", "120.00"},
			{"Расчет: В т.ч. НДС 20%", "20.00"},
		})
		rec := &domain.Recognition{Table: table, Fields: recoveryTestFields("150.00", "30.00", "180.00")}
		ocr := "Услуга установки 120.00 Расчет: В т.ч. НДС 20% 20.00 Итого: 180.00"
		if RecoverFinancialTotalsFromEvidence(rec, ocr) || rec.Fields["total"].Value != "180.00" {
			t.Fatalf("VAT-related service name misclassified as tax annotation: %+v", rec.Fields)
		}
	})

	t.Run("fractional_quantity_does_not_allow_unit_price_as_base", func(t *testing.T) {
		cols := []domain.TableColumn{
			{Index: 1, Title: "Наименование", Role: "name"},
			{Index: 2, Title: "Количество", Role: "qty"},
			{Index: 3, Title: "Цена за единицу", Role: "price"},
			{Index: 4, Title: "Налог", Role: "other"},
			{Index: 5, Title: "Всего с НДС", Role: "amount"},
		}
		for _, quantity := range []string{"0.5", "2", "1.5", "2.000", "0,25"} {
			table := recoveryTestTable(cols, [][]string{{"Лицензия", quantity, "100.00", "20.00", "120.00"}})
			rec := &domain.Recognition{Table: table, Fields: recoveryTestFields("", "", "120.00")}
			ocr := "Лицензия " + quantity + " 100.00 20.00 120.00 Итого: 120.00"
			if RecoverFinancialTotalsFromEvidence(rec, ocr) {
				t.Errorf("quantity %s: incorrectly treated unit price as row base", quantity)
			}
		}
	})

	t.Run("single_item_with_actual_footer_can_still_recover", func(t *testing.T) {
		cols := []domain.TableColumn{
			{Index: 1, Title: "Наименование", Role: "name"},
			{Index: 2, Title: "Цена", Role: "price"},
			{Index: 3, Title: "НДС, руб", Role: "vat"},
			{Index: 4, Title: "Всего с НДС", Role: "amount"},
		}
		table := recoveryTestTable(cols, [][]string{
			{"Аренда оборудования", "25.00", "5.00", "30.00"},
			{"Итого", "", "5.00", "30.00"},
		})
		rec := &domain.Recognition{Table: table, Fields: recoveryTestFields("", "", "30.00")}
		if !RecoverFinancialTotalsFromEvidence(rec, "Аренда оборудования 25.00 5.00 30.00 Итого: 30.00") {
			t.Fatal("valid one-item evidence was rejected")
		}
		if rec.Fields["amount_no_vat"].Value != "25.00" || rec.Fields["vat_amount"].Value != "5.00" {
			t.Fatalf("wrong recovered amount: %+v", rec.Fields)
		}
		if RecoverFinancialTotalsFromEvidence(rec, "Аренда оборудования 25.00 5.00 30.00 Итого: 30.00") {
			t.Fatal("recovery should be idempotent")
		}
	})
}
