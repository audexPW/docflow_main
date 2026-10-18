package recognize

import (
    "testing"

    "docflow/internal/domain"
)

// Independent, synthetic counterexamples. No Golden91 document ID or fixture.
// A product/service description containing the word VAT does not make its
// row a document-level summary. Recovery must not replace a correctly
// recognized document total with the first line's amount.
func TestFinancialRecoveryRejectsMultilineVATNamedProduct(t *testing.T) {
    cols := []domain.TableColumn{
        {Index: 1, Title: "Наименование", Role: "name"},
        {Index: 2, Title: "Стоимость без НДС", Role: "price"},
        {Index: 3, Title: "Сумма НДС", Role: "vat"},
        {Index: 4, Title: "Всего с НДС", Role: "amount"},
    }
    cases := []struct {
        name, secondName string
        first, firstVAT, firstTotal string
        second, secondVAT, secondTotal string
        documentBase, documentVAT, documentTotal string
    }{
        {
            name: "service includes VAT in title", secondName: "Консультация по НДС",
            first: "100.00", firstVAT: "20.00", firstTotal: "120.00",
            second: "50.00", secondVAT: "10.00", secondTotal: "60.00",
            documentBase: "150.00", documentVAT: "30.00", documentTotal: "180.00",
        },
        {
            name: "different amounts and VAT title", secondName: "Проверка декларации НДС",
            first: "42.50", firstVAT: "8.50", firstTotal: "51.00",
            second: "15.00", secondVAT: "3.00", secondTotal: "18.00",
            documentBase: "57.50", documentVAT: "11.50", documentTotal: "69.00",
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            table := recoveryTestTable(cols, [][]string{
                {"Монтаж оборудования", tc.first, tc.firstVAT, tc.firstTotal},
                {tc.secondName, tc.second, tc.secondVAT, tc.secondTotal},
            })
            rec := &domain.Recognition{
                Table: table,
                Fields: recoveryTestFields(tc.documentBase, tc.documentVAT, tc.documentTotal),
            }
            ocr := "Монтаж оборудования " + tc.first + " " + tc.firstVAT + " " + tc.firstTotal +
                " " + tc.secondName + " " + tc.second + " " + tc.secondVAT + " " + tc.secondTotal +
                " Итого: " + tc.documentTotal
            applied := RecoverFinancialTotalsFromEvidence(rec, ocr)
            if applied || rec.Fields["total"].Value != tc.documentTotal ||
                rec.Fields["vat_amount"].Value != tc.documentVAT {
                t.Fatalf("UNSAFE_OVERRIDE: applied=%v got total=%s vat=%s, correct document total=%s vat=%s",
                    applied, rec.Fields["total"].Value, rec.Fields["vat_amount"].Value,
                    tc.documentTotal, tc.documentVAT)
            }
        })
    }
}
