package recognize

import "testing"

// Positive VAT amount in OCR contradicts a document-level inferred zero VAT.
// Rates, zero amounts and generic 'without VAT' must not create false holds.
func TestFinancialConfidenceMixedVATEdges(t *testing.T) {
	cases := []struct {
		name string
		ocr  string
		want bool
	}{
		{
			name: "positive_including_VAT_with_comma",
			ocr:  "Услуга А Без НДС. Услуга Б, в т.ч. НДС: 20,00. К оплате 200.00.",
			want: true,
		},
		{
			name: "positive_full_including_VAT_label",
			ocr:  "Без НДС; в том числе НДС: 20.00. Всего 200.00.",
			want: true,
		},
		{
			name: "positive_VAT_with_thousands_separator",
			ocr:  "Без НДС. Сумма НДС: 1 234,56. К оплате 200.00.",
			want: true,
		},
		{
			name: "positive_VAT_followed_by_comma",
			ocr:  "Без НДС. Сумма НДС: 20.00, включая услуги. Итого 200.00.",
			want: true,
		},
		{
			name: "explicit_numeric_zero",
			ocr:  "Без НДС. Сумма НДС: 0,00. Итого 200.00.",
			want: false,
		},
		{
			name: "percentage_is_not_money",
			ocr:  "Без НДС. Сумма НДС: 20%. Итого 200.00.",
			want: false,
		},
		{
			name: "decimal_percentage_is_not_money",
			ocr:  "Без НДС. Сумма НДС: 20,00%. Итого 200.00.",
			want: false,
		},
		{
			name: "fully_exempt_document",
			ocr:  "Услуги 200.00. Все услуги без НДС.",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := guardFixture("200.00", "0", "200.00", defaultSources())
			guarded := ApplyFinancialConfidenceGuard(rec, tc.ocr)
			if guarded != tc.want {
				t.Fatalf("guarded=%t, want=%t", guarded, tc.want)
			}
			if guarded && (rec.AutoPass || len(rec.Faults) == 0) {
				t.Fatalf("automatic export remains possible: auto=%t faults=%v", rec.AutoPass, rec.Faults)
			}
			if !guarded && (!rec.AutoPass || len(rec.Faults) > 0) {
				t.Fatalf("false alert: auto=%t faults=%v", rec.AutoPass, rec.Faults)
			}
		})
	}
}
