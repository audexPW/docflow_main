package recognize

import (
    "strings"
    "testing"

    "docflow/internal/domain"
)

// Negative, structural tests: amounts written in words in different document
// sections must not be silently combined into a new financial total.
// This test does not use Golden91 or any of its expected values.
func TestFinancialRecoveryRejectsUnrelatedSections(t *testing.T) {
    scenarios := []struct {
        name string
        separator string
    }{
        {"separate_pages", "\f"},
        {"distant_sections_without_page_marker", strings.Repeat(" Другие условия договора.", 35)},
    }
    for _, tc := range scenarios {
        t.Run(tc.name, func(t *testing.T) {
            rec := &domain.Recognition{
                Fields: recoveryTestFields("40.72", "8.15", "48.87"),
            }
            ocr := "Приложение к другому акту. Сумма НДС: Один рубль 67 копеек." +
                tc.separator +
                "Основной акт. Без НДС 40,72. НДС 8,15. " +
                "Всего: Сорок восемь белорусских рублей 87 копеек."
            changed := RecoverFinancialTotalsFromEvidence(rec, ocr)
            if changed || rec.Fields["total"].Value != "48.87" ||
                rec.Fields["vat_amount"].Value != "8.15" ||
                rec.Fields["amount_no_vat"].Value != "40.72" {
                t.Fatalf("CROSS_SECTION_OVERRIDE: changed=%v fields=%+v; independent sections must not be mixed", changed, rec.Fields)
            }
        })
    }
}
