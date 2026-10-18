package recognize

import (
	"strings"
	"testing"

	"docflow/internal/domain"
)

// Holdout-style structural cases: no Golden identifiers, no Golden expected
// values. Verifies provenance locality, conflict abstention and safe recovery.
func TestFinancialRecoveryWordEvidenceLocality(t *testing.T) {
	t.Run("reject_adjacent_sections_with_page_break", func(t *testing.T) {
		rec := &domain.Recognition{Fields: recoveryTestFields("40.72", "8.15", "48.87")}
		ocr := "Приложение. Сумма НДС: Один рубль 67 копеек.\f" +
			"Основной акт. Без НДС 40,72. НДС 8,15. Всего: Сорок восемь рублей 87 копеек."
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatal("must not join amounts across a page boundary")
		}
		if rec.Fields["vat_amount"].Value != "8.15" {
			t.Fatalf("VAT overwritten from different section: %+v", rec.Fields)
		}
	})
	t.Run("reject_distant_sections_without_page_break", func(t *testing.T) {
		rec := &domain.Recognition{Fields: recoveryTestFields("40.72", "8.15", "48.87")}
		ocr := "Приложение. Сумма НДС: Один рубль 67 копеек." +
			strings.Repeat(" Другие условия договора.", 22) +
			" Основной акт. Всего: Сорок восемь рублей 87 копеек."
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatal("must not join values from separated contract sections")
		}
	})
	t.Run("recover_adjacent_VAT_then_total", func(t *testing.T) {
		rec := &domain.Recognition{Fields: recoveryTestFields("", "", "20.00")}
		ocr := "Товар. Итого 40,72 8,15 48,87. " +
			"Сумма НДС: Восемь рублей 15 копеек. \nВсего: Сорок восемь рублей 87 копеек."
		if !RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatal("must recover from one contiguous labelled footer")
		}
		if rec.Fields["total"].Value != "48.87" || rec.Fields["vat_amount"].Value != "8.15" || rec.Fields["amount_no_vat"].Value != "40.72" {
			t.Fatalf("unexpected recovered values: %+v", rec.Fields)
		}
	})
	t.Run("recover_adjacent_total_then_VAT", func(t *testing.T) {
		rec := &domain.Recognition{Fields: recoveryTestFields("", "", "20.00")}
		ocr := "К оплате: Сорок восемь рублей 87 копеек, в т.ч. НДС: Восемь рублей 15 копеек."
		if !RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatal("must recover from reversed local footer")
		}
		if rec.Fields["total"].Value != "48.87" || rec.Fields["vat_amount"].Value != "8.15" {
			t.Fatalf("unexpected reversed footer values: %+v", rec.Fields)
		}
	})
	t.Run("conflicting_labeled_footers_abstain", func(t *testing.T) {
		rec := &domain.Recognition{Fields: recoveryTestFields("", "", "")}
		ocr := "Акт А: Сумма НДС: Один рубль 67 копеек. Всего: Десять рублей 00 копеек.\f" +
			"Акт Б: Сумма НДС: Два рубля 50 копеек. Всего: Пятнадцать рублей 00 копеек."
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatal("two independent document footers must not produce a single aggregate")
		}
	})
	t.Run("VAT_words_cannot_join_with_unlabeled_total", func(t *testing.T) {
		rec := &domain.Recognition{Fields: recoveryTestFields("10.00", "2.00", "12.00")}
		ocr := "Приложение. Сумма НДС: Один рубль 67 копеек. Отдельный договор на 48.87 руб."
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatal("unlabeled total cannot be paired with VAT words")
		}
	})
}
