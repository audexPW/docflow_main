package recognize

import "testing"

func TestFinancialConfidenceMixedVATWithoutTable(t *testing.T) {
    // One position is VAT-exempt; another has positive VAT.
    // The table is missing, so only OCR text remains.
    rec := guardFixture(
        "200.00", "0", "200.00", defaultSources(),
    )

    ocr := "Услуга А: 80.00 Без НДС. " +
        "Услуга Б: 100.00, НДС 20%, " +
        "Сумма НДС: 20.00, с НДС 120.00. " +
        "Итого к оплате: 200.00."

    if !ApplyFinancialConfidenceGuard(rec, ocr) {
        t.Fatal("MIXED_VAT_BYPASS: document-level zero VAT incorrectly trusted")
    }

    if rec.AutoPass || len(rec.Faults) == 0 {
        t.Fatal("mixed VAT must block automatic export")
    }
}
